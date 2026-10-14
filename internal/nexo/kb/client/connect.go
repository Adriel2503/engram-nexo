package client

// Conexión de la PC con Nexo (tanda 2, cambio de alcance F-04): `engram nexo
// connect` valida el servidor y el token con /nexo/authz/v1/me, los guarda en
// la configuración cloud de Engram (cloud.json, permisos 0600, como `engram
// cloud config`), deja el marcador nexo.json en la carpeta de datos (sin el
// token) y hace la primera sincronización de la copia local. El token nunca se
// imprime ni se escribe fuera de cloud.json.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloudconfig"
)

// ConnectionFile es el marcador de conexión dentro de la carpeta de datos de
// Engram. Su presencia habilita las herramientas nexo_* con el perfil agent
// (`engram mcp --tools=agent`, el que escribe `engram setup`) y la
// instalación del protocolo Nexo en `engram setup`.
const ConnectionFile = "nexo.json"

// Connection es el contenido de nexo.json (sin secretos).
type Connection struct {
	Server      string    `json:"server"`
	Label       string    `json:"label,omitempty"`
	PrincipalID string    `json:"principal_id,omitempty"`
	TokenScope  string    `json:"token_scope,omitempty"`
	ConnectedAt time.Time `json:"connected_at"`
	LocalCopy   string    `json:"local_copy,omitempty"`
}

// LoadConnection lee nexo.json (nil si no existe).
func LoadConnection(dataDir string) (*Connection, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, ConnectionFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Connection
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("%s inválido: %w", ConnectionFile, err)
	}
	return &c, nil
}

// Connected informa si la PC está conectada a Nexo.
func Connected(dataDir string) bool {
	c, err := LoadConnection(dataDir)
	return err == nil && c != nil && c.Server != ""
}

func saveConnection(dataDir string, c Connection) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(filepath.Join(dataDir, ConnectionFile), raw, 0o600)
}

// ConnectResult es el resultado de Connect.
type ConnectResult struct {
	Server     string     `json:"server"`
	Label      string     `json:"label"`
	TokenScope string     `json:"token_scope"`
	Spaces     int        `json:"spaces"`
	Sync       SyncReport `json:"sync"`
	SyncError  string     `json:"sync_error,omitempty"`
	Replaced   string     `json:"replaced_server,omitempty"`
}

// Connect valida y guarda la conexión y sincroniza la copia local. Si el
// token es inválido o el servidor no responde, no guarda nada.
func Connect(ctx context.Context, dataDir, server, token string) (ConnectResult, error) {
	srv, err := cloudconfig.ValidateServerURL(server)
	if err != nil {
		return ConnectResult{}, fmt.Errorf("servidor inválido: %w", err)
	}
	srv = strings.TrimRight(srv, "/")
	token = strings.TrimSpace(token)
	if token == "" {
		return ConnectResult{}, errors.New("falta el token (--token, --token-stdin o NEXO_TOKEN)")
	}
	api := NewAPI(Settings{Server: srv, Token: token}, 20*time.Second)
	api.UserAgent = UserAgent + " (connect)"
	var me struct {
		PrincipalID string `json:"principal_id"`
		Label       string `json:"label"`
		TokenScope  string `json:"token_scope"`
		Spaces      []any  `json:"spaces"`
	}
	if err := api.Do(ctx, "GET", "/nexo/authz/v1/me", nil, &me); err != nil {
		if IsStatus(err, 401) {
			return ConnectResult{}, &TokenRejectedError{Status: 401, Msg: "el token no es válido o venció: no se guardó nada"} // NX-07: un 401 definitivo
		}
		if IsStatus(err, 403) {
			return ConnectResult{}, &TokenRejectedError{Status: 403, Msg: "el token no es de un usuario administrado (los espacios de conocimiento exigen uno): no se guardó nada"}
		}
		if IsStatus(err, 404) {
			return ConnectResult{}, &TokenRejectedError{Status: 404, Msg: "el servidor no es un servidor Nexo (no tiene /nexo/authz/v1): no se guardó nada"}
		}
		return ConnectResult{}, fmt.Errorf("no se pudo validar la conexión (%s): no se guardó nada", FriendlyError(err))
	}
	res := ConnectResult{Server: srv, Label: me.Label, TokenScope: me.TokenScope}
	if prev, _ := cloudconfig.Load(dataDir); prev != nil && prev.ServerURL != "" && strings.TrimRight(prev.ServerURL, "/") != srv {
		res.Replaced = prev.ServerURL
	}
	if err := cloudconfig.Save(dataDir, &cloudconfig.Config{ServerURL: srv, Token: token}); err != nil {
		return res, fmt.Errorf("guardar la conexión: %w", err)
	}
	local, err := OpenLocal(DefaultLocalPath())
	if err != nil {
		return res, err
	}
	defer local.Close()
	if err := saveConnection(dataDir, Connection{Server: srv, Label: me.Label, PrincipalID: me.PrincipalID, TokenScope: me.TokenScope,
		ConnectedAt: time.Now().UTC(), LocalCopy: local.Path()}); err != nil {
		return res, fmt.Errorf("guardar %s: %w", ConnectionFile, err)
	}
	rep, err := NewSyncer(api, local).Sync(ctx)
	res.Sync, res.Spaces = rep, rep.Status.Spaces
	if err != nil {
		res.SyncError = FriendlyError(err)
	}
	return res, nil
}

// DisconnectResult es el resultado de Disconnect.
type DisconnectResult struct {
	RemovedCopy  string // copia local borrada ("" si se conservó)
	ClearedCloud bool   // se borraron servidor y token de cloud.json
	// KeptCloudServer: cloud.json no se tocó porque su servidor no es el de
	// la conexión Nexo (o no hay nexo.json): es una configuración cloud ajena.
	KeptCloudServer string
}

// Disconnect quita la conexión: borra nexo.json, el token y el servidor de
// cloud.json solo si son los de la conexión Nexo (nexo.json con el mismo
// servidor) y, salvo keepCopy, la copia local. Sin nexo.json o con otro
// servidor en cloud.json, cloud.json queda igual (se informa en el resultado).
func Disconnect(dataDir string, keepCopy bool) (DisconnectResult, error) {
	var res DisconnectResult
	c, _ := LoadConnection(dataDir)
	if prev, _ := cloudconfig.Load(dataDir); prev != nil && (prev.ServerURL != "" || prev.Token != "") {
		if c != nil && c.Server != "" && strings.TrimRight(prev.ServerURL, "/") == strings.TrimRight(c.Server, "/") {
			if err := cloudconfig.Clear(dataDir); err != nil {
				return res, err
			}
			res.ClearedCloud = true
		} else {
			res.KeptCloudServer = prev.ServerURL
			if res.KeptCloudServer == "" {
				res.KeptCloudServer = "(sin servidor)"
			}
		}
	}
	if err := os.Remove(filepath.Join(dataDir, ConnectionFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	clearPendingToken(dataDir) // NX-07: un token canjeado sin conectar tampoco queda en la PC
	if keepCopy {
		return res, nil
	}
	path := DefaultLocalPath()
	if c != nil && c.LocalCopy != "" {
		path = c.LocalCopy
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return res, err
		}
	}
	res.RemovedCopy = path
	return res, nil
}
