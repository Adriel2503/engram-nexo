package cloudstore

// Nexo IA · NX-07 fase S3 (NX-06) — caducidad y alcance de los tokens contra
// Postgres real (CLOUDSTORE_TEST_DSN). CA-S08 (almacén), RS-20, RS-21, RS-23.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/features"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

func TestNexoTokenExpiryAndScope(t *testing.T) {
	t.Setenv(features.ServiceAccountsEnv, "true") // NX-07: este caso emite tokens de una cuenta de servicio (apagadas por defecto)
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	ana := nexoSeedUser(t, cs, "ana", "ana@rp.pe", "laptop-ana")

	// Columnas y valores por defecto: write y 90 días para filas nuevas.
	var scope string
	var days float64
	if err := cs.db.QueryRowContext(ctx, `SELECT nexo_scope, EXTRACT(EPOCH FROM nexo_expires_at - now()) / 86400 FROM cloud_principal_tokens WHERE id = $1::bigint`, ana.TokenIDs["laptop-ana"]).Scan(&scope, &days); err != nil {
		t.Fatal(err)
	}
	if scope != "write" || days < 89.9 || days > 90.1 {
		t.Fatalf("defecto: scope=%s días=%.2f", scope, days)
	}

	// RS-23: un token anterior a NX-07 (sin caducidad) sigue resolviendo.
	if _, err := cs.db.ExecContext(ctx, `ALTER TABLE cloud_principal_tokens DISABLE TRIGGER nexo_token_expiry_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.db.ExecContext(ctx, `UPDATE cloud_principal_tokens SET nexo_expires_at = NULL WHERE id = $1::bigint`, ana.TokenIDs["laptop-ana"]); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.db.ExecContext(ctx, `ALTER TABLE cloud_principal_tokens ENABLE TRIGGER nexo_token_expiry_guard`); err != nil {
		t.Fatal(err)
	}
	fctx := security.WithTokenFactsHolder(ctx)
	tok, _, err := cs.FindPrincipalTokenByHash(fctx, "hash-ana-laptop-ana")
	if err != nil || tok.NexoExpiresAt != nil || tok.NexoScope != "write" {
		t.Fatalf("token sin caducidad: %+v %v", tok, err)
	}
	if f, ok := security.TokenFactsFrom(fctx); !ok || f.TokenID != tok.ID || f.Scope != "write" || f.Expired {
		t.Fatalf("datos en el contexto: %+v", f)
	}

	// CA-S08: un token vencido se rechaza con token_expired (misma consulta).
	if _, err := cs.db.ExecContext(ctx, `UPDATE cloud_principal_tokens SET nexo_expires_at = now() - interval '1 minute', nexo_scope = 'read' WHERE id = $1::bigint`, ana.TokenIDs["laptop-ana"]); err != nil {
		t.Fatal(err)
	}
	fctx = security.WithTokenFactsHolder(ctx)
	if _, _, err := cs.FindPrincipalTokenByHash(fctx, "hash-ana-laptop-ana"); !errors.Is(err, security.ErrTokenExpired) || !strings.HasPrefix(err.Error(), "token_expired") {
		t.Fatalf("vencido: %v", err)
	}
	if f, _ := security.TokenFactsFrom(fctx); !f.Expired || f.Scope != "read" || f.PrincipalID != ana.PrincipalID {
		t.Fatalf("datos del vencido: %+v", f)
	}
	// Un token revocado y vencido informa revocado (lo decide Engram).
	if _, err := cs.db.ExecContext(ctx, `UPDATE cloud_principal_tokens SET revoked_at = now() WHERE id = $1::bigint`, ana.TokenIDs["laptop-ana"]); err != nil {
		t.Fatal(err)
	}
	if tok, _, err := cs.FindPrincipalTokenByHash(ctx, "hash-ana-laptop-ana"); err != nil || tok.RevokedAt == nil {
		t.Fatalf("revocado: %v", err)
	}

	// Emisión con caducidad y alcance en la misma transacción, con auditoría.
	issueCtx := security.WithTokenIssue(ctx, security.TokenIssue{Days: 7, Scope: "read"})
	created, err := cs.CreatePrincipalTokenWithAudit(issueCtx, CreatePrincipalTokenParams{PrincipalID: ana.PrincipalID, TokenPrefix: "egc_t_x", TokenHash: "hash-x", Name: "x"},
		AuthAuditEvent{ActorSource: "test", Action: "token.create", Outcome: "success", Metadata: map[string]any{"name": "x"}})
	if err != nil || created.NexoScope != "read" || created.NexoExpiresAt == nil || time.Until(*created.NexoExpiresAt) < 6*24*time.Hour || time.Until(*created.NexoExpiresAt) > 7*24*time.Hour+time.Minute {
		t.Fatalf("emisión: %+v %v", created, err)
	}
	var meta string
	if err := cs.db.QueryRowContext(ctx, `SELECT metadata::text FROM cloud_auth_audit_log WHERE action = 'token.create' ORDER BY id DESC LIMIT 1`).Scan(&meta); err != nil || !strings.Contains(meta, `"scope": "read"`) || !strings.Contains(meta, `"expires_at"`) {
		t.Fatalf("auditoría: %s %v", meta, err)
	}
	// Sin opciones (bootstrap por CLI): 90 días y write.
	def, err := cs.CreatePrincipalTokenWithAudit(ctx, CreatePrincipalTokenParams{PrincipalID: ana.PrincipalID, TokenPrefix: "egc_t_y", TokenHash: "hash-y", Name: "y"},
		AuthAuditEvent{ActorSource: "bootstrap_cli", Action: "bootstrap.cli", Outcome: "success"})
	if err != nil || def.NexoScope != "write" || def.NexoExpiresAt == nil || time.Until(*def.NexoExpiresAt) < 89*24*time.Hour {
		t.Fatalf("bootstrap: %+v %v", def, err)
	}
	// RS-20: "sin caducidad" no se admite para personas, ni siquiera por SQL.
	if _, err := cs.CreatePrincipalTokenWithAudit(security.WithTokenIssue(ctx, security.TokenIssue{Days: 0, Scope: "write"}),
		CreatePrincipalTokenParams{PrincipalID: ana.PrincipalID, TokenPrefix: "egc_t_z", TokenHash: "hash-z", Name: "z"},
		AuthAuditEvent{ActorSource: "test", Action: "token.create", Outcome: "success"}); err == nil || !strings.Contains(err.Error(), "cuentas de servicio") {
		t.Fatalf("persona sin caducidad: %v", err)
	}
	var n int
	if err := cs.db.QueryRowContext(ctx, `SELECT count(*) FROM cloud_principal_tokens WHERE token_hash = 'hash-z'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("el token rechazado no debe quedar: %d %v", n, err)
	}
	var svc string
	if err := cs.db.QueryRowContext(ctx, `INSERT INTO cloud_principals (kind, display_name, role) VALUES ('service_account', 'ci', 'member') RETURNING id::text`).Scan(&svc); err != nil {
		t.Fatal(err)
	}
	never, err := cs.CreatePrincipalTokenWithAudit(security.WithTokenIssue(ctx, security.TokenIssue{Days: 0, Scope: "write"}),
		CreatePrincipalTokenParams{PrincipalID: svc, TokenPrefix: "egc_t_s", TokenHash: "hash-s", Name: "ci"},
		AuthAuditEvent{ActorSource: "test", Action: "token.create", Outcome: "success"})
	if err != nil || never.NexoExpiresAt != nil || never.NexoScope != "write" {
		t.Fatalf("cuenta de servicio sin caducidad: %+v %v", never, err)
	}
	// Listado con caducidad y alcance (RS-24).
	list, err := cs.ListPrincipalTokens(ctx, ana.PrincipalID)
	if err != nil || len(list) != 3 {
		t.Fatalf("listado: %d %v", len(list), err)
	}
	for _, tk := range list {
		if tk.NexoScope == "" {
			t.Fatalf("listado sin alcance: %+v", tk)
		}
	}
	// Datos por id (sesiones del panel).
	f, found, err := security.LoadTokenFacts(ctx, cs.db, created.ID)
	if err != nil || !found || f.Scope != "read" || f.Expired || f.Revoked {
		t.Fatalf("LoadTokenFacts: %+v %v %v", f, found, err)
	}
	info, found, err := security.LoadTokenInfo(ctx, cs.db, never.ID)
	if err != nil || !found || !info.NoExpiry {
		t.Fatalf("LoadTokenInfo: %+v %v", info, err)
	}
	// La migración es idempotente y no cambia los tokens existentes.
	if err := cs.nexoMigrate(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := cs.ListPrincipalTokens(ctx, svc); len(again) != 1 || again[0].NexoExpiresAt != nil {
		t.Fatalf("la migración repetida cambió tokens: %+v", again)
	}
}
