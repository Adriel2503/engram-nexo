package cloudserver

// Nexo IA · NX-05 — adaptador entre cloudserver y el motor de roles
// (internal/nexo/authz).
//
//   - Arma authz.Identity desde el token (PrincipalFromContext +
//     nexoResolveActor de NX-01) y el origen de la petición
//     (NexoClientContextFrom de NX-04), y la deja en el contexto para los
//     módulos Nexo (API de roles y, en NX-10, espacios).
//   - Implementa los enganches // NX-05 de la administración de Engram
//     (requireManagedAdmin, crear usuario, habilitar y deshabilitar, tokens).
//
// Si el almacén no es Postgres de Nexo (fakes de tests de Engram, o un
// servidor embebido sin CloudStore), los enganches no intervienen y rige el
// comportamiento de Engram: en producción el almacén siempre es CloudStore.
// Con el almacén disponible, un error al resolver roles rechaza la petición
// (fallar cerrado).

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
)

// nexoAuthzDB lo implementa cloudstore.CloudStore (nexo_migrations.go).
type nexoAuthzDB interface {
	NexoDB() *sql.DB
}

// nexoAuthz devuelve el servicio de roles si el almacén es Postgres de Nexo.
// El motor no guarda estado entre peticiones: crearlo cuesta dos
// asignaciones de memoria.
func (s *CloudServer) nexoAuthz() (*authz.Service, bool) {
	for _, candidate := range []any{s.store, s.adminIdentity} {
		if p, ok := candidate.(nexoAuthzDB); ok && p != nil {
			if db := p.NexoDB(); db != nil {
				eng := authz.NewPostgres(db, authz.WithProjectNormalizer(cloudstore.NormalizeProjectGrant),
					authz.WithNetworkPolicy(nexoNetworkPolicy())) // NX-07 (RS-13)
				return authz.NewService(db, eng), true
			}
		}
	}
	return nil, false
}

// nexoAuthzIdentity arma la identidad de la petición. La identidad sale solo
// del token asignado (NX-01): token compartido y modo sin autenticación dan
// una identidad no administrada, sin roles (R-15).
func nexoAuthzIdentity(ctx context.Context) authz.Identity {
	actor := nexoResolveActor(ctx, "")
	id := authz.Identity{
		PrincipalID: actor.PrincipalID,
		TokenID:     actor.TokenID,
		Label:       actor.Name,
		Verified:    actor.Verified,
		// NX-07 (S3-02): alcance del token resuelto en esta petición (lo deja
		// FindPrincipalTokenByHash en el contexto); "" = write.
		TokenScope: nexoTokenScope(ctx, actor.TokenID),
	}
	if principal, ok := PrincipalFromContext(ctx); ok {
		id.Kind = string(principal.Kind)
		id.Enabled = principal.Enabled
		id.EngramRole = string(principal.Role)
		if !actor.Verified {
			id.Label = strings.TrimSpace(principal.DisplayName)
		}
	}
	if cc, ok := NexoClientContextFrom(ctx); ok {
		id.ClientIP, id.UserAgent = cc.IP, cc.UserAgent
	}
	return id
}

// nexoWithAuthzIdentity pone la identidad y la caché por petición en el
// contexto (R1-04). Lo usan las rutas Nexo después de withAuth.
func nexoWithAuthzIdentity(r *http.Request) (*http.Request, authz.Identity) {
	id := nexoAuthzIdentity(r.Context())
	ctx := authz.WithRequestCache(authz.WithIdentity(r.Context(), id))
	return r.WithContext(ctx), id
}

// ─── Respuestas de los enganches ─────────────────────────────────────────────

// nexoRespond escribe un rechazo de un enganche: JSON para /admin/* y
// /nexo/*, texto para el panel (igual que cada familia de Engram).
type nexoRespond func(w http.ResponseWriter, status int, code, message string)

func nexoRespondJSON(w http.ResponseWriter, status int, code, message string) {
	class := constants.UpgradeErrorClassPolicy
	if status >= 500 {
		class = constants.UpgradeErrorClassBlocked
	}
	writeActionableError(w, status, class, code, message)
}

func nexoRespondText(w http.ResponseWriter, status int, _ string, message string) {
	http.Error(w, message, status)
}

func nexoRespondDenied(w http.ResponseWriter, respond nexoRespond, c authz.Capability, d authz.Decision) {
	respond(w, http.StatusForbidden, constants.ReasonPolicyForbidden, fmt.Sprintf("forbidden: %s is required (%s)", c, d.Reason))
}

func nexoRespondAuthzError(w http.ResponseWriter, respond nexoRespond, err error) {
	respond(w, http.StatusInternalServerError, constants.UpgradeErrorCodeInternal, fmt.Sprintf("authorization unavailable: %v", err))
}

// ─── Enganche de requireManagedAdmin ─────────────────────────────────────────

// nexoAdminRouteCapabilities: capacidad que exige cada ruta administrativa
// (design §4.1). La clave es el patrón registrado en el mux.
var nexoAdminRouteCapabilities = map[string]authz.Capability{
	// API /admin/* de Engram.
	"GET /admin/users":                                        authz.CapPlatformDirectoryRead,
	"POST /admin/users":                                       authz.CapPlatformUsersManage,
	"POST /admin/users/{principalID}/enable":                  authz.CapPlatformUsersManage,
	"POST /admin/users/{principalID}/disable":                 authz.CapPlatformUsersManage,
	"GET /admin/users/{principalID}/tokens":                   authz.CapPlatformTokensManage,
	"POST /admin/users/{principalID}/tokens":                  authz.CapPlatformTokensManage,
	"POST /admin/tokens/{tokenID}/revoke":                     authz.CapPlatformTokensManage,
	"GET /admin/users/{principalID}/grants":                   authz.CapPlatformDirectoryRead,
	"POST /admin/users/{principalID}/grants":                  authz.CapPlatformUsersManage,
	"POST /admin/users/{principalID}/grants/{project}/revoke": authz.CapPlatformUsersManage,
	// Panel de Engram.
	"POST /dashboard/admin/users":                                       authz.CapPlatformUsersManage,
	"POST /dashboard/admin/users/{principalID}/enable":                  authz.CapPlatformUsersManage,
	"POST /dashboard/admin/users/{principalID}/disable":                 authz.CapPlatformUsersManage,
	"POST /dashboard/admin/users/{principalID}/tokens":                  authz.CapPlatformTokensManage,
	"POST /dashboard/admin/tokens/{tokenID}/revoke":                     authz.CapPlatformTokensManage,
	"POST /dashboard/admin/users/{principalID}/grants":                  authz.CapPlatformUsersManage,
	"POST /dashboard/admin/users/{principalID}/grants/{project}/revoke": authz.CapPlatformUsersManage,
	// Auditoría NX-03 y eventos de roles: también el auditor.
	"GET /nexo/admin/audit":               authz.CapPlatformAuditRead,
	"GET /nexo/admin/activity":            authz.CapPlatformAuditRead,
	"GET /nexo/admin/presence":            authz.CapPlatformAuditRead,
	"GET /nexo/admin/history/{entityKey}": authz.CapPlatformAuditRead,
	"GET /nexo/admin/authz-events":        authz.CapPlatformAuditRead,
}

// nexoRouteCapability devuelve la capacidad de la ruta. Una ruta
// administrativa sin entrada exige platform.users.manage (fallar cerrado:
// nunca menos que un admin).
func nexoRouteCapability(r *http.Request) authz.Capability {
	if c, ok := nexoAdminRouteCapabilities[r.Pattern]; ok {
		return c
	}
	return authz.CapPlatformUsersManage
}

// nexoRequireAdminCapability es el enganche de requireManagedAdmin (// NX-05).
// handled = false deja que Engram decida (sin almacén Nexo, o identidad no
// administrada: token compartido, admin del entorno, CLI, modo inseguro, que
// Engram ya rechaza).
func (s *CloudServer) nexoRequireAdminCapability(w http.ResponseWriter, r *http.Request) (principal cloudauth.Principal, ok bool, handled bool) {
	svc, available := s.nexoAuthz()
	if !available {
		return cloudauth.Principal{}, false, false
	}
	principal, found := PrincipalFromContext(r.Context())
	if !found {
		return cloudauth.Principal{}, false, false
	}
	id := nexoAuthzIdentity(r.Context())
	if !id.Managed() {
		return cloudauth.Principal{}, false, false
	}
	c := nexoRouteCapability(r)
	d, err := svc.Engine().Require(r.Context(), id, authz.PlatformScope(), c, map[string]any{"route": r.Pattern})
	if err != nil {
		nexoRespondAuthzError(w, nexoRespondJSON, err)
		return cloudauth.Principal{}, false, true
	}
	if !d.Allowed {
		writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden,
			fmt.Sprintf("forbidden: %s is required for this route (platform role %q)", c, roleLabel(d.Role)))
		return cloudauth.Principal{}, false, true
	}
	return principal, true, true
}

func roleLabel(r authz.Role) string {
	if r == authz.RoleNone {
		return string(authz.RoleMiembro)
	}
	return string(r)
}

// ─── Enganches sobre otra persona (R-7, R-8) ─────────────────────────────────

func isPlatformStaff(r authz.Role) bool {
	return r == authz.RoleSuperadmin || r == authz.RoleAdmin || r == authz.RoleAuditor
}

// nexoAllowCreateUserRole: crear un usuario con rol admin exige
// platform.admins.manage (solo superadmin, R-7 y RF-07). Antes de este
// enganche cualquier admin de Engram podía crear otro admin.
func (s *CloudServer) nexoAllowCreateUserRole(w http.ResponseWriter, r *http.Request, role string, respond nexoRespond) bool {
	if !strings.EqualFold(strings.TrimSpace(role), string(cloudauth.RoleAdmin)) {
		return true
	}
	return s.nexoRequireAdminsManage(w, r, respond, map[string]any{"route": r.Pattern, "action": "user.create", "role": "admin"})
}

func (s *CloudServer) nexoRequireAdminsManage(w http.ResponseWriter, r *http.Request, respond nexoRespond, details map[string]any) bool {
	svc, available := s.nexoAuthz()
	if !available {
		return true
	}
	id := nexoAuthzIdentity(r.Context())
	if !id.Managed() {
		return true // Engram ya exigió un admin administrado
	}
	d, err := svc.Engine().Require(r.Context(), id, authz.PlatformScope(), authz.CapPlatformAdminsManage, details)
	if err != nil {
		nexoRespondAuthzError(w, respond, err)
		return false
	}
	if !d.Allowed {
		nexoRespondDenied(w, respond, authz.CapPlatformAdminsManage, d)
		return false
	}
	return true
}

// nexoAllowSetUserEnabled: habilitar o deshabilitar a un admin, superadmin o
// auditor exige platform.admins.manage (R-7); el último superadmin
// habilitado no se deshabilita (R-8; además lo impide un trigger).
func (s *CloudServer) nexoAllowSetUserEnabled(w http.ResponseWriter, r *http.Request, targetPrincipalID string, enabled bool, respond nexoRespond) bool {
	svc, available := s.nexoAuthz()
	if !available {
		return true
	}
	id := nexoAuthzIdentity(r.Context())
	if !id.Managed() {
		return true
	}
	targetRole, found, err := svc.PlatformRoleOf(r.Context(), targetPrincipalID)
	if err != nil {
		nexoRespondAuthzError(w, respond, err)
		return false
	}
	if !found || !isPlatformStaff(targetRole) {
		return true
	}
	action := "user.enable"
	if !enabled {
		action = "user.disable"
	}
	details := map[string]any{"route": r.Pattern, "action": action, "target_principal_id": targetPrincipalID, "target_role": string(targetRole)}
	if !s.nexoRequireAdminsManage(w, r, respond, details) {
		return false
	}
	if enabled || targetRole != authz.RoleSuperadmin {
		return true
	}
	others, err := svc.OtherEnabledSuperadmins(r.Context(), targetPrincipalID)
	if err != nil {
		nexoRespondAuthzError(w, respond, err)
		return false
	}
	if others == 0 {
		svc.Engine().RecordDenied(r.Context(), id, authz.PlatformScope(), authz.CapPlatformAdminsManage,
			authz.Decision{Reason: authz.CodeLastSuperadmin}, details)
		respond(w, http.StatusConflict, authz.CodeLastSuperadmin, "the last enabled superadmin cannot be disabled (R-8)")
		return false
	}
	return true
}

// nexoAllowManageTargetTokens: emitir o revocar tokens de otro admin,
// superadmin o auditor exige platform.admins.manage (R-7: un admin solo
// gestiona miembros y cuentas de servicio; con un token ajeno podría actuar
// como esa persona). Los tokens propios no cambian.
func (s *CloudServer) nexoAllowManageTargetTokens(w http.ResponseWriter, r *http.Request, targetPrincipalID string, respond nexoRespond) bool {
	svc, available := s.nexoAuthz()
	if !available {
		return true
	}
	id := nexoAuthzIdentity(r.Context())
	if !id.Managed() || strings.TrimSpace(targetPrincipalID) == id.PrincipalID {
		return true
	}
	targetRole, found, err := svc.PlatformRoleOf(r.Context(), targetPrincipalID)
	if err != nil {
		nexoRespondAuthzError(w, respond, err)
		return false
	}
	if !found || !isPlatformStaff(targetRole) {
		return true
	}
	return s.nexoRequireAdminsManage(w, r, respond, map[string]any{"route": r.Pattern, "action": "token.manage", "target_principal_id": targetPrincipalID, "target_role": string(targetRole)})
}

// nexoAllowRevokeToken resuelve el dueño del token y aplica la regla anterior.
func (s *CloudServer) nexoAllowRevokeToken(w http.ResponseWriter, r *http.Request, tokenID string, respond nexoRespond) bool {
	svc, available := s.nexoAuthz()
	if !available {
		return true
	}
	owner, found, err := svc.TokenOwner(r.Context(), tokenID)
	if err != nil {
		nexoRespondAuthzError(w, respond, err)
		return false
	}
	if !found {
		return true // Engram responde el error de token inexistente
	}
	return s.nexoAllowManageTargetTokens(w, r, owner, respond)
}
