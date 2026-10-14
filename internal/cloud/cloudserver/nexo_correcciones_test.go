package cloudserver

// NX-07 · correcciones de la revisión adversarial (puntos 1 a 6 y 8): canje por el
// emisor, política vigente al canjear, nombres de token, texto externo con
// credenciales, notas libres sin ámbito y metadatos de conocimiento. Todas
// corren contra Postgres real (helpers de nexo_security_test.go). Las
// credenciales son valores generados al vuelo; nunca se imprimen.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

func nexoTestToken(t *testing.T) string {
	t.Helper()
	mt, err := cloudauth.GenerateManagedToken("live")
	if err != nil {
		t.Fatal(err)
	}
	return mt.Raw
}

func nexoTestCode(t *testing.T) string {
	t.Helper()
	c, err := security.GenerateActivationCode()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// nexoNoCred falla si el texto contiene alguna credencial (sin imprimirla).
func nexoNoCred(t *testing.T, what, text string, secrets ...string) {
	t.Helper()
	for i, s := range secrets {
		if s != "" && strings.Contains(text, s) {
			t.Errorf("%s contiene la credencial #%d", what, i)
		}
	}
}

// ─── Punto 1: el emisor no canjea su propio código; alerta de misma IP ───────

func TestNexoActivationIssuerCannotRedeemPG(t *testing.T) {
	e := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	code, id := nexoIssueForTest(t, e, "/nexo/authz/v1/tokens", `{"principal":"ana","name":"emisor","scope":"read","expires_in_days":10}`)
	body, _ := json.Marshal(map[string]string{"code": code})

	// API: la petición llega autenticada con el token del emisor.
	r := e.req("POST", "/nexo/authz/v1/activate", "carla", string(body), "10.77.4.9")
	if r.Code != http.StatusForbidden || nexoActivationJSON(t, r)["error_code"] != "activation_self_redeem" {
		t.Fatalf("el emisor canjeó su código por la API: %d", r.Code)
	}
	nexoNoCred(t, "rechazo API", r.Body.String(), code)
	// Panel: la misma persona con su sesión.
	cookie := managedDashboardLogin(t, e.srv, e.tokens["carla"], false)
	pr := performDashboardForm(e.srv, http.MethodPost, "/dashboard/activate", "code="+url.QueryEscape(code), cookie, false)
	if pr.Code != http.StatusForbidden || !strings.Contains(pr.Body.String(), "no puede canjearlo") {
		t.Fatalf("el emisor canjeó su código desde el panel: %d", pr.Code)
	}
	nexoNoCred(t, "rechazo del panel", pr.Body.String(), code)

	if e.count(`SELECT count(*) FROM nexo_activation_codes WHERE id=$1::bigint AND used_at IS NULL AND failed_attempts=0`, id) != 1 {
		t.Fatal("el rechazo no debe consumir el código ni sumarle intentos")
	}
	if e.count(`SELECT count(*) FROM cloud_principal_tokens WHERE name='emisor'`) != 0 {
		t.Fatal("se creó un token para el emisor")
	}
	if e.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action='nexo.activation.failed' AND reason_code='activation_self_redeem' AND actor_principal_id=$1::bigint`, e.ids["carla"]) != 2 {
		t.Fatal("los dos rechazos del emisor deben quedar auditados con su identidad")
	}
	if e.count(`SELECT count(*) FROM nexo_alerts WHERE rule='activation_repeated'`) != 0 {
		t.Fatal("un rechazo por ser el emisor no es una repetición de intentos")
	}
	// La persona destinataria, con su propio token, sí canjea.
	if r := e.req("POST", "/nexo/authz/v1/activate", "ana", string(body), "10.77.4.1"); r.Code != http.StatusCreated {
		t.Fatalf("la destinataria no pudo canjear: %d", r.Code)
	}
	if e.count(`SELECT count(*) FROM cloud_principal_tokens WHERE name='emisor' AND principal_id=$1::bigint`, e.ids["ana"]) != 1 {
		t.Fatal("el token canjeado debe ser de la destinataria")
	}
}

func TestNexoActivationSameIPAlertPG(t *testing.T) {
	e := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	issue := func(ip string) (string, string) {
		t.Helper()
		r := e.req("POST", "/nexo/authz/v1/tokens", "carla", `{"principal":"ana","name":"mismaip","scope":"write"}`, ip)
		if r.Code != http.StatusCreated {
			t.Fatalf("emisión: %d", r.Code)
		}
		v := nexoActivationJSON(t, r)
		return v["activation_code"].(string), v["activation"].(map[string]any)["id"].(string)
	}
	redeem := func(code, ip string) int {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"code": code})
		return e.req("POST", "/nexo/authz/v1/activate", "", string(b), ip).Code
	}
	// Misma IP en menos de un minuto: alerta alta (indicio, no prueba) y marca en la auditoría.
	c1, id1 := issue("10.77.5.1")
	if redeem(c1, "10.77.5.1") != http.StatusCreated {
		t.Fatal("canje con la IP del emisor")
	}
	if e.count(`SELECT count(*) FROM nexo_alerts WHERE rule='activation_same_ip' AND severity='high' AND details->>'activation_id'=$1 AND details->>'heuristic'='true'`, id1) != 1 {
		t.Fatal("falta la alerta de misma IP")
	}
	if e.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action='nexo.activation.redeemed' AND metadata->>'issuer_ip_match'='true'`) != 1 {
		t.Fatal("el canje no marcó la coincidencia de IP")
	}
	// IP distinta: sin alerta nueva.
	c2, _ := issue("10.77.5.1")
	if redeem(c2, "10.77.5.2") != http.StatusCreated || e.count(`SELECT count(*) FROM nexo_alerts WHERE rule='activation_same_ip'`) != 1 {
		t.Fatal("una IP distinta no debe alertar")
	}
	// Misma IP pasado el minuto: sin alerta nueva.
	c3, id3 := issue("10.77.5.1")
	if _, err := e.db.Exec(`UPDATE nexo_activation_codes SET created_at = now() - interval '2 minutes' WHERE id=$1::bigint`, id3); err != nil {
		t.Fatal(err)
	}
	if redeem(c3, "10.77.5.1") != http.StatusCreated || e.count(`SELECT count(*) FROM nexo_alerts WHERE rule='activation_same_ip'`) != 1 {
		t.Fatal("pasado el minuto no debe alertar")
	}
	scan := newNexoLeakScanner()
	for i, c := range []string{c1, c2, c3} {
		scan.addToken("código "+strconv.Itoa(i), c, nil)
	}
	for _, path := range []string{"/nexo/admin/alerts", "/nexo/admin/audit?action=nexo.activation.*"} {
		rec := e.req("GET", path, "carla", "", "10.77.5.3")
		if rec.Code == http.StatusOK && len(scan.find(rec.Body.String())) != 0 {
			t.Fatalf("fuga de código en %s", path)
		}
	}
}

// ─── Punto 5: el canje revalida la política vigente ──────────────────────────

func TestNexoActivationPolicyRevalidatedAtRedeemPG(t *testing.T) {
	e := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	code, id := nexoIssueForTest(t, e, "/nexo/authz/v1/tokens", `{"principal":"ana","name":"politica","scope":"write","expires_in_days":200}`)
	body, _ := json.Marshal(map[string]string{"code": code})

	// Entre la emisión y el canje baja el máximo de vida de los tokens.
	t.Setenv(security.TokenMaxDaysEnv, "30")
	r := e.req("POST", "/nexo/authz/v1/activate", "", string(body), "10.77.6.1")
	if r.Code != http.StatusConflict || nexoActivationJSON(t, r)["error_code"] != "activation_policy_changed" {
		t.Fatalf("canje con la política cambiada: %d", r.Code)
	}
	nexoNoCred(t, "rechazo por política", r.Body.String(), code)
	if e.count(`SELECT count(*) FROM nexo_activation_codes WHERE id=$1::bigint AND used_at IS NULL AND token_id IS NULL AND failed_attempts=0`, id) != 1 {
		t.Fatal("el código no debe consumirse ni sumar intentos")
	}
	if e.count(`SELECT count(*) FROM cloud_principal_tokens WHERE name='politica'`) != 0 {
		t.Fatal("no debe existir el token")
	}
	if e.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action='nexo.activation.failed' AND reason_code='activation_policy_changed'`) != 1 {
		t.Fatal("el rechazo debe quedar auditado")
	}
	if e.count(`SELECT count(*) FROM nexo_alerts WHERE rule='activation_repeated'`) != 0 {
		t.Fatal("no es un intento repetido")
	}
	// También vale para el alcance: una política que ya no acepta el alcance rechaza igual (el mínimo de días es 1).
	// Al volver a permitir la vida pedida, el mismo código se canjea.
	t.Setenv(security.TokenMaxDaysEnv, "365")
	if r := e.req("POST", "/nexo/authz/v1/activate", "", string(body), "10.77.6.1"); r.Code != http.StatusCreated {
		t.Fatalf("canje con la política restablecida: %d", r.Code)
	}
}

// ─── Punto 3: nombres de token validados en todas las rutas ──────────────────

func TestNexoTokenNameValidatedOnEveryRoutePG(t *testing.T) {
	e := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	tok, code := nexoTestToken(t), nexoTestCode(t)
	if r := e.req("POST", "/nexo/authz/v1/service-accounts", "pedro", `{"slug":"ci-nombres","owner":"ana"}`, "10.77.7.1"); r.Code != http.StatusCreated {
		t.Fatalf("alta de la cuenta de servicio: %d", r.Code)
	}
	r := e.req("POST", "/nexo/authz/v1/service-accounts/ci-nombres/tokens", "pedro", `{"name":"valido"}`, "10.77.7.1")
	if r.Code != http.StatusCreated {
		t.Fatalf("token de la cuenta de servicio: %d", r.Code)
	}
	saToken := nexoJSON(t, r)["token"].(map[string]any)["id"].(string)

	names := []string{tok, code, "Bearer abcdefghijkl", strings.Repeat("n", 300), "con\ncontrol"}
	before := e.count(`SELECT count(*) FROM cloud_principal_tokens`)
	panel := e.panel("carla")
	for i, name := range names {
		jb, _ := json.Marshal(map[string]string{"name": name, "principal": "ana"})
		jself, _ := json.Marshal(map[string]string{"name": name, "principal": "carla"})
		jname, _ := json.Marshal(map[string]string{"name": name})
		routes := []struct{ what, method, path, who, body string }{
			{"Nexo, otra persona", "POST", "/nexo/authz/v1/tokens", "carla", string(jb)},
			{"Nexo, autoemisión", "POST", "/nexo/authz/v1/tokens", "carla", string(jself)},
			{"Engram, otra persona", "POST", "/admin/users/" + e.ids["ana"] + "/tokens", "carla", string(jname)},
			{"Engram, autoemisión", "POST", "/admin/users/" + e.ids["carla"] + "/tokens", "carla", string(jname)},
			{"cuenta de servicio, emisión", "POST", "/nexo/authz/v1/service-accounts/ci-nombres/tokens", "pedro", string(jname)},
			{"cuenta de servicio, rotación", "POST", "/nexo/authz/v1/service-accounts/ci-nombres/tokens/" + saToken + ":rotate", "pedro", string(jname)},
		}
		for _, rt := range routes {
			rec := e.req(rt.method, rt.path, rt.who, rt.body, "10.77.7.2")
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s con nombre #%d: %d, quería 400", rt.what, i, rec.Code)
			}
			nexoNoCred(t, rt.what, rec.Body.String(), tok, code)
		}
		for _, target := range []string{e.ids["ana"], e.ids["carla"]} {
			rec := panel.post("/dashboard/admin/users/"+target+"/tokens", url.Values{"name": {name}, "csrf_token": {panel.csrf("")}})
			if rec.Code != http.StatusBadRequest {
				t.Errorf("panel (%s) con nombre #%d: %d, quería 400", target, i, rec.Code)
			}
			nexoNoCred(t, "panel", rec.Body.String(), tok, code)
		}
	}
	if e.count(`SELECT count(*) FROM cloud_principal_tokens`) != before || e.count(`SELECT count(*) FROM nexo_activation_codes`) != 0 {
		t.Fatal("un nombre inválido no debe crear tokens ni códigos")
	}
	// El almacén es el último guardián: ni siquiera con un llamador que se salte los controladores.
	hash, _ := e.hasher.Hash("x-" + tok)
	if _, err := e.cs.CreatePrincipalTokenWithAudit(t.Context(), cloudstore.CreatePrincipalTokenParams{PrincipalID: e.ids["ana"], TokenPrefix: "egc_live_zzzzzzzz", TokenHash: hash, Name: code},
		cloudstore.AuthAuditEvent{ActorSource: "test", Action: "token.create", Outcome: "success"}); err == nil {
		t.Fatal("el almacén aceptó un nombre con credencial")
	}
}

// Nombres históricos con una credencial se devuelven redactados en todos los listados.
func TestNexoHistoricTokenNamesRedactedPG(t *testing.T) {
	e := newAuditPanelEnv(t)
	tok, code := nexoTestToken(t), nexoTestCode(t)
	dirty := "laptop " + tok + " " + code
	// Un token histórico cuyo nombre se guardó antes de la validación.
	if _, err := e.db.Exec(`UPDATE cloud_principal_tokens SET name=$1 WHERE principal_id=$2::bigint AND name='ana'`, dirty, e.ids["ana"]); err != nil {
		t.Fatal(err)
	}
	var tokenID string
	if err := e.db.QueryRow(`SELECT id::text FROM cloud_principal_tokens WHERE principal_id=$1::bigint AND name=$2`, e.ids["ana"], dirty).Scan(&tokenID); err != nil {
		t.Fatal(err)
	}
	// Actividad y presencia que copiaron el nombre.
	if _, err := e.db.Exec(`INSERT INTO nexo_activity_log (actor, actor_principal_id, actor_token_id, actor_verified, operation, project) VALUES ($1,$2,$3,TRUE,'pull_mutations','proy-n')`, dirty, e.ids["ana"], tokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO nexo_actor_presence (presence_key, actor, actor_principal_id, actor_token_id, actor_verified) VALUES ($1,$2,$3,$4,TRUE) ON CONFLICT (presence_key) DO UPDATE SET actor=EXCLUDED.actor`, e.ids["ana"]+"|"+tokenID, dirty, e.ids["ana"], tokenID); err != nil {
		t.Fatal(err)
	}
	creds := []string{tok, code}
	for _, c := range []struct{ who, path string }{
		{"carla", "/nexo/authz/v1/tokens?principal=ana"},
		{"luis", "/nexo/admin/activity"},
		{"luis", "/nexo/admin/presence"},
		{"luis", "/nexo/admin/access-review"},
		{"luis", "/nexo/admin/access-review?format=json"},
		{"luis", "/nexo/admin/access-review?format=csv"},
	} {
		rec := e.req("GET", c.path, c.who, "", "10.78.7.1")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d", c.path, rec.Code)
		}
		nexoNoCred(t, c.path, rec.Body.String(), creds...)
	}
	for _, path := range []string{"/dashboard/admin/audit/activity", "/dashboard/admin/audit/access-review", "/dashboard/admin/audit/access-review/export?format=csv", "/dashboard/admin/audit/access-review/export?format=json"} {
		nexoNoCred(t, path, e.mustBody("luis", path, http.StatusOK), creds...)
	}
	nexoNoCred(t, "usuario del panel", e.mustBody("carla", "/dashboard/admin/users/"+e.ids["ana"], http.StatusOK), creds...)
	e.assertNoLeaks()
}

// ─── Punto 2: texto externo con credenciales en todas las tablas y rutas ─────

func nexoTableHas(e *nexoSecEnv, table, secret string) int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM `+table+` t WHERE position($1 in to_jsonb(t)::text) > 0`, secret)
}

func TestNexoExternalTextRedactedOnWritePG(t *testing.T) {
	e := newAuditPanelEnv(t)
	tok, code := nexoTestToken(t), nexoTestCode(t)
	dirty := "cliente/1.0 " + tok + " " + code
	do := func(method, path, who, body string) int {
		return nexoDoFrom(t, e.h, method, path, e.tokens[who], body, "10.80.1.1:40000", "", dirty).Code
	}
	do("GET", "/nexo/admin/audit", "ana", "") // denegada: cloud_auth_audit_log y nexo_authz_events con el User-Agent
	do("GET", "/sync/mutations/pull?since_seq=0", "ana", "")
	if do("POST", "/nexo/kb/v1/spaces", "carla", `{"slug":"ua-prueba","name":"UA","owner":"ana"}`) != http.StatusCreated {
		t.Fatal("no se pudo crear el espacio")
	}
	// Reconocimiento de alerta y revisión de accesos con notas y User-Agent sucios.
	a, err := evidence.InsertAlert(t.Context(), e.db, evidence.NewAlert{Rule: evidence.RuleAuthzDeniedBurst, Layer: evidence.LayerPlatform, Severity: evidence.SeverityHigh, Subject: "principal:1", DedupKey: "ua-prueba-1", Summary: "prueba"})
	if err != nil || a == nil {
		t.Fatal("no se pudo crear la alerta")
	}
	noteJSON, _ := json.Marshal(map[string]string{"note": dirty})
	if do("POST", "/nexo/admin/alerts/"+strconv.FormatInt(a.ID, 10)+":ack", "luis", string(noteJSON)) != http.StatusOK {
		t.Fatal("no se pudo reconocer la alerta")
	}
	notesJSON, _ := json.Marshal(map[string]string{"notes": dirty})
	if do("POST", "/nexo/admin/access-review:complete", "luis", string(notesJSON)) != http.StatusCreated {
		t.Fatal("no se pudo registrar la revisión")
	}
	// La declaración del cliente y el nombre del actor llegan por el actor de la mutación y por la actividad.
	actor := cloudstore.MutationActor{Name: "ana " + tok, PrincipalID: e.ids["ana"], Verified: true, ClientClaim: dirty}
	if err := e.cs.RecordActivity(t.Context(), cloudstore.ActivityEvent{Actor: actor, Operation: cloudstore.NexoOpPullMutations, Project: "proy " + code, UserAgent: dirty, LogDetail: true}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"cloud_auth_audit_log", "nexo_authz_events", "nexo_kb_events", "nexo_activity_log", "nexo_actor_presence", "nexo_alert_acks", "nexo_access_reviews", "nexo_alerts", "cloud_mutations"} {
		for i, secret := range []string{tok, code} {
			if n := nexoTableHas(e.nexoSecEnv, table, secret); n != 0 {
				t.Errorf("%s guarda la credencial #%d en %d fila(s)", table, i, n)
			}
		}
	}
	for table, col := range map[string]string{"cloud_auth_audit_log": "metadata::text", "nexo_authz_events": "user_agent", "nexo_kb_events": "user_agent", "nexo_activity_log": "user_agent",
		"nexo_actor_presence": "last_user_agent", "nexo_alert_acks": "user_agent", "nexo_access_reviews": "user_agent"} {
		if e.count(`SELECT count(*) FROM `+table+` WHERE `+col+` LIKE '%cliente/1.0%'`) == 0 {
			t.Errorf("%s: el User-Agent legítimo debía conservarse (solo se quita la credencial)", table)
		}
	}
	if e.count(`SELECT count(*) FROM nexo_activity_log WHERE client_claim LIKE 'cliente/1.0%'`) == 0 {
		t.Error("la declaración del cliente debía conservarse sin la credencial")
	}
}

func TestNexoExternalTextRedactedOnReadPG(t *testing.T) {
	e := newAuditPanelEnv(t)
	tok, code := nexoTestToken(t), nexoTestCode(t)
	creds := []string{tok, code}
	dirty := "hist " + tok + " " + code
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.db.Exec(q, args...); err != nil {
			t.Fatalf("%.60s: %v", q, err)
		}
	}
	// Filas históricas escritas antes de la redacción en escritura.
	mustExec(`INSERT INTO cloud_auth_audit_log (actor_source, action, outcome, project, reason_code, metadata) VALUES ($1,'hist.cred','success',$2,$3,$4::jsonb)`,
		"origen "+tok, "proy "+code, "motivo "+tok, `{"user_agent":"`+dirty+`","reason":"`+dirty+`","client_ip":"10.9.9.9"}`)
	mustExec(`INSERT INTO nexo_authz_events (action, scope_type, scope_key, actor_label, actor_verified, user_agent, client_claim, details) VALUES ('hist.cred','platform','*',$1,FALSE,$2,$2,$3::jsonb)`,
		"actor "+tok, dirty, `{"reason":"`+dirty+`"}`)
	mustExec(`INSERT INTO nexo_activity_log (actor, actor_verified, operation, project, user_agent, client_claim) VALUES ($1,FALSE,'pull_mutations',$2,$3,$3)`, "actor "+code, "proy "+tok, dirty)
	mustExec(`INSERT INTO nexo_actor_presence (presence_key, actor, actor_verified, last_user_agent, last_client_claim, last_project) VALUES ('hist|cred',$1,FALSE,$2,$2,$3)`, "actor "+tok, dirty, "proy "+code)
	var spaceID string
	if rec := e.req("POST", "/nexo/kb/v1/spaces", "carla", `{"slug":"hist-cred","name":"Historia","owner":"ana"}`, "10.80.2.1"); rec.Code != http.StatusCreated {
		t.Fatalf("crear espacio: %d", rec.Code)
	}
	if err := e.db.QueryRow(`SELECT id::text FROM nexo_kb_spaces WHERE slug='hist-cred'`).Scan(&spaceID); err != nil {
		t.Fatal(err)
	}
	mustExec(`INSERT INTO nexo_kb_events (space_id, space_slug, action, target_kind, target_id, actor_label, user_agent, client_claim, details) VALUES ($1::uuid,'hist-cred','hist.cred','document',$2,$3,$4,$4,$5::jsonb)`,
		spaceID, "guias/"+code, "actor "+tok, dirty, `{"doc_key":"`+code+`","note":"`+dirty+`"}`)
	mustExec(`INSERT INTO nexo_alerts (rule, severity, layer, subject, dedup_key, summary, details) VALUES ('kb_doc_expiring','warning','conocimiento',$1,$2,$3,$4::jsonb)`,
		"space:hist-cred/doc:"+code, "k|"+tok, "resumen "+dirty, `{"space":"hist-cred","doc_key":"`+code+`","owner":"`+tok+`"}`)
	var alertID int64
	if err := e.db.QueryRow(`SELECT id FROM nexo_alerts WHERE dedup_key=$1`, "k|"+tok).Scan(&alertID); err != nil {
		t.Fatal(err)
	}
	mustExec(`INSERT INTO nexo_alert_acks (alert_id, acknowledged_by_label, note, user_agent) VALUES ($1,$2,$3,$3)`, alertID, "ack "+tok, dirty)
	mustExec(`INSERT INTO nexo_access_reviews (reviewer_label, notes, summary, report_sha256, user_agent) VALUES ($1,$2,'{}'::jsonb,'abc',$2)`, "rev "+code, dirty)
	payload, _ := json.Marshal(map[string]any{"sync_id": "hist-obs", "session_id": "s", "type": "decision", "scope": "project", "title": "t", "content": "c", "project": "hist-proy"})
	if _, err := e.db.Exec(`INSERT INTO nexo_scopes(scope_type,scope_key) VALUES('project','hist-proy')`); err != nil {
		t.Fatal(err)
	}
	ctx := cloudstore.WithMutationActor(t.Context(), cloudstore.MutationActor{Name: "ana", PrincipalID: e.ids["ana"], Verified: true})
	if _, err := e.cs.InsertMutationBatch(ctx, []cloudstore.MutationEntry{{Project: "hist-proy", Entity: "observation", EntityKey: "hist-obs", Op: "upsert", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	mustExec(`UPDATE cloud_mutations SET actor_client_claim=$1, actor=$2 WHERE entity_key='hist-obs'`, dirty, "ana "+code)

	check := func(who, path string, want int) {
		t.Helper()
		rec := e.req("GET", path, who, "", "10.80.2.2")
		if rec.Code != want {
			t.Errorf("GET %s: %d, quería %d", path, rec.Code, want)
		}
		nexoNoCred(t, "API "+path, rec.Body.String(), creds...)
	}
	for _, who := range []string{"luis", "carla"} {
		check(who, "/nexo/admin/audit?action=hist.cred", 200)
		check(who, "/nexo/admin/authz-events?action=hist.*", 200)
		check(who, "/nexo/authz/v1/events?action=hist.*", 200)
		check(who, "/nexo/admin/activity", 200)
		check(who, "/nexo/admin/presence", 200)
		check(who, "/nexo/admin/history/hist-obs?include_payload=true", 200)
		check(who, "/nexo/admin/alerts", 200)
		check(who, "/nexo/admin/access-review", 200)
	}
	check("ana", "/nexo/kb/v1/spaces/hist-cred/events", 200)
	for _, path := range []string{"/dashboard/admin/audit?action=hist.cred", "/dashboard/admin/audit?src=roles&action=hist.*", "/dashboard/admin/audit/activity",
		"/dashboard/admin/audit/history?key=hist-obs", "/dashboard/admin/audit/alerts", "/dashboard/admin/audit/access-review"} {
		nexoNoCred(t, "panel "+path, e.mustBody("luis", path, http.StatusOK), creds...)
	}
	// Exportación diaria: ningún archivo conserva credenciales aunque la fila original las tuviera.
	dir := t.TempDir()
	cfg := evidence.ExportConfig{Dir: dir, Key: []byte("0123456789abcdef0123456789abcdef")}
	for _, table := range evidence.ExportTables {
		if _, err := evidence.ExportDay(t.Context(), e.db, cfg, table, time.Now().UTC(), "prueba"); err != nil {
			t.Fatalf("exportar %s: %v", table, err)
		}
	}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			raw, _ := os.ReadFile(p)
			nexoNoCred(t, "exportación "+filepath.Base(filepath.Dir(p))+"/"+d.Name(), string(raw), creds...)
		}
		return nil
	})
	// Webhook: el cuerpo de una alerta histórica sale sin credenciales.
	alerts, _, err := evidence.ListAlerts(t.Context(), e.db, evidence.AlertQuery{Limit: 50})
	if err != nil || len(alerts) == 0 {
		t.Fatalf("alertas: %v", err)
	}
	for _, a := range alerts {
		raw, _ := json.Marshal(evidence.Payload(a))
		nexoNoCred(t, "webhook", string(raw), creds...)
	}
	e.assertNoLeaks()
}

// ─── Punto 4: notas libres ───────────────────────────────────────────────────

func TestNexoFreeNotesWithheldWithoutScopePG(t *testing.T) {
	e := newAuditPanelEnv(t)
	tok := nexoTestToken(t)
	// Alerta de un espacio: la nota del reconocimiento solo la ve quien lee el espacio.
	if rec := e.req("POST", "/nexo/kb/v1/spaces", "carla", `{"slug":"notas-priv","name":"Privado","owner":"ana","visibility":"restricted"}`, "10.81.1.1"); rec.Code != http.StatusCreated {
		t.Fatalf("crear espacio: %d", rec.Code)
	}
	a, err := evidence.InsertAlert(t.Context(), e.db, evidence.NewAlert{Rule: evidence.RuleKBDocExpiring, Layer: evidence.LayerKnowledge, Severity: evidence.SeverityInfo,
		Subject: "space:notas-priv/doc:d1", DedupKey: "notas-1", Summary: "resumen reservado", Details: map[string]any{"space": "notas-priv", "doc_key": "d1"}})
	if err != nil || a == nil {
		t.Fatal("alerta no creada")
	}
	ackBody, _ := json.Marshal(map[string]string{"note": "nota-libre-reservada " + tok})
	rec := e.req("POST", "/nexo/admin/alerts/"+strconv.FormatInt(a.ID, 10)+":ack", "luis", string(ackBody), "10.81.1.2")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "nota-libre-reservada") || !strings.Contains(rec.Body.String(), `"note_withheld":true`) {
		t.Fatalf("la respuesta del reconocimiento repite la nota: %d %s", rec.Code, rec.Body.String())
	}
	nexoNoCred(t, "respuesta del reconocimiento", rec.Body.String(), tok)
	if e.count(`SELECT count(*) FROM nexo_alert_acks WHERE note LIKE 'nota-libre-reservada%'`) != 1 {
		t.Fatal("la nota debía guardarse (sin la credencial)")
	}
	for _, who := range []string{"luis", "pedro", "carla"} {
		list := e.req("GET", "/nexo/admin/alerts", who, "", "10.81.1.3")
		if list.Code == http.StatusOK {
			if strings.Contains(list.Body.String(), "nota-libre-reservada") || !strings.Contains(list.Body.String(), `"note_withheld":true`) {
				t.Errorf("%s: la lista de alertas entrega la nota libre", who)
			}
		}
	}
	if body := e.mustBody("luis", "/dashboard/admin/audit/alerts?state=todas", http.StatusOK); strings.Contains(body, "nota-libre-reservada") {
		t.Error("el panel de alertas muestra la nota libre")
	}
	// Con rol de lectura en el espacio la nota se entrega, siempre sin credenciales.
	if _, err := e.db.Exec(`INSERT INTO nexo_role_bindings(principal_id,scope_type,scope_key,role) VALUES($1,'space','notas-priv','lector')`, e.ids["luis"]); err != nil {
		t.Fatal(err)
	}
	list := e.req("GET", "/nexo/admin/alerts", "luis-read", "", "10.81.1.4")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "nota-libre-reservada") {
		t.Errorf("el lector del espacio debía ver la nota: %d", list.Code)
	}
	nexoNoCred(t, "alertas con permiso", list.Body.String(), tok)

	// Revisión de accesos: sin ámbito verificable la nota nunca sale a quien solo audita.
	notes, _ := json.Marshal(map[string]string{"notes": "nota-de-revision-reservada " + tok})
	rec = e.req("POST", "/nexo/admin/access-review:complete", "luis", string(notes), "10.81.1.5")
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "nota-de-revision-reservada") || !strings.Contains(rec.Body.String(), `"note_withheld":true`) {
		t.Fatalf("CompleteReview repite la nota: %d %s", rec.Code, rec.Body.String())
	}
	if e.count(`SELECT count(*) FROM nexo_access_reviews WHERE notes LIKE 'nota-de-revision-reservada%' AND position($1 in notes)=0`, tok) != 1 {
		t.Fatal("la nota debía guardarse sin la credencial")
	}
	for _, who := range []string{"luis", "luis-read", "carla"} {
		rep := e.req("GET", "/nexo/admin/access-review", who, "", "10.81.1.6")
		if rep.Code == http.StatusOK {
			if strings.Contains(rep.Body.String(), "nota-de-revision-reservada") || !strings.Contains(rep.Body.String(), `"note_withheld":true`) {
				t.Errorf("%s: last_review o recent_reviews entregan la nota", who)
			}
			nexoNoCred(t, "reporte de accesos", rep.Body.String(), tok)
		}
	}
	panelBody := e.mustBody("luis", "/dashboard/admin/audit/access-review", http.StatusOK)
	if strings.Contains(panelBody, "nota-de-revision-reservada") || !strings.Contains(panelBody, "el texto no se muestra aquí") {
		t.Error("el panel de revisión muestra la nota libre")
	}
	for _, f := range []string{"csv", "json"} {
		exp := e.mustBody("luis", "/dashboard/admin/audit/access-review/export?format="+f, http.StatusOK)
		if strings.Contains(exp, "nota-de-revision-reservada") {
			t.Errorf("la exportación %s incluye la nota", f)
		}
	}
	e.assertNoLeaks()
}

// ─── Punto 6: metadatos de conocimiento con credenciales ─────────────────────

func TestNexoKBMetadataRejectsCredentialsPG(t *testing.T) {
	e := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	code, tok := nexoTestCode(t), nexoTestToken(t)
	if rec := e.req("POST", "/nexo/kb/v1/spaces", "carla", `{"slug":"cred-meta","name":"Metadatos","owner":"ana"}`, "10.82.1.1"); rec.Code != http.StatusCreated {
		t.Fatalf("crear espacio: %d", rec.Code)
	}
	okDoc := func(extra string) string {
		return `{"documents":[{` + extra + `"content":"---\ncategory: lineamiento\ntitle: Titulo\n---\n# Titulo\n\nTexto sin secretos.\n"}]}`
	}
	cases := []struct{ what, body string }{
		{"código en doc_key", okDoc(`"doc_key":"guias/` + code + `","path":"a.md",`)},
		{"token en doc_key", okDoc(`"doc_key":"` + tok + `","path":"a.md",`)},
		{"código en la ruta", okDoc(`"path":"guias/` + code + `.md",`)},
		{"código en el origen", okDoc(`"doc_key":"ok-1","path":"a.md","origin":{"repo":"` + code + `"},`)},
		{"código en la nota de cambio", okDoc(`"doc_key":"ok-2","path":"a.md","change_note":"` + code + `",`)},
		{"código en el contenido", `{"documents":[{"doc_key":"ok-3","path":"a.md","content":"---\ncategory: lineamiento\ntitle: T\n---\n# T\n\n` + code + `\n"}]}`},
		{"código en el título", `{"documents":[{"doc_key":"ok-4","path":"a.md","content":"---\ncategory: lineamiento\ntitle: ` + code + `\n---\n# T\n\ntexto\n"}]}`},
	}
	for _, c := range cases {
		rec := e.req("POST", "/nexo/kb/v1/spaces/cred-meta/documents:batch", "ana", c.body, "10.82.1.2")
		nexoNoCred(t, c.what, rec.Body.String(), code, tok)
		if rec.Code >= 500 || !strings.Contains(rec.Body.String(), "rejected") {
			t.Errorf("%s: debía rechazarse (%d)", c.what, rec.Code)
		}
	}
	if e.count(`SELECT count(*) FROM nexo_kb_documents`) != 0 {
		t.Fatal("no debe publicarse ningún documento con credenciales")
	}
	for _, table := range []string{"nexo_kb_events", "nexo_kb_publish_runs", "nexo_alerts", "nexo_authz_events", "cloud_auth_audit_log"} {
		for i, s := range []string{code, tok} {
			if n := nexoTableHas(e, table, s); n != 0 {
				t.Errorf("%s guarda la credencial #%d", table, i)
			}
		}
	}
	// Propuestas y retiro con credenciales en clave o nota.
	prop, _ := json.Marshal(map[string]string{"doc_key": "guias/" + code, "title": "Titulo", "content": "# Titulo\n\nsin secretos\n"})
	if rec := e.req("POST", "/nexo/kb/v1/spaces/cred-meta/proposals", "ana", string(prop), "10.82.1.3"); rec.Code < 400 || rec.Code >= 500 {
		t.Errorf("propuesta con código en la clave: %d", rec.Code)
	} else {
		nexoNoCred(t, "propuesta", rec.Body.String(), code)
	}
	// Un documento publicado con clave válida; retirarlo con un código en la nota se rechaza.
	good := e.req("POST", "/nexo/kb/v1/spaces/cred-meta/documents:batch", "ana", okDoc(`"doc_key":"valido-1","path":"v.md",`), "10.82.1.4")
	if good.Code != http.StatusOK {
		t.Fatalf("publicación válida: %d %s", good.Code, good.Body.String())
	}
	note, _ := json.Marshal(map[string]string{"note": "retiro " + code})
	if rec := e.req("POST", "/nexo/kb/v1/spaces/cred-meta/documents/valido-1:retire", "ana", string(note), "10.82.1.5"); rec.Code < 400 || rec.Code >= 500 {
		t.Errorf("retiro con código en la nota: %d", rec.Code)
	} else {
		nexoNoCred(t, "retiro", rec.Body.String(), code)
	}
}

// Las alertas de conocimiento de documentos históricos con claves peligrosas salen redactadas
// (sujeto, clave de deduplicación y detalles) y el evaluador no las vuelve a escribir sin redactar.
func TestNexoKBAlertSubjectsRedactedPG(t *testing.T) {
	e := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	code := nexoTestCode(t)
	if rec := e.req("POST", "/nexo/kb/v1/spaces", "carla", `{"slug":"alert-meta","name":"Alertas","owner":"ana"}`, "10.82.2.1"); rec.Code != http.StatusCreated {
		t.Fatalf("crear espacio: %d", rec.Code)
	}
	var spaceID string
	if err := e.db.QueryRow(`SELECT id::text FROM nexo_kb_spaces WHERE slug='alert-meta'`).Scan(&spaceID); err != nil {
		t.Fatal(err)
	}
	// Un documento histórico cuya clave y responsable ya traían una credencial vence pronto.
	if _, err := e.db.Exec(`INSERT INTO nexo_kb_documents (id, space_id, doc_key, title, status, category, owner_label, valid_from, valid_until, current_version) VALUES (gen_random_uuid(),$1::uuid,$2,'t','active','lineamiento',$3, now()-interval '1 day', now()+interval '5 days', 1)`,
		spaceID, "guias/"+code, "resp "+code); err != nil {
		t.Fatalf("documento histórico: %v", err)
	}
	m := e.srv.nexoMonitorState()
	e.srv.nexoMonitorTick(t.Context(), m, time.Now())
	if n := nexoTableHas(e, "nexo_alerts", code); n != 0 {
		t.Fatalf("el evaluador guardó la clave peligrosa en %d alerta(s)", n)
	}
	if e.count(`SELECT count(*) FROM nexo_alerts WHERE rule='kb_doc_expiring'`) == 0 {
		t.Fatal("el evaluador debía crear la alerta de vencimiento")
	}
	rec := e.req("GET", "/nexo/admin/alerts", "carla", "", "10.82.2.2")
	nexoNoCred(t, "alertas", rec.Body.String(), code)
}
