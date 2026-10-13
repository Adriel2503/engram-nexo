package embed

// Adaptador Gemini (API REST de Google AI): batchEmbedContents con taskType
// RETRIEVAL_DOCUMENT para las secciones y RETRIEVAL_QUERY para las preguntas,
// y outputDimensionality (gemini-embedding-001 admite 128 a 3072; Nexo usa
// 768 por defecto, ver design §20). La clave va en el encabezado
// x-goog-api-key, nunca en la URL (una URL con ?key= aparecería en los
// errores de red).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type gemini struct{ cfg Config }

// geminiMaxBatch es el máximo de pedidos por batchEmbedContents.
const geminiMaxBatch = 100

func (g *gemini) maxBatch() int { return geminiMaxBatch }

type geminiPart struct {
	Text string `json:"text"`
}

type geminiRequest struct {
	Model   string `json:"model"`
	Content struct {
		Parts []geminiPart `json:"parts"`
	} `json:"content"`
	TaskType             string `json:"taskType"`
	OutputDimensionality int    `json:"outputDimensionality,omitempty"`
}

func (g *gemini) call(ctx context.Context, hc *http.Client, texts []string, kind Kind) ([][]float32, error) {
	model := strings.TrimPrefix(g.cfg.Model, "models/")
	task := "RETRIEVAL_DOCUMENT"
	if kind == KindQuery {
		task = "RETRIEVAL_QUERY"
	}
	body := struct {
		Requests []geminiRequest `json:"requests"`
	}{}
	for _, t := range texts {
		r := geminiRequest{Model: "models/" + model, TaskType: task}
		r.Content.Parts = []geminiPart{{Text: t}}
		if g.cfg.SendDims {
			r.OutputDimensionality = g.cfg.Dims
		}
		body.Requests = append(body.Requests, r)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.cfg.BaseURL+"/models/"+model+":batchEmbedContents", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.cfg.APIKey)
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
		return nil, geminiError(resp, data)
	}
	var out struct {
		Embeddings []struct {
			Values []float32 `json:"values"`
		} `json:"embeddings"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, &APIError{Provider: ProviderGemini, Status: resp.StatusCode, Message: "respuesta ilegible: " + shortBody(data)}
	}
	vecs := make([][]float32, len(out.Embeddings))
	for i, e := range out.Embeddings {
		vecs[i] = e.Values
	}
	return vecs, nil
}

// geminiError arma el error con el mensaje de Google y, en un 429, la espera
// sugerida (RetryInfo.retryDelay, "37s").
func geminiError(resp *http.Response, data []byte) error {
	e := &APIError{Provider: ProviderGemini, Status: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	var g struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				Type       string `json:"@type"`
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
