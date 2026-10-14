package cloudstore

// Nexo IA · SG-05 (TLS hacia Postgres) y pool de conexiones.
//
// cloudstore.New y la conexión de la cuenta dueña abren la base con
// nexoOpenDB (enganche // NX-07 en cloudstore.go):
//
//   - antes de abrir, revisa NEXO_DB_REQUIRE_TLS: si está activo y la cadena
//     de conexión no usa sslmode verify-full o verify-ca, devuelve un error
//     que dice por qué y el servidor no arranca;
//   - aplica NEXO_DB_MAX_OPEN_CONNS, NEXO_DB_MAX_IDLE_CONNS,
//     NEXO_DB_CONN_MAX_LIFETIME y NEXO_DB_CONN_MAX_IDLE_TIME al pool de la
//     cuenta de aplicación (valores inválidos = error al arrancar);
//   - recuerda la configuración para el log de arranque (nexoReportConnection)
//     y para GET /nexo/admin/database (NexoDBStatus).
//
// Nunca se guarda ni se imprime la cadena de conexión (lleva la contraseña).

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/features"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// nexoConn es lo que se recuerda de una conexión abierta con nexoOpenDB.
type nexoConn struct {
	name       string // variable de entorno de la conexión
	tls        security.DBTLS
	requireTLS bool
	pool       security.PoolConfig
	poolSet    bool
}

// nexoConns: *sql.DB -> *nexoConn.
var nexoConns sync.Map

// nexoOpenDB abre la base aplicando la política de TLS y, si pool es true, la
// configuración del pool (la cuenta dueña solo migra al arrancar y no la
// necesita).
//
// La cadena se interpreta UNA vez (pgx.ParseConfig) y el pool se abre con
// esa misma configuración (stdlib.OpenDB): así las conexiones nuevas que el
// pool abra más adelante usan el TLS que se validó aquí, aunque cambien el
// archivo de servicio o el entorno mientras el servidor corre.
func nexoOpenDB(name, dsn string, pool bool) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// pgconn ya oculta la contraseña en este error.
		return nil, fmt.Errorf("%s: la cadena de conexión no es válida: %w", name, err)
	}
	tls := security.InspectConfig(&cfg.Config)
	requireTLS, err := security.RequireDBTLSFromEnv()
	if err != nil {
		return nil, err
	}
	if err := tls.Enforce(name, requireTLS); err != nil {
		return nil, err
	}
	conn := &nexoConn{name: name, tls: tls, requireTLS: requireTLS}
	if pool {
		pc, err := security.PoolConfigFromEnv()
		if err != nil {
			return nil, fmt.Errorf("configuración del pool de conexiones: %w", err)
		}
		conn.pool, conn.poolSet = pc, true
		// NX-07: el servidor no arranca con un plazo de activación inválido (nunca se cambia en silencio por el de defecto).
		if _, err := security.ActivationTTLFromEnv(); err != nil {
			return nil, err
		}
		// NX-07: tampoco arranca con un valor inválido de NEXO_SERVICE_ACCOUNTS_ENABLED.
		if _, err := features.ServiceAccountsFromEnv(); err != nil {
			return nil, err
		}
	}
	db := stdlib.OpenDB(*cfg)
	if conn.poolSet {
		conn.pool.Apply(db)
	}
	nexoConns.Store(db, conn)
	return db, nil
}

// nexoReportConnection escribe en el log, una vez conectado, el TLS y el
// pool efectivos. Con NEXO_DB_REQUIRE_TLS, si la propia base dice que la
// conexión no está cifrada, devuelve un error.
func nexoReportConnection(ctx context.Context, db *sql.DB) error {
	v, ok := nexoConns.Load(db)
	if !ok {
		return nil
	}
	conn := v.(*nexoConn)
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	probe, probeErr := security.ProbeSSL(probeCtx, db)

	switch {
	case probeErr != nil:
		log.Printf("[nexo] base de datos: %s: sslmode=%s (no se pudo comprobar el cifrado: %v)", conn.name, conn.tls.SSLMode, probeErr)
	case probe.Encrypted:
		log.Printf("[nexo] base de datos: %s: sslmode=%s, conexion cifrada (%s)%s", conn.name, conn.tls.SSLMode, probe.Protocol, nexoTLSVerifiedNote(conn.tls))
	default:
		if conn.tls.Local {
			log.Printf("[nexo] base de datos: %s: sslmode=%s, conexion sin cifrar (base local)", conn.name, conn.tls.SSLMode)
		} else {
			log.Printf("[nexo] base de datos: %s: sslmode=%s, conexion SIN cifrar", conn.name, conn.tls.SSLMode)
		}
	}
	if w := conn.tls.Warning(conn.name); w != "" {
		log.Printf("[nexo] seguridad: %s", w)
	}
	if conn.requireTLS {
		if probeErr == nil && !probe.Encrypted {
			return fmt.Errorf("%s=true y la base dice que la conexión de %s no está cifrada (sslmode=%s)", security.DBRequireTLSEnv, conn.name, conn.tls.SSLMode)
		}
		log.Printf("[nexo] seguridad: %s=true: %s usa TLS verificado (sslmode=%s)", security.DBRequireTLSEnv, conn.name, conn.tls.SSLMode)
	}
	if conn.poolSet {
		nexoReportServiceAccounts(ctx, db)
	}
	if conn.poolSet {
		log.Printf("[nexo] base de datos: pool de %s: %s", conn.name, conn.pool)
		if !conn.pool.Unlimited() && conn.pool.MaxOpen < security.LowDBMaxOpen {
			log.Printf("[nexo] base de datos: %s=%d es muy bajo: las peticiones esperaran una conexion libre (recomendado: 10 o mas)", security.DBMaxOpenEnv, conn.pool.MaxOpen)
		}
	}
	return nil
}

func nexoTLSVerifiedNote(t security.DBTLS) string {
	if t.Verified {
		return ", servidor verificado"
	}
	return ", servidor SIN verificar"
}

// ─── Estado (GET /nexo/admin/database) ───────────────────────────────────────

// NexoPoolStatus es el estado del pool de conexiones (database/sql, db.Stats).
type NexoPoolStatus struct {
	MaxOpen                int   `json:"max_open"`
	MaxIdle                int   `json:"max_idle"`
	ConnMaxLifetimeSeconds int64 `json:"conn_max_lifetime_seconds"`
	ConnMaxIdleTimeSeconds int64 `json:"conn_max_idle_time_seconds"`
	Open                   int   `json:"open"`
	InUse                  int   `json:"in_use"`
	Idle                   int   `json:"idle"`
	WaitCount              int64 `json:"wait_count"`
	WaitDurationMs         int64 `json:"wait_duration_ms"`
	MaxIdleClosed          int64 `json:"max_idle_closed"`
	MaxIdleTimeClosed      int64 `json:"max_idle_time_closed"`
	MaxLifetimeClosed      int64 `json:"max_lifetime_closed"`
}

// NexoTLSStatus es el estado del TLS hacia la base (no incluye host ni usuario).
type NexoTLSStatus struct {
	SSLMode    string `json:"sslmode"`
	Local      bool   `json:"local"`
	Verified   bool   `json:"verified"`
	Encrypted  bool   `json:"encrypted"`
	Protocol   string `json:"protocol,omitempty"`
	RequireTLS bool   `json:"require_tls"`
	Warning    string `json:"warning,omitempty"`
}

// NexoServerStatus son datos de la base que importan para los requisitos.
type NexoServerStatus struct {
	PostgresVersion string `json:"postgres_version"`
	MaxConnections  int    `json:"max_connections"`
	VectorVersion   string `json:"vector_version,omitempty"`
	TwoAccounts     bool   `json:"two_accounts"`
}

// NexoDBStatus es la respuesta de GET /nexo/admin/database.
type NexoDBStatus struct {
	Pool   NexoPoolStatus   `json:"pool"`
	TLS    NexoTLSStatus    `json:"tls"`
	Server NexoServerStatus `json:"server"`
}

// NexoDBStatus junta el estado del pool, del TLS y de la base. Sin secretos:
// no devuelve la cadena de conexión, el host ni el usuario.
func (cs *CloudStore) NexoDBStatus(ctx context.Context) (NexoDBStatus, error) {
	var st NexoDBStatus
	if cs == nil || cs.db == nil {
		return st, fmt.Errorf("cloudstore: not initialized")
	}
	s := cs.db.Stats()
	st.Pool = NexoPoolStatus{
		MaxOpen: s.MaxOpenConnections, Open: s.OpenConnections, InUse: s.InUse, Idle: s.Idle,
		WaitCount: s.WaitCount, WaitDurationMs: s.WaitDuration.Milliseconds(),
		MaxIdleClosed: s.MaxIdleClosed, MaxIdleTimeClosed: s.MaxIdleTimeClosed, MaxLifetimeClosed: s.MaxLifetimeClosed,
	}
	if v, ok := nexoConns.Load(cs.db); ok {
		conn := v.(*nexoConn)
		st.Pool.MaxIdle = conn.pool.MaxIdle
		st.Pool.ConnMaxLifetimeSeconds = int64(conn.pool.MaxLifetime / time.Second)
		st.Pool.ConnMaxIdleTimeSeconds = int64(conn.pool.MaxIdleTime / time.Second)
		st.TLS = NexoTLSStatus{SSLMode: conn.tls.SSLMode, Local: conn.tls.Local, Verified: conn.tls.Verified,
			RequireTLS: conn.requireTLS, Warning: conn.tls.Warning(conn.name)}
	}
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if probe, err := security.ProbeSSL(qctx, cs.db); err == nil {
		st.TLS.Encrypted, st.TLS.Protocol = probe.Encrypted, probe.Protocol
	}
	var vector sql.NullString
	if err := cs.db.QueryRowContext(qctx, `
		SELECT current_setting('server_version'),
		       current_setting('max_connections')::int,
		       (SELECT extversion FROM pg_extension WHERE extname = 'vector')`).
		Scan(&st.Server.PostgresVersion, &st.Server.MaxConnections, &vector); err != nil {
		return st, fmt.Errorf("cloudstore: nexo db status: %w", err)
	}
	st.Server.VectorVersion = vector.String
	st.Server.TwoAccounts = strings.TrimSpace(os.Getenv(security.MigrationDatabaseURLEnv)) != ""
	return st, nil
}

// NexoPoolWaitCount devuelve cuántas veces una petición esperó una conexión
// libre desde el arranque (el monitor lo compara minuto a minuto).
func (cs *CloudStore) NexoPoolWaitCount() int64 {
	if cs == nil || cs.db == nil {
		return 0
	}
	return cs.db.Stats().WaitCount
}

// nexoReportServiceAccounts advierte al arrancar, con las cuentas de servicio
// apagadas (NEXO_SERVICE_ACCOUNTS_ENABLED=false, el defecto), cuántas cuentas
// habilitadas hay: sus tokens ya no autentican. Solo el número: sin nombres
// ni tokens.
func nexoReportServiceAccounts(ctx context.Context, db *sql.DB) {
	if features.ServiceAccountsEnabled() {
		log.Printf("[nexo] seguridad: %s=true: las cuentas de servicio están activas (el administrador puede emitir sus tokens; riesgo R2, ver SG-17)", features.ServiceAccountsEnv)
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var n int
	if err := db.QueryRowContext(probeCtx, `SELECT count(*) FROM cloud_principals WHERE kind = 'service_account' AND enabled`).Scan(&n); err != nil {
		log.Printf("[nexo] seguridad: cuentas de servicio apagadas (%s=false); no se pudo contar las habilitadas", features.ServiceAccountsEnv)
		return
	}
	if n > 0 {
		log.Printf("[nexo] ADVERTENCIA: %s=false: hay %d cuenta(s) de servicio habilitada(s) y sus tokens NO autentican hasta activar la variable", features.ServiceAccountsEnv, n)
		return
	}
	log.Printf("[nexo] seguridad: cuentas de servicio apagadas (%s=false)", features.ServiceAccountsEnv)
}
