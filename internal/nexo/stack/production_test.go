package stack

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
)

func TestStackProdPrepare(t *testing.T) {
	var out, errb bytes.Buffer
	a := New(strings.NewReader(""), &out, &errb, t.TempDir(), "test")
	a.Home = t.TempDir()
	calls := []string{}
	a.Runner = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return nil, nil
	}
	if a.Run([]string{"stack", "up", "--mode", "prod", "--name", "prueba", "--port", "18499", "--image", "imagen-sintetica:test", "--yes"}) != 0 {
		t.Fatal(errb.String())
	}
	dir, _ := state.Directory(a.Home, "prod", "prueba", "")
	s, e := state.Load(dir)
	if e != nil || !s.Prepared || s.UsersReady {
		t.Fatal("prod arrancó por defecto")
	}
	m, e := render.ReadEnv(dir)
	if e != nil {
		t.Fatal(e)
	}
	if m["NEXO_SERVICE_ACCOUNTS_ENABLED"] != "" || m["NEXO_MIGRATION_DATABASE_URL"] == "" || m["NEXO_TRUSTED_PROXIES"] != s.ProxyIP+"/32" {
		t.Fatal("protecciones prod")
	}
	b, e := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "554") || !strings.Contains(string(b), "127.0.0.1:18499:443") {
		t.Fatal("red prod")
	}
	for _, c := range calls {
		if strings.Contains(c, "up -d") || strings.Contains(c, "build ") {
			t.Fatal("no respetó --image o solo preparar")
		}
	}
	for _, v := range sensitive(m) {
		if v != "" && strings.Contains(out.String()+errb.String(), v) {
			t.Fatal("filtración")
		}
	}
}
func TestStackProdRejectsExample(t *testing.T) {
	a := New(strings.NewReader(""), io.Discard, io.Discard, t.TempDir(), "test")
	s := state.State{Mode: "prod", Dir: t.TempDir(), Project: state.Project("prod", "")}
	if a.production(context.Background(), &s, Options{Search: SearchOptions{Example: true}}, map[string]string{}) == nil {
		t.Fatal("ejemplo en prod")
	}
}
func TestStackDevAccounts(t *testing.T) {
	a := New(strings.NewReader(""), io.Discard, io.Discard, t.TempDir(), "test")
	if len(a.people("dev")) != 3 || len(a.people("prod")) != 1 {
		t.Fatal("personas por modo")
	}
	s := state.State{Mode: "dev", Bind: "127.0.0.1", Port: 18491, PGPort: 55491, Dir: t.TempDir()}
	m, e := a.environment(context.Background(), &s, Options{Search: SearchOptions{Embedder: "off", Reranker: "off", R1: "all"}})
	if e != nil {
		t.Fatal(e)
	}
	if m["NEXO_MIGRATION_DATABASE_URL"] == "" || m["ENGRAM_DATABASE_URL"] == m["NEXO_MIGRATION_DATABASE_URL"] {
		t.Fatal("dev necesita dos cuentas")
	}
}
