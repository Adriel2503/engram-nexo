package client

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloudconfig"
)

// Activate canjea sin sesión, sin redirecciones y sin repetir secretos en errores.
func Activate(ctx context.Context, server, code string) (string, error) {
	srv, err := cloudconfig.ValidateServerURL(server)
	if err != nil {
		return "", errors.New("servidor inválido")
	}
	if strings.TrimSpace(code) == "" {
		return "", errors.New("falta el código de activación")
	}
	api := NewAPI(Settings{Server: strings.TrimRight(srv, "/")}, 20*time.Second)
	api.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	api.UserAgent = UserAgent + " (activate)"
	var result struct {
		RawToken string `json:"raw_token"`
	}
	err = api.Do(ctx, "POST", "/nexo/authz/v1/activate", struct {
		Code string `json:"code"`
	}{strings.TrimSpace(code)}, &result)
	if err != nil {
		var e *APIError
		if errors.As(err, &e) {
			switch e.Code {
			case "activation_used":
				return "", errors.New("el código ya fue canjeado; solicita uno nuevo")
			case "activation_expired":
				return "", errors.New("el código venció; solicita uno nuevo")
			case "activation_revoked":
				return "", errors.New("el código fue revocado; solicita uno nuevo")
			case "activation_disabled":
				return "", errors.New("la persona está deshabilitada; contacta al administrador")
			}
			if e.Status == 429 {
				return "", errors.New("límite de intentos; espera antes de volver a intentar")
			}
		}
		return "", errors.New("no se pudo canjear el código; verifica el servidor y solicita un código nuevo si es necesario")
	}
	if strings.TrimSpace(result.RawToken) == "" {
		return "", errors.New("el servidor no entregó un token")
	}
	return result.RawToken, nil
}

// ConnectActivated guarda el token en cloud.json como Connect; nunca guarda el código.
func ConnectActivated(ctx context.Context, dataDir, server, code string) (ConnectResult, error) {
	token, err := Activate(ctx, server, code)
	if err != nil {
		return ConnectResult{}, err
	}
	return Connect(ctx, dataDir, server, token)
}
