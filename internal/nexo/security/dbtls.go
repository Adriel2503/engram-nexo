package security

// Nexo IA · SG-05 — TLS hacia Postgres.
//
// El servidor abre dos conexiones a la base (ENGRAM_DATABASE_URL, la cuenta
// de aplicación; NEXO_MIGRATION_DATABASE_URL, la cuenta dueña, solo al
// arrancar). Este archivo decide, a partir de la configuración efectiva que
// pgx arma con la cadena de conexión (la misma que usa al conectar), si esa
// conexión usa TLS verificado:
//
//   - verificado = verify-full (cifra, comprueba la CA y el nombre del host) o
//     verify-ca (cifra y comprueba la CA). require, prefer, allow y disable no
//     verifican al servidor: un intermediario puede hacerse pasar por la base.
//     require con sslrootcert lo trata pgx como verify-ca (igual que libpq) y
//     por eso cuenta como verificado.
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
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
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
	// SSLMode es el sslmode efectivo según la configuración TLS que arma pgx
	// (cadena, PGSSLMODE, archivo de servicio o "prefer" por defecto).
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

// InspectDSN decide el TLS a partir de la configuración EFECTIVA de pgx
// (pgconn.ParseConfig, la misma que usa el driver al abrir con esta cadena),
// no de una lectura propia de la cadena: así cuentan igual que en el driver
// un sslmode repetido (gana el último), PGSSLMODE, un archivo de servicio
// (PGSERVICE, con prioridad sobre las variables) y require con sslrootcert
// (que pgx trata como verify-ca, igual que libpq). Verificado = todos los
// intentos de conexión (el host y sus alternativas) usan TLS y comprueban el
// certificado del servidor. No abre ninguna conexión.
func InspectDSN(dsn string) (DBTLS, error) {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		// pgconn ya oculta la contraseña en este error.
		return DBTLS{}, fmt.Errorf("la cadena de conexión no es válida: %w", err)
	}
	tries := []*tls.Config{cfg.TLSConfig}
	hosts := []string{cfg.Host}
	for _, fb := range cfg.Fallbacks {
		tries = append(tries, fb.TLSConfig)
		hosts = append(hosts, fb.Host)
	}
	local := true
	for _, h := range hosts {
		if !isLocalDBHost(h) {
			local = false
		}
	}
	mode := effectiveSSLMode(tries)
	return DBTLS{SSLMode: mode, Local: local, Verified: mode == SSLModeVerifyFull || mode == SSLModeVerifyCA}, nil
}

// effectiveSSLMode traduce las configuraciones TLS de cada intento de pgx al
// sslmode al que equivalen. Es estricto: basta un intento sin TLS o sin
// verificación para que el conjunto no cuente como verificado.
func effectiveSSLMode(tries []*tls.Config) string {
	plain, insecure, caOnly := 0, 0, 0
	for _, tc := range tries {
		switch {
		case tc == nil:
			plain++
		case tc.VerifyPeerCertificate != nil:
			caOnly++ // verify-ca (o require con sslrootcert): cadena sí, nombre no
		case tc.InsecureSkipVerify:
			insecure++
		}
	}
	switch {
	case plain == len(tries):
		return SSLModeDisable
	case plain > 0 && tries[0] == nil:
		return SSLModeAllow // primero sin TLS, después con TLS
	case plain > 0:
		return SSLModePrefer // primero TLS sin verificar, después sin TLS
	case insecure > 0:
		return SSLModeRequire
	case caOnly > 0:
		return SSLModeVerifyCA
	default:
		return SSLModeVerifyFull
	}
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
