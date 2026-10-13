package nexocli

// engram nexo sa (SG-10) contra un servidor simulado: rutas y cuerpos que
// envía, y el token nuevo solo en la salida estándar (o en --out), nunca en
// los mensajes.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
)

const saFakeRaw = "eng_live_fake_token_for_tests_only_0123456789"

type saCall struct {
	Method, Path string
	Body         map[string]any
}

func saFakeServer(t *testing.T) (*[]saCall, func()) {
	t.Helper()
	var mu sync.Mutex
	calls := []saCall{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer admin-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		c := saCall{Method: r.Method, Path: r.URL.EscapedPath()}
		if r.URL.RawQuery != "" {
			c.Path += "?" + r.URL.RawQuery
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &c.Body)
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		exp := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
		switch {
		case strings.HasSuffix(c.Path, ":rotate"):
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"raw_token":"`+saFakeRaw+`","token":{"id":"8","name":"ci-github","scope":"write","expires_at":"`+exp+`"},"old_token":{"id":"7","revoked":true}}`)
		case strings.HasSuffix(c.Path, "/tokens"):
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"raw_token":"`+saFakeRaw+`","token":{"id":"7","name":"ci-github","scope":"write","expires_at":"`+exp+`"}}`)
		case strings.HasSuffix(c.Path, ":revoke"):
			_, _ = io.WriteString(w, `{"token_id":"8","revoked":true,"changed":true}`)
		case strings.HasSuffix(c.Path, ":disable"):
			_, _ = io.WriteString(w, `{"service_account":{"slug":"ci-wiki","enabled":false},"changed":true}`)
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"principal_id":"12","slug":"ci-wiki","owner_username":"ana","enabled":true}`)
		case r.Method == http.MethodGet && strings.Contains(c.Path, "?"):
			_, _ = io.WriteString(w, `{"items":[{"principal_id":"12","slug":"ci-wiki","owner_username":"ana","enabled":true,"bindings":[{"scope":{"type":"space","key":"wiki"},"role":"publicador"}]}]}`)
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"principal_id":"12","slug":"ci-wiki","owner_username":"ana","enabled":true,"bindings":[],"tokens":[{"id":"7","name":"ci-github","scope":"write","expires_at":"`+exp+`","last_used_at":"`+exp+`","last_seen":{"at":"`+exp+`","ip":"10.1.2.3","user_agent":"engram-nexo-kb/2","source":"kb"}}]}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	prev := saAPI
	saAPI = func(string, string) (*client.API, error) {
		return client.NewAPI(client.Settings{Server: srv.URL, Token: "admin-token"}, 5*time.Second), nil
	}
	return &calls, func() { saAPI = prev; srv.Close() }
}

func TestNexoSACommands(t *testing.T) {
	calls, done := saFakeServer(t)
	defer done()
	run := func(args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		code := Run(append([]string{"sa"}, args...), t.TempDir(), &out, &errb)
		return out.String(), errb.String(), code
	}

	if _, _, code := run("create", "--owner", "ana"); code != 2 {
		t.Fatalf("create sin slug: %d", code)
	}
	out, _, code := run("create", "--slug", "ci-wiki", "--owner", "ana", "--description", "Publica la wiki")
	if code != 0 || !strings.Contains(out, "ci-wiki") {
		t.Fatalf("create: %d %s", code, out)
	}
	if _, _, code := run("grant", "ci-wiki", "--space", "wiki", "--role", "publicador"); code != 0 {
		t.Fatalf("grant: %d", code)
	}
	if out, _, code = run("list"); code != 0 || !strings.Contains(out, "publicador@space:wiki") {
		t.Fatalf("list: %d %s", code, out)
	}
	if out, _, code = run("show", "ci-wiki"); code != 0 || !strings.Contains(out, "10.1.2.3") {
		t.Fatalf("show: %d %s", code, out)
	}

	// El token nuevo: solo él en la salida estándar.
	out, errOut, code := run("token", "issue", "ci-wiki", "--name", "ci-github", "--days", "30")
	if code != 0 || strings.TrimSpace(out) != saFakeRaw || strings.Contains(errOut, saFakeRaw) {
		t.Fatalf("token issue: %d %q %q", code, out, errOut)
	}
	// Con --out, a un archivo 0600 y nada en la salida estándar.
	file := filepath.Join(t.TempDir(), "token.txt")
	out, errOut, code = run("token", "rotate", "ci-wiki", "7", "--grace-minutes", "15", "--out", file)
	if code != 0 || out != "" || strings.Contains(errOut, saFakeRaw) || !strings.Contains(errOut, "revocado") {
		t.Fatalf("token rotate: %d %q %q", code, out, errOut)
	}
	data, err := os.ReadFile(file)
	if err != nil || strings.TrimSpace(string(data)) != saFakeRaw {
		t.Fatalf("archivo del token: %q %v", data, err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
			t.Fatalf("permisos del archivo: %v", st.Mode().Perm())
		}
	}
	if _, _, code = run("token", "revoke", "ci-wiki", "8", "--reason", "rotado"); code != 0 {
		t.Fatalf("token revoke: %d", code)
	}
	if _, errOut, code = run("disable", "ci-wiki"); code != 0 || !strings.Contains(errOut, "deshabilitada") {
		t.Fatalf("disable: %d %s", code, errOut)
	}
	if _, _, code = run("token", "rotate", "ci-wiki"); code != 2 {
		t.Fatalf("rotate sin token-id: %d", code)
	}

	want := []saCall{
		{Method: "POST", Path: "/nexo/authz/v1/service-accounts", Body: map[string]any{"slug": "ci-wiki", "owner": "ana", "description": "Publica la wiki", "display_name": ""}},
		{Method: "PUT", Path: "/nexo/authz/v1/scopes/space/wiki/members/ci-wiki", Body: map[string]any{"role": "publicador"}},
		{Method: "GET", Path: "/nexo/authz/v1/service-accounts?limit=200&enabled=true"},
		{Method: "GET", Path: "/nexo/authz/v1/service-accounts/ci-wiki"},
		{Method: "POST", Path: "/nexo/authz/v1/service-accounts/ci-wiki/tokens", Body: map[string]any{"name": "ci-github", "expires_in_days": float64(30)}},
		{Method: "POST", Path: "/nexo/authz/v1/service-accounts/ci-wiki/tokens/7:rotate", Body: map[string]any{"grace_minutes": float64(15)}},
		{Method: "POST", Path: "/nexo/authz/v1/service-accounts/ci-wiki/tokens/8:revoke", Body: map[string]any{"reason": "rotado"}},
		{Method: "POST", Path: "/nexo/authz/v1/service-accounts/ci-wiki:disable", Body: map[string]any{}},
	}
	got, _ := json.Marshal(*calls)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Fatalf("peticiones:\n got %s\nwant %s", got, exp)
	}
}

func TestNexoSAHelp(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"sa", "help"}, t.TempDir(), &out, &errb); code != 0 {
		t.Fatalf("sa help: %d", code)
	}
	for _, want := range []string{"token rotate", "--grace-minutes", "--out", "se muestra una sola"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("engram nexo sa help sin %q", want)
		}
	}
	out.Reset()
	Run([]string{"help"}, t.TempDir(), &out, &errb)
	if !strings.Contains(out.String(), "cuentas de servicio") {
		t.Error("engram nexo help no lista sa")
	}
}
