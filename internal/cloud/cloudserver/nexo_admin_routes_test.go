package cloudserver

// Nexo IA · NX-03 — tests de la API /nexo/admin/*.
//
// - Con fakes: control de acceso (401/403), validación de parámetros, límite
//   200, cursor y auditoría de cada lectura (también cuando falla).
// - Con Postgres (CLOUDSTORE_TEST_DSN): CA-30 a CA-36 de punta a punta.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

// ─── Fakes ───────────────────────────────────────────────────────────────────

type nexoTraceFake struct {
	*adminTestStore
	calls         int
	auditQuery    cloudstore.NexoAuditQuery
	activityQuery cloudstore.NexoActivityQuery
	presenceQuery cloudstore.NexoPresenceQuery
	historyQuery  cloudstore.NexoHistoryQuery
	next          string
	queryErr      error
}

func newNexoTraceFake() *nexoTraceFake {
	return &nexoTraceFake{adminTestStore: newAdminTestStore()}
}

func (f *nexoTraceFake) ListAuthAudit(_ context.Context, q cloudstore.NexoAuditQuery) ([]cloudstore.NexoAuditRecord, string, error) {
	f.calls++
	f.auditQuery = q
	return []cloudstore.NexoAuditRecord{{ID: "9", Action: "sync.auth", Outcome: "denied", ClientIP: "10.0.0.15"}}, f.next, f.queryErr
}

func (f *nexoTraceFake) ListActivity(_ context.Context, q cloudstore.NexoActivityQuery) ([]cloudstore.NexoActivityRecord, string, error) {
	f.calls++
	f.activityQuery = q
	return []cloudstore.NexoActivityRecord{{ID: "5", Actor: "luis", Operation: cloudstore.NexoOpPushMutations}}, f.next, f.queryErr
}

func (f *nexoTraceFake) ListPresence(_ context.Context, q cloudstore.NexoPresenceQuery) ([]cloudstore.NexoPresenceRecord, string, error) {
	f.calls++
	f.presenceQuery = q
	return nil, f.next, f.queryErr
}

func (f *nexoTraceFake) MutationHistory(_ context.Context, q cloudstore.NexoHistoryQuery) ([]cloudstore.NexoHistoryRecord, string, error) {
	f.calls++
	f.historyQuery = q
	return []cloudstore.NexoHistoryRecord{{Seq: "3", Version: 1, Op: "upsert"}}, f.next, f.queryErr
}

func nexoAdminPrincipal() cloudauth.Principal {
	return cloudauth.Principal{ID: "1", Kind: cloudauth.PrincipalKindHuman, DisplayName: "admin", Role: cloudauth.RoleAdmin, Enabled: true, Source: cloudauth.PrincipalSourceManagedToken, TokenID: "100"}
}

func nexoAdminFakeAuth() resolvingAuth {
	return resolvingAuth{principals: map[string]cloudauth.Principal{
		"admin-token":  nexoAdminPrincipal(),
		"member-token": {ID: "7", Kind: cloudauth.PrincipalKindHuman, DisplayName: "ana", Role: cloudauth.RoleMember, Enabled: true, Source: cloudauth.PrincipalSourceManagedToken, TokenID: "12"},
		"shared-token": {ID: "legacy:sync", Kind: cloudauth.PrincipalKindLegacy, DisplayName: "LEGACY_SYNC", Role: cloudauth.RoleMember, Enabled: true, Source: cloudauth.PrincipalSourceLegacyEnvSync},
		"legacy-admin": {ID: "legacy:admin", Kind: cloudauth.PrincipalKindLegacy, DisplayName: "LEGACY_ADMIN", Role: cloudauth.RoleAdmin, Enabled: true, Source: cloudauth.PrincipalSourceLegacyEnvAdmin},
	}}
}

var nexoAdminPaths = []string{"/nexo/admin/audit", "/nexo/admin/activity", "/nexo/admin/presence", "/nexo/admin/history/obs-1"}

func nexoDecodePage(t *testing.T, rec *httptest.ResponseRecorder) (items []map[string]any, next *string, limit int) {
	t.Helper()
	var page struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"next_cursor"`
		Limit      int              `json:"limit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v (%s)", err, rec.Body.String())
	}
	if page.Items == nil {
		t.Fatalf("items debe ser un arreglo, nunca null: %s", rec.Body.String())
	}
	return page.Items, page.NextCursor, page.Limit
}

// ─── Control de acceso (RF-30, CA-30) ────────────────────────────────────────

func TestNexoAdminRoutesAccessControl(t *testing.T) {
	cases := []struct {
		name  string
		auth  Authenticator
		token string
		want  int
	}{
		{"sin token", nexoAdminFakeAuth(), "", http.StatusUnauthorized},
		{"token inválido", nexoAdminFakeAuth(), "nope", http.StatusUnauthorized},
		{"miembro (CA-30)", nexoAdminFakeAuth(), "member-token", http.StatusForbidden},
		{"token compartido", nexoAdminFakeAuth(), "shared-token", http.StatusForbidden},
		{"admin heredado del entorno", nexoAdminFakeAuth(), "legacy-admin", http.StatusForbidden},
		{"modo sin autenticación", nil, "", http.StatusForbidden},
		{"admin con token asignado", nexoAdminFakeAuth(), "admin-token", http.StatusOK},
	}
	for _, tc := range cases {
		for _, path := range nexoAdminPaths {
			t.Run(tc.name+" "+path, func(t *testing.T) {
				store := newNexoTraceFake()
				srv := New(store, tc.auth, 0, WithAdminIdentityStore(store))
				rec := nexoDo(t, srv.Handler(), http.MethodGet, path, tc.token, "")
				if rec.Code != tc.want {
					t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
				}
				reads := 0
				for _, e := range store.auditEvents {
					if strings.HasPrefix(e.Action, "nexo.") {
						reads++
					}
				}
				if tc.want != http.StatusOK && (store.calls != 0 || reads != 0) {
					t.Fatalf("un acceso rechazado no debe consultar (%d) ni auditarse como lectura (%d)", store.calls, reads)
				}
				if tc.want == http.StatusOK && (store.calls != 1 || reads != 1) {
					t.Fatalf("lectura permitida: consultas=%d auditorías=%d", store.calls, reads)
				}
			})
		}
	}
}

func TestNexoAdminRoutesDoNotChangeEngramRoutes(t *testing.T) {
	store := newNexoTraceFake()
	srv := New(store, nexoAdminFakeAuth(), 0, WithAdminIdentityStore(store))
	if rec := nexoDo(t, srv.Handler(), http.MethodGet, "/admin/users", "admin-token", ""); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "items") {
		t.Fatalf("/admin/users = %d %s", rec.Code, rec.Body.String())
	}
	if rec := nexoDo(t, srv.Handler(), http.MethodPost, "/nexo/admin/audit", "admin-token", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /nexo/admin/audit = %d, want 405 (solo lectura)", rec.Code)
	}
}

// ─── Parámetros, límite, cursor y auditoría (RF-35, RF-36) ───────────────────

func TestNexoAdminRoutesParamsLimitCursorAndAudit(t *testing.T) {
	store := newNexoTraceFake()
	store.next = "4"
	srv := New(store, nexoAdminFakeAuth(), 0, WithAdminIdentityStore(store))
	h := srv.Handler()

	rec := nexoDo(t, h, http.MethodGet, "/nexo/admin/activity?actor=luis&project=nexo-transversal&operation=push_mutations&token=laptop-luis&from=2026-10-01&to=2026-10-12&limit=500&cursor=10", "admin-token", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("activity = %d %s", rec.Code, rec.Body.String())
	}
	items, next, limit := nexoDecodePage(t, rec)
	if len(items) != 1 || next == nil || *next != "4" || limit != 200 {
		t.Fatalf("page = %v next=%v limit=%d", items, next, limit)
	}
	q := store.activityQuery
	wantFrom := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC) // "to" con fecha cubre el día completo
	if q.Actor != "luis" || q.Project != "nexo-transversal" || q.Operation != "push_mutations" || q.Token != "laptop-luis" || q.Cursor != "10" || q.Limit != 200 || !q.From.Equal(wantFrom) || !q.To.Equal(wantTo) {
		t.Fatalf("query = %+v", q)
	}
	ev := store.auditEvents[len(store.auditEvents)-1]
	filters, _ := ev.Metadata["filters"].(map[string]any)
	if ev.Action != "nexo.activity.read" || ev.ActorPrincipalID != "1" || ev.ActorSource != string(cloudauth.PrincipalSourceManagedToken) || ev.Outcome != "success" || ev.Project != "nexo-transversal" || ev.Metadata["result_count"] != 1 || filters["actor"] != "luis" || filters["limit"] != 200 {
		t.Fatalf("auditoría de la lectura = %+v", ev)
	}

	// Sin más páginas: next_cursor es null.
	store.next = ""
	rec = nexoDo(t, h, http.MethodGet, "/nexo/admin/presence?since=2026-10-01T00:00:00Z", "admin-token", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"next_cursor":null`) || !strings.Contains(rec.Body.String(), `"items":[]`) || !strings.Contains(rec.Body.String(), `"limit":100`) {
		t.Fatalf("presence = %d %s", rec.Code, rec.Body.String())
	}
	if !store.presenceQuery.Since.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("since = %v", store.presenceQuery.Since)
	}

	rec = nexoDo(t, h, http.MethodGet, "/nexo/admin/history/obs-1?project=nexo-transversal&include_payload=true", "admin-token", "")
	if rec.Code != http.StatusOK || store.historyQuery.EntityKey != "obs-1" || !store.historyQuery.IncludePayload || store.historyQuery.Project != "nexo-transversal" {
		t.Fatalf("history = %d %+v", rec.Code, store.historyQuery)
	}
	if ev := store.auditEvents[len(store.auditEvents)-1]; ev.Action != "nexo.history.read" || ev.Metadata["filters"].(map[string]any)["entity_key"] != "obs-1" {
		t.Fatalf("auditoría del historial = %+v", ev)
	}

	rec = nexoDo(t, h, http.MethodGet, "/nexo/admin/audit?action=sync.*&outcome=denied&actor=request", "admin-token", "")
	if rec.Code != http.StatusOK || store.auditQuery.Action != "sync.*" || store.auditQuery.Outcome != "denied" || store.auditQuery.Actor != "request" {
		t.Fatalf("audit = %d %+v", rec.Code, store.auditQuery)
	}
	if ev := store.auditEvents[len(store.auditEvents)-1]; ev.Action != "nexo.audit.read" {
		t.Fatalf("auditoría de la auditoría = %+v", ev)
	}

	// Parámetros inválidos → 400, sin consultar ni auditar.
	callsBefore, auditsBefore := store.calls, len(store.auditEvents)
	for _, path := range []string{
		"/nexo/admin/activity?limit=0",
		"/nexo/admin/activity?limit=abc",
		"/nexo/admin/activity?operation=borrar_todo",
		"/nexo/admin/audit?from=ayer",
		"/nexo/admin/presence?since=2026-13-01",
		"/nexo/admin/history/obs-1?include_payload=quizas",
		"/nexo/admin/audit?actor=" + strings.Repeat("x", 300),
	} {
		if rec := nexoDo(t, h, http.MethodGet, path, "admin-token", ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400 (%s)", path, rec.Code, rec.Body.String())
		}
	}
	if store.calls != callsBefore || len(store.auditEvents) != auditsBefore {
		t.Fatalf("una consulta inválida no debe ejecutarse ni auditarse")
	}

	// Cursor que el store no puede interpretar → 400.
	store.queryErr = cloudstore.ErrNexoInvalidCursor
	if rec := nexoDo(t, h, http.MethodGet, "/nexo/admin/audit?cursor=xyz", "admin-token", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("cursor inválido = %d", rec.Code)
	}
	store.queryErr = errors.New("postgres caído")
	if rec := nexoDo(t, h, http.MethodGet, "/nexo/admin/audit", "admin-token", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("error de consulta = %d", rec.Code)
	}
}

// RF-36: si la lectura no se puede auditar, no se entregan datos.
func TestNexoAdminRoutesFailClosedWhenAuditFails(t *testing.T) {
	store := newNexoTraceFake()
	store.auditErr = errors.New("auditoría no disponible")
	srv := New(store, nexoAdminFakeAuth(), 0, WithAdminIdentityStore(store))
	for _, path := range nexoAdminPaths {
		rec := nexoDo(t, srv.Handler(), http.MethodGet, path, "admin-token", "")
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), `"items"`) {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestNexoAdminRoutesWithoutStores(t *testing.T) {
	// Sin almacén de auditoría (no hay dónde registrar la lectura) → 500.
	srv := New(&fakeStore{}, nexoAdminFakeAuth(), 0)
	if rec := nexoDo(t, srv.Handler(), http.MethodGet, "/nexo/admin/presence", "admin-token", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("sin admin store = %d", rec.Code)
	}
	// Con almacén de auditoría pero sin consultas de Nexo → 500.
	admin := newAdminTestStore()
	srv = New(&fakeStore{}, nexoAdminFakeAuth(), 0, WithAdminIdentityStore(admin))
	if rec := nexoDo(t, srv.Handler(), http.MethodGet, "/nexo/admin/presence", "admin-token", ""); rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "nexo trace store") {
		t.Fatalf("sin trace store = %d %s", rec.Code, rec.Body.String())
	}
}

// ─── De punta a punta contra Postgres (CA-30 a CA-36) ────────────────────────

type nexoAdminEnv struct {
	*nexoRealEnv
	adminID string
	h       http.Handler
}

// newNexoAdminEnv parte del entorno de NX-01/NX-02 (ana, luis, token
// compartido) y agrega un admin con token asignado y el almacén de auditoría.
func newNexoAdminEnv(t *testing.T) *nexoAdminEnv {
	t.Helper()
	base := newNexoRealEnv(t)
	env := &nexoAdminEnv{nexoRealEnv: base}
	if err := base.db.QueryRow(`INSERT INTO cloud_principals (kind, display_name, role) VALUES ('human', 'admin', 'admin') RETURNING id::text`).Scan(&env.adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := base.db.Exec(`INSERT INTO cloud_human_users (principal_id, username, email) VALUES ($1::bigint, 'admin', 'admin@rp.pe')`, env.adminID); err != nil {
		t.Fatal(err)
	}
	nexoSeedPlatformRole(t, base.db, env.adminID, "human", "admin") // NX-05: el rol de Nexo; la columna se deriva
	var adminTok string
	if err := base.db.QueryRow(`INSERT INTO cloud_principal_tokens (principal_id, token_prefix, token_hash, name) VALUES ($1::bigint, 'egc_test_admin', 'hash-admin', 'laptop-admin') RETURNING id::text`, env.adminID).Scan(&adminTok); err != nil {
		t.Fatal(err)
	}
	human := func(id, name, tokenID string, role cloudauth.Role) cloudauth.Principal {
		return cloudauth.Principal{ID: id, Kind: cloudauth.PrincipalKindHuman, DisplayName: name, Role: role, Enabled: true, Source: cloudauth.PrincipalSourceManagedToken, TokenID: tokenID}
	}
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{
		"admin-token":  human(env.adminID, "admin", adminTok, cloudauth.RoleAdmin),
		"ana-laptop":   human(base.anaID, "ana", base.anaTok["laptop-ana"], cloudauth.RoleMember),
		"luis-laptop":  human(base.luisID, "luis", base.luisTok, cloudauth.RoleMember),
		"shared-token": {ID: "legacy:sync", Kind: cloudauth.PrincipalKindLegacy, DisplayName: "LEGACY_SYNC", Role: cloudauth.RoleMember, Enabled: true, Source: cloudauth.PrincipalSourceLegacyEnvSync},
	}}
	grants := managedGrantAuthorizer{grants: map[string][]string{base.anaID: {"nexo-transversal"}, base.luisID: {"nexo-transversal"}}}
	base.srv = New(base.cs, authn, 0, WithPrincipalProjectAuthorizer(grants), WithAdminIdentityStore(base.cs))
	env.h = base.srv.Handler()
	return env
}

func TestNexoAdminAPIEndToEnd(t *testing.T) {
	env := newNexoAdminEnv(t)
	h := env.h

	// Datos: Ana crea obs-1, Luis la edita y descarga; un intento con token inválido.
	if rec := nexoDo(t, h, http.MethodPost, "/sync/mutations/push", "ana-laptop", nexoMutationBody); rec.Code != http.StatusOK {
		t.Fatalf("ana push = %d %s", rec.Code, rec.Body.String())
	}
	edit := `{"created_by":"LUIS-PC","entries":[{"project":"nexo-transversal","entity":"observation","entity_key":"obs-1","op":"upsert","payload":{"sync_id":"obs-1","session_id":"s-1","type":"decision","title":"T2","content":"C2","scope":"project"}}]}`
	if rec := nexoDo(t, h, http.MethodPost, "/sync/mutations/push", "luis-laptop", edit); rec.Code != http.StatusOK {
		t.Fatalf("luis edit = %d %s", rec.Code, rec.Body.String())
	}
	if rec := nexoDo(t, h, http.MethodGet, "/sync/mutations/pull?since_seq=0", "luis-laptop", ""); rec.Code != http.StatusOK {
		t.Fatalf("luis pull = %d", rec.Code)
	}
	if rec := nexoDo(t, h, http.MethodGet, "/sync/mutations/pull?since_seq=0", "token-robado", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token inválido = %d", rec.Code)
	}

	// CA-30: Ana (member) recibe «prohibido».
	if rec := nexoDo(t, h, http.MethodGet, "/nexo/admin/audit", "ana-laptop", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("member = %d", rec.Code)
	}
	if rec := nexoDo(t, h, http.MethodGet, "/nexo/admin/audit", "shared-token", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("token compartido = %d", rec.Code)
	}

	// CA-31: rechazos de la última semana con fecha, IP y motivo.
	weekAgo := time.Now().UTC().AddDate(0, 0, -7).Format(time.DateOnly)
	rec := nexoDo(t, h, http.MethodGet, "/nexo/admin/audit?outcome=denied&from="+weekAgo, "admin-token", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit = %d %s", rec.Code, rec.Body.String())
	}
	items, _, _ := nexoDecodePage(t, rec)
	foundDenied := false
	for _, it := range items {
		if it["outcome"] != "denied" {
			t.Fatalf("filtro outcome no aplicado: %v", it)
		}
		if it["action"] == "sync.auth" && it["reason_code"] == "unknown_token" && it["client_ip"] == "10.0.0.15" && it["user_agent"] == "Go-http-client/1.1" && it["occurred_at"] != "" {
			foundDenied = true
		}
	}
	if !foundDenied {
		t.Fatalf("no aparece el rechazo con IP y motivo: %v", items)
	}

	// CA-32: actividad de Luis en nexo-transversal.
	rec = nexoDo(t, h, http.MethodGet, "/nexo/admin/activity?actor=luis&project=nexo-transversal", "admin-token", "")
	items, _, _ = nexoDecodePage(t, rec)
	if rec.Code != http.StatusOK || len(items) != 2 {
		t.Fatalf("actividad de luis = %d %v", rec.Code, items)
	}
	for _, it := range items {
		if it["actor"] != "luis" || it["client_ip"] != "10.0.0.15" || it["token_name"] != "laptop-luis" || it["occurred_at"] == "" || it["item_count"].(float64) <= 0 {
			t.Fatalf("fila de actividad = %v", it)
		}
	}
	if items[0]["operation"] != cloudstore.NexoOpPullMutations || items[1]["operation"] != cloudstore.NexoOpPushMutations {
		t.Fatalf("orden de actividad = %v, %v", items[0]["operation"], items[1]["operation"])
	}

	// CA-33: presencia con Ana y Luis, sus tokens y última conexión.
	rec = nexoDo(t, h, http.MethodGet, "/nexo/admin/presence", "admin-token", "")
	items, _, _ = nexoDecodePage(t, rec)
	seen := map[string]map[string]any{}
	for _, it := range items {
		seen[fmt.Sprint(it["actor"])] = it
	}
	if seen["ana"]["token_name"] != "laptop-ana" || seen["luis"]["token_name"] != "laptop-luis" || seen["luis"]["last_seen_at"] == nil || seen["luis"]["last_client_claim"] != "LUIS-PC" {
		t.Fatalf("presencia = %v", items)
	}

	// CA-34: historial de obs-1 con autor y email de cada versión.
	rec = nexoDo(t, h, http.MethodGet, "/nexo/admin/history/obs-1", "admin-token", "")
	items, _, _ = nexoDecodePage(t, rec)
	if rec.Code != http.StatusOK || len(items) != 2 {
		t.Fatalf("historial = %d %v", rec.Code, items)
	}
	if items[0]["actor"] != "luis" || items[0]["actor_email"] != "luis@rp.pe" || items[0]["version"].(float64) != 2 ||
		items[1]["actor"] != "ana" || items[1]["actor_email"] != "ana@rp.pe" || items[1]["version"].(float64) != 1 || items[1]["title"] != "T" {
		t.Fatalf("versiones = %v", items)
	}
	if _, has := items[0]["payload"]; has {
		t.Fatalf("payload sin include_payload: %v", items[0])
	}
	rec = nexoDo(t, h, http.MethodGet, "/nexo/admin/history/obs-1?include_payload=true&limit=1", "admin-token", "")
	items, next, limit := nexoDecodePage(t, rec)
	payload, _ := items[0]["payload"].(map[string]any)
	if len(items) != 1 || payload["title"] != "T2" || next == nil || limit != 1 {
		t.Fatalf("include_payload = %v next=%v", items, next)
	}
	rec = nexoDo(t, h, http.MethodGet, "/nexo/admin/history/obs-1?include_payload=true&limit=1&cursor="+url.QueryEscape(*next), "admin-token", "")
	items, next, _ = nexoDecodePage(t, rec)
	if len(items) != 1 || items[0]["actor"] != "ana" || next != nil {
		t.Fatalf("segunda página del historial = %v next=%v", items, next)
	}

	// CA-36 / RF-36: cada lectura quedó auditada con el admin, filtros e IP.
	var reads int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM cloud_auth_audit_log WHERE action LIKE 'nexo.%.read' AND actor_principal_id = $1::bigint AND outcome = 'success' AND metadata->>'client_ip' = '10.0.0.15'`, env.adminID).Scan(&reads); err != nil {
		t.Fatal(err)
	}
	if reads != 6 {
		t.Fatalf("lecturas auditadas = %d, want 6", reads)
	}
	var presenceReads int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM cloud_auth_audit_log WHERE action = 'nexo.presence.read' AND (metadata->>'result_count')::int >= 2`).Scan(&presenceReads); err != nil {
		t.Fatal(err)
	}
	if presenceReads != 1 {
		t.Fatalf("la consulta de presencia no quedó en la auditoría (CA-36)")
	}
	// Las lecturas son consultables por la propia API.
	rec = nexoDo(t, h, http.MethodGet, "/nexo/admin/audit?action=nexo.*&actor=admin", "admin-token", "")
	items, _, _ = nexoDecodePage(t, rec)
	if len(items) != 6 || items[0]["actor_username"] != "admin" {
		t.Fatalf("auditoría de lecturas = %d %v", len(items), items)
	}
}

// CA-35 por HTTP: con 1.000 eventos, páginas de hasta 200 y cursor para seguir.
func TestNexoAdminAPIPaginatesThousandEvents(t *testing.T) {
	env := newNexoAdminEnv(t)
	if _, err := env.db.Exec(`INSERT INTO cloud_auth_audit_log (actor_source, action, outcome, reason_code) SELECT 'request', 'sync.auth', 'denied', 'unknown_token' FROM generate_series(1, 1000)`); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	path := "/nexo/admin/audit?action=sync.auth&limit=1000"
	pages := 0
	for {
		rec := nexoDo(t, env.h, http.MethodGet, path, "admin-token", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("page %d = %d %s", pages, rec.Code, rec.Body.String())
		}
		items, next, limit := nexoDecodePage(t, rec)
		if limit != 200 || len(items) > 200 {
			t.Fatalf("límite = %d, filas = %d", limit, len(items))
		}
		for _, it := range items {
			id := fmt.Sprint(it["id"])
			if ids[id] {
				t.Fatalf("id repetido %s", id)
			}
			ids[id] = true
		}
		pages++
		if next == nil {
			break
		}
		path = "/nexo/admin/audit?action=sync.auth&limit=1000&cursor=" + url.QueryEscape(*next)
	}
	if len(ids) != 1000 || pages != 5 {
		t.Fatalf("eventos=%d páginas=%d", len(ids), pages)
	}
}
