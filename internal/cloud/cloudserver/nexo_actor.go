package cloudserver

// Nexo IA · NX-01 — resolución del autor de un cambio.
//
// La identidad sale SOLO del token asignado (RF-02, RN-A1): el principal que
// authenticateRequest dejó en el contexto. Lo que declare la PC (created_by)
// se guarda solo como referencia en ClientClaim y nunca se usa como autor.

import (
	"context"
	"strings"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

// nexoResolveActor decide el autor de la petición actual.
//
//	Principal human/service_account con token asignado → nombre del dueño, IDs, verificado.
//	Principal legacy (token compartido)                 → "no-identificado", sin IDs.
//	Sin principal (servidor sin autenticación)          → "no-identificado", sin IDs.
func nexoResolveActor(ctx context.Context, clientClaim string) cloudstore.MutationActor {
	actor := cloudstore.MutationActor{
		Name:        cloudstore.NexoActorUnidentified,
		ClientClaim: cloudstore.NexoTruncateClaim(clientClaim),
	}
	principal, ok := PrincipalFromContext(ctx)
	if !ok || !nexoIsAssignedTokenPrincipal(principal) {
		return actor
	}
	name := strings.TrimSpace(principal.DisplayName)
	if name == "" {
		name = strings.TrimSpace(principal.ID)
	}
	actor.Name = name
	actor.PrincipalID = strings.TrimSpace(principal.ID)
	actor.TokenID = strings.TrimSpace(principal.TokenID)
	actor.Verified = true
	return actor
}

// nexoIsAssignedTokenPrincipal es true solo para usuarios administrados
// (persona o cuenta de servicio) autenticados con su propio token.
func nexoIsAssignedTokenPrincipal(principal cloudauth.Principal) bool {
	if strings.TrimSpace(principal.ID) == "" {
		return false
	}
	if principal.Kind != cloudauth.PrincipalKindHuman && principal.Kind != cloudauth.PrincipalKindServiceAccount {
		return false
	}
	return principal.Source == cloudauth.PrincipalSourceManagedToken
}
