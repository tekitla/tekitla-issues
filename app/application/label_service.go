package application

import (
	"context"

	"github.com/google/uuid"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// LabelService implementa los casos de uso de las etiquetas de un proyecto.
type LabelService struct {
	guard  projectGuard
	labels port.LabelRepository
}

// NewLabelService construye el servicio.
func NewLabelService(workspaceRepo port.WorkspaceRepository, projects port.ProjectRepository, labels port.LabelRepository) *LabelService {
	return &LabelService{
		guard:  projectGuard{workspaces: workspaces{repo: workspaceRepo}, projects: projects},
		labels: labels,
	}
}

// ListLabels devuelve las etiquetas del proyecto por nombre.
func (s *LabelService) ListLabels(ctx context.Context, ownerUID, projectID string) ([]*model.Label, error) {
	if _, err := s.guard.ensure(ctx, ownerUID, projectID); err != nil {
		return nil, err
	}
	return s.labels.List(ctx, projectID)
}

// CreateLabel crea una etiqueta. Color sin especificar se guarda como Neutral.
func (s *LabelService) CreateLabel(ctx context.Context, ownerUID, projectID, name string, color model.LabelColor) (*model.Label, error) {
	if _, err := s.guard.ensure(ctx, ownerUID, projectID); err != nil {
		return nil, err
	}
	n, err := model.NormalizeLabelName(name)
	if err != nil {
		return nil, err
	}
	c, err := normalizeLabelColor(color)
	if err != nil {
		return nil, err
	}
	return s.labels.Create(ctx, &model.Label{ID: uuid.NewString(), ProjectID: projectID, Name: n, Color: c})
}

// UpdateLabel cambia nombre y/o color (nil = no cambia). Sin cambios pedidos
// devuelve la etiqueta tal cual.
func (s *LabelService) UpdateLabel(ctx context.Context, ownerUID, projectID, labelID string, name *string, color *model.LabelColor) (*model.Label, error) {
	if _, err := s.guard.ensure(ctx, ownerUID, projectID); err != nil {
		return nil, err
	}
	if err := validateUUID("label_id", labelID); err != nil {
		return nil, err
	}
	var n *string
	if name != nil {
		v, err := model.NormalizeLabelName(*name)
		if err != nil {
			return nil, err
		}
		n = &v
	}
	var c *model.LabelColor
	if color != nil {
		v, err := normalizeLabelColor(*color)
		if err != nil {
			return nil, err
		}
		c = &v
	}
	return s.labels.Update(ctx, projectID, labelID, n, c)
}

// DeleteLabel borra la etiqueta y la quita de todos los issues.
func (s *LabelService) DeleteLabel(ctx context.Context, ownerUID, projectID, labelID string) error {
	if _, err := s.guard.ensure(ctx, ownerUID, projectID); err != nil {
		return err
	}
	if err := validateUUID("label_id", labelID); err != nil {
		return err
	}
	return s.labels.Delete(ctx, projectID, labelID)
}

func normalizeLabelColor(c model.LabelColor) (model.LabelColor, error) {
	if c == model.LabelColorUnspecified {
		return model.LabelColorNeutral, nil
	}
	if !c.IsValid() {
		return 0, domainerrors.ValidationError("invalid color", map[string]any{"field": "color", "value": int(c)})
	}
	return c, nil
}
