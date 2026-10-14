package security_test

// NX-07: presencia Y tipo real de cada columna, no solo su nombre en el esquema.
import (
	"bytes"
	"encoding/json"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
	"slices"
	"testing"
)

func TestNexoExportEveryColumnKeepsPostgresValuePG(t *testing.T) {
	env := kbtest.New(t)
	for _, table := range evidence.ExportTables {
		rows, err := env.DB.Query("SELECT column_name,udt_name FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=$1 ORDER BY ordinal_position", table)
		if err != nil {
			t.Fatal(err)
		}
		var cols [][2]string
		for rows.Next() {
			var col, typ string
			if err := rows.Scan(&col, &typ); err != nil {
				t.Fatal(err)
			}
			cols = append(cols, [2]string{col, typ})
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		for _, c := range cols {
			t.Run(table+"."+c[0], func(t *testing.T) {
				if slices.Contains(security.NexoExportChainColumns(), c[0]) || exportOmitted[table+"."+c[0]] != "" {
					return
				}
				var expr string
				switch c[1] {
				case "int8":
					expr = "9007199254740993::bigint"
				case "int4":
					expr = "7::integer"
				case "bool":
					expr = "true::boolean"
				case "timestamptz":
					expr = "'2026-10-14T01:02:03Z'::timestamptz"
				case "date":
					expr = "'2026-10-14'::date"
				case "text":
					expr = "'valor'::text"
				case "uuid":
					expr = "'12345678-1234-1234-1234-123456789abc'::uuid"
				case "jsonb":
					expr = "'{}'::jsonb"
				case "_text":
					expr = "ARRAY['valor']::text[]"
				default:
					t.Fatalf("tipo Postgres sin muestra: %s", c[1])
				}
				var raw []byte
				if err := env.DB.QueryRow("SELECT jsonb_build_object($1::text,"+expr+")::text", c[0]).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var row map[string]any
				dec := json.NewDecoder(bytes.NewReader(raw))
				dec.UseNumber()
				if err := dec.Decode(&row); err != nil {
					t.Fatal(err)
				}
				out := security.NexoExportRow(table, row)
				val, ok := out[c[0]]
				if !ok {
					t.Fatalf("valor no vacío de %s omitido por tipo %s", c[0], c[1])
				}
				if c[1] == "int8" {
					encoded, _ := json.Marshal(val)
					if string(encoded) != "9007199254740993" {
						t.Error("BIGINT pierde precisión")
					}
				}
			})
		}
	}
}
