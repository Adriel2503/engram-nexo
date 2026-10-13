package rerank

// Prueba de contrato de los adaptadores contra un servidor HTTP simulado (sin
// red): cuerpo de la petición, encabezados de autenticación, orden por
// índice, lotes, respuestas no válidas, reintentos ante 429 y 5xx, tiempo
// máximo, límites y que la clave nunca aparezca en un error.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "clave-secreta-de-prueba-123"

func testEnv(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// call es una petición recibida por el servidor simulado.
type call struct {
	path   string
	query  string
	header http.Header
	body   map[string]any
	raw    string
}

// fakeServer responde según handler(call, n) y registra las peticiones.
type fakeServer struct {
	srv         *httptest.Server
	mu          sync.Mutex
	calls       []call
	inflight    atomic.Int32
	maxInflight atomic.Int32
	handler     func(c call, n int, w http.ResponseWriter)
}

func newFake(t *testing.T, h func(c call, n int, w http.ResponseWriter)) *fakeServer {
	f := &fakeServer{handler: h}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := f.inflight.Add(1)
		for {
			m := f.maxInflight.Load()
			if cur <= m || f.maxInflight.CompareAndSwap(m, cur) {
				break
			}
		}
		defer f.inflight.Add(-1)
		raw, _ := io.ReadAll(r.Body)
		c := call{path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone(), raw: string(raw)}
		_ = json.Unmarshal(raw, &c.body)
		f.mu.Lock()
		f.calls = append(f.calls, c)
		n := len(f.calls)
		f.mu.Unlock()
		f.handler(c, n, w)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func (f *fakeServer) all() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

// docScore es el puntaje simulado de un documento: crece con su largo (así el
// orden esperado se conoce) y queda en [0, 1].
func docScore(doc string) float64 { return float64(len(doc)%10) / 10 }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// rerankReply responde como una API de reordenamiento, con los elementos en
// orden inverso (el adaptador debe ordenar por índice).
func rerankReply(envelope string) func(c call, n int, w http.ResponseWriter) {
	return func(c call, _ int, w http.ResponseWriter) {
		var docs []any
		for _, k := range []string{"documents", "texts"} {
			if d, ok := c.body[k].([]any); ok {
				docs = d
			}
		}
		var items []map[string]any
		for i := len(docs) - 1; i >= 0; i-- {
			it := map[string]any{"index": i}
			if envelope == "tei" {
				it["score"] = (docScore(docs[i].(string)) - 0.5) * 20 // logit
			} else {
				it["relevance_score"] = docScore(docs[i].(string))
			}
			items = append(items, it)
		}
		switch envelope {
		case "voyage":
			writeJSON(w, 200, map[string]any{"object": "list", "data": items})
		case "tei":
			writeJSON(w, 200, items)
		default:
			writeJSON(w, 200, map[string]any{"id": "x", "results": items})
		}
	}
}

func passages(texts ...string) []Passage {
	out := make([]Passage, len(texts))
	for i, t := range texts {
		out[i] = Passage{ID: fmt.Sprint("p", i), Text: t}
	}
	return out
}

func mk(t *testing.T, env map[string]string) *client {
	t.Helper()
	cfg, err := ConfigFrom(testEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := r.(*client)
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func wantScores(t *testing.T, got []Score, docs []Passage) {
	t.Helper()
	if len(got) != len(docs) {
		t.Fatalf("puntajes %d, pasajes %d", len(got), len(docs))
	}
	for i, s := range got {
		if !s.OK || math.Abs(s.Value-docScore(docs[i].Text)) > 1e-9 {
			t.Fatalf("puntaje %d = %+v, se esperaba %.1f", i, s, docScore(docs[i].Text))
		}
	}
}

func TestCohereContract(t *testing.T) {
	f := newFake(t, rerankReply("cohere"))
	c := mk(t, map[string]string{EnvReranker: "cohere", EnvURL: f.srv.URL, EnvAPIKey: testKey, EnvBatch: "3"})
	docs := passages("a", "bb", "ccc", "dddd", "eeeee", "ffffff", "ggggggg")
	got, err := c.Rerank(context.Background(), "¿pregunta?", docs)
	if err != nil {
		t.Fatal(err)
	}
	wantScores(t, got, docs)
	calls := f.all()
	if len(calls) != 3 {
		t.Fatalf("lotes de 3 sobre 7 pasajes = 3 llamadas, hubo %d", len(calls))
	}
	sizes := map[int]int{}
	for _, cl := range calls {
		if cl.path != "/v2/rerank" {
			t.Fatalf("ruta %q", cl.path)
		}
		if cl.header.Get("Authorization") != "Bearer "+testKey || cl.header.Get("api-key") != "" {
			t.Fatalf("autenticación: %v", cl.header)
		}
		if cl.body["model"] != "rerank-v3.5" || cl.body["query"] != "¿pregunta?" {
			t.Fatalf("cuerpo: %v", cl.body)
		}
		d := cl.body["documents"].([]any)
		if int(cl.body["top_n"].(float64)) != len(d) {
			t.Fatalf("top_n %v con %d documentos", cl.body["top_n"], len(d))
		}
		sizes[len(d)]++
	}
	if sizes[3] != 2 || sizes[1] != 1 {
		t.Fatalf("tamaños de lote: %v", sizes)
	}
	if strings.Contains(f.srv.URL, testKey) || strings.Contains(calls[0].raw, testKey) {
		t.Fatal("la clave no va en la URL ni en el cuerpo")
	}
}

func TestCohereAzureAIAPIKeyHeaderAndFullURL(t *testing.T) {
	f := newFake(t, rerankReply("cohere"))
	// Despliegue de Azure AI: la URL ya termina en /rerank y la clave va en api-key.
	c := mk(t, map[string]string{EnvReranker: "azure-cohere", EnvURL: f.srv.URL + "/providers/cohere/v2/rerank", EnvAPIKey: testKey})
	docs := passages("a", "bb", "ccc")
	got, err := c.Rerank(context.Background(), "q", docs)
	if err != nil {
		t.Fatal(err)
	}
	wantScores(t, got, docs)
	cl := f.all()[0]
	if cl.path != "/providers/cohere/v2/rerank" {
		t.Fatalf("ruta: %q", cl.path)
	}
	if cl.header.Get("api-key") != testKey || cl.header.Get("Authorization") != "" {
		t.Fatalf("encabezados de Azure: %v", cl.header)
	}
	// Con bearer explícito sobre Azure AI también funciona.
	c2 := mk(t, map[string]string{EnvReranker: "cohere", EnvURL: f.srv.URL + "/v2", EnvAPIKey: testKey, EnvAuth: "bearer", EnvModel: "rerank-multilingual-v3.0"})
	if _, err := c2.Rerank(context.Background(), "q", docs); err != nil {
		t.Fatal(err)
	}
	last := f.all()[1]
	if last.path != "/v2/rerank" || last.body["model"] != "rerank-multilingual-v3.0" || last.header.Get("Authorization") != "Bearer "+testKey {
		t.Fatalf("segunda llamada: %+v", last)
	}
}

func TestJinaVoyageTEIAndCompatibleDialects(t *testing.T) {
	docs := passages("a", "bb", "ccc", "dddd")
	cases := []struct {
		name  string
		env   map[string]string
		reply string
		path  string
		check func(t *testing.T, c call)
	}{
		{"jina", map[string]string{EnvReranker: "jina"}, "cohere", "/rerank", func(t *testing.T, c call) {
			if c.body["model"] != "jina-reranker-v2-base-multilingual" || c.body["return_documents"] != false || int(c.body["top_n"].(float64)) != 4 {
				t.Fatalf("jina: %v", c.body)
			}
		}},
		{"voyage", map[string]string{EnvReranker: "voyage"}, "voyage", "/rerank", func(t *testing.T, c call) {
			if c.body["model"] != "rerank-2.5" || int(c.body["top_k"].(float64)) != 4 || c.body["truncation"] != true || c.body["top_n"] != nil {
				t.Fatalf("voyage: %v", c.body)
			}
		}},
		{"tei", map[string]string{EnvReranker: "tei"}, "tei", "/rerank", func(t *testing.T, c call) {
			if _, ok := c.body["texts"].([]any); !ok || c.body["truncate"] != true || c.body["documents"] != nil {
				t.Fatalf("tei: %v", c.body)
			}
			if c.header.Get("Authorization") != "" {
				t.Fatalf("tei sin clave no envía Authorization: %v", c.header)
			}
		}},
		{"compatible", map[string]string{EnvReranker: "rerank-compatible", EnvModel: "bge-reranker-v2-m3"}, "cohere", "/v2/rerank", func(t *testing.T, c call) {
			if c.body["model"] != "bge-reranker-v2-m3" {
				t.Fatalf("compatible: %v", c.body)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, rerankReply(tc.reply))
			env := tc.env
			env[EnvURL] = f.srv.URL
			if tc.name != "tei" {
				env[EnvAPIKey] = testKey
			}
			c := mk(t, env)
			got, err := c.Rerank(context.Background(), "q", docs)
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "tei" {
				// Los logits se llevan a [0, 1] con la sigmoide y conservan el orden.
				for i, s := range got {
					want := 1 / (1 + math.Exp(-(docScore(docs[i].Text)-0.5)*20))
					if !s.OK || math.Abs(s.Value-want) > 1e-9 {
						t.Fatalf("tei puntaje %d: %+v, se esperaba %.4f", i, s, want)
					}
				}
			} else {
				wantScores(t, got, docs)
			}
			cl := f.all()[0]
			if cl.path != tc.path {
				t.Fatalf("ruta %q, se esperaba %q", cl.path, tc.path)
			}
			tc.check(t, cl)
		})
	}
}

func TestRerankAPIMalformedResponses(t *testing.T) {
	bodies := map[string]string{
		"vacía":           `{"results":[]}`,
		"sin índice":      `{"results":[{"relevance_score":0.5},{"relevance_score":0.2}]}`,
		"índice fuera":    `{"results":[{"index":0,"relevance_score":0.5},{"index":9,"relevance_score":0.2}]}`,
		"índice repetido": `{"results":[{"index":0,"relevance_score":0.5},{"index":0,"relevance_score":0.2}]}`,
		"falta uno":       `{"results":[{"index":1,"relevance_score":0.5}]}`,
		"no es JSON":      `<html>bad gateway</html>`,
		"sin puntaje":     `{"results":[{"index":0},{"index":1}]}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(_ call, _ int, w http.ResponseWriter) { w.WriteHeader(200); _, _ = w.Write([]byte(body)) })
			c := mk(t, map[string]string{EnvReranker: "cohere", EnvURL: f.srv.URL, EnvAPIKey: testKey})
			got, err := c.Rerank(context.Background(), "q", passages("a", "b"))
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("se esperaba ErrMalformed, got %v", err)
			}
			for _, s := range got {
				if s.OK {
					t.Fatalf("sin puntaje válido: %+v", got)
				}
			}
			if f.count() != 2 {
				t.Fatalf("una respuesta no válida se reintenta una vez: %d llamadas", f.count())
			}
		})
	}
}

func TestRetriesAndKeyRedaction(t *testing.T) {
	t.Run("429 con Retry-After y 503, luego éxito", func(t *testing.T) {
		f := newFake(t, func(c call, n int, w http.ResponseWriter) {
			switch n {
			case 1:
				w.Header().Set("Retry-After", "7")
				writeJSON(w, 429, map[string]any{"message": "rate limit " + testKey})
			case 2:
				writeJSON(w, 503, map[string]any{"message": "down"})
			default:
				rerankReply("cohere")(c, n, w)
			}
		})
		c := mk(t, map[string]string{EnvReranker: "cohere", EnvURL: f.srv.URL, EnvAPIKey: testKey})
		var waits []time.Duration
		c.sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
		docs := passages("a", "bb")
		got, err := c.Rerank(context.Background(), "q", docs)
		if err != nil {
			t.Fatal(err)
		}
		wantScores(t, got, docs)
		if f.count() != 3 || len(waits) != 2 {
			t.Fatalf("llamadas %d, esperas %v", f.count(), waits)
		}
		if waits[0] != 7*time.Second {
			t.Fatalf("respeta Retry-After: %v", waits[0])
		}
		if waits[1] <= 0 || waits[1] > 2*time.Second {
			t.Fatalf("espera exponencial con fluctuación: %v", waits[1])
		}
		if s := c.Stats(); s.Calls != 3 || s.Retries != 2 {
			t.Fatalf("estadísticas: %+v", s)
		}
	})
	t.Run("agota los reintentos: ErrUnavailable sin la clave", func(t *testing.T) {
		f := newFake(t, func(_ call, _ int, w http.ResponseWriter) {
			writeJSON(w, 500, map[string]any{"message": "internal error with " + testKey})
		})
		c := mk(t, map[string]string{EnvReranker: "cohere", EnvURL: f.srv.URL, EnvAPIKey: testKey, EnvRetries: "2"})
		got, err := c.Rerank(context.Background(), "q", passages("a", "b"))
		if !errors.Is(err, ErrUnavailable) || !IsTransient(err) {
			t.Fatalf("se esperaba ErrUnavailable: %v", err)
		}
		if f.count() != 3 {
			t.Fatalf("1 + 2 reintentos: %d", f.count())
		}
		if strings.Contains(err.Error(), testKey) {
			t.Fatalf("la clave apareció en el error: %v", err)
		}
		for _, s := range got {
			if s.OK {
				t.Fatalf("sin puntajes: %+v", got)
			}
		}
	})
	t.Run("401 es permanente: sin reintentos y sin la clave", func(t *testing.T) {
		f := newFake(t, func(_ call, _ int, w http.ResponseWriter) {
			writeJSON(w, 401, map[string]any{"error": map[string]any{"message": "invalid api key " + testKey, "status": "UNAUTHENTICATED"}})
		})
		c := mk(t, map[string]string{EnvReranker: "voyage", EnvURL: f.srv.URL, EnvAPIKey: testKey})
		_, err := c.Rerank(context.Background(), "q", passages("a"))
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != 401 || IsTransient(err) {
			t.Fatalf("401: %v", err)
		}
		if f.count() != 1 || strings.Contains(err.Error(), testKey) {
			t.Fatalf("llamadas %d, error %v", f.count(), err)
		}
	})
}

func TestCallTimeoutAndContext(t *testing.T) {
	release := make(chan struct{})
	f := newFake(t, func(_ call, _ int, w http.ResponseWriter) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(200)
	})
	defer close(release)
	cfg, _ := ConfigFrom(testEnv(map[string]string{EnvReranker: "cohere", EnvURL: f.srv.URL, EnvAPIKey: testKey, EnvRetries: "0"}))
	cfg.Timeout = 60 * time.Millisecond
	r, _ := New(cfg)
	start := time.Now()
	got, err := r.Rerank(context.Background(), "q", passages("a", "b"))
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("el tiempo máximo por llamada corta la espera: %v en %s", err, time.Since(start))
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("un timeout agotado es transitorio: %v", err)
	}
	if got[0].OK || got[1].OK {
		t.Fatalf("sin puntajes: %+v", got)
	}
	// El contexto del llamador manda sobre los reintentos.
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	cfg.Timeout, cfg.Retries = 10*time.Second, 5
	r, _ = New(cfg)
	start = time.Now()
	if _, err := r.Rerank(ctx, "q", passages("a")); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("contexto vencido: %v en %s", err, time.Since(start))
	}
}

func TestRPMAndConcurrency(t *testing.T) {
	f := newFake(t, func(c call, n int, w http.ResponseWriter) {
		time.Sleep(30 * time.Millisecond)
		rerankReply("cohere")(c, n, w)
	})
	c := mk(t, map[string]string{EnvReranker: "cohere", EnvURL: f.srv.URL, EnvAPIKey: testKey, EnvBatch: "1", EnvConcurrency: "2"})
	docs := passages("a", "bb", "ccc", "dddd", "eeeee", "ffffff")
	got, err := c.Rerank(context.Background(), "q", docs)
	if err != nil {
		t.Fatal(err)
	}
	wantScores(t, got, docs)
	if m := f.maxInflight.Load(); m != 2 {
		t.Fatalf("a lo sumo 2 llamadas en paralelo (y se aprovechan): %d", m)
	}
	// RPM: la tercera llamada del minuto espera.
	c2 := mk(t, map[string]string{EnvReranker: "cohere", EnvURL: f.srv.URL, EnvAPIKey: testKey, EnvBatch: "1", EnvRPM: "2"})
	clock := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	c2.now = func() time.Time { return clock }
	var slept time.Duration
	c2.sleep = func(_ context.Context, d time.Duration) error { slept += d; clock = clock.Add(d); return nil }
	if _, err := c2.Rerank(context.Background(), "q", passages("a", "b", "c")); err != nil {
		t.Fatal(err)
	}
	if slept < 59*time.Second || slept > 61*time.Second {
		t.Fatalf("RPM=2 con 3 llamadas espera cerca de un minuto: %s", slept)
	}
}

// ─── LLM ─────────────────────────────────────────────────────────────────────

// llmReply responde como un LLM: lee los pasajes del prompt y devuelve su
// puntaje en el formato de la API (gemini u openai).
func llmReply(api string, content func(n int, ids int) string) func(c call, n int, w http.ResponseWriter) {
	return func(c call, n int, w http.ResponseWriter) {
		prompt := ""
		switch api {
		case "gemini":
			prompt = c.body["contents"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
		default:
			prompt = c.body["messages"].([]any)[1].(map[string]any)["content"].(string)
		}
		ids := strings.Count(prompt, "<passage id=")
		text := content(n, ids)
		if text == "" {
			var sc []string
			for i := 1; i <= ids; i++ {
				sc = append(sc, fmt.Sprintf(`{"id":%d,"score":%d}`, i, i%11))
			}
			text = `{"scores":[` + strings.Join(sc, ",") + `]}`
		}
		if api == "gemini" {
			writeJSON(w, 200, map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}}}},
				"usageMetadata": map[string]any{"promptTokenCount": 1000, "candidatesTokenCount": 40}})
			return
		}
		writeJSON(w, 200, map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": text}}},
			"usage": map[string]any{"prompt_tokens": 800, "completion_tokens": 30}})
	}
}

func TestLLMGeminiContract(t *testing.T) {
	f := newFake(t, llmReply("gemini", func(int, int) string { return "" }))
	c := mk(t, map[string]string{EnvReranker: "gemini", EnvURL: f.srv.URL + "/v1beta", EnvAPIKey: testKey, EnvBatch: "3", EnvConcurrency: "1"})
	docs := passages("uno", "dos", "tres", "cuatro", "cinco", "seis", "siete")
	got, err := c.Rerank(context.Background(), "¿cómo se loguea?", docs)
	if err != nil {
		t.Fatal(err)
	}
	// El simulado puntúa a cada pasaje con su posición dentro del lote (id % 11) / 10.
	want := []float64{0.1, 0.2, 0.3, 0.1, 0.2, 0.3, 0.1}
	for i, s := range got {
		if !s.OK || math.Abs(s.Value-want[i]) > 1e-9 {
			t.Fatalf("puntaje %d = %+v, se esperaba %.1f", i, s, want[i])
		}
	}
	calls := f.all()
	if len(calls) != 3 {
		t.Fatalf("llamadas: %d", len(calls))
	}
	cl := calls[0]
	if cl.path != "/v1beta/models/gemini-2.5-flash:generateContent" || cl.query != "" {
		t.Fatalf("ruta %q?%q (la clave no va en la URL)", cl.path, cl.query)
	}
	if cl.header.Get("x-goog-api-key") != testKey || cl.header.Get("Authorization") != "" {
		t.Fatalf("autenticación: %v", cl.header)
	}
	gen := cl.body["generationConfig"].(map[string]any)
	if gen["responseMimeType"] != "application/json" || gen["temperature"] != float64(0) {
		t.Fatalf("generationConfig: %v", gen)
	}
	if tc, ok := gen["thinkingConfig"].(map[string]any); !ok || tc["thinkingBudget"] != float64(0) {
		t.Fatalf("gemini-2.5-flash va sin razonamiento: %v", gen)
	}
	if cl.body["systemInstruction"] == nil {
		t.Fatal("la rúbrica va como instrucción de sistema")
	}
	prompt := cl.body["contents"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
	for _, want := range []string{"Query: ¿cómo se loguea?", `<passage id="1">`, "uno", `<passage id="3">`, "tres", "ids 1 to 3"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt sin %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "cuatro") {
		t.Fatal("el lote 1 lleva solo 3 pasajes")
	}
	if s := c.Stats(); s.Calls != 3 || s.PromptTokens != 3000 || s.OutputTokens != 120 {
		t.Fatalf("tokens informados por el proveedor: %+v", s)
	}
}

func TestLLMOpenAIFamilies(t *testing.T) {
	docs := passages("uno", "dos")
	cases := []struct {
		name  string
		env   map[string]string
		path  string
		query string
		auth  func(h http.Header) bool
		model bool
	}{
		{"openai", map[string]string{EnvReranker: "openai", EnvModel: "gpt-test"}, "/v1/chat/completions", "",
			func(h http.Header) bool { return h.Get("Authorization") == "Bearer "+testKey }, true},
		{"azure-openai", map[string]string{EnvReranker: "azure-openai", EnvDeployment: "mi-chat"}, "/openai/deployments/mi-chat/chat/completions", "api-version=2024-10-21",
			func(h http.Header) bool { return h.Get("api-key") == testKey && h.Get("Authorization") == "" }, false},
		{"openai-compatible", map[string]string{EnvReranker: "llm", EnvLLM: "openai-compatible", EnvModel: "qwen", EnvAuth: "api-key"}, "/chat/completions", "",
			func(h http.Header) bool { return h.Get("api-key") == testKey }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, llmReply("openai", func(int, int) string {
				return "```json\n{\"scores\":[{\"id\":2,\"score\":9},{\"id\":1,\"score\":3}]}\n```"
			}))
			env := tc.env
			env[EnvURL], env[EnvAPIKey] = f.srv.URL+map[bool]string{true: "/v1", false: ""}[tc.name == "openai"], testKey
			c := mk(t, env)
			got, err := c.Rerank(context.Background(), "q", docs)
			if err != nil {
				t.Fatal(err)
			}
			if !got[0].OK || got[0].Value != 0.3 || got[1].Value != 0.9 {
				t.Fatalf("los ids van por id, no por orden, y se aceptan cercas de código: %+v", got)
			}
			cl := f.all()[0]
			if cl.path != tc.path || cl.query != tc.query {
				t.Fatalf("ruta %q?%q", cl.path, cl.query)
			}
			if !tc.auth(cl.header) {
				t.Fatalf("autenticación: %v", cl.header)
			}
			if _, ok := cl.body["model"]; ok != tc.model {
				t.Fatalf("model en el cuerpo: %v (esperado %v)", ok, tc.model)
			}
			if rf := cl.body["response_format"].(map[string]any); rf["type"] != "json_object" || cl.body["temperature"] != float64(0) {
				t.Fatalf("cuerpo: %v", cl.body)
			}
			msgs := cl.body["messages"].([]any)
			if msgs[0].(map[string]any)["role"] != "system" || !strings.Contains(msgs[1].(map[string]any)["content"].(string), `<passage id="2">`) {
				t.Fatalf("mensajes: %v", msgs)
			}
			if s := c.Stats(); s.PromptTokens != 800 || s.OutputTokens != 30 {
				t.Fatalf("tokens: %+v", s)
			}
		})
	}
}

func TestLLMMalformedBatchFallsBackOnlyThatBatch(t *testing.T) {
	// 6 pasajes en 2 lotes de 3: el modelo contesta bien el primero y basura
	// el segundo (también al reintento). El primero queda puntuado; el segundo,
	// sin puntaje, y la llamada informa el error para que la búsqueda conserve
	// su orden.
	var bad atomic.Int32
	f := newFake(t, llmReply("gemini", func(n int, ids int) string {
		return "" // se decide abajo por contenido
	}))
	inner := f.handler
	f.handler = func(c call, n int, w http.ResponseWriter) {
		prompt := c.body["contents"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
		if strings.Contains(prompt, "cuatro") {
			bad.Add(1)
			writeJSON(w, 200, map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]any{"text": "No puedo ayudarte con eso."}}}}}})
			return
		}
		inner(c, n, w)
	}
	c := mk(t, map[string]string{EnvReranker: "gemini", EnvURL: f.srv.URL, EnvAPIKey: testKey, EnvBatch: "3", EnvConcurrency: "1"})
	got, err := c.Rerank(context.Background(), "q", passages("uno", "dos", "tres", "cuatro", "cinco", "seis"))
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("error esperado: %v", err)
	}
	for i, s := range got {
		if s.OK != (i < 3) {
			t.Fatalf("solo el primer lote tiene puntaje: %+v", got)
		}
	}
	if bad.Load() != 2 {
		t.Fatalf("el lote no válido se reintenta una vez: %d", bad.Load())
	}
	if s := c.Stats(); s.FailedBatch != 1 || s.Malformed != 2 {
		t.Fatalf("estadísticas: %+v", s)
	}
}

func TestLLMFinishReasonAndBlock(t *testing.T) {
	for name, resp := range map[string]map[string]any{
		"cortada":        {"candidates": []any{map[string]any{"finishReason": "MAX_TOKENS", "content": map[string]any{"parts": []any{map[string]any{"text": `{"scores":[`}}}}}},
		"bloqueada":      {"promptFeedback": map[string]any{"blockReason": "SAFETY"}},
		"sin candidatos": {"candidates": []any{}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(_ call, _ int, w http.ResponseWriter) { writeJSON(w, 200, resp) })
			c := mk(t, map[string]string{EnvReranker: "gemini", EnvURL: f.srv.URL, EnvAPIKey: testKey, EnvRetries: "0"})
			got, err := c.Rerank(context.Background(), "q", passages("a"))
			if !errors.Is(err, ErrMalformed) || got[0].OK {
				t.Fatalf("%s: %v %+v", name, err, got)
			}
		})
	}
}

func TestGeminiRetryDelayAndKeyRedaction(t *testing.T) {
	f := newFake(t, func(c call, n int, w http.ResponseWriter) {
		if n == 1 {
			writeJSON(w, 429, map[string]any{"error": map[string]any{"code": 429, "status": "RESOURCE_EXHAUSTED", "message": "quota for key " + testKey,
				"details": []any{map[string]any{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "37s"}}}})
			return
		}
		llmReply("gemini", func(int, int) string { return "" })(c, n, w)
	})
	c := mk(t, map[string]string{EnvReranker: "gemini", EnvURL: f.srv.URL, EnvAPIKey: testKey})
	var waits []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	if _, err := c.Rerank(context.Background(), "q", passages("a")); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 1 || waits[0] != 37*time.Second {
		t.Fatalf("respeta el retryDelay de Google: %v", waits)
	}
	// Clave inválida: el error es permanente y no la repite.
	f2 := newFake(t, func(_ call, _ int, w http.ResponseWriter) {
		writeJSON(w, 400, map[string]any{"error": map[string]any{"status": "INVALID_ARGUMENT", "message": "API key not valid: " + testKey}})
	})
	c2 := mk(t, map[string]string{EnvReranker: "gemini", EnvURL: f2.srv.URL, EnvAPIKey: testKey})
	_, err := c2.Rerank(context.Background(), "q", passages("a"))
	if err == nil || strings.Contains(err.Error(), testKey) || !strings.Contains(err.Error(), "[clave]") {
		t.Fatalf("la clave debe borrarse: %v", err)
	}
}

func TestParseScores(t *testing.T) {
	ok := map[string]struct {
		raw  string
		n    int
		want []float64
	}{
		"objeto":             {`{"scores":[{"id":1,"score":7},{"id":2,"score":0}]}`, 2, []float64{0.7, 0}},
		"desordenado":        {`{"scores":[{"id":2,"score":10},{"id":1,"score":4}]}`, 2, []float64{0.4, 1}},
		"cercas":             {"```json\n{\"scores\":[{\"id\":1,\"score\":5}]}\n```", 1, []float64{0.5}},
		"texto alrededor":    {`Claro, aquí está: {"scores":[{"id":1,"score":6},{"id":2,"score":2}]} espero que sirva`, 2, []float64{0.6, 0.2}},
		"arreglo de objetos": {`[{"id":1,"score":3},{"id":2,"score":9}]`, 2, []float64{0.3, 0.9}},
		"arreglo de números": {`[8, 1, 5]`, 3, []float64{0.8, 0.1, 0.5}},
		"números en texto":   {`{"scores":[{"id":"1","score":"7/10"},{"id":2,"score":"3.5"}]}`, 2, []float64{0.7, 0.35}},
		"mapa por id":        {`{"1": 9, "2": 2}`, 2, []float64{0.9, 0.2}},
		"fuera de rango":     {`{"scores":[{"id":1,"score":15},{"id":2,"score":-3}]}`, 2, []float64{1, 0}},
		"otro nombre":        {`{"ratings":[{"index":0,"relevance":4},{"index":1,"relevance":6}]}`, 2, []float64{0.4, 0.6}},
	}
	for name, tc := range ok {
		t.Run(name, func(t *testing.T) {
			got, err := parseScores(tc.raw, tc.n)
			if err != nil {
				t.Fatal(err)
			}
			for i := range tc.want {
				if math.Abs(got[i]-tc.want[i]) > 1e-9 {
					t.Fatalf("got %v want %v", got, tc.want)
				}
			}
		})
	}
	bad := map[string]struct {
		raw string
		n   int
	}{
		"vacío":               {``, 1},
		"texto":               {`no sé`, 1},
		"falta un pasaje":     {`{"scores":[{"id":1,"score":5}]}`, 2},
		"id repetido":         {`{"scores":[{"id":1,"score":5},{"id":1,"score":6}]}`, 2},
		"id fuera":            {`{"scores":[{"id":1,"score":5},{"id":7,"score":6}]}`, 2},
		"puntaje no numérico": {`{"scores":[{"id":1,"score":"alto"},{"id":2,"score":6}]}`, 2},
		"sin puntaje":         {`{"scores":[{"id":1},{"id":2,"score":6}]}`, 2},
		"números de más":      {`[1,2,3]`, 2},
		"escalar":             {`7`, 1},
		"JSON cortado":        {`{"scores":[{"id":1,"score":5},`, 2},
	}
	for name, tc := range bad {
		t.Run("malo "+name, func(t *testing.T) {
			if _, err := parseScores(tc.raw, tc.n); !errors.Is(err, ErrMalformed) {
				t.Fatalf("se esperaba ErrMalformed: %v", err)
			}
		})
	}
}

// ─── Configuración y caché ───────────────────────────────────────────────────

func TestConfigFrom(t *testing.T) {
	if c, err := ConfigFrom(testEnv(nil)); err != nil || c.Enabled() {
		t.Fatalf("sin NEXO_KB_RERANKER: desactivado sin error: %+v %v", c, err)
	}
	if c, err := ConfigFrom(testEnv(map[string]string{EnvReranker: "off", EnvAPIKey: testKey})); err != nil || c.Enabled() {
		t.Fatalf("off: %+v %v", c, err)
	}
	errCases := map[string]map[string]string{
		"proveedor inválido":        {EnvReranker: "bing"},
		"cohere sin clave":          {EnvReranker: "cohere"},
		"compatible sin URL":        {EnvReranker: "rerank-compatible", EnvAPIKey: testKey},
		"tei sin URL":               {EnvReranker: "tei"},
		"openai sin modelo":         {EnvReranker: "openai", EnvAPIKey: testKey},
		"azure sin despliegue":      {EnvReranker: "azure-openai", EnvAPIKey: testKey, EnvURL: "https://x.openai.azure.com"},
		"llm con API inválida":      {EnvReranker: "llm", EnvLLM: "bard", EnvAPIKey: testKey},
		"autenticación inválida":    {EnvReranker: "cohere", EnvAPIKey: testKey, EnvAuth: "basic"},
		"thinking inválido":         {EnvReranker: "gemini", EnvAPIKey: testKey, EnvThinking: "mucho"},
		"archivo de clave faltante": {EnvReranker: "cohere", EnvAPIKeyFile: "no-existe.txt"},
	}
	for name, env := range errCases {
		if _, err := ConfigFrom(testEnv(env)); err == nil {
			t.Errorf("%s: se esperaba un error", name)
		} else if strings.Contains(err.Error(), testKey) {
			t.Errorf("%s: el error repite la clave", name)
		}
	}
	// Gemini: clave de GEMINI_API_KEY (la misma que usan los embeddings).
	c, err := ConfigFrom(testEnv(map[string]string{EnvReranker: "gemini", "GEMINI_API_KEY": testKey}))
	if err != nil || c.Provider != ProviderLLM || c.API != APIGemini || c.Model != DefaultGeminiModel || c.Thinking != 0 || c.Batch != 25 {
		t.Fatalf("gemini por defecto: %+v %v", c, err)
	}
	// Un modelo pro no puede apagar el razonamiento.
	if c, _ := ConfigFrom(testEnv(map[string]string{EnvReranker: "gemini", EnvModel: "models/gemini-2.5-pro", EnvAPIKey: testKey})); c.Thinking != -1 || c.Model != "gemini-2.5-pro" {
		t.Fatalf("gemini pro: %+v", c)
	}
	if c, _ := ConfigFrom(testEnv(map[string]string{EnvReranker: "gemini", EnvAPIKey: testKey, EnvThinking: "512", EnvTemperature: "default"})); c.Thinking != 512 || !c.NoTemperature {
		t.Fatalf("thinking y temperatura: %+v", c)
	}
	// La clave no sale en la descripción ni en el JSON de la configuración.
	raw, _ := json.Marshal(c)
	desc, _ := json.Marshal(c.Describe())
	if strings.Contains(string(raw), testKey) || strings.Contains(string(desc), testKey) {
		t.Fatal("la clave no puede serializarse")
	}
	if c.ID() != "llm-gemini" {
		t.Fatalf("ID: %q", c.ID())
	}
	// Archivo de clave.
	dir := t.TempDir()
	path := dir + "/k.txt"
	if err := writeFile(path, testKey+"\n"); err != nil {
		t.Fatal(err)
	}
	if c, err := ConfigFrom(testEnv(map[string]string{EnvReranker: "cohere", EnvAPIKeyFile: path})); err != nil || c.APIKey != testKey {
		t.Fatalf("archivo de clave: %v", err)
	}
	// Alias de proveedor.
	for alias, want := range map[string]string{"azure-ai": "cohere", "compatible": "rerank-compatible", "llm": "llm-gemini", "openai": "llm-openai", "openai-compatible": "llm-openai-compatible"} {
		env := map[string]string{EnvReranker: alias, EnvAPIKey: testKey, EnvURL: "https://x.example", EnvModel: "m", EnvDeployment: "d"}
		if got, err := ConfigFrom(testEnv(env)); err != nil || got.ID() != want {
			t.Errorf("alias %s: %q %v", alias, got.ID(), err)
		}
	}
}

func TestCacheReusesScoresAndAsksOnlyForMisses(t *testing.T) {
	f := newFake(t, rerankReply("cohere"))
	c := mk(t, map[string]string{EnvReranker: "cohere", EnvURL: f.srv.URL, EnvAPIKey: testKey})
	r := WithCache(c, CacheOptions{TTL: time.Hour, Max: 100})
	docs := passages("a", "bb", "ccc")
	if _, err := r.Rerank(context.Background(), "q", docs); err != nil {
		t.Fatal(err)
	}
	got, err := r.Rerank(context.Background(), "q", docs)
	if err != nil || f.count() != 1 {
		t.Fatalf("la repetición sale de la caché: %d llamadas, %v", f.count(), err)
	}
	wantScores(t, got, docs)
	// Pregunta distinta: no comparte puntajes. Un pasaje nuevo: solo se pide ese.
	if _, err := r.Rerank(context.Background(), "otra", docs); err != nil || f.count() != 2 {
		t.Fatalf("otra pregunta: %d llamadas, %v", f.count(), err)
	}
	more := append(passages("a", "bb"), Passage{ID: "n", Text: "dddd"})
	got, err = r.Rerank(context.Background(), "q", more)
	if err != nil || f.count() != 3 {
		t.Fatalf("un pasaje nuevo: %d llamadas, %v", f.count(), err)
	}
	if n := len(f.all()[2].body["documents"].([]any)); n != 1 {
		t.Fatalf("solo se pidió el pasaje que faltaba: %d documentos", n)
	}
	wantScores(t, got, more)
	if r.Name() != "cohere" || r.Model() != "rerank-v3.5" {
		t.Fatalf("nombre y modelo se reenvían: %s %s", r.Name(), r.Model())
	}
	if s, ok := r.(StatsReporter); !ok || s.Stats().Calls != 3 {
		t.Fatalf("estadísticas reenviadas")
	}
	// Los fallos no se guardan.
	bad := newFake(t, func(_ call, _ int, w http.ResponseWriter) { writeJSON(w, 400, map[string]any{"message": "no"}) })
	rb := WithCache(mk(t, map[string]string{EnvReranker: "cohere", EnvURL: bad.srv.URL, EnvAPIKey: testKey}), CacheOptions{})
	for i := 0; i < 2; i++ {
		if _, err := rb.Rerank(context.Background(), "q", docs); err == nil {
			t.Fatal("se esperaba un error")
		}
	}
	if bad.count() != 2 {
		t.Fatalf("un fallo no queda en caché: %d", bad.count())
	}
}

func TestEmptyAndOversizedInputs(t *testing.T) {
	f := newFake(t, rerankReply("cohere"))
	c := mk(t, map[string]string{EnvReranker: "cohere", EnvURL: f.srv.URL, EnvAPIKey: testKey, EnvMaxChars: "200"})
	if got, err := c.Rerank(context.Background(), "q", nil); err != nil || len(got) != 0 || f.count() != 0 {
		t.Fatalf("sin pasajes no hay llamada: %v %d", err, f.count())
	}
	long := strings.Repeat("ñ", 500) // 1000 bytes: se corta en un límite de carácter
	if _, err := c.Rerank(context.Background(), "q", passages(long, "   ")); err != nil {
		t.Fatal(err)
	}
	docs := f.all()[0].body["documents"].([]any)
	if l := len(docs[0].(string)); l > 200 || !strings.HasSuffix(docs[0].(string), "ñ") {
		t.Fatalf("el pasaje se recorta sin partir un carácter: %d bytes", l)
	}
	if docs[1].(string) != "(vacío)" {
		t.Fatalf("un pasaje vacío no viaja vacío: %q", docs[1])
	}
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }
