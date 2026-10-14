package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/embed"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/rerank"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/secrets"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/ui"
)

func TestStackProbeFailureClasses(t *testing.T) {
	key, _ := secrets.Hex(32)
	for _, tc := range []struct {
		code int
		want string
		body string
	}{{401, "clave rechazada", ""}, {403, "clave rechazada", ""}, {404, "modelo no encontrado", ""}, {429, "cuota", ""}, {402, "cuota", ""}, {503, "red", ""}, {0, "red", ""}, {200, "respuesta inválida", ""}, {400, "clave rechazada", `{"error":{"details":[{"reason":"API_KEY_INVALID"}]}}`}, {400, "clave rechazada", `{"error":{"details":[{"reason":"API_KEY_EXPIRED"}]}}`}, {400, "solicitud rechazada", ""}} {
		for _, provider := range []string{"embeddings", "reordenador"} {
			t.Run(provider+"/"+tc.want+"/"+http.StatusText(tc.code), func(t *testing.T) {
				hc := &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
					if tc.code == 0 {
						return nil, errors.New(key)
					}
					body := tc.body
					if body == "" {
						body = key
					}
					return &http.Response{StatusCode: tc.code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})}
				var err error
				if provider == "embeddings" {
					err = probeEmbed(context.Background(), embed.Config{Provider: embed.ProviderGemini, Model: "gemini-embedding-001", Dims: 768, APIKey: key, BaseURL: "https://example.test"}, hc)
				} else {
					cfg, e := rerank.ConfigFrom(func(k string) string {
						return map[string]string{rerank.EnvReranker: "cohere", rerank.EnvAPIKey: key}[k]
					})
					if e != nil {
						t.Fatal(e)
					}
					err = probeRerank(context.Background(), cfg, hc)
				}
				if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), key) {
					t.Fatal("diagnóstico de clase ausente o inseguro")
				}
			})
		}
	}
}

func TestStackSearchRecovery(t *testing.T) {
	for _, provider := range []string{"embeddings", "reordenador"} {
		for _, choice := range []string{"skip", "retry", "replace", "cancel"} {
			t.Run(provider+"/"+choice, func(t *testing.T) {
				for _, env := range []string{embed.EnvAPIKey, rerank.EnvAPIKey, "GEMINI_API_KEY", "OPENAI_API_KEY", "COHERE_API_KEY"} {
					t.Setenv(env, "")
				}
				key, _ := secrets.Hex(32)
				replacement, _ := secrets.Hex(32)
				setup := func(key string) string {
					if provider == "embeddings" {
						return "sí\n1\n\n\n" + key + "\nno\nall\n"
					}
					return "no\nsí\n1\n\n" + key + "\nall\n"
				}
				input := setup(key)
				switch choice {
				case "skip":
					input += "2\n"
				case "retry":
					input += "1\n" + setup("")
				case "replace":
					input += "1\n" + setup(replacement)
				}
				var out bytes.Buffer
				a := New(strings.NewReader(input), &out, io.Discard, t.TempDir(), "test")
				calls := 0
				a.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
					calls++
					wantKey := key
					if calls > 1 && choice == "replace" {
						wantKey = replacement
					}
					if provider == "embeddings" && r.Header.Get("x-goog-api-key") != wantKey || provider == "reordenador" && r.Header.Get("Authorization") != "Bearer "+wantKey {
						t.Error("no se usó la clave esperada")
					}
					code, body := 401, key
					if calls > 1 {
						v := make([]float32, 768)
						v[0] = 1
						b, _ := json.Marshal(map[string]any{"embeddings": []any{map[string]any{"values": v}}})
						code, body = 200, string(b)
						if provider == "reordenador" {
							body = `{"results":[{"index":0,"relevance_score":0.9},{"index":1,"relevance_score":0.1}]}`
						}
					}
					return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})}
				o := SearchOptions{Embedder: "off", Reranker: "off", R1: "all"}
				if e := a.askSearch(ui.New(a.In, a.Out, true), &o); e != nil {
					t.Fatal(e)
				}
				s := state.State{Embedder: "previous"}
				m := map[string]string{"keep": "value"}
				err := a.searchConfig(context.Background(), &s, &o, m)
				if choice == "cancel" {
					if err == nil || s.Embedder != "previous" || len(m) != 1 {
						t.Fatal("cancelación modificó la configuración")
					}
				} else if err != nil {
					t.Fatal("no se recuperó la prueba fallida")
				} else if choice == "skip" {
					if s.Embedder != "off" || s.Reranker != "off" || m[embed.EnvAPIKey] != "" || m[rerank.EnvAPIKey] != "" {
						t.Fatal("no siguió sin proveedores")
					}
				} else if provider == "embeddings" && s.Embedder != "gemini" || provider == "reordenador" && s.Reranker != "cohere" || calls != 2 {
					t.Fatal("no reintentó")
				}
				if strings.Contains(out.String(), key) || strings.Contains(out.String(), replacement) {
					t.Fatal("clave expuesta")
				}
			})
		}
	}
}

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

func TestStackStartKeyFailureContinues(t *testing.T) {
	key, _ := secrets.Hex(32)
	t.Setenv(embed.EnvAPIKey, key)
	t.Setenv("NEXO_STACK_DATABASE_URL", "")
	var out bytes.Buffer
	// Montar prod, preparar solamente: Docker es un doble y no se inicia ningún servicio.
	a := New(strings.NewReader("3\n2\n18493\nsí\n1\n\n\nno\nall\n2\n"), &out, &out, t.TempDir(), "test")
	a.Home = t.TempDir()
	a.Runner = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	a.HTTP = &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"error":{"details":[{"reason":"API_KEY_INVALID"}]}}`)), Header: make(http.Header)}, nil
	})}
	if a.Run([]string{"start", "--name", "key-recovery", "--plain", "--yes", "--no-tour", "--no-agents"}) != 0 {
		t.Fatal("start abortó en vez de continuar")
	}
	dir := filepath.Join(a.Home, "prod-key-recovery")
	s, err := state.Load(dir)
	if err != nil || !s.Prepared || s.Embedder != "off" || s.Reranker != "off" {
		t.Fatal("start no preparó el entorno sin proveedores")
	}
	env, err := render.ReadEnv(dir)
	if err != nil || env[embed.EnvAPIKey] != "" || env[rerank.EnvAPIKey] != "" {
		t.Fatal("se guardó una clave rechazada")
	}
	if !strings.Contains(out.String(), "clave rechazada") || strings.Contains(out.String(), key) {
		t.Fatal("diagnóstico ausente o inseguro")
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
