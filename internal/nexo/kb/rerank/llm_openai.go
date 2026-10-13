package rerank

// Reordenador LLM sobre la API de chat de OpenAI, Azure OpenAI o un servidor
// compatible (Ollama, vLLM, proxys): POST {base}/chat/completions con la
// rúbrica como mensaje de sistema, la pregunta y los pasajes como mensaje de
// usuario, temperatura 0 y response_format json_object.
//
//   - OpenAI: {base}/chat/completions, Authorization: Bearer, model.
//   - Azure OpenAI: {recurso}/openai/deployments/{despliegue}/chat/completions
//     ?api-version=..., encabezado api-key (o Bearer con un token de Entra ID);
//     el despliegue reemplaza al modelo en la ruta.
//   - Compatible: URL base con consulta si hace falta y autenticación Bearer,
//     api-key o ninguna.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type llmOpenAI struct{ cfg Config }

func (o *llmOpenAI) endpoint() (string, error) {
	u, err := url.Parse(o.cfg.BaseURL)
	if err != nil {
		return "", err
	}
	if o.cfg.API == APIAzureOpenAI {
		u.Path = strings.TrimRight(u.Path, "/") + "/openai/deployments/" + url.PathEscape(o.cfg.Deployment) + "/chat/completions"
		q := u.Query()
		q.Set("api-version", o.cfg.APIVersion)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	return u.String(), nil
}

func (o *llmOpenAI) call(ctx context.Context, hc *http.Client, query string, texts []string) (batchResult, error) {
	body := map[string]any{
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": buildPrompt(query, texts)},
		},
		"response_format": map[string]string{"type": "json_object"},
	}
	if !o.cfg.NoTemperature {
		body["temperature"] = 0
	}
	if o.cfg.API != APIAzureOpenAI {
		body["model"] = o.cfg.Model // en Azure el despliegue va en la ruta
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return batchResult{}, err
	}
	ep, err := o.endpoint()
	if err != nil {
		return batchResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(raw))
	if err != nil {
		return batchResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	setAuth(req, o.cfg)
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
		return batchResult{}, apiError(o.cfg.ID(), resp, data)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt int `json:"prompt_tokens"`
			Output int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return batchResult{}, fmt.Errorf("%w: %s", ErrMalformed, shortBody(data))
	}
	res := batchResult{promptTokens: out.Usage.Prompt, outputTokens: out.Usage.Output}
	if len(out.Choices) == 0 {
		return res, fmt.Errorf("%w: sin opciones", ErrMalformed)
	}
	if out.Choices[0].FinishReason == "length" {
		return res, fmt.Errorf("%w: respuesta cortada por el límite de salida", ErrMalformed)
	}
	res.scores, err = parseScores(out.Choices[0].Message.Content, len(texts))
	return res, err
}
