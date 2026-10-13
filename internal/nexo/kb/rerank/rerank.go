// Package rerank reordena los candidatos de una búsqueda con un modelo
// externo (NX-10, R3, RC-13, RC-21, RC-29): interfaz Reranker común a todos
// los adaptadores y adaptadores Cohere (v2, también en Azure AI), Jina,
// Voyage, TEI, "compatible con Cohere" y un LLM como reordenador (Gemini y la
// API de chat de OpenAI, Azure OpenAI o compatible). Cambiar de proveedor es
// configuración (NEXO_KB_RERANKER), nunca código de búsqueda; es el mismo
// diseño que embed para R2.
//
// Lo común se resuelve una sola vez en client (resilient.go): lotes,
// llamadas en paralelo con tope, reintentos con espera exponencial (respeta
// Retry-After), límites por minuto, tiempo máximo por llamada, validación de
// cada respuesta y borrado de la clave en cualquier mensaje de error. Un
// adaptador solo sabe armar una petición HTTP y leer su respuesta.
//
// El reordenador solo reordena lo que la búsqueda ya autorizó y filtró: nunca
// agrega resultados ni baja el umbral de "no hay estándar" (RC-21). Si falla,
// quien lo usa conserva el orden de la búsqueda (search/rerank.go).
//
// La clave nunca se imprime ni se guarda: Config la marca con json:"-",
// Describe() no la incluye y los errores la reemplazan por "[clave]".
package rerank

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Passage es un pasaje a puntuar: el texto es el título del documento, la ruta
// de títulos y el cuerpo de la sección (la misma composición que se vectoriza,
// store.VectorText), y el reordenador lo recorta al límite del modelo. ID es
// un identificador opaco de quien llama (no se envía al proveedor).
type Passage struct {
	ID   string
	Text string
}

// Score es el puntaje de un pasaje, normalizado a [0, 1] (1 = responde por
// completo). OK es falso si el proveedor no entregó puntaje para ese pasaje
// (lote fallido).
type Score struct {
	Value float64
	OK    bool
}

// Reranker puntúa pasajes frente a una consulta. Devuelve un Score por pasaje,
// en el mismo orden. Con un error, los puntajes que sí llegaron vienen con OK
// verdadero: quien llama decide si usa un resultado parcial.
type Reranker interface {
	// Name es el proveedor ("cohere", "llm-gemini", ...).
	Name() string
	Model() string
	Rerank(ctx context.Context, query string, passages []Passage) ([]Score, error)
}

// Proveedores.
const (
	ProviderOff        = "off"
	ProviderCohere     = "cohere"
	ProviderJina       = "jina"
	ProviderVoyage     = "voyage"
	ProviderTEI        = "tei"
	ProviderCompatible = "rerank-compatible"
	ProviderLLM        = "llm"
)

// APIs de chat del reordenador LLM (NEXO_KB_RERANK_LLM).
const (
	APIGemini           = "gemini"
	APIOpenAI           = "openai"
	APIAzureOpenAI      = "azure-openai"
	APIOpenAICompatible = "openai-compatible"
)

// Variables de entorno.
const (
	EnvReranker    = "NEXO_KB_RERANKER"
	EnvLLM         = "NEXO_KB_RERANK_LLM"
	EnvModel       = "NEXO_KB_RERANK_MODEL"
	EnvURL         = "NEXO_KB_RERANK_URL"
	EnvAPIKey      = "NEXO_KB_RERANK_API_KEY"
	EnvAPIKeyFile  = "NEXO_KB_RERANK_API_KEY_FILE"
	EnvAuth        = "NEXO_KB_RERANK_AUTH"
	EnvDeployment  = "NEXO_KB_RERANK_DEPLOYMENT"
	EnvAPIVersion  = "NEXO_KB_RERANK_API_VERSION"
	EnvBatch       = "NEXO_KB_RERANK_BATCH"
	EnvConcurrency = "NEXO_KB_RERANK_CONCURRENCY"
	EnvRPM         = "NEXO_KB_RERANK_RPM"
	EnvTimeout     = "NEXO_KB_RERANK_CALL_TIMEOUT_SECONDS"
	EnvRetries     = "NEXO_KB_RERANK_RETRIES"
	EnvMaxChars    = "NEXO_KB_RERANK_MAX_CHARS"
	EnvThinking    = "NEXO_KB_RERANK_THINKING"
	EnvTemperature = "NEXO_KB_RERANK_TEMPERATURE"
)

// Autenticación de los adaptadores que la admiten.
const (
	AuthBearer = "bearer"
	AuthAPIKey = "api-key"
	AuthNone   = "none"
)

// DefaultAzureAPIVersion es la versión de la API de Azure OpenAI por defecto.
const DefaultAzureAPIVersion = "2024-10-21"

// DefaultGeminiModel es el modelo Gemini por defecto del reordenador LLM
// (medido en R3, design §21).
const DefaultGeminiModel = "gemini-2.5-flash"

// Config es la configuración de un reordenador.
type Config struct {
	Provider string `json:"provider"`
	// API: solo con Provider = llm (gemini, openai, azure-openai,
	// openai-compatible).
	API   string `json:"api,omitempty"`
	Model string `json:"model"`
	// BaseURL: raíz de la API (Cohere https://api.cohere.com o la URL de un
	// despliegue en Azure AI; si ya termina en /rerank se usa tal cual).
	BaseURL    string `json:"base_url,omitempty"`
	Deployment string `json:"deployment,omitempty"`
	APIVersion string `json:"api_version,omitempty"`
	APIKey     string `json:"-"`
	// AuthHeader: bearer (Authorization: Bearer), api-key (encabezado api-key
	// de Azure) o none.
	AuthHeader string `json:"auth,omitempty"`
	// Batch es el máximo de pasajes por llamada.
	Batch int `json:"batch"`
	// Concurrency es el máximo de llamadas en paralelo (lotes de una misma
	// consulta).
	Concurrency int `json:"concurrency"`
	// RPM limita las llamadas por minuto. 0 = sin límite propio.
	RPM int `json:"rpm,omitempty"`
	// Timeout es el tiempo máximo de cada llamada HTTP.
	Timeout time.Duration `json:"timeout"`
	// Retries es la cantidad de reintentos ante un error transitorio.
	Retries int `json:"retries"`
	// MaxChars recorta cada pasaje (según el modelo).
	MaxChars int `json:"max_chars"`
	// Thinking (solo Gemini): -1 = no enviar (valor del modelo), 0 = sin
	// razonamiento, N = presupuesto de tokens.
	Thinking int `json:"thinking"`
	// NoTemperature omite la temperatura 0 (modelos que no la aceptan).
	NoTemperature bool `json:"no_temperature,omitempty"`
}

// Enabled informa si hay un reordenador configurado.
func (c Config) Enabled() bool { return c.Provider != "" && c.Provider != ProviderOff }

// ID es el nombre del proveedor como lo informan las respuestas.
func (c Config) ID() string {
	if c.Provider == ProviderLLM {
		return "llm-" + c.API
	}
	return c.Provider
}

// Describe es la configuración sin la clave (para las corridas de evaluación).
func (c Config) Describe() map[string]any {
	return map[string]any{"provider": c.ID(), "model": c.Model, "base_url": c.BaseURL, "deployment": c.Deployment, "batch": c.Batch,
		"concurrency": c.Concurrency, "max_chars": c.MaxChars, "thinking": c.Thinking, "call_timeout_seconds": c.Timeout.Seconds(), "retries": c.Retries, "rpm": c.RPM}
}

// ConfigFromEnv lee la configuración del entorno. NEXO_KB_RERANKER vacío u
// "off": desactivado (Config.Enabled() = false, sin error).
func ConfigFromEnv() (Config, error) { return ConfigFrom(os.Getenv) }

// ConfigFrom lee y valida la configuración con una función de entorno.
func ConfigFrom(getenv func(string) string) (Config, error) {
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	c := Config{Provider: strings.ToLower(get(EnvReranker))}
	switch c.Provider {
	case "", ProviderOff, "none", "0", "false":
		return Config{Provider: ProviderOff}, nil
	case ProviderCohere, ProviderJina, ProviderVoyage, ProviderTEI, ProviderCompatible, ProviderLLM:
	case "azure-cohere", "azure-ai":
		c.Provider = ProviderCohere
		if get(EnvAuth) == "" {
			c.AuthHeader = AuthAPIKey
		}
	case "compatible", "cohere-compatible":
		c.Provider = ProviderCompatible
	case APIGemini, "llm-gemini":
		c.Provider, c.API = ProviderLLM, APIGemini
	case APIOpenAI, "llm-openai":
		c.Provider, c.API = ProviderLLM, APIOpenAI
	case APIAzureOpenAI, "llm-azure-openai", "azure", "azure_openai":
		c.Provider, c.API = ProviderLLM, APIAzureOpenAI
	case APIOpenAICompatible, "llm-openai-compatible", "openai_compatible":
		c.Provider, c.API = ProviderLLM, APIOpenAICompatible
	default:
		return Config{}, fmt.Errorf("%s=%q no es válido (cohere, jina, voyage, tei, rerank-compatible, llm, gemini, openai, azure-openai, openai-compatible u off)", EnvReranker, c.Provider)
	}
	c.Model, c.BaseURL = get(EnvModel), strings.TrimRight(get(EnvURL), "/")
	c.APIKey = get(EnvAPIKey)
	if c.APIKey == "" {
		if f := get(EnvAPIKeyFile); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return Config{}, fmt.Errorf("%s: no se pudo leer el archivo de la clave", EnvAPIKeyFile)
			}
			c.APIKey = strings.TrimSpace(string(b))
		}
	}
	if a := strings.ToLower(get(EnvAuth)); a != "" {
		c.AuthHeader = a
	}
	if c.Provider == ProviderLLM && c.API == "" {
		c.API = strings.ToLower(orDefault(get(EnvLLM), APIGemini))
		switch c.API {
		case APIGemini, APIOpenAI, APIAzureOpenAI, APIOpenAICompatible:
		case "azure", "azure_openai":
			c.API = APIAzureOpenAI
		case "compatible", "openai_compatible":
			c.API = APIOpenAICompatible
		default:
			return Config{}, fmt.Errorf("%s=%q no es válido (gemini, openai, azure-openai u openai-compatible)", EnvLLM, c.API)
		}
	}
	// Valores por defecto de cada proveedor.
	batch, concurrency, maxChars, timeout, retries := 100, 1, 12000, 15, 2
	c.Thinking = -1
	switch {
	case c.Provider == ProviderCohere:
		c.Model = orDefault(c.Model, "rerank-v3.5")
		c.BaseURL = orDefault(c.BaseURL, "https://api.cohere.com")
		c.AuthHeader = orDefault(c.AuthHeader, AuthBearer)
		c.APIKey = orDefault(c.APIKey, firstEnv(get, "COHERE_API_KEY", "CO_API_KEY"))
	case c.Provider == ProviderJina:
		c.Model = orDefault(c.Model, "jina-reranker-v2-base-multilingual")
		c.BaseURL = orDefault(c.BaseURL, "https://api.jina.ai/v1")
		c.AuthHeader = orDefault(c.AuthHeader, AuthBearer)
		c.APIKey = orDefault(c.APIKey, firstEnv(get, "JINA_API_KEY"))
		maxChars = 3000
	case c.Provider == ProviderVoyage:
		c.Model = orDefault(c.Model, "rerank-2.5")
		c.BaseURL = orDefault(c.BaseURL, "https://api.voyageai.com/v1")
		c.AuthHeader = orDefault(c.AuthHeader, AuthBearer)
		c.APIKey = orDefault(c.APIKey, firstEnv(get, "VOYAGE_API_KEY"))
	case c.Provider == ProviderTEI:
		if c.BaseURL == "" {
			return Config{}, fmt.Errorf("%s=tei exige %s (por ejemplo http://tei:8080)", EnvReranker, EnvURL)
		}
		c.AuthHeader = orDefault(c.AuthHeader, AuthNone)
		if c.APIKey != "" && get(EnvAuth) == "" {
			c.AuthHeader = AuthBearer
		}
		batch, maxChars = 32, 2000
	case c.Provider == ProviderCompatible:
		if c.BaseURL == "" {
			return Config{}, fmt.Errorf("%s=rerank-compatible exige %s", EnvReranker, EnvURL)
		}
		c.AuthHeader = orDefault(c.AuthHeader, AuthBearer)
		if c.APIKey == "" && get(EnvAuth) == "" {
			c.AuthHeader = AuthNone
		}
	case c.API == APIGemini:
		c.Model = strings.TrimPrefix(orDefault(c.Model, DefaultGeminiModel), "models/")
		c.BaseURL = orDefault(c.BaseURL, "https://generativelanguage.googleapis.com/v1beta")
		c.APIKey = orDefault(c.APIKey, firstEnv(get, "GEMINI_API_KEY", "GOOGLE_API_KEY"))
		c.AuthHeader = ""
		batch, concurrency, maxChars, timeout = 25, 4, 1500, 30
		if strings.Contains(c.Model, "2.5") && !strings.Contains(c.Model, "pro") {
			c.Thinking = 0 // sin razonamiento: más rápido y barato para puntuar
		}
		if v := get(EnvThinking); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < -1 {
				return Config{}, fmt.Errorf("%s=%q: debe ser un entero (-1 = valor del modelo, 0 = sin razonamiento, N = tokens)", EnvThinking, v)
			}
			c.Thinking = n
		}
	case c.API == APIOpenAI:
		c.BaseURL = orDefault(c.BaseURL, "https://api.openai.com/v1")
		c.APIKey = orDefault(c.APIKey, firstEnv(get, "OPENAI_API_KEY"))
		c.AuthHeader = AuthBearer
		batch, concurrency, maxChars, timeout = 25, 4, 1500, 30
		if c.Model == "" {
			return Config{}, fmt.Errorf("%s=openai exige %s (el modelo de chat a usar como reordenador)", EnvReranker, EnvModel)
		}
	case c.API == APIAzureOpenAI:
		c.Deployment, c.APIVersion = get(EnvDeployment), orDefault(get(EnvAPIVersion), DefaultAzureAPIVersion)
		if c.BaseURL == "" || c.Deployment == "" {
			return Config{}, fmt.Errorf("%s=azure-openai exige %s (https://<recurso>.openai.azure.com) y %s (nombre del despliegue de chat)", EnvReranker, EnvURL, EnvDeployment)
		}
		c.Model = orDefault(c.Model, c.Deployment)
		c.APIKey = orDefault(c.APIKey, firstEnv(get, "AZURE_OPENAI_API_KEY"))
		switch c.AuthHeader {
		case "":
			c.AuthHeader = AuthAPIKey
		case AuthAPIKey, AuthBearer:
		default:
			return Config{}, fmt.Errorf("%s=%q no es válido para Azure (api-key o bearer)", EnvAuth, c.AuthHeader)
		}
		batch, concurrency, maxChars, timeout = 25, 4, 1500, 30
	case c.API == APIOpenAICompatible:
		if c.BaseURL == "" || c.Model == "" {
			return Config{}, fmt.Errorf("%s=openai-compatible exige %s y %s", EnvReranker, EnvURL, EnvModel)
		}
		if c.AuthHeader == "" {
			c.AuthHeader = AuthBearer
			if c.APIKey == "" {
				c.AuthHeader = AuthNone
			}
		}
		batch, concurrency, maxChars, timeout = 25, 2, 1500, 60
	}
	switch c.AuthHeader {
	case "", AuthBearer, AuthAPIKey, AuthNone:
	default:
		return Config{}, fmt.Errorf("%s=%q no es válido (bearer, api-key o none)", EnvAuth, c.AuthHeader)
	}
	if c.AuthHeader != AuthNone && c.APIKey == "" {
		return Config{}, fmt.Errorf("%s=%s: falta la clave (%s o %s)", EnvReranker, c.ID(), EnvAPIKey, EnvAPIKeyFile)
	}
	c.Batch = intEnv(get, EnvBatch, batch, 1, 1000)
	c.Concurrency = intEnv(get, EnvConcurrency, concurrency, 1, 32)
	c.RPM = intEnv(get, EnvRPM, 0, 0, 1_000_000)
	c.Timeout = time.Duration(intEnv(get, EnvTimeout, timeout, 1, 600)) * time.Second
	c.Retries = intEnv(get, EnvRetries, retries, 0, 20)
	c.MaxChars = intEnv(get, EnvMaxChars, maxChars, 200, 100_000)
	switch strings.ToLower(get(EnvTemperature)) {
	case "default", "omit", "none":
		c.NoTemperature = true
	}
	return c, nil
}

// New crea el Reranker de la configuración (con la capa común de lotes,
// reintentos y límites).
func New(c Config) (Reranker, error) {
	var a adapter
	switch c.Provider {
	case ProviderCohere, ProviderJina, ProviderVoyage, ProviderTEI, ProviderCompatible:
		a = &rerankAPI{cfg: c}
	case ProviderLLM:
		switch c.API {
		case APIGemini:
			a = &llmGemini{cfg: c}
		case APIOpenAI, APIAzureOpenAI, APIOpenAICompatible:
			a = &llmOpenAI{cfg: c}
		default:
			return nil, fmt.Errorf("rerank: API de chat %q no configurada", c.API)
		}
	default:
		return nil, fmt.Errorf("rerank: proveedor %q no configurado", c.Provider)
	}
	if c.Batch <= 0 {
		c.Batch = 25
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 1
	}
	return newClient(c, a), nil
}

// APIError es un error del proveedor.
type APIError struct {
	Provider   string
	Status     int
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("rerank %s: HTTP %d: %s", e.Provider, e.Status, e.Message)
}

// ErrUnavailable envuelve un error transitorio que agotó los reintentos (el
// proveedor no responde, límite de uso, error 5xx).
var ErrUnavailable = errors.New("proveedor de reordenamiento no disponible")

// ErrMalformed marca una respuesta que no se pudo interpretar (el modelo no
// devolvió el JSON pedido, faltan pasajes, puntajes fuera de rango). El lote
// queda sin puntaje; la búsqueda conserva su orden.
var ErrMalformed = errors.New("respuesta del reordenador no válida")

// IsTransient informa si el error es pasajero (reintentar más tarde).
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnavailable) {
		return true
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == 408 || ae.Status == 409 || ae.Status == 425 || ae.Status == 429 || ae.Status >= 500
	}
	return false
}

// Stats son los contadores acumulados de un reordenador (llamadas al
// proveedor, reintentos, respuestas no válidas, lotes fallidos y tokens que
// informa el proveedor), para medir costo.
type Stats struct {
	Calls        int64 `json:"calls"`
	Retries      int64 `json:"retries"`
	Malformed    int64 `json:"malformed"`
	FailedBatch  int64 `json:"failed_batches"`
	PromptTokens int64 `json:"prompt_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// StatsReporter lo implementan los reordenadores que cuentan su uso.
type StatsReporter interface{ Stats() Stats }

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func firstEnv(get func(string) string, keys ...string) string {
	for _, k := range keys {
		if v := get(k); v != "" {
			return v
		}
	}
	return ""
}

func intEnv(get func(string) string, key string, def, lo, hi int) int {
	if n, err := strconv.Atoi(get(key)); err == nil && n >= lo && n <= hi {
		return n
	}
	return def
}
