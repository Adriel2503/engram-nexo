package nexocli

// engram nexo sa list/show: marca de cuenta sin uso (NEXO_SA_STALE_DAYS).

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
)

func TestNexoSAStaleOutput(t *testing.T) {
	old := time.Now().Add(-200 * 24 * time.Hour).UTC().Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.RawQuery != "" {
			_, _ = io.WriteString(w, `{"items":[`+
				`{"principal_id":"12","slug":"ci-trimestral","owner_username":"ana","enabled":true,"last_activity_at":"`+old+`","idle_days":200,"stale":true,"stale_after_days":180,"bindings":[]},`+
				`{"principal_id":"13","slug":"ci-diario","owner_username":"ana","enabled":true,"last_activity_at":"`+time.Now().UTC().Format(time.RFC3339)+`","idle_days":0,"stale":false,"stale_after_days":180,"bindings":[]},`+
				`{"principal_id":"14","slug":"ci-nuevo","owner_username":"ana","enabled":true,"last_activity_at":null,"idle_days":3,"stale":false,"stale_after_days":180,"bindings":[]}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"principal_id":"12","slug":"ci-trimestral","owner_username":"ana","enabled":true,"last_activity_at":"`+old+`","idle_days":200,"stale":true,"stale_after_days":180,"bindings":[],"tokens":[]}`)
	}))
	defer srv.Close()
	prev := saAPI
	saAPI = func(string, string) (*client.API, error) {
		return client.NewAPI(client.Settings{Server: srv.URL, Token: "admin-token"}, 5*time.Second), nil
	}
	defer func() { saAPI = prev }()

	var out, errOut bytes.Buffer
	if code := runServiceAccounts([]string{"list"}, t.TempDir(), &out, &errOut); code != 0 {
		t.Fatalf("list: %d %s", code, errOut.String())
	}
	s := out.String()
	for _, want := range []string{"ULTIMO USO", "habilitada, SIN USO", "sin uso desde hace 200 días", "en las últimas 24 horas", "nunca usada (creada hace 3 días)", "1 cuenta(s) SIN USO en 180 días"} {
		if !strings.Contains(s, want) {
			t.Fatalf("list sin %q:\n%s", want, s)
		}
	}
	out.Reset()
	if code := runServiceAccounts([]string{"show", "ci-trimestral"}, t.TempDir(), &out, &errOut); code != 0 {
		t.Fatalf("show: %d %s", code, errOut.String())
	}
	s = out.String()
	for _, want := range []string{"Último uso: sin uso desde hace 200 días.", "SIN USO: 200 días sin uso (aviso a partir de 180 días", "confirme con ana"} {
		if !strings.Contains(s, want) {
			t.Fatalf("show sin %q:\n%s", want, s)
		}
	}
}
