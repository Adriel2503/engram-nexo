package cloudserver

// Nexo IA · K6 (KP-12) — miembros de un espacio de conocimiento en el panel.
//
//	GET  /dashboard/kb/spaces/{slug}/members          quién tiene cada rol (space.read o platform.directory.read)
//	POST /dashboard/kb/spaces/{slug}/members          agregar o cambiar el rol (space.members.manage)
//	POST /dashboard/kb/spaces/{slug}/members/remove   quitar (space.members.manage)
//
// Usa los mismos casos de uso y reglas que la API /nexo/authz/v1
// (authz.Service.ListScopeMembers y SetScopeMember: R-1, R-9, R-10, R-11) y
// deja los mismos eventos nexo_authz_events. La identidad sale del token de
// la sesión y la sesión hereda su alcance; los formularios llevan el token
// CSRF del panel de conocimiento. Los rechazos de las reglas salen en español
// y se muestran en la misma página.

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/a-h/templ"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/dashboard"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	kbservice "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

// maxMembersShown acota el listado de la página (la API pagina el resto).
const maxMembersShown = 1000

func (s *CloudServer) registerNexoKBMembersDashboard(mux *http.ServeMux) {
	s.nexoRoute(mux, "GET", "/dashboard/kb/spaces/{slug}/members", "space.read|platform.directory.read", s.requireDashboardSession(s.nexoKBPage(s.kbMembersPage)))
	s.nexoRoute(mux, "POST", "/dashboard/kb/spaces/{slug}/members", "space.members.manage", s.requireDashboardSession(s.nexoKBPage(s.kbMembersSet)))
	s.nexoRoute(mux, "POST", "/dashboard/kb/spaces/{slug}/members/remove", "space.members.manage", s.requireDashboardSession(s.nexoKBPage(s.kbMembersRemove)))
}

// kbMembersState es lo que una acción deja para la página.
type kbMembersState struct {
	notice string
	err    string
	status int
}

// kbMembersErrText traduce un error del servicio de roles a un texto que dice
// qué pasó y qué hacer. El estado HTTP acompaña al texto.
func (s *CloudServer) kbMembersErrText(err error) (int, string) {
	var denied *authz.DeniedError
	var rule *authz.RuleError
	switch {
	case errors.As(err, &denied):
		if denied.Decision.Hidden {
			return http.StatusNotFound, "No encontramos el espacio, o no tienes acceso. Revisa el enlace o pide acceso al responsable del espacio."
		}
		return http.StatusForbidden, "Tu rol o tu token no permiten esta acción. Solo el responsable del espacio cambia sus miembros; pídeselo, o entra con un token de escritura."
	case errors.As(err, &rule):
		switch rule.Code {
		case authz.CodeServiceAccountRole:
			return http.StatusUnprocessableEntity, "Una cuenta de servicio no puede ser responsable de un espacio. Dale el rol publicador o proponente."
		case authz.CodeLastOwner:
			return http.StatusConflict, "Todo espacio necesita al menos un responsable activo. Nombra a otro responsable antes de quitar o cambiar el rol de esta persona."
		case authz.CodeSelfPromotion:
			return http.StatusForbidden, "Nadie puede darse a sí mismo un rol más alto que el que ya tiene. Pídeselo a otro responsable."
		case authz.CodeNotFound:
			return http.StatusNotFound, "No encontramos a ese usuario o cuenta de servicio, o el espacio no existe. Escribe el nombre de usuario o el nombre corto de la cuenta tal como figura en Administración."
		case authz.CodePrincipalDisabled:
			return http.StatusConflict, "La persona o la cuenta está deshabilitada. Habilítala en Administración antes de darle un rol."
		case authz.CodeInvalid:
			return http.StatusUnprocessableEntity, "No se pudo completar: " + rule.Message
		}
		return http.StatusConflict, "No se pudo completar: " + rule.Message
	}
	nexoLogInternal("dashboard kb members", err)
	return http.StatusInternalServerError, "Ocurrió un error interno. Vuelve a intentarlo; si se repite, avisa al administrador."
}

func (s *CloudServer) kbMembersPage(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	s.kbMembersRender(w, r, svc, id, r.PathValue("slug"), kbMembersState{})
}

func kbMemberRoleOptions(forServiceAccount bool) []dashboard.NexoRoleOption {
	var out []dashboard.NexoRoleOption
	for _, role := range []authz.Role{authz.RoleLector, authz.RoleProponente, authz.RolePublicador, authz.RoleResponsable} {
		if forServiceAccount && role == authz.RoleResponsable {
			continue // R-10
		}
		out = append(out, dashboard.NexoRoleOption{Value: string(role), Label: kbRole(string(role))})
	}
	return out
}

// kbSpaceHead arma el encabezado común. El nombre y la descripción salen del
// espacio si el usuario puede leerlo; si solo lee el directorio, del slug.
func (s *CloudServer) kbSpaceHead(r *http.Request, svc *kbservice.Service, id authz.Identity, slug, active string, openProposals int) dashboard.NexoSpaceHead {
	head := dashboard.NexoSpaceHead{Slug: slug, Name: slug, Active: active, OpenProposals: openProposals, Visibility: "Espacio de conocimiento", Role: kbRole("")}
	if sp, err := svc.GetSpace(r.Context(), id, slug); err == nil {
		if sp.Name != "" {
			head.Name = sp.Name
		}
		head.Description = sp.Description
		head.Visibility = kbVisibility(sp.Visibility)
		head.Role = kbRole(sp.Role)
	}
	return head
}

// kbMembersRender arma la página de miembros con lo que dejó la acción.
func (s *CloudServer) kbMembersRender(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity, slug string, st kbMembersState) {
	az, ok := s.nexoAuthz()
	if !ok {
		http.Error(w, "knowledge spaces are not available on this server", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	scope := authz.SpaceScope(slug)
	var members []authz.Member
	var info authz.ScopeInfo
	cursor := ""
	for {
		view, err := az.ListScopeMembers(ctx, id, scope, authz.MemberQuery{Cursor: cursor, Limit: authz.MaxLimit})
		if err != nil {
			status, msg := s.kbMembersErrText(err)
			s.kbFail(w, r, status, msg)
			return
		}
		info = view.Scope
		members = append(members, view.Members...)
		if view.Next == "" || len(members) >= maxMembersShown {
			break
		}
		cursor = view.Next
	}
	canManage := false
	if d, err := az.Engine().Can(ctx, id, scope, authz.CapSpaceMembersManage); err == nil {
		canManage = d.Allowed
	}
	page := dashboard.NexoMembersPage{
		Head:      s.kbSpaceHead(r, svc, id, slug, "members", 0),
		CSRF:      s.kbCSRFToken(r),
		Notice:    st.notice,
		Error:     st.err,
		CanManage: canManage,
		Total:     len(members),
		Truncated: len(members) >= maxMembersShown,
		Roles:     kbMemberRoleOptions(false),
	}
	if info.Visibility == authz.VisibilityOpen {
		page.Implicit = kbRoleLower(string(info.DefaultRole))
	}
	if n, ok := s.kbOpenProposalCount(r, svc, id, slug); ok {
		page.Head.OpenProposals = n
	}
	groups := map[authz.Role]*dashboard.NexoMemberGroup{}
	order := []authz.Role{authz.RoleResponsable, authz.RolePublicador, authz.RoleProponente, authz.RoleLector}
	for _, role := range order {
		groups[role] = &dashboard.NexoMemberGroup{Role: string(role), Label: kbRolePlural(role)}
	}
	for _, m := range members {
		g, ok := groups[m.Role]
		if !ok {
			continue
		}
		row := dashboard.NexoMemberRow{
			Ref: m.PrincipalID, Name: m.DisplayName, Username: m.Username, Kind: "Persona",
			IsSA: m.Kind == authz.KindServiceAccount, Disabled: !m.Enabled, Role: string(m.Role),
		}
		if row.Name == "" {
			row.Name = m.Username
		}
		if row.IsSA {
			row.Kind = "Cuenta de servicio"
		}
		row.Options = kbMemberRoleOptions(row.IsSA)
		if m.UpdatedAt != nil {
			row.Since = kbDay(m.UpdatedAt.UTC())
		}
		g.Members = append(g.Members, row)
	}
	for _, role := range order {
		if g := groups[role]; len(g.Members) > 0 {
			page.Groups = append(page.Groups, *g)
		}
	}
	// Sugerencias de usuarios: solo con lectura del directorio (no se expone
	// la lista de personas a quien no la tiene).
	if canManage {
		if d, err := az.Engine().Can(ctx, id, authz.PlatformScope(), authz.CapPlatformDirectoryRead); err == nil && d.Allowed {
			if dir, _, err := az.ListPlatformMembers(ctx, id, authz.MemberQuery{Limit: authz.MaxLimit}); err == nil {
				for _, m := range dir {
					if m.Enabled && m.Username != "" {
						page.Candidates = append(page.Candidates, m.Username)
					}
				}
			}
		}
	}
	status := st.status
	if status == 0 {
		status = http.StatusOK
	}
	s.kbRenderComponent(w, r, status, "Miembros de "+page.Head.Name, "kb", dashboard.NexoMembersPageView(page))
}

func kbRoleLower(r string) string { return strings.ToLower(kbRole(r)) }

func kbRolePlural(r authz.Role) string {
	switch r {
	case authz.RoleResponsable:
		return "Responsables"
	case authz.RolePublicador:
		return "Publicadores"
	case authz.RoleProponente:
		return "Proponentes"
	}
	return "Lectores"
}

// kbMembersSet agrega un miembro o cambia su rol.
func (s *CloudServer) kbMembersSet(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	s.kbMembersApply(w, r, svc, id, false)
}

// kbMembersRemove quita a un miembro.
func (s *CloudServer) kbMembersRemove(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	s.kbMembersApply(w, r, svc, id, true)
}

func (s *CloudServer) kbMembersApply(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity, remove bool) {
	slug := r.PathValue("slug")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulario inválido", http.StatusBadRequest)
		return
	}
	az, ok := s.nexoAuthz()
	if !ok {
		http.Error(w, "knowledge spaces are not available on this server", http.StatusServiceUnavailable)
		return
	}
	fail := func(status int, msg string) {
		s.kbMembersRender(w, r, svc, id, slug, kbMembersState{err: msg, status: status})
	}
	ref := strings.TrimSpace(r.PostForm.Get("member"))
	if ref == "" {
		fail(http.StatusUnprocessableEntity, "Escribe el usuario o el nombre corto de la cuenta de servicio.")
		return
	}
	role := authz.RoleNone
	if !remove {
		role = authz.Role(strings.TrimSpace(r.PostForm.Get("role")))
		if !authz.StorableRole(authz.ScopeSpace, role) {
			fail(http.StatusUnprocessableEntity, "Elige un rol válido: lector, proponente, publicador o responsable.")
			return
		}
	}
	change, err := az.SetScopeMember(r.Context(), id, authz.SpaceScope(slug), ref, role)
	if err != nil {
		status, msg := s.kbMembersErrText(err)
		fail(status, msg)
		return
	}
	who := ref
	st := kbMembersState{}
	switch {
	case !change.Changed && remove:
		st.notice = who + " no tenía un rol asignado en este espacio. No hubo cambios."
	case !change.Changed:
		st.notice = who + " ya tiene el rol " + kbRoleLower(string(role)) + ". No hubo cambios."
	case remove:
		st.notice = "Quitamos a " + who + " del espacio (tenía el rol " + kbRoleLower(string(change.RoleBefore)) + ")."
	case change.RoleBefore == authz.RoleNone:
		st.notice = "Agregamos a " + who + " como " + kbRoleLower(string(role)) + "."
	default:
		st.notice = "Cambiamos el rol de " + who + " de " + kbRoleLower(string(change.RoleBefore)) + " a " + kbRoleLower(string(role)) + "."
	}
	s.kbMembersRender(w, r, svc, id, slug, st)
}

// kbSpaceSectionsHTML dibuja la fila de secciones (Documentos, Miembros,
// Propuestas) para las páginas del espacio que se arman con texto.
func (s *CloudServer) kbSpaceSectionsHTML(r *http.Request, svc *kbservice.Service, id authz.Identity, slug, active string) string {
	head := dashboard.NexoSpaceHead{Slug: slug, Active: active}
	if n, ok := s.kbOpenProposalCount(r, svc, id, slug); ok {
		head.OpenProposals = n
	}
	h, err := templ.ToGoHTML(r.Context(), dashboard.NexoSpaceSections(head))
	if err != nil {
		nexoLogInternal("dashboard kb sections", err)
		return ""
	}
	return string(h)
}

// kbMembersStepLink enlaza la sección Miembros del primer espacio que el
// usuario puede administrar (paso 2 de "Cómo funciona").
func kbMembersStepLink(spaces []domain.Space) string {
	for _, sp := range spaces {
		if kbHasCap(sp.Capabilities, authz.CapSpaceMembersManage) {
			return `<a href="/dashboard/kb/spaces/` + url.PathEscape(sp.Slug) + `/members">Gestionar miembros</a>`
		}
	}
	return ""
}

// kbOpenProposalCount cuenta las propuestas abiertas que ve quien mira en un
// espacio (las que puede revisar y las suyas).
func (s *CloudServer) kbOpenProposalCount(r *http.Request, svc *kbservice.Service, id authz.Identity, slug string) (int, bool) {
	items, err := svc.ListProposals(r.Context(), id, kbservice.ProposalQuery{Space: slug, Status: domain.ProposalOpen, Limit: 200})
	if err != nil {
		return 0, false
	}
	return len(items), true
}
