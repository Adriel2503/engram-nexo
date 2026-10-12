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
// una sola transacción.
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

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cloudstore: nexo activity begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if event.LogDetail {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO nexo_activity_log (actor, actor_email, actor_principal_id, actor_token_id, actor_verified,
				operation, project, item_count, client_ip, user_agent, client_claim)
			VALUES ($1, (SELECT u.email FROM cloud_human_users u WHERE u.principal_id::text = $2),
				NULLIF($2, ''), NULLIF($3, ''), $4, $5, NULLIF($6, ''), $7, NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''))`,
			actor.Name, actor.PrincipalID, actor.TokenID, actor.Verified,
			operation, project, itemCount, clientIP, userAgent, actor.ClientClaim,
		); err != nil {
			return fmt.Errorf("cloudstore: nexo activity insert: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO nexo_actor_presence AS p (presence_key, actor, actor_email, actor_principal_id, actor_token_id,
			actor_verified, last_ip, last_user_agent, last_client_claim, last_project, push_count, pull_count)
		VALUES ($1, $2, (SELECT u.email FROM cloud_human_users u WHERE u.principal_id::text = $3),
			NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), $10, $11)
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
	); err != nil {
		return fmt.Errorf("cloudstore: nexo presence upsert: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cloudstore: nexo activity commit: %w", err)
	}
	return nil
}

// PurgeActivityOlderThan borra el detalle con más de `days` días (RF-15). La
// presencia se conserva siempre. days <= 0 no borra nada.
func (cs *CloudStore) PurgeActivityOlderThan(ctx context.Context, days int) (int64, error) {
	if cs == nil || cs.db == nil {
		return 0, fmt.Errorf("cloudstore: not initialized")
	}
	if days <= 0 {
		return 0, nil
	}
	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("cloudstore: nexo purge begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('nexo.allow_retention', 'on', true)`); err != nil {
		return 0, fmt.Errorf("cloudstore: nexo purge enable: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM nexo_activity_log WHERE occurred_at < NOW() - make_interval(days => $1)`, days)
	if err != nil {
		return 0, fmt.Errorf("cloudstore: nexo purge delete: %w", err)
	}
	deleted, _ := result.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("cloudstore: nexo purge commit: %w", err)
	}
	return deleted, nil
}
