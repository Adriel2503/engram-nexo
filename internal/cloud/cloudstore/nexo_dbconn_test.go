package cloudstore

// Nexo IA · SG-05 (TLS hacia Postgres) y pool de conexiones — pruebas de
// cloudstore.New: NEXO_DB_REQUIRE_TLS, variables NEXO_DB_* inválidas, pool
// aplicado y log de arranque sin secretos.

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

const nexoDBTestPassword = "clave-que-no-debe-salir-en-ningun-log"

func nexoDBClearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{security.DBRequireTLSEnv, security.DBMaxOpenEnv, security.DBMaxIdleEnv, security.DBMaxLifetimeEnv, security.DBMaxIdleTimeEnv, security.MigrationDatabaseURLEnv, "PGSSLMODE"} {
		t.Setenv(k, "")
	}
}

func nexoCaptureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// Con NEXO_DB_REQUIRE_TLS=true y una base remota con sslmode=disable el
// servidor no arranca, dice por qué y no llega a conectarse (la base de la
// URL ni siquiera existe).
func TestNexoRequireTLSRefusesRemoteWithoutVerifiedTLS(t *testing.T) {
	nexoDBClearEnv(t)
	t.Setenv(security.DBRequireTLSEnv, "true")
	for _, dsn := range []string{
		"postgres://nexo_app:" + nexoDBTestPassword + "@db.invalido.test:5432/engram_cloud?sslmode=disable",
		"postgres://nexo_app:" + nexoDBTestPassword + "@db.invalido.test:5432/engram_cloud?sslmode=require",
		"postgres://nexo_app:" + nexoDBTestPassword + "@db.invalido.test:5432/engram_cloud",
		"postgres://nexo_app:" + nexoDBTestPassword + "@127.0.0.1:1/engram_cloud?sslmode=disable", // exigir vale también en local
	} {
		start := time.Now()
		cs, err := New(cloud.Config{DSN: dsn})
		if err == nil {
			_ = cs.Close()
			t.Fatalf("debía negarse: %s", strings.ReplaceAll(dsn, nexoDBTestPassword, "***"))
		}
		msg := err.Error()
		if !strings.Contains(msg, security.DBRequireTLSEnv) || !strings.Contains(msg, "verify-full") || !strings.Contains(msg, "ENGRAM_DATABASE_URL") {
			t.Errorf("el error debe decir por qué: %v", err)
		}
		if strings.Contains(msg, nexoDBTestPassword) {
			t.Errorf("el error trae la contraseña: %v", err)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("debe negarse antes de conectar (tardó %s)", time.Since(start))
		}
	}
}

func TestNexoRequireTLSInvalidValue(t *testing.T) {
	nexoDBClearEnv(t)
	t.Setenv(security.DBRequireTLSEnv, "tal-vez")
	_, err := New(cloud.Config{DSN: "postgres://u:p@127.0.0.1:1/x?sslmode=disable"})
	if err == nil || !strings.Contains(err.Error(), security.DBRequireTLSEnv) {
		t.Fatalf("valor inválido: %v", err)
	}
}

func TestNexoPoolInvalidEnvFailsToStart(t *testing.T) {
	nexoDBClearEnv(t)
	for env, val := range map[string]string{
		security.DBMaxOpenEnv: "cero", security.DBMaxIdleEnv: "-1", security.DBMaxLifetimeEnv: "mucho", security.DBMaxIdleTimeEnv: "0s",
	} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, val)
			_, err := New(cloud.Config{DSN: "postgres://u:p@127.0.0.1:1/x?sslmode=disable"})
			if err == nil || !strings.Contains(err.Error(), env) {
				t.Fatalf("%s=%s: %v", env, val, err)
			}
		})
	}
	t.Run("reposo mayor que el máximo", func(t *testing.T) {
		t.Setenv(security.DBMaxOpenEnv, "4")
		t.Setenv(security.DBMaxIdleEnv, "9")
		_, err := New(cloud.Config{DSN: "postgres://u:p@127.0.0.1:1/x?sslmode=disable"})
		if err == nil || !strings.Contains(err.Error(), security.DBMaxIdleEnv) {
			t.Fatalf("idle > open: %v", err)
		}
	})
}

func nexoDBSchemaDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set — skipping integration test (requires Postgres)")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN")
	}
	schema := fmt.Sprintf("nexo_dbconn_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err := admin.ExecContext(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "search_path=" + schema
}

// Por defecto el pool queda en 25/10/30m/5m; el log lo dice y el estado lo
// devuelve. Con valores propios se aplican a database/sql.
func TestNexoPoolDefaultsAndOverrides(t *testing.T) {
	dsn := nexoDBSchemaDSN(t)
	nexoDBClearEnv(t)
	buf := nexoCaptureLog(t)

	cs, err := New(cloud.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	st, err := cs.NexoDBStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Pool.MaxOpen != 25 || st.Pool.MaxIdle != 10 || st.Pool.ConnMaxLifetimeSeconds != 1800 || st.Pool.ConnMaxIdleTimeSeconds != 300 {
		t.Errorf("por defecto: %+v", st.Pool)
	}
	if st.Server.PostgresVersion == "" || st.Server.MaxConnections < 1 {
		t.Errorf("servidor: %+v", st.Server)
	}
	if st.TLS.SSLMode == "" || st.TLS.RequireTLS {
		t.Errorf("tls: %+v", st.TLS)
	}
	_ = cs.Close()
	logText := buf.String()
	for _, want := range []string{"pool de ENGRAM_DATABASE_URL", "abiertas como maximo 25", "en reposo 10", "vida maxima 30m0s", "reposo maximo 5m0s", "sslmode="} {
		if !strings.Contains(logText, want) {
			t.Errorf("el log de arranque no dice %q:\n%s", want, logText)
		}
	}
	if strings.Contains(logText, "ADVERTENCIA") {
		t.Errorf("una base local no avisa:\n%s", logText)
	}

	t.Setenv(security.DBMaxOpenEnv, "7")
	t.Setenv(security.DBMaxIdleEnv, "3")
	t.Setenv(security.DBMaxLifetimeEnv, "10m")
	t.Setenv(security.DBMaxIdleTimeEnv, "90s")
	buf.Reset()
	cs, err = New(cloud.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	st, err = cs.NexoDBStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Pool.MaxOpen != 7 || st.Pool.MaxIdle != 3 || st.Pool.ConnMaxLifetimeSeconds != 600 || st.Pool.ConnMaxIdleTimeSeconds != 90 {
		t.Errorf("valores propios: %+v", st.Pool)
	}
	if cs.NexoDB().Stats().MaxOpenConnections != 7 {
		t.Errorf("database/sql no recibió el máximo: %+v", cs.NexoDB().Stats())
	}
	if !strings.Contains(buf.String(), "abiertas como maximo 7") {
		t.Errorf("log:\n%s", buf.String())
	}
}

// El log de arranque no trae la cadena de conexión ni su contraseña.
func TestNexoStartupLogHasNoPassword(t *testing.T) {
	dsn := nexoDBSchemaDSN(t)
	nexoDBClearEnv(t)
	buf := nexoCaptureLog(t)
	cs, err := New(cloud.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	_ = cs.Close()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if pw, ok := u.User.Password(); ok && pw != "" && strings.Contains(buf.String(), pw) {
		t.Errorf("el log trae la contraseña de la base")
	}
	if strings.Contains(buf.String(), "://") {
		t.Errorf("el log no debe traer cadenas de conexión:\n%s", buf.String())
	}
}

// Regresión (revisión adversarial): el pool usa la configuración que se validó
// al abrir, no vuelve a leer el archivo de servicio en cada conexión nueva. Se
// abre con un servicio sslmode=disable, se cambia el archivo a verify-full
// contra un host inexistente y se fuerza una conexión nueva: si el pool
// releyera el archivo, la consulta fallaría.
func TestNexoOpenDBBindsValidatedConfig(t *testing.T) {
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set — skipping integration test (requires Postgres)")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN")
	}
	nexoDBClearEnv(t)
	pass, _ := u.User.Password()
	dir := t.TempDir()
	svc := dir + "/pg_service.conf"
	write := func(mode, host string) {
		t.Helper()
		body := fmt.Sprintf("[nexo]\nhost=%s\nport=%s\nuser=%s\npassword=%s\ndbname=%s\nsslmode=%s\n",
			host, u.Port(), u.User.Username(), pass, strings.TrimPrefix(u.Path, "/"), mode)
		if err := os.WriteFile(svc, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("disable", u.Hostname())
	t.Setenv("PGSERVICEFILE", svc)
	t.Setenv("PGSERVICE", "")

	db, err := nexoOpenDB("ENGRAM_DATABASE_URL", "service=nexo", false)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxIdleConns(0) // cada consulta abre una conexión nueva
	var one int
	if err := db.QueryRow(`SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("primera consulta: %v", err)
	}

	write("verify-full", "db.invalido.test")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("la conexión nueva debe usar la configuración validada al abrir, no la del archivo cambiado: %v", err)
	}
}
