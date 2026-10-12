package cloudstore

// Nexo IA · NX-07 fase S1 — dos cuentas de base de datos
// (docs/nexo/seguridad/design.md §2.1).
//
// Con NEXO_MIGRATION_DATABASE_URL, las migraciones de Engram y de Nexo, los
// privilegios y la función de retención se aplican con la cuenta dueña del
// esquema; la conexión de aplicación (ENGRAM_DATABASE_URL) no migra y se
// verifica que no sea superusuario, ni dueña de tablas, ni pueda crear objetos.
// Sin esa variable, el comportamiento es el de siempre (migra con la única
// cuenta) y se registra el aviso de seguridad degradada.

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// nexoGrantsLockKey serializa la aplicación de privilegios entre réplicas.
const nexoGrantsLockKey int64 = 0x4e58_0007 // "NX" 07

// nexoMigrateOnStartup reemplaza a migrate() en New (enganche // NX-07).
func (cs *CloudStore) nexoMigrateOnStartup(ctx context.Context) error {
	ownerDSN := strings.TrimSpace(os.Getenv(security.MigrationDatabaseURLEnv))
	if ownerDSN == "" {
		log.Printf("[nexo] %s", security.DegradedWarning)
		return cs.migrate(ctx)
	}
	return cs.nexoMigrateWithOwner(ctx, ownerDSN)
}

func (cs *CloudStore) nexoMigrateWithOwner(ctx context.Context, ownerDSN string) error {
	ownerDB, err := sql.Open("pgx", ownerDSN)
	if err != nil {
		return fmt.Errorf("cloudstore: nexo: abrir cuenta dueña (%s): %w", security.MigrationDatabaseURLEnv, err)
	}
	defer func() { _ = ownerDB.Close() }()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ownerDB.PingContext(pingCtx); err != nil {
		return fmt.Errorf("cloudstore: nexo: conectar con la cuenta dueña (%s): %w", security.MigrationDatabaseURLEnv, err)
	}

	// Ambas conexiones deben apuntar al mismo esquema.
	var ownerSchema, appSchema sql.NullString
	if err := ownerDB.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&ownerSchema); err != nil {
		return fmt.Errorf("cloudstore: nexo: esquema de la cuenta dueña: %w", err)
	}
	if err := cs.db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&appSchema); err != nil {
		return fmt.Errorf("cloudstore: nexo: esquema de la cuenta de aplicación: %w", err)
	}
	if ownerSchema.String != "" && appSchema.Valid && ownerSchema.String != appSchema.String {
		return fmt.Errorf("cloudstore: nexo: la cuenta dueña usa el esquema %q y la de aplicación %q (revise search_path)", ownerSchema.String, appSchema.String)
	}

	// 1. Migraciones de Engram y Nexo con la cuenta dueña.
	owner := &CloudStore{db: ownerDB}
	if err := owner.migrate(ctx); err != nil {
		return err
	}

	// 2. Privilegios de la cuenta de aplicación.
	appRole, err := security.ResolveAppRole(ctx, cs.db)
	if err != nil {
		return fmt.Errorf("cloudstore: nexo: %w", err)
	}
	tx, err := ownerDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cloudstore: nexo grants begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, nexoGrantsLockKey); err != nil {
		return fmt.Errorf("cloudstore: nexo grants lock: %w", err)
	}
	report, err := security.ApplyGrants(ctx, tx, appRole)
	if err != nil {
		return fmt.Errorf("cloudstore: nexo: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cloudstore: nexo grants commit: %w", err)
	}

	// 3. Verificación de la cuenta de aplicación: si no es apta, no arranca.
	account, err := security.VerifyAppAccount(ctx, cs.db)
	if err != nil {
		return fmt.Errorf("cloudstore: nexo: %w", err)
	}
	if account.Schema != report.Schema {
		return fmt.Errorf("cloudstore: nexo: la cuenta de aplicación usa el esquema %q y la dueña %q (revise search_path)", account.Schema, report.Schema)
	}
	if problems := account.Problems(); len(problems) > 0 {
		return fmt.Errorf("cloudstore: nexo: la cuenta de aplicación %q no es apta (%s); el servidor no arranca (ver docs/nexo/seguridad)",
			account.Role, strings.Join(problems, "; "))
	}
	for _, w := range account.Warnings() {
		log.Printf("[nexo] seguridad: advertencia: %s", w)
	}
	log.Printf("[nexo] seguridad: dos cuentas de base de datos (dueña %q, aplicación %q, esquema %q); %d tablas de evidencia solo lectura e inserción",
		report.OwnerRole, report.AppRole, report.Schema, len(report.EvidenceProtected))
	return nil
}
