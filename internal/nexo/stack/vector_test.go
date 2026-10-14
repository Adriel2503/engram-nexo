package stack

import (
	"bytes"
	"context"
	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/proc"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/secrets"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStackVectorFallback(t *testing.T) {
	dir := t.TempDir()
	s := state.State{Mode: "demo", Dir: dir, Project: state.Project("demo", ""), Bind: "127.0.0.1", Port: 18490, ServerURL: "http://127.0.0.1:18490", ExternalDB: true, UsersReady: true, Embedder: "gemini", Reranker: "off", R1: "all"}
	key, _ := secrets.Hex(32)
	m := map[string]string{"NEXO_KB_EMBEDDER": "gemini", "NEXO_KB_EMBED_API_KEY": key}
	if e := state.Save(s); e != nil {
		t.Fatal(e)
	}
	if e := render.WriteEnv(dir, m); e != nil {
		t.Fatal(e)
	}
	access := []render.Access{}
	for _, p := range NewSeeder().Personas() {
		tok, _ := cloudauth.GenerateManagedToken("live")
		access = append(access, render.Access{Username: p.Username, Role: p.Plataforma, Token: tok.Raw})
	}
	if e := render.AccessFile(render.Data{State: s, Access: access}); e != nil {
		t.Fatal(e)
	}
	id := proc.Identity{PID: 66, Executable: "engram.exe", Started: "prueba"}
	if e := proc.Save(filepath.Join(dir, "servidor.pid"), id); e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	a := New(strings.NewReader(""), &out, io.Discard, t.TempDir(), "test")
	a.Alive = func(proc.Identity) bool { _, e := os.Stat(filepath.Join(dir, "detener")); return os.IsNotExist(e) }
	launches := 0
	a.Launch = func(_ string, _ []string, _ []string, p string) (proc.Identity, error) {
		launches++
		return id, proc.Save(p, id)
	}
	a.Bootstrap = func(context.Context, state.State, map[string]string) error { return nil }
	a.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		b := "{\"server\":{}}"
		if r.URL.Path == "/health" {
			b = "{\"service\":\"engram-cloud\",\"status\":\"ok\"}"
		}
		if r.URL.Path == "/nexo/health/ready" {
			b = "{\"status\":\"ready\"}"
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(b))}, nil
	})}
	if e := a.checkVector(context.Background(), &s, m); e != nil {
		t.Fatal(e)
	}
	stored, e := state.Load(dir)
	if e != nil || stored.Embedder != "off" || launches != 1 {
		t.Fatal("no volvió a texto reiniciando")
	}
	if strings.Contains(out.String(), key) {
		t.Fatal("filtración")
	}
}
