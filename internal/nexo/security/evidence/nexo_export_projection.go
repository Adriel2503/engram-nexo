package evidence

// NX-07 / R-6: las exportaciones automáticas no tienen un lector de proyecto.
// Conservan identificadores y hashes de la cadena y, de cada fila, solo las
// columnas que el esquema de su tabla declara (security.NexoExportRow): el
// texto libre no se exporta como contenido, y todo texto de la fila sale sin
// formatos de credencial, también en filas históricas.
import (
	"encoding/json"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

func nexoExportMetadataLine(line string) (string, error) {
	var envelope map[string]any
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	if err := dec.Decode(&envelope); err != nil {
		return "", err
	}
	if row, ok := envelope["row"].(map[string]any); ok {
		table, _ := envelope["table"].(string)
		envelope["row"] = security.NexoExportRow(table, row)
	}
	raw, err := json.Marshal(envelope)
	return string(raw), err
}
