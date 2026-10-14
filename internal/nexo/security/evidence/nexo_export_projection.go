package evidence

// NX-07 / R-6: las exportaciones automáticas no tienen un lector de proyecto.
// Conservan identificadores y hashes de la cadena, con una proyección de la fila.
// Además, todo texto de la fila (User-Agent, declaración del cliente, nombres,
// notas, etiquetas) sale sin formatos de credencial, también en filas históricas.
import (
	"encoding/json"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

func nexoExportMetadataLine(line string) (string, error) {
	var envelope map[string]any
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		return "", err
	}
	if row, ok := envelope["row"].(map[string]any); ok {
		row = security.NexoRedactAuditMetadata(row)
		envelope["row"] = row
		for _, field := range []string{"metadata", "details"} {
			if meta, ok := row[field].(map[string]any); ok {
				row[field] = security.NexoAuditMetadata(meta, false)
			}
		}
		// Campos libres en evidencia administrativa: no se exportan como contenido.
		for _, field := range []string{"summary", "note", "notes", "content", "title", "payload"} {
			delete(row, field)
		}
		row["payload_withheld"] = true
	}
	raw, err := json.Marshal(envelope)
	return string(raw), err
}
