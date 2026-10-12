package authz

import (
	"context"
	"strconv"
	"strings"
	"sync"
)

// Identity es la identidad ya resuelta de una petición. La arma un adaptador
// del servidor (cloudserver/nexo_authz_bridge.go) a partir del token; este
// paquete no conoce HTTP ni cloudserver.
type Identity struct {
	PrincipalID string // id de cloud_principals como texto ("" = sin identidad asignada)
	TokenID     string // id de cloud_principal_tokens como texto
	Kind        string // human | service_account | legacy
	Label       string // nombre mostrado (copia para eventos)
	Verified    bool   // token administrado (NX-01); false para token compartido y modo inseguro
	Enabled     bool   // la persona está habilitada (el resolvedor de Engram ya rechaza deshabilitados)
	TokenScope  string // "" = write (hasta NX-06), "read", "write"
	EngramRole  string // admin | member (compatibilidad, requisitos §9)
	ClientIP    string
	UserAgent   string
	ClientClaim string
}

// Tipos de principal.
const (
	KindHuman          = "human"
	KindServiceAccount = "service_account"
)

// Managed informa si la identidad corresponde a un usuario administrado con
// su propio token (persona o cuenta de servicio). Solo esas identidades
// tienen roles (R-15).
func (id Identity) Managed() bool {
	if !id.Verified {
		return false
	}
	if id.Kind != KindHuman && id.Kind != KindServiceAccount {
		return false
	}
	_, ok := id.principalNumber()
	return ok
}

func (id Identity) principalNumber() (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(id.PrincipalID), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// Motivos de una decisión.
const (
	ReasonAllowed       = "allowed"
	ReasonUnmanaged     = "unmanaged_identity" // token compartido o modo inseguro (R-15)
	ReasonDisabled      = "disabled"           // persona deshabilitada o inexistente (R-13)
	ReasonNoRole        = "no_role"            // sin rol en el ámbito
	ReasonCapability    = "capability"         // el rol no incluye la capacidad
	ReasonTokenScope    = "token_scope"        // el alcance del token no la admite
	ReasonArchived      = "archived"           // ámbito archivado
	ReasonBadCapability = "invalid_capability" // la capacidad no pertenece al nivel del ámbito
	ReasonError         = "error"              // error al resolver: se falla cerrado
)

// Decision es el resultado de Can.
type Decision struct {
	Allowed bool   `json:"allowed"`
	Role    Role   `json:"role,omitempty"`
	Reason  string `json:"reason"`
	// Hidden: el ámbito no es visible para la identidad (restringido o
	// inexistente y sin rol). El llamador responde 404, no 403 (R-4).
	Hidden bool `json:"hidden,omitempty"`
}

// ScopeRole es un ámbito visible con el rol efectivo de la identidad.
type ScopeRole struct {
	Scope        Scope        `json:"scope"`
	Role         Role         `json:"role"`
	Source       string       `json:"source"` // binding | engram_grant | default_role
	Visibility   string       `json:"visibility"`
	DefaultRole  Role         `json:"default_role,omitempty"`
	Status       string       `json:"status"`
	Capabilities []Capability `json:"capabilities"`
}

// Fuentes del rol efectivo.
const (
	SourceBinding     = "binding"
	SourceEngramGrant = "engram_grant"
	SourceDefaultRole = "default_role"
	SourceEngramAdmin = "engram_admin"
	SourceBootstrap   = "bootstrap"
	SourceNone        = "none"
)

// Visibilidad y estado de un ámbito.
const (
	VisibilityOpen       = "open"
	VisibilityRestricted = "restricted"
	StatusActive         = "active"
	StatusArchived       = "archived"
)

// ─── Identidad en el contexto ────────────────────────────────────────────────

type identityKey struct{}

// WithIdentity guarda la identidad para los módulos Nexo (espacios, API de
// roles) sin que dependan de cloudserver.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom devuelve la identidad guardada con WithIdentity.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	if ctx == nil {
		return Identity{}, false
	}
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// ─── Caché por petición ──────────────────────────────────────────────────────

type cacheKey struct{}

type requestCache struct {
	mu    sync.Mutex
	roles map[string]resolved
}

// WithRequestCache habilita una caché de roles resueltos para la vida de la
// petición: varias comprobaciones sobre el mismo ámbito cuestan una sola
// consulta (design §7). No hay caché entre peticiones: un cambio de rol se ve
// en la siguiente petición (R-13).
func WithRequestCache(ctx context.Context) context.Context {
	if _, ok := ctx.Value(cacheKey{}).(*requestCache); ok {
		return ctx
	}
	return context.WithValue(ctx, cacheKey{}, &requestCache{roles: map[string]resolved{}})
}

func cacheFrom(ctx context.Context) *requestCache {
	c, _ := ctx.Value(cacheKey{}).(*requestCache)
	return c
}

func (c *requestCache) get(key string) (resolved, bool) {
	if c == nil {
		return resolved{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.roles[key]
	return r, ok
}

func (c *requestCache) put(key string, r resolved) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.roles[key] = r
}

func (c *requestCache) reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.roles = map[string]resolved{}
}
