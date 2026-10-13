package cloudserver

// Nexo IA · SG-10 — ciclo de vida de una cuenta de servicio por HTTP contra
// Postgres real (CLOUDSTORE_TEST_DSN) con la resolución real de tokens:
// alta, rol publicador, token, publicación en un espacio con el token de la
// cuenta, rotación con y sin gracia, revocación, deshabilitar y habilitar,
// denegaciones (no admin, R-10, sin caducidad sin superadmin), un evento por
// acción y el token nunca en logs, eventos ni respuestas posteriores.

import (
	"bytes"
	"encoding/json"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// nexoSafeBuffer es un buffer para log.SetOutput (los handlers registran desde
// goroutines).
type nexoSafeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *nexoSafeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *nexoSafeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestNexoServiceAccountLifecycle(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	logs := &nexoSafeBuffer{}
	log.SetOutput(io.MultiWriter(logs, os.Stderr))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	const ip = "10.0.0.20"
	do := func(method, path, who, body string, want int) map[string]any {
		t.Helper()
		rec := env.req(method, path, who, body, ip)
		if rec.Code != want {
			t.Fatalf("%s %s (%s): %d, quería %d: %s", method, path, who, rec.Code, want, rec.Body.String())
		}
		if strings.TrimSpace(rec.Body.String()) == "" || !strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "{") {
			return nil
		}
		return nexoJSON(t, rec)
	}

	// ─── Alta: solo platform.users.manage ────────────────────────────────────
	do("POST", "/nexo/authz/v1/service-accounts", "ana", `{"slug":"ci-wiki","owner":"ana"}`, http.StatusForbidden)
	if n := env.count(`SELECT count(*) FROM nexo_authz_events WHERE action = 'authz.denied' AND actor_principal_id = $1::bigint AND details->>'action' = 'service_account.created'`, env.ids["ana"]); n != 1 {
		t.Fatalf("denegación del alta auditada: %d", n)
	}
	do("POST", "/nexo/authz/v1/service-accounts", "pedro", `{"slug":"ci-wiki"}`, http.StatusBadRequest)                             // sin responsable
	do("POST", "/nexo/authz/v1/service-accounts", "pedro", `{"slug":"ci-wiki","owner":"`+env.ids["ci"]+`"}`, http.StatusBadRequest) // responsable no humano
	do("POST", "/nexo/authz/v1/service-accounts", "pedro", `{"slug":"CI Wiki","owner":"ana"}`, http.StatusBadRequest)               // slug inválido
	do("POST", "/nexo/authz/v1/service-accounts", "pedro", `{"slug":"carla","owner":"ana"}`, http.StatusConflict)                   // choca con un usuario
	sa := do("POST", "/nexo/authz/v1/service-accounts", "pedro", `{"slug":"ci-wiki","description":"Publica la wiki desde el pipeline","owner":"ana"}`, http.StatusCreated)
	saID, _ := sa["principal_id"].(string)
	if saID == "" || sa["owner_username"] != "ana" || sa["enabled"] != true || sa["created_by_principal_id"] != env.ids["pedro"] {
		t.Fatalf("alta: %v", sa)
	}
	do("POST", "/nexo/authz/v1/service-accounts", "pedro", `{"slug":"ci-wiki","owner":"ana"}`, http.StatusConflict)
	var kind string
	if err := env.db.QueryRow(`SELECT kind FROM cloud_principals WHERE id = $1::bigint`, saID).Scan(&kind); err != nil || kind != "service_account" {
		t.Fatalf("principal: %q %v", kind, err)
	}

	// ─── Rol en un espacio y R-10 ────────────────────────────────────────────
	do("POST", "/nexo/kb/v1/spaces", "carla", `{"slug":"wiki","name":"Wiki del equipo","owner":"ana"}`, http.StatusCreated)
	do("PUT", "/nexo/authz/v1/scopes/space/wiki/members/ci-wiki", "ana", `{"role":"responsable"}`, http.StatusUnprocessableEntity)
	do("PUT", "/nexo/authz/v1/platform/members/ci-wiki", "carla", `{"role":"admin"}`, http.StatusUnprocessableEntity)
	if _, err := env.db.Exec(`INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role) VALUES ($1::bigint, 'platform', '*', 'auditor')`, saID); err == nil || !strings.Contains(err.Error(), "R-10") {
		t.Fatalf("trigger R-10: %v", err)
	}
	do("PUT", "/nexo/authz/v1/scopes/space/wiki/members/ci-wiki", "ana", `{"role":"publicador"}`, http.StatusOK)

	// ─── Tokens: con caducidad; sin caducidad solo superadmin ────────────────
	do("POST", "/nexo/authz/v1/service-accounts/ci-wiki/tokens", "ana", `{"name":"ci-github"}`, http.StatusForbidden)
	do("POST", "/nexo/authz/v1/service-accounts/ci-wiki/tokens", "pedro", `{"name":"ci-github","expires_in_days":0}`, http.StatusForbidden)
	issued := do("POST", "/nexo/authz/v1/service-accounts/ci-wiki/tokens", "pedro", `{"name":"ci-github","expires_in_days":30}`, http.StatusCreated)
	raw1, _ := issued["raw_token"].(string)
	tok1 := issued["token"].(map[string]any)
	id1 := tok1["id"].(string)
	if raw1 == "" || tok1["no_expiry"] != false || tok1["scope"] != "write" {
		t.Fatalf("emisión: %v", tok1)
	}
	forever := do("POST", "/nexo/authz/v1/service-accounts/ci-wiki/tokens", "carla", `{"name":"bot-sin-caducidad","expires_in_days":0}`, http.StatusCreated)
	rawForever, _ := forever["raw_token"].(string)
	if forever["token"].(map[string]any)["no_expiry"] != true {
		t.Fatalf("sin caducidad: %v", forever)
	}
	// La ruta general de tokens acepta el slug.
	general := do("POST", "/nexo/authz/v1/tokens", "pedro", `{"principal":"ci-wiki","name":"ci-general","expires_in_days":7,"scope":"read"}`, http.StatusCreated)
	rawGeneral, _ := general["raw_token"].(string)

	// ─── Publicar en el espacio con el token de la cuenta ────────────────────
	publish := func(who, title string, want int) {
		t.Helper()
		batch := map[string]any{"documents": []map[string]any{{"path": "guias/" + title + ".md",
			"content": "---\ncategory: lineamiento\ntitle: " + title + "\n---\n# " + title + "\n\nTexto de " + title + ".\n"}}}
		raw, _ := json.Marshal(batch)
		do("POST", "/nexo/kb/v1/spaces/wiki/documents:batch", who, string(raw), want)
	}
	publish("raw:"+raw1, "despliegue", http.StatusOK)
	publish("raw:"+rawGeneral, "lectura", http.StatusForbidden) // token read: no publica

	// ─── Último uso: cuándo, desde qué IP y con qué cliente ──────────────────
	detail := do("GET", "/nexo/authz/v1/service-accounts/ci-wiki", "pedro", "", http.StatusOK)
	var t1 map[string]any
	for _, x := range detail["tokens"].([]any) {
		if m := x.(map[string]any); m["id"] == id1 {
			t1 = m
		}
	}
	if t1 == nil || t1["last_used_at"] == nil {
		t.Fatalf("último uso: %v", detail)
	}
	seen, _ := t1["last_seen"].(map[string]any)
	if seen == nil || seen["ip"] != ip || seen["source"] != "kb" || seen["user_agent"] != "nexo-sec-test/1.0" {
		t.Fatalf("última acción del token: %v", t1)
	}
	if b := detail["bindings"].([]any); len(b) != 1 || b[0].(map[string]any)["role"] != "publicador" {
		t.Fatalf("roles de la cuenta: %v", detail["bindings"])
	}
	list := do("GET", "/nexo/authz/v1/service-accounts?enabled=true", "pedro", "", http.StatusOK)
	if items := list["items"].([]any); len(items) != 1 || items[0].(map[string]any)["slug"] != "ci-wiki" {
		t.Fatalf("listado: %v", list)
	}
	do("GET", "/nexo/authz/v1/service-accounts", "ana", "", http.StatusForbidden)
	members := do("GET", "/nexo/authz/v1/scopes/space/wiki/members", "ana", "", http.StatusOK)
	if !strings.Contains(mustJSON(t, members), `"username":"ci-wiki"`) {
		t.Fatalf("miembros del espacio con el slug: %v", members)
	}

	// ─── Rotación sin gracia: el anterior queda revocado ─────────────────────
	do("POST", "/nexo/authz/v1/service-accounts/ci-wiki/tokens/"+id1+":rotate", "ana", `{}`, http.StatusForbidden)
	rot := do("POST", "/nexo/authz/v1/service-accounts/ci-wiki/tokens/"+id1+":rotate", "pedro", `{"expires_in_days":30}`, http.StatusCreated)
	raw2, _ := rot["raw_token"].(string)
	id2 := rot["token"].(map[string]any)["id"].(string)
	if raw2 == "" || raw2 == raw1 || rot["old_token"].(map[string]any)["revoked"] != true || rot["token"].(map[string]any)["name"] != "ci-github" {
		t.Fatalf("rotación: %v", rot)
	}
	publish("raw:"+raw1, "viejo", http.StatusUnauthorized)
	publish("raw:"+raw2, "nuevo", http.StatusOK)
	do("POST", "/nexo/authz/v1/service-accounts/ci-wiki/tokens/"+id1+":rotate", "pedro", `{}`, http.StatusConflict) // ya revocado

	// ─── Rotación con gracia: el anterior sigue hasta vencer; luego se revoca ─
	do("POST", "/nexo/authz/v1/service-accounts/ci-wiki/tokens/"+id2+":rotate", "pedro", `{"grace_minutes":99999}`, http.StatusBadRequest)
	rot = do("POST", "/nexo/authz/v1/service-accounts/ci-wiki/tokens/"+id2+":rotate", "pedro", `{"grace_minutes":60}`, http.StatusCreated)
	raw3, _ := rot["raw_token"].(string)
	expRaw, _ := rot["old_token"].(map[string]any)["expires_at"].(string)
	exp, err := time.Parse(time.RFC3339Nano, expRaw)
	if err != nil || exp.Before(time.Now().Add(55*time.Minute)) || exp.After(time.Now().Add(65*time.Minute)) {
		t.Fatalf("gracia del token anterior: %v (%v)", rot, err)
	}
	publish("raw:"+raw2, "en-gracia", http.StatusOK)
	do("POST", "/nexo/authz/v1/service-accounts/ci-wiki/tokens/"+id2+":revoke", "pedro", `{"reason":"fin de la gracia"}`, http.StatusOK)
	publish("raw:"+raw2, "tras-revocar", http.StatusUnauthorized)
	publish("raw:"+raw3, "tercero", http.StatusOK)
	var anaToken string
	if err := env.db.QueryRow(`SELECT id::text FROM cloud_principal_tokens WHERE principal_id = $1::bigint LIMIT 1`, env.ids["ana"]).Scan(&anaToken); err != nil {
		t.Fatal(err)
	}
	do("POST", "/nexo/authz/v1/service-accounts/ci-wiki/tokens/"+anaToken+":revoke", "pedro", `{}`, http.StatusNotFound) // token de otra persona
	do("POST", "/nexo/authz/v1/service-accounts/ci-wiki/tokens/"+id1+":hack", "pedro", `{}`, http.StatusNotFound)

	// ─── Deshabilitar: todos los tokens se rechazan; habilitar los recupera ──
	do("POST", "/nexo/authz/v1/service-accounts/ci-wiki:disable", "ana", "", http.StatusForbidden)
	dis := do("POST", "/nexo/authz/v1/service-accounts/ci-wiki:disable", "pedro", "", http.StatusOK)
	if dis["changed"] != true {
		t.Fatalf("deshabilitar: %v", dis)
	}
	publish("raw:"+raw3, "deshabilitada", http.StatusUnauthorized)
	publish("raw:"+rawForever, "deshabilitada-2", http.StatusUnauthorized)
	do("POST", "/nexo/authz/v1/service-accounts/ci-wiki/tokens", "pedro", `{"name":"otro"}`, http.StatusConflict)
	do("POST", "/nexo/authz/v1/service-accounts/ci-wiki:enable", "pedro", "", http.StatusOK)
	publish("raw:"+raw3, "habilitada", http.StatusOK)

	// Cambio de responsable.
	upd := do("PATCH", "/nexo/authz/v1/service-accounts/ci-wiki", "pedro", `{"owner":"luis","description":"Wiki y manuales"}`, http.StatusOK)
	if upd["owner_username"] != "luis" || upd["description"] != "Wiki y manuales" {
		t.Fatalf("cambio: %v", upd)
	}

	// ─── Un evento por acción ────────────────────────────────────────────────
	for action, want := range map[string]int{
		"service_account.created": 1, "service_account.updated": 1, "service_account.disabled": 1, "service_account.enabled": 1,
		"service_account.token_issued": 3, "service_account.token_rotated": 2, "service_account.token_revoked": 1,
	} {
		if n := env.count(`SELECT count(*) FROM nexo_authz_events WHERE action = $1 AND target_principal_id = $2::bigint`, action, saID); n != want {
			t.Errorf("eventos %s: %d, quería %d", action, n, want)
		}
	}
	if n := env.count(`SELECT count(*) FROM nexo_authz_events WHERE action = 'binding.granted' AND target_principal_id = $1::bigint AND role_after = 'publicador'`, saID); n != 1 {
		t.Errorf("evento del rol publicador: %d", n)
	}
	if n := env.count(`SELECT count(*) FROM nexo_authz_events WHERE action = 'authz.denied' AND details->>'rule' = 'service_account_role'`); n < 2 {
		t.Errorf("denegaciones R-10 auditadas: %d", n)
	}

	// ─── El token en claro nunca queda en logs, eventos ni respuestas ────────
	raws := []string{raw1, raw2, raw3, rawForever, rawGeneral}
	again := env.req("GET", "/nexo/authz/v1/service-accounts/ci-wiki", "pedro", "", ip).Body.String() +
		env.req("GET", "/nexo/authz/v1/service-accounts", "pedro", "", ip).Body.String() +
		env.req("GET", "/nexo/authz/v1/tokens?principal=ci-wiki", "pedro", "", ip).Body.String() +
		env.req("GET", "/nexo/authz/v1/events?action=service_account.*", "carla", "", ip).Body.String()
	var stored string
	if err := env.db.QueryRow(`SELECT COALESCE(string_agg(details::text, ' '), '') FROM nexo_authz_events`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var audit string
	if err := env.db.QueryRow(`SELECT COALESCE(string_agg(metadata::text, ' '), '') FROM cloud_auth_audit_log`).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	var kb string
	if err := env.db.QueryRow(`SELECT COALESCE(string_agg(COALESCE(details::text, ''), ' '), '') FROM nexo_kb_events`).Scan(&kb); err != nil {
		t.Fatal(err)
	}
	for i, raw := range raws {
		if raw == "" {
			t.Fatalf("token %d vacío", i)
		}
		for where, text := range map[string]string{"logs": logs.String(), "respuestas": again, "nexo_authz_events": stored, "cloud_auth_audit_log": audit, "nexo_kb_events": kb} {
			if strings.Contains(text, raw) {
				t.Fatalf("el token %d aparece en %s", i, where)
			}
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Panel: listado, alta, token mostrado una vez, rotación, revocación,
// deshabilitar; un miembro no ve la página y sin CSRF no hay cambios.
func TestNexoServiceAccountPanel(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	cookie := managedDashboardLogin(t, env.srv, env.tokens["carla"], false)
	page := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/admin/service-accounts", cookie)
	body := page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, "Cuentas de servicio") || !strings.Contains(body, "Crear cuenta de servicio") {
		t.Fatalf("página: %d %s", page.Code, body)
	}
	m := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("sin token CSRF")
	}
	csrf := url.QueryEscape(html.UnescapeString(m[1]))

	if rec := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/service-accounts", "slug=ci-panel&owner=ana", cookie, false); rec.Code != http.StatusForbidden {
		t.Fatalf("alta sin CSRF: %d", rec.Code)
	}
	rec := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/service-accounts", "csrf_token="+csrf+"&slug=ci-panel&owner=ana&description=Bot+del+panel", cookie, false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Cuenta ci-panel creada") {
		t.Fatalf("alta: %d %s", rec.Code, rec.Body.String())
	}
	rec = performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/service-accounts", "csrf_token="+csrf+"&slug=ci-panel&owner=ana", cookie, false)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Ya existe") {
		t.Fatalf("alta repetida: %d", rec.Code)
	}

	// Token: se muestra una vez y la página siguiente no lo trae.
	rec = performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/service-accounts/ci-panel/token", "csrf_token="+csrf+"&name=ci-github&expires_in_days=30&scope=write", cookie, false)
	tm := regexp.MustCompile(`<code class="nexo-sa-token">([^<]+)</code>`).FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || tm == nil || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("token en el panel: %d %s", rec.Code, rec.Body.String())
	}
	raw := html.UnescapeString(tm[1])
	if r := env.req("GET", "/nexo/authz/v1/me", "raw:"+raw, "", "10.0.0.30"); r.Code != http.StatusOK {
		t.Fatalf("el token del panel no sirve: %d", r.Code)
	}
	again := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/admin/service-accounts?sa=ci-panel", cookie).Body.String()
	if strings.Contains(again, raw) || !strings.Contains(again, "ci-github") || !strings.Contains(again, "Activo") || strings.Contains(again, "<td>Activo</td><td>-</td>") {
		t.Fatalf("detalle después de emitir: %s", again)
	}
	var tokenID string
	if err := env.db.QueryRow(`SELECT t.id::text FROM cloud_principal_tokens t JOIN nexo_service_accounts sa ON sa.principal_id = t.principal_id WHERE sa.slug = 'ci-panel'`).Scan(&tokenID); err != nil {
		t.Fatal(err)
	}
	// Rotar sin gracia: token nuevo y el anterior revocado.
	rec = performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/service-accounts/ci-panel/rotate", "csrf_token="+csrf+"&token_id="+tokenID+"&grace_minutes=0", cookie, false)
	tm = regexp.MustCompile(`<code class="nexo-sa-token">([^<]+)</code>`).FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || tm == nil || !strings.Contains(rec.Body.String(), "anterior quedó revocado") {
		t.Fatalf("rotar en el panel: %d %s", rec.Code, rec.Body.String())
	}
	raw2 := html.UnescapeString(tm[1])
	if r := env.req("GET", "/nexo/authz/v1/me", "raw:"+raw, "", "10.0.0.30"); r.Code != http.StatusUnauthorized {
		t.Fatalf("token rotado sigue sirviendo: %d", r.Code)
	}
	// Deshabilitar: el token nuevo deja de servir.
	if rec = performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/service-accounts/ci-panel/disable", "csrf_token="+csrf, cookie, false); rec.Code != http.StatusOK {
		t.Fatalf("deshabilitar: %d", rec.Code)
	}
	if r := env.req("GET", "/nexo/authz/v1/me", "raw:"+raw2, "", "10.0.0.30"); r.Code != http.StatusUnauthorized {
		t.Fatalf("cuenta deshabilitada con token vigente: %d", r.Code)
	}
	if rec = performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/service-accounts/ci-panel/enable", "csrf_token="+csrf, cookie, false); rec.Code != http.StatusOK {
		t.Fatalf("habilitar: %d", rec.Code)
	}
	var newID string
	if err := env.db.QueryRow(`SELECT t.id::text FROM cloud_principal_tokens t JOIN nexo_service_accounts sa ON sa.principal_id = t.principal_id WHERE sa.slug = 'ci-panel' AND t.revoked_at IS NULL`).Scan(&newID); err != nil {
		t.Fatal(err)
	}
	if rec = performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/service-accounts/ci-panel/revoke", "csrf_token="+csrf+"&token_id="+newID, cookie, false); rec.Code != http.StatusOK {
		t.Fatalf("revocar: %d", rec.Code)
	}
	if r := env.req("GET", "/nexo/authz/v1/me", "raw:"+raw2, "", "10.0.0.30"); r.Code != http.StatusUnauthorized {
		t.Fatalf("token revocado sigue sirviendo: %d", r.Code)
	}
	for _, action := range []string{"service_account.created", "service_account.token_issued", "service_account.token_rotated", "service_account.disabled", "service_account.enabled", "service_account.token_revoked"} {
		if n := env.count(`SELECT count(*) FROM nexo_authz_events e JOIN nexo_service_accounts sa ON sa.principal_id = e.target_principal_id WHERE sa.slug = 'ci-panel' AND e.action = $1`, action); n != 1 {
			t.Errorf("evento %s desde el panel: %d", action, n)
		}
	}

	// Un miembro no ve la página.
	member := managedDashboardLogin(t, env.srv, env.tokens["ana"], false)
	if rec := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/admin/service-accounts", member); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "ci-panel") {
		t.Fatalf("miembro en la página: %d", rec.Code)
	}
}
