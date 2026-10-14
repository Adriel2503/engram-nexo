package cloudserver

// Nexo IA · NX-05 (unificación de roles de plataforma) — Administración >
// Usuarios con el rol de Nexo (docs/nexo/panel/design.md §13).
//
//	GET  /dashboard/admin/users                       lista y alta con el rol de Nexo (rutas de Engram, ver los enganches)
//	GET  /dashboard/admin/users/{principalID}         detalle con el rol de Nexo (ruta de Engram)
//	POST /dashboard/admin/users/{principalID}/role    cambiar el rol de plataforma (platform.admins.manage)
//
// El cambio de rol usa el mismo caso de uso que la API
// (PUT /nexo/authz/v1/platform/members/{principal}): el servidor decide, la
// interfaz solo refleja. Reglas: solo un superadmin (platform.admins.manage),
// 403 en la auto-promoción, 409 si es el último superadmin, evento
// binding.* con actor, antes y después, y la columna de Engram derivada en la
// misma transacción. El formulario pide confirmación (primer POST sin
// "confirm" = pantalla de confirmación; el segundo aplica) y lleva el token
// CSRF del panel.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/dashboard"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
)

func (s *CloudServer) registerNexoUsersDashboard(mux *http.ServeMux) {
	s.nexoRoute(mux, "POST", "/dashboard/admin/users/{principalID}/role", "platform.admins.manage", s.requireDashboardSession(s.nexoUsersPanel(s.usersPanelSetRole)))
}

// nexoDashboardCan decide, para dibujar, si la sesión administrada del panel
// tiene la capacidad de plataforma. handled = false deja que Engram decida
// (sin almacén Nexo o identidad no administrada). Es una decisión de vista:
// la petición que cambia algo la vuelve a decidir su ruta con el alcance real
// del token. Falla cerrado.
func (s *CloudServer) nexoDashboardCan(r *http.Request, principal cloudauth.Principal, c authz.Capability) (allowed, handled bool) {
	if principal.Source != cloudauth.PrincipalSourceManagedToken {
		return false, false
	}
	svc, available := s.nexoAuthz()
	if !available {
		return false, false
	}
	ctx := WithPrincipal(r.Context(), principal)
	id := nexoAuthzIdentity(ctx)
	if !id.Managed() {
		return false, false
	}
	id.TokenScope = ""
	ctx = authz.WithRequestCache(authz.WithIdentity(ctx, id))
	d, err := svc.Engine().Can(ctx, id, authz.PlatformScope(), c)
	if err != nil {
		nexoLogInternal("dashboard decision "+string(c), err)
		return false, true
	}
	return d.Allowed, true
}

// nexoUsersView arma los datos de las pantallas de usuarios (MountConfig.NexoUsersView).
func (s *CloudServer) nexoUsersView(r *http.Request) dashboard.NexoUsersView {
	view := dashboard.NexoUsersView{CSRF: s.kbCSRFToken(r)}
	canAssign := false
	if principal, ok := s.dashboardPrincipalFromRequest(r); ok {
		canAssign, _ = s.nexoDashboardCan(r, principal, authz.CapPlatformAdminsManage)
	}
	view.CanChangeRole = canAssign
	for _, v := range dashboard.NexoRoleOrder {
		view.Options = append(view.Options, dashboard.NexoUserRoleOption{
			Value: v, Label: dashboard.NexoRoleLabel(v), Desc: dashboard.NexoRoleDescription(v),
			Disabled: v != dashboard.NexoRoleMiembro && !canAssign,
		})
	}
	view.Roles = map[string]string{}
	if roles, ok := s.nexoPlatformRoles(r.Context()); ok {
		for id, role := range roles {
			view.Roles[id] = string(role)
		}
	}
	return view
}

// nexoRenderManagedUserDetail dibuja el detalle con el rol de Nexo. Devuelve
// false si no hay almacén Nexo (rige la pantalla de Engram).
func (s *CloudServer) nexoRenderManagedUserDetail(w http.ResponseWriter, r *http.Request, user cloudstore.HumanUser, tokens []cloudstore.PrincipalToken, grants []cloudstore.ProjectGrant, notice, errText string, status int) bool {
	if _, available := s.nexoAuthz(); !available || !dashboard.NexoBrand() {
		return false
	}
	v := dashboard.NexoUserDetailView{NexoUsersView: s.nexoUsersView(r), Notice: notice, Error: errText}
	if notice == "" && errText == "" && r.Method == http.MethodGet && r.URL.Query().Get("rol") == "actualizado" {
		v.Notice = "Rol actualizado. El cambio quedó en la auditoría."
	}
	if status == 0 {
		status = http.StatusOK
	}
	renderDashboardAdminComponentStatus(w, r, status, s.dashboardAdminLayout(r, "Managed User", dashboard.NexoManagedUserDetailPage(user, tokens, grants, s.canManageManagedUsers(r), v)))
	return true
}

type usersPanelHandler func(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity)

// nexoUsersPanel arma la identidad desde la sesión (con el alcance de su
// token: una sesión read no cambia nada) y comprueba CSRF en los POST.
func (s *CloudServer) nexoUsersPanel(fn usersPanelHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc, ok := s.nexoAuthz()
		if !ok {
			http.Error(w, "La administración de roles no está disponible en este servidor.", http.StatusServiceUnavailable)
			return
		}
		principal, ok := s.dashboardPrincipalFromRequest(r)
		if !ok {
			http.Error(w, "Cambiar roles necesita una sesión de una persona con usuario en Nexo. Entra con el token que te asignó el administrador.", http.StatusForbidden)
			return
		}
		r = r.WithContext(WithPrincipal(r.Context(), principal))
		r, id := nexoWithAuthzIdentity(r)
		id.TokenScope = s.nexoEffectiveTokenScope(r.Context(), principal)
		r = r.WithContext(authz.WithIdentity(r.Context(), id))
		if !id.Managed() {
			http.Error(w, "Cambiar roles necesita el token de una persona con usuario en Nexo. Entra con el token que te asignó el administrador.", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, maxDashboardLoginBodyBytes)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "Formulario no válido.", http.StatusBadRequest)
				return
			}
			if !s.kbCSRFOK(r) {
				http.Error(w, "El formulario venció o no es válido. Vuelve a abrir la página e inténtalo de nuevo.", http.StatusForbidden)
				return
			}
		}
		fn(w, r, svc, id)
	}
}

// usersPanelErrText traduce un error del servicio al texto de la página.
func usersPanelErrText(err error) (int, string) {
	var denied *authz.DeniedError
	var rule *authz.RuleError
	switch {
	case errors.As(err, &denied):
		if denied.Decision.Reason == authz.ReasonNetwork {
			return http.StatusForbidden, "Cambiar roles de plataforma solo se permite desde las redes autorizadas del servidor."
		}
		return http.StatusForbidden, "Solo un superadmin puede cambiar el rol de plataforma (" + string(denied.Capability) + "). Pídeselo a uno, o entra con un token de escritura si usas uno de lectura."
	case errors.As(err, &rule):
		switch rule.Code {
		case authz.CodeLastSuperadmin:
			return http.StatusConflict, "No se puede cambiar: es el último superadmin habilitado. Asigna primero el rol de superadmin a otra persona."
		case authz.CodeSelfPromotion:
			return http.StatusForbidden, "Nadie puede asignarse a sí mismo un rol mayor al que tiene."
		case authz.CodeInvalid:
			return http.StatusBadRequest, "El rol no es válido. Elige miembro, auditor, administrador o superadmin."
		case authz.CodeNotFound:
			return http.StatusNotFound, "No encontramos a esa persona."
		case authz.CodeServiceAccountRole:
			return http.StatusUnprocessableEntity, "Una cuenta de servicio solo puede ser miembro."
		}
		return http.StatusConflict, "No se pudo completar: " + rule.Message
	}
	nexoLogInternal("dashboard role change", err)
	return http.StatusInternalServerError, "Ocurrió un error interno. Vuelve a intentarlo; si se repite, avisa al administrador."
}

// usersPanelLoad lee a la persona con sus tokens y permisos para redibujar su detalle.
func (s *CloudServer) usersPanelLoad(ctx context.Context, principalID string) (cloudstore.HumanUser, []cloudstore.PrincipalToken, []cloudstore.ProjectGrant, bool, error) {
	store := s.adminIdentity
	if store == nil {
		return cloudstore.HumanUser{}, nil, nil, false, errors.New("admin identity store is not configured")
	}
	users, err := store.ListHumanUsers(ctx)
	if err != nil {
		return cloudstore.HumanUser{}, nil, nil, false, err
	}
	for _, u := range users {
		if u.PrincipalID != principalID {
			continue
		}
		tokens, err := store.ListPrincipalTokens(ctx, principalID)
		if err != nil {
			return cloudstore.HumanUser{}, nil, nil, false, err
		}
		grants, err := store.ListProjectGrants(ctx, principalID)
		if err != nil {
			return cloudstore.HumanUser{}, nil, nil, false, err
		}
		if roles, ok := s.nexoPlatformRoles(ctx); ok {
			if role, found := roles[u.PrincipalID]; found {
				u.PlatformRole = string(role)
			}
		}
		return u, tokens, grants, true, nil
	}
	return cloudstore.HumanUser{}, nil, nil, false, nil
}

func (s *CloudServer) usersPanelFail(w http.ResponseWriter, r *http.Request, principalID string, status int, msg string) {
	s.usersPanelPage(w, r, principalID, status, "", msg)
}

// usersPanelPage redibuja el detalle de la persona con un aviso o un error.
func (s *CloudServer) usersPanelPage(w http.ResponseWriter, r *http.Request, principalID string, status int, notice, msg string) {
	user, tokens, grants, found, err := s.usersPanelLoad(r.Context(), principalID)
	if err != nil {
		nexoLogInternal("dashboard role change load", err)
		http.Error(w, "Ocurrió un error interno. Vuelve a intentarlo; si se repite, avisa al administrador.", http.StatusInternalServerError)
		return
	}
	if !found {
		renderDashboardAdminComponentStatus(w, r, http.StatusNotFound, s.dashboardAdminLayout(r, "Managed User", dashboard.EmptyState("Managed User Not Found", "No managed user exists with that principal id.")))
		return
	}
	if !s.nexoRenderManagedUserDetail(w, r, user, tokens, grants, notice, msg, status) {
		http.Error(w, notice+msg, status)
	}
}

// usersPanelSetRole es POST /dashboard/admin/users/{principalID}/role.
func (s *CloudServer) usersPanelSetRole(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	principalID := strings.TrimSpace(r.PathValue("principalID"))
	role, valid := authz.ParseAssignableRole(r.FormValue("role"))
	if principalID == "" || !valid {
		s.usersPanelFail(w, r, principalID, http.StatusBadRequest, "El rol no es válido. Elige miembro, auditor, administrador o superadmin.")
		return
	}
	user, _, _, found, err := s.usersPanelLoad(r.Context(), principalID)
	if err != nil {
		nexoLogInternal("dashboard role change load", err)
		http.Error(w, "Ocurrió un error interno. Vuelve a intentarlo; si se repite, avisa al administrador.", http.StatusInternalServerError)
		return
	}
	if !found {
		renderDashboardAdminComponentStatus(w, r, http.StatusNotFound, s.dashboardAdminLayout(r, "Managed User", dashboard.EmptyState("Managed User Not Found", "No managed user exists with that principal id.")))
		return
	}
	// La capacidad se comprueba antes de mostrar la confirmación: así una
	// denegación queda en la auditoría y no se confirma algo imposible.
	d, err := svc.Engine().Require(r.Context(), id, authz.PlatformScope(), authz.CapPlatformAdminsManage, map[string]any{"route": r.Pattern, "action": "platform_role.set", "target": principalID, "role": string(role)})
	if err != nil {
		status, msg := usersPanelErrText(err)
		s.usersPanelFail(w, r, principalID, status, msg)
		return
	}
	if !d.Allowed {
		status, msg := usersPanelErrText(&authz.DeniedError{Capability: authz.CapPlatformAdminsManage, Scope: authz.PlatformScope(), Decision: d})
		s.usersPanelFail(w, r, principalID, status, msg)
		return
	}
	current := authz.Role(dashboard.NexoRoleMiembro)
	if user.PlatformRole != "" {
		current = authz.Role(user.PlatformRole)
	}
	if current == role {
		s.usersPanelPage(w, r, principalID, http.StatusOK, "No hubo cambios: su rol de plataforma ya era "+dashboard.NexoRoleLabel(string(current))+".", "")
		return
	}
	if r.FormValue("confirm") != "1" {
		renderDashboardAdminComponent(w, r, s.dashboardAdminLayout(r, "Managed User", dashboard.NexoRoleConfirmPage(dashboard.NexoRoleConfirm{
			PrincipalID: principalID, Username: user.Username, DisplayName: user.DisplayName,
			From: string(current), To: string(role), CSRF: s.kbCSRFToken(r), Self: principalID == id.PrincipalID,
		})))
		return
	}
	if _, err := svc.SetPlatformRole(r.Context(), id, principalID, role); err != nil {
		status, msg := usersPanelErrText(err)
		s.usersPanelFail(w, r, principalID, status, msg)
		return
	}
	s.nexo.auditNav.forget(principalID)
	next := "/dashboard/admin/users/" + url.PathEscape(principalID) + "?rol=actualizado"
	if principalID == id.PrincipalID && role != authz.RoleAdmin && role != authz.RoleSuperadmin {
		next = "/dashboard/" // ya no administra: el detalle le respondería 403
	}
	redirectDashboardAdmin(w, r, next)
}
