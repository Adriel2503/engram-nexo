package cloudserver

// Nexo IA · K6 (KP-12) — miembros de un espacio en el panel, de punta a punta
// contra Postgres (CLOUDSTORE_TEST_DSN) con sesiones y tokens reales: listado
// con y sin permiso, agregar, cambiar y quitar, la regla R-10 (cuenta de
// servicio no puede ser responsable), R-9 (último responsable), CSRF, token
// read, escape de HTML y un evento de auditoría por acción.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// kbPanel es una sesión del panel para las pruebas.
type kbPanel struct {
	t      *testing.T
	env    *nexoSecEnv
	cookie *http.Cookie
}

func (e *nexoSecEnv) panel(person string) *kbPanel {
	e.t.Helper()
	return &kbPanel{t: e.t, env: e, cookie: managedDashboardLogin(e.t, e.srv, e.tokens[person], false)}
}

func (p *kbPanel) get(path string) *httptest.ResponseRecorder {
	return performDashboardRequest(p.env.srv, http.MethodGet, path, p.cookie)
}

func (p *kbPanel) post(path string, form url.Values) *httptest.ResponseRecorder {
	return performDashboardForm(p.env.srv, http.MethodPost, path, form.Encode(), p.cookie, false)
}

// csrf devuelve el token CSRF de la sesión (derivado de la cookie, como lo
// emiten los formularios del panel).
func (p *kbPanel) csrf(_ string) string {
	p.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(p.cookie)
	tok := p.env.srv.kbCSRFToken(req)
	if tok == "" {
		p.t.Fatal("sin token CSRF para la sesión")
	}
	return tok
}

func kbForm(csrf string, kv ...string) url.Values {
	v := url.Values{"csrf_token": {csrf}}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

// kbPanelSetup: espacio abierto "arq" con responsable ana y la cuenta de
// servicio ci-wiki (sin rol todavía).
func kbPanelSetup(t *testing.T) *nexoSecEnv {
	t.Helper()
	env := newNexoSecEnv(t, nil)
	if rec := env.req(http.MethodPost, "/nexo/kb/v1/spaces", "carla", `{"slug":"arq","name":"Arquitectura","description":"Estándares","owner":"ana"}`, "10.0.0.5"); rec.Code != http.StatusCreated {
		t.Fatalf("crear espacio: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.req(http.MethodPost, "/nexo/authz/v1/service-accounts", "pedro", `{"slug":"ci-wiki","owner":"ana"}`, "10.0.0.5"); rec.Code != http.StatusCreated {
		t.Fatalf("crear cuenta de servicio: %d %s", rec.Code, rec.Body.String())
	}
	return env
}

func (e *nexoSecEnv) binding(person, scope string) string {
	e.t.Helper()
	var role string
	err := e.db.QueryRow(`SELECT COALESCE((SELECT role FROM nexo_role_bindings WHERE principal_id = $1::bigint AND scope_type = 'space' AND scope_key = $2), '')`, e.ids[person], scope).Scan(&role)
	if err != nil {
		e.t.Fatal(err)
	}
	return role
}

func (e *nexoSecEnv) authzEvents(action, person string) int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM nexo_authz_events WHERE action = $1 AND scope_key = 'arq' AND target_principal_id = $2::bigint`, action, e.ids[person])
}

func TestNexoKBMembersPanel(t *testing.T) {
	env := kbPanelSetup(t)
	const base = "/dashboard/kb/spaces/arq/members"
	ana, luis, pedro := env.panel("ana"), env.panel("luis"), env.panel("pedro")

	// ─── Listado: el responsable ve los controles; el lector, solo la lista ──
	rec := ana.get(base)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Miembros de Arquitectura") || !strings.Contains(body, "Responsables") ||
		!strings.Contains(body, "Agregar miembro") || !strings.Contains(body, "Espacio abierto: quienes no aparecen aquí tienen el rol lector") {
		t.Fatalf("responsable: %d %s", rec.Code, body)
	}
	if !strings.Contains(body, `name="csrf_token" value="`+ana.csrf("")+`"`) {
		t.Fatal("los formularios de miembros llevan el token CSRF de la sesión")
	}
	if !strings.Contains(body, ">Documentos<") || !strings.Contains(body, ">Miembros<") || !strings.Contains(body, "Propuestas") {
		t.Fatalf("secciones del espacio: %s", body)
	}
	rec = luis.get(base)
	body = rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "Agregar miembro") || strings.Contains(body, "Quitar") || strings.Contains(body, "csrf_token") ||
		!strings.Contains(body, "Solo el responsable del espacio y los administradores cambian roles") || !strings.Contains(body, "ana") {
		t.Fatalf("lector ve controles o no ve la lista: %d %s", rec.Code, body)
	}

	// ─── Espacio restringido: sin rol, 404; el administrador lee el directorio ─
	if rec := env.req(http.MethodPost, "/nexo/kb/v1/spaces", "carla", `{"slug":"secreto","name":"Secreto","owner":"ana","visibility":"restricted"}`, "10.0.0.5"); rec.Code != http.StatusCreated {
		t.Fatalf("espacio restringido: %d %s", rec.Code, rec.Body.String())
	}
	if rec := luis.get("/dashboard/kb/spaces/secreto/members"); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "Secreto") {
		t.Fatalf("sin rol en espacio restringido: %d", rec.Code)
	}
	rec = pedro.get("/dashboard/kb/spaces/secreto/members")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Agregar miembro") || !strings.Contains(rec.Body.String(), "ana") {
		t.Fatalf("administrador con lectura de directorio: %d %s", rec.Code, rec.Body.String())
	}
	if rec := luis.get("/dashboard/kb/spaces/no-existe/members"); rec.Code != http.StatusNotFound {
		t.Fatalf("espacio inexistente: %d", rec.Code)
	}

	// ─── Denegaciones: lector, administrador sin rol, sin CSRF, token read ───
	luisCSRF := luis.csrf("/dashboard/kb")
	rec = luis.post(base, kbForm(luisCSRF, "member", "luis", "role", "responsable"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Tu rol o tu token no permiten esta acción") || env.binding("luis", "arq") != "" {
		t.Fatalf("lector se asigna responsable: %d %s", rec.Code, rec.Body.String())
	}
	if n := env.count(`SELECT count(*) FROM nexo_authz_events WHERE action = $1 AND scope_key = 'arq' AND actor_principal_id = $2::bigint`, authz.ActionDenied, env.ids["luis"]); n != 1 {
		t.Fatalf("la denegación del lector queda auditada: %d", n)
	}
	pedroCSRF := pedro.csrf("/dashboard/kb")
	if rec := pedro.post(base, kbForm(pedroCSRF, "member", "luis", "role", "lector")); rec.Code != http.StatusForbidden || env.binding("luis", "arq") != "" {
		t.Fatalf("un administrador sin rol en el espacio no cambia miembros: %d", rec.Code)
	}
	anaCSRF := ana.csrf(base)
	if rec := ana.post(base, url.Values{"member": {"luis"}, "role": {"publicador"}}); rec.Code != http.StatusForbidden || env.binding("luis", "arq") != "" {
		t.Fatalf("sin CSRF: %d", rec.Code)
	}
	if rec := ana.post(base+"/remove", kbForm("token-ajeno", "member", env.ids["ana"])); rec.Code != http.StatusForbidden || env.binding("ana", "arq") != "responsable" {
		t.Fatalf("CSRF inválido al quitar: %d", rec.Code)
	}
	env.issue("ana", "ana-read", security.TokenIssue{Days: 30, Scope: "read"})
	anaRead := env.panel("ana-read")
	if rec := anaRead.post(base, kbForm(anaRead.csrf("/dashboard/kb"), "member", "luis", "role", "publicador")); rec.Code != http.StatusForbidden || env.binding("luis", "arq") != "" {
		t.Fatalf("token read cambia miembros: %d %s", rec.Code, rec.Body.String())
	}
	if rec := anaRead.get(base); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Agregar miembro") {
		t.Fatalf("sesión read ve el formulario de alta: %d", rec.Code)
	}

	// ─── Agregar, cambiar y quitar ───────────────────────────────────────────
	rec = ana.post(base, kbForm(anaCSRF, "member", "luis", "role", "publicador"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Agregamos a luis como publicador") || env.binding("luis", "arq") != "publicador" {
		t.Fatalf("agregar: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Publicadores") || !strings.Contains(rec.Body.String(), `role="status"`) {
		t.Fatalf("el listado muestra al nuevo publicador: %s", rec.Body.String())
	}
	if n := env.authzEvents(authz.ActionBindingGranted, "luis"); n != 1 {
		t.Fatalf("evento binding.granted: %d", n)
	}
	rec = ana.post(base, kbForm(anaCSRF, "member", env.ids["luis"], "role", "proponente"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Cambiamos el rol de "+env.ids["luis"]+" de publicador a proponente") || env.binding("luis", "arq") != "proponente" {
		t.Fatalf("cambiar: %d %s", rec.Code, rec.Body.String())
	}
	if n := env.authzEvents(authz.ActionBindingChanged, "luis"); n != 1 {
		t.Fatalf("evento binding.changed: %d", n)
	}
	rec = ana.post(base, kbForm(anaCSRF, "member", env.ids["luis"], "role", "proponente"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "No hubo cambios") || env.authzEvents(authz.ActionBindingChanged, "luis") != 1 {
		t.Fatalf("mismo rol: %d", rec.Code)
	}
	rec = ana.post(base+"/remove", kbForm(anaCSRF, "member", env.ids["luis"]))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Quitamos a "+env.ids["luis"]+" del espacio") || env.binding("luis", "arq") != "" {
		t.Fatalf("quitar: %d %s", rec.Code, rec.Body.String())
	}
	if n := env.authzEvents(authz.ActionBindingRevoked, "luis"); n != 1 {
		t.Fatalf("evento binding.revoked: %d", n)
	}
	if rec := ana.post(base+"/remove", kbForm(anaCSRF, "member", env.ids["luis"])); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "no tenía un rol asignado") {
		t.Fatalf("quitar a quien no tiene rol: %d", rec.Code)
	}

	// ─── Reglas: R-10, R-9, R-11, usuario inexistente y entradas inválidas ───
	rec = ana.post(base, kbForm(anaCSRF, "member", "ci-wiki", "role", "responsable"))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Una cuenta de servicio no puede ser responsable de un espacio") ||
		!strings.Contains(rec.Body.String(), `role="alert"`) || strings.Contains(rec.Body.String(), "R-10") {
		t.Fatalf("R-10: %d %s", rec.Code, rec.Body.String())
	}
	if n := env.count(`SELECT count(*) FROM nexo_role_bindings b JOIN nexo_service_accounts sa ON sa.principal_id = b.principal_id WHERE sa.slug = 'ci-wiki' AND b.scope_key = 'arq'`); n != 0 {
		t.Fatalf("la cuenta de servicio quedó con rol: %d", n)
	}
	if n := env.count(`SELECT count(*) FROM nexo_authz_events WHERE action = $1 AND scope_key = 'arq' AND details->>'rule' = 'service_account_role'`, authz.ActionDenied); n != 1 {
		t.Fatalf("el rechazo de R-10 queda auditado: %d", n)
	}
	rec = ana.post(base, kbForm(anaCSRF, "member", "ci-wiki", "role", "publicador"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Agregamos a ci-wiki como publicador") || !strings.Contains(rec.Body.String(), "Cuenta de servicio") {
		t.Fatalf("cuenta de servicio como publicador: %d %s", rec.Code, rec.Body.String())
	}
	// La fila de una cuenta de servicio no ofrece el rol responsable.
	row := rec.Body.String()
	i := strings.Index(row, "Cuenta de servicio, con este rol desde")
	if i < 0 {
		t.Fatalf("la fila de la cuenta de servicio no aparece: %s", row)
	}
	if end := strings.Index(row[i:], "</li>"); end < 0 || strings.Contains(row[i:i+end], `value="responsable"`) {
		t.Fatalf("la fila de la cuenta ofrece responsable")
	}
	rec = ana.post(base+"/remove", kbForm(anaCSRF, "member", env.ids["ana"]))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "al menos un responsable activo") || env.binding("ana", "arq") != "responsable" {
		t.Fatalf("R-9: %d %s", rec.Code, rec.Body.String())
	}
	rec = ana.post(base, kbForm(anaCSRF, "member", "nadie", "role", "lector"))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "No encontramos a ese usuario") {
		t.Fatalf("usuario inexistente: %d %s", rec.Code, rec.Body.String())
	}
	if rec := ana.post(base, kbForm(anaCSRF, "member", "", "role", "lector")); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Escribe el usuario") {
		t.Fatalf("usuario vacío: %d", rec.Code)
	}
	if rec := ana.post(base, kbForm(anaCSRF, "member", "luis", "role", "superadmin")); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Elige un rol válido") || env.binding("luis", "arq") != "" {
		t.Fatalf("rol inválido: %d", rec.Code)
	}
	// R-11: dos responsables; uno no se sube más de lo que es (no aplica a
	// responsable) pero sí puede dejar de serlo si hay otro.
	if rec := ana.post(base, kbForm(anaCSRF, "member", "luis", "role", "responsable")); rec.Code != http.StatusOK || env.binding("luis", "arq") != "responsable" {
		t.Fatalf("segundo responsable: %d", rec.Code)
	}
	if rec := ana.post(base, kbForm(anaCSRF, "member", "ana", "role", "lector")); rec.Code != http.StatusOK || env.binding("ana", "arq") != "lector" {
		t.Fatalf("el responsable puede bajarse el rol si hay otro: %d %s", rec.Code, rec.Body.String())
	}
	// Ana ya es lectora: cambiar roles vuelve a estar denegado para ella.
	if rec := ana.post(base, kbForm(anaCSRF, "member", "ana", "role", "responsable")); rec.Code != http.StatusForbidden || env.binding("ana", "arq") != "lector" {
		t.Fatalf("auto-promoción: %d", rec.Code)
	}

	// ─── Los textos de personas se escapan ───────────────────────────────────
	var evaID string
	if err := env.db.QueryRow(`INSERT INTO cloud_principals (kind, display_name, role) VALUES ('human', 'Eva <b>negrita</b>', 'member') RETURNING id::text`).Scan(&evaID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`INSERT INTO cloud_human_users (principal_id, username, email) VALUES ($1::bigint, 'eva', 'eva@rp.pe')`, evaID); err != nil {
		t.Fatal(err)
	}
	luisOwner := env.panel("luis")
	luisCSRF = luisOwner.csrf(base)
	rec = luisOwner.post(base, kbForm(luisCSRF, "member", "eva", "role", "lector"))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "<b>negrita</b>") || !strings.Contains(rec.Body.String(), "&lt;b&gt;negrita") {
		t.Fatalf("escape del nombre: %d", rec.Code)
	}
}

// Un 500 del servicio de roles no muestra el detalle (solo el log).
func TestNexoKBMembersInternalErrorIsGeneric(t *testing.T) {
	s := &CloudServer{}
	status, msg := s.kbMembersErrText(http.ErrAbortHandler)
	if status != http.StatusInternalServerError || strings.Contains(msg, "abort") || !strings.Contains(msg, "error interno") {
		t.Fatalf("error interno: %d %q", status, msg)
	}
}
