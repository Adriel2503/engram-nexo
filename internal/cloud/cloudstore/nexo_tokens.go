package cloudstore

// Nexo IA · NX-07 fase S3 (NX-06) — caducidad y alcance de los tokens
// administrados. Lógica de los enganches // NX-07 de identity.go:
//
//   - FindPrincipalTokenByHash trae nexo_expires_at y nexo_scope en la misma
//     consulta que valida el token y llama a nexoCheckResolvedToken: si venció
//     (y no está revocado, que Engram ya rechaza) devuelve
//     security.ErrTokenExpired; siempre deja los datos del token en el
//     contexto de la petición (security.TokenFacts).
//   - CreatePrincipalTokenWithAudit llama a nexoApplyTokenIssue dentro de su
//     transacción: caducidad y alcance pedidos (o 90 días y write) y ambos
//     datos en la metadata del evento de auditoría.
//   - ListPrincipalTokens completa ambos datos con nexoFillTokenFacts.

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/features"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// nexoCheckResolvedToken registra los datos del token y rechaza uno vencido.
func nexoCheckResolvedToken(ctx context.Context, token PrincipalToken, expired bool) error {
	security.RecordTokenFacts(ctx, security.TokenFacts{
		TokenID:     token.ID,
		PrincipalID: token.PrincipalID,
		Scope:       token.NexoScope,
		ExpiresAt:   token.NexoExpiresAt,
		Expired:     expired,
		Revoked:     token.RevokedAt != nil,
	})
	if expired && token.RevokedAt == nil {
		return security.ErrTokenExpired
	}
	return nil
}

// nexoApplyTokenIssue fija caducidad y alcance del token recién insertado en
// la transacción de creación y los agrega a la metadata de su auditoría.
func nexoApplyTokenIssue(ctx context.Context, tx *sql.Tx, token *PrincipalToken, audit *AuthAuditEvent) error {
	issue := security.TokenIssueFrom(ctx)
	expires, scope, err := security.ApplyTokenIssue(ctx, tx, token.ID, issue)
	if err != nil {
		return fmt.Errorf("cloudstore: %w", err)
	}
	token.NexoExpiresAt, token.NexoScope = expires, scope
	meta := make(map[string]any, len(audit.Metadata)+2)
	for k, v := range audit.Metadata {
		meta[k] = v
	}
	meta["scope"] = scope
	if expires != nil {
		meta["expires_at"] = expires.UTC().Format(time.RFC3339)
	} else {
		meta["expires_at"] = "never"
	}
	audit.Metadata = meta
	return nil
}

// nexoFillTokenFacts completa caducidad y alcance de un listado de tokens
// (una consulta).
func (cs *CloudStore) nexoFillTokenFacts(ctx context.Context, tokens []PrincipalToken) ([]PrincipalToken, error) {
	if len(tokens) == 0 {
		return tokens, nil
	}
	ids := make([]int64, 0, len(tokens))
	for _, t := range tokens {
		if n, err := strconv.ParseInt(t.ID, 10, 64); err == nil {
			ids = append(ids, n)
		}
	}
	rows, err := cs.db.QueryContext(ctx, `SELECT id::text, nexo_expires_at, nexo_scope FROM cloud_principal_tokens WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: nexo token facts: %w", err)
	}
	defer rows.Close()
	type facts struct {
		expires *time.Time
		scope   string
	}
	byID := make(map[string]facts, len(tokens))
	for rows.Next() {
		var id, scope string
		var expires sql.NullTime
		if err := rows.Scan(&id, &expires, &scope); err != nil {
			return nil, fmt.Errorf("cloudstore: nexo token facts: %w", err)
		}
		f := facts{scope: scope}
		if expires.Valid {
			v := expires.Time
			f.expires = &v
		}
		byID[id] = f
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: nexo token facts: %w", err)
	}
	for i := range tokens {
		if f, ok := byID[tokens[i].ID]; ok {
			tokens[i].NexoExpiresAt, tokens[i].NexoScope = f.expires, f.scope
		}
	}
	return tokens, nil
}

// ErrServiceAccountsDisabled: respaldo del interruptor NEXO_SERVICE_ACCOUNTS_ENABLED.
// Ninguna emisión de token para una cuenta de servicio pasa con el interruptor apagado.
var ErrServiceAccountsDisabled = features.ErrServiceAccountsDisabled
