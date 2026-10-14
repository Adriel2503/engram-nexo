package cloudserver

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

func nexoActivationJSON(t *testing.T, r *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if json.Unmarshal(r.Body.Bytes(), &v) != nil {
		t.Fatal("respuesta JSON inválida (contenido oculto)")
	}
	return v
}
func nexoRedeemForTest(t *testing.T, e *nexoSecEnv, r *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	v := nexoActivationJSON(t, r)
	if code, ok := v["activation_code"].(string); ok {
		if v["raw_token"] != nil || v["credential_type"] != "activation_code" {
			t.Fatal("el administrador recibió una credencial indebida")
		}
		b, _ := json.Marshal(map[string]string{"code": code})
		r = e.req("POST", "/nexo/authz/v1/activate", "", string(b), "10.77.1.2")
		if r.Code != 201 {
			t.Fatalf("canje: estado %d (contenido oculto)", r.Code)
		}
	}
	return r
}
func nexoIssueForTest(t *testing.T, e *nexoSecEnv, route, body string) (string, string) {
	t.Helper()
	r := e.req("POST", route, "carla", body, "10.77.1.1")
	if r.Code != 201 {
		t.Fatalf("emisión: estado %d (contenido oculto)", r.Code)
	}
	v := nexoActivationJSON(t, r)
	if v["raw_token"] != nil || v["token"] != nil || v["credential_type"] != "activation_code" {
		t.Fatal("la emisión entregó token ajeno")
	}
	code, _ := v["activation_code"].(string)
	if code == "" {
		t.Fatal("falta código (valor oculto)")
	}
	scan := newNexoLeakScanner()
	scan.addToken("activación", code, nil)
	if len(scan.find(r.Body.String(), code)) != 0 {
		t.Fatal("fuga en emisión")
	}
	return code, v["activation"].(map[string]any)["id"].(string)
}
func TestNexoActivationLifecyclePG(t *testing.T) {
	e := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	t.Setenv("NEXO_ACTIVATION_TTL", "48h")
	code, id := nexoIssueForTest(t, e, "/nexo/authz/v1/tokens", `{"principal":"ana","name":"equipo","scope":"read","expires_in_days":15}`)
	if r := e.req("GET", "/nexo/authz/v1/me", "raw:"+code, "", "10.77.1.3"); r.Code != 401 {
		t.Fatal("el código funciona como token")
	}
	if e.count("SELECT count(*) FROM cloud_principal_tokens WHERE name='equipo'") != 0 {
		t.Fatal("se emitió token antes del canje")
	}
	body, _ := json.Marshal(map[string]string{"code": code})
	redeemed := e.req("POST", "/nexo/authz/v1/activate", "", string(body), "10.77.1.2")
	if redeemed.Code != 201 {
		t.Fatalf("canje feliz: %d", redeemed.Code)
	}
	v := nexoActivationJSON(t, redeemed)
	raw, _ := v["raw_token"].(string)
	tok := v["token"].(map[string]any)
	if raw == "" || tok["principal_id"] != e.ids["ana"] || tok["scope"] != "read" {
		t.Fatal("token canjeado incorrecto")
	}
	expires, _ := time.Parse(time.RFC3339Nano, tok["expires_at"].(string))
	if time.Until(expires) < 15*24*time.Hour-time.Minute || time.Until(expires) > 15*24*time.Hour+time.Minute {
		t.Fatal("vida del token incorrecta")
	}
	scan := newNexoLeakScanner()
	scan.addToken("código", code, nil)
	scan.addToken("token", raw, e.hasher.Hash)
	if len(scan.find(redeemed.Body.String(), raw)) != 0 {
		t.Fatal("fuga en canje")
	}
	if r := e.req("POST", "/nexo/authz/v1/activate", "", string(body), "10.77.1.2"); r.Code != 400 || nexoActivationJSON(t, r)["error_code"] != "activation_used" {
		t.Fatal("doble canje permitido")
	}
	if e.count("SELECT count(*) FROM nexo_alerts WHERE rule='activation_repeated'") != 1 {
		t.Fatal("sin alerta de reutilización")
	}
	if e.count("SELECT count(*) FROM cloud_auth_audit_log WHERE action='nexo.activation.redeemed' AND metadata->>'client_ip'='10.77.1.2' AND metadata->>'user_agent'='nexo-sec-test/1.0'") != 1 {
		t.Fatal("canje sin origen auditado")
	}
	if e.count("SELECT count(*) FROM nexo_activation_codes WHERE id=$1 AND used_at IS NOT NULL AND token_id IS NOT NULL", id) != 1 {
		t.Fatal("código no consumido")
	}
	for _, state := range []string{"expired", "revoked", "disabled"} {
		c, cid := nexoIssueForTest(t, e, "/admin/users/"+e.ids["ana"]+"/tokens", `{"name":"rechazo","scope":"write","expires_in_days":7}`)
		switch state {
		case "expired":
			if _, err := e.db.Exec("UPDATE nexo_activation_codes SET expires_at=now()-interval '1 second' WHERE id=$1", cid); err != nil {
				t.Fatal("no se pudo vencer código")
			}
		case "revoked":
			b, _ := json.Marshal(map[string]string{"principal": "ana"})
			if r := e.req("POST", "/nexo/authz/v1/activations/"+cid+"/revoke", "carla", string(b), "10.77.1.1"); r.Code != 200 {
				t.Fatal("revocación fallida")
			}
		case "disabled":
			if err := e.cs.SetHumanUserEnabled(t.Context(), e.ids["ana"], false); err != nil {
				t.Fatal("deshabilitación fallida")
			}
		}
		b, _ := json.Marshal(map[string]string{"code": c})
		r := e.req("POST", "/nexo/authz/v1/activate", "", string(b), "10.77.1.2")
		if r.Code != 400 || nexoActivationJSON(t, r)["error_code"] != "activation_"+state {
			t.Fatal("código rechazado sin motivo correcto")
		}
		scan.addToken(state, c, nil)
		if len(scan.find(r.Body.String())) != 0 {
			t.Fatal("fuga en rechazo")
		}
	}
	pending := e.req("GET", "/nexo/authz/v1/activations?principal=ana", "carla", "", "10.77.1.1")
	if pending.Code != 200 || len(scan.find(pending.Body.String())) != 0 {
		t.Fatal("fuga en pendientes")
	}
	audit := e.req("GET", "/nexo/admin/audit?action=nexo.activation.*", "carla", "", "10.77.1.1")
	if audit.Code != 200 || len(scan.find(audit.Body.String())) != 0 {
		t.Fatal("fuga en auditoría")
	}
}
func TestNexoActivationConcurrentPG(t *testing.T) {
	e := newNexoSecEnv(t, nil)
	code, _ := nexoIssueForTest(t, e, "/nexo/authz/v1/tokens", `{"principal":"ana","name":"simultaneo"}`)
	body, _ := json.Marshal(map[string]string{"code": code})
	var wg sync.WaitGroup
	results := make(chan int, 2)
	start := make(chan struct{})
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- e.req("POST", "/nexo/authz/v1/activate", "", string(body), "10.77.2.1").Code
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for status := range results {
		if status == 201 {
			successes++
		} else if status != 400 {
			t.Fatalf("estado inesperado: %d", status)
		}
	}
	if successes != 1 || e.count("SELECT count(*) FROM cloud_principal_tokens WHERE name='simultaneo'") != 1 {
		t.Fatal("dos canjes ganaron")
	}
}
func TestNexoActivationAttemptLimitPG(t *testing.T) {
	e := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off", security.AuthLockoutEnv: `{"max_failures":2,"window_seconds":300,"lockout_seconds":900}`})
	c, err := security.GenerateActivationCode()
	if err != nil {
		t.Fatal("generación fallida")
	}
	b, _ := json.Marshal(map[string]string{"code": c})
	for range 2 {
		if e.req("POST", "/nexo/authz/v1/activate", "", string(b), "10.77.3.1").Code != 400 {
			t.Fatal("fallo no registrado")
		}
	}
	if r := e.req("POST", "/nexo/authz/v1/activate", "", string(b), "10.77.3.1"); r.Code != 429 || r.Header().Get("Retry-After") == "" {
		t.Fatal("sin bloqueo por repetición")
	}
	e2 := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: `{"login":{"ip":1}}`})
	if r := e2.req("POST", "/nexo/authz/v1/activate", "", string(b), "10.77.4.1"); r.Code != 400 {
		t.Fatal("primer intento no registrado")
	}
	if e2.req("POST", "/nexo/authz/v1/activate", "", string(b), "10.77.4.1").Code != 429 {
		t.Fatal("sin límite login")
	}
}
func TestNexoActivationPanelAndExceptionsPG(t *testing.T) {
	e := newAuditPanelEnv(t)
	route := "/dashboard/admin/users/" + e.ids["ana"] + "/tokens"
	csrf := e.csrf("carla", "/dashboard/admin/users/"+e.ids["ana"])
	r := e.post("carla", route, "csrf_token="+csrf+"&name=panel&scope=read&expires_in_days=7")
	if r.Code != 200 || nexoFullTokenRe.Match(r.Body.Bytes()) {
		t.Fatal("panel entregó token ajeno")
	}
	code := nexoActivationCodeRe.FindString(r.Body.String())
	if code == "" {
		t.Fatal("panel sin código")
	}
	delete(e.bodies, "POST carla "+route)
	e.get("carla", "/dashboard/admin/users/"+e.ids["ana"])
	e.assertNoLeaks()
	pub := httptest.NewRequest("GET", "/dashboard/activate", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, pub)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Activar mi cuenta") {
		t.Fatal("pantalla no es pública")
	}
	pub = httptest.NewRequest("POST", "/dashboard/activate", strings.NewReader("code="+code))
	pub.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, pub)
	raw := nexoFullTokenRe.FindString(rec.Body.String())
	if rec.Code != 200 || raw == "" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("canje del panel fallido")
	}
	scan := newNexoLeakScanner()
	scan.addToken("código", code, nil)
	scan.addToken("token", raw, e.hasher.Hash)
	if len(scan.find(rec.Body.String(), raw)) != 0 {
		t.Fatal("fuga del panel")
	}
	for _, body := range []string{`{"principal":"carla","name":"propio"}`, `{"principal":"` + e.ids["ci"] + `","name":"bot"}`} {
		r := e.req("POST", "/nexo/authz/v1/tokens", "carla", body, "10.77.5.1")
		v := nexoActivationJSON(t, r)
		if r.Code != 201 || v["raw_token"] == nil || v["activation_code"] != nil {
			t.Fatal("excepción directa cambió")
		}
	}
	// Revocación del panel con CSRF: no se revela el valor al listar.
	_, cid := nexoIssueForTest(t, e.nexoSecEnv, "/nexo/authz/v1/tokens", `{"principal":"ana","name":"pendiente"}`)
	pendingPath := "/dashboard/admin/users/" + e.ids["ana"]
	csrf = e.csrf("carla", pendingPath)
	r = e.post("carla", pendingPath+"/activations/"+cid+"/revoke", "csrf_token="+csrf)
	if r.Code != 303 {
		t.Fatal("no se revocó desde el panel")
	}
}

// Un error público mal formado no repite la credencial recibida.
func TestNexoActivationParsingNoLeaks(t *testing.T) {
	s := New(&fakeStore{}, nil, 0)
	c, err := security.GenerateActivationCode()
	if err != nil {
		t.Fatal("generación fallida")
	}
	r := httptest.NewRequest("POST", "/nexo/authz/v1/activate", strings.NewReader("{"+c))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != 400 || regexp.MustCompile(regexp.QuoteMeta(c)).Match(rec.Body.Bytes()) {
		t.Fatal("fuga de código en validación")
	}
}
