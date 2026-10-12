package cloudstore

// Nexo IA · NX-02 — registro de conexiones.
//
// nexo_activity_log guarda el detalle (solo inserción, RF-13) y
// nexo_actor_presence una fila por persona + token (RF-12). Nunca se guardan
// valores de tokens ni contenido de recuerdos (RF-14).

import (
	"context"
	"fmt"
	"strings"
)

// Operaciones registradas en nexo_activity_log.operation.
const (
	NexoOpPushChunk     = "push_chunk"
	NexoOpPushMutations = "push_mutations"
	NexoOpPullChunk     = "pull_chunk"
	NexoOpPullMutations = "pull_mutations"
	NexoOpPullManifest  = "pull_manifest"
)

// nexoPresenceKeyUnidentified agrupa en presencia todas las conexiones sin
// token asignado.
const nexoPresenceKeyUnidentified = NexoActorUnidentified

const nexoMaxUserAgentRunes = 256

// ActivityEvent es una conexión exitosa al servidor.
type ActivityEvent struct {
	Actor     MutationActor
	Operation string
	Project   string
	ItemCount int
	ClientIP  string
	UserAgent string
	// LogDetail indica si se agrega una fila en nexo_activity_log. Si es
	// false solo se actualiza la presencia (descargas vacías, manifiesto).
	LogDetail bool
}

// NexoPresenceKey devuelve la llave de presencia de un actor.
func NexoPresenceKey(actor MutationActor) string {
	actor = actor.normalized()
	if !actor.Verified {
		return nexoPresenceKeyUnidentified
	}
	return actor.PrincipalID + "|" + actor.TokenID
}

// RecordActivity inserta el detalle (si LogDetail) y actualiza la presencia en
// una sola sentencia atómica.
func (cs *CloudStore) RecordActivity(ctx context.Context, event ActivityEvent) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	actor := event.Actor.normalized()
	operation := strings.TrimSpace(event.Operation)
	if operation == "" {
		return fmt.Errorf("cloudstore: nexo activity operation is required")
	}
	project := strings.TrimSpace(event.Project)
	clientIP := nexoTruncateRunes(strings.TrimSpace(event.ClientIP), 64)
	userAgent := nexoTruncateRunes(strings.TrimSpace(event.UserAgent), nexoMaxUserAgentRunes)
	itemCount := event.ItemCount
	if itemCount < 0 {
		itemCount = 0
	}

	var pushInc, pullInc int64
	switch operation {
	case NexoOpPushChunk, NexoOpPushMutations:
		pushInc = 1
	case NexoOpPullChunk, NexoOpPullMutations:
		if event.LogDetail {
			pullInc = 1
		}
	}

	// RNF-03 (T-23): una sola sentencia (un viaje a Postgres) que es atómica por
	// sí misma: el detalle (CTE "detail", solo si LogDetail) y la presencia se
	// escriben juntos o no se escribe nada. Además, set_config(..., true) deja
	// synchronous_commit=off solo para esta transacción implícita: el registro no
	// espera el fsync del WAL. Antes eran BEGIN + INSERT + UPSERT + COMMIT
	// síncrono (~6 ms medidos por petición). Ante una caída de Postgres se
	// pueden perder las últimas filas de actividad (milisegundos), nunca datos
	// de sincronización; las escrituras de Engram siguen siendo síncronas.
	if _, err := cs.db.ExecContext(ctx, `
		WITH cfg AS MATERIALIZED (
			SELECT set_config('synchronous_commit', 'off', true) AS async_commit,
				(SELECT u.email FROM cloud_human_users u WHERE u.principal_id::text = $3) AS email
		), detail AS (
			INSERT INTO nexo_activity_log (actor, actor_email, actor_principal_id, actor_token_id, actor_verified,
				operation, project, item_count, client_ip, user_agent, client_claim)
			SELECT $2, cfg.email, NULLIF($3, ''), NULLIF($4, ''), $5,
				$13, NULLIF($9, ''), $14, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, '')
			FROM cfg WHERE $12::boolean
			RETURNING id
		)
		INSERT INTO nexo_actor_presence AS p (presence_key, actor, actor_email, actor_principal_id, actor_token_id,
			actor_verified, last_ip, last_user_agent, last_client_claim, last_project, push_count, pull_count)
		SELECT $1, $2, cfg.email, NULLIF($3, ''), NULLIF($4, ''), $5,
			NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), $10, $11
		FROM cfg
		ON CONFLICT (presence_key) DO UPDATE SET
			actor             = EXCLUDED.actor,
			actor_email       = EXCLUDED.actor_email,
			actor_verified    = EXCLUDED.actor_verified,
			last_seen_at      = NOW(),
			last_ip           = COALESCE(EXCLUDED.last_ip, p.last_ip),
			last_user_agent   = COALESCE(EXCLUDED.last_user_agent, p.last_user_agent),
			last_client_claim = COALESCE(EXCLUDED.last_client_claim, p.last_client_claim),
			last_project      = COALESCE(EXCLUDED.last_project, p.last_project),
			push_count        = p.push_count + EXCLUDED.push_count,
			pull_count        = p.pull_count + EXCLUDED.pull_count`,
		NexoPresenceKey(actor), actor.Name, actor.PrincipalID, actor.TokenID, actor.Verified,
		clientIP, userAgent, actor.ClientClaim, project, pushInc, pullInc,
		event.LogDetail, operation, itemCount,
	); err != nil {
		return fmt.Errorf("cloudstore: nexo activity record: %w", err)
	}
	return nil
}

// PurgeActivityOlderThan borra el detalle con más de `days` días (RF-15). La
// presencia se conserva siempre. days <= 0 no borra nada.
//
// NX-07 (RS-03): el borrado lo hace la función SECURITY DEFINER
// nexo_purge_activity, propiedad de la cuenta dueña, que exige un mínimo de 30
// días y deja un evento retention.purged en nexo_authz_events. La aplicación no
// necesita (ni tiene, con dos cuentas) privilegio DELETE sobre la tabla.
func (cs *CloudStore) PurgeActivityOlderThan(ctx context.Context, days int) (int64, error) {
	if cs == nil || cs.db == nil {
		return 0, fmt.Errorf("cloudstore: not initialized")
	}
	if days <= 0 {
		return 0, nil
	}
	var deleted int64
	if err := cs.db.QueryRowContext(ctx, `SELECT nexo_purge_activity($1)`, days).Scan(&deleted); err != nil {
		return 0, fmt.Errorf("cloudstore: nexo purge: %w", err)
	}
	return deleted, nil
}
