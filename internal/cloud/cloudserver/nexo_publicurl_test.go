package cloudserver

// NX-10: X-Forwarded-Proto y X-Forwarded-Host solo cuentan si la conexión
// viene de un proxy confiable (NEXO_TRUSTED_PROXIES); los enlaces de
// /nexo/kb/v1 salen entonces con el esquema y el host públicos.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/httpapi"
)

func TestNexoKBPublicOriginBehindTrustedProxy(t *testing.T) {
	cases := []struct {
		name    string
		trusted string
		remote  string
		want    string
	}{
		{"proxy confiable", "172.18.0.0/16", "172.18.0.5:443", "https://nexo.example.com"},
		{"conexion no confiable ignora las cabeceras", "172.18.0.0/16", "10.0.0.15:5555", "http://engram:18080"},
		{"sin proxies configurados ignora las cabeceras", "", "172.18.0.5:443", "http://engram:18080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(NexoTrustedProxiesEnv, tc.trusted)
			t.Setenv(httpapi.EnvPublicURL, "")
			srv := New(&fakeStore{}, nil, 0)
			var got string
			srv.mux.HandleFunc("GET /nexo-test-publicurl", func(w http.ResponseWriter, r *http.Request) {
				got = httpapi.BaseURL(nexoWithKBPublicOrigin(r))
				w.WriteHeader(http.StatusNoContent)
			})
			r := httptest.NewRequest(http.MethodGet, "/nexo-test-publicurl", nil)
			r.Host = "engram:18080"
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("X-Forwarded-Host", "nexo.example.com")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, r)
			if rec.Code != http.StatusNoContent || got != tc.want {
				t.Fatalf("status=%d BaseURL=%q, want %q", rec.Code, got, tc.want)
			}
		})
	}
}
