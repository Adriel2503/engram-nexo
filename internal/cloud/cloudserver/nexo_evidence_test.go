package cloudserver

// Nexo IA · NX-07 fase S4 y arreglos A1/A2 — pruebas HTTP contra Postgres
// real con la resolución real de tokens (helpers de nexo_security_test.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

// A1: un token revocado (o de una persona deshabilitada) no bloquea a la
// persona por defecto; queda auditado con la persona y dispara la alerta
// revoked_token_used.
func TestNexoA1RevokedTokenDoesNotLockPerson(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	revoked := env.issue("luis", "luis-viejo", security.TokenIssue{Days: 90, Scope: "write"})
	if _, err := env.db.Exec(`UPDATE cloud_principal_tokens SET revoked_at = now() WHERE id = $1::bigint`, revoked); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ { // dos PCs viejas con autosync durante ~15 min
		ip := fmt.Sprintf("10.70.0.%d", i%2+1)
		if rec := env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", "luis-viejo", "", ip); rec.Code != http.StatusUnauthorized {
			t.Fatalf("revocado %d: %d", i, rec.Code)
		}
	}
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "luis", "", "10.70.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("A1: el token nuevo de la persona no debe bloquearse: %d %s", rec.Code, rec.Body.String())
	}
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = 'auth.lockout'`); n != 0 {
		t.Fatalf("A1: no hay bloqueo por persona por defecto: %d eventos", n)
	}
	// Auditado con la persona y el id del token (nunca el token).
	var meta string
	if err := env.db.QueryRow(`SELECT metadata::text FROM cloud_auth_audit_log WHERE action = 'sync.auth' AND reason_code = 'token_revoked' ORDER BY id LIMIT 1`).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(meta, `"nexo_principal_id": "`+env.ids["luis"]+`"`) || !strings.Contains(meta, `"credential_id": "`+revoked+`"`) || strings.Contains(meta, env.tokens["luis-viejo"]) {
		t.Fatalf("metadata del rechazo: %s", meta)
	}
	alerts, ran, err := evidence.EvaluateOnce(context.Background(), env.db, evidence.DefaultRules(), time.Now())
	if err != nil || !ran {
		t.Fatal(err)
	}
	found := 0
	for _, a := range alerts {
		if a.Rule == evidence.RuleRevokedTokenUsed && strings.HasPrefix(a.Subject, "principal:"+env.ids["luis"]+"|") {
			found++
		}
	}
	if found != 2 { // una por IP
		t.Fatalf("alertas revoked_token_used = %d: %+v", found, alerts)
	}
	// La persona deshabilitada sigue el mismo criterio.
	if _, err := env.db.Exec(`UPDATE cloud_principals SET enabled = FALSE WHERE id = $1::bigint`, env.ids["ana"]); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", "ana", "", "10.70.1.1")
	}
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = 'auth.lockout'`); n != 0 {
		t.Fatalf("persona deshabilitada: sin bloqueo: %d", n)
	}
}

// A2: rechazos repetidos (misma huella, IP y motivo) dejan una fila completa
// y un resumen con el conteo por ventana; otra IP es otra clave; con
// NEXO_AUDIT_DEDUP_SECONDS=0 cada rechazo deja su fila.
func TestNexoA2RepeatedRejectionsAreSummarized(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	const bad = "raw:egc_live_malpegado_a2_xxxxxxxxxxxxxxxxxxxxxxx"
	for i := 0; i < 30; i++ {
		env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", bad, "", "10.80.0.1")
	}
	for i := 0; i < 3; i++ {
		env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", bad, "", "10.80.0.2")
	}
	login := func(token string) {
		req := httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader(url.Values{"token": {token}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "10.80.0.3:1"
		env.h.ServeHTTP(httptest.NewRecorder(), req)
	}
	for i := 0; i < 5; i++ {
		login("egc_live_login_malpegado_xxxxxxxxxxxxxxxxxxxx")
	}
	full := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE outcome = 'denied' AND action IN ('sync.auth', 'dashboard.login') AND NOT metadata ? 'nexo_repeated'`)
	if full != 3 {
		t.Fatalf("filas completas = %d, want 3 (una por clave)", full)
	}
	var fp string
	if err := env.db.QueryRow(`SELECT metadata->>'credential_fp' FROM cloud_auth_audit_log WHERE action = 'sync.auth' AND metadata->>'client_ip' = '10.80.0.1'`).Scan(&fp); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(fp) || strings.Contains(fp, "malpegado") {
		t.Fatalf("huella: %q", fp)
	}
	if n := env.srv.nexoFlushAuditDedup(context.Background(), true); n != 3 {
		t.Fatalf("resúmenes = %d, want 3", n)
	}
	rows, err := env.db.Query(`SELECT metadata->>'client_ip', (metadata->>'nexo_repeated')::int, metadata->>'nexo_first_at', metadata->>'nexo_last_at', metadata->>'credential_fp', metadata->'nexo_user_agents'
		FROM cloud_auth_audit_log WHERE metadata ? 'nexo_repeated' ORDER BY metadata->>'client_ip'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[string]int{"10.80.0.1": 29, "10.80.0.2": 2, "10.80.0.3": 4}
	got := map[string]int{}
	for rows.Next() {
		var ip, first, last, rfp string
		var n int
		var agents []byte
		if err := rows.Scan(&ip, &n, &first, &last, &rfp, &agents); err != nil {
			t.Fatal(err)
		}
		got[ip] = n
		if first == "" || last == "" || rfp == "" || !strings.Contains(string(agents), "nexo-sec-test/1.0") && ip != "10.80.0.3" {
			t.Fatalf("resumen %s incompleto: %s %s %s %s", ip, first, last, rfp, agents)
		}
	}
	for ip, n := range want {
		if got[ip] != n {
			t.Fatalf("conteo %s = %d, want %d (%v)", ip, got[ip], n, got)
		}
	}
	// Sin agrupar.
	env0 := newNexoSecEnv(t, map[string]string{security.AuditDedupEnv: "0"})
	for i := 0; i < 7; i++ {
		env0.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", bad, "", "10.80.0.1")
	}
	if n := env0.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = 'sync.auth' AND reason_code = 'unknown_token'`); n != 7 {
		t.Fatalf("NEXO_AUDIT_DEDUP_SECONDS=0: %d filas, want 7", n)
	}
}

func (e *nexoSecEnv) makeAuditor(person string) {
	e.t.Helper()
	if _, err := e.db.Exec(`INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role) VALUES ($1::bigint, 'platform', '*', 'auditor')`, e.ids[person]); err != nil {
		e.t.Fatal(err)
	}
}

// S4-01, S4-03 y S4-04 por HTTP: permisos, verificación (CA-S09), alertas y
// reconocimiento, revisión de accesos (CA-S12).
func TestNexoEvidenceRoutes(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	env.makeAuditor("luis")
	env.issue("luis", "luis-read", security.TokenIssue{Days: 30, Scope: "read"})
	// Primera petición de carla: superadmin de arranque.
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "carla", "", "10.90.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("me: %d", rec.Code)
	}

	// Verificación de la cadena.
	for who, want := range map[string]int{"carla": 200, "pedro": 200, "luis": 200, "luis-read": 200, "ana": 403, "ci": 403} {
		if rec := env.req(http.MethodGet, "/nexo/admin/audit/verify", who, "", "10.90.0.1"); rec.Code != want {
			t.Fatalf("verify como %s: %d %s", who, rec.Code, rec.Body.String())
		}
	}
	if rec := env.req(http.MethodGet, "/nexo/admin/audit/verify", "", "", "10.90.0.1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin token: %d", rec.Code)
	}
	rec := env.req(http.MethodGet, "/nexo/admin/audit/verify?table=cloud_auth_audit_log", "luis", "", "10.90.0.1")
	body := nexoJSON(t, rec)
	if body["ok"] != true {
		t.Fatalf("cadena íntegra: %s", rec.Body.String())
	}
	if rec := env.req(http.MethodGet, "/nexo/admin/audit/verify?table=nexo_kb_versions", "luis", "", "10.90.0.1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("tabla sin cadena: %d", rec.Code)
	}
	// CA-S09: la cuenta dueña altera una fila (desactivando la guarda).
	var altered int64
	if err := env.db.QueryRow(`SELECT id FROM cloud_auth_audit_log WHERE nexo_chain_seq IS NOT NULL ORDER BY nexo_chain_seq LIMIT 1 OFFSET 1`).Scan(&altered); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`ALTER TABLE cloud_auth_audit_log DISABLE TRIGGER nexo_guard_cloud_auth_audit_log`,
		fmt.Sprintf(`UPDATE cloud_auth_audit_log SET reason_code = 'alterado' WHERE id = %d`, altered),
		`ALTER TABLE cloud_auth_audit_log ENABLE TRIGGER nexo_guard_cloud_auth_audit_log`,
	} {
		if _, err := env.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	rec = env.req(http.MethodGet, "/nexo/admin/audit/verify", "carla", "", "10.90.0.1")
	var rep evidence.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Tables[0].FirstBroken == nil || rep.Tables[0].FirstBroken.ID != altered {
		t.Fatalf("CA-S09: la verificación debe fallar en la fila %d: %s", altered, rec.Body.String())
	}
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = 'nexo.audit_verify.read'`); n < 5 {
		t.Fatalf("lecturas auditadas: %d", n)
	}

	// Alertas: denegaciones de ana (5 en un minuto) por las rutas de S4.
	for i := 0; i < 5; i++ {
		env.req(http.MethodGet, "/nexo/admin/alerts", "ana", "", "10.90.0.7")
	}
	if _, _, err := evidence.EvaluateOnce(context.Background(), env.db, evidence.DefaultRules(), time.Now()); err != nil {
		t.Fatal(err)
	}
	rec = env.req(http.MethodGet, "/nexo/admin/alerts?rule=authz_denied_burst", "luis", "", "10.90.0.1")
	page := nexoJSON(t, rec)
	items, _ := page["items"].([]any)
	if rec.Code != 200 || len(items) != 1 || items[0].(map[string]any)["subject"] != "principal:"+env.ids["ana"] || items[0].(map[string]any)["layer"] != "plataforma" {
		t.Fatalf("alertas: %d %s", rec.Code, rec.Body.String())
	}
	id := fmt.Sprint(int64(items[0].(map[string]any)["id"].(float64)))
	if rec := env.req(http.MethodPost, "/nexo/admin/alerts/"+id+":ack", "luis-read", `{"note":"x"}`, "10.90.0.1"); rec.Code != http.StatusForbidden {
		t.Fatalf("ack con token read: %d", rec.Code)
	}
	if rec := env.req(http.MethodPost, "/nexo/admin/alerts/"+id+":ack", "ana", `{}`, "10.90.0.1"); rec.Code != http.StatusForbidden {
		t.Fatalf("ack de un miembro: %d", rec.Code)
	}
	if rec := env.req(http.MethodPost, "/nexo/admin/alerts/"+id+":ack", "luis", `{"note":"revisado con ana"}`, "10.90.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("ack: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.req(http.MethodPost, "/nexo/admin/alerts/"+id+":ack", "luis", ``, "10.90.0.1"); rec.Code != http.StatusConflict {
		t.Fatalf("segundo ack: %d", rec.Code)
	}
	if rec := env.req(http.MethodPost, "/nexo/admin/alerts/999999:ack", "luis", ``, "10.90.0.1"); rec.Code != http.StatusNotFound {
		t.Fatalf("ack inexistente: %d", rec.Code)
	}
	if rec := env.req(http.MethodPost, "/nexo/admin/alerts/"+id+":borrar", "luis", ``, "10.90.0.1"); rec.Code != http.StatusNotFound {
		t.Fatalf("acción desconocida: %d", rec.Code)
	}
	if n := env.count(`SELECT count(*) FROM nexo_alert_acks a JOIN nexo_alerts x ON x.id = a.alert_id WHERE a.acknowledged_by_principal_id = $1::bigint AND a.note = 'revisado con ana'`, env.ids["luis"]); n != 1 {
		t.Fatalf("reconocimiento registrado: %d", n)
	}

	// CA-S12: revisión de accesos.
	if _, err := env.db.Exec(`UPDATE cloud_principal_tokens SET nexo_expires_at = NULL WHERE principal_id = $1::bigint`, env.ids["ci"]); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`UPDATE cloud_principal_tokens SET created_at = now() - interval '200 days', last_used_at = now() - interval '120 days' WHERE principal_id = $1::bigint`, env.ids["pedro"]); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`UPDATE cloud_principals SET created_at = now() - interval '300 days' WHERE id = $1::bigint`, env.ids["pedro"]); err != nil {
		t.Fatal(err)
	}
	if rec := env.req(http.MethodGet, "/nexo/admin/access-review", "ana", "", "10.90.0.1"); rec.Code != http.StatusForbidden {
		t.Fatalf("revisión como miembro: %d", rec.Code)
	}
	rec = env.req(http.MethodGet, "/nexo/admin/access-review", "luis-read", "", "10.90.0.1")
	var review evidence.AccessReview
	if err := json.Unmarshal(rec.Body.Bytes(), &review); err != nil || rec.Code != 200 {
		t.Fatalf("revisión: %d %v", rec.Code, err)
	}
	flags := map[string][]string{}
	for _, p := range review.People {
		flags[p.PrincipalID] = p.Flags
	}
	if !strings.Contains(strings.Join(flags[env.ids["ci"]], ","), "token_no_expiry") || !strings.Contains(strings.Join(flags[env.ids["pedro"]], ","), "inactive") {
		t.Fatalf("marcas: %+v", flags)
	}
	if strings.Contains(rec.Body.String(), env.tokens["luis"]) || strings.Contains(rec.Body.String(), "token_hash") {
		t.Fatal("la revisión no expone tokens")
	}
	if rec := env.req(http.MethodPost, "/nexo/admin/access-review:complete", "luis-read", `{"notes":"x"}`, "10.90.0.1"); rec.Code != http.StatusForbidden {
		t.Fatalf("completar con token read: %d", rec.Code)
	}
	rec = env.req(http.MethodPost, "/nexo/admin/access-review:complete", "luis", `{"notes":"revisión trimestral Q4"}`, "10.90.0.1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("completar: %d %s", rec.Code, rec.Body.String())
	}
	if n := env.count(`SELECT count(*) FROM nexo_access_reviews WHERE reviewer_principal_id = $1::bigint AND notes = 'revisión trimestral Q4' AND client_ip = '10.90.0.1'`, env.ids["luis"]); n != 1 {
		t.Fatalf("revisión registrada: %d", n)
	}
	rec = env.req(http.MethodGet, "/nexo/admin/access-review", "carla", "", "10.90.0.1")
	if !strings.Contains(rec.Body.String(), `"last_review":{`) {
		t.Fatalf("la última revisión aparece en el reporte: %s", rec.Body.String()[:200])
	}
}

// Monitor: resúmenes A2, alertas con webhook, verificación periódica con
// alerta de cadena rota, exportación diaria y retención.
func TestNexoMonitorTick(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()
	dir := t.TempDir()
	t.Setenv(evidence.AlertWebhookEnv, hook.URL+"/teams")
	t.Setenv(evidence.ExportDirEnv, dir)
	t.Setenv(evidence.ExportKeyEnv, "clave-de-prueba-del-monitor-0123456789abcdef")
	t.Setenv(NexoAuditVerifyMinutesEnv, "60")
	t.Setenv(evidence.DisableInactiveEnv, "")
	env := newNexoSecEnv(t, nil)
	for i := 0; i < 5; i++ {
		env.req(http.MethodGet, "/nexo/admin/alerts", "ana", "", "10.95.0.1")
	}
	for i := 0; i < 4; i++ {
		env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", "raw:egc_live_monitor_xxxxxxxxxxxxxxxxxxxxxxxx", "", "10.95.0.2")
	}
	m := env.srv.nexoMonitorState()
	env.srv.nexo.sec.dedup.Flush(false) // nada vencido todavía
	env.srv.nexoFlushAuditDedup(context.Background(), true)
	env.srv.nexoMonitorTick(context.Background(), m, time.Now())
	m.webhookWG.Wait()
	mu.Lock()
	joined := strings.Join(bodies, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "authz_denied_burst") || !strings.Contains(joined, `"layer":"plataforma"`) {
		t.Fatalf("webhook: %s", joined)
	}
	if strings.Contains(joined, "egc_live") || strings.Contains(joined, "credential_fp") {
		t.Fatalf("el webhook no lleva secretos: %s", joined)
	}
	// Exportación diaria de ayer (vacía) para todas las tablas.
	for _, tbl := range evidence.ExportTables {
		if _, err := os.Stat(filepath.Join(dir, tbl, time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)+".jsonl.sig")); err != nil {
			t.Fatalf("exportación de %s: %v", tbl, err)
		}
	}
	// Punto de control registrado y verificación sin alerta.
	if n := env.count(`SELECT count(*) FROM nexo_audit_chain_checkpoints`); n == 0 {
		t.Fatal("punto de control")
	}
	if n := env.count(`SELECT count(*) FROM nexo_alerts WHERE rule = 'audit_chain_broken'`); n != 0 {
		t.Fatalf("cadena íntegra sin alerta: %d", n)
	}
	// Romper la cadena y forzar la verificación siguiente.
	for _, q := range []string{
		`ALTER TABLE cloud_auth_audit_log DISABLE TRIGGER nexo_guard_cloud_auth_audit_log`,
		`UPDATE cloud_auth_audit_log SET action = 'x' WHERE nexo_chain_seq = 1`,
		`ALTER TABLE cloud_auth_audit_log ENABLE TRIGGER nexo_guard_cloud_auth_audit_log`,
	} {
		if _, err := env.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	env.srv.nexoMonitorTick(context.Background(), m, time.Now().Add(61*time.Minute))
	m.webhookWG.Wait()
	if n := env.count(`SELECT count(*) FROM nexo_alerts WHERE rule = 'audit_chain_broken' AND layer = 'plataforma' AND severity = 'critical'`); n != 1 {
		t.Fatalf("alerta de cadena rota: %d", n)
	}
	mu.Lock()
	joined = strings.Join(bodies, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "audit_chain_broken") {
		t.Fatalf("webhook de cadena rota: %s", joined)
	}
}
