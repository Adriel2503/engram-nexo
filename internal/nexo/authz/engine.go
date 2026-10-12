package authz

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Authorizer es la interfaz que consumen las rutas de plataforma, las de
// proyectos (etapa R2) y las de espacios (NX-10).
type Authorizer interface {
	// EffectiveRole devuelve el rol efectivo (RoleNone = ninguno). En
	// plataforma, una persona sin rol administrativo es RoleMiembro.
	EffectiveRole(ctx context.Context, id Identity, s Scope) (Role, error)
	// Can decide si la identidad tiene la capacidad en el ámbito. Con error
	// la decisión es siempre denegada (fallar cerrado).
	Can(ctx context.Context, id Identity, s Scope, c Capability) (Decision, error)
	// VisibleScopes lista los ámbitos del nivel que la identidad ve, con su
	// rol. Una sola consulta.
	VisibleScopes(ctx context.Context, id Identity, t ScopeType) ([]ScopeRole, error)
}

// PlatformFacts son los datos crudos para resolver el rol de plataforma.
type PlatformFacts struct {
	Found              bool   // existe el principal
	Enabled            bool   // habilitado
	Kind               string // human | service_account
	EngramRole         string // admin | member
	Binding            Role   // fila en nexo_role_bindings (RoleNone si no hay)
	BootstrapCandidate bool   // no hay superadmin habilitado y este es el admin humano de menor id
}

// ScopeFacts son los datos crudos para resolver el rol en un proyecto o espacio.
type ScopeFacts struct {
	Found       bool // existe el principal
	Enabled     bool
	Kind        string
	Exists      bool // hay fila en nexo_scopes
	Visibility  string
	DefaultRole Role
	Status      string
	Binding     Role
	EngramGrant bool // fila en cloud_project_grants (solo proyectos)
}

// ScopeFactsRow es un ámbito candidato de VisibleScopes.
type ScopeFactsRow struct {
	Key string
	ScopeFacts
}

// Store es la fuente de datos del motor. La implementación de producción es
// PGStore (Postgres); los tests de la matriz usan una en memoria.
type Store interface {
	PlatformFacts(ctx context.Context, principalID int64) (PlatformFacts, error)
	ScopeFacts(ctx context.Context, principalID int64, s Scope) (ScopeFacts, error)
	VisibleScopeFacts(ctx context.Context, principalID int64, t ScopeType) ([]ScopeFactsRow, error)
	EnsureBootstrapSuperadmin(ctx context.Context) error
	RecordEvent(ctx context.Context, ev Event) error
}

// Engine implementa Authorizer.
type Engine struct {
	store     Store
	normalize func(string) string
	now       func() time.Time
	network   NetworkPolicy // NX-07 (RS-13)
}

// Option configura el motor.
type Option func(*Engine)

// WithProjectNormalizer fija la normalización de nombres de proyecto; debe
// ser la misma de cloud_project_grants (cloudstore.NormalizeProjectGrant).
func WithProjectNormalizer(fn func(string) string) Option {
	return func(e *Engine) {
		if fn != nil {
			e.normalize = fn
		}
	}
}

// NewEngine crea un motor sobre un Store.
func NewEngine(store Store, opts ...Option) *Engine {
	e := &Engine{store: store, normalize: defaultProjectNormalizer, now: time.Now}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

func defaultProjectNormalizer(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

var spaceSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// ErrInvalidScope indica un tipo o clave de ámbito inválidos.
var ErrInvalidScope = errors.New("authz: invalid scope")

// NormalizeScope valida el tipo y normaliza la clave: plataforma "*",
// proyecto con la normalización de Engram, espacio como slug.
func (e *Engine) NormalizeScope(s Scope) (Scope, error) {
	switch s.Type {
	case ScopePlatform:
		if k := strings.TrimSpace(s.Key); k != "" && k != PlatformKey {
			return Scope{}, fmt.Errorf("%w: platform key must be %q", ErrInvalidScope, PlatformKey)
		}
		return PlatformScope(), nil
	case ScopeProject:
		key := e.normalize(s.Key)
		if key == "" {
			return Scope{}, fmt.Errorf("%w: project name is required", ErrInvalidScope)
		}
		return Scope{Type: ScopeProject, Key: key}, nil
	case ScopeSpace:
		key := strings.ToLower(strings.TrimSpace(s.Key))
		if !spaceSlugRe.MatchString(key) {
			return Scope{}, fmt.Errorf("%w: space slug must match %s", ErrInvalidScope, spaceSlugRe.String())
		}
		return Scope{Type: ScopeSpace, Key: key}, nil
	}
	return Scope{}, fmt.Errorf("%w: unknown scope type %q", ErrInvalidScope, s.Type)
}

// resolved es un rol ya calculado (se guarda en la caché de la petición).
type resolved struct {
	role    Role
	source  string
	enabled bool
	facts   ScopeFacts
}

// resolvePlatform aplica design §3 (plataforma).
func resolvePlatform(f PlatformFacts) (Role, string) {
	if !f.Found || !f.Enabled {
		return RoleNone, SourceNone
	}
	if f.Kind == KindServiceAccount {
		return RoleMiembro, SourceNone // R-10: una cuenta de servicio solo es miembro en plataforma
	}
	if f.Binding != RoleNone && StorableRole(ScopePlatform, f.Binding) {
		return f.Binding, SourceBinding
	}
	if f.BootstrapCandidate {
		return RoleSuperadmin, SourceBootstrap
	}
	if f.EngramRole == "admin" {
		return RoleAdmin, SourceEngramAdmin // compatibilidad (requisitos §9)
	}
	return RoleMiembro, SourceNone
}

// resolveScope aplica design §3 (proyectos y espacios) y R-3.
func resolveScope(t ScopeType, f ScopeFacts) (Role, string) {
	if f.Binding != RoleNone && StorableRole(t, f.Binding) {
		if f.Kind == KindServiceAccount && f.Binding == RoleResponsable {
			return RolePublicador, SourceBinding // R-10, defensa ante datos inconsistentes
		}
		return f.Binding, SourceBinding
	}
	if t == ScopeProject && f.EngramGrant {
		return RolePublicador, SourceEngramGrant // compatibilidad: lo que permite Engram hoy
	}
	if f.Exists && f.Visibility == VisibilityOpen && f.Status == StatusActive {
		return effectiveDefaultRole(f.DefaultRole), SourceDefaultRole
	}
	return RoleNone, SourceNone
}

// effectiveDefaultRole: lector por defecto, o proponente; nunca más (R-3).
func effectiveDefaultRole(r Role) Role {
	if r == RoleProponente {
		return RoleProponente
	}
	return RoleLector
}

func (e *Engine) resolve(ctx context.Context, id Identity, s Scope) (resolved, error) {
	pid, _ := id.principalNumber()
	key := id.PrincipalID + "|" + s.String()
	cache := cacheFrom(ctx)
	if r, ok := cache.get(key); ok {
		return r, nil
	}
	if e == nil || e.store == nil {
		return resolved{}, errors.New("authz: store is not configured")
	}
	var r resolved
	if s.Type == ScopePlatform {
		f, err := e.store.PlatformFacts(ctx, pid)
		if err != nil {
			return resolved{}, fmt.Errorf("authz: platform role: %w", err)
		}
		if f.BootstrapCandidate {
			if err := e.store.EnsureBootstrapSuperadmin(ctx); err != nil {
				return resolved{}, fmt.Errorf("authz: bootstrap superadmin: %w", err)
			}
		}
		r.role, r.source = resolvePlatform(f)
		r.enabled = f.Found && f.Enabled
		r.facts = ScopeFacts{Found: f.Found, Enabled: f.Enabled, Kind: f.Kind, Exists: true, Status: StatusActive, Visibility: VisibilityRestricted}
	} else {
		f, err := e.store.ScopeFacts(ctx, pid, s)
		if err != nil {
			return resolved{}, fmt.Errorf("authz: scope role: %w", err)
		}
		r.role, r.source = resolveScope(s.Type, f)
		r.enabled = f.Found && f.Enabled
		r.facts = f
	}
	cache.put(key, r)
	return r, nil
}

// EffectiveRole implementa Authorizer.
func (e *Engine) EffectiveRole(ctx context.Context, id Identity, s Scope) (Role, error) {
	role, _, err := e.EffectiveRoleSource(ctx, id, s)
	return role, err
}

// EffectiveRoleSource es EffectiveRole con la fuente del rol (binding,
// engram_grant, default_role, engram_admin, bootstrap, none).
func (e *Engine) EffectiveRoleSource(ctx context.Context, id Identity, s Scope) (Role, string, error) {
	ns, err := e.NormalizeScope(s)
	if err != nil {
		return RoleNone, SourceNone, err
	}
	if !id.Managed() || !id.Enabled {
		return RoleNone, SourceNone, nil
	}
	r, err := e.resolve(ctx, id, ns)
	if err != nil {
		return RoleNone, SourceNone, err
	}
	if !r.enabled {
		return RoleNone, SourceNone, nil
	}
	return r.role, r.source, nil
}

// Can implementa Authorizer (R-2: rol efectivo ∩ alcance del token, persona
// habilitada; se evalúa en cada petición).
func (e *Engine) Can(ctx context.Context, id Identity, s Scope, c Capability) (Decision, error) {
	level, ok := LevelOf(c)
	if !ok || level != s.Type {
		return Decision{Reason: ReasonBadCapability}, nil
	}
	ns, err := e.NormalizeScope(s)
	if err != nil {
		return Decision{Reason: ReasonNoRole, Hidden: true}, nil
	}
	if !id.Managed() {
		return Decision{Reason: ReasonUnmanaged}, nil // R-15
	}
	if !id.Enabled {
		return Decision{Reason: ReasonDisabled}, nil
	}
	r, err := e.resolve(ctx, id, ns)
	if err != nil {
		return Decision{Reason: ReasonError}, err // principio 5: fallar cerrado
	}
	d := decide(ns.Type, r, id.TokenScope, c)
	// NX-07 (RS-13): administración de plataforma solo desde redes permitidas.
	if d.Allowed && e.network.Restricts(c) && !e.network.Allows(id.ClientIP) {
		return Decision{Role: d.Role, Reason: ReasonNetwork}, nil
	}
	return d, nil
}

// decide es la parte pura de Can (probada con la matriz completa).
func decide(t ScopeType, r resolved, tokenScope string, c Capability) Decision {
	if !r.enabled {
		return Decision{Reason: ReasonDisabled}
	}
	if t != ScopePlatform {
		if r.role == RoleNone {
			return Decision{Reason: ReasonNoRole, Hidden: true} // R-4
		}
		if r.facts.Status == StatusArchived && !strings.HasSuffix(string(c), ".audit.read") {
			return Decision{Role: r.role, Reason: ReasonArchived}
		}
	}
	if !RoleHas(t, r.role, c) {
		return Decision{Role: r.role, Reason: ReasonCapability}
	}
	if !TokenScopeAllows(tokenScope, c) {
		return Decision{Role: r.role, Reason: ReasonTokenScope}
	}
	return Decision{Allowed: true, Role: r.role, Reason: ReasonAllowed}
}

// Require es Can más el evento authz.denied cuando se deniega (R-14). La
// escritura del evento es de mejor esfuerzo: la denegación ya está decidida.
func (e *Engine) Require(ctx context.Context, id Identity, s Scope, c Capability, details map[string]any) (Decision, error) {
	d, err := e.Can(ctx, id, s, c)
	if err != nil {
		return d, err
	}
	if !d.Allowed {
		e.RecordDenied(ctx, id, s, c, d, details)
	}
	return d, nil
}

// RecordDenied registra un evento authz.denied (mejor esfuerzo, con tiempo
// máximo y sin cancelación de la petición).
func (e *Engine) RecordDenied(ctx context.Context, id Identity, s Scope, c Capability, d Decision, details map[string]any) {
	if e == nil || e.store == nil {
		return
	}
	if ns, err := e.NormalizeScope(s); err == nil {
		s = ns
	}
	meta := map[string]any{"capability": string(c), "reason": d.Reason}
	if d.Hidden {
		meta["hidden"] = true
	}
	for k, v := range details {
		meta[k] = v
	}
	ev := Event{Action: ActionDenied, Scope: s, Actor: id, RoleBefore: d.Role, Details: meta}
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := e.store.RecordEvent(recCtx, ev); err != nil {
		log.Printf("[nexo] authz: no se pudo registrar authz.denied (%s %s): %v", s, c, err)
	}
}

// VisibleScopes implementa Authorizer. En plataforma devuelve un solo
// elemento con el rol de plataforma.
func (e *Engine) VisibleScopes(ctx context.Context, id Identity, t ScopeType) ([]ScopeRole, error) {
	if !t.Valid() {
		return nil, fmt.Errorf("%w: unknown scope type %q", ErrInvalidScope, t)
	}
	if !id.Managed() || !id.Enabled {
		return []ScopeRole{}, nil
	}
	if t == ScopePlatform {
		role, source, err := e.EffectiveRoleSource(ctx, id, PlatformScope())
		if err != nil {
			return nil, err
		}
		if role == RoleNone {
			return []ScopeRole{}, nil
		}
		return []ScopeRole{{Scope: PlatformScope(), Role: role, Source: source, Visibility: VisibilityRestricted, Status: StatusActive, Capabilities: Capabilities(t, role, id.TokenScope)}}, nil
	}
	if e == nil || e.store == nil {
		return nil, errors.New("authz: store is not configured")
	}
	pid, _ := id.principalNumber()
	rows, err := e.store.VisibleScopeFacts(ctx, pid, t)
	if err != nil {
		return nil, fmt.Errorf("authz: visible scopes: %w", err)
	}
	out := make([]ScopeRole, 0, len(rows))
	cache := cacheFrom(ctx)
	for _, row := range rows {
		if !row.Found || !row.Enabled {
			return []ScopeRole{}, nil
		}
		role, source := resolveScope(t, row.ScopeFacts)
		s := Scope{Type: t, Key: row.Key}
		cache.put(id.PrincipalID+"|"+s.String(), resolved{role: role, source: source, enabled: true, facts: row.ScopeFacts})
		status := row.Status
		if !row.Exists {
			status = StatusActive // proyecto de Engram sin fila: restringido y activo (compatibilidad)
		}
		if role == RoleNone || status != StatusActive {
			continue
		}
		vis := row.Visibility
		if !row.Exists {
			vis = VisibilityRestricted
		}
		sr := ScopeRole{Scope: s, Role: role, Source: source, Visibility: vis, Status: status, Capabilities: Capabilities(t, role, id.TokenScope)}
		if row.Exists && vis == VisibilityOpen {
			sr.DefaultRole = effectiveDefaultRole(row.DefaultRole)
		}
		out = append(out, sr)
	}
	slices.SortFunc(out, func(a, b ScopeRole) int { return strings.Compare(a.Scope.Key, b.Scope.Key) })
	return out, nil
}

// InvalidateCache borra la caché de la petición (después de una escritura).
func InvalidateCache(ctx context.Context) { cacheFrom(ctx).reset() }
