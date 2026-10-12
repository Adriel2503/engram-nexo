package cloudserver

// Nexo IA · T-23 / RNF-03 — benchmark del costo por petición de sincronización.
//
// Mide push/pull de punta a punta (handler HTTP → Postgres real) con un token
// asignado. El archivo solo usa APIs que ya existen en Engram 3.3.2, así que
// puede copiarse sin cambios sobre el código original (commit 35d1452) para
// comparar: la diferencia es el sobrecosto de NX-01, NX-02 y NX-04.
//
//	CLOUDSTORE_TEST_DSN=postgres://... go test -run '^$' -bench NexoOverhead -benchtime 300x -count 5 ./internal/cloud/cloudserver/
//
// Sin CLOUDSTORE_TEST_DSN se omite.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

type nexoBenchEnv struct {
	h  http.Handler
	db *sql.DB
}

func newNexoBenchEnv(b *testing.B) *nexoBenchEnv {
	b.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		b.Skip("CLOUDSTORE_TEST_DSN (URL) not set (requires Postgres)")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("nexo_bench_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _, _ = admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	schemaDSN := dsn + sep + "search_path=" + schema
	cs, err := cloudstore.New(cloud.Config{DSN: schemaDSN})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = cs.Close() })
	db, err := sql.Open("pgx", schemaDSN)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })

	var pid, tid string
	if err := db.QueryRow(`INSERT INTO cloud_principals (kind, display_name, role) VALUES ('human', 'ana', 'member') RETURNING id::text`).Scan(&pid); err != nil {
		b.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cloud_human_users (principal_id, username, email) VALUES ($1::bigint, 'ana', 'ana@rp.pe')`, pid); err != nil {
		b.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO cloud_principal_tokens (principal_id, token_prefix, token_hash, name) VALUES ($1::bigint, 'egc_bench', 'hash-bench', 'laptop-ana') RETURNING id::text`, pid).Scan(&tid); err != nil {
		b.Fatal(err)
	}
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{
		"ana-laptop": {ID: pid, Kind: cloudauth.PrincipalKindHuman, DisplayName: "ana", Role: cloudauth.RoleMember, Enabled: true, Source: cloudauth.PrincipalSourceManagedToken, TokenID: tid},
	}}
	srv := New(cs, authn, 0, WithPrincipalProjectAuthorizer(managedGrantAuthorizer{grants: map[string][]string{pid: {"nexo-transversal"}}}))
	env := &nexoBenchEnv{h: srv.Handler(), db: db}

	// Datos base: 10 paquetes y 30 cambios.
	for i := 0; i < 10; i++ {
		env.do(b, http.MethodPost, "/sync/push", nexoBenchChunk(fmt.Sprintf("seed-%d", i)))
	}
	for i := 0; i < 10; i++ {
		env.do(b, http.MethodPost, "/sync/mutations/push", nexoBenchMutations(fmt.Sprintf("seed-%d", i), 3))
	}
	return env
}

func (e *nexoBenchEnv) do(b *testing.B, method, path, body string) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "10.0.0.15:51000"
	req.Header.Set("User-Agent", "Go-http-client/1.1")
	req.Header.Set("Authorization", "Bearer ana-laptop")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		b.Fatalf("%s %s = %d %s", method, path, rec.Code, rec.Body.String())
	}
}

func nexoBenchChunk(key string) string {
	return `{"project":"nexo-transversal","created_by":"Administrador","data":{"sessions":[{"id":"s-` + key + `","directory":"/tmp/s"}],"observations":[{"sync_id":"obs-` + key + `","session_id":"s-` + key + `","type":"decision","title":"T ` + key + `","content":"C ` + key + `","scope":"project"}]}}`
}

func nexoBenchMutations(key string, n int) string {
	var entries []string
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("%s-%d", key, i)
		entries = append(entries, `{"project":"nexo-transversal","entity":"observation","entity_key":"obs-`+k+`","op":"upsert","payload":{"sync_id":"obs-`+k+`","session_id":"s-1","type":"decision","title":"T `+k+`","content":"C","scope":"project"}}`)
	}
	return `{"created_by":"Administrador","entries":[` + strings.Join(entries, ",") + `]}`
}

func (e *nexoBenchEnv) latestSeq(b *testing.B) int64 {
	var seq int64
	if err := e.db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM cloud_mutations`).Scan(&seq); err != nil {
		b.Fatal(err)
	}
	return seq
}

func BenchmarkNexoOverheadPushMutations(b *testing.B) {
	env := newNexoBenchEnv(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env.do(b, http.MethodPost, "/sync/mutations/push", nexoBenchMutations(fmt.Sprintf("pm-%d", i), 3))
	}
}

func BenchmarkNexoOverheadPushChunk(b *testing.B) {
	env := newNexoBenchEnv(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env.do(b, http.MethodPost, "/sync/push", nexoBenchChunk(fmt.Sprintf("pc-%d", i)))
	}
}

func BenchmarkNexoOverheadPullMutations3(b *testing.B) {
	env := newNexoBenchEnv(b)
	since := env.latestSeq(b) - 3
	path := fmt.Sprintf("/sync/mutations/pull?since_seq=%d", since)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env.do(b, http.MethodGet, path, "")
	}
}

func BenchmarkNexoOverheadPullMutationsEmpty(b *testing.B) {
	env := newNexoBenchEnv(b)
	path := fmt.Sprintf("/sync/mutations/pull?since_seq=%d", env.latestSeq(b))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env.do(b, http.MethodGet, path, "")
	}
}

func BenchmarkNexoOverheadPullManifest(b *testing.B) {
	env := newNexoBenchEnv(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env.do(b, http.MethodGet, "/sync/pull?project=nexo-transversal", "")
	}
}
