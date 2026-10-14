package seed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudserver"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/stackapi"
)

type testAuth struct {
	r *cloudauth.PrincipalResolver
}

func (a testAuth) Authorize(*http.Request) error {
	return errors.New("legacy Authorize no se usa con resolución de principales")
}

func (a testAuth) ResolveBearerToken(ctx context.Context, token string) (cloudauth.Principal, error) {
	return a.r.ResolveBearerToken(ctx, token)
}

type testTokenLookup struct {
	cs *cloudstore.CloudStore
}

func (l testTokenLookup) FindManagedTokenByHash(ctx context.Context, hash string) (cloudauth.ManagedTokenRecord, cloudauth.Principal, error) {
	token, p, err := l.cs.FindPrincipalTokenByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, cloudstore.ErrPrincipalTokenNotFound) {
			return cloudauth.ManagedTokenRecord{}, cloudauth.Principal{}, cloudauth.ErrUnknownToken
		}
		return cloudauth.ManagedTokenRecord{}, cloudauth.Principal{}, err
	}
	kind := cloudauth.PrincipalKindHuman
	if p.Kind == cloudstore.PrincipalKindServiceAccount {
		kind = cloudauth.PrincipalKindServiceAccount
	}
	role := cloudauth.RoleMember
	if p.Role == cloudstore.PrincipalRoleAdmin {
		role = cloudauth.RoleAdmin
	}
	return cloudauth.ManagedTokenRecord{ID: token.ID, PrincipalID: token.PrincipalID, RevokedAt: token.RevokedAt},
		cloudauth.Principal{ID: p.ID, Kind: kind, DisplayName: p.DisplayName, Role: role, Enabled: p.Enabled, Source: cloudauth.PrincipalSourceManagedToken}, nil
}

func (l testTokenLookup) MarkManagedTokenUsed(ctx context.Context, tokenID string) error {
	err := l.cs.MarkPrincipalTokenUsed(ctx, tokenID)
	switch {
	case errors.Is(err, cloudstore.ErrPrincipalTokenRevoked):
		return cloudauth.ErrTokenRevoked
	case errors.Is(err, cloudstore.ErrPrincipalDisabled):
		return cloudauth.ErrPrincipalDisabled
	}
	return err
}

func TestSeedPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("se salta la prueba de integración con Postgres: falta CLOUDSTORE_TEST_DSN")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("se salta la prueba: CLOUDSTORE_TEST_DSN debe ser una URL de conexión postgres://")
	}

	ctx := context.Background()
	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("conectar a postgres: %v", err)
	}
	t.Cleanup(func() { _ = adminDB.Close() })

	schema := fmt.Sprintf("seed_int_test_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("crear schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	cs, err := cloudstore.New(cloud.Config{DSN: dsn + sep + "search_path=" + schema})
	if err != nil {
		t.Fatalf("iniciar cloudstore en schema %s: %v", schema, err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	hasher, err := cloudauth.NewManagedTokenHasher([]byte("test-pepper-seed-at-least-32-bytes-long"))
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}

	resolver := cloudauth.NewPrincipalResolver(cloudauth.ResolverConfig{
		Hasher:        hasher,
		ManagedTokens: testTokenLookup{cs: cs},
	})

	srv := cloudserver.New(cs, testAuth{r: resolver}, 0,
		cloudserver.WithAdminIdentityStore(cs),
		cloudserver.WithManagedTokenHasher(hasher),
	)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	seederInstance := New()
	personas := seederInstance.Personas()
	tokens := map[string]string{}
	db := cs.NexoDB()

	// Crear las 6 personas y emitir sus tokens
	for _, p := range personas {
		reqRole := "member"
		if p.Plataforma == "superadmin" || p.Plataforma == "admin" {
			reqRole = "admin"
		}
		var id string
		if err := db.QueryRowContext(ctx, `INSERT INTO cloud_principals (kind, display_name, role) VALUES ('human', $1, $2) RETURNING id::text`, p.Username, reqRole).Scan(&id); err != nil {
			t.Fatalf("crear principal %s: %v", p.Username, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO cloud_human_users (principal_id, username, email) VALUES ($1::bigint, $2, $3)`, id, p.Username, p.Email); err != nil {
			t.Fatalf("crear usuario humano %s: %v", p.Username, err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := authz.AssignOnCreateTx(ctx, tx, id, "human", reqRole, "test"); err != nil {
			_ = tx.Rollback()
			t.Fatalf("asignar rol de plataforma a %s: %v", p.Username, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if p.Plataforma == "auditor" {
			if _, err := db.ExecContext(ctx, `INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role, granted_by_principal_id) VALUES ($1::bigint, 'platform', '*', 'auditor', $1::bigint)`, id); err != nil {
				t.Fatalf("asignar auditor a %s: %v", p.Username, err)
			}
		}

		// Emitir token administrado
		mt, err := cloudauth.GenerateManagedToken("live")
		if err != nil {
			t.Fatalf("generar token: %v", err)
		}
		h, err := hasher.Hash(mt.Raw)
		if err != nil {
			t.Fatalf("hasher.Hash: %v", err)
		}
		_, err = cs.CreatePrincipalTokenWithAudit(security.WithTokenIssue(ctx, security.TokenIssue{Days: 90, Scope: "write"}),
			cloudstore.CreatePrincipalTokenParams{PrincipalID: id, TokenPrefix: mt.Prefix, TokenHash: h, Name: p.Username},
			cloudstore.AuthAuditEvent{ActorSource: "test", Action: "token.create", Outcome: "success"})
		if err != nil {
			t.Fatalf("guardar token de %s: %v", p.Username, err)
		}
		tokens[p.Username] = mt.Raw
	}

	env := stackapi.Env{
		ServerURL: ts.URL,
		Tokens:    tokens,
	}

	// 1. Carga inicial
	rep, err := seederInstance.Load(ctx, env, stackapi.LoadOptions{Example: true})
	if err != nil {
		t.Fatalf("Load falló: %v", err)
	}

	if rep.Spaces != 6 {
		t.Errorf("rep.Spaces = %d, want 6", rep.Spaces)
	}
	if rep.Documents != 12 {
		t.Errorf("rep.Documents = %d, want 12", rep.Documents)
	}
	if rep.Memories != 5 { // 3 documentos + 2 propuestas; 1 omitida (obsoleta)
		t.Errorf("rep.Memories = %d, want 5", rep.Memories)
	}
	if rep.Proposals != 3 {
		t.Errorf("rep.Proposals = %d, want 3", rep.Proposals)
	}

	// 2. Repetir Load debe ser idempotente y no duplicar
	rep2, err := seederInstance.Load(ctx, env, stackapi.LoadOptions{Example: true})
	if err != nil {
		t.Fatalf("Load (segunda llamada) falló: %v", err)
	}
	if rep2.Spaces != 0 {
		t.Errorf("rep2.Spaces = %d, want 0 (sin cambios)", rep2.Spaces)
	}
	if rep2.Documents != 0 {
		t.Errorf("rep2.Documents = %d, want 0 (sin cambios)", rep2.Documents)
	}
	if rep2.Memories != 0 {
		t.Errorf("rep2.Memories = %d, want 0 (sin cambios)", rep2.Memories)
	}
	if rep2.Proposals != 0 {
		t.Errorf("rep2.Proposals = %d, want 0 (sin cambios)", rep2.Proposals)
	}

	// 3. El lector no ve operaciones (restringido sin acceso -> 404 hidden)
	lectorReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/nexo/kb/v1/spaces/operaciones", nil)
	lectorReq.Header.Set("Authorization", "Bearer "+tokens["lector"])
	lectorResp, err := http.DefaultClient.Do(lectorReq)
	if err != nil {
		t.Fatalf("petición lector: %v", err)
	}
	_ = lectorResp.Body.Close()
	if lectorResp.StatusCode != http.StatusNotFound && lectorResp.StatusCode != http.StatusForbidden {
		t.Errorf("lector no debe ver operaciones: código HTTP recibido %d", lectorResp.StatusCode)
	}

	// 4. El auditor ve eventos sin contenido (payload_withheld: true)
	auditorReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/nexo/kb/v1/spaces/operaciones/events?limit=50", nil)
	auditorReq.Header.Set("Authorization", "Bearer "+tokens["auditor"])
	auditorResp, err := http.DefaultClient.Do(auditorReq)
	if err != nil {
		t.Fatalf("petición auditor: %v", err)
	}
	var evOut struct {
		Items []struct {
			PayloadWithheld bool `json:"payload_withheld"`
		} `json:"items"`
	}
	_ = json.NewDecoder(auditorResp.Body).Decode(&evOut)
	_ = auditorResp.Body.Close()
	if auditorResp.StatusCode != http.StatusOK {
		t.Errorf("auditor debe poder leer eventos de auditoría: HTTP %d", auditorResp.StatusCode)
	}
	if len(evOut.Items) > 0 {
		withheld := false
		for _, it := range evOut.Items {
			if it.PayloadWithheld {
				withheld = true
				break
			}
		}
		if !withheld {
			t.Errorf("los eventos de operaciones para el auditor deben tener payload_withheld: true")
		}
	}

	// 5. El proponente no ve las propuestas de otros
	propReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/nexo/kb/v1/proposals", nil)
	propReq.Header.Set("Authorization", "Bearer "+tokens["proponente"])
	propResp, err := http.DefaultClient.Do(propReq)
	if err != nil {
		t.Fatalf("petición proponente: %v", err)
	}
	var propOut struct {
		Items []struct {
			AuthorID string `json:"author_principal_id"`
		} `json:"items"`
	}
	_ = json.NewDecoder(propResp.Body).Decode(&propOut)
	_ = propResp.Body.Close()
	if propResp.StatusCode != http.StatusOK {
		t.Errorf("proponente listar propuestas: HTTP %d", propResp.StatusCode)
	}

	// 6. Probar RemoveExample
	remRep, err := seederInstance.RemoveExample(ctx, env)
	if err != nil {
		t.Fatalf("RemoveExample falló: %v", err)
	}
	if remRep.Spaces != 6 {
		t.Errorf("remRep.Spaces = %d, want 6 (archivados)", remRep.Spaces)
	}
	if remRep.Documents == 0 {
		t.Errorf("remRep.Documents debe retirar documentos, retiró 0")
	}
	if len(remRep.Notes) == 0 {
		t.Errorf("remRep.Notes debe reportar la falta de API para borrado permanente de espacios")
	}
}
