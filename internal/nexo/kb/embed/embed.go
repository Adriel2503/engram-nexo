// Package embed convierte textos en vectores con un proveedor externo
// (NX-10, R2, RC-12): interfaz Embedder común a todos los proveedores y
// adaptadores Gemini, OpenAI, Azure OpenAI y compatible con OpenAI (Ollama,
// vLLM, TEI y similares). Cambiar de proveedor es configuración
// (NEXO_KB_EMBEDDER), nunca código de búsqueda.
//
// Lo común se resuelve una sola vez en client (resilient.go): lotes,
// reintentos con espera exponencial, límites de uso por minuto, tiempo máximo
// por llamada, verificación de la cantidad y la dimensión de los vectores,
// normalización cuando el modelo no entrega vectores unitarios y borrado de
// la clave en cualquier mensaje de error. Un adaptador solo sabe armar una
// petición HTTP y leer su respuesta. La tabla de modelos (models.go) valida
// la configuración al arrancar.
//
// La clave nunca se imprime ni se guarda: Config la marca con json:"-",
// Describe() no la incluye y los errores la reemplazan por "[clave]".
package embed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Kind es el uso del texto: el proveedor puede optimizar el vector para
// documentos (lo que se guarda) o para consultas (la pregunta).
type Kind string

// Usos de un texto.
const (
	KindDocument Kind = "document"
	KindQuery    Kind = "query"
)

// Embedder convierte textos en vectores. Devuelve un vector por texto, en el
// mismo orden, todos de Dims() dimensiones.
type Embedder interface {
	Provider() string
	Model() string
	Dims() int
	Embed(ctx context.Context, texts []string, kind Kind) ([][]float32, error)
}

// Proveedores.
const (
	ProviderOff              = "off"
	ProviderGemini           = "gemini"
	ProviderOpenAI           = "openai"
	ProviderAzureOpenAI      = "azure-openai"
	ProviderOpenAICompatible = "openai-compatible"
)

// Límites de dimensión (design §20.2): el tipo vector de pgvector indexa con
// HNSW hasta 2000 dimensiones; de 2001 a 4000 se indexa como halfvec
// (media precisión, índice sobre embedding::halfvec(D)). Más de 4000 no se
// admite.
const (
	MaxDims       = 4000
	MaxVectorDims = 2000
)

// Variables de entorno.
const (
	EnvEmbedder    = "NEXO_KB_EMBEDDER"
	EnvModel       = "NEXO_KB_EMBED_MODEL"
	EnvDims        = "NEXO_KB_EMBED_DIMS"
	EnvURL         = "NEXO_KB_EMBED_URL"
	EnvAPIKey      = "NEXO_KB_EMBED_API_KEY"
	EnvAPIKeyFile  = "NEXO_KB_EMBED_API_KEY_FILE"
	EnvAuth        = "NEXO_KB_EMBED_AUTH"
	EnvDeployment  = "NEXO_KB_EMBED_DEPLOYMENT"
	EnvAPIVersion  = "NEXO_KB_EMBED_API_VERSION"
	EnvSendDims    = "NEXO_KB_EMBED_SEND_DIMS"
	EnvBatch       = "NEXO_KB_EMBED_BATCH"
	EnvRPM         = "NEXO_KB_EMBED_RPM"
	EnvTPM         = "NEXO_KB_EMBED_TPM"
	EnvTimeout     = "NEXO_KB_EMBED_TIMEOUT_SECONDS"
	EnvRetries     = "NEXO_KB_EMBED_RETRIES"
	EnvMaxChars    = "NEXO_KB_EMBED_MAX_CHARS"
	EnvQueryPrefix = "NEXO_KB_EMBED_QUERY_PREFIX"
	EnvDocPrefix   = "NEXO_KB_EMBED_DOC_PREFIX"
)

// Autenticación de los adaptadores OpenAI, Azure y compatible.
const (
	AuthBearer = "bearer"
	AuthAPIKey = "api-key"
	AuthNone   = "none"
)

// DefaultAzureAPIVersion es la versión de la API de Azure OpenAI por defecto
// (GA con el parámetro dimensions).
const DefaultAzureAPIVersion = "2024-10-21"

// Config es la configuración de un proveedor.
type Config struct {
	Provider string `json:"provider"`
	// Model es el modelo (en Azure, el modelo detrás del despliegue: define
	// las capacidades; el despliegue va en Deployment).
	Model string `json:"model"`
	Dims  int    `json:"dims"`
	// BaseURL: raíz de la API (Gemini .../v1beta, OpenAI .../v1, Azure
	// https://<recurso>.openai.azure.com, compatible la URL que expone
	// /embeddings, con consulta si hace falta).
	BaseURL    string `json:"base_url,omitempty"`
	Deployment string `json:"deployment,omitempty"`
	APIVersion string `json:"api_version,omitempty"`
	APIKey     string `json:"-"`
	// AuthHeader: bearer (Authorization: Bearer), api-key (encabezado de
	// Azure) o none (Ollama, TEI locales).
	AuthHeader string `json:"auth,omitempty"`
	// Batch es el máximo de textos por llamada (se acota al del modelo).
	Batch int `json:"batch"`
	// RPM y TPM limitan las llamadas y los tokens estimados (caracteres/4)
	// por minuto. 0 = sin límite propio (el proveedor responde 429 y se
	// reintenta).
	RPM int `json:"rpm,omitempty"`
	TPM int `json:"tpm,omitempty"`
	// Timeout es el tiempo máximo de cada llamada HTTP.
	Timeout time.Duration `json:"timeout"`
	// Retries es la cantidad de reintentos ante un error transitorio.
	Retries int `json:"retries"`
	// MaxChars recorta cada texto (por defecto, según el máximo de tokens del
	// modelo).
	MaxChars int `json:"max_chars"`
	// Prefijos para modelos que los esperan (multilingual-e5: "query: " y
	// "passage: "). Gemini usa taskType y no los necesita.
	QueryPrefix string `json:"query_prefix,omitempty"`
	DocPrefix   string `json:"doc_prefix,omitempty"`
	// SendDims envía la dimensión pedida (OpenAI dimensions, Gemini
	// outputDimensionality). Lo decide la tabla de modelos.
	SendDims bool `json:"send_dims"`
	// Normalize normaliza los vectores devueltos (modelos que no los
	// entregan unitarios a la dimensión pedida).
	Normalize bool `json:"normalize"`
}

// Enabled informa si hay un proveedor configurado.
func (c Config) Enabled() bool { return c.Provider != "" && c.Provider != ProviderOff }

// Describe es la configuración sin la clave (para nexo_kb_index_versions y
// las corridas de evaluación).
func (c Config) Describe() map[string]any {
	return map[string]any{"provider": c.Provider, "model": c.Model, "dims": c.Dims, "base_url": c.BaseURL, "deployment": c.Deployment,
		"query_prefix": c.QueryPrefix, "doc_prefix": c.DocPrefix, "max_chars": c.MaxChars, "storage": StorageType(c.Dims)}
}

// StorageType es el tipo de pgvector con que se indexa una dimensión.
func StorageType(dims int) string {
	if dims > MaxVectorDims {
		return "halfvec"
	}
	return "vector"
}

// ConfigFromEnv lee la configuración del entorno. NEXO_KB_EMBEDDER vacío u
// "off": desactivado (Config.Enabled() = false, sin error).
func ConfigFromEnv() (Config, error) {
	return ConfigFrom(os.Getenv)
}

// ConfigFrom lee y valida la configuración con una función de entorno.
func ConfigFrom(getenv func(string) string) (Config, error) {
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	c := Config{Provider: strings.ToLower(get(EnvEmbedder))}
	switch c.Provider {
	case "", ProviderOff, "none", "0", "false":
		return Config{Provider: ProviderOff}, nil
	case ProviderGemini, ProviderOpenAI, ProviderAzureOpenAI, ProviderOpenAICompatible:
	case "azure", "azure_openai":
		c.Provider = ProviderAzureOpenAI
	case "compatible", "openai_compatible":
		c.Provider = ProviderOpenAICompatible
	default:
		return Config{}, fmt.Errorf("%s=%q no es válido (gemini, openai, azure-openai, openai-compatible u off)", EnvEmbedder, c.Provider)
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
	c.AuthHeader = strings.ToLower(get(EnvAuth))
	dimsSet := false
	if v := get(EnvDims); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > MaxDims {
			return Config{}, fmt.Errorf("%s=%q: debe ser un entero entre 1 y %d (hasta %d con vector, de %d a %d con halfvec)", EnvDims, v, MaxDims, MaxVectorDims, MaxVectorDims+1, MaxDims)
		}
		c.Dims, dimsSet = n, true
	}
	switch c.Provider {
	case ProviderGemini:
		c.Model = strings.TrimPrefix(orDefault(c.Model, "gemini-embedding-001"), "models/")
		c.BaseURL = orDefault(c.BaseURL, "https://generativelanguage.googleapis.com/v1beta")
		if c.APIKey == "" {
			c.APIKey = firstEnv(get, "GEMINI_API_KEY", "GOOGLE_API_KEY")
		}
		c.AuthHeader = ""
	case ProviderOpenAI:
		c.Model = orDefault(c.Model, "text-embedding-3-small")
		c.BaseURL = orDefault(c.BaseURL, "https://api.openai.com/v1")
		if c.APIKey == "" {
			c.APIKey = firstEnv(get, "OPENAI_API_KEY")
		}
		c.AuthHeader = AuthBearer
	case ProviderAzureOpenAI:
		c.Deployment, c.APIVersion = get(EnvDeployment), orDefault(get(EnvAPIVersion), DefaultAzureAPIVersion)
		if c.BaseURL == "" || c.Deployment == "" {
			return Config{}, fmt.Errorf("%s=azure-openai exige %s (https://<recurso>.openai.azure.com) y %s (nombre del despliegue)", EnvEmbedder, EnvURL, EnvDeployment)
		}
		c.Model = orDefault(c.Model, "text-embedding-3-small")
		if c.APIKey == "" {
			c.APIKey = firstEnv(get, "AZURE_OPENAI_API_KEY")
		}
		switch c.AuthHeader {
		case "":
			c.AuthHeader = AuthAPIKey
		case AuthAPIKey, AuthBearer: // bearer: token de Entra ID en la clave
		default:
			return Config{}, fmt.Errorf("%s=%q no es válido para Azure (api-key o bearer)", EnvAuth, c.AuthHeader)
		}
	case ProviderOpenAICompatible:
		if c.BaseURL == "" || c.Model == "" {
			return Config{}, fmt.Errorf("%s=openai-compatible exige %s y %s", EnvEmbedder, EnvURL, EnvModel)
		}
		switch c.AuthHeader {
		case "":
			c.AuthHeader = AuthBearer
			if c.APIKey == "" {
				c.AuthHeader = AuthNone
			}
		case AuthBearer, AuthAPIKey, AuthNone:
		default:
			return Config{}, fmt.Errorf("%s=%q no es válido (bearer, api-key o none)", EnvAuth, c.AuthHeader)
		}
		c.QueryPrefix, c.DocPrefix = getenv(EnvQueryPrefix), getenv(EnvDocPrefix)
	}
	if c.AuthHeader != AuthNone && c.APIKey == "" {
		return Config{}, fmt.Errorf("%s=%s: falta la clave (%s o %s)", EnvEmbedder, c.Provider, EnvAPIKey, EnvAPIKeyFile)
	}
	// Dimensión, envío del parámetro, lote y largo según la tabla de modelos.
	m, known := LookupModel(c.Provider, c.Model)
	maxBatch, maxChars := 32, 7000
	if known {
		if !dimsSet {
			c.Dims = m.DefaultDims
		}
		if err := m.Validate(c.Dims); err != nil {
			return Config{}, fmt.Errorf("%s=%d: %w", EnvDims, c.Dims, err)
		}
		c.SendDims = m.Reducible
		c.Normalize = !m.NormalizedAt(c.Dims)
		maxBatch, maxChars = m.MaxBatch, m.MaxChars()
	} else {
		// Modelo desconocido (servidor compatible, despliegue propio): la
		// dimensión se declara y se comprueba en cada respuesta.
		if !dimsSet {
			return Config{}, fmt.Errorf("el modelo %q no está en la tabla de modelos conocidos de %s: declare %s (se comprueba con la primera respuesta)", c.Model, c.Provider, EnvDims)
		}
		c.SendDims = isTrue(get(EnvSendDims))
		c.Normalize = true
		if c.Provider == ProviderOpenAI || c.Provider == ProviderAzureOpenAI {
			maxBatch = 2048
		}
		if c.Provider == ProviderGemini {
			maxBatch = 100
		}
	}
	c.Batch = intEnv(get, EnvBatch, maxBatch, 1, maxBatch)
	c.RPM = intEnv(get, EnvRPM, 0, 0, 1_000_000)
	c.TPM = intEnv(get, EnvTPM, 0, 0, 100_000_000)
	c.Timeout = time.Duration(intEnv(get, EnvTimeout, 30, 1, 600)) * time.Second
	c.Retries = intEnv(get, EnvRetries, 5, 0, 20)
	c.MaxChars = intEnv(get, EnvMaxChars, maxChars, 200, 100_000)
	return c, nil
}

// New crea el Embedder de la configuración (con la capa común de lotes,
// reintentos y límites).
func New(c Config) (Embedder, error) {
	var a adapter
	switch c.Provider {
	case ProviderGemini:
		a = &gemini{cfg: c}
	case ProviderOpenAI, ProviderAzureOpenAI, ProviderOpenAICompatible:
		a = &openAI{cfg: c}
	default:
		return nil, fmt.Errorf("embed: proveedor %q no configurado", c.Provider)
	}
	if c.Dims <= 0 || c.Dims > MaxDims {
		return nil, fmt.Errorf("embed: dimensión %d fuera de rango (1..%d)", c.Dims, MaxDims)
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
	return fmt.Sprintf("embed %s: HTTP %d: %s", e.Provider, e.Status, e.Message)
}

// ErrUnavailable envuelve un error transitorio que agotó los reintentos (el
// proveedor no responde, límite de uso, error 5xx). El indexador libera el
// trabajo sin contarlo como fallo; la búsqueda responde solo por texto.
var ErrUnavailable = errors.New("proveedor de embeddings no disponible")

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

func isTrue(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "1" || v == "true" || v == "yes" || v == "si" || v == "sí"
}
