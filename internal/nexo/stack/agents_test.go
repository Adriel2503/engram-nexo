package stack

import (
	"bytes"
	"context"
	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/ui"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStackAgentsFakeExecutables(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	t.Setenv("STACK_AGENT_HELPER", "1")
	t.Setenv("STACK_AGENT_TEST_DIR", cfg)
	src, _ := os.Executable()
	b, e := os.ReadFile(src)
	if e != nil {
		t.Fatal(e)
	}
	name := "engram"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(dir, name)
	if e = os.WriteFile(bin, b, 0700); e != nil {
		t.Fatal(e)
	}
	s := state.State{Dir: filepath.Join(dir, "estado"), Mode: "demo", ServerURL: "http://127.0.0.1:18490"}
	tok, _ := cloudauth.GenerateManagedToken("live")
	if e = render.AccessFile(render.Data{State: s, Access: []render.Access{{Username: "publicador", Role: "publicador", Token: tok.Raw}}}); e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	a := New(strings.NewReader(""), &out, io.Discard, filepath.Join(dir, "datos"), "test")
	a.Executable = bin
	a.AgentCatalog = func() []AgentInfo {
		return []AgentInfo{{Name: "codex", Dir: filepath.Join(cfg, "codex"), Installed: true}, {Name: "pi", Dir: filepath.Join(cfg, "pi"), Installed: true}}
	}
	calls := 0
	a.ConnectToken = func(_ context.Context, data, server, token string) (client.ConnectResult, error) {
		calls++
		if token != tok.Raw || server != s.ServerURL || data != a.DataDir {
			t.Fatal("conexión equivocada")
		}
		return client.ConnectResult{}, nil
	}
	if e = a.agents(context.Background(), s, Options{}, ui.New(strings.NewReader("\n"), &out, true), false); e != nil {
		t.Fatal(e)
	}
	if calls != 1 || !a.agentsSeen(s) {
		t.Fatal("no quedó conexión múltiple")
	}
	for _, name := range []string{"codex", "pi"} {
		b, e := os.ReadFile(filepath.Join(cfg, name, "config.json"))
		if e != nil || !strings.Contains(string(b), a.DataDir) {
			t.Fatal("ejecutable falso no recibió datos aislados")
		}
	}
	if strings.Contains(out.String(), tok.Raw) || !strings.Contains(out.String(), "disconnect --keep-copy") {
		t.Fatal("salida insegura o sin reversión")
	}
}
func TestStackTourCLIWithoutAgents(t *testing.T) {
	var out bytes.Buffer
	a := New(strings.NewReader("sí\nsí\nsí\n"), &out, io.Discard, t.TempDir(), "test")
	a.AgentCatalog = func() []AgentInfo { return []AgentInfo{{Name: "codex", Installed: false}} }
	s := state.State{Dir: t.TempDir(), Mode: "demo", ServerURL: "http://127.0.0.1:18490"}
	if e := a.tour(context.Background(), s, Options{Plain: true}); e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"engram save", "engram search", "engram nexo kb search"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("alternativa CLI ausente")
		}
	}
}

func TestStackNextMenu(t *testing.T) {
	var out bytes.Buffer
	a := New(strings.NewReader("3\n"), &out, io.Discard, t.TempDir(), "test")
	a.Interactive = true
	s := state.State{Dir: t.TempDir(), Mode: "demo", Name: "prueba"}
	if e := a.next(context.Background(), s, Options{Plain: true}); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), "Siguiente paso") || !strings.Contains(out.String(), "--name prueba") {
		t.Fatal("menú o comandos incompletos")
	}
	out.Reset()
	if e := a.next(context.Background(), s, Options{NoTour: true}); e != nil || out.Len() != 0 {
		t.Fatal("no respetó --no-tour")
	}
}
