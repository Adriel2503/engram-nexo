package cloudstore

// Nexo IA · NX-01 — autoría por cambio.
//
// El actor resuelto por el servidor (a partir del token asignado) viaja en el
// contexto de la petición para no cambiar las firmas de WriteChunk ni de
// InsertMutationBatch, que usan interfaces y fakes de los tests de Engram.
//
// Dentro de la transacción de escritura, nexoApplyMutationActor publica el
// actor como variables locales de la transacción (set_config(..., true)) y el
// trigger nexo_stamp_actor (ver nexo_migrations.go) lo copia en cada fila de
// cloud_mutations y cloud_chunks. El email se copia en el mismo INSERT con una
// subconsulta a cloud_human_users. Así las sentencias INSERT de Engram no
// cambian.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// NexoActorUnidentified es el autor que se registra cuando la petición no usa
// un token asignado a una persona o cuenta de servicio (RF-04).
const NexoActorUnidentified = "no-identificado"

// nexoMaxClientClaimRunes limita lo que declara la PC (solo referencia).
const nexoMaxClientClaimRunes = 128

// MutationActor describe quién hizo un cambio. La identidad sale solo del token
// asignado; ClientClaim es lo que declaró la PC y nunca se usa como autor.
// El email no viaja aquí: se copia al insertar con una subconsulta a
// cloud_human_users.
type MutationActor struct {
	Name        string // actor (copia del nombre mostrado)
	PrincipalID string // actor_principal_id (cloud_principals.id como texto)
	TokenID     string // actor_token_id (cloud_principal_tokens.id como texto)
	Verified    bool   // actor_verified
	ClientClaim string // actor_client_claim (solo referencia)
}

type nexoMutationActorKey struct{}

// WithMutationActor deja el actor en el contexto para que la capa de
// almacenamiento lo escriba junto a cada cambio.
func WithMutationActor(ctx context.Context, actor MutationActor) context.Context {
	return context.WithValue(ctx, nexoMutationActorKey{}, actor)
}

// MutationActorFromContext devuelve el actor guardado con WithMutationActor.
func MutationActorFromContext(ctx context.Context) (MutationActor, bool) {
	if ctx == nil {
		return MutationActor{}, false
	}
	actor, ok := ctx.Value(nexoMutationActorKey{}).(MutationActor)
	return actor, ok
}

// NexoTruncateClaim recorta lo declarado por el cliente a un largo acotado.
// NX-07: la declaración viene de afuera; se quitan credenciales antes de recortar.
func NexoTruncateClaim(claim string) string {
	return security.NexoRedactExternal(claim, nexoMaxClientClaimRunes)
}

func nexoTruncateRunes(value string, max int) string {
	if max <= 0 || utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max])
}

// normalized garantiza que un actor sin identidad verificada quede como
// "no-identificado" y sin referencias (RF-04).
func (a MutationActor) normalized() MutationActor {
	a.Name = security.NexoRedactExternal(a.Name, 0) // NX-07: nombres históricos con credenciales
	a.PrincipalID = strings.TrimSpace(a.PrincipalID)
	a.TokenID = strings.TrimSpace(a.TokenID)
	a.ClientClaim = NexoTruncateClaim(a.ClientClaim)
	if !a.Verified {
		a.Name = NexoActorUnidentified
		a.PrincipalID = ""
		a.TokenID = ""
	}
	if a.Name == "" {
		a.Name = NexoActorUnidentified
	}
	return a
}

// nexoApplyMutationActor publica el actor del contexto como variables locales
// de la transacción. Sin actor en el contexto no hace nada (las filas quedan
// sin autor, igual que los datos anteriores a NX-01).
func nexoApplyMutationActor(ctx context.Context, tx *sql.Tx) error {
	actor, ok := MutationActorFromContext(ctx)
	if !ok || tx == nil {
		return nil
	}
	actor = actor.normalized()
	verified := "false"
	if actor.Verified {
		verified = "true"
	}
	if _, err := tx.ExecContext(ctx, `SELECT
			set_config('nexo.actor', $1, true),
			set_config('nexo.actor_principal_id', $2, true),
			set_config('nexo.actor_token_id', $3, true),
			set_config('nexo.actor_verified', $4, true),
			set_config('nexo.actor_client_claim', $5, true)`,
		actor.Name, actor.PrincipalID, actor.TokenID, verified, actor.ClientClaim,
	); err != nil {
		return fmt.Errorf("cloudstore: nexo apply mutation actor: %w", err)
	}
	return nil
}
