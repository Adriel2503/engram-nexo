package evidence

// Webhook de alertas (S4-03, DS-3): opcional. Sin NEXO_ALERT_WEBHOOK_URL las
// alertas quedan en la base y en la API.
//
// Cuerpo JSON compatible con los webhooks entrantes de Slack ("text") y de
// Microsoft Teams (conector clásico, MessageCard: "text" y "summary"); los
// campos de nexo_alert son para receptores propios. No lleva secretos: ni
// tokens, ni huellas, ni el contenido de los eventos; solo la regla, la capa,
// el sujeto (principal:<id>, ip:<ip>) y un resumen.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// AlertWebhookEnv es la URL del webhook.
const AlertWebhookEnv = "NEXO_ALERT_WEBHOOK_URL"

// Webhook envía alertas a un webhook HTTP con reintentos.
type Webhook struct {
	url     string
	host    string
	client  *http.Client
	backoff []time.Duration
}

// WebhookFromEnv crea el webhook desde NEXO_ALERT_WEBHOOK_URL (nil si no
// está). Una URL inválida se ignora con aviso; http (sin TLS) se acepta con
// aviso (pruebas y redes internas).
func WebhookFromEnv() *Webhook {
	raw := strings.TrimSpace(os.Getenv(AlertWebhookEnv))
	if raw == "" {
		return nil
	}
	w, err := NewWebhook(raw)
	if err != nil {
		log.Printf("[nexo] seguridad: %s inválida (%v): las alertas solo quedan en la base", AlertWebhookEnv, err)
		return nil
	}
	return w
}

// NewWebhook valida la URL.
func NewWebhook(raw string) (*Webhook, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("se espera una URL http(s) absoluta")
	}
	if u.Scheme == "http" {
		log.Printf("[nexo] seguridad: %s usa http sin TLS (host %s): úselo solo en redes internas o pruebas", AlertWebhookEnv, u.Host)
	}
	return &Webhook{
		url:     u.String(),
		host:    u.Host,
		client:  &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		backoff: []time.Duration{0, 2 * time.Second, 10 * time.Second},
	}, nil
}

// Host devuelve el host del webhook (para el log: la URL completa puede
// llevar una clave, como en Slack o Teams, y nunca se registra).
func (w *Webhook) Host() string {
	if w == nil {
		return ""
	}
	return w.host
}

// WithBackoff cambia los reintentos (pruebas).
func (w *Webhook) WithBackoff(b ...time.Duration) *Webhook {
	w.backoff = b
	return w
}

// WebhookPayload es el cuerpo enviado.
type WebhookPayload struct {
	Text      string       `json:"text"`
	Summary   string       `json:"summary"`
	NexoAlert webhookAlert `json:"nexo_alert"`
}

type webhookAlert struct {
	ID         int64     `json:"id"`
	Rule       string    `json:"rule"`
	Severity   string    `json:"severity"`
	Layer      Layer     `json:"layer"`
	Subject    string    `json:"subject"`
	OccurredAt time.Time `json:"occurred_at"`
}

// Payload arma el cuerpo de una alerta.
func Payload(a Alert) WebhookPayload {
	// NX-07: el sujeto puede venir de texto externo (clave de documento, proyecto): también las
	// alertas históricas salen sin formatos de credencial.
	a.Subject = security.NexoRedactAuditText(a.Subject)
	text := fmt.Sprintf("[Nexo] alerta %s (%s, capa %s): %s", a.Rule, a.Severity, a.Layer, a.Subject)
	return WebhookPayload{
		Text:    text,
		Summary: fmt.Sprintf("Nexo: %s", a.Rule),
		NexoAlert: webhookAlert{
			ID: a.ID, Rule: a.Rule, Severity: a.Severity, Layer: a.Layer, Subject: a.Subject, OccurredAt: a.OccurredAt.UTC(),
		},
	}
}

// Send envía una alerta con reintentos (respuesta 2xx = entregada).
func (w *Webhook) Send(ctx context.Context, a Alert) error {
	if w == nil {
		return nil
	}
	body, err := json.Marshal(Payload(a))
	if err != nil {
		return err
	}
	var last error
	for _, wait := range w.backoff {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "nexo-alerts/1")
		resp, err := w.client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			last = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			// El error de net/http incluye la URL (puede llevar una clave): no
			// se propaga.
			last = fmt.Errorf("sin respuesta")
		}
	}
	return fmt.Errorf("webhook %s: alerta %d no entregada tras %d intentos: %v", w.host, a.ID, len(w.backoff), last)
}
