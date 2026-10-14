package dashboard

// Nexo IA · NX-05 (unificación de roles de plataforma) — datos y textos de
// Administración > Usuarios. Una persona tiene un solo rol de plataforma, el de
// Nexo (miembro, auditor, admin, superadmin); la columna role de Engram es un
// valor derivado y nunca se muestra "a secas". Los arma cloudserver
// (nexo_users_dashboard.go) con el motor de roles; aquí solo hay texto ya
// resuelto y las plantillas (nexo_users.templ).

import (
	"context"
	"net/http"

	"github.com/a-h/templ"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

// Valores de rol (los mismos de internal/nexo/authz).
const (
	NexoRoleMiembro    = "miembro"
	NexoRoleAuditor    = "auditor"
	NexoRoleAdmin      = "admin"
	NexoRoleSuperadmin = "superadmin"
)

// NexoRoleOrder es el orden de menor a mayor privilegio.
var NexoRoleOrder = []string{NexoRoleMiembro, NexoRoleAuditor, NexoRoleAdmin, NexoRoleSuperadmin}

// NexoRoleLabel es el nombre del rol en español.
func NexoRoleLabel(role string) string {
	switch role {
	case NexoRoleSuperadmin:
		return "Superadmin"
	case NexoRoleAdmin:
		return "Administrador"
	case NexoRoleAuditor:
		return "Auditor"
	}
	return "Miembro"
}

// NexoRoleDescription dice en una línea qué puede hacer el rol.
func NexoRoleDescription(role string) string {
	switch role {
	case NexoRoleSuperadmin:
		return "Todo lo del administrador, y además asigna roles de plataforma, gestiona a otros administradores y recupera espacios sin responsable."
	case NexoRoleAdmin:
		return "Crea miembros, emite sus tokens y crea espacios. No gestiona a otros administradores ni cambia roles de plataforma."
	case NexoRoleAuditor:
		return "Lee la auditoría y el directorio de personas. No cambia nada."
	}
	return "Usa la memoria y los espacios a los que le den acceso. No administra."
}

// NexoEngramView explica cómo ve Engram este rol (la columna derivada).
func NexoEngramView(role string) string {
	if role == NexoRoleSuperadmin || role == NexoRoleAdmin {
		return "Engram lo ve como administrador. Se deriva de este rol; no se edita aparte."
	}
	return "Engram lo ve como miembro. Se deriva de este rol; no se edita aparte."
}

// / NexoEngramShort es la equivalencia en una línea para el selector.
func NexoEngramShort(role string) string {
	if role == NexoRoleSuperadmin || role == NexoRoleAdmin {
		return "En Engram figura como admin"
	}
	return "En Engram figura como member"
}

// NexoRoleChipClass es la clase del chip del rol (estructura del tema:
// gris, azul, verde claro y verde pleno según el privilegio).
func NexoRoleChipClass(role string) string {
	switch role {
	case NexoRoleSuperadmin:
		return "nx-chip nx-chip-top"
	case NexoRoleAdmin:
		return "nx-chip nx-chip-ok"
	case NexoRoleAuditor:
		return "nx-chip nx-chip-sched"
	}
	return "nx-chip nx-chip-ret"
}

// NexoUserRoleOption es una opción del selector de rol.
type NexoUserRoleOption struct {
	Value    string
	Label    string
	Desc     string
	Disabled bool // la persona que mira no puede asignarlo (el servidor decide igual)
	Selected bool
}

// NexoUsersView son los datos que cloudserver da a las pantallas de usuarios.
type NexoUsersView struct {
	CSRF string
	// Options: los cuatro roles, con Disabled según quién mira.
	Options []NexoUserRoleOption
	// Roles: rol de plataforma de cada persona (id de principal -> valor).
	Roles map[string]string
	// CanChangeRole: quien mira puede cambiar roles (platform.admins.manage).
	CanChangeRole bool
}

func (v NexoUsersView) roleOf(principalID string) string {
	if r, ok := v.Roles[principalID]; ok && r != "" {
		return r
	}
	return NexoRoleMiembro
}

// NexoUserDetailView agrega lo propio del detalle.
type NexoUserDetailView struct {
	NexoUsersView
	Notice string
	Error  string
}

// optionsFor devuelve las opciones con la actual marcada.
func (v NexoUsersView) optionsFor(current string) []NexoUserRoleOption {
	out := make([]NexoUserRoleOption, len(v.Options))
	for i, o := range v.Options {
		o.Selected = o.Value == current
		out[i] = o
	}
	return out
}

// firstEnabled devuelve la opción marcada por defecto de un formulario de alta
// (miembro siempre, que cualquier administrador puede asignar).
func (v NexoUsersView) optionsForCreate() []NexoUserRoleOption {
	return v.optionsFor(NexoRoleMiembro)
}

// ─── Rol en la cabecera ──────────────────────────────────────────────────────

type nexoRoleCtxKey struct{}

// WithNexoRole deja en el contexto el rol de plataforma de quien mira; la
// cabecera lo muestra junto a su nombre. Lo pone cloudserver
// (nexo_audit_nav.go) después de leerlo del motor; es solo interfaz.
func WithNexoRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, nexoRoleCtxKey{}, role)
}

func nexoRoleFrom(ctx context.Context) string {
	v, _ := ctx.Value(nexoRoleCtxKey{}).(string)
	return v
}

// ─── Enganches de dashboard.go ───────────────────────────────────────────────

// nexoUsersPage devuelve la página de usuarios de Nexo si cloudserver la dio
// (NexoUsersView en MountConfig) y, si no, la de Engram.
func (h *handlers) nexoUsersPage(r *http.Request, canManage bool) templ.Component {
	if h.cfg.NexoUsersView == nil {
		return ManagedUsersPage(canManage)
	}
	return NexoManagedUsersPage(canManage, h.cfg.NexoUsersView(r))
}

// nexoUsersList devuelve la lista de Nexo (con el rol de Nexo) o la de Engram.
func (h *handlers) nexoUsersList(r *http.Request, rows []cloudstore.HumanUser, canManage bool) templ.Component {
	if h.cfg.NexoUsersView == nil {
		return ManagedUsersListPartial(rows, canManage)
	}
	return NexoManagedUsersListPartial(rows, canManage, h.cfg.NexoUsersView(r))
}
