package cloudserver

// Pruebas de la unificación de roles de plataforma (NX-05, docs/nexo/roles/
// design.md §11) contra Postgres: un solo rol (el de Nexo), la columna de
// Engram derivada, un solo punto de decisión y el panel de Usuarios.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

// ─── Utilidades ──────────────────────────────────────────────────────────────

// platformState devuelve el rol de Nexo (miembro si no hay asignación) y la
// columna de Engram de una persona.
func (e *nexoSecEnv) platformState(person string) (nexoRole, engramRole string) {
	e.t.Helper()
	if err := e.db.QueryRow(`
		SELECT COALESCE((SELECT b.role FROM nexo_role_bindings b WHERE b.principal_id = p.id AND b.scope_type = 'platform' AND b.scope_key = '*'), 'miembro'), p.role
		FROM cloud_principals p WHERE p.id = $1::bigint`, e.ids[person]).Scan(&nexoRole, &engramRole); err != nil {
		e.t.Fatal(err)
	}
	return nexoRole, engramRole
}

func (e *nexoSecEnv) wantState(person, nexoRole, engramRole string) {
	e.t.Helper()
	gotN, gotE := e.platformState(person)
	if gotN != nexoRole || gotE != engramRole {
		e.t.Fatalf("%s: rol de Nexo %q (esperaba %q), columna de Engram %q (esperaba %q)", person, gotN, nexoRole, gotE, engramRole)
	}
}

func (e *nexoSecEnv) panelCookie(who string) *http.Cookie {
	e.t.Helper()
	return managedDashboardLogin(e.t, e.srv, e.tokens[who], false)
}

func (e *nexoSecEnv) csrf(cookie *http.Cookie) string {
	req := httptest.NewRequest(http.MethodGet, "/dashboard/", nil)
	req.AddCookie(cookie)
	return e.srv.kbCSRFToken(req)
}

func (e *nexoSecEnv) postRole(cookie *http.Cookie, person, role string, confirm bool, withCSRF bool) *httptest.ResponseRecorder {
	form := url.Values{"role": {role}}
	if confirm {
		form.Set("confirm", "1")
	}
	if withCSRF {
		form.Set("csrf_token", e.csrf(cookie))
	}
	return performDashboardForm(e.srv, http.MethodPost, "/dashboard/admin/users/"+e.ids[person]+"/role", form.Encode(), cookie, false)
}

func (e *nexoSecEnv) eventCount(action, person string) int {
	return e.count(`SELECT count(*) FROM nexo_authz_events WHERE action = $1 AND scope_type = 'platform' AND target_principal_id = $2::bigint`, action, e.ids[person])
}

// ─── Coherencia al crear (API y panel) ───────────────────────────────────────

func TestNexoPlatformRoleCreateKeepsBothColumnsCoherent(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	cases := []struct{ role, nexo, engram string }{
		{"member", "miembro", "member"}, // valor histórico de Engram
		{"miembro", "miembro", "member"},
		{"auditor", "auditor", "member"},
		{"admin", "admin", "admin"},
		{"superadmin", "superadmin", "admin"},
	}
	for i, c := range cases {
		// API de administración de Engram.
		name := "api" + string(rune('a'+i))
		rec := env.req(http.MethodPost, "/admin/users", "carla", `{"username":"`+name+`","role":"`+c.role+`"}`, "10.20.0.1")
		if rec.Code != http.StatusCreated {
			t.Fatalf("API %s: %d %s", c.role, rec.Code, rec.Body.String())
		}
		resp := nexoJSON(t, rec)
		if resp["platform_role"] != c.nexo || resp["role"] != c.engram {
			t.Fatalf("API %s: respuesta platform_role=%v role=%v, want %s/%s", c.role, resp["platform_role"], resp["role"], c.nexo, c.engram)
		}
		env.ids[name] = env.idOf(name)
		env.wantState(name, c.nexo, c.engram)
		if c.nexo != "miembro" && env.eventCount("binding.granted", name) != 1 {
			t.Fatalf("API %s: falta el evento binding.granted", c.role)
		}
		// Panel.
		pname := "pan" + string(rune('a'+i))
		cookie := env.panelCookie("carla")
		form := url.Values{"username": {pname}, "role": {c.role}, "csrf_token": {env.csrf(cookie)}}
		if p := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/users", form.Encode(), cookie, false); p.Code != http.StatusSeeOther {
			t.Fatalf("panel %s: %d %s", c.role, p.Code, p.Body.String())
		}
		env.ids[pname] = env.idOf(pname)
		env.wantState(pname, c.nexo, c.engram)
	}
	// Un rol que no existe se rechaza y no crea nada.
	before := env.count(`SELECT count(*) FROM cloud_principals`)
	if rec := env.req(http.MethodPost, "/admin/users", "carla", `{"username":"raro","role":"root"}`, "10.20.0.1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("rol inválido: %d %s", rec.Code, rec.Body.String())
	}
	if env.count(`SELECT count(*) FROM cloud_principals`) != before {
		t.Fatal("un rol inválido no crea personas")
	}
}

func (e *nexoSecEnv) idOf(username string) string {
	e.t.Helper()
	var id string
	if err := e.db.QueryRow(`SELECT principal_id::text FROM cloud_human_users WHERE username = $1`, username).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// ─── Un admin solo crea miembros (el servidor decide) ────────────────────────

func TestNexoPlatformRoleAdminOnlyCreatesMembers(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	cookie := env.panelCookie("pedro")
	for _, role := range []string{"auditor", "admin", "superadmin"} {
		before := env.count(`SELECT count(*) FROM cloud_principals`)
		bindings := env.count(`SELECT count(*) FROM nexo_role_bindings`)
		if rec := env.req(http.MethodPost, "/admin/users", "pedro", `{"username":"x`+role+`","role":"`+role+`"}`, "10.20.0.2"); rec.Code != http.StatusForbidden {
			t.Fatalf("API %s por un admin: %d %s", role, rec.Code, rec.Body.String())
		}
		form := url.Values{"username": {"y" + role}, "role": {role}, "csrf_token": {env.csrf(cookie)}}
		if rec := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/users", form.Encode(), cookie, false); rec.Code != http.StatusForbidden {
			t.Fatalf("panel %s por un admin: %d %s", role, rec.Code, rec.Body.String())
		}
		if env.count(`SELECT count(*) FROM cloud_principals`) != before || env.count(`SELECT count(*) FROM nexo_role_bindings`) != bindings {
			t.Fatalf("%s: la denegación no cambia nada", role)
		}
	}
	// La denegación queda en la auditoría de Nexo.
	if n := env.count(`SELECT count(*) FROM nexo_authz_events WHERE action = 'authz.denied' AND details->>'capability' = 'platform.admins.manage'`); n < 6 {
		t.Fatalf("denegaciones auditadas = %d", n)
	}
	// Un miembro sí.
	form := url.Values{"username": {"nuevo"}, "role": {"miembro"}, "csrf_token": {env.csrf(cookie)}}
	if rec := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/users", form.Encode(), cookie, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("miembro por un admin: %d %s", rec.Code, rec.Body.String())
	}
	env.ids["nuevo"] = env.idOf("nuevo")
	env.wantState("nuevo", "miembro", "member")
	// Un rol que no es miembro sin token CSRF se rechaza aunque quien lo pida pueda asignarlo.
	form = url.Values{"username": {"sincsrf"}, "role": {"auditor"}}
	if rec := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/users", form.Encode(), env.panelCookie("carla"), false); rec.Code != http.StatusForbidden {
		t.Fatalf("sin CSRF: %d", rec.Code)
	}
}

// ─── Cambiar el rol desde el detalle (confirmación, CSRF, 403, 409) ──────────

func TestNexoPlatformRoleChangeFromPanel(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	carla := env.panelCookie("carla")

	// El detalle muestra el rol de Nexo en español y el formulario de cambio.
	page := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/admin/users/"+env.ids["ana"], carla)
	body := page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, "Rol de plataforma") || !strings.Contains(body, "Miembro") || !strings.Contains(body, "/role") {
		t.Fatalf("detalle: %d", page.Code)
	}
	if strings.Contains(body, ">admin<") || strings.Contains(body, "Managed Users") {
		t.Fatalf("el detalle no muestra la columna de Engram a secas")
	}

	// Primer POST: pide confirmación y no cambia nada.
	conf := env.postRole(carla, "ana", "auditor", false, true)
	if conf.Code != http.StatusOK || !strings.Contains(conf.Body.String(), "Confirmar cambio de rol") || !strings.Contains(conf.Body.String(), `name="confirm" value="1"`) {
		t.Fatalf("confirmación: %d %s", conf.Code, conf.Body.String())
	}
	env.wantState("ana", "miembro", "member")
	// Sin CSRF: 403 y nada cambia.
	if rec := env.postRole(carla, "ana", "auditor", true, false); rec.Code != http.StatusForbidden {
		t.Fatalf("sin CSRF: %d", rec.Code)
	}
	env.wantState("ana", "miembro", "member")
	// Confirmado: cambia el rol de Nexo y la columna acompaña, con evento.
	for _, step := range []struct{ role, nexo, engram, action string }{
		{"auditor", "auditor", "member", "binding.granted"},
		{"admin", "admin", "admin", "binding.changed"},
		{"superadmin", "superadmin", "admin", "binding.changed"},
		{"member", "miembro", "member", "binding.revoked"},
	} {
		rec := env.postRole(carla, "ana", step.role, true, true)
		if rec.Code != http.StatusSeeOther || !strings.Contains(dashboardRedirect(rec), "rol=actualizado") {
			t.Fatalf("%s: %d %s", step.role, rec.Code, rec.Body.String())
		}
		env.wantState("ana", step.nexo, step.engram)
		if env.eventCount(step.action, "ana") == 0 {
			t.Fatalf("%s: falta el evento %s", step.role, step.action)
		}
	}
	var actor, before, after string
	if err := env.db.QueryRow(`SELECT actor_label, role_before, role_after FROM nexo_authz_events WHERE target_principal_id = $1::bigint AND action = 'binding.revoked'`, env.ids["ana"]).Scan(&actor, &before, &after); err != nil || actor != "carla" || before != "superadmin" || after != "miembro" {
		t.Fatalf("evento de auditoría: %q %q %q (%v)", actor, before, after, err)
	}
	// Mismo rol: sin cambios, sin evento nuevo.
	events := env.eventCount("binding.revoked", "ana")
	if rec := env.postRole(carla, "ana", "miembro", true, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "No hubo cambios") {
		t.Fatalf("sin cambios: %d", rec.Code)
	}
	if env.eventCount("binding.revoked", "ana") != events {
		t.Fatal("sin cambios no hay evento")
	}
	// Rol inválido y persona inexistente.
	if rec := env.postRole(carla, "ana", "root", true, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("rol inválido: %d", rec.Code)
	}
	env.ids["fantasma"] = "999999"
	if rec := env.postRole(carla, "fantasma", "admin", true, true); rec.Code != http.StatusNotFound {
		t.Fatalf("persona inexistente: %d", rec.Code)
	}
}

func TestNexoPlatformRoleChangeDeniedAndLastSuperadmin(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	env.withProjectAuthorizer(nil)
	pedro := env.panelCookie("pedro") // admin
	ana := env.panelCookie("ana")     // miembro
	carla := env.panelCookie("carla") // único superadmin

	// Un admin no cambia roles ni se promueve a sí mismo (403, sin cambios).
	for _, c := range []struct {
		cookie       *http.Cookie
		person, role string
	}{
		{pedro, "ana", "auditor"}, {pedro, "pedro", "superadmin"}, {pedro, "luis", "admin"}, {ana, "ana", "admin"},
	} {
		if rec := env.postRole(c.cookie, c.person, c.role, true, true); rec.Code != http.StatusForbidden {
			t.Fatalf("%s -> %s: %d %s", c.person, c.role, rec.Code, rec.Body.String())
		}
	}
	env.wantState("ana", "miembro", "member")
	env.wantState("pedro", "admin", "admin")
	env.wantState("luis", "miembro", "member")
	// Por la API también.
	if rec := env.req(http.MethodPut, "/nexo/authz/v1/platform/members/pedro", "pedro", `{"role":"superadmin"}`, "10.20.0.3"); rec.Code != http.StatusForbidden {
		t.Fatalf("auto-promoción por la API: %d %s", rec.Code, rec.Body.String())
	}
	env.wantState("pedro", "admin", "admin")

	// El último superadmin no se degrada: 409 y nada cambia.
	rec := env.postRole(carla, "carla", "admin", true, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "último superadmin") {
		t.Fatalf("último superadmin: %d %s", rec.Code, rec.Body.String())
	}
	env.wantState("carla", "superadmin", "admin")
	if rec := env.req(http.MethodPut, "/nexo/authz/v1/platform/members/carla", "carla", `{"role":"miembro"}`, "10.20.0.3"); rec.Code != http.StatusConflict {
		t.Fatalf("último superadmin por la API: %d", rec.Code)
	}

	// Con otro superadmin, carla puede bajarse: la lleva al inicio (ya no administra).
	if rec := env.postRole(carla, "pedro", "superadmin", true, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("ascenso por la superadmin: %d %s", rec.Code, rec.Body.String())
	}
	env.wantState("pedro", "superadmin", "admin")
	rec = env.postRole(carla, "carla", "miembro", true, true)
	if rec.Code != http.StatusSeeOther || dashboardRedirect(rec) != "/dashboard/" {
		t.Fatalf("auto-degradación: %d %q", rec.Code, dashboardRedirect(rec))
	}
	env.wantState("carla", "miembro", "member")
	// Y deja de administrar de inmediato.
	if page := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/admin/users", carla); page.Code != http.StatusForbidden {
		t.Fatalf("carla ya no administra: %d", page.Code)
	}
}

// ─── Las rutas de Engram que miraban la columna deciden con el motor de Nexo ──

func TestNexoEngramAdminRoutesDecideThroughNexo(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	env.withProjectAuthorizer(nil)
	// Desalineaciones fabricadas a propósito (alguien evitó los triggers):
	// luis "admin" en la columna sin asignación y pedro (admin de Nexo)
	// "member" en la columna.
	for _, q := range []string{
		`ALTER TABLE cloud_principals DISABLE TRIGGER nexo_principal_role_derive`,
		`UPDATE cloud_principals SET role = 'admin' WHERE id = ` + env.ids["luis"],
		`UPDATE cloud_principals SET role = 'member' WHERE id = ` + env.ids["pedro"],
		`ALTER TABLE cloud_principals ENABLE TRIGGER nexo_principal_role_derive`,
	} {
		if _, err := env.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	env.wantState("luis", "miembro", "admin")
	env.wantState("pedro", "admin", "member")

	// La columna no concede nada: luis no administra en ninguna ruta.
	luis := env.panelCookie("luis")
	for _, p := range []struct{ method, path, body string }{
		{http.MethodGet, "/admin/users", ""},
		{http.MethodPost, "/admin/users", `{"username":"zz"}`},
		{http.MethodGet, "/admin/users/" + env.ids["ana"] + "/tokens", ""},
	} {
		if rec := env.req(p.method, p.path, "luis", p.body, "10.20.0.4"); rec.Code != http.StatusForbidden {
			t.Fatalf("luis %s %s: %d %s", p.method, p.path, rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{"/dashboard/admin", "/dashboard/admin/users", "/dashboard/admin/users/" + env.ids["ana"], "/dashboard/admin/projects", "/dashboard/admin/health", "/dashboard/admin/audit-log"} {
		if rec := performDashboardRequest(env.srv, http.MethodGet, path, luis); rec.Code != http.StatusForbidden {
			t.Fatalf("luis panel %s: %d", path, rec.Code)
		}
	}
	// La asignación de Nexo sí: pedro administra aunque la columna diga member.
	pedro := env.panelCookie("pedro")
	for _, path := range []string{"/dashboard/admin", "/dashboard/admin/users", "/dashboard/admin/users/" + env.ids["ana"], "/dashboard/admin/projects"} {
		if rec := performDashboardRequest(env.srv, http.MethodGet, path, pedro); rec.Code != http.StatusOK {
			t.Fatalf("pedro panel %s: %d", path, rec.Code)
		}
	}
	if rec := env.req(http.MethodGet, "/admin/users", "pedro", "", "10.20.0.4"); rec.Code != http.StatusOK {
		t.Fatalf("pedro API: %d", rec.Code)
	}
	// Un auditor lee el directorio y no cambia nada (decisión de Nexo, igual que antes).
	if _, err := env.db.Exec(`INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role) VALUES ($1::bigint, 'platform', '*', 'auditor')`, env.ids["ana"]); err != nil {
		t.Fatal(err)
	}
	if rec := env.req(http.MethodGet, "/admin/users", "ana", "", "10.20.0.4"); rec.Code != http.StatusOK {
		t.Fatalf("auditor lee el directorio: %d", rec.Code)
	}
	if rec := env.req(http.MethodPost, "/admin/users", "ana", `{"username":"zz"}`, "10.20.0.4"); rec.Code != http.StatusForbidden {
		t.Fatalf("auditor no crea: %d", rec.Code)
	}
}

// ─── Token heredado ENGRAM_CLOUD_ADMIN: como hoy ─────────────────────────────

func TestNexoLegacyAdminTokenKeepsDocumentedBehavior(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	// Servidor como lo arma el token heredado ENGRAM_CLOUD_ADMIN (sin resolvedor de tokens asignados).
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	authSvc.SetBearerToken("sync-token")
	authSvc.SetDashboardSessionTokens([]string{"legacy-admin-token-e2e"})
	srv := New(env.cs, authSvc, 0, WithAdminIdentityStore(env.cs), WithPrincipalProjectAuthorizer(managedGrantAuthorizer{}), WithDashboardAdminToken("legacy-admin-token-e2e"))
	login := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader("token=legacy-admin-token-e2e"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(login, loginReq)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("login con el token heredado: %d %s", login.Code, login.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range login.Result().Cookies() {
		if c.Name == dashboardSessionCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("sin cookie de sesión")
	}
	// Ve las pantallas de administración (solo lectura), como hoy.
	for _, path := range []string{"/dashboard/admin", "/dashboard/admin/users", "/dashboard/admin/users/" + env.ids["ana"]} {
		if rec := performDashboardRequest(srv, http.MethodGet, path, cookie); rec.Code != http.StatusOK {
			t.Fatalf("token heredado en %s: %d", path, rec.Code)
		}
	}
	// No administra usuarios (requireManagedAdmin exige un token asignado) ni cambia roles.
	form := url.Values{"username": {"viaheredado"}, "role": {"miembro"}}
	if rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users", form.Encode(), cookie, false); rec.Code != http.StatusForbidden {
		t.Fatalf("el token heredado no crea personas: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/dashboard/", nil)
	req.AddCookie(cookie)
	form = url.Values{"role": {"superadmin"}, "confirm": {"1"}, "csrf_token": {srv.kbCSRFToken(req)}}
	if rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/"+env.ids["ana"]+"/role", form.Encode(), cookie, false); rec.Code != http.StatusForbidden {
		t.Fatalf("el token heredado no cambia roles: %d", rec.Code)
	}
	env.wantState("ana", "miembro", "member")
	if env.count(`SELECT count(*) FROM cloud_human_users WHERE username = 'viaheredado'`) != 0 {
		t.Fatal("no se creó la persona")
	}
}

// ─── Cabecera y lista ────────────────────────────────────────────────────────

func TestNexoPlatformRoleShownInHeaderAndList(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	env.withProjectAuthorizer(nil)
	for who, label := range map[string]string{"carla": "Superadmin", "pedro": "Administrador", "ana": "Miembro"} {
		page := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/", env.panelCookie(who))
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `<span class="user-name">`+who+`</span>`) ||
			!strings.Contains(page.Body.String(), `nx-chip`) || !strings.Contains(page.Body.String(), ">"+label+"</span>") {
			t.Fatalf("%s: cabecera sin el rol %q (%d)", who, label, page.Code)
		}
	}
	// Lista: rol de Nexo por persona, sin la palabra "admin" a secas de Engram.
	if _, err := env.db.Exec(`INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role) VALUES ($1::bigint, 'platform', '*', 'auditor')`, env.ids["luis"]); err != nil {
		t.Fatal(err)
	}
	list := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/admin/users/list", env.panelCookie("carla"))
	body := list.Body.String()
	if list.Code != http.StatusOK {
		t.Fatalf("lista: %d", list.Code)
	}
	for _, want := range []string{"Superadmin", "Administrador", "Auditor", "Miembro", "Rol de plataforma"} {
		if !strings.Contains(body, want) {
			t.Fatalf("la lista no muestra %q", want)
		}
	}
	// El formulario de alta ofrece los cuatro roles; a un admin solo le deja elegir miembro.
	create := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/admin/users", env.panelCookie("pedro")).Body.String()
	for _, v := range []string{`value="miembro"`, `value="auditor"`, `value="admin"`, `value="superadmin"`} {
		if !strings.Contains(create, v) {
			t.Fatalf("el alta no ofrece %s", v)
		}
	}
	if strings.Count(create, " disabled") < 3 {
		t.Fatalf("a un admin la interfaz le desactiva auditor, admin y superadmin")
	}
	superCreate := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/admin/users", env.panelCookie("carla")).Body.String()
	if strings.Contains(superCreate, `name="role" value="superadmin" disabled`) {
		t.Fatal("a la superadmin no se le desactiva ningún rol")
	}
}

// ─── La sincronización de proyectos no cambia ────────────────────────────────

func TestNexoPlatformRoleChangeDoesNotTouchProjectGrants(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	env.withProjectAuthorizer(map[string][]string{"ana": {"nexo-transversal"}})
	if _, err := env.db.Exec(`INSERT INTO cloud_project_grants (principal_id, project) VALUES ($1::bigint, 'nexo-transversal')`, env.ids["ana"]); err != nil {
		t.Fatal(err)
	}
	before := env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0&project=nexo-transversal", "ana", "", "10.20.0.5").Code
	carla := env.panelCookie("carla")
	for _, role := range []string{"admin", "auditor", "miembro"} {
		if rec := env.postRole(carla, "ana", role, true, true); rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: %d", role, rec.Code)
		}
		if got := env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0&project=nexo-transversal", "ana", "", "10.20.0.5").Code; got != before {
			t.Fatalf("la sincronización de ana cambió con su rol de plataforma (%s): %d -> %d", role, before, got)
		}
		if env.count(`SELECT count(*) FROM cloud_project_grants WHERE principal_id = $1::bigint AND project = 'nexo-transversal'`, env.ids["ana"]) != 1 {
			t.Fatal("el permiso de proyecto de Engram no cambia")
		}
	}
	// Quien no tiene el permiso de proyecto no lee ese proyecto, sea cual sea su rol de plataforma (lectura de manifiesto).
	if rec := env.req(http.MethodGet, "/sync/pull?project=nexo-transversal", "pedro", "", "10.20.0.5"); rec.Code != http.StatusForbidden {
		t.Fatalf("un admin sin permiso de proyecto no lee el proyecto: %d", rec.Code)
	}
}

// withProjectAuthorizer vuelve a armar el servidor con permisos de proyecto de
// Engram (los de la tabla de prueba, por id de persona). El panel de Engram
// los necesita para dibujar páginas que leen proyectos.
func (e *nexoSecEnv) withProjectAuthorizer(grants map[string][]string) {
	e.t.Helper()
	byID := map[string][]string{}
	for person, projects := range grants {
		byID[e.ids[person]] = projects
	}
	resolver := cloudauth.NewPrincipalResolver(cloudauth.ResolverConfig{Hasher: e.hasher, ManagedTokens: nexoTestTokenLookup{cs: e.cs}, Legacy: cloudauth.LegacyCredentials{SyncToken: "shared-sync-token-e2e"}})
	e.srv = New(e.cs, nexoRealAuth{r: resolver}, 0, WithAdminIdentityStore(e.cs), WithManagedTokenHasher(e.hasher), WithPrincipalStateStore(e.cs),
		WithPrincipalProjectAuthorizer(managedGrantAuthorizer{grants: byID}))
	e.h = e.srv.Handler()
}
