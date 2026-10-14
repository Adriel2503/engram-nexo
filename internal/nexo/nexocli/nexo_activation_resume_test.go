package nexocli

// NX-07: `engram nexo connect --resume` continúa un canje interrumpido sin
// pedir otro código y sin imprimir credenciales.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloudconfig"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
)

func TestNexoConnectResumeAfterInterruptedActivation(t *testing.T) {
	const code, token = "nxa_codigo-de-prueba-0123456789", "egc_live_0123abcd_Zy9-Zy9-Zy9-Zy9-Zy9-Zy9-"
	var redeemed atomic.Int32
	var meDown atomic.Bool
	meDown.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/nexo/authz/v1/activate":
			redeemed.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"raw_token": token})
		case "/nexo/authz/v1/me":
			if meDown.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"principal_id": "1", "label": "persona", "token_scope": "read", "spaces": []any{}})
		default:
			_, _ = w.Write([]byte(`{"spaces":[],"documents":[],"next_cursor":null}`))
		}
	}))
	defer srv.Close()
	t.Setenv(client.EnvLocalDB, filepath.Join(t.TempDir(), "kb.db"))
	t.Setenv("NEXO_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	data := t.TempDir()

	var out, errOut bytes.Buffer
	if runConnect([]string{"--server", srv.URL, "--activate", code}, data, &out, &errOut) == 0 {
		t.Fatal("con /me caído la conexión debía fallar")
	}
	if all := out.String() + errOut.String(); strings.Contains(all, code) || strings.Contains(all, token) || !strings.Contains(all, "--resume") {
		t.Fatalf("salida incorrecta (debe indicar --resume y no repetir credenciales): %.80q", all)
	}
	if cfg, _ := cloudconfig.Load(data); cfg.Token != "" {
		t.Fatal("cloud.json no debe tener token hasta validar")
	}

	meDown.Store(false)
	out.Reset()
	errOut.Reset()
	if runConnect([]string{"--server", srv.URL, "--resume"}, data, &out, &errOut) != 0 {
		t.Fatalf("--resume falló: %.80q", errOut.String())
	}
	if all := out.String() + errOut.String(); strings.Contains(all, token) {
		t.Fatal("la CLI imprimió el token")
	}
	if cfg, _ := cloudconfig.Load(data); cfg.Token != token {
		t.Fatal("token no guardado al reanudar")
	}
	if redeemed.Load() != 1 {
		t.Fatalf("canjes: %d", redeemed.Load())
	}
	// Sin activación pendiente, --resume lo dice.
	out.Reset()
	errOut.Reset()
	if runConnect([]string{"--server", srv.URL, "--resume"}, data, &out, &errOut) == 0 || !strings.Contains(errOut.String(), "no hay una activación interrumpida") {
		t.Fatalf("--resume sin pendiente: %q", errOut.String())
	}
	// --resume no se combina con credenciales.
	if runConnect([]string{"--server", srv.URL, "--resume", "--token", "x"}, data, &out, &errOut) != 2 {
		t.Fatal("--resume con --token debe ser un error de uso")
	}
}
