package cloudserver

// Nexo IA · NX-04 — tests del middleware de contexto de petición (T-12) y de
// los metadatos de la conexión en actividad, presencia y auditoría (T-17,
// CA-20 a CA-24).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

// nexoDoFrom es nexoDo con origen configurable (IP de conexión, cabecera
// X-Forwarded-For y User-Agent).
func nexoDoFrom(t *testing.T, h http.Handler, method, path, token, body, remote, xff, ua string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = remote
	req.Header.Set("User-Agent", ua)
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// CA-20, CA-21, CA-22, CA-23 de punta a punta (handler → Postgres).
func TestNexoConnectionMetadataEndToEnd(t *testing.T) {
	// Caddy en la red Docker 172.18.0.0/16 es el proxy confiable (CA-21).
	t.Setenv(NexoTrustedProxiesEnv, "172.18.0.0/16")
	env := newNexoRealEnv(t)
	h := env.srv.Handler()

	presence := func(key string) (ip, ua, claim, actor string) {
		t.Helper()
		if err := env.db.QueryRow(`SELECT last_ip, last_user_agent, last_client_claim, actor FROM nexo_actor_presence WHERE presence_key = $1`, key).Scan(&ip, &ua, &claim, &actor); err != nil {
			t.Fatalf("presence %q: %v", key, err)
		}
		return
	}
	lastActivityIP := func(actor string) (ip, ua string) {
		t.Helper()
		if err := env.db.QueryRow(`SELECT client_ip, user_agent FROM nexo_activity_log WHERE actor = $1 ORDER BY id DESC LIMIT 1`, actor).Scan(&ip, &ua); err != nil {
			t.Fatalf("activity %q: %v", actor, err)
		}
		return
	}
	anaKey := env.anaID + "|" + env.anaTok["laptop-ana"]
	luisKey := env.luisID + "|" + env.luisTok

	// CA-20: conexión directa desde 10.0.0.15.
	if rec := nexoDoFrom(t, h, http.MethodPost, "/sync/mutations/push", "ana-laptop", nexoMutationBody, "10.0.0.15:51000", "", "Go-http-client/1.1"); rec.Code != http.StatusOK {
		t.Fatalf("ana push = %d %s", rec.Code, rec.Body.String())
	}
	if ip, ua := lastActivityIP("ana"); ip != "10.0.0.15" || ua != "Go-http-client/1.1" {
		t.Fatalf("actividad de ana ip=%q ua=%q", ip, ua)
	}
	if ip, ua, _, _ := presence(anaKey); ip != "10.0.0.15" || ua != "Go-http-client/1.1" {
		t.Fatalf("presencia de ana ip=%q ua=%q", ip, ua)
	}

	// CA-21: detrás de Caddy (confiable) se registra la IP de Ana, no la del proxy.
	if rec := nexoDoFrom(t, h, http.MethodPost, "/sync/mutations/push", "ana-laptop", nexoMutationBody, "172.18.0.5:443", "10.0.0.16", "Go-http-client/1.1"); rec.Code != http.StatusOK {
		t.Fatalf("ana push via proxy = %d", rec.Code)
	}
	if ip, _ := lastActivityIP("ana"); ip != "10.0.0.16" {
		t.Fatalf("detrás del proxy confiable ip=%q, want 10.0.0.16", ip)
	}

	// CA-22: cabecera falsa desde una conexión no confiable → se ignora.
	if rec := nexoDoFrom(t, h, http.MethodPost, "/sync/mutations/push", "ana-laptop", nexoMutationBody, "10.0.0.15:51000", "1.2.3.4", "Go-http-client/1.1"); rec.Code != http.StatusOK {
		t.Fatalf("ana push con cabecera falsa = %d", rec.Code)
	}
	if ip, _ := lastActivityIP("ana"); ip != "10.0.0.15" {
		t.Fatalf("cabecera falsa aceptada: ip=%q", ip)
	}

	// CA-23: la PC de Luis declara LUIS-PC; la presencia lo muestra como
	// «declarado por la PC», separado del autor.
	edit := `{"created_by":"LUIS-PC","entries":[{"project":"nexo-transversal","entity":"observation","entity_key":"obs-1","op":"upsert","payload":{"sync_id":"obs-1","session_id":"s-1","type":"decision","title":"T2","content":"C2","scope":"project"}}]}`
	if rec := nexoDoFrom(t, h, http.MethodPost, "/sync/mutations/push", "luis-laptop", edit, "10.0.0.20:5000", "", "Go-http-client/1.1"); rec.Code != http.StatusOK {
		t.Fatalf("luis push = %d", rec.Code)
	}
	if ip, _, claim, actor := presence(luisKey); claim != "LUIS-PC" || actor != "luis" || ip != "10.0.0.20" {
		t.Fatalf("presencia de luis claim=%q actor=%q ip=%q", claim, actor, ip)
	}
}

// CA-24 y RF-24: los eventos de la auditoría de seguridad guardan IP y cliente
// (login rechazado en el panel, rechazo de sync, gestión de tokens).
func TestNexoAuthAuditCarriesClientOrigin(t *testing.T) {
	t.Setenv(NexoTrustedProxiesEnv, "172.18.0.0/16")
	env := newNexoAdminEnv(t)
	h := env.srv.Handler()

	// Login en el panel con un token inválido, detrás del proxy confiable.
	form := url.Values{"token": {"token-invalido"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0)")
	req.Header.Set("X-Forwarded-For", "10.0.0.33")
	req.RemoteAddr = "172.18.0.5:443"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "invalid token") {
		t.Fatalf("login inválido = %d", rec.Code)
	}
	var ip, ua string
	if err := env.db.QueryRow(`SELECT metadata->>'client_ip', metadata->>'user_agent' FROM cloud_auth_audit_log WHERE action = 'dashboard.login' AND outcome = 'denied'`).Scan(&ip, &ua); err != nil {
		t.Fatalf("login rechazado no auditado: %v", err)
	}
	if ip != "10.0.0.33" || ua != "Mozilla/5.0 (Windows NT 10.0)" {
		t.Fatalf("login rechazado ip=%q ua=%q", ip, ua)
	}
	if strings.Contains(ip+ua, "token-invalido") {
		t.Fatalf("el valor del token no debe guardarse")
	}

	// Rechazo de sincronización con token inválido.
	if rec := nexoDoFrom(t, h, http.MethodGet, "/sync/mutations/pull?since_seq=0", "token-robado", "", "10.0.0.44:1234", "", "curl/8.0"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sync con token inválido = %d", rec.Code)
	}
	if err := env.db.QueryRow(`SELECT metadata->>'client_ip', metadata->>'user_agent' FROM cloud_auth_audit_log WHERE action = 'sync.auth' AND outcome = 'denied'`).Scan(&ip, &ua); err != nil {
		t.Fatal(err)
	}
	if ip != "10.0.0.44" || ua != "curl/8.0" {
		t.Fatalf("sync.auth ip=%q ua=%q", ip, ua)
	}

	// Gestión (alta de permiso) por la API de Engram: también con origen.
	if rec := nexoDoFrom(t, h, http.MethodPost, "/admin/users/"+env.luisID+"/grants", "admin-token", `{"project":"otro"}`, "10.0.0.55:1", "", "admin-cli"); rec.Code != http.StatusCreated {
		t.Fatalf("grant = %d %s", rec.Code, rec.Body.String())
	}
	var project string
	if err := env.db.QueryRow(`SELECT metadata->>'client_ip', metadata->>'user_agent', metadata->>'project' FROM cloud_auth_audit_log WHERE action = 'grant.create'`).Scan(&ip, &ua, &project); err != nil {
		t.Fatal(err)
	}
	if ip != "10.0.0.55" || ua != "admin-cli" || project != "otro" {
		t.Fatalf("grant.create ip=%q ua=%q project=%q (la metadata original debe conservarse)", ip, ua, project)
	}
}

// Sin pasar por el middleware (llamada interna) la metadata queda como la
// define Engram.
func TestNexoAuthAuditWithoutOriginKeepsEngramMetadata(t *testing.T) {
	store := newAdminTestStore()
	srv := New(store, nil, 0, WithAdminIdentityStore(store))
	srv.recordAuthAuditBestEffort(context.Background(), cloudstore.AuthAuditEvent{ActorSource: "request", Action: "sync.auth", Outcome: "denied", Metadata: map[string]any{"source": "request"}}, "test")
	if len(store.auditEvents) != 1 || len(store.auditEvents[0].Metadata) != 1 {
		t.Fatalf("metadata = %+v", store.auditEvents)
	}
}

func TestNexoClientIP(t *testing.T) {
	trusted := nexoParseTrustedProxies("172.18.0.0/16, 10.9.9.9")
	cases := []struct {
		name    string
		remote  string
		xff     []string
		trusted bool
		want    string
	}{
		{"sin proxy configurado ignora la cabecera", "10.0.0.15:5555", []string{"1.2.3.4"}, false, "10.0.0.15"},
		{"conexión directa no confiable ignora cabecera falsa (CA-22)", "10.0.0.15:5555", []string{"1.2.3.4"}, true, "10.0.0.15"},
		{"detrás de proxy confiable usa la IP del cliente (CA-21)", "172.18.0.5:443", []string{"10.0.0.15"}, true, "10.0.0.15"},
		{"proxy confiable sin cabecera usa la conexión", "172.18.0.5:443", nil, true, "172.18.0.5"},
		{"cliente que falsifica la cabecera tras el proxy", "172.18.0.5:443", []string{"6.6.6.6, 10.0.0.15"}, true, "10.0.0.15"},
		{"varios proxies confiables encadenados", "10.9.9.9:80", []string{"10.0.0.15, 172.18.0.7"}, true, "10.0.0.15"},
		{"cabecera ilegible usa la conexión", "172.18.0.5:443", []string{"no-es-ip"}, true, "172.18.0.5"},
		{"IPv6", "[2001:db8::1]:443", nil, true, "2001:db8::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/health", nil)
			r.RemoteAddr = tc.remote
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			list := trusted
			if !tc.trusted {
				list = nil
			}
			if got := nexoClientIP(r, list); got != tc.want {
				t.Fatalf("nexoClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNexoParseTrustedProxiesIgnoresInvalid(t *testing.T) {
	got := nexoParseTrustedProxies("10.0.0.1, basura, 192.168.0.0/24;2001:db8::/32 300.1.1.1")
	if len(got) != 3 {
		t.Fatalf("prefixes = %v, want 3 valid entries", got)
	}
}

func TestNexoMiddlewareStoresClientContextOnServerHandler(t *testing.T) {
	t.Setenv(NexoTrustedProxiesEnv, "127.0.0.1")
	srv := New(&fakeStore{}, nil, 0)
	var seen NexoClientContext
	var ok bool
	srv.mux.HandleFunc("GET /nexo-test-probe", func(w http.ResponseWriter, r *http.Request) {
		seen, ok = NexoClientContextFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	r := httptest.NewRequest(http.MethodGet, "/nexo-test-probe", nil)
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("X-Forwarded-For", "10.0.0.15")
	r.Header.Set("User-Agent", strings.Repeat("u", 400))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusNoContent || !ok {
		t.Fatalf("probe status=%d context=%v", rec.Code, ok)
	}
	if seen.IP != "10.0.0.15" || len(seen.UserAgent) != 256 {
		t.Fatalf("client context = ip %q ua len %d", seen.IP, len(seen.UserAgent))
	}
}
