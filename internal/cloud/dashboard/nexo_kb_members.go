package dashboard

// Nexo IA · K6 (KP-12, KP-13) — datos de las páginas de miembros y de
// propuestas de un espacio (nexo_kb_members.templ, nexo_kb_proposals.templ).
// Los arma cloudserver (nexo_kb_members.go, nexo_kb_proposals.go) con los
// mismos casos de uso que la API; aquí solo hay texto ya formateado, sin tipos
// de Nexo. Todo se escapa en la plantilla (Markdown ya renderizado aparte).

import (
	"net/url"
	"strconv"
)

// NexoSpaceHead es el encabezado común de las páginas de un espacio.
type NexoSpaceHead struct {
	Slug        string
	Name        string
	Description string
	Visibility  string // texto: "Abierto a todo el equipo"
	Role        string // texto: rol de quien mira
	Active      string // "docs" | "members" | "proposals"
	// OpenProposals es el número de propuestas abiertas visibles para quien mira.
	OpenProposals int
}

// NexoMembersPage es /dashboard/kb/spaces/{slug}/members.
type NexoMembersPage struct {
	Head      NexoSpaceHead
	CSRF      string
	Notice    string
	Error     string
	CanManage bool
	// Implicit explica el rol que tienen sin asignación en un espacio abierto.
	Implicit string
	Groups   []NexoMemberGroup
	Total    int
	// Roles son las opciones de rol al agregar.
	Roles []NexoRoleOption
	// Candidates son nombres de usuario sugeridos (solo con lectura de directorio).
	Candidates []string
	// Truncated avisa que el listado se cortó en el máximo mostrado.
	Truncated bool
}

// NexoRoleOption es una opción de rol.
type NexoRoleOption struct {
	Value string
	Label string
}

// NexoMemberGroup agrupa a los miembros de un rol.
type NexoMemberGroup struct {
	Role    string
	Label   string
	Members []NexoMemberRow
}

// NexoMemberRow es un miembro con su rol.
type NexoMemberRow struct {
	Ref      string // identificador para los formularios (id del principal)
	Name     string
	Username string
	Kind     string // "Persona" | "Cuenta de servicio"
	IsSA     bool
	Disabled bool
	Role     string
	Since    string
	// Options son los roles a los que se puede cambiar (sin responsable para una cuenta de servicio, R-10).
	Options []NexoRoleOption
}

// NexoProposalsPage es el listado de propuestas (global o de un espacio).
type NexoProposalsPage struct {
	Title   string
	Lead    string
	Head    *NexoSpaceHead // nil en el listado global
	Error   string
	Filters []NexoFilter
	Items   []NexoProposalRow
	Empty   string
	// Hint explica cómo se crean propuestas cuando el listado está vacío.
	Hint string
}

// NexoFilter es un filtro del listado.
type NexoFilter struct {
	Label   string
	Href    string
	Current bool
}

// NexoProposalRow es una propuesta del listado.
type NexoProposalRow struct {
	Href        string
	Title       string
	Space       string
	Target      string
	Author      string
	Date        string
	StatusLabel string
	StatusClass string
}

// NexoProposalPage es el detalle de una propuesta.
type NexoProposalPage struct {
	Head        NexoSpaceHead
	CSRF        string
	Notice      string
	NoticeLink  string // enlace al documento publicado
	NoticeLinkT string
	Error       string
	ID          string
	Title       string
	StatusLabel string
	StatusClass string
	Open        bool
	Author      string
	Date        string
	Target      string
	Vigencia    string
	Rationale   string
	Category    string
	// Stale avisa que el documento cambió desde la versión que vio el autor.
	Stale       string
	CurrentHTML string // Markdown ya renderizado (mdhtml)
	CurrentName string // "Versión vigente (v3)"
	HasCurrent  bool
	ProposedHTML string
	// Decisión (propuesta cerrada).
	Decision string
	// Formulario de decisión (solo revisores y propuesta abierta).
	CanReview  bool
	ValidFrom  string
	ValidUntil string
	NeedsKey   bool
	DocKey     string
	ShowForce  bool
	ApproveURL string
	DiscardURL string
	// Valores devueltos tras un error de validación.
	FormNote   string
	FormReason string
}

func nexoMembersURL(slug string) string { return "/dashboard/kb/spaces/" + url.PathEscape(slug) + "/members" }
func nexoSpaceURL(slug string) string   { return "/dashboard/kb/spaces/" + url.PathEscape(slug) }
func nexoProposalsURL(slug string) string {
	return "/dashboard/kb/spaces/" + url.PathEscape(slug) + "/proposals"
}

func nexoItoa(n int) string { return strconv.Itoa(n) }

func nexoPendingLabel(n int) string { return "Propuestas (" + strconv.Itoa(n) + ")" }

func nexoMembersGridClass(canManage bool) string {
	if canManage {
		return "nx-space-grid"
	}
	return "nx-space-grid nx-single"
}

// nexoMembersLead dice qué rol tienen quienes no aparecen y quién cambia roles.
func nexoMembersLead(p NexoMembersPage) string {
	lead := ""
	if p.Implicit != "" {
		lead = "Espacio abierto: quienes no aparecen aquí tienen el rol " + p.Implicit + ". "
	} else {
		lead = "Espacio restringido: solo ven su contenido las personas de esta lista. "
	}
	if p.CanManage {
		return lead + "Tú puedes agregar miembros, cambiar su rol y quitarlos."
	}
	return lead + "Solo el responsable del espacio y los administradores cambian roles."
}

// nexoMemberLine es la línea gris de un miembro: usuario, tipo y desde cuándo.
func nexoMemberLine(m NexoMemberRow) string {
	line := m.Kind
	if m.Username != "" && m.Username != m.Name {
		line = m.Username + ", " + m.Kind
	}
	if m.Since != "" {
		line += ", con este rol desde " + m.Since
	}
	return line
}

// nexoProposalLine es la línea gris de una propuesta del listado.
func nexoProposalLine(it NexoProposalRow) string {
	line := it.Target
	if it.Space != "" {
		line = it.Space + ": " + line
	}
	return line + ". Propuso " + it.Author + " el " + it.Date
}

func nexoProposalGridClass(p NexoProposalPage) string {
	if p.CanReview && p.Open {
		return "nx-space-grid"
	}
	return "nx-space-grid nx-single"
}

func nexoCompareClass(hasCurrent bool) string {
	if hasCurrent {
		return "nx-compare"
	}
	return "nx-compare nx-compare-one"
}
