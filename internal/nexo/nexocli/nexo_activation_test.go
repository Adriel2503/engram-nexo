package nexocli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloudconfig"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

func TestNexoConnectActivateWithoutPostgres(t *testing.T) {
	code, _ := security.GenerateActivationCode()
	token, err := cloudauth.GenerateManagedToken("live")
	if err != nil {
		t.Fatal("generación fallida")
	}
	redeemed := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/nexo/authz/v1/activate":
			var body struct {
				Code string `json:"code"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.Method != "POST" || body.Code != code {
				t.Error("petición de canje incorrecta")
			}
			redeemed++
			_ = json.NewEncoder(w).Encode(map[string]any{"raw_token": token.Raw})
		case "/nexo/authz/v1/me":
			if r.Header.Get("Authorization") != "Bearer "+token.Raw {
				t.Error("se guardó otra credencial")
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
	old := stdin
	t.Cleanup(func() { stdin = old })
	for _, mode := range []string{"argumento", "entrada"} {
		var out, errOut bytes.Buffer
		args := []string{"--server", srv.URL}
		if mode == "argumento" {
			args = append(args, "--activate", code)
		} else {
			stdin = strings.NewReader(code + "\n")
			args = append(args, "--activate-stdin")
		}
		if runConnect(args, data, &out, &errOut) != 0 {
			t.Fatal("connect --activate falló (salida oculta)")
		}
		if strings.Contains(out.String()+errOut.String(), code) || strings.Contains(out.String()+errOut.String(), token.Raw) {
			t.Fatal("la CLI imprimió una credencial")
		}
		cfg, err := cloudconfig.Load(data)
		if err != nil || cfg.Token != token.Raw {
			t.Fatal("token canjeado no guardado")
		}
		marker, _ := os.ReadFile(filepath.Join(data, client.ConnectionFile))
		if strings.Contains(string(marker), code) || strings.Contains(string(marker), token.Raw) {
			t.Fatal("credencial en marcador")
		}
	}
	if redeemed != 2 {
		t.Fatal("no se llamó a la ruta de canje")
	}
}
