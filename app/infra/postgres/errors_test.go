package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

func TestTranslateError(t *testing.T) {
	if translateError(nil, "project", "get", nil) != nil {
		t.Error("nil must stay nil")
	}
	if err := translateError(gorm.ErrRecordNotFound, "project", "get", nil); !domainerrors.IsNotFound(err) {
		t.Errorf("record not found: got %v", err)
	}
	unique := &pgconn.PgError{Code: "23505", ConstraintName: "projects_owner_uid_key_key"}
	if err := translateError(unique, "project", "create", nil); !domainerrors.IsConflict(err) {
		t.Errorf("unique violation: got %v", err)
	}
	if err := translateError(errors.New("boom"), "project", "list", nil); !domainerrors.IsInternal(err) {
		t.Errorf("generic error: got %v", err)
	}
}
