package port

import (
	"context"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// StatusRepository define la persistencia de Status (las columnas de cada
// proyecto). Las escrituras toman el lock de la fila del proyecto: el máximo
// por proyecto y «el último de su categoría» se verifican sin carreras.
//
// Contrato de errores:
//   - estado de otro proyecto o inexistente -> NotFoundError
//   - after_status_id / move_to_status_id que no es del proyecto -> ValidationError
//   - nombre repetido                       -> ConflictError (reason STATUS_NAME_TAKEN)
//   - máximo, último de su categoría, o con issues sin destino -> FailedPreconditionError
//   - cualquier otra cosa                   -> InternalError
type StatusRepository interface {
	// List devuelve los estados del proyecto por rank.
	List(ctx context.Context, projectID string) ([]*model.Status, error)
	// Get devuelve un estado del proyecto.
	Get(ctx context.Context, projectID, id string) (*model.Status, error)
	// Create inserta el estado (ID, ProjectID, Name y Category seteados)
	// después de afterID ("" = al final).
	Create(ctx context.Context, status *model.Status, afterID string) (*model.Status, error)
	// Update aplica el patch (ya validado). Color sin especificar se guarda
	// como el de la categoría del estado.
	Update(ctx context.Context, projectID, id string, patch StatusPatch) (*model.Status, error)
	// Delete borra el estado y pasa sus issues a moveToID ("" solo si no tiene
	// issues), registrando cada cambio a nombre de actorUID.
	Delete(ctx context.Context, projectID, id, moveToID, actorUID string) error
	// BatchUpdate guarda la lista completa de estados en una transacción, con
	// el lock del proyecto: valida con model.PlanStatusBatch contra los
	// estados actuales, borra (moviendo los issues, con su evento), actualiza
	// y crea. FailedPrecondition (reason STATUS_HAS_ISSUES) si un estado
	// borrado tiene issues y no tiene destino. Devuelve los estados por rank.
	BatchUpdate(ctx context.Context, projectID string, specs []model.StatusSpec, deletions []model.StatusDeletion, actorUID string) ([]*model.Status, error)
}

// StatusPatch son los cambios de UpdateStatus: nil = no cambia. AfterID
// presente y vacío = al principio.
type StatusPatch struct {
	Name     *string
	AfterID  *string
	Color    *model.LabelColor
	WIPLimit *int32
	Hidden   *bool
}

// IsEmpty informa si el patch no pide ningún cambio.
func (p StatusPatch) IsEmpty() bool {
	return p.Name == nil && p.AfterID == nil && p.Color == nil && p.WIPLimit == nil && p.Hidden == nil
}

// LabelRepository define la persistencia de Label.
//
// Contrato de errores:
//   - etiqueta de otro proyecto o inexistente -> NotFoundError
//   - nombre repetido                         -> ConflictError (reason LABEL_NAME_TAKEN)
//   - máximo por proyecto                     -> FailedPreconditionError
//   - cualquier otra cosa                     -> InternalError
type LabelRepository interface {
	// List devuelve las etiquetas del proyecto por nombre.
	List(ctx context.Context, projectID string) ([]*model.Label, error)
	// Create inserta la etiqueta (ID, ProjectID, Name y Color seteados).
	Create(ctx context.Context, label *model.Label) (*model.Label, error)
	// Update cambia nombre y/o color (nil = no cambia).
	Update(ctx context.Context, projectID, id string, name *string, color *model.LabelColor) (*model.Label, error)
	// Delete borra la etiqueta y la quita de todos los issues.
	Delete(ctx context.Context, projectID, id string) error
}
