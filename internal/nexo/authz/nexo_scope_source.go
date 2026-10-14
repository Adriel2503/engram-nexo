package authz

import (
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
)

// NX-07: identidad de lectura privada; las etiquetas redactadas solo son salida.
// Los campos privados no se serializan ni son recorridos por redact.Struct.
type NexoScopeSource struct{ original *Scope }

// NexoRememberScope conserva el ámbito original antes de redactar una copia.
func (s *NexoScopeSource) NexoRememberScope(scope Scope) {
	if s.original == nil {
		s.original = &scope
	}
}

// NexoAuthorizationScope usa la fuente guardada o el registro aún sin redactar.
// Una clave que ya es una marca de redacción (filas escritas con la etiqueta, sin el
// original) no identifica ningún proyecto ni espacio: se evalúa con clave vacía, que
// solo alcanzan los roles de plataforma, nunca la lectura de otro proyecto homónimo.
func (s NexoScopeSource) NexoAuthorizationScope(unredacted Scope) Scope {
	scope := unredacted
	if s.original != nil {
		scope = *s.original
	}
	if strings.Contains(scope.Key, redact.CredentialMark) || strings.Contains(scope.Key, redact.ConnectionMark) {
		return Scope{Type: scope.Type}
	}
	return scope
}

// NexoAuditScope resuelve el ámbito de un registro original de auditoría.
func NexoAuditScope(project string, metadata map[string]any) Scope {
	if space, ok := metadata["space"].(string); ok && project == "" {
		return SpaceScope(space)
	}
	return ProjectScope(project)
}

// NexoAlertScope resuelve el ámbito de una alerta original: el espacio manda sobre el proyecto.
func NexoAlertScope(details map[string]any) Scope {
	if key, ok := details["space"].(string); ok {
		return SpaceScope(key)
	}
	key, _ := details["project"].(string)
	return ProjectScope(key)
}
