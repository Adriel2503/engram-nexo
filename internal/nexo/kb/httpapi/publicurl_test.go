package httpapi

// NX-10: enlaces de las citas detrás de un proxy (RC-08). NEXO_KB_PUBLIC_URL
// manda; si no está, el origen informado por un proxy confiable; si no, la
// conexión.

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

func TestBaseURL(t *testing.T) {
	cases := []struct {
		name         string
		env          string
		tls          bool
		proto, fhost string
		withOrigin   bool
		want         string
	}{
		{name: "conexion http directa", want: "http://nexo.local:18080"},
		{name: "conexion tls directa", tls: true, want: "https://nexo.local:18080"},
		{name: "proxy confiable https", withOrigin: true, proto: "https", fhost: "nexo.example.com", want: "https://nexo.example.com"},
		{name: "proxy confiable solo esquema", withOrigin: true, proto: "HTTPS", want: "https://nexo.local:18080"},
		{name: "esquema invalido se ignora", withOrigin: true, proto: "javascript", fhost: "nexo.example.com", want: "http://nexo.example.com"},
		{name: "host invalido se ignora", withOrigin: true, proto: "https", fhost: "evil.com/x@y", want: "https://nexo.local:18080"},
		{name: "la variable manda", env: "https://kb.example.com/", withOrigin: true, proto: "http", fhost: "otro", want: "https://kb.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvPublicURL, tc.env)
			r := httptest.NewRequest("GET", "/nexo/kb/v1/sync", nil)
			r.Host = "nexo.local:18080"
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if tc.withOrigin {
				r = r.WithContext(WithForwardedOrigin(r.Context(), tc.proto, tc.fhost))
			}
			if got := BaseURL(r); got != tc.want {
				t.Fatalf("BaseURL = %q, want %q", got, tc.want)
			}
		})
	}
}
