package embed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAPI simula Gemini, OpenAI, Azure OpenAI y los compatibles: responde
// vectores deterministas de dims dimensiones (sin normalizar) y registra lo
// que recibió. failFirst respuestas con failStatus antes de responder bien.
type fakeAPI struct {
	dims       int
	failFirst  int32
	failStatus int
	calls      atomic.Int32
	lastPath   string
	lastQuery  string
	lastAuth   http.Header
	lastBody   map[string]any
	batches    []int
}

const testKey = "clave-secreta-de-prueba-123"

func rawVec(text string, dims int) []float32 {
	v := make([]float32, dims)
	for i := range v {
		v[i] = float32(len(text)%7+i+1) / 10
	}
	return v
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := f.calls.Add(1)
	f.lastPath, f.lastQuery, f.lastAuth = r.URL.Path, r.URL.RawQuery, r.Header.Clone()
	raw, _ := io.ReadAll(r.Body)
	f.lastBody = map[string]any{}
	_ = json.Unmarshal(raw, &f.lastBody)
	if n <= f.failFirst {
		status := f.failStatus
		if status == 0 {
			status = http.StatusTooManyRequests
		}
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"quota exceeded for ` + testKey + `"}}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, ":batchEmbedContents") {
		reqs, _ := f.lastBody["requests"].([]any)
		f.batches = append(f.batches, len(reqs))
		var embs []map[string]any
		for _, rq := range reqs {
			m := rq.(map[string]any)
			text := m["content"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
			embs = append(embs, map[string]any{"values": rawVec(text, f.dims)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": embs})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/embeddings") {
		in, _ := f.lastBody["input"].([]any)
		f.batches = append(f.batches, len(in))
		var data []map[string]any
		// Orden invertido: el adaptador debe ordenar por index.
		for i := len(in) - 1; i >= 0; i-- {
			data = append(data, map[string]any{"index": i, "embedding": rawVec(in[i].(string), f.dims)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func testEnv(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

type contractCase struct {
	name string
	env  map[string]string
	dims int
	// check revisa la última petición.
	check func(t *testing.T, f *fakeAPI, c Config)
}

func bodyDims(f *fakeAPI) (float64, bool) {
	if reqs, ok := f.lastBody["requests"].([]any); ok {
		v, ok := reqs[0].(map[string]any)["outputDimensionality"].(float64)
		return v, ok
	}
	v, ok := f.lastBody["dimensions"].(float64)
	return v, ok
}

func wantDims(want int) func(*testing.T, *fakeAPI, Config) {
	return func(t *testing.T, f *fakeAPI, _ Config) {
		t.Helper()
		got, ok := bodyDims(f)
		if want == 0 && ok {
			t.Fatalf("no debía enviar la dimensión (%v)", got)
		}
		if want > 0 && (!ok || int(got) != want) {
			t.Fatalf("dimensión enviada %v, se esperaba %d", got, want)
		}
	}
}

func contractCases(base string) []contractCase {
	return []contractCase{
		{name: "gemini-embedding-001-768", dims: 768, env: map[string]string{EnvEmbedder: "gemini", EnvURL: base + "/v1beta", "GEMINI_API_KEY": testKey, EnvBatch: "3"},
			check: func(t *testing.T, f *fakeAPI, c Config) {
				if f.lastPath != "/v1beta/models/gemini-embedding-001:batchEmbedContents" {
					t.Fatalf("ruta %s", f.lastPath)
				}
				if f.lastAuth.Get("x-goog-api-key") != testKey || strings.Contains(f.lastQuery, "key") {
					t.Fatal("la clave va en x-goog-api-key, nunca en la URL")
				}
				if r0 := f.lastBody["requests"].([]any)[0].(map[string]any); r0["model"] != "models/gemini-embedding-001" {
					t.Fatalf("pedido %v", r0)
				}
				wantDims(768)(t, f, c)
				if !c.Normalize {
					t.Fatal("gemini por debajo de 3072 se normaliza")
				}
			}},
		{name: "gemini-embedding-001-3072", dims: 3072, env: map[string]string{EnvEmbedder: "gemini", EnvURL: base, "GEMINI_API_KEY": testKey, EnvDims: "3072", EnvBatch: "3"},
			check: func(t *testing.T, f *fakeAPI, c Config) {
				wantDims(3072)(t, f, c)
				if c.Normalize || StorageType(c.Dims) != "halfvec" {
					t.Fatalf("3072: normalizado de origen y halfvec (%v %s)", c.Normalize, StorageType(c.Dims))
				}
			}},
		{name: "openai-3-small-768", dims: 768, env: map[string]string{EnvEmbedder: "openai", EnvURL: base + "/v1", "OPENAI_API_KEY": testKey, EnvDims: "768", EnvBatch: "3"},
			check: func(t *testing.T, f *fakeAPI, c Config) {
				if f.lastPath != "/v1/embeddings" || f.lastAuth.Get("Authorization") != "Bearer "+testKey {
					t.Fatalf("ruta %s auth %q", f.lastPath, f.lastAuth.Get("Authorization"))
				}
				if f.lastBody["model"] != "text-embedding-3-small" {
					t.Fatalf("cuerpo %v", f.lastBody)
				}
				wantDims(768)(t, f, c)
			}},
		{name: "openai-3-large-3072", dims: 3072, env: map[string]string{EnvEmbedder: "openai", EnvModel: "text-embedding-3-large", EnvURL: base + "/v1", "OPENAI_API_KEY": testKey, EnvBatch: "3"},
			check: func(t *testing.T, f *fakeAPI, c Config) {
				wantDims(3072)(t, f, c)
				if c.Dims != 3072 || c.Describe()["storage"] != "halfvec" {
					t.Fatalf("large por defecto 3072 en halfvec: %+v", c.Describe())
				}
			}},
		{name: "openai-ada-002", dims: 1536, env: map[string]string{EnvEmbedder: "openai", EnvModel: "text-embedding-ada-002", EnvURL: base + "/v1", "OPENAI_API_KEY": testKey, EnvBatch: "3"},
			check: func(t *testing.T, f *fakeAPI, c Config) { wantDims(0)(t, f, c) }},
		{name: "azure-openai", dims: 1536, env: map[string]string{EnvEmbedder: "azure-openai", EnvURL: base, EnvDeployment: "emb-prod", EnvAPIKey: testKey, EnvBatch: "3"},
			check: func(t *testing.T, f *fakeAPI, c Config) {
				if f.lastPath != "/openai/deployments/emb-prod/embeddings" || f.lastQuery != "api-version="+DefaultAzureAPIVersion {
					t.Fatalf("ruta %s?%s", f.lastPath, f.lastQuery)
				}
				if f.lastAuth.Get("api-key") != testKey || f.lastAuth.Get("Authorization") != "" {
					t.Fatal("Azure usa el encabezado api-key")
				}
				if _, ok := f.lastBody["model"]; ok {
					t.Fatal("Azure: el despliegue va en la ruta, no el modelo en el cuerpo")
				}
				wantDims(1536)(t, f, c)
			}},
		{name: "compatible-url-con-consulta", dims: 8, env: map[string]string{EnvEmbedder: "openai-compatible", EnvURL: base + "/openai/deployments/emb?api-version=2024-10-21",
			EnvModel: "emb", EnvDims: "8", EnvAPIKey: testKey, EnvAuth: "api-key", EnvBatch: "3", EnvQueryPrefix: "query: ", EnvDocPrefix: "passage: "},
			check: func(t *testing.T, f *fakeAPI, c Config) {
				if f.lastPath != "/openai/deployments/emb/embeddings" || f.lastQuery != "api-version=2024-10-21" {
					t.Fatalf("ruta %s?%s", f.lastPath, f.lastQuery)
				}
				if f.lastAuth.Get("api-key") != testKey {
					t.Fatal("encabezado api-key")
				}
				wantDims(0)(t, f, c) // modelo desconocido: no se envía sin NEXO_KB_EMBED_SEND_DIMS
			}},
		{name: "compatible-ollama", dims: 8, env: map[string]string{EnvEmbedder: "openai-compatible", EnvURL: base + "/v1", EnvModel: "bge-m3", EnvDims: "8", EnvBatch: "3"},
			check: func(t *testing.T, f *fakeAPI, c Config) {
				if f.lastAuth.Get("Authorization") != "" {
					t.Fatal("sin clave no se envía autenticación")
				}
			}},
	}
}

// TestEmbedderContract es la prueba de contrato común: todo adaptador debe
// pasarla (lotes, orden, dimensión, normalización, reintentos ante 429 y 5xx,
// errores sin la clave).
func TestEmbedderContract(t *testing.T) {
	for i := range contractCases("") {
		f := &fakeAPI{}
		srv := httptest.NewServer(f)
		tc := contractCases(srv.URL)[i]
		f.dims = tc.dims
		t.Run(tc.name, func(t *testing.T) {
			defer srv.Close()
			cfg, err := ConfigFrom(testEnv(tc.env))
			if err != nil {
				t.Fatal(err)
			}
			if raw, _ := json.Marshal(cfg); strings.Contains(string(raw), testKey) {
				t.Fatal("la configuración serializada no debe incluir la clave")
			}
			for _, v := range cfg.Describe() {
				if s, ok := v.(string); ok && strings.Contains(s, testKey) {
					t.Fatal("Describe no debe incluir la clave")
				}
			}
			e, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			cl := e.(*client)
			var slept []time.Duration
			cl.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
			if e.Dims() != tc.dims || e.Provider() == "" || e.Model() == "" {
				t.Fatalf("identidad %s %s %d", e.Provider(), e.Model(), e.Dims())
			}
			texts := []string{"uno", "dos dos", "tres tres tres", "cuatro", "cinco cinco", "seis seis seis"}
			vecs, err := e.Embed(context.Background(), texts, KindDocument)
			if err != nil {
				t.Fatal(err)
			}
			if len(vecs) != len(texts) {
				t.Fatalf("vectores %d", len(vecs))
			}
			for i, v := range vecs {
				want := rawVec(cl.cfg.DocPrefix+texts[i], tc.dims)
				if cfg.Normalize {
					normalize(want)
				}
				if len(v) != tc.dims || math.Abs(float64(v[0]-want[0])) > 1e-6 {
					t.Fatalf("vector %d fuera de orden o de dimensión: %v", i, v[:2])
				}
				if cfg.Normalize && math.Abs(norm(v)-1) > 1e-4 {
					t.Fatalf("vector %d sin normalizar: %f", i, norm(v))
				}
			}
			if len(f.batches) != 2 || f.batches[0] != 3 {
				t.Fatalf("lotes %v (se esperaban 2 de 3)", f.batches)
			}
			tc.check(t, f, cfg)
			// Reintento ante 429 con Retry-After y ante 5xx.
			for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
				f.calls.Store(0)
				f.failFirst, f.failStatus = 2, status
				slept = nil
				if _, err := e.Embed(context.Background(), []string{"x"}, KindQuery); err != nil {
					t.Fatalf("HTTP %d: debía reintentar: %v", status, err)
				}
				if len(slept) != 2 || slept[0] != time.Second {
					t.Fatalf("HTTP %d: esperas %v (Retry-After: 1)", status, slept)
				}
			}
			f.calls.Store(0)
			f.failFirst, f.failStatus = 100, 0
			_, err = e.Embed(context.Background(), []string{"x"}, KindQuery)
			if !errors.Is(err, ErrUnavailable) || !IsTransient(err) {
				t.Fatalf("error agotado: %v", err)
			}
			if cfg.APIKey != "" && strings.Contains(err.Error(), testKey) {
				t.Fatal("el error no debe contener la clave")
			}
			f.calls.Store(0)
			f.failFirst = 0
			// La dimensión de la respuesta se comprueba siempre.
			f.dims = tc.dims + 1
			if _, err := e.Embed(context.Background(), []string{"y"}, KindDocument); err == nil || !strings.Contains(err.Error(), "dimensiones") || IsTransient(err) {
				t.Fatalf("dimensión distinta: %v", err)
			}
		})
	}
}

func norm(v []float32) float64 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	return math.Sqrt(n)
}

func TestGeminiTaskTypes(t *testing.T) {
	f := &fakeAPI{dims: 768}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cfg, err := ConfigFrom(testEnv(map[string]string{EnvEmbedder: "gemini", EnvURL: srv.URL, "GEMINI_API_KEY": testKey}))
	if err != nil {
		t.Fatal(err)
	}
	e, _ := New(cfg)
	for kind, want := range map[Kind]string{KindDocument: "RETRIEVAL_DOCUMENT", KindQuery: "RETRIEVAL_QUERY"} {
		if _, err := e.Embed(context.Background(), []string{"a"}, kind); err != nil {
			t.Fatal(err)
		}
		if got := f.lastBody["requests"].([]any)[0].(map[string]any)["taskType"]; got != want {
			t.Fatalf("%s: taskType %v", kind, got)
		}
	}
}

func TestPermanentErrorsAreNotRetried(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"input too long"}}`))
	}))
	defer bad.Close()
	cfg, _ := ConfigFrom(testEnv(map[string]string{EnvEmbedder: "openai", EnvURL: bad.URL, "OPENAI_API_KEY": testKey}))
	e, _ := New(cfg)
	n := 0
	e.(*client).sleep = func(context.Context, time.Duration) error { n++; return nil }
	if _, err := e.Embed(context.Background(), []string{"a"}, KindDocument); err == nil || IsTransient(err) || n != 0 {
		t.Fatalf("un 400 no se reintenta: %v (esperas %d)", err, n)
	}
}

// TestModelTable valida la configuración contra la tabla de modelos.
func TestModelTable(t *testing.T) {
	ok := func(env map[string]string) Config {
		t.Helper()
		c, err := ConfigFrom(testEnv(env))
		if err != nil {
			t.Fatalf("%v: %v", env, err)
		}
		return c
	}
	bad := func(env map[string]string, want string) {
		t.Helper()
		if _, err := ConfigFrom(testEnv(env)); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%v: se esperaba error con %q, got %v", env, want, err)
		}
	}
	if c := ok(map[string]string{}); c.Enabled() {
		t.Fatal("sin variable: apagado")
	}
	bad(map[string]string{EnvEmbedder: "gemini"}, "falta la clave")
	bad(map[string]string{EnvEmbedder: "cohere"}, "no es válido")
	c := ok(map[string]string{EnvEmbedder: "gemini", "GEMINI_API_KEY": "k"})
	if c.Dims != 768 || c.Model != "gemini-embedding-001" || c.Retries != 5 || c.Timeout != 30*time.Second || c.Batch != 100 || !c.SendDims {
		t.Fatalf("gemini por defecto: %+v", c)
	}
	bad(map[string]string{EnvEmbedder: "gemini", "GEMINI_API_KEY": "k", EnvDims: "64"}, "admite de 128 a 3072")
	bad(map[string]string{EnvEmbedder: "gemini", "GEMINI_API_KEY": "k", EnvModel: "text-embedding-004", EnvDims: "1536"}, "admite de 1 a 768")
	bad(map[string]string{EnvEmbedder: "gemini", "GEMINI_API_KEY": "k", EnvDims: "5000"}, "entre 1 y 4000")
	c = ok(map[string]string{EnvEmbedder: "openai", "OPENAI_API_KEY": "k"})
	if c.Dims != 1536 || !c.SendDims || c.Normalize || c.Batch != 2048 || c.MaxChars < 20000 {
		t.Fatalf("openai por defecto: %+v", c)
	}
	bad(map[string]string{EnvEmbedder: "openai", "OPENAI_API_KEY": "k", EnvModel: "text-embedding-ada-002", EnvDims: "768"}, "dimensión fija 1536")
	c = ok(map[string]string{EnvEmbedder: "openai", "OPENAI_API_KEY": "k", EnvModel: "text-embedding-ada-002"})
	if c.Dims != 1536 || c.SendDims {
		t.Fatalf("ada-002: 1536 sin enviar dimensions: %+v", c)
	}
	bad(map[string]string{EnvEmbedder: "openai", "OPENAI_API_KEY": "k", EnvModel: "text-embedding-3-small", EnvDims: "3072"}, "admite de 1 a 1536")
	bad(map[string]string{EnvEmbedder: "openai", "OPENAI_API_KEY": "k", EnvModel: "modelo-nuevo"}, "declare NEXO_KB_EMBED_DIMS")
	c = ok(map[string]string{EnvEmbedder: "openai", "OPENAI_API_KEY": "k", EnvModel: "modelo-nuevo", EnvDims: "1024"})
	if c.SendDims || !c.Normalize {
		t.Fatalf("modelo desconocido: sin dimensions salvo NEXO_KB_EMBED_SEND_DIMS y normalizado: %+v", c)
	}
	bad(map[string]string{EnvEmbedder: "azure-openai", EnvAPIKey: "k", EnvURL: "https://x.openai.azure.com"}, EnvDeployment)
	c = ok(map[string]string{EnvEmbedder: "azure-openai", EnvAPIKey: "k", EnvURL: "https://x.openai.azure.com", EnvDeployment: "d", EnvModel: "text-embedding-3-large", EnvDims: "1536"})
	if c.AuthHeader != AuthAPIKey || c.APIVersion != DefaultAzureAPIVersion || c.Dims != 1536 || !c.SendDims {
		t.Fatalf("azure: %+v", c)
	}
	bad(map[string]string{EnvEmbedder: "openai-compatible", EnvURL: "http://x"}, EnvModel)
	bad(map[string]string{EnvEmbedder: "openai-compatible", EnvURL: "http://x", EnvModel: "bge-m3"}, "declare NEXO_KB_EMBED_DIMS")
	c = ok(map[string]string{EnvEmbedder: "openai-compatible", EnvURL: "http://x", EnvModel: "bge-m3", EnvDims: "1024", EnvSendDims: "1"})
	if !c.SendDims || c.AuthHeader != AuthNone {
		t.Fatalf("compatible: %+v", c)
	}
}

func TestRateLimitWindow(t *testing.T) {
	f := &fakeAPI{dims: 1536}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cfg, _ := ConfigFrom(testEnv(map[string]string{EnvEmbedder: "openai", EnvURL: srv.URL, "OPENAI_API_KEY": "k", EnvBatch: "1", EnvRPM: "2"}))
	e, _ := New(cfg)
	cl := e.(*client)
	now := time.Unix(1000, 0)
	cl.now = func() time.Time { return now }
	var waited time.Duration
	cl.sleep = func(_ context.Context, d time.Duration) error { waited += d; now = now.Add(d); return nil }
	if _, err := e.Embed(context.Background(), []string{"a", "b", "c"}, KindDocument); err != nil {
		t.Fatal(err)
	}
	if waited < time.Minute-time.Second {
		t.Fatalf("con RPM=2 la tercera llamada espera al minuto siguiente (esperó %v)", waited)
	}
}

// TestEmbedderLive llama a la API real del proveedor configurado, solo con
// NEXO_KB_EMBED_LIVE=1 (fuera del CI; NEXO_KB_EMBEDDER y su clave en el
// entorno, gemini por defecto): dos textos con la dimensión configurada,
// normalizados, y la pregunta parecida queda más cerca. Es la prueba de humo
// de OpenAI y Azure OpenAI cuando haya una clave.
func TestEmbedderLive(t *testing.T) {
	if os.Getenv("NEXO_KB_EMBED_LIVE") != "1" {
		t.Skip("NEXO_KB_EMBED_LIVE=1 no definido (prueba con la API real del proveedor)")
	}
	cfg, err := ConfigFrom(func(k string) string {
		if k == EnvEmbedder && os.Getenv(k) == "" {
			return "gemini"
		}
		return os.Getenv(k)
	})
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := e.Embed(context.Background(), []string{"Política de reintentos: tres intentos con espera exponencial en llamadas HTTP.", "Los nombres de tablas van en singular."}, KindDocument)
	if err != nil {
		t.Fatal(err)
	}
	q, err := e.Embed(context.Background(), []string{"cuántas veces se reintenta una llamada a otro servicio"}, KindQuery)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(norm(docs[0])-1) > 1e-4 {
		t.Fatalf("normalizado: %f", norm(docs[0]))
	}
	if cos(q[0], docs[0]) <= cos(q[0], docs[1]) {
		t.Fatalf("la pregunta debía quedar más cerca de la política de reintentos: %.3f <= %.3f", cos(q[0], docs[0]), cos(q[0], docs[1]))
	}
}

func cos(a, b []float32) float64 {
	var ab, aa, bb float64
	for i := range a {
		ab += float64(a[i]) * float64(b[i])
		aa += float64(a[i]) * float64(a[i])
		bb += float64(b[i]) * float64(b[i])
	}
	return ab / math.Sqrt(aa*bb)
}
