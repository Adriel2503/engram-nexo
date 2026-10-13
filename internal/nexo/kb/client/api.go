// Package client es el cliente Nexo de los espacios de conocimiento (NX-10,
// design §6.4 y §6.5, tareas D-01 y D-02): cliente HTTP de /nexo/kb/v1,
// copia local de solo lectura en un SQLite propio (FTS5, BM25), sincronizada
// con el feed /sync, y las herramientas MCP nexo_*.
//
// La copia local es un archivo aparte (por defecto ~/.nexo/kb.db): nunca
// toca la base de Engram ni su esquema.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloudconfig"
)

// UserAgent identifica al cliente Nexo ante el servidor (queda en los
// eventos como referencia; la identidad sale siempre del token).
const UserAgent = "engram-nexo-kb/2"

// API llama a /nexo/kb/v1 (y a /nexo/authz/v1/me).
type API struct {
	Server    string
	Token     string
	HTTP      *http.Client
	UserAgent string
}

// APIError es un error devuelto por la API.
type APIError struct {
	Status int
	Code   string `json:"error_code"`
	Msg    string `json:"error"`
}

func (e *APIError) Error() string { return fmt.Sprintf("HTTP %d %s: %s", e.Status, e.Code, e.Msg) }

// Settings son el servidor y el token configurados.
type Settings struct {
	Server string
	Token  string
}

// LoadSettings lee el servidor y el token de `engram cloud config`
// (cloud.json) o de ENGRAM_CLOUD_SERVER y ENGRAM_CLOUD_TOKEN; server, si no
// está vacío, reemplaza al configurado. No valida que existan.
func LoadSettings(dataDir, server string) (Settings, error) {
	cc, err := cloudconfig.Load(dataDir)
	if err != nil {
		return Settings{}, fmt.Errorf("leer configuración cloud: %w", err)
	}
	cc = cloudconfig.ApplyServerOverride(cc)
	token, _ := cloudconfig.EffectiveToken(dataDir)
	if strings.TrimSpace(server) == "" && cc != nil {
		server = cc.ServerURL
	}
	return Settings{Server: strings.TrimRight(strings.TrimSpace(server), "/"), Token: strings.TrimSpace(token)}, nil
}

// New crea el cliente con el servidor y el token configurados.
func New(dataDir, server string, timeout time.Duration) (*API, error) {
	st, err := LoadSettings(dataDir, server)
	if err != nil {
		return nil, err
	}
	if st.Server == "" {
		return nil, errors.New("falta el servidor: engram cloud config --server <url> o ENGRAM_CLOUD_SERVER")
	}
	if st.Token == "" {
		return nil, errors.New("falta el token: ENGRAM_CLOUD_TOKEN o engram cloud config (el conocimiento exige un token de usuario administrado)")
	}
	return NewAPI(st, timeout), nil
}

// NewAPI crea el cliente con datos ya resueltos.
func NewAPI(st Settings, timeout time.Duration) *API {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &API{Server: st.Server, Token: st.Token, HTTP: &http.Client{Timeout: timeout}, UserAgent: UserAgent}
}

func (c *API) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Server+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	ua := c.UserAgent
	if ua == "" {
		ua = UserAgent
	}
	req.Header.Set("User-Agent", ua)
	return req, nil
}

// Do envía una petición JSON y decodifica la respuesta en out.
func (c *API) Do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := c.newRequest(ctx, method, path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.send(req, out)
}

// DoMultipart envía un cuerpo multipart y decodifica la respuesta JSON.
func (c *API) DoMultipart(ctx context.Context, path string, body *bytes.Buffer, contentType string, out any) error {
	req, err := c.newRequest(ctx, "POST", path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	return c.send(req, out)
}

func (c *API) send(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		ae := &APIError{Status: resp.StatusCode}
		_ = json.Unmarshal(raw, ae)
		if ae.Msg == "" {
			ae.Msg = strings.TrimSpace(string(raw))
			if len(ae.Msg) > 300 {
				ae.Msg = ae.Msg[:300]
			}
		}
		return ae
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Unreachable informa si el error significa que el servidor no respondió
// (red, tiempo agotado, o un proxy que responde 502/503/504): en ese caso el
// cliente responde desde la copia local (RF-42). Un 401/403/404/422 no es
// "sin conexión": el servidor respondió.
func Unreachable(err error) bool {
	if err == nil {
		return false
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == http.StatusBadGateway || ae.Status == http.StatusServiceUnavailable || ae.Status == http.StatusGatewayTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	// url.Error envuelve errores de conexión con mensajes de la plataforma.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such host") || strings.Contains(msg, "connectex") ||
		strings.Contains(msg, "connection reset") || strings.Contains(msg, "timeout")
}

// IsStatus informa si err es un APIError con ese estado.
func IsStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}
