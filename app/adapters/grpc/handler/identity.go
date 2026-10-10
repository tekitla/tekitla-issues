package handler

import (
	"context"
	"strings"

	"google.golang.org/grpc/metadata"

	domainerrors "github.com/vrojas1015/tequio-issues/app/domain/errors"
)

// MetadataUserID es la metadata con el uid que propaga el api-gateway después
// de autenticar el token (también manda x-roles y x-request-id; los roles no
// se usan en la rebanada 1). El servicio es interno (sin puerto publicado):
// confía en el gateway.
const MetadataUserID = "x-user-id"

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
