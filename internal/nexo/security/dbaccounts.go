// Package security reúne el endurecimiento de seguridad de Nexo (NX-07).
//
// Fase S1 (base de datos, docs/nexo/seguridad/design.md §2): dos cuentas de
// Postgres (dueña del esquema para migrar, aplicación para operar),
// privilegios mínimos por tabla con tablas de evidencia de solo lectura e
// inserción, verificación de la cuenta de aplicación al iniciar y retención
// mediante una función SECURITY DEFINER de la cuenta dueña.
//
// El paquete no depende de cloudstore ni de cloudserver: recibe conexiones o
// transacciones ya abiertas.
package security

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Variables de entorno de la fase S1.
const (
	// MigrationDatabaseURLEnv es la URL de la cuenta dueña del esquema. Si
	// está definida, las migraciones (Engram y Nexo), los privilegios y las
	// funciones se aplican con ella y ENGRAM_DATABASE_URL queda solo para
	// operar (RS-01).
	MigrationDatabaseURLEnv = "NEXO_MIGRATION_DATABASE_URL"
	// AppRoleEnv permite fijar el rol de aplicación al que se otorgan los
	// privilegios. Por defecto es el usuario de la conexión de aplicación
	// (current_user de ENGRAM_DATABASE_URL).
	AppRoleEnv = "NEXO_DB_APP_ROLE"
)

// DegradedWarning es el aviso al iniciar con una sola cuenta (RS-04, CA-S03).
const DegradedWarning = "seguridad degradada: una sola cuenta de base de datos (ver docs/nexo/seguridad)"

// EvidenceTables son las tablas de evidencia: la cuenta de aplicación solo
// puede leerlas e insertar en ellas (sin UPDATE, DELETE ni TRUNCATE, RS-02).
// La lista sale del inventario de escrituras de Engram (design §2.2): ningún
// camino de ejecución de Engram ni de Nexo actualiza o borra estas tablas; las
// únicas sentencias UPDATE sobre cloud_mutations y cloud_chunks están en
// migrate(), que con dos cuentas corre con la cuenta dueña.
//
// Las tablas que todavía no existen se omiten sin error, así las fases S4
// (alertas, exportaciones, revisiones de acceso) y NX-10 (conocimiento) quedan
// protegidas apenas sus migraciones las crean. Para sumar una tabla nueva basta
// con agregarla aquí.
var EvidenceTables = []string{
	// Engram
	"cloud_auth_audit_log",
	"cloud_sync_audit_log",
	"cloud_mutations",
	"cloud_chunks",
	// NX-02 y NX-05
	"nexo_activity_log",
	"nexo_authz_events",
	// NX-07 fase S4 (internal/nexo/security/evidence)
	"nexo_alerts",
	"nexo_alert_acks",
	"nexo_audit_exports",
	"nexo_access_reviews",
	"nexo_audit_chain_checkpoints",
	// NX-10 conocimiento
	"nexo_kb_events",
	"nexo_kb_versions",
}

// OwnerManagedTables son tablas que solo escribe la cuenta dueña (migración y
// funciones SECURITY DEFINER): la aplicación solo las lee (S4-01: anclas de
// la cadena de hashes). Si la aplicación pudiera escribirlas, podría
// "justificar" un hueco con un ancla falsa.
var OwnerManagedTables = []string{
	"nexo_audit_chain_anchors",
}

// DefinerFunctions son las funciones SECURITY DEFINER que la aplicación puede
// ejecutar (depuraciones con mínimo, S1 y S4-05). Las que no existen se
// omiten.
var DefinerFunctions = []string{
	"nexo_purge_activity(integer)",
	"nexo_purge_evidence(text, integer)",
}

// RetentionMinimumDays es el plazo mínimo de retención (RS-03). La función de
// depuración rechaza plazos menores y el trigger de inmutabilidad nunca deja
// borrar filas más recientes, aunque se active cualquier variable de sesión.
const RetentionMinimumDays = 30

// PurgeFunction es la función SECURITY DEFINER de depuración de la actividad.
const PurgeFunction = "nexo_purge_activity"

// ActionRetentionPurged es la acción del evento que deja cada depuración en
// nexo_authz_events.
const ActionRetentionPurged = "retention.purged"

func quoteIdent(name string) string { return pgx.Identifier{name}.Sanitize() }

// MigrateRetention crea (o reemplaza) la función de depuración segura. Se
// ejecuta dentro de la transacción de migraciones Nexo, con la cuenta que
// migra: esa cuenta queda como dueña de la función. El search_path de la
// función se fija al esquema actual (buena práctica para SECURITY DEFINER).
func MigrateRetention(ctx context.Context, tx *sql.Tx) error {
	var schema sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return fmt.Errorf("security: esquema actual: %w", err)
	}
	if !schema.Valid || schema.String == "" {
		return fmt.Errorf("security: no hay esquema actual (search_path vacío)")
	}
	s := quoteIdent(schema.String)
	queries := []string{
		`CREATE OR REPLACE FUNCTION ` + PurgeFunction + `(retention_days integer) RETURNS bigint
		LANGUAGE plpgsql SECURITY DEFINER SET search_path = ` + s + `, pg_temp AS $$
		DECLARE
			n bigint;
		BEGIN
			IF retention_days IS NULL OR retention_days < ` + fmt.Sprint(RetentionMinimumDays) + ` THEN
				RAISE EXCEPTION 'nexo_retention_minimum: la retención mínima es de ` + fmt.Sprint(RetentionMinimumDays) + ` días (pedido: %)', retention_days
					USING ERRCODE = 'check_violation';
			END IF;
			PERFORM set_config('nexo.purge_in_progress', 'on', true);
			DELETE FROM nexo_activity_log WHERE occurred_at < now() - make_interval(days => retention_days);
			GET DIAGNOSTICS n = ROW_COUNT;
			PERFORM set_config('nexo.purge_in_progress', 'off', true);
			IF n > 0 THEN
				INSERT INTO nexo_authz_events (action, scope_type, scope_key, actor_label, details)
				VALUES ('` + ActionRetentionPurged + `', 'platform', '*', 'sistema',
					jsonb_build_object('table', 'nexo_activity_log', 'retention_days', retention_days,
						'deleted', n, 'session_user', session_user));
			END IF;
			RETURN n;
		END
		$$`,
		`REVOKE ALL ON FUNCTION ` + PurgeFunction + `(integer) FROM PUBLIC`,
	}
	for _, q := range queries {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("security: retención: %w", err)
		}
	}
	return nil
}

// GrantReport resume lo aplicado por ApplyGrants.
type GrantReport struct {
	Schema            string
	OwnerRole         string
	AppRole           string
	EvidenceProtected []string // tablas de evidencia existentes, solo SELECT/INSERT
	EvidenceMissing   []string // tablas de la lista que todavía no existen
	OwnerManaged      []string // tablas solo de lectura para la aplicación (S4-01)
	DatabaseHardened  bool     // se revocó CREATE y TEMPORARY a PUBLIC en la base
}

// ResolveAppRole devuelve el rol de aplicación: NEXO_DB_APP_ROLE si está
// definido, o el usuario de la conexión de aplicación.
func ResolveAppRole(ctx context.Context, appDB *sql.DB) (string, error) {
	if v := strings.TrimSpace(os.Getenv(AppRoleEnv)); v != "" {
		return v, nil
	}
	var role string
	if err := appDB.QueryRowContext(ctx, `SELECT current_user`).Scan(&role); err != nil {
		return "", fmt.Errorf("security: rol de aplicación: %w", err)
	}
	return role, nil
}

// ApplyGrants aplica los privilegios de la cuenta de aplicación (design §2.2).
// Idempotente: revoca todo y vuelve a otorgar lo necesario en cada arranque.
// Se ejecuta con la cuenta dueña dentro de una transacción (con el advisory
// lock de migración para que varias réplicas no choquen).
func ApplyGrants(ctx context.Context, tx *sql.Tx, appRole string) (GrantReport, error) {
	var rep GrantReport
	appRole = strings.TrimSpace(appRole)
	if appRole == "" {
		return rep, fmt.Errorf("security: rol de aplicación vacío")
	}
	var schema sql.NullString
	var ownsSchema, ownsDB, appExists bool
	var database string
	if err := tx.QueryRowContext(ctx, `
		SELECT current_schema(), current_user, current_database(),
			COALESCE((SELECT pg_has_role(current_user, n.nspowner, 'USAGE') FROM pg_namespace n WHERE n.nspname = current_schema()), FALSE),
			(SELECT pg_has_role(current_user, d.datdba, 'USAGE') FROM pg_database d WHERE d.datname = current_database()),
			EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, appRole).
		Scan(&schema, &rep.OwnerRole, &database, &ownsSchema, &ownsDB, &appExists); err != nil {
		return rep, fmt.Errorf("security: privilegios: %w", err)
	}
	if !schema.Valid || schema.String == "" {
		return rep, fmt.Errorf("security: privilegios: no hay esquema actual (search_path vacío)")
	}
	rep.Schema, rep.AppRole = schema.String, appRole
	if !appExists {
		return rep, fmt.Errorf("security: el rol de aplicación %q no existe", appRole)
	}
	if appRole == rep.OwnerRole {
		return rep, fmt.Errorf("security: la cuenta de aplicación y la dueña son la misma (%q); use cuentas distintas o quite %s", appRole, MigrationDatabaseURLEnv)
	}
	if !ownsSchema {
		return rep, fmt.Errorf("security: la cuenta dueña %q no es dueña del esquema %q (ALTER SCHEMA %s OWNER TO %s)", rep.OwnerRole, rep.Schema, rep.Schema, rep.OwnerRole)
	}
	s, app := quoteIdent(rep.Schema), quoteIdent(appRole)

	stmts := []string{
		// Esquema: nadie crea objetos; la aplicación solo lo usa.
		`REVOKE ALL ON SCHEMA ` + s + ` FROM PUBLIC`,
		`REVOKE ALL ON SCHEMA ` + s + ` FROM ` + app,
		`GRANT USAGE ON SCHEMA ` + s + ` TO ` + app,
		// Tablas: lectura y escritura donde Engram lo necesita.
		`REVOKE ALL ON ALL TABLES IN SCHEMA ` + s + ` FROM ` + app,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ` + s + ` TO ` + app,
		`REVOKE ALL ON ALL SEQUENCES IN SCHEMA ` + s + ` FROM ` + app,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA ` + s + ` TO ` + app,
		// Objetos que la cuenta dueña cree después (migraciones futuras).
		`ALTER DEFAULT PRIVILEGES IN SCHEMA ` + s + ` GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO ` + app,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA ` + s + ` GRANT USAGE, SELECT ON SEQUENCES TO ` + app,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return rep, fmt.Errorf("security: privilegios: %s: %w", firstWords(q), err)
		}
	}

	// Vistas: solo lectura.
	views, err := queryStrings(ctx, tx, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('v', 'm') ORDER BY 1`, rep.Schema)
	if err != nil {
		return rep, fmt.Errorf("security: privilegios: vistas: %w", err)
	}
	for _, v := range views {
		if _, err := tx.ExecContext(ctx, `REVOKE INSERT, UPDATE, DELETE ON `+s+`.`+quoteIdent(v)+` FROM `+app); err != nil {
			return rep, fmt.Errorf("security: privilegios: vista %s: %w", v, err)
		}
	}

	// Tablas de evidencia: solo SELECT e INSERT.
	for _, table := range EvidenceTables {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT to_regclass(quote_ident($1) || '.' || quote_ident($2)) IS NOT NULL`, rep.Schema, table).Scan(&exists); err != nil {
			return rep, fmt.Errorf("security: privilegios: %s: %w", table, err)
		}
		if !exists {
			rep.EvidenceMissing = append(rep.EvidenceMissing, table)
			continue
		}
		if _, err := tx.ExecContext(ctx, `REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON `+s+`.`+quoteIdent(table)+` FROM `+app); err != nil {
			return rep, fmt.Errorf("security: privilegios: evidencia %s: %w", table, err)
		}
		rep.EvidenceProtected = append(rep.EvidenceProtected, table)
	}

	// Tablas que solo escribe la dueña (S4-01): la aplicación solo lee.
	for _, table := range OwnerManagedTables {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT to_regclass(quote_ident($1) || '.' || quote_ident($2)) IS NOT NULL`, rep.Schema, table).Scan(&exists); err != nil {
			return rep, fmt.Errorf("security: privilegios: %s: %w", table, err)
		}
		if !exists {
			continue
		}
		if _, err := tx.ExecContext(ctx, `REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON `+s+`.`+quoteIdent(table)+` FROM `+app); err != nil {
			return rep, fmt.Errorf("security: privilegios: %s: %w", table, err)
		}
		rep.OwnerManaged = append(rep.OwnerManaged, table)
	}

	// Depuración: solo por las funciones de la cuenta dueña (S1 y S4-05).
	for _, sig := range DefinerFunctions {
		var hasFn bool
		if err := tx.QueryRowContext(ctx, `SELECT to_regprocedure(quote_ident($1) || '.' || $2) IS NOT NULL`, rep.Schema, sig).Scan(&hasFn); err != nil {
			return rep, fmt.Errorf("security: privilegios: función %s: %w", sig, err)
		}
		if !hasFn {
			continue
		}
		fn := s + `.` + sig
		for _, q := range []string{`REVOKE ALL ON FUNCTION ` + fn + ` FROM PUBLIC`, `GRANT EXECUTE ON FUNCTION ` + fn + ` TO ` + app} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return rep, fmt.Errorf("security: privilegios: %s: %w", firstWords(q), err)
			}
		}
	}

	// Base de datos: si la cuenta dueña es dueña de la base, nadie más crea
	// esquemas ni tablas temporales; la aplicación solo se conecta.
	if ownsDB {
		d := quoteIdent(database)
		for _, q := range []string{
			`REVOKE CREATE, TEMPORARY ON DATABASE ` + d + ` FROM PUBLIC`,
			`REVOKE CREATE, TEMPORARY ON DATABASE ` + d + ` FROM ` + app,
			`GRANT CONNECT ON DATABASE ` + d + ` TO ` + app,
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return rep, fmt.Errorf("security: privilegios: %s: %w", firstWords(q), err)
			}
		}
		rep.DatabaseHardened = true
	}
	return rep, nil
}

// AppAccountReport es el resultado de VerifyAppAccount.
type AppAccountReport struct {
	Role           string
	Schema         string
	Superuser      bool
	CreateRole     bool
	CreateDB       bool
	BypassRLS      bool
	OwnsObjects    []string // objetos del esquema de los que es dueña (o miembro del dueño)
	SchemaCreate   bool
	DatabaseCreate bool
	TempTables     bool
}

// Problems devuelve los motivos por los que la cuenta no sirve como cuenta de
// aplicación (RS-01). Vacío = apta.
func (r AppAccountReport) Problems() []string {
	var p []string
	if r.Superuser {
		p = append(p, "es superusuario")
	}
	if len(r.OwnsObjects) > 0 {
		shown := r.OwnsObjects
		if len(shown) > 5 {
			shown = append(append([]string{}, shown[:5]...), "...")
		}
		p = append(p, "es dueña de tablas ("+strings.Join(shown, ", ")+")")
	}
	if r.CreateRole {
		p = append(p, "tiene CREATEROLE")
	}
	if r.CreateDB {
		p = append(p, "tiene CREATEDB")
	}
	if r.SchemaCreate {
		p = append(p, "puede crear objetos en el esquema "+r.Schema)
	}
	if r.DatabaseCreate {
		p = append(p, "puede crear esquemas en la base")
	}
	return p
}

// Warnings devuelve observaciones que no impiden arrancar.
func (r AppAccountReport) Warnings() []string {
	var w []string
	if r.TempTables {
		w = append(w, "la cuenta de aplicación puede crear tablas temporales (REVOKE TEMPORARY ON DATABASE ... FROM PUBLIC)")
	}
	if r.BypassRLS {
		w = append(w, "la cuenta de aplicación tiene BYPASSRLS")
	}
	return w
}

// VerifyAppAccount comprueba, con la conexión de aplicación, que la cuenta no
// es superusuario, no es dueña de objetos del esquema ni miembro de su dueño y
// no puede crear objetos (design §2.1, punto 3).
func VerifyAppAccount(ctx context.Context, appDB *sql.DB) (AppAccountReport, error) {
	var r AppAccountReport
	var schema sql.NullString
	if err := appDB.QueryRowContext(ctx, `
		SELECT current_user, current_schema(), r.rolsuper, r.rolcreaterole, r.rolcreatedb, r.rolbypassrls,
			COALESCE(has_schema_privilege(current_schema(), 'CREATE'), FALSE),
			has_database_privilege(current_database(), 'CREATE'),
			has_database_privilege(current_database(), 'TEMPORARY')
		FROM pg_roles r WHERE r.rolname = current_user`).
		Scan(&r.Role, &schema, &r.Superuser, &r.CreateRole, &r.CreateDB, &r.BypassRLS, &r.SchemaCreate, &r.DatabaseCreate, &r.TempTables); err != nil {
		return r, fmt.Errorf("security: verificar cuenta de aplicación: %w", err)
	}
	if !schema.Valid || schema.String == "" {
		return r, fmt.Errorf("security: verificar cuenta de aplicación: no hay esquema actual (search_path vacío o sin USAGE)")
	}
	r.Schema = schema.String
	owned, err := queryStrings(ctx, appDB, `
		SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')
		  AND pg_has_role(current_user, c.relowner, 'MEMBER')
		ORDER BY 1`)
	if err != nil {
		return r, fmt.Errorf("security: verificar cuenta de aplicación: %w", err)
	}
	r.OwnsObjects = owned
	return r, nil
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queryStrings(ctx context.Context, q queryer, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func firstWords(q string) string {
	f := strings.Fields(q)
	if len(f) > 4 {
		f = f[:4]
	}
	return strings.Join(f, " ")
}
