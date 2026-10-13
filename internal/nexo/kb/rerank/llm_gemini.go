package rerank

// Reordenador LLM sobre la API REST de Gemini (generateContent): instrucción
// de sistema con la rúbrica, un mensaje de usuario con la pregunta y los
// pasajes, temperatura 0 y respuesta JSON (responseMimeType). La clave va en
// el encabezado x-goog-api-key, nunca en la URL.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type llmGemini struct{ cfg Config }

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

func (g *llmGemini) call(ctx context.Context, hc *http.Client, query string, texts []string) (batchResult, error) {
	gen := map[string]any{
		"responseMimeType": "application/json",
		"responseSchema": map[string]any{
			"type": "OBJECT",
			"properties": map[string]any{"scores": map[string]any{
				"type": "ARRAY",
				"items": map[string]any{"type": "OBJECT", "properties": map[string]any{
					"id": map[string]any{"type": "INTEGER"}, "score": map[string]any{"type": "NUMBER"}},
					"required": []string{"id", "score"}},
			}},
			"required": []string{"scores"},
		},
	}
	if !g.cfg.NoTemperature {
		gen["temperature"] = 0
	}
	if g.cfg.Thinking >= 0 {
		gen["thinkingConfig"] = map[string]any{"thinkingBudget": g.cfg.Thinking}
	}
	body := map[string]any{
		"systemInstruction": geminiContent{Parts: []geminiPart{{Text: systemPrompt}}},
		"contents":          []geminiContent{{Role: "user", Parts: []geminiPart{{Text: buildPrompt(query, texts)}}}},
		"generationConfig":  gen,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return batchResult{}, err
	}
	model := strings.TrimPrefix(g.cfg.Model, "models/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(g.cfg.BaseURL, "/")+"/models/"+model+":generateContent", bytes.NewReader(raw))
	if err != nil {
		return batchResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.cfg.APIKey)
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
		return batchResult{}, geminiError(g.cfg.ID(), resp, data)
	}
	var out struct {
		Candidates []struct {
			Content      geminiContent `json:"content"`
			FinishReason string        `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		Usage struct {
			Prompt   int `json:"promptTokenCount"`
			Output   int `json:"candidatesTokenCount"`
			Thoughts int `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return batchResult{}, fmt.Errorf("%w: %s", ErrMalformed, shortBody(data))
	}
	res := batchResult{promptTokens: out.Usage.Prompt, outputTokens: out.Usage.Output + out.Usage.Thoughts}
	if out.PromptFeedback.BlockReason != "" {
		return res, fmt.Errorf("%w: el proveedor bloqueó el lote (%s)", ErrMalformed, out.PromptFeedback.BlockReason)
	}
	if len(out.Candidates) == 0 {
		return res, fmt.Errorf("%w: sin candidatos", ErrMalformed)
	}
	c := out.Candidates[0]
	if c.FinishReason == "MAX_TOKENS" {
		return res, fmt.Errorf("%w: respuesta cortada por el límite de salida", ErrMalformed)
	}
	var sb strings.Builder
	for _, p := range c.Content.Parts {
		sb.WriteString(p.Text)
	}
	res.scores, err = parseScores(sb.String(), len(texts))
	return res, err
}

// geminiError arma el error con el mensaje de Google y, en un 429, la espera
// sugerida (RetryInfo.retryDelay, "37s").
func geminiError(provider string, resp *http.Response, data []byte) error {
	e := &APIError{Provider: provider, Status: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	var g struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				RetryDelay string `json:"retryDelay"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &g) == nil && g.Error.Message != "" {
		e.Message = shortBody([]byte(g.Error.Status + ": " + g.Error.Message))
		for _, d := range g.Error.Details {
			if d.RetryDelay != "" {
				if dur, err := time.ParseDuration(d.RetryDelay); err == nil && dur > 0 {
					e.RetryAfter = dur
				}
			}
		}
	} else {
		e.Message = shortBody(data)
	}
	return e
}
