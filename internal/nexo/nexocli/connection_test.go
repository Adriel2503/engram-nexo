package nexocli

// engram nexo connect | status | disconnect | setup contra la API real
// (Postgres, CLOUDSTORE_TEST_DSN).

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloudconfig"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

func runNexo(t *testing.T, dataDir string, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, dataDir, &out, &errb)
	return out.String(), errb.String(), code
}

func TestConnectStatusDisconnect(t *testing.T) {
	env := kbtest.New(t)
	tm := env.NewTeam()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "general", Name: "General", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	srv := env.HTTPServer()
	dataDir := t.TempDir()
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	t.Setenv("NEXO_TOKEN", "")
	t.Setenv(client.EnvLocalDB, filepath.Join(t.TempDir(), "kb.db"))

	// Token inválido: no guarda nada.
	stdin = strings.NewReader("no-existe\n")
	_, errOut, code := runNexo(t, dataDir, "connect", "--server", srv.URL, "--token-stdin")
	if code != 1 || !strings.Contains(errOut, "no es válido") || client.Connected(dataDir) {
		t.Fatalf("token inválido: %d %s", code, errOut)
	}
	if _, err := os.Stat(cloudconfig.Path(dataDir)); err == nil {
		t.Fatal("se guardó cloud.json con un token inválido")
	}

	// Token correcto: guarda, valida y sincroniza; nunca imprime el token.
	stdin = strings.NewReader(tm.Luis + "\n")
	out, errOut, code := runNexo(t, dataDir, "connect", "--server", srv.URL, "--token-stdin")
	if code != 0 || !strings.Contains(out, "Conectado a "+srv.URL+" como luis") || !strings.Contains(out, "1 espacios") || !client.Connected(dataDir) {
		t.Fatalf("connect: %d %s %s", code, out, errOut)
	}
	cc, _ := cloudconfig.Load(dataDir)
	if cc.ServerURL != srv.URL || cc.Token != tm.Luis {
		t.Fatalf("cloud.json: %+v", cc.ServerURL)
	}
	conn, _ := os.ReadFile(filepath.Join(dataDir, client.ConnectionFile))
	// (en este servidor de prueba el token es el id del principal, así que
	// se verifica que no exista un campo de token).
	if strings.Contains(string(conn), `"token"`) {
		t.Fatal("nexo.json contiene el token")
	}

	out, _, code = runNexo(t, dataDir, "status")
	if code != 0 || !strings.Contains(out, "conectado a "+srv.URL) || !strings.Contains(out, "identidad luis") || !strings.Contains(out, "1 espacios") {
		t.Fatalf("status: %d %s", code, out)
	}
	srv.SetDown(true)
	out, _, code = runNexo(t, dataDir, "status")
	if code != 0 || !strings.Contains(out, "no respondió") {
		t.Fatalf("status sin servidor: %d %s", code, out)
	}
	srv.SetDown(false)

	out, _, code = runNexo(t, dataDir, "disconnect")
	if code != 0 || client.Connected(dataDir) || !strings.Contains(out, "Copia local borrada") {
		t.Fatalf("disconnect: %d %s", code, out)
	}
	if cc, _ := cloudconfig.Load(dataDir); cc.Token != "" {
		t.Fatal("disconnect dejó el token")
	}
	out, _, _ = runNexo(t, dataDir, "status")
	if !strings.Contains(out, "no está conectada") {
		t.Fatalf("status desconectado: %s", out)
	}
}
