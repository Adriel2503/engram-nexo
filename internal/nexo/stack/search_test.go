package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/secrets"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/ui"
)

func TestStackKeyValidation(t *testing.T) {
	key, e := secrets.Hex(32)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("NEXO_KB_EMBED_API_KEY", key)
	var out, errb bytes.Buffer
	a := New(strings.NewReader(""), &out, &errb, t.TempDir(), "test")
	calls := 0
	success := true
	a.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("x-goog-api-key") != key || strings.Contains(r.URL.String(), key) {
			t.Fatal("clave en ruta o cabecera ausente")
		}
		v := make([]float32, 768)
		v[0] = 1
		b, _ := json.Marshal(map[string]any{"embeddings": []any{map[string]any{"values": v}}})
		code := 200
		if !success {
			code = 401
			b = []byte(key)
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(b)), Header: make(http.Header)}, nil
	})}
	s := state.State{}
	m := map[string]string{}
	o := SearchOptions{Embedder: "gemini", Reranker: "off", R1: "all"}
	if e = a.searchConfig(context.Background(), &s, &o, m); e != nil {
		t.Fatal(e)
	}
	if calls != 1 || m["NEXO_KB_EMBED_API_KEY"] != key {
		t.Fatal("validación mínima")
	}
	success = false
	if a.searchConfig(context.Background(), &s, &o, map[string]string{}) == nil {
		t.Fatal("aceptó clave rechazada")
	}
	if strings.Contains(out.String()+errb.String(), key) {
		t.Fatal("filtración de clave")
	}
}
func TestStackSearchAlwaysAsks(t *testing.T) {
	var out bytes.Buffer
	a := New(strings.NewReader(""), &out, io.Discard, t.TempDir(), "test")
	o := SearchOptions{Embedder: "off", Reranker: "off", R1: "all"}
	if e := a.askSearch(ui.New(strings.NewReader("no\nno\nall\n"), &out, true), &o); e != nil {
		t.Fatal(e)
	}
	for _, word := range []string{"búsqueda por significado", "reordenador"} {
		if !strings.Contains(out.String(), word) {
			t.Fatal("pregunta omitida")
		}
	}
}
func TestStackStartConnect(t *testing.T) {
	key, _ := secrets.Hex(32)
	var out, errb bytes.Buffer
	a := New(strings.NewReader("2\nhttp://127.0.0.1:18490\n"+key+"\n2\n"), &out, &errb, t.TempDir(), "test")
	connected, setup := false, false
	a.Connect = func(_ context.Context, dir, server, code string) (client.ConnectResult, error) {
		if code != key || dir != a.DataDir {
			t.Fatal("entrada")
		}
		connected = true
		return client.ConnectResult{Label: "persona sintética"}, nil
	}
	a.AgentCatalog = func() []AgentInfo {
		return []AgentInfo{{Name: "claude-code", Dir: t.TempDir()}, {Name: "codex", Dir: t.TempDir()}}
	}
	a.Setup = func(_ context.Context, agent string) error { setup = agent == "codex"; return nil }
	if a.Run([]string{"start", "--plain"}) != 0 || !connected || !setup {
		t.Fatal("conexión guiada")
	}
	if strings.Contains(out.String()+errb.String(), key) {
		t.Fatal("se imprimió el código")
	}
}
