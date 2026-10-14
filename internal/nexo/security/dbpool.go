package security

// Nexo IA · pool de conexiones a Postgres.
//
// El servidor abre un solo *sql.DB con la cuenta de aplicación. Go, sin
// configuración, deja 2 conexiones en reposo y no las recicla nunca (no
// recupera conexiones que un firewall o un balanceador cortó en silencio).
// Estas variables fijan valores por defecto:
//
//	NEXO_DB_MAX_OPEN_CONNS     0    sin límite (el tope es max_connections de Postgres)
//	NEXO_DB_MAX_IDLE_CONNS     10   conexiones en reposo que se conservan
//	NEXO_DB_CONN_MAX_LIFETIME  30m  vida máxima de una conexión
//	NEXO_DB_CONN_MAX_IDLE_TIME 5m   tiempo máximo en reposo de una conexión
//
// Por qué sin límite por defecto (OP-13, backlog): hay código, de Engram y de
// Nexo, que con una transacción o un cursor abiertos pide otra conexión del
// mismo pool (relleno de sesiones al migrar, WriteChunk ante un choque,
// publicación de conocimiento con la caché del modo sin tildes fría,
// VectorSupport). Con un límite, varias de esas operaciones a la vez pueden
// ocupar todas las conexiones esperando otra y colgar el servidor. Sin
// límite, al llegar a max_connections de Postgres las peticiones nuevas
// fallan con error y el resto sigue. Fijar un límite exige antes la revisión
// completa de esas conexiones anidadas (OP-13).
//
// Un valor inválido (entero fuera de rango, un límite menor que el mínimo,
// duración que no se entiende o menor o igual que cero, reposo mayor que el
// máximo) hace que el servidor no arranque, con un mensaje que nombra la
// variable.

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
	DefaultDBMaxOpen     = 0 // sin límite (ver arriba y OP-13)
	DefaultDBMaxIdle     = 10
	DefaultDBMaxLifetime = 30 * time.Minute
	DefaultDBMaxIdleTime = 5 * time.Minute
	// maxDBConns es un tope de cordura (un error de tecleo, no un límite real).
	maxDBConns = 10000
	// LowDBMaxOpen: por debajo de esto el log avisa de que las peticiones
	// pueden esperar una conexión libre.
	LowDBMaxOpen = 5
	// MinDBMaxOpen: mínimo aceptado. Hay código (por ejemplo el relleno de
	// sesiones de Engram al migrar, cloudstore.go) que mantiene un cursor
	// abierto y pide otra conexión del mismo pool: con 1 el arranque se
	// bloquea para siempre. 4 deja margen para esos anidamientos.
	MinDBMaxOpen = 4
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

	intVar := func(name string, dst *int, min int) bool {
		raw := strings.TrimSpace(get(name))
		if raw == "" {
			return false
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < min || n > maxDBConns {
			errs = append(errs, fmt.Errorf("%s=%q no es válido: debe ser un entero entre %d y %d", name, raw, min, maxDBConns))
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

	openSet = intVar(DBMaxOpenEnv, &cfg.MaxOpen, 0) // 0 = sin límite
	idleSet = intVar(DBMaxIdleEnv, &cfg.MaxIdle, 1)
	durVar(DBMaxLifetimeEnv, &cfg.MaxLifetime)
	durVar(DBMaxIdleTimeEnv, &cfg.MaxIdleTime)

	// Un límite explícito no puede ser menor que el mínimo. Si solo se bajó
	// el máximo, el valor de reposo por defecto no puede superarlo: se ajusta
	// sin error. Si el reposo se fijó a mano y supera el máximo, es un error.
	if openSet && cfg.MaxOpen > 0 && cfg.MaxOpen < MinDBMaxOpen {
		errs = append(errs, fmt.Errorf("%s=%d no es válido: use 0 (sin límite) o al menos %d (con menos, una migración que anida consultas puede bloquear el arranque)", DBMaxOpenEnv, cfg.MaxOpen, MinDBMaxOpen))
	}
	if cfg.Unlimited() {
		if len(errs) > 0 {
			return DefaultPoolConfig(), errors.Join(errs...)
		}
		return cfg, nil
	}
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

// Unlimited informa si el pool no limita las conexiones abiertas.
func (c PoolConfig) Unlimited() bool { return c.MaxOpen <= 0 }

// String resume los valores para el log de arranque.
func (c PoolConfig) String() string {
	open := strconv.Itoa(c.MaxOpen)
	if c.Unlimited() {
		open = "sin limite"
	}
	return fmt.Sprintf("abiertas como maximo %s (%s), en reposo %d (%s), vida maxima %s (%s), reposo maximo %s (%s)",
		open, DBMaxOpenEnv, c.MaxIdle, DBMaxIdleEnv, c.MaxLifetime, DBMaxLifetimeEnv, c.MaxIdleTime, DBMaxIdleTimeEnv)
}
