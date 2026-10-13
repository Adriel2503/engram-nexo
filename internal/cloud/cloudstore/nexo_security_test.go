package cloudstore

// Nexo IA · NX-07 fase S1 — pruebas contra Postgres real con dos cuentas
// (CA-S01, CA-S02, CA-S03). Los roles de prueba se crean con la cuenta de
// CLOUDSTORE_TEST_DSN (superusuario del contenedor de pruebas) y se borran al
// terminar.

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"github.com/jackc/pgx/v5/pgconn"
)

type nexoTwoAccountEnv struct {
	admin    *sql.DB // superusuario de pruebas, con search_path al esquema
	schema   string
	owner    string
	app      string
	ownerDSN string
	appDSN   string
	baseDSN  string
}

func nexoRandomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

func nexoDSNWith(t *testing.T, base, user, password, schema string) string {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	if user != "" {
		u.User = url.UserPassword(user, password)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func newNexoTwoAccountEnv(t *testing.T) *nexoTwoAccountEnv {
	t.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set — skipping integration test (requires Postgres)")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN")
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	e := &nexoTwoAccountEnv{
		schema:  "nexo_sec_" + suffix,
		owner:   "nexo_owner_t" + suffix,
		app:     "nexo_app_t" + suffix,
		baseDSN: dsn,
	}
	password := nexoRandomHex(t, 16) // solo hexadecimal: seguro dentro del literal SQL
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	ctx := context.Background()
	for _, q := range []string{
		`CREATE ROLE ` + e.owner + ` LOGIN PASSWORD '` + password + `' NOSUPERUSER NOCREATEDB NOCREATEROLE`,
		`CREATE ROLE ` + e.app + ` LOGIN PASSWORD '` + password + `' NOSUPERUSER NOCREATEDB NOCREATEROLE`,
		`CREATE SCHEMA ` + e.schema + ` AUTHORIZATION ` + e.owner,
	} {
		if _, err := admin.ExecContext(ctx, q); err != nil {
			_ = admin.Close()
			t.Fatalf("setup %q: %v", strings.Fields(q)[0]+" "+strings.Fields(q)[1], err)
		}
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+e.schema+` CASCADE`)
		_, _ = admin.ExecContext(ctx, `DROP OWNED BY `+e.owner+`, `+e.app)
		_, _ = admin.ExecContext(ctx, `DROP ROLE IF EXISTS `+e.app)
		_, _ = admin.ExecContext(ctx, `DROP ROLE IF EXISTS `+e.owner)
		_ = admin.Close()
	})
	e.ownerDSN = nexoDSNWith(t, dsn, e.owner, password, e.schema)
	e.appDSN = nexoDSNWith(t, dsn, e.app, password, e.schema)
	e.admin = admin
	return e
}

// open crea el CloudStore como lo hace el servidor con dos cuentas.
func (e *nexoTwoAccountEnv) open(t *testing.T) *CloudStore {
	t.Helper()
	t.Setenv(security.MigrationDatabaseURLEnv, e.ownerDSN)
	cs, err := New(cloud.Config{DSN: e.appDSN})
	if err != nil {
		t.Fatalf("New con dos cuentas: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (e *nexoTwoAccountEnv) q(table string) string { return e.schema + "." + table }

func nexoExpectDenied(t *testing.T, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, stmt string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), stmt)
	if err == nil {
		t.Fatalf("%s: se esperaba rechazo por privilegios y se ejecutó", stmt)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("%s: se esperaba SQLSTATE 42501 (insufficient_privilege), se obtuvo %v", stmt, err)
	}
}

func nexoRecordN(t *testing.T, cs *CloudStore, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := cs.RecordActivity(context.Background(), ActivityEvent{Actor: MutationActor{}, Operation: NexoOpPushChunk, Project: "p", LogDetail: true}); err != nil {
			t.Fatalf("RecordActivity: %v", err)
		}
	}
}

// CA-S01 y RS-01/RS-02: con dos cuentas la base queda en manos de la dueña y
// la aplicación no puede modificar la evidencia ni crear objetos.
func TestNexoTwoAccountsAppCannotTamperEvidence(t *testing.T) {
	e := newNexoTwoAccountEnv(t)
	cs := e.open(t)
	ctx := context.Background()

	// Todas las tablas del esquema son de la cuenta dueña.
	var total, foreign int
	if err := e.admin.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE tableowner <> $2) FROM pg_tables WHERE schemaname = $1`, e.schema, e.owner).Scan(&total, &foreign); err != nil {
		t.Fatalf("owners: %v", err)
	}
	if total < 15 || foreign != 0 {
		t.Fatalf("tablas=%d, de otra cuenta=%d; se esperaba todo de %s", total, foreign, e.owner)
	}

	// La aplicación opera: inserta actividad y actualiza la presencia (UPDATE
	// en una tabla que no es de evidencia).
	nexoRecordN(t, cs, 2)
	if _, err := cs.db.ExecContext(ctx, `UPDATE cloud_principals SET updated_at = updated_at WHERE FALSE`); err != nil {
		t.Fatalf("UPDATE en tabla operativa debe estar permitido: %v", err)
	}

	protected := 0
	for _, table := range security.EvidenceTables {
		var exists bool
		if err := e.admin.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, e.q(table)).Scan(&exists); err != nil {
			t.Fatalf("to_regclass: %v", err)
		}
		if !exists {
			continue
		}
		protected++
		var col string
		if err := e.admin.QueryRowContext(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 ORDER BY ordinal_position LIMIT 1`, e.schema, table).Scan(&col); err != nil {
			t.Fatalf("columna de %s: %v", table, err)
		}
		var n int
		if err := cs.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("SELECT en %s debe estar permitido: %v", table, err)
		}
		// Intentar darse el privilegio no sirve (no es dueña ni tiene GRANT OPTION).
		_, _ = cs.db.ExecContext(ctx, `GRANT UPDATE, DELETE, TRUNCATE ON `+table+` TO `+e.app)
		for _, stmt := range []string{
			`UPDATE ` + table + ` SET ` + col + ` = ` + col,
			`DELETE FROM ` + table,
			`TRUNCATE ` + table,
			`ALTER TABLE ` + table + ` DISABLE TRIGGER ALL`,
			`ALTER TABLE ` + table + ` OWNER TO ` + e.app,
			`DROP TABLE ` + table,
		} {
			nexoExpectDenied(t, cs.db, stmt)
		}
	}
	if protected < 6 {
		t.Fatalf("tablas de evidencia existentes = %d, se esperaban al menos 6", protected)
	}

	// No puede crear objetos ni tomar el rol de la dueña.
	for _, stmt := range []string{
		`CREATE TABLE nexo_intruso (id int)`,
		`CREATE VIEW nexo_intruso_v AS SELECT 1 AS x`,
		`CREATE FUNCTION nexo_intruso_f() RETURNS int LANGUAGE sql AS 'SELECT 1'`,
		`CREATE SCHEMA nexo_intruso_` + strings.TrimPrefix(e.schema, "nexo_sec_"),
		`SET ROLE ` + e.owner,
		`CREATE OR REPLACE FUNCTION nexo_activity_log_immutable() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RETURN OLD; END'`,
		`CREATE OR REPLACE FUNCTION nexo_purge_activity(retention_days integer) RETURNS bigint LANGUAGE sql AS 'SELECT 0::bigint'`,
	} {
		nexoExpectDenied(t, cs.db, stmt)
	}

	// Las filas siguen ahí.
	var rows int
	if err := e.admin.QueryRowContext(ctx, `SELECT count(*) FROM `+e.q("nexo_activity_log")).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("filas de actividad = %d, %v; want 2", rows, err)
	}
}

// CA-S02: la depuración borra solo lo más antiguo que el plazo (mínimo 30
// días) por la función de la cuenta dueña; ninguna variable de sesión permite
// borrar filas recientes.
func TestNexoTwoAccountsRetentionIsSafe(t *testing.T) {
	e := newNexoTwoAccountEnv(t)
	cs := e.open(t)
	ctx := context.Background()
	nexoRecordN(t, cs, 3)

	// Envejecer dos filas (200 y 40 días) con el superusuario de pruebas.
	tx, err := e.admin.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, q := range []string{
		`ALTER TABLE ` + e.q("nexo_activity_log") + ` DISABLE TRIGGER nexo_activity_log_no_update_delete`,
		`UPDATE ` + e.q("nexo_activity_log") + ` SET occurred_at = NOW() - INTERVAL '200 days' WHERE id = (SELECT min(id) FROM ` + e.q("nexo_activity_log") + `)`,
		`UPDATE ` + e.q("nexo_activity_log") + ` SET occurred_at = NOW() - INTERVAL '40 days' WHERE id = (SELECT min(id) + 1 FROM ` + e.q("nexo_activity_log") + `)`,
		`ALTER TABLE ` + e.q("nexo_activity_log") + ` ENABLE TRIGGER nexo_activity_log_no_update_delete`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			_ = tx.Rollback()
			t.Fatalf("envejecer: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// La aplicación, aun activando las variables de sesión (la antigua y la
	// nueva), no puede borrar: no tiene privilegio DELETE.
	conn, err := cs.db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT set_config('nexo.allow_retention', 'on', false), set_config('nexo.purge_in_progress', 'on', false)`); err != nil {
		t.Fatalf("set_config: %v", err)
	}
	nexoExpectDenied(t, conn, `DELETE FROM nexo_activity_log`)
	_ = conn.Close()

	// Plazo menor al mínimo: rechazado.
	if _, err := cs.PurgeActivityOlderThan(ctx, 10); err == nil || !strings.Contains(err.Error(), "nexo_retention_minimum") {
		t.Fatalf("purge(10) debe rechazarse por el mínimo, got %v", err)
	}
	// 180 días: borra solo la fila de 200 días y deja un evento.
	if n, err := cs.PurgeActivityOlderThan(ctx, 180); err != nil || n != 1 {
		t.Fatalf("purge(180) = %d, %v; want 1", n, err)
	}
	var deleted, days, sessionUser string
	if err := cs.db.QueryRowContext(ctx, `SELECT details->>'deleted', details->>'retention_days', details->>'session_user' FROM nexo_authz_events WHERE action = $1 ORDER BY id DESC LIMIT 1`, security.ActionRetentionPurged).Scan(&deleted, &days, &sessionUser); err != nil {
		t.Fatalf("evento de depuración: %v", err)
	}
	if deleted != "1" || days != "180" || sessionUser != e.app {
		t.Fatalf("evento = deleted %s, days %s, session_user %s", deleted, days, sessionUser)
	}
	// 30 días (el mínimo): borra la de 40 días; la reciente queda.
	if n, err := cs.PurgeActivityOlderThan(ctx, 30); err != nil || n != 1 {
		t.Fatalf("purge(30) = %d, %v; want 1", n, err)
	}
	var left int
	if err := cs.db.QueryRowContext(ctx, `SELECT count(*) FROM nexo_activity_log`).Scan(&left); err != nil || left != 1 {
		t.Fatalf("filas restantes = %d, %v; want 1", left, err)
	}

	// Ni siquiera la cuenta dueña borra filas recientes, con cualquier variable.
	ownerDB, err := sql.Open("pgx", e.ownerDSN)
	if err != nil {
		t.Fatalf("open owner: %v", err)
	}
	defer ownerDB.Close()
	otx, err := ownerDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("owner begin: %v", err)
	}
	defer func() { _ = otx.Rollback() }()
	if _, err := otx.ExecContext(ctx, `SELECT set_config('nexo.allow_retention', 'on', true), set_config('nexo.purge_in_progress', 'on', true)`); err != nil {
		t.Fatalf("owner set_config: %v", err)
	}
	if _, err := otx.ExecContext(ctx, `DELETE FROM nexo_activity_log`); err == nil || !strings.Contains(err.Error(), "solo inserción") {
		t.Fatalf("la dueña no debe poder borrar filas recientes: %v", err)
	}
}

// RS-03 con una sola cuenta: se mantiene la comprobación de antigüedad.
func TestNexoSingleAccountRetentionKeepsAgeCheck(t *testing.T) {
	t.Setenv(security.MigrationDatabaseURLEnv, "")
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	nexoRecordN(t, cs, 2)

	conn, err := cs.db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT set_config('nexo.allow_retention', 'on', false), set_config('nexo.purge_in_progress', 'on', false)`); err != nil {
		t.Fatalf("set_config: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM nexo_activity_log`); err == nil || !strings.Contains(err.Error(), "solo inserción") {
		t.Fatalf("con variables de sesión activas, DELETE de filas recientes debe fallar: %v", err)
	}
	if _, err := cs.PurgeActivityOlderThan(ctx, 29); err == nil {
		t.Fatalf("purge(29) debe rechazarse por el mínimo")
	}
	if n, err := cs.PurgeActivityOlderThan(ctx, 30); err != nil || n != 0 {
		t.Fatalf("purge(30) sobre filas recientes = %d, %v; want 0", n, err)
	}
	if n := nexoActivityCount(t, cs, `TRUE`); n != 2 {
		t.Fatalf("filas = %d, want 2", n)
	}
}

// Design §2.1 punto 3: con dos cuentas, si la cuenta de aplicación es
// superusuario, dueña de tablas (o miembro de la dueña) o es la misma cuenta,
// el servidor no arranca.
func TestNexoTwoAccountsRejectsUnsafeAppAccount(t *testing.T) {
	e := newNexoTwoAccountEnv(t)
	ctx := context.Background()

	t.Run("superusuario", func(t *testing.T) {
		t.Setenv(security.MigrationDatabaseURLEnv, e.ownerDSN)
		cs, err := New(cloud.Config{DSN: nexoDSNWith(t, e.baseDSN, "", "", e.schema)})
		if err == nil {
			_ = cs.Close()
			t.Fatalf("una cuenta de aplicación superusuario no debe arrancar")
		}
		if !strings.Contains(err.Error(), "superusuario") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("misma cuenta", func(t *testing.T) {
		t.Setenv(security.MigrationDatabaseURLEnv, e.ownerDSN)
		cs, err := New(cloud.Config{DSN: e.ownerDSN})
		if err == nil {
			_ = cs.Close()
			t.Fatalf("la misma cuenta para migrar y operar no debe arrancar con %s", security.MigrationDatabaseURLEnv)
		}
		if !strings.Contains(err.Error(), "misma") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("miembro de la dueña", func(t *testing.T) {
		if _, err := e.admin.ExecContext(ctx, `GRANT `+e.owner+` TO `+e.app); err != nil {
			t.Fatalf("grant role: %v", err)
		}
		defer func() { _, _ = e.admin.ExecContext(ctx, `REVOKE `+e.owner+` FROM `+e.app) }()
		t.Setenv(security.MigrationDatabaseURLEnv, e.ownerDSN)
		cs, err := New(cloud.Config{DSN: e.appDSN})
		if err == nil {
			_ = cs.Close()
			t.Fatalf("una cuenta miembro de la dueña no debe arrancar")
		}
		if !strings.Contains(err.Error(), "dueña de tablas") {
			t.Fatalf("error = %v", err)
		}
	})
}

// Idempotencia y lista extensible: un segundo arranque vuelve a aplicar los
// privilegios sin error y protege tablas de evidencia creadas después (por
// ejemplo, las de NX-10).
func TestNexoTwoAccountsGrantsAreIdempotentAndExtensible(t *testing.T) {
	e := newNexoTwoAccountEnv(t)
	_ = e.open(t)
	ctx := context.Background()

	// Una migración futura (con la cuenta dueña) crea nexo_alerts (fase S4).
	// NX-10: antes era nexo_kb_events, que ahora crea la migración real.
	tx, err := e.admin.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, q := range []string{
		`SET LOCAL ROLE ` + e.owner,
		`CREATE TABLE ` + e.q("nexo_alerts") + ` (id BIGSERIAL PRIMARY KEY, action TEXT NOT NULL)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			_ = tx.Rollback()
			t.Fatalf("crear tabla futura: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	cs := e.open(t) // segundo arranque
	if _, err := cs.db.ExecContext(ctx, `INSERT INTO nexo_alerts (action) VALUES ('alert.raised')`); err != nil {
		t.Fatalf("INSERT en evidencia nueva debe estar permitido: %v", err)
	}
	nexoExpectDenied(t, cs.db, `UPDATE nexo_alerts SET action = 'x'`)
	nexoExpectDenied(t, cs.db, `DELETE FROM nexo_alerts`)
	nexoExpectDenied(t, cs.db, `TRUNCATE nexo_alerts`)
}

// CA-S03: con una sola cuenta el servidor arranca y avisa.
func TestNexoSingleAccountDegradedWarning(t *testing.T) {
	t.Setenv(security.MigrationDatabaseURLEnv, "")
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	cs := openIsolatedCloudStore(t)
	log.SetOutput(prev)
	if !strings.Contains(buf.String(), security.DegradedWarning) {
		t.Fatalf("falta el aviso de seguridad degradada; log: %q", buf.String())
	}
	nexoRecordN(t, cs, 1)
}

// El mínimo del trigger coincide con el de la función y de la configuración.
func TestNexoRetentionMinimumIsConsistent(t *testing.T) {
	joined := strings.Join(nexoMigrationQueries, "\n")
	want := fmt.Sprintf("make_interval(days => %d)", security.RetentionMinimumDays)
	if !strings.Contains(joined, want) {
		t.Fatalf("el trigger nexo_activity_log_immutable debe usar %s", want)
	}
	if strings.Contains(joined, "allow_retention") {
		t.Fatalf("el bypass nexo.allow_retention debe estar eliminado")
	}
}
