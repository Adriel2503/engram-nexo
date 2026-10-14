package security

// Nexo IA · SG-05 — TLS hacia Postgres.
//
// El servidor abre dos conexiones a la base (ENGRAM_DATABASE_URL, la cuenta
// de aplicación; NEXO_MIGRATION_DATABASE_URL, la cuenta dueña, solo al
// arrancar). Este archivo decide, a partir de la cadena de conexión, si esa
// conexión usa TLS verificado:
//
//   - verificado = sslmode verify-full (cifra, comprueba la CA y el nombre del
//     host) o verify-ca (cifra y comprueba la CA). require, prefer, allow y
//     disable no verifican al servidor: un intermediario puede hacerse pasar
//     por la base. require con sslrootcert tampoco cuenta (se pide más
//     explícito).
//   - local = todos los hosts son localhost, una IP de loopback o un socket
//     Unix. Ahí no hay red de por medio y sin TLS es lo normal.
//   - Sin TLS verificado hacia un host no local, el log lo avisa al arrancar
//     (y GET /nexo/admin/database lo repite). Con NEXO_DB_REQUIRE_TLS=true el
//     servidor no arranca si alguna de las dos conexiones no usa verify-full
//     o verify-ca (cualquier host, sin excepciones), y dice por qué.
//
// Solo se lee la cadena de conexión para sacar el modo y los hosts: nunca se
// imprime ni se guarda (lleva la contraseña).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// DBRequireTLSEnv exige TLS verificado hacia la base (verify-full o
// verify-ca) en las dos conexiones; si no, el servidor no arranca.
const DBRequireTLSEnv = "NEXO_DB_REQUIRE_TLS"

// Modos de sslmode de Postgres.
const (
	SSLModeDisable    = "disable"
	SSLModeAllow      = "allow"
	SSLModePrefer     = "prefer"
	SSLModeRequire    = "require"
	SSLModeVerifyCA   = "verify-ca"
	SSLModeVerifyFull = "verify-full"
)

// DBTLS es lo que la cadena de conexión dice sobre el TLS.
type DBTLS struct {
	// SSLMode es el sslmode efectivo (el de la cadena, PGSSLMODE o "prefer",
	// el valor por defecto de Postgres y de pgx).
	SSLMode string
	// Local: todos los hosts son localhost, loopback o un socket Unix.
	Local bool
	// Verified: verify-full o verify-ca.
	Verified bool
}

// ParseRequireDBTLS interpreta NEXO_DB_REQUIRE_TLS: vacío o false = no
// exigir; true = exigir. Cualquier otro valor es un error.
func ParseRequireDBTLS(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	}
	return false, fmt.Errorf("%s=%q no es válido: use true o false", DBRequireTLSEnv, raw)
}

// RequireDBTLSFromEnv lee NEXO_DB_REQUIRE_TLS.
func RequireDBTLSFromEnv() (bool, error) { return ParseRequireDBTLS(os.Getenv(DBRequireTLSEnv)) }

// InspectDSN saca el sslmode y si los hosts son locales. No abre ninguna
// conexión.
func InspectDSN(dsn string) (DBTLS, error) {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		// pgconn ya oculta la contraseña en este error.
		return DBTLS{}, fmt.Errorf("la cadena de conexión no es válida: %w", err)
	}
	mode := strings.ToLower(strings.TrimSpace(dsnParam(dsn, "sslmode")))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(os.Getenv("PGSSLMODE")))
	}
	if mode == "" {
		mode = SSLModePrefer
	}
	switch mode {
	case SSLModeDisable, SSLModeAllow, SSLModePrefer, SSLModeRequire, SSLModeVerifyCA, SSLModeVerifyFull:
	default:
		return DBTLS{}, fmt.Errorf("sslmode=%q no es válido (disable, allow, prefer, require, verify-ca o verify-full)", mode)
	}
	hosts := []string{cfg.Host}
	for _, fb := range cfg.Fallbacks {
		hosts = append(hosts, fb.Host)
	}
	local := true
	for _, h := range hosts {
		if !isLocalDBHost(h) {
			local = false
		}
	}
	return DBTLS{SSLMode: mode, Local: local, Verified: mode == SSLModeVerifyFull || mode == SSLModeVerifyCA}, nil
}

// isLocalDBHost: localhost, cualquier IP de loopback, un socket Unix (ruta
// absoluta) o un host vacío (pgx usa el socket o localhost).
func isLocalDBHost(h string) bool {
	h = strings.Trim(strings.TrimSpace(h), "[]")
	if h == "" || strings.HasPrefix(h, "/") || strings.HasPrefix(h, "@") {
		return true
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// dsnParam lee un parámetro de la cadena de conexión: la consulta de una URL
// postgres:// o un par clave=valor.
func dsnParam(dsn, key string) string {
	dsn = strings.TrimSpace(dsn)
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return ""
		}
		return u.Query().Get(key)
	}
	// Formato "clave=valor clave='valor con espacios'".
	rest := dsn
	for len(rest) > 0 {
		rest = strings.TrimLeft(rest, " \t\r\n")
		eq := strings.IndexByte(rest, '=')
		if eq < 0 {
			return ""
		}
		k := strings.TrimSpace(rest[:eq])
		rest = strings.TrimLeft(rest[eq+1:], " \t")
		var val string
		if strings.HasPrefix(rest, "'") {
			var b strings.Builder
			i := 1
			for ; i < len(rest); i++ {
				if rest[i] == '\\' && i+1 < len(rest) {
					i++
					b.WriteByte(rest[i])
					continue
				}
				if rest[i] == '\'' {
					break
				}
				b.WriteByte(rest[i])
			}
			val = b.String()
			if i < len(rest) {
				i++
			}
			rest = rest[i:]
		} else {
			end := strings.IndexAny(rest, " \t\r\n")
			if end < 0 {
				end = len(rest)
			}
			val = rest[:end]
			rest = rest[end:]
		}
		if k == key {
			return val
		}
	}
	return ""
}

// Enforce devuelve un error claro si requireTLS está activo y la conexión no
// usa verify-full ni verify-ca. name es la variable de la conexión.
func (t DBTLS) Enforce(name string, requireTLS bool) error {
	if !requireTLS || t.Verified {
		return nil
	}
	return fmt.Errorf("%s=true exige TLS verificado hacia la base y %s usa sslmode=%s: use sslmode=verify-full (o verify-ca) con sslrootcert=<CA de la base> en %s (docs/nexo/manuales/servidor-produccion.md, sección 2.1)",
		DBRequireTLSEnv, name, t.SSLMode, name)
}

// Warning es el aviso de arranque: una base que no es local sin TLS
// verificado. Vacío si no hay nada que avisar.
func (t DBTLS) Warning(name string) string {
	if t.Local || t.Verified {
		return ""
	}
	return fmt.Sprintf("ADVERTENCIA: %s apunta a una base que no es local y no usa TLS verificado (sslmode=%s): el tráfico puede ir sin cifrar o hacia un servidor suplantado. Use sslmode=verify-full con sslrootcert=<CA de la base> o exíjalo con %s=true (docs/nexo/manuales/servidor-produccion.md, sección 2.1)",
		name, t.SSLMode, DBRequireTLSEnv)
}

// SSLProbe es lo que la propia base dice de la conexión que acaba de abrir.
type SSLProbe struct {
	Encrypted bool
	Protocol  string // TLSv1.3
}

// ProbeSSL consulta pg_stat_ssl para la conexión en uso (vista estándar de
// Postgres; cada sesión ve la suya). Si la vista no responde, el error lo
// decide quien llama: es informativo.
func ProbeSSL(ctx context.Context, db *sql.DB) (SSLProbe, error) {
	var p SSLProbe
	var ssl sql.NullBool
	var version sql.NullString
	err := db.QueryRowContext(ctx, `SELECT ssl, version FROM pg_stat_ssl WHERE pid = pg_backend_pid()`).Scan(&ssl, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, fmt.Errorf("pg_stat_ssl: %w", err)
	}
	p.Encrypted, p.Protocol = ssl.Bool, version.String
	return p, nil
}
