package auditcli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/auditcli"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := auditcli.Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestAuditCLIVerifyExportAndVerifyExport(t *testing.T) {
	env := kbtest.New(t)
	for i := 0; i < 3; i++ {
		if err := env.CS.InsertAuthAuditEvent(env.Ctx(), cloudstore.AuthAuditEvent{ActorSource: "cli-test", Action: "test.event", Outcome: "success"}); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	t.Setenv(auditcli.DatabaseURLEnv, env.DSN)
	t.Setenv(evidence.ExportDirEnv, dir)
	t.Setenv(evidence.ExportKeyEnv, "clave-cli-de-prueba-0123456789abcdef0123")

	code, out, errOut := run("verify")
	if code != 0 || !strings.Contains(out, `"ok": true`) {
		t.Fatalf("verify: %d %s %s", code, out, errOut)
	}
	if code, _, _ := run("verify", "--table", "nexo_kb_versions"); code != 2 {
		t.Fatalf("tabla sin cadena: %d", code)
	}
	if code, _, _ := run("export"); code != 2 {
		t.Fatalf("export sin --since: %d", code)
	}
	code, out, errOut = run("export", "--since", "today", "--include-today")
	if code != 0 {
		t.Fatalf("export: %d %s %s", code, out, errOut)
	}
	var res struct {
		Results []evidence.ExportResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Results) != len(evidence.ExportTables) {
		t.Fatalf("resultado: %s %v", out, err)
	}
	file := filepath.Join(dir, "cloud_auth_audit_log", time.Now().UTC().Format(time.DateOnly)+".jsonl")
	if code, out, _ := run("verify-export", file, "--db"); code != 0 || !strings.Contains(out, `"ok": true`) {
		t.Fatalf("verify-export: %d %s", code, out)
	}
	data, _ := os.ReadFile(file)
	if err := os.WriteFile(file, bytes.Replace(data, []byte("test.event"), []byte("TEST.EVENT"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run("verify-export", file); code != 1 || !strings.Contains(out, `"file_hash_ok": false`) {
		t.Fatalf("archivo alterado: %d %s", code, out)
	}
	t.Setenv(evidence.ExportDirEnv, "")
	if code, _, errOut := run("export", "--since", "today"); code != 1 || !strings.Contains(errOut, "desactivada") {
		t.Fatalf("sin directorio: %d %s", code, errOut)
	}
}

func TestVerifyReviewCommand(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 13, 15, 30, 0, 0, time.UTC)
	exp := evidence.NewReviewExport(evidence.AccessReview{GeneratedAt: at, InactiveDays: 90, People: []evidence.ReviewPerson{{PrincipalID: "1", DisplayName: "Carla", Kind: "human", Enabled: true, PlatformRole: "miembro"}}}, evidence.ReviewExportActor{PrincipalID: "5", Name: "Luis"}, false)
	jsonBody, jsonSHA, err := exp.JSON()
	if err != nil {
		t.Fatal(err)
	}
	csvBody, csvSHA, err := exp.CSV()
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	jsonPath := write(evidence.ReviewExportFilename(at, jsonSHA, "json"), jsonBody)
	csvPath := write(evidence.ReviewExportFilename(at, csvSHA, "csv"), csvBody)
	run := func(args ...string) (int, string) {
		var out, errOut bytes.Buffer
		code := auditcli.Run(append([]string{"verify-review"}, args...), &out, &errOut)
		return code, out.String() + errOut.String()
	}
	for _, p := range []string{jsonPath, csvPath} {
		if code, out := run(p); code != 0 || !strings.Contains(out, `"ok": true`) {
			t.Errorf("%s: %d %s", p, code, out)
		}
	}
	// Un archivo cambiado, un CSV renombrado y un archivo ajeno fallan.
	bad := write("nexo-revision-accesos-20261013T153000Z-"+jsonSHA+".json", bytes.Replace(jsonBody, []byte("Carla"), []byte("Karla"), 1))
	if code, out := run(bad); code != 1 || !strings.Contains(out, `"ok": false`) {
		t.Errorf("JSON cambiado: %d %s", code, out)
	}
	badCSV := write("nexo-revision-accesos-20261013T153000Z-"+csvSHA+".csv", bytes.Replace(csvBody, []byte("Carla"), []byte("Karla"), 1))
	if code, out := run(badCSV); code != 1 || !strings.Contains(out, `"ok": false`) {
		t.Errorf("CSV cambiado: %d %s", code, out)
	}
	if code, _ := run(write("renombrado.csv", csvBody)); code != 1 {
		t.Errorf("CSV sin huella en el nombre: %d", code)
	}
	if code, _ := run(write("notas.txt", []byte("x"))); code != 1 {
		t.Errorf("tipo no soportado: %d", code)
	}
	if code, _ := run(); code != 2 {
		t.Errorf("sin archivo: %d", code)
	}
	if code, _ := run(filepath.Join(dir, "no-existe.json")); code != 1 {
		t.Errorf("archivo inexistente: %d", code)
	}
}
