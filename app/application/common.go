package application

import (
	"strings"

	"github.com/google/uuid"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

// Paginación: default y máximo de limit para todos los listados.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 100
)

// normalizeLimit aplica el default (limit <= 0) y el máximo.
func normalizeLimit(limit int32) int32 {
	switch {
	case limit <= 0:
		return DefaultPageLimit
	case limit > MaxPageLimit:
		return MaxPageLimit
	}
	return limit
}

// requireOwner valida que haya un usuario. El adapter gRPC ya lo exige; esto
// es la red de seguridad para cualquier otro llamador.
func requireOwner(ownerUID string) error {
	if strings.TrimSpace(ownerUID) == "" {
		return domainerrors.UnauthorizedError("user is not authenticated", nil)
	}
	return nil
}

// validateUUID valida un identificador UUID de entrada.
func validateUUID(field, id string) error {
	if strings.TrimSpace(id) == "" {
		return domainerrors.ValidationError(field+" is required", map[string]any{"field": field})
	}
	if _, err := uuid.Parse(id); err != nil {
		return domainerrors.ValidationError(field+" must be a UUID", map[string]any{"field": field, "value": id})
	}
	return nil
}

// validateIssueNumber valida el número de issue (desde 1).
func validateIssueNumber(number int32) error {
	if number <= 0 {
		return domainerrors.ValidationError("number must be positive", map[string]any{"field": "number", "value": number})
	}
	return nil
}
