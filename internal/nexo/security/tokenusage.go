package security

// SG-10 · Último uso de los tokens (de cuentas de servicio y de cualquier
// principal): cuándo y desde qué IP y cliente. No agrega escrituras por
// petición; reúne lo que ya se registra:
//
//   - cloud_principal_tokens.last_used_at: lo actualiza Engram en cada
//     petición autenticada (la hora exacta del último uso);
//   - la IP y el cliente de la última acción registrada con ese token:
//     nexo_actor_presence (sincronización, NX-02), nexo_kb_events
//     (conocimiento: subidas, publicaciones, denegaciones) y
//     nexo_authz_events (administración y denegaciones).
//
// Una petición de solo lectura que no deja registro (por ejemplo una
// búsqueda) mueve last_used_at pero no la IP: por eso se informan las dos
// horas por separado.

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
)

// TokenLastSeen es la última acción registrada de un token.
type TokenLastSeen struct {
	At        time.Time `json:"at"`
	IP        string    `json:"ip,omitempty"`
	UserAgent string    `json:"user_agent,omitempty"`
	Source    string    `json:"source"` // sync | kb | authz
}

// LoadTokensLastSeen devuelve, por id de token, la última acción registrada
// de los tokens de un principal (los tokens sin registro no aparecen).
func LoadTokensLastSeen(ctx context.Context, db *sql.DB, principalID string, tokenIDs []string) (map[string]TokenLastSeen, error) {
	out := map[string]TokenLastSeen{}
	pid, err := strconv.ParseInt(strings.TrimSpace(principalID), 10, 64)
	if err != nil || len(tokenIDs) == 0 {
		return out, nil
	}
	ids := make([]int64, 0, len(tokenIDs))
	for _, t := range tokenIDs {
		if n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
			ids = append(ids, n)
		}
	}
	rows, err := db.QueryContext(ctx, `
		SELECT t.id::text, u.at, COALESCE(u.ip, ''), COALESCE(u.ua, ''), u.source
		FROM unnest($2::bigint[]) AS t(id)
		CROSS JOIN LATERAL (
			SELECT * FROM (
				(SELECT pr.last_seen_at AS at, pr.last_ip AS ip, pr.last_user_agent AS ua, 'sync' AS source
				 FROM nexo_actor_presence pr WHERE pr.presence_key = $1::bigint::text || '|' || t.id::text)
				UNION ALL
				(SELECT k.occurred_at, k.client_ip, k.user_agent, 'kb'
				 FROM nexo_kb_events k WHERE k.actor_principal_id = $1::bigint AND k.actor_token_id = t.id
				 ORDER BY k.id DESC LIMIT 1)
				UNION ALL
				(SELECT a.occurred_at, a.client_ip, a.user_agent, 'authz'
				 FROM nexo_authz_events a WHERE a.actor_principal_id = $1::bigint AND a.actor_token_id = t.id
				 ORDER BY a.id DESC LIMIT 1)
			) x ORDER BY x.at DESC LIMIT 1
		) u`, pid, ids)
	if err != nil {
		return nil, fmt.Errorf("nexo: último uso de tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var ls TokenLastSeen
		if err := rows.Scan(&id, &ls.At, &ls.IP, &ls.UserAgent, &ls.Source); err != nil {
			return nil, fmt.Errorf("nexo: último uso de tokens: %w", err)
		}
		redact.Struct(&ls) // NX-07: User-Agent histórico (presencia, eventos de conocimiento y de roles) sin credenciales
		out[id] = ls
	}
	return out, rows.Err()
}
