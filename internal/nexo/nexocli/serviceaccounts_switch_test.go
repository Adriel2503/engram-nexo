package nexocli

// NX-07: con NEXO_SERVICE_ACCOUNTS_ENABLED apagado el servidor responde 403
// service_accounts_disabled; `engram nexo sa` muestra el mensaje en español, sale con 1 y no imprime ningún token.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/features"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
)

func TestNexoSACommandsReportDisabledSwitch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error_class":"policy","error_code":"` + features.ServiceAccountsDisabledCode + `","error":"` + features.ServiceAccountsDisabledMessage + `"}`))
	}))
	defer srv.Close()
	prev := saAPI
	saAPI = func(string, string) (*client.API, error) {
		return client.NewAPI(client.Settings{Server: srv.URL, Token: "admin-token"}, 5*time.Second), nil
	}
	defer func() { saAPI = prev }()
	for _, args := range [][]string{
		{"create", "--slug", "ci-wiki", "--owner", "ana"},
		{"enable", "ci-wiki"},
		{"token", "issue", "ci-wiki"},
		{"token", "rotate", "ci-wiki", "7"},
	} {
		var out, errb bytes.Buffer
		code := Run(append([]string{"sa"}, args...), t.TempDir(), &out, &errb)
		if code != 1 || out.Len() != 0 || !strings.Contains(errb.String(), "NEXO_SERVICE_ACCOUNTS_ENABLED=true") || strings.Contains(errb.String(), "HTTP 403") {
			t.Fatalf("%v: código %d, salida %q, errores %q", args, code, out.String(), errb.String())
		}
	}
}
