package httpapi

// Nexo IA · NX-10 — origen público de la petición para los enlaces de las
// citas (RC-08). Detrás de un proxy HTTPS (Caddy) la conexión con el servidor
// es HTTP y r.TLS es nil: el esquema y el host públicos llegan en
// X-Forwarded-Proto y X-Forwarded-Host. El servidor (cloudserver, que conoce
// los proxies confiables de NEXO_TRUSTED_PROXIES) los deja en el contexto solo
// si la conexión viene de uno de ellos; este paquete nunca lee esas cabeceras
// directamente.

import (
	"context"
	"strings"
)

type forwardedOriginKey struct{}

type forwardedOrigin struct {
	scheme string // "http" o "https"; vacío = no informado
	host   string // vacío = no informado
}

// WithForwardedOrigin guarda el esquema y el host públicos informados por un
// proxy confiable. Valores inválidos se descartan.
func WithForwardedOrigin(ctx context.Context, scheme, host string) context.Context {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if scheme != "http" && scheme != "https" {
		scheme = ""
	}
	host = strings.TrimSpace(host)
	if !validHost(host) {
		host = ""
	}
	if scheme == "" && host == "" {
		return ctx
	}
	return context.WithValue(ctx, forwardedOriginKey{}, forwardedOrigin{scheme: scheme, host: host})
}

func forwardedOriginFrom(ctx context.Context) (forwardedOrigin, bool) {
	if ctx == nil {
		return forwardedOrigin{}, false
	}
	o, ok := ctx.Value(forwardedOriginKey{}).(forwardedOrigin)
	return o, ok
}

func validHost(host string) bool {
	return host != "" && !strings.ContainsAny(host, " /\\@\t\r\n,")
}
