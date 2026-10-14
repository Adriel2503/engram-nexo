package evidence

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

func TestNexoExportProjectionKeepsEvidenceWithoutContent(t *testing.T) {
	row := map[string]any{"table": "nexo_kb_events", "seq": 1, "digest": "digest-original", "hash": "hash-original", "row": map[string]any{"id": 1, "details": map[string]any{"doc_key": "doc-1", "title": "contenido reservado", "payload": map[string]any{"body": "contenido reservado"}}, "summary": "contenido reservado"}}
	raw, _ := json.Marshal(row)
	line, err := nexoExportMetadataLine(string(raw))
	if err != nil || strings.Contains(line, "contenido reservado") || !strings.Contains(line, "doc-1") || !strings.Contains(line, "digest-original") || !strings.Contains(line, "payload_withheld") {
		t.Fatal("proyección de exportación incorrecta")
	}
}

// NX-07: todo texto de la fila sale sin formatos de credencial (User-Agent, declaración del
// cliente, etiquetas, detalles anidados), pero los hash y el digest de la cadena no se tocan.
func TestNexoExportProjectionRedactsCredentialsInRow(t *testing.T) {
	code, _ := security.GenerateActivationCode()
	token := "egc_live_0123abcd_" + strings.Repeat("Zz9-", 11)
	row := map[string]any{"table": "nexo_authz_events", "seq": 7, "digest": "digest-original", "hash": "hash-original", "prev_hash": "prev-original",
		"row": map[string]any{"id": 7, "user_agent": "agente " + code, "client_claim": token, "actor_label": "ana " + token,
			"details": map[string]any{"reason": "motivo " + code, "nested": map[string]any{"doc_key": code}}}}
	raw, _ := json.Marshal(row)
	line, err := nexoExportMetadataLine(string(raw))
	if err != nil || strings.Contains(line, code) || strings.Contains(line, token) {
		t.Fatalf("la exportación conserva una credencial: %v", err)
	}
	for _, keep := range []string{"digest-original", "hash-original", "prev-original", "agente ", "[credencial oculta]"} {
		if !strings.Contains(line, keep) {
			t.Fatalf("falta %q en la fila proyectada", keep)
		}
	}
}
