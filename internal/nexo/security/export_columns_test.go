package security_test

// NX-07 / R-6: la exportación solo lleva las columnas que el esquema de cada
// tabla declara (security.NexoExportRow). Una columna nueva en una tabla
// exportada rompe esta prueba hasta que se decida si se exporta (se declara
// en nexo_projection_export.go) o es texto libre que no sale (se anota aquí).

import (
	"slices"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

// exportOmitted: columnas de texto libre que no se exportan, con el motivo.
var exportOmitted = map[string]string{
	"nexo_alerts.summary":       "texto libre del resumen de la alerta (puede llevar nombres de documentos y de personas)",
	"nexo_alert_acks.note":      "nota libre del reconocimiento",
	"nexo_access_reviews.notes": "nota libre de la revisión de accesos",
}

func TestExportRowSchemasCoverEveryColumn(t *testing.T) {
	env := kbtest.New(t)
	chain := security.NexoExportChainColumns()
	for _, table := range evidence.ExportTables {
		rows, err := env.DB.Query(`SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position`, table)
		if err != nil {
			t.Fatal(err)
		}
		declared := security.NexoExportColumns(table)
		if len(declared) == 0 {
			t.Errorf("%s: sin esquema de exportación", table)
		}
		seen := map[string]bool{}
		for rows.Next() {
			var col string
			if err := rows.Scan(&col); err != nil {
				t.Fatal(err)
			}
			seen[col] = true
			if slices.Contains(chain, col) || slices.Contains(declared, col) {
				continue
			}
			if _, ok := exportOmitted[table+"."+col]; !ok {
				t.Errorf("%s.%s: columna sin clasificar para la exportación. Declárela en nexoExportSchemas (security/nexo_projection_export.go) o anótela en exportOmitted con el motivo", table, col)
			}
		}
		_ = rows.Close()
		for _, col := range declared {
			if !seen[col] {
				t.Errorf("%s declara la columna %s, que ya no existe", table, col)
			}
		}
	}
}
