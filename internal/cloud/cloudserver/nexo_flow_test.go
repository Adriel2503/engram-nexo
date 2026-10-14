package cloudserver

// Nexo IA · NX-07 fase S5 — corrida de punta a punta en el proceso (servidor
// real, Postgres de CLOUDSTORE_TEST_DSN, resolución real de tokens) que
// comparten la revisión de respuestas (S5-01) y la prueba de secretos en
// registros (S5-03): login del panel, emisión de tokens, sync push/pull,
// publicación y búsqueda en la base de conocimiento, lecturas de /admin/*,
// /nexo/* y del panel, intentos con tokens inválidos y revocación.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// nexoFlowResponse es una respuesta de la corrida.
type nexoFlowResponse struct {
	label string // "METODO ruta"
	code  int
	ctype string
	body  []byte
	allow []string // valores esperados en esta respuesta (token recién emitido)
}

type nexoFlow struct {
	env       *nexoSecEnv
	responses []nexoFlowResponse
	scanner   *nexoLeakScanner
	cookies   []string // valores de cookies de sesión del panel
	tokenIDs  map[string]string
}

func (f *nexoFlow) do(method, path, who, body string, want ...int) nexoFlowResponse {
	f.env.t.Helper()
	rec := f.env.req(method, path, who, body, "10.95.0.1")
	return f.record(method+" "+path, rec, want...)
}

func (f *nexoFlow) record(label string, rec *httptest.ResponseRecorder, want ...int) nexoFlowResponse {
	f.env.t.Helper()
	if len(want) > 0 {
		ok := false
		for _, w := range want {
			ok = ok || rec.Code == w
		}
		if !ok {
			f.env.t.Fatalf("%s: %d, want %v: %s", label, rec.Code, want, rec.Body.String())
		}
	}
	resp := nexoFlowResponse{label: label, code: rec.Code, ctype: rec.Header().Get("Content-Type"), body: rec.Body.Bytes()}
	f.responses = append(f.responses, resp)
	return resp
}

func (f *nexoFlow) panel(method, path string, cookie *http.Cookie, form string) nexoFlowResponse {
	f.env.t.Helper()
	var rec *httptest.ResponseRecorder
	if method == http.MethodGet {
		rec = performDashboardRequest(f.env.srv, method, path, cookie)
	} else {
		rec = performDashboardForm(f.env.srv, method, path, form, cookie, false)
	}
	return f.record("PANEL "+method+" "+path, rec)
}

// issued registra un token emitido por HTTP (la respuesta lo contiene).
func (f *nexoFlow) issued(person string, resp nexoFlowResponse) string {
	f.env.t.Helper()
	var out struct {
		Raw   string         `json:"raw_token"`
		Token map[string]any `json:"token"`
	}
	if err := json.Unmarshal(resp.body, &out); err != nil || out.Raw == "" {
		f.env.t.Fatalf("%s: sin raw_token: %v %s", resp.label, err, resp.body)
	}
	f.env.tokens[person] = out.Raw
	f.tokenIDs[person], _ = out.Token["id"].(string)
	f.responses[len(f.responses)-1].allow = []string{out.Raw}
	f.scanner.addToken(person, out.Raw, f.env.hasher.Hash)
	return out.Raw
}

func nexoRandomToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "egc_live_" + hex.EncodeToString(b[:4]) + "_" + strings.ReplaceAll(hex.EncodeToString(b[4:]), "_", "-")
}

// runNexoSecurityFlow ejecuta la corrida completa y devuelve el registro de
// respuestas y el buscador con todos los secretos de la corrida.
func runNexoSecurityFlow(t *testing.T, env *nexoSecEnv) *nexoFlow {
	t.Helper()
	f := &nexoFlow{env: env, scanner: newNexoLeakScanner(), tokenIDs: map[string]string{}}
	for name, raw := range env.tokens {
		f.scanner.addToken(name, raw, env.hasher.Hash)
	}
	const shared = "shared-sync-token-e2e"
	f.scanner.addToken("shared", shared, nil)
	ids := env.ids

	// Arranque: la primera petición de carla la hace superadmin.
	f.do(http.MethodGet, "/nexo/authz/v1/me", "carla", "", 200)

	// Alta de una persona y emisión de tokens por /admin/* y por la API Nexo.
	resp := f.do(http.MethodPost, "/admin/users", "carla", `{"username":"maria","email":"maria@rp.pe","display_name":"Maria","role":"member"}`, 201)
	var user struct {
		ID string `json:"principal_id"`
	}
	if err := json.Unmarshal(resp.body, &user); err != nil || user.ID == "" {
		t.Fatalf("alta: %v %s", err, resp.body)
	}
	ids["maria"] = user.ID
	f.issued("maria", f.do(http.MethodPost, "/admin/users/"+user.ID+"/tokens", "carla", `{"name":"maria-pc","expires_in_days":30}`, 201))
	f.issued("ana-read", f.do(http.MethodPost, "/nexo/authz/v1/tokens", "carla", `{"principal":"ana","name":"ana-lectura","expires_in_days":15,"scope":"read"}`, 201))
	f.do(http.MethodPost, "/admin/users/"+ids["ana"]+"/grants", "carla", `{"project":"nexo-transversal"}`, 201)

	// Panel: login correcto, login con un token desconocido y páginas de administración.
	bad := nexoRandomToken(t)
	f.scanner.addToken("unknown", bad, env.hasher.Hash)
	login := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader(url.Values{"token": {bad}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	env.srv.Handler().ServeHTTP(login, req)
	f.record("PANEL POST /dashboard/login (unknown token)", login)
	cookie := managedDashboardLogin(t, env.srv, env.tokens["carla"], false)
	f.cookies = append(f.cookies, cookie.Value)
	f.panel(http.MethodGet, "/dashboard/admin/users/"+ids["ana"], cookie, "")
	f.panel(http.MethodGet, "/dashboard/admin/users/list", cookie, "")
	f.panel(http.MethodGet, "/dashboard/admin/audit-log/list", cookie, "")
	created := f.panel(http.MethodPost, "/dashboard/admin/users/"+ids["luis"]+"/tokens", cookie, "name=luis-panel&expires_in_days=10")
	if created.code != http.StatusOK {
		t.Fatalf("emisión en el panel: %d %s", created.code, created.body)
	}
	// El panel muestra el token recién emitido una sola vez: se permite en esa página.
	if m := nexoFullTokenRe.Find(created.body); m != nil {
		f.responses[len(f.responses)-1].allow = []string{string(m)}
		f.scanner.addToken("luis-panel", string(m), env.hasher.Hash)
	}

	// Sincronización de Engram con el token de ana y lecturas.
	f.do(http.MethodPost, "/sync/push", "ana", nexoChunkBody, 200)
	f.do(http.MethodPost, "/sync/mutations/push", "ana", nexoMutationBody, 200)
	f.do(http.MethodGet, "/sync/pull?project=nexo-transversal", "ana", "", 200)
	f.do(http.MethodGet, "/sync/mutations/pull?since_seq=0", "ana", "", 200)
	f.do(http.MethodGet, "/sync/mutations/pull?since_seq=0", "ana-read", "", 200)
	f.do(http.MethodPost, "/sync/mutations/push", "ana-read", nexoMutationBody, 403)

	// Credenciales inválidas: token desconocido, token compartido, cabecera
	// mal formada y token revocado.
	f.do(http.MethodGet, "/sync/pull?project=nexo-transversal", "raw:"+bad, "", 401)
	f.do(http.MethodGet, "/nexo/authz/v1/me", "raw:"+bad, "", 401)
	f.do(http.MethodGet, "/nexo/authz/v1/me", "raw:"+shared, "", 403)
	malformed := httptest.NewRequest(http.MethodGet, "/sync/pull?project=nexo-transversal", nil)
	malformed.RemoteAddr = "10.95.0.1:40000"
	malformed.Header.Set("Authorization", "Basic "+bad)
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, malformed)
	f.record("GET /sync/pull (malformed Authorization)", rec)

	// Base de conocimiento: espacio, publicación, lectura, búsqueda.
	f.do(http.MethodPost, "/nexo/kb/v1/spaces", "carla", `{"slug":"seguridad-s5","name":"Seguridad S5","owner":"ana"}`, 201)
	batch := `{"documents":[{"path":"guias/tokens.md","content":"---\ncategory: lineamiento\ntitle: Tokens\n---\n# Tokens\n\n## Rotacion\n\nRotar los tokens cada 90 dias y nunca pegarlos en un chat.\n","origin":{"repo":"realplaza/seguridad","commit":"abc123"}}]}`
	f.do(http.MethodPost, "/nexo/kb/v1/spaces/seguridad-s5/documents:batch", "ana", batch, 200)
	key := url.PathEscape("guias/tokens")
	f.do(http.MethodGet, "/nexo/kb/v1/spaces", "luis", "", 200)
	f.do(http.MethodGet, "/nexo/kb/v1/spaces/seguridad-s5", "luis", "", 200)
	f.do(http.MethodGet, "/nexo/kb/v1/spaces/seguridad-s5/documents", "luis", "", 200)
	f.do(http.MethodGet, "/nexo/kb/v1/spaces/seguridad-s5/documents/"+key, "luis", "", 200)
	f.do(http.MethodGet, "/nexo/kb/v1/spaces/seguridad-s5/documents/"+key+"/versions", "luis", "", 200)
	f.do(http.MethodGet, "/nexo/kb/v1/search?q="+url.QueryEscape("rotar tokens"), "luis", "", 200)
	f.do(http.MethodGet, "/nexo/kb/v1/spaces/seguridad-s5/events", "ana", "", 200)
	f.do(http.MethodGet, "/nexo/kb/v1/spaces/seguridad-s5/publish-runs", "ana", "", 200)
	f.do(http.MethodPost, "/nexo/kb/v1/spaces/seguridad-s5/proposals", "luis", `{"doc_key":"guias/tokens","title":"Tokens","content":"# Tokens\n\nPropuesta.\n","note":"mejora"}`, 201, 403, 422)
	f.do(http.MethodGet, "/nexo/kb/v1/proposals", "ana", "", 200)
	f.do(http.MethodGet, "/nexo/kb/v1/changes?since=2020-01-01", "luis", "", 200)
	f.do(http.MethodGet, "/nexo/kb/v1/sync", "luis", "", 200)
	f.do(http.MethodGet, "/nexo/kb/v1/eval-runs", "carla", "", 200)
	f.do(http.MethodGet, "/nexo/kb/v1/admin/index", "carla", "", 200)

	// Lecturas de administración y auditoría.
	f.do(http.MethodGet, "/admin/users", "carla", "", 200)
	f.do(http.MethodGet, "/admin/users/"+ids["ana"]+"/tokens", "carla", "", 200)
	f.do(http.MethodGet, "/admin/users/"+ids["ana"]+"/grants", "carla", "", 200)
	f.do(http.MethodGet, "/nexo/admin/audit?limit=200", "carla", "", 200)
	f.do(http.MethodGet, "/nexo/admin/activity?limit=200", "carla", "", 200)
	f.do(http.MethodGet, "/nexo/admin/presence", "carla", "", 200)
	f.do(http.MethodGet, "/nexo/admin/history/obs-1?project=nexo-transversal&include_payload=true", "carla", "", 200)
	f.do(http.MethodGet, "/nexo/admin/authz-events?limit=200", "carla", "", 200)
	f.do(http.MethodGet, "/nexo/admin/database", "carla", "", 200) // SG-05: pool y TLS, sin secretos
	f.do(http.MethodGet, "/nexo/authz/v1/events?limit=200", "carla", "", 200)
	f.do(http.MethodGet, "/nexo/authz/v1/platform/members", "carla", "", 200)
	f.do(http.MethodGet, "/nexo/authz/v1/scopes/space/seguridad-s5/members", "ana", "", 200)
	f.do(http.MethodGet, "/nexo/authz/v1/me", "ana-read", "", 200)
	f.do(http.MethodGet, "/nexo/authz/v1/tokens", "ana", "", 200)
	f.do(http.MethodGet, "/nexo/authz/v1/tokens?principal=ana", "carla", "", 200)
	f.do(http.MethodGet, "/nexo/admin/audit/verify", "carla", "", 200)
	f.do(http.MethodGet, "/nexo/admin/alerts", "carla", "", 200)
	f.do(http.MethodGet, "/nexo/admin/access-review", "carla", "", 200)
	f.do(http.MethodGet, "/nexo/health/ready", "", "", 200)

	// Revocación y uso del token revocado.
	f.do(http.MethodPost, "/admin/tokens/"+f.tokenIDs["maria"]+"/revoke", "carla", `{"reason":"rotacion"}`, 200)
	f.do(http.MethodGet, "/sync/pull?project=nexo-transversal", "maria", "", 401)
	return f
}
