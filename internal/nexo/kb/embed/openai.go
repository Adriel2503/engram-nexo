package embed

// Adaptadores OpenAI, Azure OpenAI y compatible con OpenAI: POST
// {base}/embeddings con input en lote.
//
//   - OpenAI: {base}/embeddings, Authorization: Bearer, model y (si el modelo
//     lo admite) dimensions.
//   - Azure OpenAI: {recurso}/openai/deployments/{despliegue}/embeddings
//     ?api-version=..., encabezado api-key (o Bearer con un token de Entra
//     ID); el despliegue reemplaza al modelo en la ruta.
//   - Compatible (Ollama, vLLM, TEI, proxys): URL base con consulta si hace
//     falta y autenticación Bearer, api-key o ninguna.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type openAI struct{ cfg Config }

func (o *openAI) maxBatch() int { return 2048 }

func (o *openAI) endpoint() (string, error) {
	u, err := url.Parse(o.cfg.BaseURL)
	if err != nil {
		return "", err
	}
	if o.cfg.Provider == ProviderAzureOpenAI {
		u.Path = strings.TrimRight(u.Path, "/") + "/openai/deployments/" + url.PathEscape(o.cfg.Deployment) + "/embeddings"
		q := u.Query()
		q.Set("api-version", o.cfg.APIVersion)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/embeddings"
	return u.String(), nil
}

func (o *openAI) call(ctx context.Context, hc *http.Client, texts []string, _ Kind) ([][]float32, error) {
	body := map[string]any{"input": texts, "encoding_format": "float"}
	if o.cfg.Provider != ProviderAzureOpenAI {
		body["model"] = o.cfg.Model // en Azure el despliegue va en la ruta
	}
	if o.cfg.SendDims {
		body["dimensions"] = o.cfg.Dims
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	ep, err := o.endpoint()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	switch o.cfg.AuthHeader {
	case AuthAPIKey:
		req.Header.Set("api-key", o.cfg.APIKey)
	case AuthNone:
	default:
		req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		e := &APIError{Provider: o.cfg.Provider, Status: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
		var oe struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &oe) == nil && oe.Error.Message != "" {
			e.Message = shortBody([]byte(oe.Error.Message))
		} else {
			e.Message = shortBody(data)
		}
		return nil, e
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, &APIError{Provider: o.cfg.Provider, Status: resp.StatusCode, Message: "respuesta ilegible: " + shortBody(data)}
	}
	sort.SliceStable(out.Data, func(i, j int) bool { return out.Data[i].Index < out.Data[j].Index })
	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		vecs[i] = d.Embedding
	}
	return vecs, nil
}
