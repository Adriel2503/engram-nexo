package evidence_test

// S4-02 (RS-31, CA-S10): exportación firmada y su verificación.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

const testExportKey = "clave-de-prueba-0123456789abcdef0123456789"

func TestExportSignedAndTamperDetected(t *testing.T) {
	env := kbtest.New(t)
	ctx := context.Background()
	cfg := evidence.ExportConfig{Dir: t.TempDir(), Key: []byte(testExportKey)}
	today := time.Now().UTC()
	yesterday := today.AddDate(0, 0, -1)

	// Tres eventos de ayer y dos de hoy (la hora se reescribe y la cadena se
	// reconstruye: simula filas nacidas ayer).
	insertAudit(t, env.CS, 3, "ayer")
	adminExec(t, env.Admin, `ALTER TABLE cloud_auth_audit_log DISABLE TRIGGER nexo_guard_cloud_auth_audit_log`)
	adminExec(t, env.Admin, `UPDATE cloud_auth_audit_log SET occurred_at = $1 WHERE action = 'ayer'`, yesterday)
	adminExec(t, env.Admin, `ALTER TABLE cloud_auth_audit_log ENABLE TRIGGER nexo_guard_cloud_auth_audit_log`)
	rebuildChain(t, env.Admin, "cloud_auth_audit_log")
	insertAudit(t, env.CS, 2, "hoy")

	r1, err := evidence.ExportDay(ctx, env.DB, cfg, "cloud_auth_audit_log", yesterday, "test")
	if err != nil || r1.Rows != 3 || r1.FirstSeq != 1 || r1.LastSeq != 3 {
		t.Fatalf("exportar ayer: %+v %v", r1, err)
	}
	r2, err := evidence.ExportDay(ctx, env.DB, cfg, "cloud_auth_audit_log", today, "test")
	if err != nil || r2.Rows != 2 || r2.FirstSeq != 4 {
		t.Fatalf("exportar hoy: %+v %v", r2, err)
	}
	if again, err := evidence.ExportDay(ctx, env.DB, cfg, "cloud_auth_audit_log", today, "test"); err != nil || !again.Skipped {
		t.Fatalf("un día exportado no se repite: %+v %v", again, err)
	}
	if before, err := evidence.ExportDay(ctx, env.DB, cfg, "cloud_auth_audit_log", yesterday.AddDate(0, 0, -1), "test"); err != nil || !before.Skipped {
		t.Fatalf("un día anterior a otro exportado se salta: %+v %v", before, err)
	}
	// Tablas sin cadena y días sin eventos también dejan archivo firmado.
	if r, err := evidence.ExportDay(ctx, env.DB, cfg, "nexo_alerts", today, "test"); err != nil || r.Rows != 0 || r.File == "" {
		t.Fatalf("alertas sin filas: %+v %v", r, err)
	}
	var registered int
	if err := env.DB.QueryRow(`SELECT count(*) FROM nexo_audit_exports`).Scan(&registered); err != nil || registered != 3 {
		t.Fatalf("registro: %d %v", registered, err)
	}

	chk, err := evidence.VerifyExport(ctx, r2.File, cfg.Key, env.DB)
	if err != nil || !chk.OK || chk.PreviousOK == nil || !*chk.PreviousOK || chk.DatabaseOK == nil || !*chk.DatabaseOK {
		t.Fatalf("verificación de hoy: %+v %v", chk, err)
	}
	if chk, _ := evidence.VerifyExport(ctx, r1.File, cfg.Key, nil); !chk.OK || chk.Rows != 3 {
		t.Fatalf("verificación de ayer: %+v", chk)
	}
	// Clave equivocada.
	if chk, _ := evidence.VerifyExport(ctx, r2.File, []byte("otra-clave-0123456789abcdef0123456789"), nil); chk.OK || chk.SignatureOK {
		t.Fatalf("clave distinta: %+v", chk)
	}
	// CA-S10: modificar el archivo hace fallar la verificación.
	data, _ := os.ReadFile(r2.File)
	tampered := bytes.Replace(data, []byte(`"hoy"`), []byte(`"HOY"`), 1)
	if bytes.Equal(tampered, data) {
		t.Fatal("no se pudo alterar el archivo de prueba")
	}
	if err := os.WriteFile(r2.File, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if chk, _ := evidence.VerifyExport(ctx, r2.File, cfg.Key, nil); chk.OK || chk.FileHashOK {
		t.Fatalf("archivo alterado: %+v", chk)
	}
	_ = os.WriteFile(r2.File, data, 0o600)
	// Alterar la firma (por ejemplo, la cantidad de filas) también.
	sig, _ := os.ReadFile(r2.File + ".sig")
	if err := os.WriteFile(r2.File+".sig", bytes.Replace(sig, []byte(`"rows": 2`), []byte(`"rows": 1`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if chk, _ := evidence.VerifyExport(ctx, r2.File, cfg.Key, nil); chk.OK || chk.SignatureOK {
		t.Fatalf("firma alterada: %+v", chk)
	}
	_ = os.WriteFile(r2.File+".sig", sig, 0o600)
	// Alterar la base después de exportar: la comparación con la base lo ve.
	adminExec(t, env.Admin, `ALTER TABLE cloud_auth_audit_log DISABLE TRIGGER nexo_guard_cloud_auth_audit_log`)
	adminExec(t, env.Admin, `UPDATE cloud_auth_audit_log SET outcome = 'alterado' WHERE nexo_chain_seq = 5`)
	adminExec(t, env.Admin, `ALTER TABLE cloud_auth_audit_log ENABLE TRIGGER nexo_guard_cloud_auth_audit_log`)
	chk, err = evidence.VerifyExport(ctx, r2.File, cfg.Key, env.DB)
	if err != nil || chk.OK || chk.DatabaseOK == nil || *chk.DatabaseOK || !strings.Contains(strings.Join(chk.Problems, " "), "posición 5") {
		t.Fatalf("base alterada después de exportar: %+v %v", chk, err)
	}
	if filepath.Base(filepath.Dir(r2.File)) != "cloud_auth_audit_log" {
		t.Fatalf("ruta: %s", r2.File)
	}
}

func TestExportConfigFromEnv(t *testing.T) {
	t.Setenv(evidence.ExportDirEnv, "")
	if _, ok, why := evidence.ExportConfigFromEnv(); ok || !strings.Contains(why, "desactivada") {
		t.Fatalf("sin directorio: %v %q", ok, why)
	}
	t.Setenv(evidence.ExportDirEnv, t.TempDir())
	t.Setenv(evidence.ExportKeyEnv, "corta")
	if _, ok, why := evidence.ExportConfigFromEnv(); ok || !strings.Contains(why, evidence.ExportKeyEnv) {
		t.Fatalf("clave corta: %v %q", ok, why)
	}
	t.Setenv(evidence.ExportKeyEnv, testExportKey)
	if cfg, ok, _ := evidence.ExportConfigFromEnv(); !ok || cfg.KeyID() == "" || strings.Contains(cfg.KeyID(), "clave") {
		t.Fatalf("configurada: %v", ok)
	}
}

// ExportRange no exporta el día en curso salvo que se pida.
func TestExportRangeStopsYesterday(t *testing.T) {
	env := kbtest.New(t)
	cfg := evidence.ExportConfig{Dir: t.TempDir(), Key: []byte(testExportKey)}
	now := time.Now().UTC()
	res, err := evidence.ExportRange(context.Background(), env.DB, cfg, now.AddDate(0, 0, -2), time.Time{}, now, "test", false)
	if err != nil || len(res) != 2*len(evidence.ExportTables) {
		t.Fatalf("rango: %d %v", len(res), err)
	}
	for _, r := range res {
		if r.Date == now.Format(time.DateOnly) {
			t.Fatalf("exportó el día en curso: %+v", r)
		}
	}
	res, err = evidence.ExportRange(context.Background(), env.DB, cfg, now, time.Time{}, now, "test", true)
	if err != nil || len(res) != len(evidence.ExportTables) || res[0].Date != now.Format(time.DateOnly) {
		t.Fatalf("con el día en curso: %+v %v", res, err)
	}
}
