package handler

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// MetadataUserID es la metadata con el uid que propaga el api-gateway después
// de autenticar el token (también manda x-roles y x-request-id; los roles no
// se usan en la rebanada 1). El servicio es interno (sin puerto publicado):
// confía en el gateway.
const MetadataUserID = "x-user-id"

// MetadataAgentID es el id del agente (issue 025, ADR 0009) que propaga el
// gateway cuando el request llega con un token de agente; x-user-id es
// entonces la persona dueña. El gateway borra el que mande el cliente.
const MetadataAgentID = "x-agent-id"

// requireUserID devuelve el uid del usuario autenticado (metadata x-user-id).
// Sin metadata o con valor vacío -> UnauthorizedError (UNAUTHENTICATED).
//
// La identidad se resuelve acá, en el adapter, y se pasa como parámetro
// explícito a la capa de aplicación: el dominio no lee el context.
func requireUserID(ctx context.Context) (string, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		for _, v := range md.Get(MetadataUserID) {
			if uid := strings.TrimSpace(v); uid != "" {
				return uid, nil
			}
		}
	}
	return "", domainerrors.UnauthorizedError("missing user identity", map[string]any{"metadata": MetadataUserID})
}

// requireAuthor es requireUserID más el agente (x-agent-id, opcional). Un
// x-agent-id que no es un UUID no lo pudo poner el gateway: UNAUTHENTICATED.
func requireAuthor(ctx context.Context) (model.Author, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return model.Author{}, err
	}
	agentID := agentIDFrom(ctx)
	if agentID != "" {
		parsed, err := uuid.Parse(agentID)
		if err != nil {
			return model.Author{}, domainerrors.UnauthorizedError("invalid agent identity", map[string]any{"metadata": MetadataAgentID})
		}
		agentID = parsed.String()
	}
	return model.Author{UserID: uid, AgentID: agentID}, nil
}

// agentIDFrom devuelve el x-agent-id de la metadata ("" si no viene).
func agentIDFrom(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		for _, v := range md.Get(MetadataAgentID) {
			if id := strings.TrimSpace(v); id != "" {
				return id
			}
		}
	}
	return ""
}
