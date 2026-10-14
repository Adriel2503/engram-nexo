package authz

// Matriz completa del motor con un store en memoria: nivel x rol (asignación,
// permiso de Engram, rol implícito) x capacidad x alcance de token x
// visibilidad x estado del ámbito x tipo y estado de la persona. Más los
// casos de identidad (R-15), fallar cerrado y la caché por petición.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type fakeStore struct {
	platform map[int64]PlatformFacts
	scope    map[string]ScopeFacts
	visible  []ScopeFactsRow
	err      error
	calls    int
	events   []Event
}

func (f *fakeStore) PlatformFacts(_ context.Context, pid int64) (PlatformFacts, error) {
	f.calls++
	if f.err != nil {
		return PlatformFacts{}, f.err
	}
	return f.platform[pid], nil
}

func (f *fakeStore) ScopeFacts(_ context.Context, pid int64, s Scope) (ScopeFacts, error) {
	f.calls++
	if f.err != nil {
		return ScopeFacts{}, f.err
	}
	return f.scope[fmt.Sprintf("%d|%s", pid, s)], nil
}

func (f *fakeStore) VisibleScopeFacts(_ context.Context, _ int64, _ ScopeType) ([]ScopeFactsRow, error) {
	f.calls++
	return f.visible, f.err
}

func (f *fakeStore) RecordEvent(_ context.Context, ev Event) error {
	f.events = append(f.events, ev)
	return nil
}

func human(id string) Identity {
	return Identity{PrincipalID: id, TokenID: "100", Kind: KindHuman, Label: "p" + id, Verified: true, Enabled: true}
}

type scopeState struct {
	name       string
	exists     bool
	visibility string
	status     string
}

var scopeStates = []scopeState{
	{"inexistente", false, "", ""},
	{"abierto-activo", true, VisibilityOpen, StatusActive},
	{"restringido-activo", true, VisibilityRestricted, StatusActive},
	{"abierto-archivado", true, VisibilityOpen, StatusArchived},
	{"restringido-archivado", true, VisibilityRestricted, StatusArchived},
}

// expectScope es el oráculo de design §3 y requisitos §5 (R-3, R-4, R-10).
func expectScope(level ScopeType, binding Role, grant bool, st scopeState, def Role, kind string, enabled bool, ts string, c Capability) (bool, string, bool) {
	if !enabled {
		return false, ReasonDisabled, false
	}
	role := RoleNone
	switch {
	case binding != RoleNone:
		role = binding
		if kind == KindServiceAccount && binding == RoleResponsable {
			role = RolePublicador
		}
	case level == ScopeProject && grant:
		role = RolePublicador
	case st.exists && st.visibility == VisibilityOpen && st.status == StatusActive:
		role = def
	}
	if role == RoleNone {
		return false, ReasonNoRole, true
	}
	if st.status == StatusArchived && !strings.HasSuffix(string(c), ".audit.read") {
		return false, ReasonArchived, false
	}
	if !oracleHas(level, role, c) {
		return false, ReasonCapability, false
	}
	if !oracleToken(ts, c) {
		return false, ReasonTokenScope, false
	}
	return true, ReasonAllowed, false
}

func TestEngineScopeMatrix(t *testing.T) {
	ctx := context.Background()
	bindings := []Role{RoleNone, RoleLector, RoleProponente, RolePublicador, RoleResponsable}
	total := 0
	for _, level := range []ScopeType{ScopeProject, ScopeSpace} {
		for _, binding := range bindings {
			for _, grant := range []bool{false, true} {
				for _, st := range scopeStates {
					for _, def := range []Role{RoleLector, RoleProponente} {
						for _, kind := range []string{KindHuman, KindServiceAccount} {
							for _, enabled := range []bool{true, false} {
								key := "alpha"
								store := &fakeStore{scope: map[string]ScopeFacts{}}
								f := ScopeFacts{Found: true, Enabled: enabled, Kind: kind, Binding: binding, EngramGrant: grant && level == ScopeProject}
								if st.exists {
									f.Exists, f.Visibility, f.Status, f.DefaultRole = true, st.visibility, st.status, def
								}
								store.scope[fmt.Sprintf("7|%s:%s", level, key)] = f
								eng := NewEngine(store)
								id := human("7")
								id.Kind = kind
								for _, c := range AllCapabilities(level) {
									for _, ts := range tokenScopes {
										total++
										id.TokenScope = ts
										d, err := eng.Can(ctx, id, Scope{Type: level, Key: key}, c)
										if err != nil {
											t.Fatal(err)
										}
										wantAllowed, wantReason, wantHidden := expectScope(level, binding, grant, st, def, kind, enabled, ts, c)
										if d.Allowed != wantAllowed || d.Reason != wantReason || d.Hidden != wantHidden {
											t.Fatalf("%s binding=%q grant=%v %s def=%s kind=%s enabled=%v %s token=%q: got %+v, want allowed=%v reason=%s hidden=%v",
												level, binding, grant, st.name, def, kind, enabled, c, ts, d, wantAllowed, wantReason, wantHidden)
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	t.Logf("casos de la matriz de ámbitos: %d", total)
}

// expectPlatform es el oráculo de design §3 (plataforma) y R-10. La columna
// role de Engram no decide nada (design §11): solo cuenta la asignación.
func expectPlatform(binding Role, kind string, enabled bool, ts string, c Capability) (bool, string, Role) {
	if !enabled {
		return false, ReasonDisabled, RoleNone
	}
	role := RoleMiembro
	switch {
	case kind == KindServiceAccount:
	case binding != RoleNone:
		role = binding
	}
	if !oracleHas(ScopePlatform, role, c) {
		return false, ReasonCapability, role
	}
	if !oracleToken(ts, c) {
		return false, ReasonTokenScope, role
	}
	return true, ReasonAllowed, role
}

func TestEnginePlatformMatrix(t *testing.T) {
	ctx := context.Background()
	total := 0
	for _, binding := range []Role{RoleNone, RoleAuditor, RoleAdmin, RoleSuperadmin} {
		for _, engramRole := range []string{"member", "admin"} {
			{
				for _, kind := range []string{KindHuman, KindServiceAccount} {
					for _, enabled := range []bool{true, false} {
						store := &fakeStore{platform: map[int64]PlatformFacts{
							5: {Found: true, Enabled: enabled, Kind: kind, EngramRole: engramRole, Binding: binding},
						}}
						eng := NewEngine(store)
						id := human("5")
						id.Kind = kind
						for _, c := range AllCapabilities(ScopePlatform) {
							for _, ts := range tokenScopes {
								total++
								id.TokenScope = ts
								d, err := eng.Can(ctx, id, PlatformScope(), c)
								if err != nil {
									t.Fatal(err)
								}
								wantAllowed, wantReason, wantRole := expectPlatform(binding, kind, enabled, ts, c)
								if d.Allowed != wantAllowed || d.Reason != wantReason || d.Hidden {
									t.Fatalf("binding=%q engram=%s kind=%s enabled=%v %s token=%q: got %+v want %v/%s",
										binding, engramRole, kind, enabled, c, ts, d, wantAllowed, wantReason)
								}
								if enabled && d.Role != wantRole {
									t.Fatalf("rol = %s, want %s", d.Role, wantRole)
								}
							}
						}
					}
				}
			}
		}
	}
	t.Logf("casos de la matriz de plataforma: %d", total)
}

// R-15: token compartido, modo inseguro e identidades no administradas no
// tienen roles en ningún nivel; el motor no consulta la base.
func TestEngineUnmanagedIdentityHasNoRoles(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	eng := NewEngine(store)
	ids := map[string]Identity{
		"token compartido": {PrincipalID: "legacy:sync", Kind: "legacy", Label: "LEGACY_SYNC", Enabled: true},
		"modo inseguro":    {},
		"no verificado":    {PrincipalID: "3", Kind: KindHuman, Enabled: true},
		"id no numérico":   {PrincipalID: "p-admin", Kind: KindHuman, Verified: true, Enabled: true},
		"kind legacy":      {PrincipalID: "3", Kind: "legacy", Verified: true, Enabled: true},
	}
	for name, id := range ids {
		for _, s := range []Scope{PlatformScope(), ProjectScope("alpha"), SpaceScope("general")} {
			for _, c := range AllCapabilities(s.Type) {
				d, err := eng.Can(ctx, id, s, c)
				if err != nil || d.Allowed || d.Reason != ReasonUnmanaged {
					t.Fatalf("%s %s %s: %+v %v", name, s, c, d, err)
				}
			}
			role, err := eng.EffectiveRole(ctx, id, s)
			if err != nil || role != RoleNone {
				t.Fatalf("%s: rol %q", name, role)
			}
		}
		vis, err := eng.VisibleScopes(ctx, id, ScopeSpace)
		if err != nil || len(vis) != 0 {
			t.Fatalf("%s: visibles %v", name, vis)
		}
	}
	if store.calls != 0 {
		t.Fatalf("sin identidad administrada no se consulta la base (%d)", store.calls)
	}
	disabled := human("3")
	disabled.Enabled = false
	if d, _ := eng.Can(ctx, disabled, PlatformScope(), CapSelfRead); d.Allowed || d.Reason != ReasonDisabled {
		t.Fatalf("deshabilitada: %+v", d)
	}
}

// Principio 5: si no se puede resolver el rol, se deniega con error.
func TestEngineFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{err: errors.New("db down")}
	eng := NewEngine(store)
	for _, s := range []Scope{PlatformScope(), ProjectScope("alpha"), SpaceScope("general")} {
		d, err := eng.Can(ctx, human("1"), s, AllCapabilities(s.Type)[0])
		if err == nil || d.Allowed || d.Reason != ReasonError {
			t.Fatalf("%s: %+v %v", s, d, err)
		}
	}
	if _, err := eng.VisibleScopes(ctx, human("1"), ScopeProject); err == nil {
		t.Fatal("VisibleScopes debe devolver el error")
	}
	if d, err := NewEngine(nil).Can(ctx, human("1"), PlatformScope(), CapSelfRead); err == nil || d.Allowed {
		t.Fatal("sin store: denegado con error")
	}
}

func TestEngineCapabilityLevelAndScopeValidation(t *testing.T) {
	ctx := context.Background()
	eng := NewEngine(&fakeStore{})
	if d, _ := eng.Can(ctx, human("1"), ProjectScope("alpha"), CapSpaceRead); d.Allowed || d.Reason != ReasonBadCapability {
		t.Fatalf("capacidad de otro nivel: %+v", d)
	}
	if d, _ := eng.Can(ctx, human("1"), PlatformScope(), "platform.root"); d.Allowed || d.Reason != ReasonBadCapability {
		t.Fatalf("capacidad desconocida: %+v", d)
	}
	for _, bad := range []Scope{SpaceScope("A B"), SpaceScope("x"), SpaceScope(""), ProjectScope("   "), {Type: "workspace", Key: "x"}} {
		if d, _ := eng.Can(ctx, human("1"), bad, CapSpaceRead); d.Allowed {
			t.Fatalf("ámbito inválido %v: %+v", bad, d)
		}
	}
	norm, err := NewEngine(&fakeStore{}, WithProjectNormalizer(func(s string) string { return strings.ToUpper(strings.TrimSpace(s)) })).NormalizeScope(ProjectScope(" ms-factura "))
	if err != nil || norm.Key != "MS-FACTURA" {
		t.Fatalf("normalizador inyectado: %v %v", norm, err)
	}
}

// Require registra authz.denied (R-14) y no registra lo permitido.
func TestEngineRequireRecordsDenials(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{platform: map[int64]PlatformFacts{1: {Found: true, Enabled: true, Kind: KindHuman, EngramRole: "admin", Binding: RoleAdmin}}}
	eng := NewEngine(store)
	id := human("1")
	id.ClientIP = "10.0.0.9"
	if d, _ := eng.Require(ctx, id, PlatformScope(), CapPlatformUsersManage, nil); !d.Allowed {
		t.Fatal("admin gestiona usuarios")
	}
	if len(store.events) != 0 {
		t.Fatal("lo permitido no se registra")
	}
	d, _ := eng.Require(ctx, id, PlatformScope(), CapPlatformAdminsManage, map[string]any{"route": "POST /admin/users"})
	if d.Allowed || len(store.events) != 1 {
		t.Fatalf("denegado: %+v eventos=%d", d, len(store.events))
	}
	ev := store.events[0]
	if ev.Action != ActionDenied || ev.Actor.ClientIP != "10.0.0.9" || ev.Details["capability"] != string(CapPlatformAdminsManage) || ev.Details["route"] != "POST /admin/users" || ev.RoleBefore != RoleAdmin {
		t.Fatalf("evento: %+v", ev)
	}
}

func TestEngineVisibleScopesAndCache(t *testing.T) {
	ctx := WithRequestCache(context.Background())
	store := &fakeStore{visible: []ScopeFactsRow{
		{Key: "abierto", ScopeFacts: ScopeFacts{Found: true, Enabled: true, Kind: KindHuman, Exists: true, Visibility: VisibilityOpen, Status: StatusActive, DefaultRole: RoleProponente}},
		{Key: "asignado", ScopeFacts: ScopeFacts{Found: true, Enabled: true, Kind: KindHuman, Exists: true, Visibility: VisibilityRestricted, Status: StatusActive, Binding: RoleResponsable}},
		{Key: "archivado", ScopeFacts: ScopeFacts{Found: true, Enabled: true, Kind: KindHuman, Exists: true, Visibility: VisibilityRestricted, Status: StatusArchived, Binding: RoleLector}},
		{Key: "engram", ScopeFacts: ScopeFacts{Found: true, Enabled: true, Kind: KindHuman, EngramGrant: true}},
	}}
	eng := NewEngine(store)
	id := human("2")
	id.TokenScope = TokenScopeRead
	got, err := eng.VisibleScopes(ctx, id, ScopeProject)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Role{"abierto": RoleProponente, "asignado": RoleResponsable, "engram": RolePublicador}
	if len(got) != len(want) {
		t.Fatalf("visibles: %+v", got)
	}
	for _, sr := range got {
		if want[sr.Scope.Key] != sr.Role {
			t.Fatalf("%s: %s", sr.Scope.Key, sr.Role)
		}
		for _, c := range sr.Capabilities {
			if !strings.HasSuffix(string(c), ".read") {
				t.Fatalf("token read con %s", c)
			}
		}
	}
	if got[2].Scope.Key != "engram" || got[2].Source != SourceEngramGrant || got[2].Visibility != VisibilityRestricted {
		t.Fatalf("proyecto de Engram sin fila: %+v", got[2])
	}
	calls := store.calls
	if d, _ := eng.Can(ctx, id, ProjectScope("asignado"), CapProjectRead); !d.Allowed {
		t.Fatalf("%+v", d)
	}
	if store.calls != calls {
		t.Fatal("la caché por petición debe evitar otra consulta")
	}
	InvalidateCache(ctx)
	_, _ = eng.Can(ctx, id, ProjectScope("asignado"), CapProjectRead)
	if store.calls != calls+1 {
		t.Fatal("tras invalidar se vuelve a consultar")
	}
	plat, err := eng.VisibleScopes(context.Background(), human("9"), ScopePlatform)
	if err != nil || len(plat) != 0 {
		t.Fatalf("principal inexistente: %v %v", plat, err)
	}
}

func TestEngineIdentityContext(t *testing.T) {
	id := human("4")
	ctx := WithIdentity(context.Background(), id)
	got, ok := IdentityFrom(ctx)
	if !ok || got != id {
		t.Fatal("identidad en contexto")
	}
	if _, ok := IdentityFrom(context.Background()); ok {
		t.Fatal("sin identidad")
	}
}
