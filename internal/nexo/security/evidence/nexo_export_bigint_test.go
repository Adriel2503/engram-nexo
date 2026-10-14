package evidence_test

// NX-07: to_jsonb entrega BIGINT como números; la firma debe cubrirlos sin pérdida.
import (
	"bytes"
	"encoding/json"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
	"os"
	"testing"
	"time"
)

func TestNexoExportDayKeepsRealBigintIdentitiesPG(t *testing.T) {
	env := kbtest.New(t)
	const actor int64 = 9007199254740993
	const target int64 = 9223372036854775806
	for _, id := range []int64{actor, target} {
		if _, err := env.DB.Exec("INSERT INTO cloud_principals(id,kind,display_name,enabled,role) VALUES($1,'human','prueba',true,'member')", id); err != nil {
			t.Fatal(err)
		}
	}
	var eventID int64
	if err := env.DB.QueryRow("INSERT INTO cloud_auth_audit_log(actor_principal_id,actor_source,target_principal_id,action,outcome) VALUES($1,'test',$2,'bigint','ok') RETURNING id", actor, target).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	cfg := evidence.ExportConfig{Dir: t.TempDir(), Key: []byte(testExportKey)}
	res, err := evidence.ExportDay(t.Context(), env.DB, cfg, "cloud_auth_audit_log", time.Now().UTC(), "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(res.File)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	found := false
	for dec.More() {
		var envelope struct {
			ID  json.Number
			Row map[string]any
		}
		if err := dec.Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		n, _ := envelope.ID.Int64()
		if n != eventID {
			continue
		}
		found = true
		for field, want := range map[string]int64{"actor_principal_id": actor, "target_principal_id": target} {
			value, ok := envelope.Row[field].(json.Number)
			got, err := value.Int64()
			if !ok || err != nil || got != want {
				t.Errorf("%s omitido o perdió precisión BIGINT", field)
			}
		}
	}
	if !found {
		t.Error("falta el evento exportado")
	}
	chk, err := evidence.VerifyExport(t.Context(), res.File, cfg.Key, env.DB)
	if err != nil || !chk.OK || !chk.SignatureOK || !chk.FileHashOK || chk.DatabaseOK == nil || !*chk.DatabaseOK {
		t.Error("la evidencia firmada no se verifica contra Postgres")
	}
}
