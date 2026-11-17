package port

import (
	"context"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// FlowRepository lee lo que usan las métricas de flujo (issue 036).
type FlowRepository interface {
	// ListFlowIssues devuelve los issues del proyecto con sus etiquetas y su
	// historial (en orden created_at, seq). Con labelIDs, solo los issues con
	// alguna de ellas (ya validadas: son del proyecto).
	ListFlowIssues(ctx context.Context, projectID string, labelIDs []string) ([]model.FlowIssue, error)
}
