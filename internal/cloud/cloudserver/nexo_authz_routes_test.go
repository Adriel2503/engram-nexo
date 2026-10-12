package cloudserver

// Nexo IA · NX-05 — tests HTTP de la API /nexo/authz/v1 y de los enganches en
// la administración de Engram (/admin/*, panel y /nexo/admin/*), contra
// Postgres real (CLOUDSTORE_TEST_DSN). Criterios CA-R01, CA-R02, CA-R03,
// CA-R04, CA-R09 y CA-R10 de docs/nexo/roles/requirements.md §8.

import (
	"context"
	"database/sql"
	"encoding/json"
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

type nexoAuthzEnv struct {
	t   *testing.T
	cs  *cloudstore.CloudStore
	db  *sql.DB
	srv *CloudServer
	h   http.Handler
	ids map[string]string
}

// newNexoAuthzEnv: carla (primer admin de Engram: superadmin de arranque),
// pedro, sofia, luis, ana y marta (miembros) y la cuenta de servicio ci.
// Cada uno tiene el token "<nombre>-token".
func newNexoAuthzEnv(t *testing.T) *nexoAuthzEnv {
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
	schema := fmt.Sprintf("cloudserver_authz_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	cs, err := cloudstore.New(cloud.Config{DSN: dsn + sep + "search_path=" + schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	env := &nexoAuthzEnv{t: t, cs: cs, db: cs.NexoDB(), ids: map[string]string{}}
	principals := map[string]cloudauth.Principal{}
	seed := []struct{ name, kind, role string }{
		{"carla", "human", "admin"}, {"pedro", "human", "member"}, {"sofia", "human", "member"},
		{"luis", "human", "member"}, {"ana", "human", "member"}, {"marta", "human", "member"},
		{"ci", "service_account", "member"},
	}
	for i, p := range seed {
		var id, tok string
		if err := env.db.QueryRow(`INSERT INTO cloud_principals (kind, display_name, role) VALUES ($1, $2, $3) RETURNING id::text`, p.kind, p.name, p.role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if p.kind == "human" {
			if _, err := env.db.Exec(`INSERT INTO cloud_human_users (principal_id, username, email) VALUES ($1::bigint, $2, $3)`, id, p.name, p.name+"@rp.pe"); err != nil {
				t.Fatal(err)
			}
		}
		if err := env.db.QueryRow(`INSERT INTO cloud_principal_tokens (principal_id, token_prefix, token_hash, name) VALUES ($1::bigint, $2, $3, $4) RETURNING id::text`,
			id, fmt.Sprintf("egc_test_%d", i), "hash-"+p.name, "laptop-"+p.name).Scan(&tok); err != nil {
			t.Fatal(err)
		}
		env.ids[p.name] = id
		principals[p.name+"-token"] = cloudauth.Principal{ID: id, Kind: cloudauth.PrincipalKind(p.kind), DisplayName: p.name, Role: cloudauth.Role(p.role),
			Enabled: true, Source: cloudauth.PrincipalSourceManagedToken, TokenID: tok}
	}
	principals["shared-token"] = cloudauth.Principal{ID: "legacy:sync", Kind: cloudauth.PrincipalKindLegacy, DisplayName: "LEGACY_SYNC", Role: cloudauth.RoleMember, Enabled: true, Source: cloudauth.PrincipalSourceLegacyEnvSync}
	hasher, err := cloudauth.NewManagedTokenHasher([]byte("test-token-pepper-at-least-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	env.srv = New(cs, resolvingAuth{principals: principals}, 0, WithAdminIdentityStore(cs), WithManagedTokenHasher(hasher))
	env.h = env.srv.Handler()
	return env
}

func (e *nexoAuthzEnv) do(method, path, who, body string, want int) *httptest.ResponseRecorder {
	e.t.Helper()
	token := ""
	if who != "" {
		token = who + "-token"
	}
	rec := nexoDo(e.t, e.h, method, path, token, body)
	if want != 0 && rec.Code != want {
		e.t.Fatalf("%s %s como %q = %d, want %d: %s", method, path, who, rec.Code, want, rec.Body.String())
	}
	return rec
}

func (e *nexoAuthzEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return v
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return decodeJSON[map[string]any](t, rec)["error_code"].(string)
}

const authzV1 = "/nexo/authz/v1"

func TestNexoAuthzPlatformEndToEnd(t *testing.T) {
	env := newNexoAuthzEnv(t)
	ids := env.ids

	// R-15: sin token 401; token compartido 403.
	env.do(http.MethodGet, authzV1+"/me", "", "", http.StatusUnauthorized)
	env.do(http.MethodGet, authzV1+"/me", "shared", "", http.StatusForbidden)

	// CA-R01: la usuaria de bootstrap es superadmin y crea a Pedro como admin.
	me := decodeJSON[map[string]any](t, env.do(http.MethodGet, authzV1+"/me", "carla", "", http.StatusOK))
	if me["platform"].(map[string]any)["role"] != "superadmin" || me["token_scope"] != "write" {
		t.Fatalf("me carla: %v", me)
	}
	change := decodeJSON[map[string]any](t, env.do(http.MethodPut, authzV1+"/platform/members/"+ids["pedro"], "carla", `{"role":"admin"}`, http.StatusOK))
	if change["role_before"] != "miembro" || change["role_after"] != "admin" || change["changed"] != true {
		t.Fatalf("CA-R01: %v", change)
	}
	env.do(http.MethodPost, "/admin/users", "carla", `{"username":"jefe","role":"admin"}`, http.StatusCreated)

	// CA-R02: Pedro crea a Luis2 como miembro, pero no crea ni promueve admins
	// por la API Nexo, ni por /admin/*, ni por el panel.
	env.do(http.MethodPost, "/admin/users", "pedro", `{"username":"luis2","role":"member"}`, http.StatusCreated)
	rec := env.do(http.MethodPost, "/admin/users", "pedro", `{"username":"intruso","role":"admin"}`, http.StatusForbidden)
	if !strings.Contains(rec.Body.String(), "platform.admins.manage") {
		t.Fatalf("mensaje: %s", rec.Body.String())
	}
	env.do(http.MethodPut, authzV1+"/platform/members/"+ids["luis"], "pedro", `{"role":"admin"}`, http.StatusForbidden)
	env.do(http.MethodPut, authzV1+"/platform/members/"+ids["pedro"], "pedro", `{"role":"superadmin"}`, http.StatusForbidden)
	env.do(http.MethodPost, "/admin/users/"+ids["carla"]+"/disable", "pedro", "", http.StatusForbidden)
	env.do(http.MethodPost, "/admin/users/"+ids["carla"]+"/tokens", "pedro", `{"name":"robado"}`, http.StatusForbidden)
	env.do(http.MethodPost, "/admin/users/"+ids["luis"]+"/tokens", "pedro", `{"name":"laptop-luis-2"}`, http.StatusCreated)
	env.do(http.MethodPost, "/admin/users/"+ids["luis"]+"/disable", "pedro", "", http.StatusOK)
	env.do(http.MethodPost, "/admin/users/"+ids["luis"]+"/enable", "pedro", "", http.StatusOK)
	var carlaTok string
	if err := env.db.QueryRow(`SELECT id::text FROM cloud_principal_tokens WHERE principal_id = $1::bigint`, ids["carla"]).Scan(&carlaTok); err != nil {
		t.Fatal(err)
	}
	env.do(http.MethodPost, "/admin/tokens/"+carlaTok+"/revoke", "pedro", `{"reason":"x"}`, http.StatusForbidden)
	if n := env.count(`SELECT COUNT(*) FROM cloud_human_users WHERE username = 'intruso'`); n != 0 {
		t.Fatal("CA-R02: no se creó el admin")
	}
	if n := env.count(`SELECT COUNT(*) FROM cloud_principal_tokens WHERE principal_id = $1::bigint`, ids["carla"]); n != 1 {
		t.Fatal("CA-R02: no se emitió token para la superadmin")
	}
	// Panel.
	cookie := managedDashboardLogin(t, env.srv, "pedro-token", false)
	if rec := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/users", "username=intruso2&role=admin", cookie, false); rec.Code != http.StatusForbidden {
		t.Fatalf("CA-R02 panel crear admin = %d %s", rec.Code, rec.Body.String())
	}
	if rec := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/users/"+ids["carla"]+"/disable", "", cookie, false); rec.Code != http.StatusForbidden {
		t.Fatalf("CA-R02 panel deshabilitar superadmin = %d", rec.Code)
	}
	if rec := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/users/"+ids["carla"]+"/tokens", "name=robado", cookie, false); rec.Code != http.StatusForbidden {
		t.Fatalf("CA-R02 panel token de superadmin = %d", rec.Code)
	}
	if rec := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/users", "username=miembro-panel&role=member", cookie, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("panel crear miembro = %d %s", rec.Code, rec.Body.String())
	}
	if n := env.count(`SELECT COUNT(*) FROM cloud_human_users WHERE username IN ('intruso2')`); n != 0 {
		t.Fatal("CA-R02 panel: no se creó el admin")
	}

	// CA-R03: no se degrada ni deshabilita a la última superadmin (API Nexo,
	// /admin/* y panel).
	if rec := env.do(http.MethodPut, authzV1+"/platform/members/"+ids["carla"], "carla", `{"role":"admin"}`, http.StatusConflict); errorCode(t, rec) != "last_superadmin" {
		t.Fatal(rec.Body.String())
	}
	env.do(http.MethodDelete, authzV1+"/platform/members/"+ids["carla"], "carla", "", http.StatusConflict)
	if rec := env.do(http.MethodPost, "/admin/users/"+ids["carla"]+"/disable", "carla", "", http.StatusConflict); errorCode(t, rec) != "last_superadmin" {
		t.Fatal(rec.Body.String())
	}
	carlaCookie := managedDashboardLogin(t, env.srv, "carla-token", false)
	if rec := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/users/"+ids["carla"]+"/disable", "", carlaCookie, false); rec.Code != http.StatusConflict {
		t.Fatalf("CA-R03 panel = %d", rec.Code)
	}
	// La superadmin sí deshabilita y habilita a un admin.
	env.do(http.MethodPost, "/admin/users/"+ids["pedro"]+"/disable", "carla", "", http.StatusOK)
	env.do(http.MethodPost, "/admin/users/"+ids["pedro"]+"/enable", "carla", "", http.StatusOK)

	// CA-R04: Sofía (auditora) lee la auditoría global y las asignaciones;
	// cualquier escritura le da 403.
	env.do(http.MethodPut, authzV1+"/platform/members/sofia", "carla", `{"role":"auditor"}`, http.StatusOK)
	for _, path := range []string{"/nexo/admin/audit", "/nexo/admin/activity", "/nexo/admin/presence", "/nexo/admin/history/obs-1", "/nexo/admin/authz-events", "/admin/users", "/admin/users/" + ids["luis"] + "/grants", authzV1 + "/platform/members", authzV1 + "/events"} {
		env.do(http.MethodGet, path, "sofia", "", http.StatusOK)
	}
	for _, w := range []struct{ method, path, body string }{
		{http.MethodPost, "/admin/users", `{"username":"x","role":"member"}`},
		{http.MethodPost, "/admin/users/" + ids["luis"] + "/disable", ""},
		{http.MethodPost, "/admin/users/" + ids["luis"] + "/tokens", `{"name":"x"}`},
		{http.MethodPost, "/admin/users/" + ids["luis"] + "/grants", `{"project":"alpha"}`},
		{http.MethodGet, "/admin/users/" + ids["luis"] + "/tokens", ""},
		{http.MethodPut, authzV1 + "/platform/members/" + ids["luis"], `{"role":"auditor"}`},
		{http.MethodPost, authzV1 + "/scopes", `{"type":"space","key":"x-space","owner":"luis"}`},
	} {
		env.do(w.method, w.path, "sofia", w.body, http.StatusForbidden)
	}
	// Un miembro no entra a la administración.
	env.do(http.MethodGet, "/admin/users", "luis", "", http.StatusForbidden)
	env.do(http.MethodGet, "/nexo/admin/audit", "luis", "", http.StatusForbidden)
	env.do(http.MethodGet, authzV1+"/events", "luis", "", http.StatusForbidden)
	env.do(http.MethodGet, authzV1+"/me", "luis", "", http.StatusOK)

	// CA-R10: asignaciones, cambios y denegaciones en la auditoría con actor,
	// objetivo, antes, después, token, IP y cliente.
	page := decodeJSON[nexoPage[map[string]any]](t, env.do(http.MethodGet, authzV1+"/events?action=authz.denied&principal=pedro", "sofia", "", http.StatusOK))
	routes := map[string]bool{}
	for _, it := range page.Items {
		d, _ := it["details"].(map[string]any)
		route := fmt.Sprint(d["route"])
		routes[route] = true
		if it["actor"] != "pedro" || it["client_ip"] == "" || it["actor_token_id"] == "" || it["actor_email"] != "pedro@rp.pe" || it["actor_verified"] != true || d["capability"] == nil {
			t.Fatalf("denegación incompleta: %v", it)
		}
		// La API usa nexoDo (IP y cliente fijos); el panel, httptest por defecto.
		if strings.HasPrefix(route, "POST /admin/") && (it["client_ip"] != "10.0.0.15" || it["user_agent"] != "Go-http-client/1.1") {
			t.Fatalf("origen de la denegación: %v", it)
		}
	}
	for _, r := range []string{"POST /admin/users", "POST /admin/users/{principalID}/disable", "POST /dashboard/admin/users", "POST /admin/tokens/{tokenID}/revoke"} {
		if !routes[r] {
			t.Fatalf("CA-R10: falta la denegación de %s en %v", r, routes)
		}
	}
	page = decodeJSON[nexoPage[map[string]any]](t, env.do(http.MethodGet, "/nexo/admin/authz-events?scope_type=platform&action=binding.*", "carla", "", http.StatusOK))
	found := false
	for _, it := range page.Items {
		if it["action"] == "binding.granted" && it["target_principal_id"] == ids["sofia"] && it["role_before"] == "miembro" && it["role_after"] == "auditor" && it["actor"] == "carla" {
			found = true
		}
	}
	if !found {
		t.Fatalf("CA-R10: falta la asignación de Sofía: %v", page.Items)
	}
	if n := env.count(`SELECT COUNT(*) FROM cloud_auth_audit_log WHERE action = 'nexo.authz_events.read'`); n < 2 {
		t.Fatalf("la lectura de eventos queda auditada (RF-36): %d", n)
	}
	env.do(http.MethodGet, authzV1+"/events?action=nope", "carla", "", http.StatusBadRequest)
	env.do(http.MethodGet, authzV1+"/events?cursor=abc", "carla", "", http.StatusBadRequest)
}

func TestNexoAuthzScopesEndToEnd(t *testing.T) {
	env := newNexoAuthzEnv(t)
	ids := env.ids
	env.do(http.MethodPut, authzV1+"/platform/members/pedro", "carla", `{"role":"admin"}`, http.StatusOK)

	// Validaciones y reglas al crear (400 / 403 / 409 / 422).
	env.do(http.MethodPost, authzV1+"/scopes", "pedro", `{"type":"space","key":"Mal Slug!","owner":"ana"}`, http.StatusBadRequest)
	env.do(http.MethodPost, authzV1+"/scopes", "pedro", `{"type":"platform","key":"*","owner":"ana"}`, http.StatusBadRequest)
	env.do(http.MethodPost, authzV1+"/scopes", "pedro", `{"type":"space","key":"general"}`, http.StatusBadRequest)
	env.do(http.MethodPost, authzV1+"/scopes", "pedro", `{"type":"space","key":"general","owner":"pedro"}`, http.StatusForbidden)
	env.do(http.MethodPost, authzV1+"/scopes", "pedro", `{"type":"space","key":"general","owner":"`+ids["ci"]+`"}`, http.StatusUnprocessableEntity)
	env.do(http.MethodPost, authzV1+"/scopes", "pedro", `{"type":"space","key":"general","owner":"nadie"}`, http.StatusNotFound)
	env.do(http.MethodPost, authzV1+"/scopes", "pedro", `{"type":"space","key":"general","owner":"ana","extra":1}`, http.StatusBadRequest)
	info := decodeJSON[map[string]any](t, env.do(http.MethodPost, authzV1+"/scopes", "pedro", `{"type":"space","key":"lineamientos-frontend","owner":"ana"}`, http.StatusCreated))
	if info["visibility"] != "open" || info["default_role"] != "lector" {
		t.Fatalf("R-5: %v", info)
	}
	env.do(http.MethodPost, authzV1+"/scopes", "pedro", `{"type":"space","key":"lineamientos-frontend","owner":"ana"}`, http.StatusConflict)
	env.do(http.MethodPost, authzV1+"/scopes", "pedro", `{"type":"space","key":"seguridad","visibility":"restricted","owner":"marta"}`, http.StatusCreated)

	lf := authzV1 + "/scopes/space/lineamientos-frontend"
	sec := authzV1 + "/scopes/space/seguridad"
	// Ana (responsable) gestiona miembros; la CI publicadora, nunca responsable.
	env.do(http.MethodPut, lf+"/members/"+ids["ci"], "ana", `{"role":"publicador"}`, http.StatusOK)
	env.do(http.MethodPut, lf+"/members/"+ids["ci"], "ana", `{"role":"responsable"}`, http.StatusUnprocessableEntity)
	env.do(http.MethodPut, lf+"/members/luis", "ana", `{"role":"jefe"}`, http.StatusBadRequest)
	env.do(http.MethodPut, lf+"/members/luis", "luis", `{"role":"publicador"}`, http.StatusForbidden) // lector implícito
	env.do(http.MethodDelete, lf+"/members/ana", "ana", "", http.StatusConflict)                      // R-9
	members := decodeJSON[nexoScopeMembersPage](t, env.do(http.MethodGet, lf+"/members", "luis", "", http.StatusOK))
	if len(members.Items) != 2 || members.Scope.Visibility != "open" {
		t.Fatalf("miembros: %+v", members)
	}
	// Rol implícito proponente (CA-R06 vía API).
	env.do(http.MethodPatch, lf, "ana", `{"default_role":"proponente"}`, http.StatusOK)
	env.do(http.MethodPatch, lf, "ana", `{"default_role":"publicador"}`, http.StatusBadRequest)
	env.do(http.MethodPatch, lf, "luis", `{"visibility":"restricted"}`, http.StatusForbidden)
	me := decodeJSON[map[string]any](t, env.do(http.MethodGet, authzV1+"/me", "luis", "", http.StatusOK))
	spaces := me["spaces"].([]any)
	if len(spaces) != 1 || spaces[0].(map[string]any)["role"] != "proponente" {
		t.Fatalf("me luis: %v", me)
	}

	// CA-R09: ser admin o superadmin no da acceso a un espacio restringido;
	// para quien no tiene rol ni directorio, 404.
	env.do(http.MethodGet, sec+"/members", "luis", "", http.StatusNotFound)
	env.do(http.MethodPatch, sec, "luis", `{"visibility":"open"}`, http.StatusNotFound)
	env.do(http.MethodPut, sec+"/members/luis", "pedro", `{"role":"lector"}`, http.StatusNotFound)
	env.do(http.MethodPut, sec+"/members/pedro", "carla", `{"role":"publicador"}`, http.StatusNotFound)
	env.do(http.MethodGet, sec+"/members", "pedro", "", http.StatusOK) // directorio (RF-06)
	for _, who := range []string{"pedro", "carla"} {
		me := decodeJSON[map[string]any](t, env.do(http.MethodGet, authzV1+"/me", who, "", http.StatusOK))
		for _, s := range me["spaces"].([]any) {
			if s.(map[string]any)["scope"].(map[string]any)["key"] == "seguridad" {
				t.Fatalf("CA-R09: %s ve seguridad", who)
			}
		}
	}
	env.do(http.MethodGet, authzV1+"/scopes/space/no-existe/members", "pedro", "", http.StatusNotFound)

	// Archivar y restaurar (plataforma); recuperar (solo superadmin, R-9).
	env.do(http.MethodPost, sec+":archive", "ana", "", http.StatusForbidden)
	env.do(http.MethodPost, sec+":archive", "pedro", "", http.StatusOK)
	env.do(http.MethodPost, sec+":restore", "pedro", "", http.StatusOK)
	env.do(http.MethodPost, sec+":explode", "pedro", "", http.StatusNotFound)
	env.do(http.MethodPost, sec+":recover", "pedro", `{"owner":"ana"}`, http.StatusForbidden)
	env.do(http.MethodPost, sec+":recover", "carla", `{"owner":"ana"}`, http.StatusConflict) // ya tiene responsable
	env.do(http.MethodPost, "/admin/users/"+ids["marta"]+"/disable", "pedro", "", http.StatusOK)
	rec := decodeJSON[map[string]any](t, env.do(http.MethodPost, sec+":recover", "carla", `{"owner":"ana"}`, http.StatusOK))
	if rec["role_after"] != "responsable" {
		t.Fatalf("recuperación: %v", rec)
	}

	// Proyecto con compatibilidad de Engram: el permiso es publicador.
	env.do(http.MethodPost, "/admin/users/"+ids["luis"]+"/grants", "pedro", `{"project":"MS-Factura"}`, http.StatusCreated)
	me = decodeJSON[map[string]any](t, env.do(http.MethodGet, authzV1+"/me", "luis", "", http.StatusOK))
	projects := me["projects"].([]any)
	if len(projects) != 1 || projects[0].(map[string]any)["role"] != "publicador" || projects[0].(map[string]any)["source"] != "engram_grant" {
		t.Fatalf("compatibilidad: %v", projects)
	}
	env.do(http.MethodPut, authzV1+"/scopes/project/ms-factura/members/luis", "carla", `{"role":"lector"}`, http.StatusNotFound) // sin configurar y sin rol
	env.do(http.MethodPost, authzV1+"/scopes", "pedro", `{"type":"project","key":"MS-Factura","owner":"ana"}`, http.StatusCreated)
	env.do(http.MethodPut, authzV1+"/scopes/project/ms-factura/members/luis", "ana", `{"role":"proponente"}`, http.StatusOK)
	pm := decodeJSON[nexoScopeMembersPage](t, env.do(http.MethodGet, authzV1+"/scopes/project/ms-factura/members?limit=1", "ana", "", http.StatusOK))
	if len(pm.Items) != 1 || pm.NextCursor == nil || pm.Scope.Visibility != "restricted" {
		t.Fatalf("paginación de miembros: %+v", pm)
	}
	pm = decodeJSON[nexoScopeMembersPage](t, env.do(http.MethodGet, authzV1+"/scopes/project/ms-factura/members?limit=1&cursor="+*pm.NextCursor, "ana", "", http.StatusOK))
	if len(pm.Items) != 1 || pm.Items[0].Role != "responsable" || pm.NextCursor != nil {
		t.Fatalf("página 2: %+v", pm)
	}
}

// Sin el almacén Postgres de Nexo (fakes de Engram) los enganches no
// intervienen: rige el comportamiento original de Engram.
func TestNexoAuthzHooksAreInertWithoutNexoStore(t *testing.T) {
	admin := cloudauth.Principal{ID: "p-admin", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleAdmin, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	store := newAdminTestStore()
	srv := adminHandlerTestServer(t, admin, store)
	if rec := performAdminRequest(srv, http.MethodPost, "/admin/users", `{"username":"otro","role":"admin"}`); rec.Code != http.StatusCreated {
		t.Fatalf("sin almacén Nexo: %d %s", rec.Code, rec.Body.String())
	}
	if rec := performAdminRequest(srv, http.MethodGet, authzV1+"/me", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("API de roles sin almacén = %d", rec.Code)
	}
}
