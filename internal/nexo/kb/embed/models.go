package embed

// Tabla de modelos conocidos (design §20.2): dimensión nativa, por defecto y
// rango admitido, si la dimensión se puede reducir (Matryoshka: OpenAI
// dimensions, Gemini outputDimensionality), lote máximo, tokens por texto y
// si los vectores llegan normalizados. La configuración se valida contra la
// tabla al arrancar; un modelo que no está en la tabla se admite con la
// dimensión declarada (NEXO_KB_EMBED_DIMS) y se comprueba en cada respuesta.

import (
	"fmt"
	"slices"
	"strings"
)

// ModelInfo describe un modelo de embeddings.
type ModelInfo struct {
	Provider    string `json:"provider"`
	Name        string `json:"name"`
	NativeDims  int    `json:"native_dims"`
	DefaultDims int    `json:"default_dims"`
	MinDims     int    `json:"min_dims"`
	MaxDims     int    `json:"max_dims"`
	// Reducible: acepta el parámetro de dimensión. Si no, la única dimensión
	// válida es la nativa y el parámetro no se envía.
	Reducible bool `json:"reducible"`
	// Recommended son las dimensiones que el proveedor recomienda.
	Recommended []int `json:"recommended,omitempty"`
	MaxBatch    int   `json:"max_batch"`
	MaxTokens   int   `json:"max_input_tokens"`
	// Normalized: "native" (unitarios solo a la dimensión nativa), "always" o
	// "never".
	Normalized string `json:"normalized"`
	Note       string `json:"note,omitempty"`
}

// MaxChars es el recorte por defecto del texto: unos 3,5 caracteres por
// token (español), con margen.
func (m ModelInfo) MaxChars() int {
	return max(200, m.MaxTokens*7/2*9/10)
}

// NormalizedAt informa si los vectores llegan unitarios a esa dimensión.
func (m ModelInfo) NormalizedAt(dims int) bool {
	switch m.Normalized {
	case "always":
		return true
	case "native":
		return dims == m.NativeDims
	}
	return false
}

// Validate comprueba una dimensión contra el modelo.
func (m ModelInfo) Validate(dims int) error {
	if !m.Reducible && dims != m.NativeDims {
		return fmt.Errorf("el modelo %s tiene dimensión fija %d y no admite otra", m.Name, m.NativeDims)
	}
	if dims < m.MinDims || dims > m.MaxDims {
		return fmt.Errorf("el modelo %s admite de %d a %d dimensiones (recomendadas: %v)", m.Name, m.MinDims, m.MaxDims, m.Recommended)
	}
	if dims > MaxDims {
		return fmt.Errorf("más de %d dimensiones no se pueden indexar con HNSW en pgvector", MaxDims)
	}
	return nil
}

// Models es la tabla de modelos conocidos.
var Models = []ModelInfo{
	{Provider: ProviderGemini, Name: "gemini-embedding-001", NativeDims: 3072, DefaultDims: 768, MinDims: 128, MaxDims: 3072, Reducible: true,
		Recommended: []int{768, 1536, 3072}, MaxBatch: 100, MaxTokens: 2048, Normalized: "native",
		Note: "multilingüe; por debajo de 3072 los vectores no llegan normalizados (Nexo los normaliza)"},
	{Provider: ProviderGemini, Name: "text-embedding-004", NativeDims: 768, DefaultDims: 768, MinDims: 1, MaxDims: 768, Reducible: true,
		Recommended: []int{768}, MaxBatch: 100, MaxTokens: 2048, Normalized: "native", Note: "modelo anterior de Google; preferir gemini-embedding-001"},
	{Provider: ProviderOpenAI, Name: "text-embedding-3-small", NativeDims: 1536, DefaultDims: 1536, MinDims: 1, MaxDims: 1536, Reducible: true,
		Recommended: []int{512, 768, 1536}, MaxBatch: 2048, MaxTokens: 8191, Normalized: "always"},
	{Provider: ProviderOpenAI, Name: "text-embedding-3-large", NativeDims: 3072, DefaultDims: 3072, MinDims: 1, MaxDims: 3072, Reducible: true,
		Recommended: []int{256, 1024, 1536, 3072}, MaxBatch: 2048, MaxTokens: 8191, Normalized: "always", Note: "3072 se indexa como halfvec"},
	{Provider: ProviderOpenAI, Name: "text-embedding-ada-002", NativeDims: 1536, DefaultDims: 1536, MinDims: 1536, MaxDims: 1536, Reducible: false,
		Recommended: []int{1536}, MaxBatch: 2048, MaxTokens: 8191, Normalized: "always", Note: "dimensión fija; no se envía dimensions"},
}

// LookupModel busca un modelo. Azure y los servidores compatibles usan la
// tabla de OpenAI cuando el modelo es uno de OpenAI (en Azure, el modelo
// detrás del despliegue).
func LookupModel(provider, model string) (ModelInfo, bool) {
	model = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "models/")
	p := provider
	if p == ProviderAzureOpenAI || p == ProviderOpenAICompatible {
		p = ProviderOpenAI
	}
	i := slices.IndexFunc(Models, func(m ModelInfo) bool { return m.Provider == p && m.Name == model })
	if i < 0 {
		return ModelInfo{}, false
	}
	return Models[i], true
}
