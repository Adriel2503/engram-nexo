package cloudstore

// Nexo IA · NX-04 — origen de la petición en la auditoría de seguridad.
//
// El middleware de cloudserver deja en el contexto la IP real y el User-Agent
// de cada petición (WithNexoRequestOrigin). Al insertar cualquier evento en
// cloud_auth_audit_log, insertAuthAuditEvent agrega esos datos a metadata
// (client_ip, user_agent) sin cambiar el esquema de Engram (RF-24). Así quedan
// cubiertos todos los caminos que auditan: rechazos de sync, login y bootstrap
// del panel, gestión de usuarios, tokens y permisos (incluida la emisión de
// tokens, que audita dentro de su propia transacción).

import (
	"context"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"strings"
)

// Claves que NX-04 agrega a cloud_auth_audit_log.metadata.
const (
	NexoAuditMetaClientIP  = "client_ip"
	NexoAuditMetaUserAgent = "user_agent"
)

// NexoRequestOrigin es el origen de la petición visto por el servidor.
type NexoRequestOrigin struct {
	ClientIP  string
	UserAgent string
}

type nexoRequestOriginKey struct{}

// WithNexoRequestOrigin deja el origen de la petición en el contexto.
func WithNexoRequestOrigin(ctx context.Context, origin NexoRequestOrigin) context.Context {
	return context.WithValue(ctx, nexoRequestOriginKey{}, origin)
}

// NexoRequestOriginFrom devuelve el origen guardado con WithNexoRequestOrigin.
func NexoRequestOriginFrom(ctx context.Context) (NexoRequestOrigin, bool) {
	if ctx == nil {
		return NexoRequestOrigin{}, false
	}
	origin, ok := ctx.Value(nexoRequestOriginKey{}).(NexoRequestOrigin)
	return origin, ok
}

// nexoAuditMetadata devuelve metadata con client_ip y user_agent agregados si
// hay origen en el contexto. Nunca modifica el mapa recibido ni pisa claves
// que el evento ya traiga. Sin origen conserva sus campos, retirando formatos de credencial del texto libre.
func nexoAuditMetadata(ctx context.Context, metadata map[string]any) map[string]any {
	metadata = security.NexoRedactAuditMetadata(metadata)
	origin, ok := NexoRequestOriginFrom(ctx)
	if !ok {
		return metadata
	}
	ip := nexoTruncateRunes(strings.TrimSpace(origin.ClientIP), 64)
	ua := nexoTruncateRunes(strings.TrimSpace(origin.UserAgent), nexoMaxUserAgentRunes)
	if ip == "" && ua == "" {
		return metadata
	}
	out := make(map[string]any, len(metadata)+2)
	for k, v := range metadata {
		out[k] = v
	}
	if _, exists := out[NexoAuditMetaClientIP]; !exists && ip != "" {
		out[NexoAuditMetaClientIP] = ip
	}
	if _, exists := out[NexoAuditMetaUserAgent]; !exists && ua != "" {
		out[NexoAuditMetaUserAgent] = ua
	}
	return security.NexoRedactAuditMetadata(out)
}
