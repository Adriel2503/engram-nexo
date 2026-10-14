package stack

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/proc"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/stackapi"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fakeSeeder struct{ loads int }

func (f *fakeSeeder) Personas() []stackapi.Persona { return NewSeeder().Personas() }
func (f *fakeSeeder) Load(_ context.Context, e stackapi.Env, _ stackapi.LoadOptions) (stackapi.Report, error) {
	f.loads++
	e.Logf("proceso %s", e.Tokens["lector"])
	return stackapi.Report{Documents: 12}, nil
}
func (f *fakeSeeder) RemoveExample(context.Context, stackapi.Env) (stackapi.Report, error) {
	return stackapi.Report{}, nil
}
func (f *fakeSeeder) LoadOwn(context.Context, stackapi.Env, stackapi.OwnOptions) (stackapi.Report, error) {
	return stackapi.Report{}, nil
}
func TestStackLifecycle(t *testing.T) {
	home := t.TempDir()
	var out, errb bytes.Buffer
	a := New(strings.NewReader(""), &out, &errb, t.TempDir(), "test")
	a.Home = home
	seed := &fakeSeeder{}
	a.Seeder = seed
	created, redeemed, launches := 0, 0, 0
	all := []string{}
	token := func() string {
		m, e := cloudauth.GenerateManagedToken("live")
		if e != nil {
			t.Fatal(e)
		}
		all = append(all, m.Raw)
		return m.Raw
	}
	a.Runner = func(_ context.Context, n string, args ...string) ([]byte, error) {
		if n != "docker" {
			t.Fatal("ejecutor ajeno")
		}
		if strings.Contains(strings.Join(args, " "), "compose") && len(args) > 2 && args[1] != "version" && !strings.Contains(strings.Join(args, " "), "nexo-stack-demo-codexrun") {
			t.Fatal("recurso ajeno")
		}
		return nil, nil
	}
	a.Launch = func(_ string, _ []string, _ []string, p string) (proc.Identity, error) {
		launches++
		id := proc.Identity{PID: 99, Executable: "engram.exe", Started: "prueba"}
		return id, proc.Save(p, id)
	}
	a.Alive = func(proc.Identity) bool {
		_, e := os.Stat(filepath.Join(home, "demo-codexrun", "detener"))
		return errorsIsNotExist(e)
	}
	a.Bootstrap = func(_ context.Context, s state.State, _ map[string]string) error {
		return render.AccessFile(render.Data{State: s, Access: []render.Access{{Username: "superadmin", Role: "superadmin", Token: token()}}})
	}
	a.Activate = func(context.Context, string, string) (string, error) { redeemed++; return token(), nil }
	a.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		body := "{}"
		if r.URL.Path == "/health" {
			body = "{\"service\":\"engram-cloud\",\"status\":\"ok\"}"
		}
		if r.URL.Path == "/nexo/health/ready" {
			body = "{\"status\":\"ready\"}"
		}
		if r.URL.Path == "/admin/users" {
			created++
		}
		if r.URL.Path == "/nexo/authz/v1/tokens" {
			body = "{\"activation_code\":\"código sintético\"}"
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	base := []string{"--mode", "demo", "--name", "codexrun", "--yes", "--plain", "--port", "18490", "--pg-port", "55490"}
	for _, cmd := range []string{"up", "up", "status", "logs", "down", "reset"} {
		if a.Run(append([]string{"stack", cmd}, base...)) != 0 {
			t.Fatalf("%s falló: %s", cmd, errb.String())
		}
	}
	if created != 5 || redeemed != 5 || launches != 1 || seed.loads != 1 {
		t.Fatalf("no idempotente: personas=%d canjes=%d procesos=%d cargas=%d", created, redeemed, launches, seed.loads)
	}
	for _, s := range all {
		if strings.Contains(out.String()+errb.String(), s) {
			t.Fatal("salida contiene token")
		}
	}
	if _, e := os.Stat(filepath.Join(home, "demo-codexrun", "estado.json")); !os.IsNotExist(e) {
		t.Fatal("reset conserva estado")
	}
}
func errorsIsNotExist(e error) bool { return os.IsNotExist(e) }
func TestStackProtection(t *testing.T) {
	o, e := parse([]string{"--embed-key", "valor"})
	_ = o
	if e == nil {
		t.Fatal("aceptó clave por opción")
	}
	var out, errb bytes.Buffer
	a := New(strings.NewReader(""), &out, &errb, t.TempDir(), "test")
	a.Home = t.TempDir()
	if a.Run([]string{"stack", "up", "--yes"}) == 0 {
		t.Fatal("modo ausente")
	}
	if a.Run([]string{"stack", "up", "--mode", "demo", "--name", "../ajeno"}) == 0 {
		t.Fatal("nombre con traversal")
	}
}
