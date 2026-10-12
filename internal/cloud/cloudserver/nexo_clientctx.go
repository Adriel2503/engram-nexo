package cloudserver

// Nexo IA · NX-04 (base, tarea T-12) — contexto de la petición.
//
// Un middleware captura una sola vez la IP real y el User-Agent de cada
// petición y los deja en el contexto (RF-20). La cabecera X-Forwarded-For solo
// se tiene en cuenta si la conexión viene de un proxy confiable configurado en
// NEXO_TRUSTED_PROXIES (lista de IP o CIDR separadas por comas o espacios);
// si no, se usa la IP de la conexión (RF-21, CA-22).

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

// NexoTrustedProxiesEnv es la variable con los proxies confiables.
const NexoTrustedProxiesEnv = "NEXO_TRUSTED_PROXIES"

const nexoMaxUserAgentRunes = 256

// NexoClientContext es el origen de la petición.
type NexoClientContext struct {
	IP        string // IP real del cliente
	UserAgent string // recortado a 256 caracteres
}

type nexoClientContextKey struct{}

// NexoClientContextFrom devuelve el origen capturado por el middleware.
func NexoClientContextFrom(ctx context.Context) (NexoClientContext, bool) {
	if ctx == nil {
		return NexoClientContext{}, false
	}
	cc, ok := ctx.Value(nexoClientContextKey{}).(NexoClientContext)
	return cc, ok
}

// nexoWithClientContext guarda el origen para los registros del servidor y,
// además, para la capa de almacenamiento, que lo agrega a la metadata de
// cloud_auth_audit_log (NX-04, RF-24).
func nexoWithClientContext(ctx context.Context, cc NexoClientContext) context.Context {
	ctx = cloudstore.WithNexoRequestOrigin(ctx, cloudstore.NexoRequestOrigin{ClientIP: cc.IP, UserAgent: cc.UserAgent})
	return context.WithValue(ctx, nexoClientContextKey{}, cc)
}

// nexoClientContextMiddleware envuelve el handler del servidor.
func nexoClientContextMiddleware(trusted []netip.Prefix, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := NexoClientContextFrom(r.Context()); !ok {
			cc := NexoClientContext{
				IP:        nexoClientIP(r, trusted),
				UserAgent: nexoTrimUserAgent(r.UserAgent()),
			}
			r = r.WithContext(nexoWithClientContext(r.Context(), cc))
		}
		next.ServeHTTP(w, r)
	})
}

// nexoHandler aplica el middleware con los proxies confiables del entorno.
func nexoHandler(next http.Handler) http.Handler {
	return nexoClientContextMiddleware(nexoParseTrustedProxies(os.Getenv(NexoTrustedProxiesEnv)), next)
}

// nexoParseTrustedProxies interpreta NEXO_TRUSTED_PROXIES. Entradas inválidas
// se ignoran (con log).
func nexoParseTrustedProxies(raw string) []netip.Prefix {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == ';' })
	prefixes := make([]netip.Prefix, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if strings.Contains(field, "/") {
			prefix, err := netip.ParsePrefix(field)
			if err != nil {
				log.Printf("[nexo] %s: entrada inválida %q ignorada: %v", NexoTrustedProxiesEnv, field, err)
				continue
			}
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(field)
		if err != nil {
			log.Printf("[nexo] %s: entrada inválida %q ignorada: %v", NexoTrustedProxiesEnv, field, err)
			continue
		}
		addr = addr.Unmap()
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return prefixes
}

func nexoIsTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	addr = addr.Unmap()
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// nexoClientIP devuelve la IP real. Si la conexión viene de un proxy
// confiable, recorre X-Forwarded-For de derecha a izquierda y devuelve la
// primera IP que no sea un proxy confiable (con un solo proxy, como Caddy,
// es el cliente). Si no, devuelve la IP de la conexión.
func nexoClientIP(r *http.Request, trusted []netip.Prefix) string {
	remote := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	remoteAddr, err := netip.ParseAddr(remote)
	if err != nil {
		return remote
	}
	remoteAddr = remoteAddr.Unmap()
	if len(trusted) == 0 || !nexoIsTrusted(remoteAddr, trusted) {
		return remoteAddr.String()
	}
	var hops []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(header, ",") {
			if part = strings.TrimSpace(part); part != "" {
				hops = append(hops, part)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(hops[i])
		if err != nil {
			// Valor ilegible: no se puede confiar en lo que haya a su izquierda.
			break
		}
		hop = hop.Unmap()
		if nexoIsTrusted(hop, trusted) {
			continue
		}
		return hop.String()
	}
	return remoteAddr.String()
}

func nexoTrimUserAgent(ua string) string {
	ua = strings.TrimSpace(ua)
	if utf8.RuneCountInString(ua) <= nexoMaxUserAgentRunes {
		return ua
	}
	return string([]rune(ua)[:nexoMaxUserAgentRunes])
}
