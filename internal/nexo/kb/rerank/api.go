package rerank

// Adaptadores de las APIs de reordenamiento "consulta + documentos ->
// índice + puntaje": Cohere (v2, también el despliegue de Azure AI), Jina,
// Voyage, TEI (Hugging Face Text Embeddings Inference, por ejemplo con
// bge-reranker-v2-m3 local) y "compatible" (la forma de Cohere v2 en una URL
// propia). Un solo adaptador con cuatro dialectos: cambia la ruta, el cuerpo
// y el sobre de la respuesta; el resto (autenticación, errores, validación)
// es común.
//
//	cohere / compatible  POST {base}/v2/rerank   {model, query, documents, top_n}        -> {results:[{index, relevance_score}]}
//	jina                 POST {base}/rerank      {model, query, documents, top_n}        -> {results:[{index, relevance_score}]}
//	voyage               POST {base}/rerank      {model, query, documents, top_k}        -> {data:[{index, relevance_score}]}
//	tei                  POST {base}/rerank      {query, texts, truncate}                -> [{index, score}]
//
// Si la URL base ya termina en /rerank se usa tal cual (despliegues de Azure
// AI y proxys con otra ruta). La clave va en un encabezado (Authorization:
// Bearer o api-key), nunca en la URL.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
)

type rerankAPI struct{ cfg Config }

func (a *rerankAPI) endpoint() string {
	base := strings.TrimRight(a.cfg.BaseURL, "/")
	if strings.HasSuffix(base, "/rerank") {
		return base
	}
	switch a.cfg.Provider {
	case ProviderCohere, ProviderCompatible:
		if strings.HasSuffix(base, "/v2") {
			return base + "/rerank"
		}
		return base + "/v2/rerank"
	}
	return base + "/rerank"
}

func (a *rerankAPI) body(query string, texts []string) map[string]any {
	n := len(texts)
	switch a.cfg.Provider {
	case ProviderTEI:
		return map[string]any{"query": query, "texts": texts, "truncate": true}
	case ProviderVoyage:
		b := map[string]any{"query": query, "documents": texts, "top_k": n, "truncation": true}
		if a.cfg.Model != "" {
			b["model"] = a.cfg.Model
		}
		return b
	case ProviderJina:
		b := map[string]any{"query": query, "documents": texts, "top_n": n, "return_documents": false}
		if a.cfg.Model != "" {
			b["model"] = a.cfg.Model
		}
		return b
	}
	b := map[string]any{"query": query, "documents": texts, "top_n": n}
	if a.cfg.Model != "" {
		b["model"] = a.cfg.Model
	}
	return b
}

func (a *rerankAPI) call(ctx context.Context, hc *http.Client, query string, texts []string) (batchResult, error) {
	raw, err := json.Marshal(a.body(query, texts))
	if err != nil {
		return batchResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(), bytes.NewReader(raw))
	if err != nil {
		return batchResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	setAuth(req, a.cfg)
	resp, err := hc.Do(req)
	if err != nil {
		return batchResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return batchResult{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return batchResult{}, apiError(a.cfg.ID(), resp, data)
	}
	scores, err := parseRerankResponse(data, len(texts))
	return batchResult{scores: scores}, err
}

// setAuth pone la clave en el encabezado que corresponde.
func setAuth(req *http.Request, cfg Config) {
	switch cfg.AuthHeader {
	case AuthAPIKey:
		req.Header.Set("api-key", cfg.APIKey)
	case AuthNone:
	default:
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
}

// apiError arma el error con el mensaje del proveedor (varias formas:
// {"message"}, {"error":{"message"}}, {"error":"..."}, {"detail"}) y la espera
// sugerida por Retry-After.
func apiError(provider string, resp *http.Response, data []byte) error {
	e := &APIError{Provider: provider, Status: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	var g struct {
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &g) == nil {
		switch {
		case g.Message != "":
			e.Message = shortBody([]byte(g.Message))
		case len(g.Error) > 0:
			var inner struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			}
			var s string
			if json.Unmarshal(g.Error, &inner) == nil && inner.Message != "" {
				e.Message = shortBody([]byte(strings.TrimSpace(inner.Status + ": " + inner.Message)))
			} else if json.Unmarshal(g.Error, &s) == nil && s != "" {
				e.Message = shortBody([]byte(s))
			}
		case len(g.Detail) > 0:
			e.Message = shortBody(g.Detail)
		}
	}
	if e.Message == "" {
		e.Message = shortBody(data)
	}
	return e
}

type rerankItem struct {
	Index          *int     `json:"index"`
	RelevanceScore *float64 `json:"relevance_score"`
	Score          *float64 `json:"score"`
}

// parseRerankResponse lee {"results"|"data": [...]} o un arreglo y devuelve un
// puntaje por pasaje, por índice (la API puede devolver otro orden y, con
// top_n menor, menos elementos: eso es una respuesta no válida).
func parseRerankResponse(data []byte, n int) ([]float64, error) {
	var items []rerankItem
	var obj struct {
		Results []rerankItem `json:"results"`
		Data    []rerankItem `json:"data"`
	}
	trimmed := bytes.TrimSpace(data)
	switch {
	case len(trimmed) > 0 && trimmed[0] == '[':
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrMalformed, shortBody(data))
		}
	default:
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrMalformed, shortBody(data))
		}
		items = obj.Results
		if len(items) == 0 {
			items = obj.Data
		}
	}
	scores := make([]float64, n)
	seen := make([]bool, n)
	for _, it := range items {
		if it.Index == nil || *it.Index < 0 || *it.Index >= n {
			return nil, fmt.Errorf("%w: índice fuera de rango", ErrMalformed)
		}
		v := it.RelevanceScore
		if v == nil {
			v = it.Score
		}
		if v == nil {
			return nil, fmt.Errorf("%w: elemento sin puntaje", ErrMalformed)
		}
		if seen[*it.Index] {
			return nil, fmt.Errorf("%w: índice repetido", ErrMalformed)
		}
		seen[*it.Index] = true
		scores[*it.Index] = normalizeScore(*v)
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("%w: falta el pasaje %d", ErrMalformed, i)
		}
	}
	return scores, nil
}

// normalizeScore deja el puntaje en [0, 1]: lo que ya está en el rango se
// conserva; un logit se pasa por la sigmoide.
func normalizeScore(v float64) float64 {
	if math.IsNaN(v) || (v >= 0 && v <= 1) {
		return v
	}
	return 1 / (1 + math.Exp(-v))
}
