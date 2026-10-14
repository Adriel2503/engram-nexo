package security

// NX-07: valores de los productores reales de API, panel y almacenes. El tipo
// declarado de cada campo debe coincidir con el tipo que llega (números,
// booleanos, listas de strings); si no, el campo desaparece de la proyección.
import (
	"testing"
)

func TestNexoProjectionMatchesProducerTypes(t *testing.T) {
	for _, tc := range []struct {
		stream NexoStream
		meta   map[string]any
		field  string
	}{
		{NexoStreamRoles, map[string]any{"owner_principal_id": "7"}, "owner_principal_id"},
		{NexoStreamRoles, map[string]any{"owner_principal_id": int64(7)}, "owner_principal_id"},
		{NexoStreamRoles, map[string]any{"hidden": true}, "hidden"},
		{NexoStreamAudit, map[string]any{"alert_id": int64(7)}, "alert_id"},
		{NexoStreamAudit, map[string]any{"review_id": int64(7)}, "review_id"},
		{NexoStreamAudit, map[string]any{"activation_id": int64(7)}, "activation_id"},
		{NexoStreamAudit, map[string]any{"only_flagged": true}, "only_flagged"},
		{NexoStreamAudit, map[string]any{"bytes": 1024}, "bytes"},
		{NexoStreamAudit, map[string]any{"tables": []string{"nexo_kb_events"}}, "tables"},
		{NexoStreamAudit, map[string]any{"grant_projects": []string{"proyecto"}}, "grant_projects"},
		{NexoStreamKB, map[string]any{"categories": []string{"estandar"}}, "categories"},
		{NexoStreamAlert, map[string]any{"principal_id": int64(9007199254740993)}, "principal_id"},
		{NexoStreamAlert, map[string]any{"owner_principal_id": int64(7)}, "owner_principal_id"},
	} {
		t.Run(string(tc.stream)+"/"+tc.field, func(t *testing.T) {
			if _, ok := NexoProject(tc.stream, tc.meta, false)[tc.field]; !ok {
				t.Error("el tipo producido desaparece de la proyección")
			}
		})
	}
}

func TestNexoProjectionIDRejectsNonIdentifiers(t *testing.T) {
	for _, v := range []any{true, 1.5, "nxa_aaaaaaaaaaaaaaaa", []any{"x"}} {
		if _, ok := NexoProject(NexoStreamAudit, map[string]any{"alert_id": v}, false)["alert_id"]; ok {
			t.Errorf("%T no es un identificador válido", v)
		}
	}
	if got := NexoProject(NexoStreamAudit, map[string]any{"alert_id": int64(9007199254740993)}, false)["alert_id"]; got != int64(9007199254740993) {
		t.Errorf("el identificador BIGINT perdió precisión: %v", got)
	}
}
