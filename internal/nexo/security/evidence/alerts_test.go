package evidence_test

// S4-03 (RS-32, CA-S11): reglas, evaluador, reconocimiento y webhook.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

func evaluate(t testing.TB, env *kbtest.Env, rules evidence.RuleSet) []evidence.Alert {
	t.Helper()
	alerts, ran, err := evidence.EvaluateOnce(context.Background(), env.DB, rules, time.Now())
	if err != nil || !ran {
		t.Fatalf("evaluador: ran=%v err=%v", ran, err)
	}
	return alerts
}

func byRule(alerts []evidence.Alert, rule string) []evidence.Alert {
	var out []evidence.Alert
	for _, a := range alerts {
		if a.Rule == rule {
			out = append(out, a)
		}
	}
	return out
}

func TestParseRules(t *testing.T) {
	r, err := evidence.ParseRules(`{"authz_denied_burst":{"threshold":3,"window_seconds":30},"logging_stalled":{"enabled":true}}`)
	if err != nil || r[evidence.RuleAuthzDeniedBurst].Threshold != 3 || r[evidence.RuleAuthzDeniedBurst].Window != 30*time.Second || !r[evidence.RuleLoggingStalled].Enabled {
		t.Fatalf("reglas: %+v %v", r, err)
	}
	if def := evidence.DefaultRules(); def[evidence.RuleLoggingStalled].Enabled || def[evidence.RuleAuthzDeniedBurst].Threshold != 5 {
		t.Fatalf("por defecto: %+v", def)
	}
	for _, bad := range []string{`{"nope":{}}`, `{"authz_denied_burst":{"threshold":0}}`, `{"authz_denied_burst":{"severity":"x"}}`, `{`} {
		if _, err := evidence.ParseRules(bad); err == nil {
			t.Fatalf("%s debe fallar", bad)
		}
	}
	off, _ := evidence.ParseRules("off")
	for name, c := range off {
		if c.Enabled {
			t.Fatalf("off: %s activa", name)
		}
	}
}

// CA-S11 (unidad): cinco denegaciones de la misma persona en un minuto
// generan una alerta con su capa; la siguiente pasada no la repite; cuatro no
// alcanzan.
func TestAlertAuthzDeniedBurstAndLayers(t *testing.T) {
	env := kbtest.New(t)
	rules := evidence.DefaultRules()
	for i := 0; i < 5; i++ {
		env.Exec(`INSERT INTO nexo_authz_events (action, scope_type, scope_key, actor_principal_id, actor_label, client_ip, details)
			VALUES ('authz.denied', 'space', 'estandares', 77, 'luis', '10.0.0.9', '{"capability":"space.publish","reason":"role"}')`)
	}
	for i := 0; i < 4; i++ {
		env.Exec(`INSERT INTO nexo_authz_events (action, scope_type, scope_key, actor_principal_id, actor_label, details)
			VALUES ('authz.denied', 'platform', '*', 78, 'ana', '{"capability":"platform.audit.read"}')`)
	}
	for i := 0; i < 5; i++ {
		env.Exec(`INSERT INTO cloud_auth_audit_log (actor_principal_id, actor_source, project, action, outcome, reason_code, metadata)
			VALUES (NULL, 'request', 'nexo-privado', 'sync.authorize', 'denied', 'project_forbidden', '{"client_ip":"10.0.0.5"}')`)
	}
	got := byRule(evaluate(t, env, rules), evidence.RuleAuthzDeniedBurst)
	if len(got) != 2 {
		t.Fatalf("alertas de ráfaga: %+v", got)
	}
	layers := map[string]evidence.Layer{}
	for _, a := range got {
		layers[a.Subject] = a.Layer
		if a.Severity != evidence.SeverityHigh || !strings.Contains(a.Summary, "5 denegaciones") {
			t.Fatalf("alerta: %+v", a)
		}
	}
	if layers["principal:77"] != evidence.LayerKnowledge || layers["ip:10.0.0.5"] != evidence.LayerProjects {
		t.Fatalf("capas: %+v", layers)
	}
	if again := evaluate(t, env, rules); len(again) != 0 {
		t.Fatalf("la segunda pasada no repite: %+v", again)
	}
	// Otra réplica no evalúa mientras una tiene el candado.
	tx, err := env.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('nexo_alert_evaluator'))`); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := evidence.EvaluateOnce(context.Background(), env.DB, rules, time.Now()); ran || err != nil {
		t.Fatalf("con el candado tomado no debe evaluar: ran=%v err=%v", ran, err)
	}
	_ = tx.Rollback()
}

// A1 y A2: token revocado, token vencido y una PC con un token inválido fijo
// (filas resumen del agrupador) generan su alerta; un mismo token repetido no
// es una ráfaga de fallos (tokens distintos), diez distintos sí.
func TestAlertTokenRules(t *testing.T) {
	env := kbtest.New(t)
	rules := evidence.DefaultRules()
	env.Exec(`INSERT INTO cloud_auth_audit_log (actor_source, action, outcome, reason_code, metadata) VALUES
		('request', 'sync.auth', 'denied', 'token_revoked', '{"client_ip":"10.0.0.3","nexo_principal_id":"12","credential_id":"40","credential_fp":"aa"}'),
		('request', 'sync.auth', 'denied', 'token_expired', '{"client_ip":"10.0.0.4","nexo_principal_id":"13","credential_id":"41","credential_fp":"bb"}'),
		('request', 'sync.auth', 'denied', 'unknown_token', '{"client_ip":"10.0.0.21","credential_fp":"cc","user_agent":"Go-http-client/1.1"}'),
		('request', 'sync.auth', 'denied', 'unknown_token', '{"client_ip":"10.0.0.21","credential_fp":"cc","nexo_repeated":19,"nexo_user_agents":["Go-http-client/1.1"]}')`)
	alerts := evaluate(t, env, rules)
	if r := byRule(alerts, evidence.RuleRevokedTokenUsed); len(r) != 1 || r[0].Subject != "principal:12|ip:10.0.0.3" || r[0].Layer != evidence.LayerPlatform {
		t.Fatalf("revoked_token_used: %+v", r)
	}
	if r := byRule(alerts, evidence.RuleExpiredTokenUsed); len(r) != 1 || r[0].Subject != "token:41|ip:10.0.0.4" {
		t.Fatalf("expired_token_used: %+v", r)
	}
	if r := byRule(alerts, evidence.RuleInvalidTokenRepeated); len(r) != 1 || r[0].Subject != "fp:cc|ip:10.0.0.21" || r[0].Details["count"].(float64) != 20 {
		t.Fatalf("invalid_token_repeated: %+v", r)
	}
	if r := byRule(alerts, evidence.RuleAuthFailuresBurst); len(r) != 0 {
		t.Fatalf("un mismo token repetido no es una ráfaga: %+v", r)
	}
	for i := 0; i < 10; i++ {
		env.Exec(`INSERT INTO cloud_auth_audit_log (actor_source, action, outcome, reason_code, metadata)
			VALUES ('request', 'sync.auth', 'denied', 'unknown_token', jsonb_build_object('client_ip', '10.0.0.66', 'credential_fp', 'fp' || $1::int))`, i)
	}
	if r := byRule(evaluate(t, env, rules), evidence.RuleAuthFailuresBurst); len(r) != 1 || r[0].Subject != "ip:10.0.0.66" {
		t.Fatalf("auth_failures_burst: %+v", r)
	}
}

func TestAlertPlatformRoleAndSharedToken(t *testing.T) {
	env := kbtest.New(t)
	env.Exec(`INSERT INTO nexo_authz_events (action, scope_type, scope_key, target_principal_id, role_before, role_after, actor_label)
		VALUES ('binding.granted', 'platform', '*', 9, NULL, 'admin', 'jefe'), ('binding.granted', 'space', 'x', 9, NULL, 'lector', 'jefe')`)
	env.Exec(`INSERT INTO nexo_activity_log (actor, operation, project, client_ip) VALUES ('no-identificado', 'push_chunk', 'p', '10.0.0.8')`)
	alerts := evaluate(t, env, evidence.DefaultRules())
	if r := byRule(alerts, evidence.RulePlatformRoleChanged); len(r) != 1 || !strings.Contains(r[0].Summary, "ninguno -> admin") {
		t.Fatalf("platform_role_changed: %+v", r)
	}
	if r := byRule(alerts, evidence.RuleSharedTokenUsed); len(r) != 1 || r[0].Layer != evidence.LayerProjects {
		t.Fatalf("shared_token_used: %+v", r)
	}
	rules := evidence.DefaultRules()
	stalled := rules[evidence.RuleLoggingStalled]
	stalled.Enabled, stalled.Window = true, time.Millisecond
	rules[evidence.RuleLoggingStalled] = stalled
	time.Sleep(5 * time.Millisecond)
	if r := byRule(evaluate(t, env, rules), evidence.RuleLoggingStalled); len(r) != 1 {
		t.Fatalf("logging_stalled: %+v", r)
	}
}

func TestAlertAckAndList(t *testing.T) {
	env := kbtest.New(t)
	a, err := evidence.InsertAlert(context.Background(), env.DB, evidence.NewAlert{Rule: evidence.RuleAuditExportFailed, Severity: "high", Layer: evidence.LayerPlatform, Subject: "export", Summary: "x"})
	if err != nil || a == nil {
		t.Fatalf("insertar: %v", err)
	}
	if b, _ := evidence.InsertAlert(context.Background(), env.DB, evidence.NewAlert{Rule: evidence.RuleAuditExportFailed, Severity: "high", Layer: evidence.LayerPlatform, Subject: "export", Summary: "x", Cooldown: time.Hour}); b != nil {
		t.Fatal("la espera por regla y sujeto evita duplicados")
	}
	items, _, err := evidence.ListAlerts(context.Background(), env.DB, evidence.AlertQuery{Unacked: true})
	if err != nil || len(items) != 1 || items[0].Ack != nil {
		t.Fatalf("sin reconocer: %+v %v", items, err)
	}
	ack, err := evidence.AckAlert(context.Background(), env.DB, a.ID, evidence.AckActor{PrincipalID: "5", Label: "auditora"}, "revisado")
	if err != nil || ack.Label != "auditora" || ack.Note != "revisado" {
		t.Fatalf("ack: %+v %v", ack, err)
	}
	if _, err := evidence.AckAlert(context.Background(), env.DB, a.ID, evidence.AckActor{Label: "x"}, ""); err != evidence.ErrAlertAcked {
		t.Fatalf("segundo ack: %v", err)
	}
	if _, err := evidence.AckAlert(context.Background(), env.DB, a.ID+100, evidence.AckActor{Label: "x"}, ""); err != evidence.ErrAlertNotFound {
		t.Fatalf("inexistente: %v", err)
	}
	if items, _, _ := evidence.ListAlerts(context.Background(), env.DB, evidence.AlertQuery{Unacked: true}); len(items) != 0 {
		t.Fatalf("reconocida no aparece en unacked: %+v", items)
	}
	if items, _, _ := evidence.ListAlerts(context.Background(), env.DB, evidence.AlertQuery{}); len(items) != 1 || items[0].Ack == nil {
		t.Fatalf("lista con reconocimiento: %+v", items)
	}
}

// CA-S11 (webhook): JSON compatible con Slack y Teams, reintentos, sin
// secretos en el cuerpo.
func TestWebhookRetriesAndPayload(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = raw
		mu.Unlock()
		if n < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	wh, err := evidence.NewWebhook(srv.URL + "/hook?secret=abc")
	if err != nil {
		t.Fatal(err)
	}
	wh.WithBackoff(0, 0, 0)
	a := evidence.Alert{ID: 7, Rule: evidence.RuleRevokedTokenUsed, Severity: "warning", Layer: evidence.LayerPlatform,
		Subject: "principal:12|ip:10.0.0.3", Summary: "uso de un token revocado", Details: map[string]any{"credential_fp": "secreto-no"}, OccurredAt: time.Now()}
	if err := wh.Send(context.Background(), a); err != nil || calls.Load() != 3 {
		t.Fatalf("envío con reintentos: %v (%d llamadas)", err, calls.Load())
	}
	var p map[string]any
	mu.Lock()
	_ = json.Unmarshal(body, &p)
	raw := string(body)
	mu.Unlock()
	if !strings.Contains(p["text"].(string), "revoked_token_used") || p["summary"] == nil || p["nexo_alert"].(map[string]any)["layer"] != "plataforma" {
		t.Fatalf("cuerpo: %s", raw)
	}
	if strings.Contains(raw, "secreto-no") || strings.Contains(raw, "credential_fp") {
		t.Fatalf("el cuerpo no lleva detalles: %s", raw)
	}
	if wh.Host() == "" || strings.Contains(wh.Host(), "secret") {
		t.Fatalf("host: %q", wh.Host())
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer failing.Close()
	wf, _ := evidence.NewWebhook(failing.URL + "/x?token=zzz")
	wf.WithBackoff(0, 0)
	if err := wf.Send(context.Background(), a); err == nil || strings.Contains(err.Error(), "zzz") {
		t.Fatalf("error sin la URL: %v", err)
	}
	if _, err := evidence.NewWebhook("ftp://x"); err == nil {
		t.Fatal("solo http(s)")
	}
}
