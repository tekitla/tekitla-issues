package port

import (
	"context"
	"time"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// ShareLinkRepository define la persistencia de los enlaces de solo lectura
// (issue 009). Guarda solo el SHA-256 del token; el hash nunca sale del repo.
//
// La pertenencia del proyecto a los espacios del usuario la verifica el
// service antes de llamar a Create, List, Get y Revoke.
//
// Contrato de errores: no existe (o es de otro proyecto) -> NotFoundError;
// tope de enlaces activos -> FailedPreconditionError (reason
// SHARE_LINK_LIMIT); hash repetido -> ConflictError; resto -> InternalError.
type ShareLinkRepository interface {
	// Create guarda el enlace (todo seteado y ya validado) con su hash, con
	// el lock del proyecto tomado: si el proyecto ya tiene maxActive enlaces
	// activos en link.CreatedAt, FailedPrecondition (reason SHARE_LINK_LIMIT).
	Create(ctx context.Context, link *model.ShareLink, hash []byte, maxActive int) (*model.ShareLink, error)
	// List devuelve los enlaces del proyecto, los más nuevos primero (también
	// los revocados y los vencidos).
	List(ctx context.Context, projectID string) ([]*model.ShareLink, error)
	// Revoke revoca el enlace del proyecto en at. Uno ya revocado no cambia.
	// Devuelve el enlace como queda.
	Revoke(ctx context.Context, projectID, id string, at time.Time) (*model.ShareLink, error)
	// Resolve busca el enlace activo en now con ese hash y lo devuelve.
	// NotFound en cualquier otro caso, sin decir cuál. Marca last_used_at
	// (como mucho una vez por minuto) a mejor esfuerzo: si esa escritura
	// falla, el enlace se devuelve igual.
	Resolve(ctx context.Context, hash []byte, now time.Time) (*model.ShareLink, error)
}
