package auditcli

// SG-07 — `engram nexo audit verify-review ARCHIVO`: comprueba la huella de un
// CSV o JSON descargado de la revisión de accesos. No usa la base: sirve en
// cualquier equipo con el binario, meses después de la descarga.

import (
	"context"
	"fmt"
	"os"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

func runVerifyReview(_ context.Context, args []string) (any, bool, error) {
	if len(args) != 1 {
		return nil, false, usageError{"verify-review pide un archivo: engram nexo audit verify-review ARCHIVO.csv|ARCHIVO.json"}
	}
	raw, err := os.ReadFile(args[0])
	if err != nil {
		return nil, false, fmt.Errorf("leer %s: %w", args[0], err)
	}
	verdict, err := evidence.VerifyReviewExportFile(args[0], raw)
	if err != nil {
		return nil, false, err
	}
	return verdict, verdict.OK, nil
}
