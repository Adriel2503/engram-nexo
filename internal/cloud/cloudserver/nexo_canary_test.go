package cloudserver

// NX-07 · prueba CANARIO de extremo a extremo (Postgres real).
//
// Cierra el patrón, no la lista: en vez de comprobar campo por campo, escribe
// una credencial de token (egc_), un código de activación (nxa_) y un texto
// reservado de documento en TODOS los campos de texto libre que el sistema
// acepta (descubiertos recorriendo los INSERT/UPDATE de cloudstore y de los
// almacenes y los handlers; ver la lista de escenarios) y recorre TODAS las
// salidas: respuestas de la API, páginas del panel, exportación diaria,
// exportación de la CLI, exportación de la revisión de accesos, cuerpo del
// webhook y el registro del servidor.
//
// Reglas que comprueba:
//
//  1. Ninguna credencial (egc_ ni nxa_) sale en ninguna respuesta, a nadie,
//     tampoco a lectores con rol sobre el espacio.
//  2. El texto reservado de cada campo no sale a lectores sin rol sobre el
//     ámbito (auditor, administrador, superadmin) en ninguna salida, salvo los
//     campos de identidad que un auditor ve por diseño (nombres de personas,
//     de tokens y de cuentas de servicio, motivos de revocación, User-Agent y
//     declaración del cliente: allowedAudit abajo, cada uno con su motivo).
//  3. Cobertura: toda columna de texto de las tablas del esquema está
//     clasificada en canaryColumns. Una columna nueva hace fallar la prueba
//     hasta que se agregue aquí (con su escenario de escritura o con el motivo
//     por el que no admite texto externo). Toda ruta GET registrada está en la
//     lista de salidas o en canaryUncheckedRoutes con su motivo.
//
// Cada escenario se ejecuta una vez con texto reservado en todos sus campos y
// una vez por campo con la credencial en ese campo (así se prueba también la
// validación que rechaza la escritura).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

// allowedAudit: campos de identidad que un auditor ve por diseño (la auditoría existe para saber quién,
// con qué token y desde qué cliente). Siempre salen sin credenciales; el texto reservado puede aparecer.
var allowedAudit = map[string]string{
	"username":         "usuario de la persona: directorio de plataforma",
	"email":            "correo de la persona: directorio de plataforma",
	"display_name":     "nombre de la persona o de la cuenta de servicio: directorio de plataforma",
	"token_name":       "nombre del token: lista de tokens y auditoría de emisión",
	"revoke_reason":    "motivo de revocación de un token escrito por quien administra",
	"sa_description":   "descripción de la cuenta de servicio: directorio de plataforma (platform.directory.read)",
	"sa_slug":          "nombre corto de la cuenta de servicio: identificador",
	"user_agent":       "User-Agent del cliente: dato de la conexión registrada",
	"client_claim":     "declaración del cliente: dato de la conexión registrada",
	"actor_label":      "nombre de quien actuó",
	"project":          "nombre del proyecto: identificador de ámbito",
	"space_slug":       "identificador del espacio",
	"review_note":      "nota de la revisión de accesos: no se entrega (note_withheld), ver prueba propia",
	"doc_key":          "clave del documento: identificador definido por el productor; los eventos y alertas de conocimiento la conservan (riesgo abierto 4 del informe)",
	"fm_id":            "id del front matter: pasa a ser la clave del documento",
	"proposal_doc_key": "clave propuesta del documento: identificador",
	"path":             "ruta del archivo: de ella sale la clave del documento",
	"sa_display_name":  "nombre de la cuenta de servicio: directorio de plataforma",
	"sa_description_2": "descripción de la cuenta de servicio: directorio de plataforma",
	"sa_token_name":    "nombre del token de la cuenta de servicio",
	"sa_revoke_reason": "motivo de revocación de un token de cuenta de servicio",
	"activation_name":  "nombre del token que se emitirá al canjear el código: lista de pendientes de quien administra",
	"grant_project":    "nombre del proyecto de un permiso: identificador de ámbito",
	"claim_name":       "nombre con que la PC declaró al actor: dato de la conexión",
	"presence_project": "último proyecto de una conexión: dato de la conexión registrada",
}

type canary struct {
	t         *testing.T
	e         *auditPanelEnv
	tok       string // credencial de token con formato válido, generada al vuelo
	code      string // código de activación, generado al vuelo
	rnd       string
	tags      map[string]string // etiqueta -> texto reservado de ese campo
	n         int
	statuses  map[string]int
	seen      map[string]bool
	ok        map[string]int  // escrituras aceptadas (2xx) por ruta
	roleSeen  map[string]bool // etiquetas que vio algún lector con rol (la prueba no es vacía)
	anySeen   map[string]bool // etiquetas que alguna salida mostró
	firstFail []string
	logs      *nexoSyncBuffer
}

func newCanary(t *testing.T) *canary {
	t.Helper()
	env := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off", security.AuthLockoutEnv: "off"})
	env.makeAuditor("luis")
	env.issue("luis", "luis-read", security.TokenIssue{Days: 30, Scope: "read"})
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "carla", "", "10.99.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("me de carla: %d", rec.Code)
	}
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	h := hex.EncodeToString(raw)
	code, err := security.GenerateActivationCode()
	if err != nil {
		t.Fatal(err)
	}
	c := &canary{t: t, tok: "egc_live_" + h[:8] + "_" + strings.ReplaceAll(h[8:], "0", "Q") + "ZZZZZZZZ", code: code, rnd: h[:10], tags: map[string]string{}, statuses: map[string]int{}, seen: map[string]bool{}, ok: map[string]int{}, roleSeen: map[string]bool{}, anySeen: map[string]bool{}}
	c.e = &auditPanelEnv{nexoSecEnv: env, cookies: map[string]*http.Cookie{}, bodies: map[string]string{}}
	c.logs = nexoCaptureLogs(t)
	return c
}

// reserved devuelve el texto reservado del campo tag.
func (c *canary) reserved(tag string) string {
	v := "RESERVADO-" + tag + "-" + c.rnd
	c.tags[tag] = v
	return v
}

// cred devuelve un valor con las dos credenciales para el campo tag.
func (c *canary) cred(tag string) string {
	c.tags[tag] = "RESERVADO-" + tag + "-" + c.rnd
	return "cred-" + tag + " " + c.tok + " " + c.code
}

var canaryRe = regexp.MustCompile(`RESERVADO-([a-z0-9_]+)-`)

// canaryIDRe reconoce los identificadores variables de una ruta (uuid, números y los espacios y cuentas de la prueba).
var canaryIDRe = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f-]{27}|\b[0-9]+\b|\b(?:cn|cs|ci)-[0-9a-z-]+`)

func (c *canary) uniq(prefix string) string {
	c.n++
	return prefix + strconv.Itoa(c.n) + "-" + c.rnd[:4]
}

// req envía una petición como who y registra el estado (diagnóstico).
func (c *canary) req(who, method, path, body string) *httpRecorder {
	c.t.Helper()
	rec := c.e.req(method, path, who, body, "10.99.1."+strconv.Itoa(1+c.n%200))
	c.statuses[method+" "+path] = rec.Code
	if rec.Code < 300 {
		c.ok[method+" "+canaryIDRe.ReplaceAllString(path, "{id}")]++
	}
	if rec.Code >= 400 && !c.dup("fail", strconv.Itoa(rec.Code), method+" "+path) {
		c.firstFail = append(c.firstFail, fmt.Sprintf("%s %s -> %d %.160s", method, canaryIDRe.ReplaceAllString(path, "{id}"), rec.Code, strings.ReplaceAll(rec.Body.String(), c.tok, "<TOKEN>")))
	}
	c.scanBody("respuesta de la escritura "+method+" "+path, rec.Body.String(), "ana")
	return &httpRecorder{Code: rec.Code, Body: rec.Body.String()}
}

type httpRecorder struct {
	Code int
	Body string
}

func (c *canary) post(who, path string, v any) *httpRecorder {
	raw, _ := json.Marshal(v)
	return c.req(who, "POST", path, string(raw))
}

func (c *canary) patch(who, path string, v any) *httpRecorder {
	raw, _ := json.Marshal(v)
	return c.req(who, "PATCH", path, string(raw))
}

func (c *canary) exec(q string, args ...any) {
	c.t.Helper()
	if _, err := c.e.db.Exec(q, args...); err != nil {
		c.t.Fatalf("%.80s: %v", q, err)
	}
}

// roleReaders ven el contenido de los espacios de la prueba: solo se exige que no vean credenciales.
var canaryRoleReaders = map[string]bool{"ana": true}

// scanBody aplica las reglas 1 y 2 a una salida. reader es quien la recibió ("" = archivo o registro).
func (c *canary) scanBody(where, body, reader string) {
	c.t.Helper()
	if body == "" {
		return
	}
	flat := html.UnescapeString(body)
	for _, m := range canaryRe.FindAllStringSubmatch(flat, -1) {
		c.anySeen[m[1]] = true
	}
	for _, secret := range []string{c.tok, c.code} {
		if strings.Contains(body, secret) || strings.Contains(flat, secret) {
			if c.dup("cred", secret[:6], where) {
				continue
			}
			c.t.Errorf("CREDENCIAL en %s (lector %q): %s", where, reader, c.snip(flat, secret))
		}
	}
	if canaryRoleReaders[reader] {
		for _, m := range canaryRe.FindAllStringSubmatch(flat, -1) {
			c.roleSeen[m[1]] = true
		}
		return
	}
	for _, m := range canaryRe.FindAllStringSubmatch(flat, -1) {
		tag := m[1]
		full := "RESERVADO-" + tag + "-" + c.rnd
		if !strings.Contains(flat, full) {
			continue
		}
		if _, ok := allowedAudit[tag]; ok || canaryIdentityTag.MatchString(tag) {
			continue
		}
		if c.dup("txt", tag, where) {
			continue
		}
		c.t.Errorf("TEXTO RESERVADO del campo %q en %s (lector %q sin rol): %s", tag, where, reader, c.snip(flat, full))
	}
}

// dup informa si ya se reportó este hallazgo (misma clase, campo y salida sin números).
func (c *canary) dup(kind, tag, where string) bool {
	key := kind + "|" + tag + "|" + regexp.MustCompile(`[0-9]+`).ReplaceAllString(where, "#")
	if c.seen[key] {
		return true
	}
	c.seen[key] = true
	return false
}

func (c *canary) snip(text, needle string) string {
	i := strings.Index(text, needle)
	if i < 0 {
		return ""
	}
	a, b := max(0, i-60), min(len(text), i+len(needle)+40)
	return strings.ReplaceAll(strings.ReplaceAll(text[a:b], c.tok, "<TOKEN>"), c.code, "<CODIGO>")
}

// ─── Escenarios de escritura ─────────────────────────────────────────────────

type canaryScenario struct {
	name   string
	fields []string // campos de texto libre que escribe (cada uno lleva su etiqueta)
	run    func(c *canary, v func(field string) string)
}

func (c *canary) runScenario(s canaryScenario) {
	c.t.Helper()
	// Una vez con texto reservado en todos los campos.
	s.run(c, func(f string) string { return c.reserved(f) })
	// Una vez por campo con las credenciales en ese campo.
	for _, field := range s.fields {
		field := field
		s.run(c, func(f string) string {
			if f == field {
				return c.cred(f)
			}
			return c.reserved(f)
		})
	}
}

// ─── Salidas ─────────────────────────────────────────────────────────────────

var canaryReaders = []string{"luis", "luis-read", "pedro", "carla", "ana", "ci"}

func (c *canary) apiPaths(slug string) []string {
	return []string{
		"/nexo/admin/audit?limit=200", "/nexo/admin/activity?limit=200", "/nexo/admin/presence?limit=200",
		"/nexo/admin/history/obs-canary?include_payload=true", "/nexo/admin/history/obs-canary",
		"/nexo/admin/authz-events?limit=200", "/nexo/authz/v1/events?limit=200", "/nexo/admin/alerts?limit=200&state=todas",
		"/nexo/admin/access-review", "/nexo/admin/audit/verify", "/nexo/admin/database",
		"/nexo/authz/v1/me", "/nexo/authz/v1/platform/members", "/nexo/authz/v1/tokens",
		"/nexo/authz/v1/service-accounts?enabled=true", "/nexo/authz/v1/service-accounts", "/nexo/authz/v1/activations?principal=ana",
		"/admin/users", "/nexo/kb/v1/spaces", "/nexo/kb/v1/spaces/" + slug, "/nexo/kb/v1/spaces/" + slug + "/events?limit=200",
		"/nexo/kb/v1/spaces/" + slug + "/publish-runs", "/nexo/kb/v1/spaces/" + slug + "/documents",
		"/nexo/kb/v1/eval-runs", "/nexo/kb/v1/proposals", "/nexo/kb/v1/admin/index",
		"/nexo/authz/v1/scopes/space/" + slug + "/members",
	}
}

func (c *canary) panelPaths(slug string) []string {
	return []string{
		"/dashboard/admin/audit", "/dashboard/admin/audit?src=roles", "/dashboard/admin/audit/activity", "/dashboard/admin/audit/history?key=obs-canary",
		"/dashboard/admin/audit/integrity", "/dashboard/admin/audit/alerts?state=todas", "/dashboard/admin/audit/access-review",
		"/dashboard/admin/audit/access-review/export?format=csv", "/dashboard/admin/audit/access-review/export?format=json",
		"/dashboard/admin/users", "/dashboard/admin/service-accounts?all=1", "/dashboard/kb", "/dashboard/kb/search-status",
		"/dashboard/kb/spaces/" + slug, "/dashboard/kb/spaces/" + slug + "/members", "/dashboard/kb/proposals",
		"/dashboard", "/dashboard/activity", "/dashboard/browser/observations", "/dashboard/browser/sessions", "/dashboard/browser/prompts", "/dashboard/projects", "/dashboard/contributors", "/dashboard/admin",
		"/dashboard/admin/projects", "/dashboard/admin/health", "/dashboard/admin/audit-log", "/dashboard/admin/audit-log/list", "/dashboard/projects/list", "/dashboard/contributors/list", "/dashboard/admin/users/list",
	}
}

func (c *canary) scanOutputs(slug string) {
	c.t.Helper()
	dynAPI, dynPanel := c.dynamicPaths(slug)
	for _, who := range canaryReaders {
		for _, p := range append(c.apiPaths(slug), dynAPI...) {
			rec := c.e.req("GET", p, who, "", "10.99.2."+strconv.Itoa(1+c.n%200))
			c.n++
			c.scanBody("API "+p, rec.Body.String(), who)
		}
		if who == "luis-read" || who == "ci" {
			continue // sin sesión de panel propia (mismo lector que luis / token de cuenta de servicio)
		}
		for _, p := range append(c.panelPaths(slug), dynPanel...) {
			rec := c.e.get(who, p)
			c.scanBody("panel "+p, rec.Body.String(), who)
		}
	}
}

// scanFiles recorre los archivos de una carpeta (exportaciones).
func (c *canary) scanFiles(dir, label string) {
	c.t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			raw, _ := os.ReadFile(p)
			c.scanBody(label+" "+filepath.Base(filepath.Dir(p))+"/"+d.Name(), string(raw), "")
			n++
		}
		return nil
	})
	if n == 0 {
		c.t.Errorf("%s: no se exportó ningún archivo", label)
	}
}

func (c *canary) scanExportsAndWebhook() {
	c.t.Helper()
	dir := c.t.TempDir()
	cfg := evidence.ExportConfig{Dir: dir, Key: []byte("0123456789abcdef0123456789abcdef")}
	for _, table := range evidence.ExportTables {
		if _, err := evidence.ExportDay(c.t.Context(), c.e.db, cfg, table, time.Now().UTC(), "canario"); err != nil {
			c.t.Fatalf("exportar %s: %v", table, err)
		}
	}
	c.scanFiles(dir, "exportación diaria")
	cli := c.t.TempDir()
	cliCfg := evidence.ExportConfig{Dir: cli, Key: []byte("0123456789abcdef0123456789abcdef")}
	if _, err := evidence.ExportRange(c.t.Context(), c.e.db, cliCfg, time.Now().UTC().AddDate(0, 0, -1), time.Now().UTC(), time.Now().UTC(), "canario-cli", true); err != nil {
		c.t.Fatalf("exportación CLI: %v", err)
	}
	c.scanFiles(cli, "exportación CLI v2")
	alerts, _, err := evidence.ListAlerts(c.t.Context(), c.e.db, evidence.AlertQuery{Limit: 500})
	if err != nil {
		c.t.Fatal(err)
	}
	for _, a := range alerts {
		raw, _ := json.Marshal(evidence.Payload(a))
		c.scanBody("webhook", string(raw), "")
	}
}

func TestCanaryFreeTextNeverLeaksPG(t *testing.T) {
	c := newCanary(t)
	for _, s := range canaryScenarios() {
		c.runScenario(s)
	}
	slug := c.historic()
	c.scanOutputs(slug)
	c.scanExportsAndWebhook()
	c.scanBody("registro del servidor", c.logs.String(), "")
	if len(c.tags) < 3 {
		t.Fatalf("el canario escribió muy pocos campos: %d", len(c.tags))
	}
	c.logStatuses()
	c.assertCoverage()
	for _, f := range c.firstFail {
		t.Log("rechazo: " + f)
	}
}

// logStatuses deja en el registro de la prueba qué escrituras se aceptaron (2xx) y cuáles se rechazaron.
func (c *canary) logStatuses() {
	count := map[string]string{}
	for k, st := range c.statuses {
		key := canaryIDRe.ReplaceAllString(k, "{id}")
		count[key] += " " + strconv.Itoa(st)
	}
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c.t.Logf("escritura %s ->%s", k, count[k])
	}
}

// assertCoverage evita una prueba vacía: las escrituras clave tienen que haberse aceptado al menos una vez
// y los lectores con rol tienen que haber visto el texto de varios campos.
func (c *canary) assertCoverage() {
	c.t.Helper()
	for _, want := range []string{
		"POST /nexo/kb/v1/spaces", "PATCH /nexo/kb/v1/spaces/{id}", "POST /nexo/kb/v1/spaces/{id}/documents:batch", "POST /nexo/kb/v1/spaces/{id}/proposals",
		"POST /nexo/kb/v1/eval-runs", "POST /admin/users", "POST /nexo/authz/v1/tokens", "POST /nexo/authz/v1/service-accounts", "POST /nexo/admin/access-review:complete",
	} {
		if c.ok[want] == 0 {
			c.t.Errorf("el canario no logró ninguna escritura aceptada en %q: la prueba sería vacía", want)
		}
	}
	for _, tag := range canaryMustBeSeen {
		if !c.anySeen[tag] {
			c.t.Errorf("ninguna salida mostró el campo %q: las salidas del canario no lo recorren", tag)
		}
	}
	if len(c.roleSeen) < 12 {
		keys := make([]string, 0, len(c.roleSeen))
		for k := range c.roleSeen {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		c.t.Errorf("el lector con rol solo vio %d campos reservados (%v): las salidas no recorren lo escrito", len(keys), keys)
	}
}

// canaryIdentityTag: campos de identidad y de conexión de las filas históricas (quién, con qué token, desde qué
// cliente), que un auditor ve por diseño. Siempre salen sin credenciales.
var canaryIdentityTag = regexp.MustCompile(`^h_.*(_ua|_claim|_actor|_label|_email|_username|_display_name|_by|_contributor|_proposed_by|_reviewed_by|_published_by)$|^h_(person_|token_|sa_description)`)

func init() {
	for tag, why := range map[string]string{
		"h_audit_name":        "nombre (de token o persona) en la auditoría de seguridad: etiqueta administrativa",
		"h_audit_reason":      "motivo administrativo en la auditoría de seguridad: etiqueta administrativa",
		"h_authz_reason":      "motivo administrativo en un evento de roles: etiqueta administrativa",
		"h_authz_description": "descripción de la cuenta de servicio en un evento de roles: directorio de plataforma",
		"h_audit_project":     "nombre de proyecto: identificador de ámbito",
		"h_act_project":       "nombre de proyecto: identificador de ámbito",
		"h_pres_project":      "nombre de proyecto: identificador de ámbito",
		"h_alert_doc":         "clave de documento en el sujeto de una alerta: identificador definido por el productor",
	} {
		allowedAudit[tag] = why
	}
}

// canaryMustBeSeen: etiquetas que alguna salida debe haber mostrado (con la credencial ya retirada). Si no, la
// salida que las recorre no existe y la prueba sería vacía para ese campo.
var canaryMustBeSeen = []string{
	"h_person_display_name", "h_person_email", "h_token_name", "h_kbevent_actor", "h_alert_plat_username",
	"h_act_actor", "h_pres_actor", "h_audit_name", "h_authz_reason", "h_review_label", "h_ack_label", "h_run_actor", "h_eval_actor",
}

// dbStrings devuelve la primera columna de una consulta (ids, slugs).
func (c *canary) dbStrings(q string, args ...any) []string {
	c.t.Helper()
	rows, err := c.e.db.Query(q, args...)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// dynamicPaths arma las salidas que dependen de lo escrito: detalle de cada cuenta de servicio y de cada persona,
// sus tokens y permisos, y las propuestas y documentos del espacio con filas históricas.
func (c *canary) dynamicPaths(slug string) (api, panel []string) {
	for _, sa := range c.dbStrings(`SELECT slug FROM nexo_service_accounts ORDER BY slug`) {
		api = append(api, "/nexo/authz/v1/service-accounts/"+sa)
		panel = append(panel, "/dashboard/admin/service-accounts?all=1&sa="+sa)
	}
	for _, id := range c.dbStrings(`SELECT principal_id::text FROM cloud_human_users ORDER BY 1`) {
		api = append(api, "/admin/users/"+id+"/tokens", "/admin/users/"+id+"/grants", "/nexo/authz/v1/tokens?principal="+id)
		panel = append(panel, "/dashboard/admin/users/"+id)
	}
	for _, id := range c.dbStrings(`SELECT p.id::text FROM nexo_kb_proposals p JOIN nexo_kb_spaces s ON s.id = p.space_id WHERE s.slug = $1`, slug) {
		api = append(api, "/nexo/kb/v1/proposals/"+id)
		panel = append(panel, "/dashboard/kb/spaces/"+slug+"/proposals/"+id)
	}
	api = append(api, "/nexo/kb/v1/spaces/"+slug+"/documents/hist", "/nexo/kb/v1/spaces/"+slug+"/documents/hist/versions", "/nexo/kb/v1/search?q=hist&spaces="+slug)
	panel = append(panel, "/dashboard/kb/spaces/"+slug+"/doc?doc=hist", "/dashboard/kb/spaces/"+slug+"/proposals")
	return api, panel
}
