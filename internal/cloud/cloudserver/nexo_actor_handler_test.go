package cloudserver

// Nexo IA · NX-01 / NX-02 — tests de handlers.
//
// - Con fakes: el handler resuelve el autor desde el token y lo pasa al store;
//   el registro de actividad nunca rompe la sincronización.
// - Con Postgres (CLOUDSTORE_TEST_DSN): flujo HTTP completo hasta las tablas.

import (
	"context"
	"database/sql"
	"errors"
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

// ─── Fakes ───────────────────────────────────────────────────────────────────

type nexoCapturingStore struct {
	*fakeMutationStore
	chunkActor     cloudstore.MutationActor
	chunkActorOK   bool
	chunkCreatedBy string
	batchActor     cloudstore.MutationActor
	batchActorOK   bool
	events         []cloudstore.ActivityEvent
	recordErr      error
	recordPanic    bool
}

func newNexoCapturingStore() *nexoCapturingStore {
	return &nexoCapturingStore{fakeMutationStore: newFakeMutationStore()}
}

func (s *nexoCapturingStore) WriteChunk(ctx context.Context, project, chunkID, createdBy, clientCreatedAt string, payload []byte) error {
	s.chunkActor, s.chunkActorOK = cloudstore.MutationActorFromContext(ctx)
	s.chunkCreatedBy = createdBy
	return s.fakeMutationStore.WriteChunk(ctx, project, chunkID, createdBy, clientCreatedAt, payload)
}

func (s *nexoCapturingStore) InsertMutationBatch(ctx context.Context, batch []cloudstore.MutationEntry) ([]int64, error) {
	s.batchActor, s.batchActorOK = cloudstore.MutationActorFromContext(ctx)
	return s.fakeMutationStore.InsertMutationBatch(ctx, batch)
}

func (s *nexoCapturingStore) RecordActivity(_ context.Context, event cloudstore.ActivityEvent) error {
	if s.recordPanic {
		panic("activity store exploded")
	}
	s.events = append(s.events, event)
	return s.recordErr
}

const nexoChunkBody = `{"project":"nexo-transversal","created_by":"Administrador","data":{"sessions":[{"id":"s-1","directory":"/tmp/s-1"}],"observations":[{"sync_id":"obs-1","session_id":"s-1","type":"decision","title":"T","content":"C","scope":"project"}]}}`
const nexoMutationBody = `{"created_by":"Administrador","entries":[{"project":"nexo-transversal","entity":"session","entity_key":"s-1","op":"upsert","payload":{"id":"s-1","directory":"/tmp/s-1"}},{"project":"nexo-transversal","entity":"observation","entity_key":"obs-1","op":"upsert","payload":{"sync_id":"obs-1","session_id":"s-1","type":"decision","title":"T","content":"C","scope":"project"}}]}`

func nexoDo(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "10.0.0.15:51000"
	req.Header.Set("User-Agent", "Go-http-client/1.1")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func nexoTestAuth() resolvingAuth {
	return resolvingAuth{principals: map[string]cloudauth.Principal{
		"ana-token":    {ID: "7", Kind: cloudauth.PrincipalKindHuman, DisplayName: "ana", Role: cloudauth.RoleMember, Enabled: true, Source: cloudauth.PrincipalSourceManagedToken, TokenID: "12"},
		"shared-token": {ID: "legacy:sync", Kind: cloudauth.PrincipalKindLegacy, DisplayName: "LEGACY_SYNC", Role: cloudauth.RoleMember, Enabled: true, Source: cloudauth.PrincipalSourceLegacyEnvSync},
	}}
}

// ─── Handlers con fakes ──────────────────────────────────────────────────────

func TestNexoHandlersResolveActorFromAssignedToken(t *testing.T) {
	ms := newNexoCapturingStore()
	srv := New(ms, nexoTestAuth(), 0, WithPrincipalProjectAuthorizer(managedGrantAuthorizer{grants: map[string][]string{"7": {"nexo-transversal"}}}))

	if rec := nexoDo(t, srv.Handler(), http.MethodPost, "/sync/push", "ana-token", nexoChunkBody); rec.Code != http.StatusOK {
		t.Fatalf("push chunk = %d %s", rec.Code, rec.Body.String())
	}
	want := cloudstore.MutationActor{Name: "ana", PrincipalID: "7", TokenID: "12", Verified: true, ClientClaim: "Administrador"}
	if !ms.chunkActorOK || ms.chunkActor != want || ms.chunkCreatedBy != "ana" {
		t.Fatalf("chunk actor = %+v (ok=%v) created_by=%q", ms.chunkActor, ms.chunkActorOK, ms.chunkCreatedBy)
	}

	if rec := nexoDo(t, srv.Handler(), http.MethodPost, "/sync/mutations/push", "ana-token", nexoMutationBody); rec.Code != http.StatusOK {
		t.Fatalf("mutation push = %d %s", rec.Code, rec.Body.String())
	}
	if !ms.batchActorOK || ms.batchActor != want {
		t.Fatalf("batch actor = %+v", ms.batchActor)
	}
	last := ms.mutations[len(ms.mutations)-1]
	if last.CreatedBy != "ana" {
		t.Fatalf("mutation created_by = %q, want ana", last.CreatedBy)
	}
}

func TestNexoHandlersSharedTokenAndNoAuthAreUnidentified(t *testing.T) {
	for _, tc := range []struct {
		name  string
		auth  Authenticator
		token string
	}{
		{"token compartido", nexoTestAuth(), "shared-token"},
		{"sin autenticación", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := newNexoCapturingStore()
			srv := New(ms, tc.auth, 0)
			if rec := nexoDo(t, srv.Handler(), http.MethodPost, "/sync/push", tc.token, nexoChunkBody); rec.Code != http.StatusOK {
				t.Fatalf("push chunk = %d %s", rec.Code, rec.Body.String())
			}
			want := cloudstore.MutationActor{Name: cloudstore.NexoActorUnidentified, ClientClaim: "Administrador"}
			if ms.chunkActor != want || ms.chunkCreatedBy != cloudstore.NexoActorUnidentified {
				t.Fatalf("chunk actor = %+v created_by=%q", ms.chunkActor, ms.chunkCreatedBy)
			}
			if rec := nexoDo(t, srv.Handler(), http.MethodPost, "/sync/mutations/push", tc.token, nexoMutationBody); rec.Code != http.StatusOK {
				t.Fatalf("mutation push = %d %s", rec.Code, rec.Body.String())
			}
			if ms.batchActor != want {
				t.Fatalf("batch actor = %+v", ms.batchActor)
			}
		})
	}
}

// RF-10, RF-11, RF-12, CA-12 con fakes: qué se registra por operación.
func TestNexoActivityRecordedPerOperation(t *testing.T) {
	ms := newNexoCapturingStore()
	srv := New(ms, nexoTestAuth(), 0, WithPrincipalProjectAuthorizer(managedGrantAuthorizer{grants: map[string][]string{"7": {"nexo-transversal"}}}))
	h := srv.Handler()

	nexoDo(t, h, http.MethodPost, "/sync/push", "ana-token", nexoChunkBody)
	nexoDo(t, h, http.MethodPost, "/sync/mutations/push", "ana-token", nexoMutationBody)
	nexoDo(t, h, http.MethodGet, "/sync/pull?project=nexo-transversal", "ana-token", "")
	var chunkID string
	for id := range ms.chunks {
		chunkID = id
	}
	nexoDo(t, h, http.MethodGet, "/sync/pull/"+chunkID+"?project=nexo-transversal", "ana-token", "")
	nexoDo(t, h, http.MethodGet, "/sync/mutations/pull?since_seq=0", "ana-token", "")
	nexoDo(t, h, http.MethodGet, "/sync/mutations/pull?since_seq=999999", "ana-token", "")

	type exp struct {
		op     string
		count  int
		detail bool
		claim  string
	}
	want := []exp{
		{cloudstore.NexoOpPushChunk, 2, true, "Administrador"},
		{cloudstore.NexoOpPushMutations, 2, true, "Administrador"},
		{cloudstore.NexoOpPullManifest, 0, false, ""},
		{cloudstore.NexoOpPullChunk, 2, true, ""},
	}
	if len(ms.events) != 6 {
		t.Fatalf("events = %d, want 6: %+v", len(ms.events), ms.events)
	}
	for i, w := range want {
		e := ms.events[i]
		if e.Operation != w.op || e.ItemCount != w.count || e.LogDetail != w.detail || e.Actor.ClientClaim != w.claim || e.Actor.Name != "ana" || e.ClientIP != "10.0.0.15" || e.UserAgent != "Go-http-client/1.1" || e.Project != "nexo-transversal" {
			t.Fatalf("event[%d] = %+v, want %+v", i, e, w)
		}
	}
	if e := ms.events[4]; e.Operation != cloudstore.NexoOpPullMutations || e.ItemCount == 0 || !e.LogDetail {
		t.Fatalf("non-empty mutation pull = %+v", e)
	}
	if e := ms.events[5]; e.Operation != cloudstore.NexoOpPullMutations || e.ItemCount != 0 || e.LogDetail {
		t.Fatalf("empty mutation pull must only touch presence: %+v", e)
	}
}

// CA-14: rechazos (proyecto pausado, sin permiso, sin token) no se registran.
func TestNexoRejectedRequestsAreNotRecorded(t *testing.T) {
	ms := newNexoCapturingStore()
	ms.syncEnabledMap["paused"] = false
	srv := New(ms, nexoTestAuth(), 0, WithPrincipalProjectAuthorizer(managedGrantAuthorizer{grants: map[string][]string{"7": {"nexo-transversal", "paused"}}}))
	h := srv.Handler()
	paused := strings.ReplaceAll(nexoChunkBody, "nexo-transversal", "paused")
	if rec := nexoDo(t, h, http.MethodPost, "/sync/push", "ana-token", paused); rec.Code != http.StatusConflict {
		t.Fatalf("paused push = %d", rec.Code)
	}
	if rec := nexoDo(t, h, http.MethodPost, "/sync/mutations/push", "ana-token", strings.ReplaceAll(nexoMutationBody, "nexo-transversal", "paused")); rec.Code != http.StatusConflict {
		t.Fatalf("paused mutation push = %d", rec.Code)
	}
	if rec := nexoDo(t, h, http.MethodPost, "/sync/push", "ana-token", strings.ReplaceAll(nexoChunkBody, "nexo-transversal", "ajeno")); rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden push = %d", rec.Code)
	}
	if rec := nexoDo(t, h, http.MethodPost, "/sync/push", "token-invalido", nexoChunkBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized push = %d", rec.Code)
	}
	if len(ms.events) != 0 {
		t.Fatalf("rejections must not be recorded as activity: %+v", ms.events)
	}
}

// CA-15 / RF-16 con fakes: error o pánico del registro no rompe la subida.
func TestNexoActivityFailureNeverBreaksSync(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*nexoCapturingStore)
	}{
		{"error", func(s *nexoCapturingStore) { s.recordErr = errors.New("postgres rechazó la escritura") }},
		{"pánico", func(s *nexoCapturingStore) { s.recordPanic = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := newNexoCapturingStore()
			tc.setup(ms)
			srv := New(ms, nexoTestAuth(), 0)
			if rec := nexoDo(t, srv.Handler(), http.MethodPost, "/sync/push", "ana-token", nexoChunkBody); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
				t.Fatalf("push = %d %s", rec.Code, rec.Body.String())
			}
			if rec := nexoDo(t, srv.Handler(), http.MethodPost, "/sync/mutations/push", "ana-token", nexoMutationBody); rec.Code != http.StatusOK {
				t.Fatalf("mutation push = %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// ─── Flujo HTTP completo contra Postgres ─────────────────────────────────────

type nexoRealEnv struct {
	srv     *CloudServer
	cs      *cloudstore.CloudStore
	db      *sql.DB
	anaID   string
	anaTok  map[string]string
	luisID  string
	luisTok string
}

func newNexoRealEnv(t *testing.T) *nexoRealEnv {
	t.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set (requires Postgres)")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("cloudserver_nexo_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	schemaDSN := dsn + sep + "search_path=" + schema
	cs, err := cloudstore.New(cloud.Config{DSN: schemaDSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	db, err := sql.Open("pgx", schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	env := &nexoRealEnv{cs: cs, db: db, anaTok: map[string]string{}}
	seed := func(username, email string, tokens ...string) (string, map[string]string) {
		var id string
		if err := db.QueryRow(`INSERT INTO cloud_principals (kind, display_name, role) VALUES ('human', $1, 'member') RETURNING id::text`, username).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO cloud_human_users (principal_id, username, email) VALUES ($1::bigint, $2, $3)`, id, username, email); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, name := range tokens {
			var tid string
			if err := db.QueryRow(`INSERT INTO cloud_principal_tokens (principal_id, token_prefix, token_hash, name) VALUES ($1::bigint, $2, $3, $4) RETURNING id::text`, id, "egc_test_"+name, "hash-"+name, name).Scan(&tid); err != nil {
				t.Fatal(err)
			}
			out[name] = tid
		}
		return id, out
	}
	env.anaID, env.anaTok = seed("ana", "ana@rp.pe", "laptop-ana", "ci")
	var luisTok map[string]string
	env.luisID, luisTok = seed("luis", "luis@rp.pe", "laptop-luis")
	env.luisTok = luisTok["laptop-luis"]

	human := func(id, name, tokenID string) cloudauth.Principal {
		return cloudauth.Principal{ID: id, Kind: cloudauth.PrincipalKindHuman, DisplayName: name, Role: cloudauth.RoleMember, Enabled: true, Source: cloudauth.PrincipalSourceManagedToken, TokenID: tokenID}
	}
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{
		"ana-laptop":   human(env.anaID, "ana", env.anaTok["laptop-ana"]),
		"ana-ci":       human(env.anaID, "ana", env.anaTok["ci"]),
		"luis-laptop":  human(env.luisID, "luis", env.luisTok),
		"shared-token": {ID: "legacy:sync", Kind: cloudauth.PrincipalKindLegacy, DisplayName: "LEGACY_SYNC", Role: cloudauth.RoleMember, Enabled: true, Source: cloudauth.PrincipalSourceLegacyEnvSync},
	}}
	grants := managedGrantAuthorizer{grants: map[string][]string{env.anaID: {"nexo-transversal", "pausado"}, env.luisID: {"nexo-transversal"}}}
	env.srv = New(cs, authn, 0, WithPrincipalProjectAuthorizer(grants))
	return env
}

func (e *nexoRealEnv) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

// CA-01..CA-06 y CA-10..CA-15 de punta a punta (handler → Postgres).
func TestNexoEndToEndAuthorshipAndActivity(t *testing.T) {
	env := newNexoRealEnv(t)
	h := env.srv.Handler()

	// CA-01/CA-02/CA-10: Ana sube con su token aunque la PC declare "Administrador".
	if rec := nexoDo(t, h, http.MethodPost, "/sync/push", "ana-laptop", nexoChunkBody); rec.Code != http.StatusOK {
		t.Fatalf("ana push = %d %s", rec.Code, rec.Body.String())
	}
	var actor, email, claim, createdBy string
	if err := env.db.QueryRow(`SELECT actor, actor_email, actor_client_claim, created_by FROM cloud_chunks WHERE project_name = 'nexo-transversal'`).Scan(&actor, &email, &claim, &createdBy); err != nil {
		t.Fatal(err)
	}
	if actor != "ana" || email != "ana@rp.pe" || claim != "Administrador" || createdBy != "ana" {
		t.Fatalf("chunk actor=%q email=%q claim=%q created_by=%q", actor, email, claim, createdBy)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM nexo_activity_log WHERE actor='ana' AND operation='push_chunk' AND item_count=2 AND client_ip='10.0.0.15' AND project='nexo-transversal' AND actor_token_id=$1`, env.anaTok["laptop-ana"]); n != 1 {
		t.Fatalf("push_chunk activity rows = %d", n)
	}

	// CA-03/CA-04: Luis edita y borra; CA-06: Ana desde otro token.
	edit := `{"created_by":"LUIS-PC","entries":[{"project":"nexo-transversal","entity":"observation","entity_key":"obs-1","op":"upsert","payload":{"sync_id":"obs-1","session_id":"s-1","type":"decision","title":"T2","content":"C2","scope":"project"}}]}`
	if rec := nexoDo(t, h, http.MethodPost, "/sync/mutations/push", "luis-laptop", edit); rec.Code != http.StatusOK {
		t.Fatalf("luis edit = %d %s", rec.Code, rec.Body.String())
	}
	del := `{"created_by":"LUIS-PC","entries":[{"project":"nexo-transversal","entity":"observation","entity_key":"obs-1","op":"delete","payload":{"sync_id":"obs-1"}}]}`
	if rec := nexoDo(t, h, http.MethodPost, "/sync/mutations/push", "luis-laptop", del); rec.Code != http.StatusOK {
		t.Fatalf("luis delete = %d %s", rec.Code, rec.Body.String())
	}
	rows, err := env.db.Query(`SELECT op, actor, actor_email, token_name, actor_verified FROM nexo_mutation_history WHERE entity_key='obs-1' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var op, a, em, tok string
		var v bool
		if err := rows.Scan(&op, &a, &em, &tok, &v); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s/%s/%s/%s/%v", op, a, em, tok, v))
	}
	rows.Close()
	want := []string{"upsert/ana/ana@rp.pe/laptop-ana/true", "upsert/luis/luis@rp.pe/laptop-luis/true", "delete/luis/luis@rp.pe/laptop-luis/true"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("history = %v, want %v", got, want)
	}
	if rec := nexoDo(t, h, http.MethodPost, "/sync/mutations/push", "ana-ci", nexoMutationBody); rec.Code != http.StatusOK {
		t.Fatalf("ana ci push = %d", rec.Code)
	}
	if n := env.count(t, `SELECT COUNT(DISTINCT token_name) FROM nexo_mutation_history WHERE actor='ana'`); n != 2 {
		t.Fatalf("ana tokens in history = %d, want 2", n)
	}

	// CA-05: token compartido → no identificado, la PC solo como referencia.
	shared := strings.ReplaceAll(nexoMutationBody, "obs-1", "obs-shared")
	if rec := nexoDo(t, h, http.MethodPost, "/sync/mutations/push", "shared-token", shared); rec.Code != http.StatusOK {
		t.Fatalf("shared push = %d", rec.Code)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM cloud_mutations WHERE entity_key='obs-shared' AND actor='no-identificado' AND NOT actor_verified AND actor_principal_id IS NULL AND actor_client_claim='Administrador'`); n != 1 {
		t.Fatalf("shared-token mutation rows = %d", n)
	}

	// CA-11/CA-12: Luis descarga cambios; luego 100 consultas vacías.
	if rec := nexoDo(t, h, http.MethodGet, "/sync/mutations/pull?since_seq=0", "luis-laptop", ""); rec.Code != http.StatusOK {
		t.Fatalf("luis pull = %d", rec.Code)
	}
	var latest int64
	if err := env.db.QueryRow(`SELECT MAX(seq) FROM cloud_mutations`).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if rec := nexoDo(t, h, http.MethodGet, fmt.Sprintf("/sync/mutations/pull?since_seq=%d", latest), "luis-laptop", ""); rec.Code != http.StatusOK {
			t.Fatalf("empty pull = %d", rec.Code)
		}
	}
	if n := env.count(t, `SELECT COUNT(*) FROM nexo_activity_log WHERE actor='luis' AND operation='pull_mutations'`); n != 1 {
		t.Fatalf("luis pull rows = %d, want 1", n)
	}
	var pullCount, pushCount int64
	var lastIP string
	if err := env.db.QueryRow(`SELECT pull_count, push_count, last_ip FROM nexo_actor_presence WHERE presence_key=$1`, env.luisID+"|"+env.luisTok).Scan(&pullCount, &pushCount, &lastIP); err != nil {
		t.Fatal(err)
	}
	if pullCount != 1 || pushCount != 2 || lastIP != "10.0.0.15" {
		t.Fatalf("luis presence pull=%d push=%d ip=%q", pullCount, pushCount, lastIP)
	}

	// CA-14: un rechazo (proyecto pausado) no queda como actividad.
	if err := env.cs.SetProjectSyncEnabled("pausado", false, "admin", "mantenimiento"); err != nil {
		t.Fatal(err)
	}
	before := env.count(t, `SELECT COUNT(*) FROM nexo_activity_log`)
	if rec := nexoDo(t, h, http.MethodPost, "/sync/push", "ana-laptop", strings.ReplaceAll(nexoChunkBody, "nexo-transversal", "pausado")); rec.Code != http.StatusConflict {
		t.Fatalf("paused push = %d", rec.Code)
	}
	if after := env.count(t, `SELECT COUNT(*) FROM nexo_activity_log`); after != before {
		t.Fatalf("rejected push was recorded (%d → %d)", before, after)
	}

	// CA-15: Postgres rechaza la escritura del registro; la subida funciona igual.
	if _, err := env.db.Exec(`ALTER TABLE nexo_activity_log RENAME TO nexo_activity_log_fuera`); err != nil {
		t.Fatal(err)
	}
	broken := strings.ReplaceAll(nexoMutationBody, "obs-1", "obs-broken")
	if rec := nexoDo(t, h, http.MethodPost, "/sync/mutations/push", "ana-laptop", broken); rec.Code != http.StatusOK {
		t.Fatalf("push with broken activity log = %d %s", rec.Code, rec.Body.String())
	}
	if n := env.count(t, `SELECT COUNT(*) FROM cloud_mutations WHERE entity_key='obs-broken' AND actor='ana'`); n != 1 {
		t.Fatalf("mutation not stored when activity log failed")
	}
	if _, err := env.db.Exec(`ALTER TABLE nexo_activity_log_fuera RENAME TO nexo_activity_log`); err != nil {
		t.Fatal(err)
	}

	// RF-14: nunca se guarda el valor del token.
	if n := env.count(t, `SELECT COUNT(*) FROM nexo_activity_log WHERE row_to_json(nexo_activity_log)::text ~ '(ana-laptop|luis-laptop|shared-token)'`); n != 0 {
		t.Fatalf("token values leaked into activity log")
	}
}

// T-14: la depuración usa el store real y respeta NEXO_ACTIVITY_RETENTION_DAYS.
func TestNexoActivityRetentionConfig(t *testing.T) {
	t.Setenv(NexoActivityRetentionEnv, "")
	if d := nexoActivityRetentionDays(); d != 180 {
		t.Fatalf("default = %d", d)
	}
	t.Setenv(NexoActivityRetentionEnv, "30")
	if d := nexoActivityRetentionDays(); d != 30 {
		t.Fatalf("30 → %d", d)
	}
	t.Setenv(NexoActivityRetentionEnv, "0")
	if d := nexoActivityRetentionDays(); d != 0 {
		t.Fatalf("0 → %d", d)
	}
	t.Setenv(NexoActivityRetentionEnv, "abc")
	if d := nexoActivityRetentionDays(); d != 180 {
		t.Fatalf("invalid → %d", d)
	}
}

func TestNexoPurgeActivityOnceAgainstPostgres(t *testing.T) {
	env := newNexoRealEnv(t)
	if rec := nexoDo(t, env.srv.Handler(), http.MethodPost, "/sync/push", "ana-laptop", nexoChunkBody); rec.Code != http.StatusOK {
		t.Fatalf("push = %d", rec.Code)
	}
	if deleted := env.srv.nexoPurgeActivityOnce(context.Background(), 180); deleted != 0 {
		t.Fatalf("fresh rows must survive purge, deleted %d", deleted)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM nexo_activity_log`); n != 1 {
		t.Fatalf("rows = %d", n)
	}
	if deleted := env.srv.nexoPurgeActivityOnce(context.Background(), 0); deleted != 0 {
		t.Fatalf("days=0 must not purge")
	}
}
