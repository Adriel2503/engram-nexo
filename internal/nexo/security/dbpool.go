package security

// Nexo IA · pool de conexiones a Postgres.
//
// El servidor abre un solo *sql.DB con la cuenta de aplicación. Go, sin
// configuración, no limita las conexiones abiertas, deja 2 en reposo y no las
// recicla nunca: con mucho tráfico puede agotar max_connections del Postgres
// (sobre todo uno administrado, con un tope bajo) y no recupera conexiones
// que un firewall o un balanceador cortó en silencio. Estas variables fijan
// valores razonables por defecto:
//
//	NEXO_DB_MAX_OPEN_CONNS     25   conexiones abiertas como máximo por réplica
//	NEXO_DB_MAX_IDLE_CONNS     10   conexiones en reposo que se conservan
//	NEXO_DB_CONN_MAX_LIFETIME  30m  vida máxima de una conexión
//	NEXO_DB_CONN_MAX_IDLE_TIME 5m   tiempo máximo en reposo de una conexión
//
// Un valor inválido (no entero positivo, duración que no se entiende o
// menor o igual que cero, reposo mayor que el máximo) hace que el servidor
// no arranque, con un mensaje que nombra la variable.

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Variables del pool.
const (
	DBMaxOpenEnv     = "NEXO_DB_MAX_OPEN_CONNS"
	DBMaxIdleEnv     = "NEXO_DB_MAX_IDLE_CONNS"
	DBMaxLifetimeEnv = "NEXO_DB_CONN_MAX_LIFETIME"
	DBMaxIdleTimeEnv = "NEXO_DB_CONN_MAX_IDLE_TIME"
)

// Valores por defecto y topes.
const (
	DefaultDBMaxOpen     = 25
	DefaultDBMaxIdle     = 10
	DefaultDBMaxLifetime = 30 * time.Minute
	DefaultDBMaxIdleTime = 5 * time.Minute
	// maxDBConns es un tope de cordura (un error de tecleo, no un límite real).
	maxDBConns = 10000
	// LowDBMaxOpen: por debajo de esto el log avisa de que las peticiones
	// pueden esperar una conexión libre.
	LowDBMaxOpen = 5
)

// PoolConfig son los valores efectivos del pool.
type PoolConfig struct {
	MaxOpen     int
	MaxIdle     int
	MaxLifetime time.Duration
	MaxIdleTime time.Duration
}

// DefaultPoolConfig son los valores por defecto.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{MaxOpen: DefaultDBMaxOpen, MaxIdle: DefaultDBMaxIdle, MaxLifetime: DefaultDBMaxLifetime, MaxIdleTime: DefaultDBMaxIdleTime}
}

// PoolConfigFromEnv lee las cuatro variables. Si alguna es inválida devuelve
// un error que las lista todas.
func PoolConfigFromEnv() (PoolConfig, error) { return ParsePoolConfig(os.Getenv) }

// ParsePoolConfig es PoolConfigFromEnv con el origen de las variables
// inyectado (pruebas).
func ParsePoolConfig(get func(string) string) (PoolConfig, error) {
	cfg := DefaultPoolConfig()
	var errs []error
	openSet, idleSet := false, false

	intVar := func(name string, dst *int) bool {
		raw := strings.TrimSpace(get(name))
		if raw == "" {
			return false
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxDBConns {
			errs = append(errs, fmt.Errorf("%s=%q no es válido: debe ser un entero entre 1 y %d", name, raw, maxDBConns))
			return false
		}
		*dst = n
		return true
	}
	durVar := func(name string, dst *time.Duration) {
		raw := strings.TrimSpace(get(name))
		if raw == "" {
			return
		}
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s=%q no es válido: debe ser una duración positiva como 30m, 90s o 1h", name, raw))
			return
		}
		*dst = d
	}

	openSet = intVar(DBMaxOpenEnv, &cfg.MaxOpen)
	idleSet = intVar(DBMaxIdleEnv, &cfg.MaxIdle)
	durVar(DBMaxLifetimeEnv, &cfg.MaxLifetime)
	durVar(DBMaxIdleTimeEnv, &cfg.MaxIdleTime)

	// Si solo se bajó el máximo, el valor de reposo por defecto no puede
	// superarlo: se ajusta sin error. Si el reposo se fijó a mano y supera el
	// máximo, es un error.
	if openSet && !idleSet && cfg.MaxIdle > cfg.MaxOpen {
		cfg.MaxIdle = cfg.MaxOpen
	}
	if len(errs) == 0 && cfg.MaxIdle > cfg.MaxOpen {
		errs = append(errs, fmt.Errorf("%s=%d no puede ser mayor que %s=%d", DBMaxIdleEnv, cfg.MaxIdle, DBMaxOpenEnv, cfg.MaxOpen))
	}
	if len(errs) > 0 {
		return DefaultPoolConfig(), errors.Join(errs...)
	}
	return cfg, nil
}

// Apply fija los valores en el pool.
func (c PoolConfig) Apply(db *sql.DB) {
	db.SetMaxOpenConns(c.MaxOpen)
	db.SetMaxIdleConns(c.MaxIdle)
	db.SetConnMaxLifetime(c.MaxLifetime)
	db.SetConnMaxIdleTime(c.MaxIdleTime)
}

// String resume los valores para el log de arranque.
func (c PoolConfig) String() string {
	return fmt.Sprintf("abiertas como maximo %d (%s), en reposo %d (%s), vida maxima %s (%s), reposo maximo %s (%s)",
		c.MaxOpen, DBMaxOpenEnv, c.MaxIdle, DBMaxIdleEnv, c.MaxLifetime, DBMaxLifetimeEnv, c.MaxIdleTime, DBMaxIdleTimeEnv)
}
