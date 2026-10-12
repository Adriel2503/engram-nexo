// Package authz es el motor de autorización de Nexo IA (NX-05).
//
// Un único modelo con tres niveles de ámbito (plataforma, proyecto, espacio).
// En cada nivel un rol es un conjunto de capacidades; el código comprueba
// capacidades, nunca nombres de rol. Agregar un rol es agregar una fila a las
// tablas de este archivo (y el valor permitido en el trigger de combinaciones
// de migrate.go), sin tocar rutas ni datos de contenido (RNF-R3).
//
// Especificación: docs/nexo/roles/requirements.md (§4 capacidades, §5 reglas)
// y docs/nexo/roles/design.md (§3 motor).
package authz

import (
	"slices"
	"strings"
)

// ScopeType es el nivel de un ámbito.
type ScopeType string

const (
	ScopePlatform ScopeType = "platform"
	ScopeProject  ScopeType = "project"
	ScopeSpace    ScopeType = "space"
)

// PlatformKey es la clave única del ámbito de plataforma.
const PlatformKey = "*"

// Valid informa si t es un nivel conocido.
func (t ScopeType) Valid() bool {
	return t == ScopePlatform || t == ScopeProject || t == ScopeSpace
}

// Scope identifica un ámbito: (platform, "*"), (project, nombre normalizado)
// o (space, slug).
type Scope struct {
	Type ScopeType `json:"type"`
	Key  string    `json:"key"`
}

// PlatformScope devuelve el ámbito de plataforma.
func PlatformScope() Scope { return Scope{Type: ScopePlatform, Key: PlatformKey} }

// ProjectScope y SpaceScope son atajos. La clave del proyecto se normaliza en
// el motor con la misma normalización que cloud_project_grants.
func ProjectScope(name string) Scope { return Scope{Type: ScopeProject, Key: name} }
func SpaceScope(slug string) Scope   { return Scope{Type: ScopeSpace, Key: slug} }

func (s Scope) String() string { return string(s.Type) + ":" + s.Key }

// Role es un rol de un nivel. RoleNone ("") significa ninguno.
type Role string

const (
	RoleNone Role = ""

	// Plataforma. "miembro" es la ausencia de fila: no se guarda.
	RoleSuperadmin Role = "superadmin"
	RoleAdmin      Role = "admin"
	RoleAuditor    Role = "auditor"
	RoleMiembro    Role = "miembro"

	// Proyectos y espacios (los mismos roles, ordenados).
	RoleLector      Role = "lector"
	RoleProponente  Role = "proponente"
	RolePublicador  Role = "publicador"
	RoleResponsable Role = "responsable"
)

// Capability es lo que el código comprueba.
type Capability string

const (
	// Plataforma (requisitos §4.1).
	CapPlatformAdminsManage     Capability = "platform.admins.manage"
	CapPlatformSettings         Capability = "platform.settings"
	CapPlatformScopeRecover     Capability = "platform.scope.recover"
	CapPlatformUsersManage      Capability = "platform.users.manage"
	CapPlatformTokensManage     Capability = "platform.tokens.manage"
	CapPlatformScopeCreate      Capability = "platform.scope.create"
	CapPlatformScopeArchive     Capability = "platform.scope.archive"
	CapPlatformScopeAssignOwner Capability = "platform.scope.assign_owner"
	CapPlatformAuditRead        Capability = "platform.audit.read"
	CapPlatformDirectoryRead    Capability = "platform.directory.read"
	CapSelfRead                 Capability = "self.read"

	// Proyectos (§4.2).
	CapProjectRead          Capability = "project.read"
	CapProjectWriteOwn      Capability = "project.write.own"
	CapProjectWriteAny      Capability = "project.write.any"
	CapProjectAuditRead     Capability = "project.audit.read"
	CapProjectMembersManage Capability = "project.members.manage"
	CapProjectConfigure     Capability = "project.configure"

	// Espacios (§4.3).
	CapSpaceRead          Capability = "space.read"
	CapSpacePropose       Capability = "space.propose"
	CapSpacePublish       Capability = "space.publish"
	CapSpaceReview        Capability = "space.review"
	CapSpaceAuditRead     Capability = "space.audit.read"
	CapSpaceMembersManage Capability = "space.members.manage"
	CapSpaceConfigure     Capability = "space.configure"
)

// Alcances de token (§4.4). "" equivale a write hasta NX-06.
const (
	TokenScopeRead  = "read"
	TokenScopeWrite = "write"
)

// levelRoles: orden de roles por nivel. Cada rol hereda las capacidades de
// los anteriores de su nivel y agrega las suyas (roleAdds).
var levelRoles = map[ScopeType][]Role{
	ScopePlatform: {RoleMiembro, RoleAuditor, RoleAdmin, RoleSuperadmin},
	ScopeProject:  {RoleLector, RoleProponente, RolePublicador, RoleResponsable},
	ScopeSpace:    {RoleLector, RoleProponente, RolePublicador, RoleResponsable},
}

var roleAdds = map[ScopeType]map[Role][]Capability{
	ScopePlatform: {
		RoleMiembro: {CapSelfRead},
		RoleAuditor: {CapPlatformAuditRead, CapPlatformDirectoryRead},
		RoleAdmin: {CapPlatformUsersManage, CapPlatformTokensManage, CapPlatformScopeCreate,
			CapPlatformScopeArchive, CapPlatformScopeAssignOwner},
		RoleSuperadmin: {CapPlatformAdminsManage, CapPlatformSettings, CapPlatformScopeRecover},
	},
	ScopeProject: {
		RoleLector:      {CapProjectRead},
		RoleProponente:  {CapProjectWriteOwn},
		RolePublicador:  {CapProjectWriteAny, CapProjectAuditRead},
		RoleResponsable: {CapProjectMembersManage, CapProjectConfigure},
	},
	ScopeSpace: {
		RoleLector:      {CapSpaceRead},
		RoleProponente:  {CapSpacePropose},
		RolePublicador:  {CapSpacePublish, CapSpaceReview, CapSpaceAuditRead},
		RoleResponsable: {CapSpaceMembersManage, CapSpaceConfigure},
	},
}

// roleCaps se calcula una vez a partir de levelRoles y roleAdds.
var roleCaps = buildRoleCaps()

func buildRoleCaps() map[ScopeType]map[Role]map[Capability]struct{} {
	out := map[ScopeType]map[Role]map[Capability]struct{}{}
	for level, roles := range levelRoles {
		out[level] = map[Role]map[Capability]struct{}{}
		acc := map[Capability]struct{}{}
		for _, role := range roles {
			for _, c := range roleAdds[level][role] {
				acc[c] = struct{}{}
			}
			caps := make(map[Capability]struct{}, len(acc))
			for c := range acc {
				caps[c] = struct{}{}
			}
			out[level][role] = caps
		}
	}
	return out
}

// Roles devuelve los roles de un nivel en orden ascendente.
func Roles(t ScopeType) []Role { return slices.Clone(levelRoles[t]) }

// ValidRole informa si r es un rol del nivel t (incluye miembro en plataforma).
func ValidRole(t ScopeType, r Role) bool { return slices.Contains(levelRoles[t], r) }

// StorableRole informa si r se guarda en nexo_role_bindings para el nivel t
// (miembro de plataforma no se guarda).
func StorableRole(t ScopeType, r Role) bool {
	return ValidRole(t, r) && !(t == ScopePlatform && r == RoleMiembro)
}

// Rank devuelve la posición del rol en su nivel (-1 si no pertenece). Sirve
// para la regla de no auto-promoción (R-11).
func Rank(t ScopeType, r Role) int {
	if t == ScopePlatform && r == RoleNone {
		r = RoleMiembro
	}
	return slices.Index(levelRoles[t], r)
}

// LevelOf devuelve el nivel al que pertenece una capacidad.
func LevelOf(c Capability) (ScopeType, bool) {
	s := string(c)
	switch {
	case c == CapSelfRead, strings.HasPrefix(s, "platform."):
		return ScopePlatform, knownCapability(ScopePlatform, c)
	case strings.HasPrefix(s, "project."):
		return ScopeProject, knownCapability(ScopeProject, c)
	case strings.HasPrefix(s, "space."):
		return ScopeSpace, knownCapability(ScopeSpace, c)
	}
	return "", false
}

func knownCapability(t ScopeType, c Capability) bool {
	roles := levelRoles[t]
	_, ok := roleCaps[t][roles[len(roles)-1]][c]
	return ok
}

// AllCapabilities devuelve las capacidades de un nivel, ordenadas.
func AllCapabilities(t ScopeType) []Capability {
	roles := levelRoles[t]
	return sortedCaps(roleCaps[t][roles[len(roles)-1]])
}

// RoleHas informa si el rol r del nivel t incluye la capacidad c (sin
// considerar el alcance del token).
func RoleHas(t ScopeType, r Role, c Capability) bool {
	if t == ScopePlatform && r == RoleNone {
		r = RoleMiembro
	}
	_, ok := roleCaps[t][r][c]
	return ok
}

// Capabilities devuelve las capacidades efectivas de un rol con un alcance
// de token (R-2: rol ∩ alcance del token), ordenadas.
func Capabilities(t ScopeType, r Role, tokenScope string) []Capability {
	if t == ScopePlatform && r == RoleNone {
		r = RoleMiembro
	}
	out := map[Capability]struct{}{}
	for c := range roleCaps[t][r] {
		if TokenScopeAllows(tokenScope, c) {
			out[c] = struct{}{}
		}
	}
	return sortedCaps(out)
}

// TokenScopeAllows aplica el alcance del token (§4.4): un token "read" solo
// admite capacidades de lectura (*.read, *.audit.read, self.read); "write" o
// vacío (hasta NX-06) admite todas las del rol. Un alcance desconocido no
// admite nada (fallar cerrado).
func TokenScopeAllows(tokenScope string, c Capability) bool {
	switch strings.TrimSpace(tokenScope) {
	case "", TokenScopeWrite:
		return true
	case TokenScopeRead:
		return strings.HasSuffix(string(c), ".read")
	default:
		return false
	}
}

// NormalizeTokenScope devuelve el alcance efectivo ("write" si está vacío).
func NormalizeTokenScope(tokenScope string) string {
	if strings.TrimSpace(tokenScope) == "" {
		return TokenScopeWrite
	}
	return strings.TrimSpace(tokenScope)
}

func sortedCaps(set map[Capability]struct{}) []Capability {
	out := make([]Capability, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}
