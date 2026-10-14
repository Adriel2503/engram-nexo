package stack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/embed"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/rerank"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/ui"
)

func (a *App) searchConfig(ctx context.Context, s *state.State, o *SearchOptions, m map[string]string) error {
	for {
		next, env := *s, maps.Clone(m)
		err := a.searchConfigOnce(ctx, &next, o, env)
		if err == nil {
			*s = next
			clear(m)
			maps.Copy(m, env)
			return nil
		}
		var failure *probeFailure
		if o.u == nil || !errors.As(err, &failure) || ctx.Err() != nil {
			return err
		}
		_, _ = fmt.Fprintln(a.Out, err)
		choice, e := o.u.Choose("¿Cómo quieres continuar?", []string{"Reintentar", "Seguir sin búsqueda por significado"})
		if e != nil {
			return e
		}
		if choice == "Reintentar" {
			o.retryKeys = true
			if e = a.askSearch(o.u, o); e != nil {
				return e
			}
		} else {
			o.Embedder, o.Reranker = "off", "off"
			o.KeyFile, o.RerankKeyFile, o.key, o.rerankKey = "", "", "", ""
		}
	}
}

func (a *App) searchConfigOnce(ctx context.Context, s *state.State, o *SearchOptions, m map[string]string) error {
	if previous := m[embed.EnvEmbedder]; previous != "" && previous != o.Embedder {
		delete(m, embed.EnvAPIKey)
	}
	if previous := m[rerank.EnvReranker]; previous != "" && previous != o.Reranker {
		delete(m, rerank.EnvAPIKey)
	}
	keys := map[string]string{"NEXO_KB_EMBEDDER": o.Embedder, "NEXO_KB_EMBED_MODEL": o.Model, "NEXO_KB_EMBED_DIMS": o.Dims, "NEXO_KB_EMBED_URL": o.URL, "NEXO_KB_EMBED_DEPLOYMENT": o.Deployment, "NEXO_KB_EMBED_AUTH": o.Auth, "NEXO_KB_RERANKER": o.Reranker, "NEXO_KB_RERANK_MODEL": o.RerankModel, "NEXO_KB_RERANK_URL": o.RerankURL, "NEXO_KB_RERANK_DEPLOYMENT": o.RerankDeployment, "NEXO_KB_RERANK_AUTH": o.RerankAuth, "NEXO_KB_R1": o.R1}
	for k, v := range keys {
		m[k] = v
	}
	for _, k := range []string{"NEXO_KB_EMBED_API_KEY", "NEXO_KB_RERANK_API_KEY", "GEMINI_API_KEY", "OPENAI_API_KEY", "AZURE_OPENAI_API_KEY", "COHERE_API_KEY", "JINA_API_KEY", "VOYAGE_API_KEY"} {
		if v := os.Getenv(k); v != "" {
			m[k] = v
		}
	}
	if o.KeyFile != "" {
		b, e := os.ReadFile(o.KeyFile)
		if e != nil {
			return errors.New("no se pudo leer --embed-key-file")
		}
		m[embed.EnvAPIKey] = strings.TrimSpace(string(b))
	}
	if o.RerankKeyFile != "" {
		b, e := os.ReadFile(o.RerankKeyFile)
		if e != nil {
			return errors.New("no se pudo leer --rerank-key-file")
		}
		m[rerank.EnvAPIKey] = strings.TrimSpace(string(b))
	}
	if o.key != "" {
		m[embed.EnvAPIKey] = o.key
	}
	if o.rerankKey != "" {
		m[rerank.EnvAPIKey] = o.rerankKey
	}
	if o.Reranker == o.Embedder && m[rerank.EnvAPIKey] == "" {
		m[rerank.EnvAPIKey] = m[embed.EnvAPIKey]
	}
	if e := validateR1(o.R1); e != nil {
		return e
	}
	get := func(k string) string { return m[k] }
	ec, e := embed.ConfigFrom(get)
	if e != nil {
		return errors.New("configuración de búsqueda por significado inválida; revisa modelo, dimensión, URL y clave")
	}
	rc, e := rerank.ConfigFrom(get)
	if e != nil {
		return errors.New("configuración del reordenador inválida; revisa modelo, URL y clave")
	}
	if ec.Enabled() || rc.Enabled() {
		_, _ = fmt.Fprintln(a.Out, "Cada documento y cada consulta reordenada envía texto al proveedor configurado y puede generar costo. Cambiar modelo o dimensión crea un índice nuevo.")
	}
	if ec.Enabled() {
		if e = probeEmbed(ctx, ec, a.HTTP); e != nil {
			return e
		}
		m[embed.EnvAPIKey] = ec.APIKey
		m[embed.EnvModel] = ec.Model
		m[embed.EnvDims] = fmt.Sprint(ec.Dims)
	}
	if rc.Enabled() {
		if e = probeRerank(ctx, rc, a.HTTP); e != nil {
			return e
		}
		m[rerank.EnvAPIKey] = rc.APIKey
		m[rerank.EnvModel] = rc.Model
		m["NEXO_KB_RERANK_DEFAULT"] = "on"
	} else {
		m["NEXO_KB_RERANK_DEFAULT"] = "off"
	} // Guardar solo claves canónicas, nunca alias heredados.
	for _, k := range []string{"GEMINI_API_KEY", "OPENAI_API_KEY", "AZURE_OPENAI_API_KEY", "COHERE_API_KEY", "JINA_API_KEY", "VOYAGE_API_KEY"} {
		delete(m, k)
	}
	if !ec.Enabled() {
		delete(m, embed.EnvAPIKey)
	}
	if !rc.Enabled() {
		delete(m, rerank.EnvAPIKey)
	}
	s.Embedder = o.Embedder
	s.Reranker = o.Reranker
	s.R1 = o.R1
	return nil
}
func validateR1(v string) error {
	if v == "all" || v == "off" {
		return nil
	}
	allowed := map[string]bool{"doc_context": true, "split_tables": true, "synonyms": true, "threshold": true, "diversity": true, "quote": true}
	for _, k := range strings.Split(v, ",") {
		if !allowed[strings.TrimSpace(k)] {
			return errors.New("--r1 admite all, off o doc_context,split_tables,synonyms,threshold,diversity,quote")
		}
	}
	return nil
}

// probeProxy permite reutilizar exactamente los adaptadores Nexo sin cambiar sus APIs.
// Solo escucha en loopback durante la prueba mínima y redirige con el cliente HTTP inyectado.
func probeProxy(base string, hc *http.Client, status *atomic.Int32) (string, func(), error) {
	up, e := url.Parse(base)
	if e != nil || up.Host == "" || up.User != nil || up.RawQuery != "" || up.Fragment != "" {
		return "", nil, errors.New("URL de proveedor inválida; no incluyas credenciales")
	}
	if up.Scheme != "https" {
		ip := net.ParseIP(up.Hostname())
		if up.Scheme != "http" || up.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", nil, errors.New("el proveedor externo exige HTTPS; HTTP solo se admite en loopback")
		}
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return "", nil, e
	}
	copyClient := *hc
	copyClient.Timeout = 20 * time.Second
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := r.Clone(r.Context())
		req.RequestURI = ""
		req.URL.Scheme = up.Scheme
		req.URL.Host = up.Host
		req.Host = up.Host
		resp, e := copyClient.Do(req)
		if e != nil {
			status.Store(http.StatusBadGateway)
			http.Error(w, "proveedor no disponible", http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		status.Store(int32(resp.StatusCode))
		// Gemini también rechaza claves con HTTP 400. Solo usamos códigos
		// estructurados conocidos; el mensaje libre nunca llega al diagnóstico.
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			status.Store(http.StatusBadGateway)
			http.Error(w, "respuesta incompleta del proveedor", http.StatusBadGateway)
			return
		}
		if resp.StatusCode == http.StatusBadRequest {
			var failure struct {
				Error struct {
					Details []struct {
						Reason string `json:"reason"`
					} `json:"details"`
				} `json:"error"`
			}
			if json.Unmarshal(body, &failure) == nil {
				for _, detail := range failure.Error.Details {
					if detail.Reason == "API_KEY_INVALID" || detail.Reason == "API_KEY_EXPIRED" {
						status.Store(http.StatusUnauthorized)
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
	})}
	go func() { _ = srv.Serve(l) }()
	return "http://" + l.Addr().String() + up.Path, func() { _ = srv.Close() }, nil
}
func probeEmbed(ctx context.Context, c embed.Config, hc *http.Client) error {
	var status atomic.Int32
	base, close, e := probeProxy(c.BaseURL, hc, &status)
	if e != nil {
		return e
	}
	defer close()
	c.BaseURL = base
	c.Retries = 0
	c.Timeout = 20 * time.Second
	x, e := embed.New(c)
	if e == nil {
		_, e = x.Embed(ctx, []string{"Prueba sintética de conexión"}, embed.KindQuery)
	}
	if e != nil {
		return newProbeFailure("embeddings", status.Load())
	}
	return nil
}
func probeRerank(ctx context.Context, c rerank.Config, hc *http.Client) error {
	var status atomic.Int32
	base, close, e := probeProxy(c.BaseURL, hc, &status)
	if e != nil {
		return e
	}
	defer close()
	c.BaseURL = base
	c.Retries = 0
	c.Timeout = 20 * time.Second
	x, e := rerank.New(c)
	if e == nil {
		_, e = x.Rerank(ctx, "prueba", []rerank.Passage{{ID: "1", Text: "Prueba sintética"}, {ID: "2", Text: "Otra frase sintética"}})
	}
	if e != nil {
		return newProbeFailure("reordenador", status.Load())
	}
	return nil
}

// No incorpora texto del proveedor: puede contener la clave, incluso codificada.
type probeFailure struct{ message string }

func (e *probeFailure) Error() string { return e.message }
func newProbeFailure(provider string, status int32) error {
	cause := "respuesta inválida del proveedor; revisa modelo y dimensiones"
	switch {
	case status == 401 || status == 403:
		cause = "clave rechazada o sin permisos; revisa la clave y el acceso"
	case status == 404:
		cause = "modelo no encontrado; revisa modelo, URL y despliegue"
	case status == 429:
		cause = "cuota o límite de solicitudes; revisa el saldo o espera antes de reintentar"
	case status == 402:
		cause = "cuota o saldo agotado; revisa la facturación"
	case status == 400:
		cause = "solicitud rechazada; revisa clave, modelo y parámetros"
	case status == 0 || status >= 500:
		cause = "red o proveedor no disponible; revisa la conexión y reintenta"
	}
	return &probeFailure{message: fmt.Sprintf("No se validó %s: %s. No se guardó la configuración.", provider, cause)}
}
func (a *App) askSearch(u *ui.UI, o *SearchOptions) error {
	o.u = u
	previousEmbedder, previousReranker := o.Embedder, o.Reranker
	yes, e := u.Confirm("¿Quieres activar la búsqueda por significado? Mejora las preguntas parafraseadas y requiere un proveedor", o.Embedder != "off")
	if e != nil {
		return e
	}
	if yes {
		o.Embedder, e = u.Choose("Proveedor de embeddings", []string{"gemini", "openai", "azure-openai", "openai-compatible"})
		if e != nil {
			return e
		}
		if o.Embedder != previousEmbedder {
			o.key, o.KeyFile, o.URL, o.Deployment, o.Auth = "", "", "", "", ""
		}
		model, dims := "gemini-embedding-001", "768"
		if o.Embedder != "gemini" {
			model, dims = "text-embedding-3-small", "1536"
		}
		o.Model, e = u.Ask("Modelo", model, false)
		if e != nil {
			return e
		}
		o.Dims, e = u.Ask("Dimensión", dims, false)
		if e != nil {
			return e
		}
		if o.Embedder == "azure-openai" || o.Embedder == "openai-compatible" {
			o.URL, e = u.Ask("URL del proveedor", "", false)
			if e != nil {
				return e
			}
		}
		if o.Embedder == "azure-openai" {
			o.Deployment, e = u.Ask("Despliegue Azure", "", false)
			if e != nil {
				return e
			}
		}
		if o.retryKeys || !hasProviderKey(o.Embedder, embed.EnvAPIKey) && o.KeyFile == "" {
			label := "Clave del proveedor (entrada oculta)"
			if o.retryKeys {
				label += "; vacío conserva la clave anterior"
			}
			key, e := u.Ask(label, "", true)
			if e != nil {
				return e
			}
			if key != "" {
				o.key, o.KeyFile = key, ""
			}
		}
	} else {
		o.Embedder = "off"
	}
	yes, e = u.Confirm("¿Quieres activar el reordenador? Envía la pregunta y los mejores pasajes al proveedor y puede generar costo", o.Reranker != "off")
	if e != nil {
		return e
	}
	if yes {
		o.Reranker, e = u.Choose("Proveedor del reordenador", []string{"cohere", "jina", "voyage", "gemini", "openai", "azure-openai", "openai-compatible", "tei", "rerank-compatible"})
		if e != nil {
			return e
		}
		if o.Reranker != previousReranker {
			o.rerankKey, o.RerankKeyFile, o.RerankURL, o.RerankDeployment, o.RerankAuth = "", "", "", "", ""
		}
		def := ""
		switch o.Reranker {
		case "gemini":
			def = rerank.DefaultGeminiModel
		case "cohere":
			def = "rerank-v3.5"
		case "jina":
			def = "jina-reranker-v2-base-multilingual"
		case "voyage":
			def = "rerank-2.5"
		}
		o.RerankModel, e = u.Ask("Modelo del reordenador", def, false)
		if e != nil {
			return e
		}
		if o.Reranker == "azure-openai" || o.Reranker == "openai-compatible" || o.Reranker == "tei" || o.Reranker == "rerank-compatible" {
			o.RerankURL, e = u.Ask("URL del reordenador", "", false)
			if e != nil {
				return e
			}
		}
		if o.Reranker == "azure-openai" {
			o.RerankDeployment, e = u.Ask("Despliegue de chat Azure", "", false)
			if e != nil {
				return e
			}
		}
		if o.Reranker == o.Embedder && o.key != "" {
			o.rerankKey = o.key
		} else if o.Reranker != "tei" && (o.retryKeys || !hasProviderKey(o.Reranker, rerank.EnvAPIKey) && o.RerankKeyFile == "") {
			label := "Clave del reordenador (entrada oculta)"
			if o.retryKeys {
				label += "; vacío conserva la clave anterior"
			}
			key, err := u.Ask(label, "", true)
			e = err
			if e != nil {
				return e
			}
			if key != "" {
				o.rerankKey, o.RerankKeyFile = key, ""
			}
		}
	} else {
		o.Reranker = "off"
	}
	o.R1, e = u.Ask("Mejoras: all, off o lista de doc_context,split_tables,synonyms,threshold,diversity,quote", "all", false)
	return e
}
func hasProviderKey(p, canonical string) bool {
	if os.Getenv(canonical) != "" {
		return true
	}
	alias := map[string]string{"gemini": "GEMINI_API_KEY", "openai": "OPENAI_API_KEY", "azure-openai": "AZURE_OPENAI_API_KEY", "cohere": "COHERE_API_KEY", "jina": "JINA_API_KEY", "voyage": "VOYAGE_API_KEY"}
	return os.Getenv(alias[p]) != ""
}
func (a *App) config(ctx context.Context, s state.State, o Options) error {
	m, e := render.ReadEnv(s.Dir)
	if e != nil {
		return e
	}
	if a.Interactive && !o.Yes {
		if e = a.askSearch(ui.New(a.In, a.Out, o.Plain), &o.Search); e != nil {
			return e
		}
	}
	next := s
	if e = a.searchConfig(ctx, &next, &o.Search, m); e != nil {
		return e
	}
	if e = render.Files(render.Data{State: next, Env: m, TwoAccounts: s.Mode != "demo"}); e != nil {
		return e
	}
	if e = state.Save(next); e != nil {
		return e
	}
	if s.Mode == "prod" && !o.Up {
		_, _ = fmt.Fprintln(a.Out, "Configuración guardada. Usa stack up --mode prod --up para aplicarla.")
		return nil
	}
	if e = a.down(ctx, s, false); e != nil {
		return e
	}
	o.Mode, o.Name, o.Dir = s.Mode, s.Name, s.Dir
	o.NoData = true
	o.Yes = true
	return a.upUnlocked(ctx, o)
}
