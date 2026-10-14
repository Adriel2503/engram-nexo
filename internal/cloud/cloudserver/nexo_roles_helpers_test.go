package cloudserver

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
)

// nexoSeedPlatformRole deja el rol de plataforma de una persona sembrada con
// SQL directo, como lo hace el almacén de Engram al crear una persona: quien
// pide role=admin (la columna de Engram se deriva y ya no la escribe nadie) es
// admin y, si todavía no hay superadmin, el primero queda superadmin. Una
// persona con role=member no cambia.
func nexoSeedPlatformRole(t testing.TB, db *sql.DB, principalID, kind, requestedEngramRole string) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authz.AssignOnCreateTx(context.Background(), tx, principalID, kind, requestedEngramRole, "test"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
