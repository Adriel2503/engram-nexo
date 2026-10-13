package cloudserver

// Nexo IA · NX-05 — API de roles /nexo/authz/v1 (design §5).
//
//	GET    /nexo/authz/v1/me                                        self.read
//	GET    /nexo/authz/v1/platform/members                          platform.directory.read
//	PUT    /nexo/authz/v1/platform/members/{principal}   {role}     platform.admins.manage
//	DELETE /nexo/authz/v1/platform/members/{principal}              platform.admins.manage
//	POST   /nexo/authz/v1/scopes                         {type,key,visibility,default_role,owner}
//	                                                                platform.scope.create + assign_owner
//	PATCH  /nexo/authz/v1/scopes/{type}/{key}            {visibility,default_role}
//	                                                                project.configure | space.configure
//	POST   /nexo/authz/v1/scopes/{type}/{key}:archive | :restore    platform.scope.archive
//	POST   /nexo/authz/v1/scopes/{type}/{key}:recover    {owner}    platform.scope.recover
//	GET    /nexo/authz/v1/scopes/{type}/{key}/members               *.read del ámbito o platform.directory.read
//	PUT    /nexo/authz/v1/scopes/{type}/{key}/members/{principal} {role}
//	DELETE /nexo/authz/v1/scopes/{type}/{key}/members/{principal}   project.members.manage | space.members.manage
//	GET    /nexo/authz/v1/events                                    platform.audit.read
//	GET    /nexo/admin/authz-events                                 igual que /events (familia NX-03)
//	GET    /nexo/authz/v1/tokens[?principal=]                       self.read o platform.tokens.manage (NX-07)
//	POST   /nexo/authz/v1/tokens  {principal,name,expires_in_days,scope}
//	                                                                platform.tokens.manage (NX-07)
//
// Errores como NX-03 ({error_class, error_code, error}): 401 sin token
// válido, 403 sin capacidad (y evento authz.denied), 404 ámbito no visible o
// inexistente, 409 regla de negocio (último superadmin, último responsable,
// ámbito existente), 400 validación. Paginación por cursor (máximo 200).
// Token compartido y modo sin autenticación: 403 en todas (R-15).

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
)

const nexoAuditActionAuthzEventsRead = "nexo.authz_events.read"

const nexoAuthzPrefix = "/nexo/authz/v1"

func (s *CloudServer) registerNexoAuthzRoutes(mux *http.ServeMux) {
	s.nexoRoute(mux, "GET", nexoAuthzPrefix+"/me", "self.read", s.nexoAuthzRoute(s.handleNexoAuthzMe))
	s.nexoRoute(mux, "GET", nexoAuthzPrefix+"/platform/members", "platform.directory.read", s.nexoAuthzRoute(s.handleNexoAuthzPlatformMembers))
	s.nexoRoute(mux, "PUT", nexoAuthzPrefix+"/platform/members/{principal}", "platform.admins.manage", s.nexoAuthzRoute(s.handleNexoAuthzSetPlatformMember))
	s.nexoRoute(mux, "DELETE", nexoAuthzPrefix+"/platform/members/{principal}", "platform.admins.manage", s.nexoAuthzRoute(s.handleNexoAuthzDeletePlatformMember))
	s.nexoRoute(mux, "POST", nexoAuthzPrefix+"/scopes", "platform.scope.create+platform.scope.assign_owner", s.nexoAuthzRoute(s.handleNexoAuthzCreateScope))
	s.nexoRoute(mux, "PATCH", nexoAuthzPrefix+"/scopes/{type}/{key}", "project.configure|space.configure", s.nexoAuthzRoute(s.handleNexoAuthzUpdateScope))
	s.nexoRoute(mux, "POST", nexoAuthzPrefix+"/scopes/{type}/{keyAction}", "platform.scope.archive|platform.scope.recover", s.nexoAuthzRoute(s.handleNexoAuthzScopeAction))
	s.nexoRoute(mux, "GET", nexoAuthzPrefix+"/scopes/{type}/{key}/members", "project.read|space.read|platform.directory.read", s.nexoAuthzRoute(s.handleNexoAuthzScopeMembers))
	s.nexoRoute(mux, "PUT", nexoAuthzPrefix+"/scopes/{type}/{key}/members/{principal}", "project.members.manage|space.members.manage", s.nexoAuthzRoute(s.handleNexoAuthzSetScopeMember))
	s.nexoRoute(mux, "DELETE", nexoAuthzPrefix+"/scopes/{type}/{key}/members/{principal}", "project.members.manage|space.members.manage", s.nexoAuthzRoute(s.handleNexoAuthzDeleteScopeMember))
	s.nexoRoute(mux, "GET", nexoAuthzPrefix+"/events", "platform.audit.read", s.withAuth(s.handleNexoAuthzEvents))
	s.registerNexoTokenRoutes(mux) // NX-07 (S3-03): emisión y listado de tokens con caducidad y alcance
}

type nexoAuthzHandler func(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity)

// nexoAuthzRoute: withAuth (401) + almacén de roles (500 si falta) +
// identidad en el contexto + identidad administrada (403, R-15).
func (s *CloudServer) nexoAuthzRoute(h nexoAuthzHandler) http.HandlerFunc {
	return s.withAuth(func(w http.ResponseWriter, r *http.Request) {
		svc, ok := s.nexoAuthz()
		if !ok {
			writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, "nexo authz store is not configured")
			return
		}
		r, id := nexoWithAuthzIdentity(r)
		if !id.Managed() {
			svc.Engine().RecordDenied(r.Context(), id, authz.PlatformScope(), authz.CapSelfRead, authz.Decision{Reason: authz.ReasonUnmanaged}, map[string]any{"route": r.Pattern})
			writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden, "forbidden: a managed user token is required (shared token and insecure mode have no roles)")
			return
		}
		h(w, r, svc, id)
	})
}

// nexoAuthzWriteError traduce errores del servicio a HTTP.
func nexoAuthzWriteError(w http.ResponseWriter, err error) {
	var denied *authz.DeniedError
	var rule *authz.RuleError
	switch {
	case errors.As(err, &denied):
		if denied.Decision.Hidden {
			writeActionableError(w, http.StatusNotFound, constants.UpgradeErrorClassRepairable, authz.CodeNotFound, fmt.Sprintf("scope %s not found", denied.Scope))
			return
		}
		writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden,
			fmt.Sprintf("forbidden: %s is required (%s)", denied.Capability, denied.Decision.Reason))
	case errors.As(err, &rule):
		status := http.StatusConflict
		class := constants.UpgradeErrorClassPolicy
		switch rule.Code {
		case authz.CodeInvalid:
			status, class = http.StatusBadRequest, constants.UpgradeErrorClassRepairable
		case authz.CodeNotFound:
			status, class = http.StatusNotFound, constants.UpgradeErrorClassRepairable
		case authz.CodeSelfPromotion:
			status = http.StatusForbidden
		case authz.CodeServiceAccountRole:
			status = http.StatusUnprocessableEntity
		}
		writeActionableError(w, status, class, rule.Code, rule.Message)
	case errors.Is(err, authz.ErrInvalidCursor):
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "invalid cursor")
	default:
		nexoInternalError(w, "nexo authz", err)
	}
}

func nexoAuthzBadRequest(w http.ResponseWriter, message string) {
	writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, authz.CodeInvalid, message)
}

// ─── Mis roles ───────────────────────────────────────────────────────────────

func (s *CloudServer) handleNexoAuthzMe(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	view, err := svc.Me(r.Context(), id)
	if err != nil {
		nexoAuthzWriteError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, s.nexoMeWithToken(r.Context(), view)) // NX-07: caducidad, alcance y último uso (RS-24)
}

// ─── Plataforma ──────────────────────────────────────────────────────────────

func (s *CloudServer) handleNexoAuthzPlatformMembers(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	p := nexoParams{values: r.URL.Query()}
	q := authz.MemberQuery{Role: authz.Role(p.str("role")), Cursor: p.str("cursor"), Limit: p.limit()}
	if p.fail(w) {
		return
	}
	items, next, err := svc.ListPlatformMembers(r.Context(), id, q)
	if err != nil {
		nexoAuthzWriteError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, newNexoPage(items, next, authz.NormalizeLimit(q.Limit)))
}

type nexoRoleBody struct {
	Role string `json:"role"`
}

func (s *CloudServer) handleNexoAuthzSetPlatformMember(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	var body nexoRoleBody
	if err := decodeJSONBody(r, &body); err != nil {
		nexoAuthzBadRequest(w, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if strings.TrimSpace(body.Role) == "" {
		nexoAuthzBadRequest(w, "role is required (superadmin, admin, auditor or miembro)")
		return
	}
	change, err := svc.SetPlatformRole(r.Context(), id, r.PathValue("principal"), authz.Role(strings.TrimSpace(body.Role)))
	if err != nil {
		nexoAuthzWriteError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, change)
}

func (s *CloudServer) handleNexoAuthzDeletePlatformMember(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	change, err := svc.SetPlatformRole(r.Context(), id, r.PathValue("principal"), authz.RoleMiembro)
	if err != nil {
		nexoAuthzWriteError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, change)
}

// ─── Ámbitos ─────────────────────────────────────────────────────────────────

type nexoCreateScopeBody struct {
	Type        string `json:"type"`
	Key         string `json:"key"`
	Visibility  string `json:"visibility"`
	DefaultRole string `json:"default_role"`
	Owner       string `json:"owner"`
}

func (s *CloudServer) handleNexoAuthzCreateScope(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	var body nexoCreateScopeBody
	if err := decodeJSONBody(r, &body); err != nil {
		nexoAuthzBadRequest(w, fmt.Sprintf("invalid body: %v", err))
		return
	}
	info, err := svc.CreateScope(r.Context(), id, authz.CreateScopeInput{
		Type: authz.ScopeType(strings.TrimSpace(body.Type)), Key: body.Key,
		Visibility: strings.TrimSpace(body.Visibility), DefaultRole: authz.Role(strings.TrimSpace(body.DefaultRole)), Owner: body.Owner,
	})
	if err != nil {
		nexoAuthzWriteError(w, err)
		return
	}
	jsonResponse(w, http.StatusCreated, info)
}

type nexoUpdateScopeBody struct {
	Visibility  *string `json:"visibility"`
	DefaultRole *string `json:"default_role"`
}

func nexoScopeFromPath(r *http.Request, key string) authz.Scope {
	return authz.Scope{Type: authz.ScopeType(strings.TrimSpace(r.PathValue("type"))), Key: key}
}

func (s *CloudServer) handleNexoAuthzUpdateScope(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	var body nexoUpdateScopeBody
	if err := decodeJSONBody(r, &body); err != nil {
		nexoAuthzBadRequest(w, fmt.Sprintf("invalid body: %v", err))
		return
	}
	in := authz.UpdateScopeInput{Visibility: body.Visibility}
	if body.DefaultRole != nil {
		role := authz.Role(strings.TrimSpace(*body.DefaultRole))
		in.DefaultRole = &role
	}
	info, err := svc.UpdateScope(r.Context(), id, nexoScopeFromPath(r, r.PathValue("key")), in)
	if err != nil {
		nexoAuthzWriteError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, info)
}

type nexoOwnerBody struct {
	Owner string `json:"owner"`
}

// handleNexoAuthzScopeAction atiende {key}:archive, :restore y :recover (el
// mux de Go no admite un sufijo fijo dentro de un segmento).
func (s *CloudServer) handleNexoAuthzScopeAction(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	raw := r.PathValue("keyAction")
	i := strings.LastIndex(raw, ":")
	if i <= 0 {
		writeActionableError(w, http.StatusNotFound, constants.UpgradeErrorClassRepairable, authz.CodeNotFound, "unknown scope action: use {key}:archive, {key}:restore or {key}:recover")
		return
	}
	sc := nexoScopeFromPath(r, raw[:i])
	switch raw[i+1:] {
	case "archive", "restore":
		info, err := svc.SetScopeArchived(r.Context(), id, sc, raw[i+1:] == "archive")
		if err != nil {
			nexoAuthzWriteError(w, err)
			return
		}
		jsonResponse(w, http.StatusOK, info)
	case "recover":
		var body nexoOwnerBody
		if err := decodeJSONBody(r, &body); err != nil {
			nexoAuthzBadRequest(w, fmt.Sprintf("invalid body: %v", err))
			return
		}
		change, err := svc.RecoverScope(r.Context(), id, sc, body.Owner)
		if err != nil {
			nexoAuthzWriteError(w, err)
			return
		}
		jsonResponse(w, http.StatusOK, change)
	default:
		writeActionableError(w, http.StatusNotFound, constants.UpgradeErrorClassRepairable, authz.CodeNotFound, "unknown scope action: use :archive, :restore or :recover")
	}
}

type nexoScopeMembersPage struct {
	Scope      authz.ScopeInfo `json:"scope"`
	Items      []authz.Member  `json:"items"`
	NextCursor *string         `json:"next_cursor"`
	Limit      int             `json:"limit"`
}

func (s *CloudServer) handleNexoAuthzScopeMembers(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	p := nexoParams{values: r.URL.Query()}
	q := authz.MemberQuery{Role: authz.Role(p.str("role")), Cursor: p.str("cursor"), Limit: p.limit()}
	if p.fail(w) {
		return
	}
	view, err := svc.ListScopeMembers(r.Context(), id, nexoScopeFromPath(r, r.PathValue("key")), q)
	if err != nil {
		nexoAuthzWriteError(w, err)
		return
	}
	page := newNexoPage(view.Members, view.Next, authz.NormalizeLimit(q.Limit))
	jsonResponse(w, http.StatusOK, nexoScopeMembersPage{Scope: view.Scope, Items: page.Items, NextCursor: page.NextCursor, Limit: page.Limit})
}

func (s *CloudServer) handleNexoAuthzSetScopeMember(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	var body nexoRoleBody
	if err := decodeJSONBody(r, &body); err != nil {
		nexoAuthzBadRequest(w, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if strings.TrimSpace(body.Role) == "" {
		nexoAuthzBadRequest(w, "role is required (lector, proponente, publicador or responsable)")
		return
	}
	change, err := svc.SetScopeMember(r.Context(), id, nexoScopeFromPath(r, r.PathValue("key")), r.PathValue("principal"), authz.Role(strings.TrimSpace(body.Role)))
	if err != nil {
		nexoAuthzWriteError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, change)
}

func (s *CloudServer) handleNexoAuthzDeleteScopeMember(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	change, err := svc.SetScopeMember(r.Context(), id, nexoScopeFromPath(r, r.PathValue("key")), r.PathValue("principal"), authz.RoleNone)
	if err != nil {
		nexoAuthzWriteError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, change)
}

// ─── Eventos (también GET /nexo/admin/authz-events) ──────────────────────────

// handleNexoAuthzEvents lista nexo_authz_events (platform.audit.read). Como
// las lecturas de NX-03, la consulta queda en cloud_auth_audit_log
// (nexo.authz_events.read); si no se puede registrar, no se entregan datos.
func (s *CloudServer) handleNexoAuthzEvents(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.nexoAuthz()
	if !ok {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, "nexo authz store is not configured")
		return
	}
	r, id := nexoWithAuthzIdentity(r)
	if !id.Managed() {
		svc.Engine().RecordDenied(r.Context(), id, authz.PlatformScope(), authz.CapPlatformAuditRead, authz.Decision{Reason: authz.ReasonUnmanaged}, map[string]any{"route": r.Pattern})
		writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden, "forbidden: a managed user token is required")
		return
	}
	p := nexoParams{values: r.URL.Query()}
	q := authz.EventQuery{
		ScopeType: p.str("scope_type"),
		ScopeKey:  p.str("scope_key"),
		Action:    p.str("action"),
		Principal: p.str("principal"),
		From:      p.time("from", false),
		To:        p.time("to", true),
		Cursor:    p.str("cursor"),
		Limit:     p.limit(),
	}
	if q.ScopeType != "" && !authz.ScopeType(q.ScopeType).Valid() {
		p.errs = append(p.errs, "scope_type must be platform, project or space")
	}
	if a := q.Action; a != "" && !strings.HasSuffix(a, "*") && !slices.Contains(authz.EventActions, a) {
		p.errs = append(p.errs, "action must be one of "+strings.Join(authz.EventActions, ", ")+" or a prefix ending in *")
	}
	if p.fail(w) {
		return
	}
	items, next, err := svc.ListEvents(r.Context(), id, q)
	if err != nil {
		nexoAuthzWriteError(w, err)
		return
	}
	if store, ok := s.adminStore(w); ok {
		principal, _ := PrincipalFromContext(r.Context())
		if !s.nexoAuditRead(w, r, store, principal, nexoAuditActionAuthzEventsRead, "", p.filters(), len(items)) {
			return
		}
	} else {
		return
	}
	jsonResponse(w, http.StatusOK, newNexoPage(items, next, authz.NormalizeLimit(q.Limit)))
}
