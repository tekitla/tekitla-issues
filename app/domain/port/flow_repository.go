package port

import (
	"context"
	"time"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// FlowRepository lee lo que usan las métricas de flujo (issue 036).
type FlowRepository interface {
	// ListFlowIssues devuelve los issues del proyecto con sus etiquetas y su
	// historial (en orden created_at, seq). Con labelIDs, solo los issues con
	// alguna de ellas (ya validadas: son del proyecto).
	ListFlowIssues(ctx context.Context, projectID string, labelIDs []string) ([]model.FlowIssue, error)
	// ListSessionTracks devuelve la historia (antes de to) de las sesiones
	// que alguna vez se ligaron al proyecto (con number > 0, a ese issue),
	// de cualquier persona, con todas sus transiciones en orden (at, seq):
	// también las ligadas a otros issues, que cierran los tramos.
	ListSessionTracks(ctx context.Context, projectID string, number int32, to time.Time) ([]model.SessionTrack, error)
}
