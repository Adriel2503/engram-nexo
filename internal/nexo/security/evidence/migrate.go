package evidence

import (
	"context"
	"database/sql"
)

// Migrate crea las tablas y funciones de la fase S4 (alertas, revisiones,
// exportaciones, cadena de hashes, guardas y depuración). Idempotente. Se
// ejecuta al final de la transacción de migraciones Nexo (advisory lock), con
// la cuenta que migra, después de authz.Migrate y kbstore.Migrate (las tablas
// encadenadas ya existen).
func Migrate(ctx context.Context, tx *sql.Tx) error {
	schema, err := currentSchema(ctx, tx)
	if err != nil {
		return err
	}
	if err := migrateAlerts(ctx, tx); err != nil {
		return err
	}
	if err := migrateReviewsAndExports(ctx, tx); err != nil {
		return err
	}
	if err := migrateChain(ctx, tx, schema); err != nil {
		return err
	}
	if err := migrateRetention(ctx, tx, schema); err != nil {
		return err
	}
	return nil
}

// PurgeFunctionSignature es la firma para los privilegios (EXECUTE).
const PurgeFunctionSignature = PurgeFunction + "(text, integer)"
