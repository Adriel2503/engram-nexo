package cloudserver

// Nexo IA · NX-04 (base, T-12) — tests del middleware de contexto de petición.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
