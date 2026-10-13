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
