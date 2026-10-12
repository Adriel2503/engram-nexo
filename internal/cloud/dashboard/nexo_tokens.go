package dashboard

// Nexo IA · NX-07 fase S3 (RS-24): caducidad y alcance de los tokens en el
// panel. Los usa components.templ (ManagedUserDetailPage y
// ManagedUserTokenCreatedPage) reemplazando expresiones existentes, sin
// agregar nodos: así el archivo generado cambia en pocas líneas.

import (
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

// nexoTokenExpiry: fecha de caducidad, "Never" (sin caducidad) o "-" si el
// almacén no informa los datos de Nexo.
func nexoTokenExpiry(tok cloudstore.PrincipalToken) string {
	if tok.NexoExpiresAt != nil {
		return formatTimeValue(*tok.NexoExpiresAt)
	}
	if tok.NexoScope != "" {
		return "Never"
	}
	return "-"
}

// nexoTokenScope: read o write ("-" si no se conoce).
func nexoTokenScope(tok cloudstore.PrincipalToken) string {
	if tok.NexoScope == "" {
		return "-"
	}
	return tok.NexoScope
}

// nexoTokenStatus: etiqueta y tono del estado de un token no revocado
// (Expired si venció). Devuelve los dos argumentos de StatusBadge para no
// agregar nodos a la plantilla (diff mínimo del archivo generado).
func nexoTokenStatus(tok cloudstore.PrincipalToken) (string, string) {
	if nexoTokenExpired(tok) {
		return "Expired", "danger"
	}
	return "Active", "success"
}

// nexoTokenExpired informa si el token ya venció.
func nexoTokenExpired(tok cloudstore.PrincipalToken) bool {
	return tok.NexoExpiresAt != nil && !tok.NexoExpiresAt.After(time.Now())
}
