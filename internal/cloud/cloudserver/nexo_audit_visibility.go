package cloudserver

// NX-07 / R-6: el acceso administrativo no autoriza contenido.
//
// Todo registro que sale de aquí pasa por dos barreras:
//
//  1. redact.Struct limpia, en el lugar, TODOS los textos de la fila (cada campo
//     string, mapas, listas y documentos JSON anidados), con o sin permiso de
//     lectura: un campo nuevo queda cubierto sin enumerarlo;
//  2. los metadatos (mapas) pasan por security.NexoProject con el esquema de su
//     flujo: sin lectura del ámbito solo salen los campos declarados.
import (
	"context"
	"fmt"
	"net/http"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

func (s *CloudServer) nexoAuditContentAllowed(ctx context.Context, scope authz.Scope) (bool, error) {
	svc, ok := s.nexoAuthz()
	if !ok {
		return false, nil
	}
	capability := authz.CapProjectRead
	if scope.Type == authz.ScopeSpace {
		capability = authz.CapSpaceRead
	} else if scope.Type != authz.ScopeProject {
		return false, nil
	}
	d, err := svc.Engine().Can(ctx, nexoAuthzIdentity(ctx), scope, capability)
	return d.Allowed && err == nil, err
}
func (s *CloudServer) nexoFilterHistory(w http.ResponseWriter, r *http.Request, items []cloudstore.NexoHistoryRecord) bool {
	ctx := authz.WithRequestCache(r.Context())
	for i := range items {
		// El título y el payload de una memoria pueden traer una credencial pegada por quien la escribió:
		// leer el proyecto no autoriza usarla. Se limpia una copia siempre, con o sin permiso.
		redact.Struct(&items[i])
		allowed, err := s.nexoAuditContentAllowed(ctx, authz.ProjectScope(items[i].Project))
		if err != nil {
			nexoInternalError(w, "visibilidad del historial", fmt.Errorf("autorización no disponible: %w", err))
			return false
		}
		if !allowed {
			items[i].Title = ""
			items[i].Payload = nil
			items[i].PayloadWithheld = true
		}
	}
	return true
}
func (s *CloudServer) nexoFilterSecurityAudit(w http.ResponseWriter, r *http.Request, items []cloudstore.NexoAuditRecord) bool {
	ctx := authz.WithRequestCache(r.Context())
	for i := range items {
		redact.Struct(&items[i])
		scope := authz.ProjectScope(items[i].Project)
		if space, ok := items[i].Metadata["space"].(string); ok && items[i].Project == "" {
			scope = authz.SpaceScope(space)
		}
		allowed, err := s.nexoAuditContentAllowed(ctx, scope)
		if err != nil {
			nexoInternalError(w, "visibilidad de auditoría", fmt.Errorf("autorización no disponible: %w", err))
			return false
		}
		items[i].Metadata = security.NexoProject(security.NexoStreamAudit, items[i].Metadata, allowed)
	}
	return true
}
func (s *CloudServer) nexoFilterRoleEvents(w http.ResponseWriter, r *http.Request, items []authz.EventRecord) bool {
	ctx := authz.WithRequestCache(r.Context())
	for i := range items {
		redact.Struct(&items[i])
		allowed, err := s.nexoAuditContentAllowed(ctx, authz.Scope{Type: authz.ScopeType(items[i].ScopeType), Key: items[i].ScopeKey})
		if err != nil {
			nexoInternalError(w, "visibilidad de eventos", fmt.Errorf("autorización no disponible: %w", err))
			return false
		}
		items[i].Details = security.NexoProject(security.NexoStreamRoles, items[i].Details, allowed)
	}
	return true
}
func (s *CloudServer) nexoFilterAlerts(w http.ResponseWriter, r *http.Request, items []evidence.Alert) bool {
	ctx := authz.WithRequestCache(r.Context())
	for i := range items {
		redact.Struct(&items[i])
		scope := authz.ProjectScope("")
		if key, ok := items[i].Details["space"].(string); ok {
			scope = authz.SpaceScope(key)
		} else if key, ok := items[i].Details["project"].(string); ok {
			scope = authz.ProjectScope(key)
		}
		allowed, err := s.nexoAuditContentAllowed(ctx, scope)
		if err != nil {
			nexoInternalError(w, "visibilidad de alertas", fmt.Errorf("autorización no disponible: %w", err))
			return false
		}
		alertStream := security.NexoStreamAlert
		if items[i].Layer == evidence.LayerKnowledge {
			alertStream = security.NexoStreamAlertKB
		}
		items[i].Details = security.NexoProject(alertStream, items[i].Details, allowed)
		// El resumen es texto libre: solo se entrega en el ámbito autorizado.
		if !allowed {
			items[i].Summary = "Evento de auditoría: " + items[i].Rule
			if items[i].Ack != nil { // la nota del reconocimiento también es texto libre
				items[i].Ack.WithholdNote()
			}
		}
	}
	return true
}
