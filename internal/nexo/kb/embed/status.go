package embed

// Nexo IA · NX-10 (panel) — de dónde sale la clave del proveedor, para la
// pantalla "Estado de la búsqueda". Devuelve solo el nombre de la variable,
// nunca el valor.

import "strings"

// Origen de la clave.
const (
	KeyFromEnv  = "env"
	KeyFromFile = "file"
)

// KeyInfo dice si hay una clave y de dónde sale. No contiene la clave.
type KeyInfo struct {
	// Required: el proveedor la pide (Ollama o TEI locales no).
	Required   bool   `json:"required"`
	Configured bool   `json:"configured"`
	Source     string `json:"source,omitempty"`   // KeyFromEnv o KeyFromFile
	Variable   string `json:"variable,omitempty"` // nombre de la variable de entorno
}

// providerKeyVars son las variables de respaldo de cada proveedor, en el
// orden en que las lee ConfigFrom.
var providerKeyVars = map[string][]string{
	ProviderGemini:      {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
	ProviderOpenAI:      {"OPENAI_API_KEY"},
	ProviderAzureOpenAI: {"AZURE_OPENAI_API_KEY"},
}

// KeyStatus describe la clave de c. getenv es el entorno con que se armó c
// (os.Getenv en el servidor); sirve solo para saber qué variable la entregó.
func KeyStatus(c Config, getenv func(string) string) KeyInfo {
	info := KeyInfo{Required: c.Enabled() && c.AuthHeader != AuthNone}
	if c.APIKey == "" {
		return info
	}
	info.Configured = true
	if getenv == nil {
		return info
	}
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	switch {
	case get(EnvAPIKey) != "":
		info.Source, info.Variable = KeyFromEnv, EnvAPIKey
	case get(EnvAPIKeyFile) != "":
		info.Source, info.Variable = KeyFromFile, EnvAPIKeyFile
	default:
		for _, v := range providerKeyVars[c.Provider] {
			if get(v) != "" {
				info.Source, info.Variable = KeyFromEnv, v
				break
			}
		}
	}
	return info
}
