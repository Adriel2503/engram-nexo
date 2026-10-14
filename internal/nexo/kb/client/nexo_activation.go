package client

// NX-07: canje de un código de activación desde la CLI.
//
// El servidor entrega el token una sola vez, al consumir el código. Si la CLI
// lo perdiera (fallo de /me, vencimiento, error de disco), el siguiente intento
// recibiría activation_used y quedaría un token vigente cuyo secreto nadie
// conoce. Por eso:
//
//  1. antes de canjear se comprueba que la carpeta de datos admite escritura;
//  2. el token recibido se guarda en el acto (nexo-pending.json, permisos 0600,
//     en la misma carpeta y con el mismo tratamiento que cloud.json) y solo
//     después se hace cualquier otra operación de red;
//  3. Connect usa el token guardado; si algo falla, el archivo se conserva y
//     `engram nexo connect --server <url> --resume` (o repetir --activate con el
//     mismo servidor) continúa sin volver a canjear;
//  4. el archivo se borra cuando Connect termina bien, o cuando el servidor
//     rechaza el token (401/403: reintentar no sirve).
//
// El código nunca se guarda ni se imprime.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloudconfig"
)

// PendingTokenFile es el token ya canjeado que todavía no se conectó (sin el código).
const PendingTokenFile = "nexo-pending.json"

// TokenRejectedError indica que el servidor respondió que el token no sirve
// (401, 403) o que no es un servidor Nexo (404).
type TokenRejectedError struct {
	Status int
	Msg    string
}

func (e *TokenRejectedError) Error() string { return e.Msg }

// definitive es verdadero si repetir el intento con el mismo token no puede servir.
func (e *TokenRejectedError) definitive() bool { return e.Status == 401 || e.Status == 403 }

type pendingToken struct {
	Server  string    `json:"server"`
	Token   string    `json:"token"`
	SavedAt time.Time `json:"saved_at"`
}

func pendingPath(dataDir string) string { return filepath.Join(dataDir, PendingTokenFile) }

func sameServer(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(a), "/"), strings.TrimRight(strings.TrimSpace(b), "/"))
}

func loadPendingToken(dataDir string) (*pendingToken, error) {
	raw, err := os.ReadFile(pendingPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p pendingToken
	if err := json.Unmarshal(raw, &p); err != nil || strings.TrimSpace(p.Token) == "" {
		return nil, errors.New("nexo-pending.json no es válido")
	}
	return &p, nil
}

// savePendingToken escribe el token con permisos 0600 de forma atómica (archivo
// temporal en la misma carpeta y renombrado): un corte no deja un archivo a medias.
func savePendingToken(dataDir string, p pendingToken) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dataDir, ".nexo-pending-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, pendingPath(dataDir)); err != nil {
		cleanup()
		return err
	}
	return nil
}

func clearPendingToken(dataDir string) { _ = os.Remove(pendingPath(dataDir)) }

// checkWritable comprueba, antes de consumir el código, que la carpeta de datos
// admite archivos nuevos y que el cloud.json existente se puede reescribir.
// No modifica nada.
func checkWritable(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	probe, err := os.CreateTemp(dataDir, ".nexo-write-check-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	_ = probe.Close()
	if err := os.Remove(name); err != nil {
		return err
	}
	if f, err := os.OpenFile(cloudconfig.Path(dataDir), os.O_WRONLY, 0); err == nil {
		_ = f.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

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
				return "", errors.New("el código ya fue canjeado; solicita uno nuevo (si el canje anterior se interrumpió en esta PC, usa --resume)")
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

// ResumeActivation conecta con un token ya canjeado y guardado por una
// activación interrumpida. No contacta el endpoint de canje.
func ResumeActivation(ctx context.Context, dataDir, server string) (ConnectResult, error) {
	srv, err := cloudconfig.ValidateServerURL(server)
	if err != nil {
		return ConnectResult{}, errors.New("servidor inválido")
	}
	pending, err := loadPendingToken(dataDir)
	if err != nil {
		return ConnectResult{}, err
	}
	if pending == nil || !sameServer(pending.Server, srv) {
		return ConnectResult{}, errors.New("no hay una activación interrumpida para este servidor en esta PC")
	}
	return connectPending(ctx, dataDir, srv, pending.Token)
}

// connectPending conecta con el token guardado y limpia el archivo cuando ya no hace falta.
func connectPending(ctx context.Context, dataDir, srv, token string) (ConnectResult, error) {
	res, err := Connect(ctx, dataDir, srv, token)
	if err == nil {
		clearPendingToken(dataDir)
		return res, nil
	}
	var rejected *TokenRejectedError
	if errors.As(err, &rejected) && rejected.definitive() {
		clearPendingToken(dataDir) // el servidor ya no acepta este token: guardarlo no sirve
		return res, err
	}
	// Fallo transitorio (red, 503) o posterior a guardar cloud.json: el token se conserva.
	return res, fmt.Errorf("%w; el token ya canjeado quedó guardado en %s: repite con --resume sin código nuevo", err, pendingPath(dataDir))
}

// ConnectActivated canjea el código y conecta. El token se guarda antes de cualquier
// otra operación de red; el código nunca se guarda.
func ConnectActivated(ctx context.Context, dataDir, server, code string) (ConnectResult, error) {
	srv, err := cloudconfig.ValidateServerURL(server)
	if err != nil {
		return ConnectResult{}, errors.New("servidor inválido")
	}
	srv = strings.TrimRight(srv, "/")
	// Una activación interrumpida de esta PC se reanuda sin canjear (el código ya está usado).
	if pending, perr := loadPendingToken(dataDir); perr == nil && pending != nil && sameServer(pending.Server, srv) {
		res, err := connectPending(ctx, dataDir, srv, pending.Token)
		var rejected *TokenRejectedError
		if err == nil || !errors.As(err, &rejected) || !rejected.definitive() {
			return res, err
		}
		// El token guardado ya no sirve (revocado o vencido): se canjea el código recibido.
	}
	if err := checkWritable(dataDir); err != nil {
		return ConnectResult{}, errors.New("no se puede escribir en la carpeta de datos de Engram; no se canjeó el código: corrige los permisos y repite")
	}
	token, err := Activate(ctx, srv, code)
	if err != nil {
		return ConnectResult{}, err
	}
	if err := savePendingToken(dataDir, pendingToken{Server: srv, Token: token, SavedAt: time.Now().UTC()}); err != nil {
		return ConnectResult{}, errors.New("el código se canjeó pero no se pudo guardar el token en esta PC: pide al administrador que revoque el token y emita un código nuevo")
	}
	return connectPending(ctx, dataDir, srv, token)
}
