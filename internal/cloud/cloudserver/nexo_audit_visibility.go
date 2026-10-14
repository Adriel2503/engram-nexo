package cloudserver

// NX-07 / R-6: el acceso administrativo no autoriza contenido.
import (
	"context"
	"errors"
	"net/http"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
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
		// Texto que viene de afuera: nunca con formatos de credencial, haya o no permiso de lectura.
		items[i].ClientClaim = security.NexoRedactAuditText(items[i].ClientClaim)
		items[i].Actor = security.NexoRedactAuditText(items[i].Actor)
		items[i].TokenName = security.NexoRedactAuditText(items[i].TokenName)
		allowed, err := s.nexoAuditContentAllowed(ctx, authz.ProjectScope(items[i].Project))
		if err != nil {
			nexoInternalError(w, "visibilidad del historial", errors.New("autorización no disponible"))
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
		items[i].UserAgent = security.NexoRedactAuditText(items[i].UserAgent)
		items[i].ActorSource = security.NexoRedactAuditText(items[i].ActorSource)
		scope := authz.ProjectScope(items[i].Project)
		if space, ok := items[i].Metadata["space"].(string); ok && items[i].Project == "" {
			scope = authz.SpaceScope(space)
		}
		allowed, err := s.nexoAuditContentAllowed(ctx, scope)
		if err != nil {
			nexoInternalError(w, "visibilidad de auditoría", errors.New("autorización no disponible"))
			return false
		}
		items[i].Metadata = security.NexoAuditMetadata(items[i].Metadata, allowed)
	}
	return true
}
func (s *CloudServer) nexoFilterRoleEvents(w http.ResponseWriter, r *http.Request, items []authz.EventRecord) bool {
	ctx := authz.WithRequestCache(r.Context())
	for i := range items {
		allowed, err := s.nexoAuditContentAllowed(ctx, authz.Scope{Type: authz.ScopeType(items[i].ScopeType), Key: items[i].ScopeKey})
		if err != nil {
			nexoInternalError(w, "visibilidad de eventos", errors.New("autorización no disponible"))
			return false
		}
		items[i].Details = security.NexoAuditMetadata(items[i].Details, allowed)
	}
	return true
}
func (s *CloudServer) nexoFilterAlerts(w http.ResponseWriter, r *http.Request, items []evidence.Alert) bool {
	ctx := authz.WithRequestCache(r.Context())
	for i := range items {
		scope := authz.ProjectScope("")
		if key, ok := items[i].Details["space"].(string); ok {
			scope = authz.SpaceScope(key)
		} else if key, ok := items[i].Details["project"].(string); ok {
			scope = authz.ProjectScope(key)
		}
		allowed, err := s.nexoAuditContentAllowed(ctx, scope)
		if err != nil {
			nexoInternalError(w, "visibilidad de alertas", errors.New("autorización no disponible"))
			return false
		}
		items[i].Details = security.NexoAuditMetadata(items[i].Details, allowed)
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
