package cloudserver

// Nexo IA · SG-07 — pruebas de las pantallas del auditor contra Postgres real
// (helpers de nexo_security_test.go y nexo_evidence_test.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

type auditPanelEnv struct {
	*nexoSecEnv
	cookies map[string]*http.Cookie
	bodies  map[string]string // ruta -> última respuesta (para el buscador de secretos)
}

// newAuditPanelEnv: luis es auditor (con un token read aparte), carla
// superadmin, pedro admin y ana miembro.
func newAuditPanelEnv(t *testing.T) *auditPanelEnv {
	t.Helper()
	env := newNexoSecEnv(t, nil)
	env.makeAuditor("luis")
	env.issue("luis", "luis-read", security.TokenIssue{Days: 30, Scope: "read"})
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "carla", "", "10.91.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("me de carla: %d", rec.Code)
	}
	return &auditPanelEnv{nexoSecEnv: env, cookies: map[string]*http.Cookie{}, bodies: map[string]string{}}
}

func (e *auditPanelEnv) cookie(who string) *http.Cookie {
	e.t.Helper()
	if c, ok := e.cookies[who]; ok {
		return c
	}
	c := managedDashboardLogin(e.t, e.srv, e.tokens[who], false)
	e.cookies[who] = c
	return c
}

func (e *auditPanelEnv) get(who, path string) *httptest.ResponseRecorder {
	e.t.Helper()
	var cookie *http.Cookie
	if who != "" {
		cookie = e.cookie(who)
	}
	rec := performDashboardRequest(e.srv, http.MethodGet, path, cookie)
	e.bodies[who+" "+path] = rec.Body.String()
	return rec
}

func (e *auditPanelEnv) post(who, path, form string) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := performDashboardForm(e.srv, http.MethodPost, path, form, e.cookie(who), false)
	e.bodies["POST "+who+" "+path] = rec.Body.String()
	return rec
}

var auditCSRFRe = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// csrf lee el token CSRF de la página (la sesión de who) y lo deja listo para
// un formulario.
func (e *auditPanelEnv) csrf(who, path string) string {
	e.t.Helper()
	rec := e.get(who, path)
	m := auditCSRFRe.FindStringSubmatch(rec.Body.String())
	if m == nil {
		e.t.Fatalf("%s sin token CSRF (%d): %.300s", path, rec.Code, rec.Body.String())
	}
	return url.QueryEscape(html.UnescapeString(m[1]))
}

func (e *auditPanelEnv) mustBody(who, path string, want int) string {
	e.t.Helper()
	rec := e.get(who, path)
	if rec.Code != want {
		e.t.Fatalf("GET %s como %s = %d, quería %d: %.400s", path, who, rec.Code, want, rec.Body.String())
	}
	return rec.Body.String()
}

// assertNoLeaks: ninguna página contiene un token, su hash ni una cabecera
// Authorization (mismo buscador que S5).
func (e *auditPanelEnv) assertNoLeaks() {
	e.t.Helper()
	scan := newNexoLeakScanner()
	for name, raw := range e.tokens {
		scan.addToken(name, raw, e.hasher.Hash)
	}
	rows, err := e.db.Query(`SELECT token_hash FROM cloud_principal_tokens`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			e.t.Fatal(err)
		}
		scan.addHash("db", h)
	}
	for name, body := range e.bodies {
		if got := scan.find(html.UnescapeString(body)); len(got) > 0 {
			e.t.Errorf("%s filtra secretos: %v", name, got)
		}
	}
}

var auditPagePaths = []string{
	"/dashboard/admin/audit",
	"/dashboard/admin/audit?src=roles",
	"/dashboard/admin/audit/activity",
	"/dashboard/admin/audit/history",
	"/dashboard/admin/audit/integrity",
	"/dashboard/admin/audit/alerts",
	"/dashboard/admin/audit/access-review",
}

// Cada página se dibuja para el auditor, el admin y el superadmin; un miembro
// recibe 403 sin datos y sin pestaña, sin sesión se va al login (negar por
// defecto), y el intento deja un authz.denied.
func TestNexoAuditPanelAccess(t *testing.T) {
	env := newAuditPanelEnv(t)
	titles := map[string]string{
		"/dashboard/admin/audit":               "Registro de auditoría",
		"/dashboard/admin/audit?src=roles":     "Registro de auditoría",
		"/dashboard/admin/audit/activity":      "Actividad y presencia",
		"/dashboard/admin/audit/history":       "Historial de un elemento",
		"/dashboard/admin/audit/integrity":     "Integridad de la evidencia",
		"/dashboard/admin/audit/alerts":        "Alertas de seguridad",
		"/dashboard/admin/audit/access-review": "Revisión de accesos",
	}
	for _, who := range []string{"luis", "luis-read", "carla", "pedro"} {
		for _, path := range auditPagePaths {
			body := env.mustBody(who, path, http.StatusOK)
			for _, want := range []string{titles[path], `lang="es"`, `href="/dashboard/admin/audit"`, `aria-current="page"`, "Revisión de accesos"} {
				if !strings.Contains(body, want) {
					t.Errorf("%s en %s: falta %q", who, path, want)
				}
			}
		}
	}
	// Pestaña "Auditoría" en el menú para el auditor (no es admin de Engram).
	if body := env.mustBody("luis", "/dashboard/admin/audit", http.StatusOK); !strings.Contains(body, `href="/dashboard/admin/audit" aria-current="page"`) || !strings.Contains(body, ">Auditoría</a>") {
		t.Errorf("el menú del auditor no tiene la pestaña Auditoría")
	}
	for _, path := range append(append([]string{}, auditPagePaths...), "/dashboard/admin/audit/history?key=obs-1") {
		rec := env.get("ana", path)
		body := rec.Body.String()
		if rec.Code != http.StatusForbidden || !strings.Contains(body, "Sin acceso a Auditoría") || strings.Contains(body, `href="/dashboard/admin/audit"`) {
			t.Errorf("miembro en %s: %d %.200s", path, rec.Code, body)
		}
		if rec := env.get("", path); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/dashboard/login") {
			t.Errorf("sin sesión en %s: %d", path, rec.Code)
		}
	}
	if n := env.count(`SELECT count(*) FROM nexo_authz_events WHERE action = 'authz.denied' AND actor_principal_id = $1::bigint AND details->>'route' LIKE 'GET /dashboard/admin/audit%'`, env.ids["ana"]); n < len(auditPagePaths) {
		t.Errorf("denegaciones del miembro auditadas: %d", n)
	}
	// Las lecturas quedan en la auditoría con las mismas acciones que la API.
	for _, action := range []string{nexoAuditActionAuditRead, nexoAuditActionAuthzEventsRead, nexoAuditActionActivityRead, nexoAuditActionPresenceRead,
		nexoAuditActionAlertsRead, nexoAuditActionAccessReview} {
		if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = $1 AND actor_principal_id = $2::bigint`, action, env.ids["luis"]); n == 0 {
			t.Errorf("lectura %s del auditor sin registrar", action)
		}
	}
	env.assertNoLeaks()
}

func auditCount(body, needle string) int { return strings.Count(body, needle) }

func (e *auditPanelEnv) insertAudit(action, outcome, project, metadata string) {
	e.t.Helper()
	if _, err := e.db.Exec(`INSERT INTO cloud_auth_audit_log (actor_source, action, outcome, project, metadata) VALUES ('test-seed', $1, $2, NULLIF($3, ''), NULLIF($4, '')::jsonb)`,
		action, outcome, project, metadata); err != nil {
		e.t.Fatal(err)
	}
}

// Filtros de actor, acción, resultado, fecha y ámbito; paginación; roles; el
// detalle no muestra secretos y el historial se enlaza.
func TestNexoAuditPanelLogFilters(t *testing.T) {
	env := newAuditPanelEnv(t)
	// NX-07 / R-6: el texto libre solo se muestra en campos conocidos (proyección explícita); una clave
	// desconocida como "nota" se retiene sin lectura del ámbito. Por eso los textos de esta prueba van en campos conocidos.
	env.insertAudit("x.uno", "success", "proy-a", `{"reason":"hola"}`)
	env.insertAudit("x.dos", "denied", "proy-b", ``)
	env.insertAudit("x.secreto", "success", "", `{"authorization":"Bearer abcdef1234567890","source":"visible","tok":"egc_live_0123abcd_`+strings.Repeat("Ab-9", 11)+`","token_hash":"hmac-sha256:v1:AAAABBBBCCCCDDDD","hash_x":"zz","client_ip":"10.1.1.1","filters":{"password":"p","outcome":"si"}}`)
	env.insertAudit("nexo.history.read", "success", "", `{"filters":{"entity_key":"obs-xyz"},"result_count":2}`)
	base := "/dashboard/admin/audit"

	body := env.mustBody("luis", base+"?action=x.uno", 200)
	if !strings.Contains(body, `<code class="nxa-code">x.uno</code>`) || strings.Contains(body, `>x.dos<`) || !strings.Contains(body, "hola") || !strings.Contains(body, "Quitar filtros") {
		t.Errorf("filtro por acción: %.600s", body)
	}
	if body = env.mustBody("luis", base+"?outcome=denied&action=x.*", 200); !strings.Contains(body, ">x.dos<") || strings.Contains(body, ">x.uno<") || !strings.Contains(body, "Denegado") {
		t.Errorf("filtro por resultado: %.600s", body)
	}
	if body = env.mustBody("luis", base+"?project=PROY-A", 200); !strings.Contains(body, ">x.uno<") || strings.Contains(body, ">x.dos<") {
		t.Errorf("filtro por ámbito: %.600s", body)
	}
	if body = env.mustBody("luis", base+"?actor=test-seed&action=x.*", 200); auditCount(body, `<code class="nxa-code">x.`) != 3 {
		t.Errorf("filtro por actor: %d filas", auditCount(body, `<code class="nxa-code">x.`))
	}
	if body = env.mustBody("luis", base+"?actor=nadie-existe", 200); !strings.Contains(body, "No hay eventos con esos filtros") {
		t.Errorf("vacío con filtros: %.400s", body)
	}
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	if body = env.mustBody("luis", base+"?from="+tomorrow, 200); !strings.Contains(body, "No hay eventos con esos filtros") {
		t.Errorf("rango futuro: %.400s", body)
	}
	today := time.Now().UTC().Format("2006-01-02")
	if body = env.mustBody("luis", base+"?from="+today+"&to="+today+"&action=x.uno", 200); !strings.Contains(body, ">x.uno<") {
		t.Errorf("rango de hoy (incluye el día final): %.400s", body)
	}
	if body = env.mustBody("luis", base+"?from=ayer", http.StatusBadRequest); !strings.Contains(body, "Algún filtro no es válido") {
		t.Errorf("fecha inválida: %.300s", body)
	}
	if body = env.mustBody("luis", base+"?cursor=abc", http.StatusBadRequest); !strings.Contains(body, "no es válido") {
		t.Errorf("cursor inválido: %.300s", body)
	}

	// Detalle: lo seguro se ve, los secretos no.
	body = env.mustBody("luis", base+"?action=x.secreto", 200)
	for _, want := range []string{"source", "visible", "filters", "outcome=si"} {
		if !strings.Contains(body, want) {
			t.Errorf("detalle sin %q: %.800s", want, body)
		}
	}
	for _, bad := range []string{"Bearer", "abcdef1234567890", "egc_live_", "hmac-sha256", "AAAABBBB", "password", "hash_x", "token_hash", "10.1.1.1</dd>"} {
		if strings.Contains(body, bad) {
			t.Errorf("el detalle muestra %q", bad)
		}
	}
	// Una clave de texto libre desconocida se retiene (el auditor no tiene rol en el ámbito).
	env.insertAudit("x.libre", "success", "", `{"nota":"texto-libre-reservado","reason":"motivo-visible"}`)
	if body = env.mustBody("luis", base+"?action=x.libre", 200); strings.Contains(body, "texto-libre-reservado") || !strings.Contains(body, "motivo-visible") {
		t.Errorf("el texto libre desconocido debía retenerse y el campo conocido mostrarse: %.600s", body)
	}
	// Enlace al historial desde una consulta de historial.
	if body = env.mustBody("luis", base+"?action=nexo.history.read", 200); !strings.Contains(body, `href="/dashboard/admin/audit/history?key=obs-xyz"`) {
		t.Errorf("sin enlace al historial: %.600s", body)
	}

	// Paginación por cursor.
	if _, err := env.db.Exec(`INSERT INTO cloud_auth_audit_log (actor_source, action, outcome) SELECT 'test-bulk', 'bulk.n', 'success' FROM generate_series(1, 60)`); err != nil {
		t.Fatal(err)
	}
	page1 := env.mustBody("luis", base+"?action=bulk.*", 200)
	if n := auditCount(page1, `<code class="nxa-code">bulk.n</code>`); n != nexoAuditPanelPageSize || !strings.Contains(page1, "Ver más antiguos") || strings.Contains(page1, "Volver al inicio") {
		t.Fatalf("página 1: %d filas", n)
	}
	m := regexp.MustCompile(`href="([^"]+)" rel="next"`).FindStringSubmatch(page1)
	if m == nil {
		t.Fatal("sin enlace a la página siguiente")
	}
	page2 := env.mustBody("luis", html.UnescapeString(m[1]), 200)
	if n := auditCount(page2, `<code class="nxa-code">bulk.n</code>`); n != 10 || strings.Contains(page2, "Ver más antiguos") || !strings.Contains(page2, "Volver al inicio") {
		t.Fatalf("página 2: %d filas", n)
	}

	// Roles: la denegación de un miembro aparece; los filtros de ámbito funcionan.
	env.req(http.MethodGet, "/nexo/admin/alerts", "ana", "", "10.91.0.7")
	roles := base + "?src=roles"
	if body = env.mustBody("luis", roles+"&action=authz.denied", 200); !strings.Contains(body, `<code class="nxa-code">authz.denied</code>`) || !strings.Contains(body, "Denegado") || !strings.Contains(body, "Acción denegada por falta de permiso") {
		t.Errorf("roles authz.denied: %.600s", body)
	}
	if body = env.mustBody("luis", roles+"&action=authz.denied&scope_type=space", 200); !strings.Contains(body, "No hay eventos con esos filtros") {
		t.Errorf("roles por ámbito: %.400s", body)
	}
	if body = env.mustBody("luis", roles+"&action=authz.denied&scope_type=platform&actor="+env.ids["ana"], 200); !strings.Contains(body, "authz.denied") || strings.Contains(body, "No hay eventos") {
		t.Errorf("roles por ámbito y persona: %.400s", body)
	}
	env.mustBody("luis", roles+"&scope_type=nube", http.StatusBadRequest)
	env.mustBody("luis", roles+"&action=inventada", http.StatusBadRequest)
	env.assertNoLeaks()
}

// Actividad, presencia e historial de un elemento con datos de sincronización.
func TestNexoAuditPanelActivityAndHistory(t *testing.T) {
	env := newAuditPanelEnv(t)
	var tokenID string
	if err := env.db.QueryRow(`SELECT id::text FROM cloud_principal_tokens WHERE principal_id = $1::bigint ORDER BY id LIMIT 1`, env.ids["ana"]).Scan(&tokenID); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		fmt.Sprintf(`INSERT INTO nexo_actor_presence (presence_key, actor, actor_principal_id, actor_token_id, actor_verified, last_ip, last_user_agent, last_project, push_count, pull_count)
			VALUES ('ana|%s', 'ana', '%s', '%s', true, '10.20.30.40', 'engram/3.4 ANA-PC', 'proy-a', 7, 3)`, tokenID, env.ids["ana"], tokenID),
		`INSERT INTO nexo_actor_presence (presence_key, actor, actor_verified, last_ip, last_user_agent, push_count) VALUES ('anon', 'sin-nombre', false, '10.9.9.9', 'curl/8', 1)`,
		fmt.Sprintf(`INSERT INTO nexo_activity_log (actor, actor_principal_id, actor_token_id, actor_verified, operation, project, item_count, client_ip, user_agent)
			VALUES ('ana', '%s', '%s', true, 'push_mutations', 'proy-a', 12, '10.20.30.40', 'engram/3.4 ANA-PC')`, env.ids["ana"], tokenID),
		fmt.Sprintf(`INSERT INTO nexo_activity_log (actor, actor_principal_id, actor_token_id, actor_verified, operation, project, item_count, client_ip, user_agent)
			VALUES ('ana', '%s', '%s', true, 'pull_chunk', 'proy-b', 1, '10.20.30.41', 'engram/3.4 ANA-PC')`, env.ids["ana"], tokenID),
	} {
		if _, err := env.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	base := "/dashboard/admin/audit/activity"
	body := env.mustBody("luis", base, 200)
	for _, want := range []string{"Quién se conectó", "Qué se sincronizó", "10.20.30.40", "engram/3.4 ANA-PC", "Subida de cambios", "Descarga de un bloque", "proy-a", "identidad no verificada", "sin-nombre"} {
		if !strings.Contains(body, want) {
			t.Errorf("actividad sin %q", want)
		}
	}
	if body = env.mustBody("luis", base+"?operation=pull_chunk", 200); strings.Contains(body, "Subida de cambios</td>") || !strings.Contains(body, "Descarga de un bloque") {
		t.Errorf("filtro por operación: %.300s", body)
	}
	if body = env.mustBody("luis", base+"?project=proy-a", 200); strings.Contains(body, "10.20.30.41") {
		t.Errorf("filtro por proyecto")
	}
	if body = env.mustBody("luis", base+"?actor=no-identificado", 200); strings.Contains(body, "10.20.30.41") {
		t.Errorf("filtro por no identificado muestra a ana")
	}
	if body = env.mustBody("luis", base+"?actor=nadie", 200); !strings.Contains(body, "No hay operaciones con esos filtros") || !strings.Contains(body, "No hay conexiones registradas") {
		t.Errorf("vacío: %.300s", body)
	}
	env.mustBody("luis", base+"?operation=borrar", http.StatusBadRequest)
	for _, action := range []string{nexoAuditActionActivityRead, nexoAuditActionPresenceRead} {
		if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = $1 AND actor_principal_id = $2::bigint AND metadata ? 'result_count'`, action, env.ids["luis"]); n == 0 {
			t.Errorf("%s sin auditar", action)
		}
	}

	// Historial: tres versiones de un elemento con autores distintos.
	ctx := context.Background()
	anaCtx := cloudstore.WithMutationActor(ctx, cloudstore.MutationActor{Name: "ana", PrincipalID: env.ids["ana"], TokenID: tokenID, Verified: true, ClientClaim: "ANA-PC"})
	luisCtx := cloudstore.WithMutationActor(ctx, cloudstore.MutationActor{Name: "luis", PrincipalID: env.ids["luis"], Verified: false, ClientClaim: "LUIS-PC"})
	obs := func(title string) cloudstore.MutationEntry {
		payload, _ := json.Marshal(map[string]any{"sync_id": "obs-hist", "session_id": "s1", "type": "decision", "title": title, "content": "CONTENIDO-SECRETO-" + title, "scope": "project", "project": "proy-a"})
		return cloudstore.MutationEntry{Project: "proy-a", Entity: "observation", EntityKey: "obs-hist", Op: "upsert", Payload: payload}
	}
	if _, err := env.cs.InsertMutationBatch(anaCtx, []cloudstore.MutationEntry{obs("Primera decisión")}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.cs.InsertMutationBatch(luisCtx, []cloudstore.MutationEntry{obs("Segunda decisión")}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.cs.InsertMutationBatch(luisCtx, []cloudstore.MutationEntry{{Project: "proy-a", Entity: "observation", EntityKey: "obs-hist", Op: "delete", Payload: json.RawMessage(`{"sync_id":"obs-hist"}`)}}); err != nil {
		t.Fatal(err)
	}
	hist := "/dashboard/admin/audit/history"
	if body = env.mustBody("luis", hist, 200); !strings.Contains(body, "Buscar un elemento") || strings.Contains(body, "Versiones de") {
		t.Errorf("búsqueda sin clave: %.300s", body)
	}
	body = env.mustBody("luis", hist+"?key=obs-hist", 200)
	for _, want := range []string{"Versiones de obs-hist", "Creación o cambio", "Eliminación", "ana", "luis", "ANA-PC", "LUIS-PC", "identidad no verificada"} {
		if !strings.Contains(body, want) {
			t.Errorf("historial sin %q", want)
		}
	}
	if strings.Contains(body, "CONTENIDO-SECRETO") {
		t.Errorf("el historial no debe mostrar el contenido de la memoria")
	}
	if body = env.mustBody("luis", hist+"?key=obs-hist&project=otro", 200); !strings.Contains(body, "No hay versiones registradas") {
		t.Errorf("historial con otro proyecto: %.300s", body)
	}
	if body = env.mustBody("luis", hist+"?key=no-existe", 200); !strings.Contains(body, "No hay versiones registradas") {
		t.Errorf("historial vacío")
	}
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = $1 AND metadata->'filters'->>'entity_key' = 'obs-hist'`, nexoAuditActionHistoryRead); n == 0 {
		t.Errorf("consulta de historial sin auditar")
	}
	env.assertNoLeaks()
}

// La verificación de la cadena: íntegra, y rota después de alterar una fila o
// borrar otra (CA-S09), con las filas exactas.
func TestNexoAuditPanelIntegrity(t *testing.T) {
	env := newAuditPanelEnv(t)
	path := "/dashboard/admin/audit/integrity"
	body := env.mustBody("luis", path, 200)
	if !strings.Contains(body, "Verificar ahora") || strings.Contains(body, "nxa-verdict") && strings.Contains(body, "Comprobado el") {
		t.Errorf("la página no debe verificar sola: %.300s", body)
	}
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = $1`, nexoAuditActionChainVerifyRead); n != 0 {
		t.Errorf("verificó sin pedirlo: %d", n)
	}
	body = env.mustBody("luis-read", path+"?run=1", 200) // un token read puede verificar (es lectura)
	if !strings.Contains(body, "La cadena está íntegra") || !strings.Contains(body, `role="status"`) || strings.Contains(body, "Se detectó") {
		t.Fatalf("cadena íntegra: %.1200s", body)
	}
	if body = env.mustBody("luis", path+"?run=1&table=cloud_auth_audit_log", 200); !strings.Contains(body, "La cadena está íntegra") || auditCount(body, "<tbody>") < 1 || strings.Contains(body, "Eventos de conocimiento</td>") {
		t.Errorf("una sola tabla: %.600s", body)
	}
	env.mustBody("luis", path+"?run=1&table=nexo_kb_versions", http.StatusBadRequest)
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = $1 AND actor_principal_id = $2::bigint`, nexoAuditActionChainVerifyRead, env.ids["luis"]); n != 2 {
		t.Errorf("verificación auditada como la API: %d", n)
	}

	// Alterar el contenido de una fila y borrar otra (con la guarda apagada).
	var altered, deleted int64
	if err := env.db.QueryRow(`SELECT id FROM cloud_auth_audit_log WHERE nexo_chain_seq IS NOT NULL ORDER BY nexo_chain_seq LIMIT 1 OFFSET 1`).Scan(&altered); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT id FROM cloud_auth_audit_log WHERE nexo_chain_seq IS NOT NULL ORDER BY nexo_chain_seq LIMIT 1 OFFSET 4`).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`ALTER TABLE cloud_auth_audit_log DISABLE TRIGGER nexo_guard_cloud_auth_audit_log`,
		fmt.Sprintf(`UPDATE cloud_auth_audit_log SET reason_code = 'alterado' WHERE id = %d`, altered),
		fmt.Sprintf(`DELETE FROM cloud_auth_audit_log WHERE id = %d`, deleted),
		`ALTER TABLE cloud_auth_audit_log ENABLE TRIGGER nexo_guard_cloud_auth_audit_log`,
	} {
		if _, err := env.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	body = env.mustBody("carla", path+"?run=1", 200)
	for _, want := range []string{"Se detectó una alteración", `role="alert"`, "nxa-verdict-bad", "Primera fila alterada",
		fmt.Sprintf("id %d, posición", altered), "Huecos en la cadena", "al inicio|en el medio|al final", "Con problemas", "Qué hacer"} {
		ok := false
		for _, alt := range strings.Split(want, "|") {
			ok = ok || strings.Contains(body, alt)
		}
		if !ok {
			t.Errorf("verificación rota sin %q: %.1500s", want, body)
		}
	}
	if !regexp.MustCompile(`filas alteradas`).MatchString(body) || !regexp.MustCompile(`filas faltantes`).MatchString(body) {
		t.Errorf("el veredicto debe contar filas alteradas y faltantes")
	}
	// Mismo resultado que la API.
	rec := env.req(http.MethodGet, "/nexo/admin/audit/verify", "carla", "", "10.91.0.1")
	var rep evidence.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Tables[0].FirstBroken == nil || rep.Tables[0].FirstBroken.ID != altered || len(rep.Tables[0].Gaps) == 0 {
		t.Fatalf("la API y la pantalla deben coincidir: %s", rec.Body.String())
	}
	gap := rep.Tables[0].Gaps[0]
	if !strings.Contains(body, fmt.Sprintf(`<td class="nxa-num">%d</td>`, gap.FromSeq)) || !strings.Contains(body, fmt.Sprintf(`<td class="nxa-num">%d</td>`, gap.ToSeq)) {
		t.Errorf("el hueco %+v no aparece en la pantalla", gap)
	}
	env.assertNoLeaks()
}

// Alertas: listado con la regla nueva, reconocer con CSRF y los mismos eventos
// de auditoría que la API.
func TestNexoAuditPanelAlerts(t *testing.T) {
	env := newAuditPanelEnv(t)
	ctx := context.Background()
	staleAlert, err := evidence.InsertAlert(ctx, env.db, evidence.NewAlert{Rule: evidence.RuleSAStale, Severity: evidence.SeverityWarning, Layer: evidence.LayerPlatform,
		Subject: "service_account:ci-wiki", Summary: "la cuenta ci-wiki lleva 200 días sin uso", DedupKey: "k1"})
	if err != nil || staleAlert == nil {
		t.Fatal(err)
	}
	burst, err := evidence.InsertAlert(ctx, env.db, evidence.NewAlert{Rule: evidence.RuleAuthzDeniedBurst, Severity: evidence.SeverityHigh, Layer: evidence.LayerPlatform,
		Subject: "principal:" + env.ids["ana"], Summary: "5 denegaciones en un minuto", DedupKey: "k2"})
	if err != nil || burst == nil {
		t.Fatal(err)
	}
	third, err := evidence.InsertAlert(ctx, env.db, evidence.NewAlert{Rule: evidence.RuleKBDocExpiring, Severity: evidence.SeverityInfo, Layer: evidence.LayerKnowledge,
		Subject: "space:arq", Summary: "vence un documento", DedupKey: "k3"})
	if err != nil || third == nil {
		t.Fatal(err)
	}
	path := "/dashboard/admin/audit/alerts"
	body := env.mustBody("luis", path, 200)
	for _, want := range []string{"Cuenta de servicio sin uso", "service_account_stale", "Evento de auditoría: service_account_stale", "Ráfaga de acciones denegadas", "Pendiente", "Reconocer alerta", "Advertencia", "Alta"} {
		if !strings.Contains(body, want) {
			t.Errorf("alertas sin %q", want)
		}
	}
	// Filtros.
	if body = env.mustBody("luis", path+"?rule=service_account_stale", 200); strings.Contains(body, `<code class="nxa-code">authz_denied_burst</code>`) || !strings.Contains(body, `<code class="nxa-code">service_account_stale</code>`) {
		t.Errorf("filtro por regla")
	}
	if body = env.mustBody("luis", path+"?severity=high", 200); strings.Contains(body, `<code class="nxa-code">service_account_stale</code>`) || !strings.Contains(body, `<code class="nxa-code">authz_denied_burst</code>`) {
		t.Errorf("filtro por severidad")
	}
	if body = env.mustBody("luis", path+"?layer=conocimiento", 200); !strings.Contains(body, `<code class="nxa-code">kb_doc_expiring</code>`) || strings.Contains(body, `<code class="nxa-code">authz_denied_burst</code>`) {
		t.Errorf("filtro por capa")
	}
	env.mustBody("luis", path+"?rule=inventada", http.StatusBadRequest)
	env.mustBody("luis", path+"?severity=extrema", http.StatusBadRequest)

	action := fmt.Sprintf("%s/%d/ack", path, staleAlert.ID)
	csrf := env.csrf("luis", path)

	// Sin CSRF, con otro origen, miembro y token read: no se reconoce nada.
	if rec := env.post("luis", action, "note=hola"); rec.Code != http.StatusForbidden {
		t.Fatalf("ack sin CSRF: %d", rec.Code)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, action, strings.NewReader("csrf_token="+csrf))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://otro.example")
	req.AddCookie(env.cookie("luis"))
	env.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ack con otro origen: %d", rec.Code)
	}
	if rec := env.post("ana", action, "csrf_token="+csrf); rec.Code != http.StatusForbidden {
		t.Fatalf("ack de un miembro: %d", rec.Code)
	}
	readBody := env.mustBody("luis-read", path, 200)
	if strings.Contains(readBody, "Reconocer alerta") || !strings.Contains(readBody, "token de escritura") {
		t.Errorf("la sesión read no debe ofrecer reconocer")
	}
	if rec := env.post("luis-read", action, "csrf_token="+env.csrfFor("luis-read")); rec.Code != http.StatusForbidden {
		t.Fatalf("ack con token read: %d", rec.Code)
	}
	if n := env.count(`SELECT count(*) FROM nexo_alert_acks`); n != 0 {
		t.Fatalf("no debía haber reconocimientos: %d", n)
	}

	// Reconocer.
	rec = env.post("luis", action, "csrf_token="+csrf+"&state=pendientes&note=revisado+con+la+persona+responsable")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "ok=ack") {
		t.Fatalf("ack: %d %s", rec.Code, rec.Body.String())
	}
	if n := env.count(`SELECT count(*) FROM nexo_alert_acks WHERE alert_id = $1 AND acknowledged_by_principal_id = $2::bigint AND note = 'revisado con la persona responsable' AND client_ip IS NOT NULL`, staleAlert.ID, env.ids["luis"]); n != 1 {
		t.Fatalf("reconocimiento registrado: %d", n)
	}
	if body = env.mustBody("luis", rec.Header().Get("Location"), 200); !strings.Contains(body, "Alerta reconocida") || strings.Contains(body, `<code class="nxa-code">service_account_stale</code>`) {
		t.Errorf("tras reconocer, la alerta sale de pendientes: %.500s", body)
	}
	if body = env.mustBody("luis", path+"?state=todas", 200); !strings.Contains(body, "Reconocida") || strings.Contains(body, "revisado con la persona responsable") || !strings.Contains(body, "texto no mostrado: requiere rol de lectura") || !strings.Contains(body, `<code class="nxa-code">service_account_stale</code>`) {
		t.Errorf("todas muestra la reconocida y retiene la nota libre (R-6: la alerta no tiene un ámbito que el auditor pueda leer)")
	}
	if rec = env.post("luis", action, "csrf_token="+csrf); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ya fue reconocida") {
		t.Fatalf("segundo ack: %d", rec.Code)
	}
	if rec = env.post("luis", path+"/999999/ack", "csrf_token="+csrf); rec.Code != http.StatusNotFound {
		t.Fatalf("ack inexistente: %d", rec.Code)
	}
	if rec = env.post("luis", path+"/abc/ack", "csrf_token="+csrf); rec.Code != http.StatusNotFound {
		t.Fatalf("ack con id inválido: %d", rec.Code)
	}

	// Mismos eventos que la API: otra alerta por la API, mismo registro.
	if rec := env.req(http.MethodPost, fmt.Sprintf("/nexo/admin/alerts/%d:ack", burst.ID), "luis", `{"note":"por la API"}`, "10.91.0.9"); rec.Code != http.StatusOK {
		t.Fatalf("ack por la API: %d %s", rec.Code, rec.Body.String())
	}
	rows, err := env.db.Query(`SELECT metadata::text, actor_principal_id::text, outcome, project IS NULL FROM cloud_auth_audit_log WHERE action = $1 ORDER BY id`, nexoAuditActionAlertAck)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var metas []map[string]any
	for rows.Next() {
		var meta, actor, outcome string
		var noProject bool
		if err := rows.Scan(&meta, &actor, &outcome, &noProject); err != nil {
			t.Fatal(err)
		}
		if actor != env.ids["luis"] || outcome != "success" || !noProject {
			t.Errorf("evento de ack: actor=%s outcome=%s", actor, outcome)
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(meta), &m); err != nil {
			t.Fatal(err)
		}
		delete(m, cloudstore.NexoAuditMetaClientIP)
		delete(m, cloudstore.NexoAuditMetaUserAgent)
		metas = append(metas, m)
	}
	if len(metas) != 2 {
		t.Fatalf("eventos nexo.alert.ack = %d, quería 2 (panel y API)", len(metas))
	}
	if len(metas[0]) != len(metas[1]) || fmt.Sprint(metas[0]["alert_id"]) != fmt.Sprint(float64(staleAlert.ID)) || fmt.Sprint(metas[1]["alert_id"]) != fmt.Sprint(float64(burst.ID)) {
		t.Errorf("la metadata del panel y de la API deben tener la misma forma: %v %v", metas[0], metas[1])
	}
	env.assertNoLeaks()
}

// Revisión de accesos: el reporte, las marcas, registrar la revisión (mismos
// eventos que la API), la última revisión con fecha y autor y las negativas.
func TestNexoAuditPanelAccessReview(t *testing.T) {
	env := newAuditPanelEnv(t)
	if _, err := env.db.Exec(`UPDATE cloud_principal_tokens SET nexo_expires_at = NULL WHERE principal_id = $1::bigint`, env.ids["ci"]); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`UPDATE cloud_principal_tokens SET created_at = now() - interval '200 days', last_used_at = now() - interval '120 days' WHERE principal_id = $1::bigint`, env.ids["pedro"]); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`UPDATE cloud_principals SET created_at = now() - interval '300 days' WHERE id IN ($1::bigint, $2::bigint)`, env.ids["pedro"], env.ids["ci"]); err != nil {
		t.Fatal(err)
	}
	path := "/dashboard/admin/audit/access-review"
	body := env.mustBody("luis", path, 200)
	for _, want := range []string{"Última revisión registrada", "Todavía no se registró ninguna revisión", "Marcar revisión como hecha", "Personas y cuentas", "Token sin caducidad", "Inactiva",
		"Tokens nunca usados", "carla", "pedro", "ana", "superadmin", "Solo con marcas", "Cuentas de servicio sin uso", "NEXO_SA_STALE_DAYS", "(cuenta de servicio)"} {
		if !strings.Contains(body, want) {
			t.Errorf("revisión sin %q", want)
		}
	}
	if body = env.mustBody("luis", path+"?inactive_days=30&only=flags", 200); !strings.Contains(body, `value="30"`) || !strings.Contains(body, `aria-current="page">Solo con marcas`) {
		t.Errorf("umbral y filtro de marcas")
	}
	env.mustBody("luis", path+"?inactive_days=0", http.StatusBadRequest)
	env.mustBody("luis", path+"?inactive_days=abc", http.StatusBadRequest)

	complete := path + "/complete"
	csrf := env.csrf("luis", path)
	if rec := env.post("luis", complete, "notes=sin+csrf"); rec.Code != http.StatusForbidden {
		t.Fatalf("completar sin CSRF: %d", rec.Code)
	}
	if rec := env.post("ana", complete, "csrf_token="+csrf); rec.Code != http.StatusForbidden {
		t.Fatalf("completar como miembro: %d", rec.Code)
	}
	readBody := env.mustBody("luis-read", path, 200)
	if strings.Contains(readBody, "Marcar revisión como hecha") || !strings.Contains(readBody, "token de escritura") {
		t.Errorf("la sesión read no debe ofrecer registrar")
	}
	if rec := env.post("luis-read", complete, "csrf_token="+env.csrfFor("luis-read")); rec.Code != http.StatusForbidden {
		t.Fatalf("completar con token read: %d", rec.Code)
	}
	if rec := env.post("luis", complete, "csrf_token="+csrf+"&inactive_days=99999"); rec.Code != http.StatusBadRequest {
		t.Fatalf("días inválidos: %d", rec.Code)
	}
	if n := env.count(`SELECT count(*) FROM nexo_access_reviews`); n != 0 {
		t.Fatalf("no debía haber revisiones: %d", n)
	}

	rec := env.post("luis", complete, "csrf_token="+csrf+"&inactive_days=90&notes=Q4+2026%3A+2+tokens+rotados")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "ok=review") {
		t.Fatalf("completar: %d %s", rec.Code, rec.Body.String())
	}
	if n := env.count(`SELECT count(*) FROM nexo_access_reviews WHERE reviewer_principal_id = $1::bigint AND notes = 'Q4 2026: 2 tokens rotados' AND client_ip IS NOT NULL AND length(report_sha256) = 64`, env.ids["luis"]); n != 1 {
		t.Fatalf("revisión registrada: %d", n)
	}
	body = env.mustBody("luis", rec.Header().Get("Location"), 200)
	today := time.Now().UTC().Format("2006-01-02")
	for _, want := range []string{"Revisión registrada con tu nombre", "hecha por luis", today, "el texto no se muestra aquí", "Lo que se revisó entonces", "hace un momento"} {
		if !strings.Contains(body, want) {
			t.Errorf("última revisión sin %q: %.900s", want, body)
		}
	}
	if strings.Contains(body, "Q4 2026: 2 tokens rotados") {
		t.Errorf("NX-07: la nota libre de una revisión no sale en el panel")
	}
	if strings.Contains(body, "Todavía no se registró") {
		t.Errorf("sigue diciendo que no hay revisión")
	}

	// Mismos eventos que la API.
	if rec := env.req(http.MethodPost, "/nexo/admin/access-review:complete", "luis", `{"notes":"por la API","inactive_days":90}`, "10.91.0.9"); rec.Code != http.StatusCreated {
		t.Fatalf("completar por la API: %d %s", rec.Code, rec.Body.String())
	}
	rows, err := env.db.Query(`SELECT metadata::text, actor_principal_id::text, outcome FROM cloud_auth_audit_log WHERE action = $1 ORDER BY id`, nexoAuditActionReviewComplete)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var shapes []string
	for rows.Next() {
		var meta, actor, outcome string
		if err := rows.Scan(&meta, &actor, &outcome); err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(meta), &m); err != nil {
			t.Fatal(err)
		}
		if actor != env.ids["luis"] || outcome != "success" || m["review_id"] == nil || len(fmt.Sprint(m["report_sha256"])) != 64 {
			t.Errorf("evento de revisión: %s actor=%s", meta, actor)
		}
		delete(m, cloudstore.NexoAuditMetaClientIP)
		delete(m, cloudstore.NexoAuditMetaUserAgent)
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		shapes = append(shapes, strings.Join(keys, ","))
	}
	if len(shapes) != 2 || len(shapes[0]) != len(shapes[1]) {
		t.Fatalf("eventos nexo.access_review.complete del panel y de la API: %v", shapes)
	}
	// El reporte del panel dice lo mismo que la API.
	rep := env.req(http.MethodGet, "/nexo/admin/access-review", "luis", "", "10.91.0.9")
	var review evidence.AccessReview
	if err := json.Unmarshal(rep.Body.Bytes(), &review); err != nil || review.LastReview == nil || review.LastReview.Reviewer == "" {
		t.Fatalf("reporte de la API: %v", err)
	}
	env.assertNoLeaks()
}

// csrfFor calcula el token CSRF de la sesión de who (para probar un POST de
// una sesión cuya página no trae formulario).
func (e *auditPanelEnv) csrfFor(who string) string {
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit", nil)
	req.AddCookie(e.cookie(who))
	return url.QueryEscape(e.srv.kbCSRFToken(req))
}
