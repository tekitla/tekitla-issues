package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

// SQLSTATE de unique_violation y foreign_key_violation.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
)

// translateError traduce un error de GORM/pgx al catálogo del dominio:
//
//	gorm.ErrRecordNotFound -> NotFoundError
//	PG 23505               -> ConflictError
//	PG 23503               -> FailedPreconditionError (p. ej. un estado que se
//	                          borró mientras se movía un issue a él)
//	resto                  -> InternalError (con la causa en details)
//
// entity se usa en los mensajes ("project not found"); details se adjunta tal cual.
func translateError(err error, entity, op string, details map[string]any) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainerrors.NotFoundError(entity+" not found", details)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		d := cloneDetails(details)
		d["constraint"] = pgErr.ConstraintName
		return domainerrors.ConflictError(entity+" already exists", d)
	}
	if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
		d := cloneDetails(details)
		d["constraint"] = pgErr.ConstraintName
		return domainerrors.FailedPreconditionError(entity+" references a row that no longer exists", d)
	}
	d := cloneDetails(details)
	d["cause"] = err.Error()
	return domainerrors.InternalError("failed to "+op+" "+entity, d)
}

func cloneDetails(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}
