package authz

// Matriz nivel x rol x capacidad x alcance de token contra las tablas de
// requisitos §4, transcritas aquí de forma literal (oráculo independiente de
// la construcción por herencia de roles.go).

import (
	"strings"
	"testing"
)

// oracle[nivel][capacidad] = roles que la tienen (requisitos §4.1 a §4.3).
var oracle = map[ScopeType]map[Capability][]Role{
	ScopePlatform: {
		CapPlatformAdminsManage:     {RoleSuperadmin},
		CapPlatformSettings:         {RoleSuperadmin},
		CapPlatformScopeRecover:     {RoleSuperadmin},
		CapPlatformUsersManage:      {RoleSuperadmin, RoleAdmin},
		CapPlatformTokensManage:     {RoleSuperadmin, RoleAdmin},
		CapPlatformScopeCreate:      {RoleSuperadmin, RoleAdmin},
		CapPlatformScopeArchive:     {RoleSuperadmin, RoleAdmin},
		CapPlatformScopeAssignOwner: {RoleSuperadmin, RoleAdmin},
		CapPlatformAuditRead:        {RoleSuperadmin, RoleAdmin, RoleAuditor},
		CapPlatformDirectoryRead:    {RoleSuperadmin, RoleAdmin, RoleAuditor},
		CapSelfRead:                 {RoleSuperadmin, RoleAdmin, RoleAuditor, RoleMiembro},
	},
	ScopeProject: {
		CapProjectRead:          {RoleLector, RoleProponente, RolePublicador, RoleResponsable},
		CapProjectWriteOwn:      {RoleProponente, RolePublicador, RoleResponsable},
		CapProjectWriteAny:      {RolePublicador, RoleResponsable},
		CapProjectAuditRead:     {RolePublicador, RoleResponsable},
		CapProjectMembersManage: {RoleResponsable},
		CapProjectConfigure:     {RoleResponsable},
	},
	ScopeSpace: {
		CapSpaceRead:          {RoleLector, RoleProponente, RolePublicador, RoleResponsable},
		CapSpacePropose:       {RoleProponente, RolePublicador, RoleResponsable},
		CapSpacePublish:       {RolePublicador, RoleResponsable},
		CapSpaceReview:        {RolePublicador, RoleResponsable},
		CapSpaceAuditRead:     {RolePublicador, RoleResponsable},
		CapSpaceMembersManage: {RoleResponsable},
		CapSpaceConfigure:     {RoleResponsable},
	},
}

// readCaps: lo que admite un token "read" (requisitos §4.4).
var readCaps = map[Capability]bool{
	CapPlatformAuditRead: true, CapPlatformDirectoryRead: true, CapSelfRead: true,
	CapProjectRead: true, CapProjectAuditRead: true,
	CapSpaceRead: true, CapSpaceAuditRead: true,
}

var tokenScopes = []string{"", TokenScopeWrite, TokenScopeRead, "admin"}

func oracleHas(t ScopeType, r Role, c Capability) bool {
	for _, x := range oracle[t][c] {
		if x == r {
			return true
		}
	}
	return false
}

func oracleToken(scope string, c Capability) bool {
	switch scope {
	case "", TokenScopeWrite:
		return true
	case TokenScopeRead:
		return readCaps[c]
	}
	return false
}

func TestCapabilityTablesMatchRequirements(t *testing.T) {
	cases := 0
	for level, caps := range oracle {
		if got, want := len(AllCapabilities(level)), len(caps); got != want {
			t.Fatalf("%s: %d capacidades, requisitos tienen %d", level, got, want)
		}
		for _, role := range Roles(level) {
			for c := range caps {
				if got, want := RoleHas(level, role, c), oracleHas(level, role, c); got != want {
					t.Errorf("%s/%s/%s: RoleHas=%v, requisitos=%v", level, role, c, got, want)
				}
				for _, ts := range tokenScopes {
					cases++
					want := oracleHas(level, role, c) && oracleToken(ts, c)
					got := RoleHas(level, role, c) && TokenScopeAllows(ts, c)
					if got != want {
						t.Errorf("%s/%s/%s/token=%q: %v, want %v", level, role, c, ts, got, want)
					}
					inList := false
					for _, x := range Capabilities(level, role, ts) {
						if x == c {
							inList = true
						}
					}
					if inList != want {
						t.Errorf("Capabilities(%s,%s,%q) contiene %s = %v, want %v", level, role, ts, c, inList, want)
					}
				}
			}
		}
	}
	if cases != (4*11+4*6+4*7)*len(tokenScopes) {
		t.Fatalf("casos de la matriz = %d", cases)
	}
}

func TestRolesAreOrderedAndInherit(t *testing.T) {
	for level := range oracle {
		roles := Roles(level)
		for i := 1; i < len(roles); i++ {
			for _, c := range AllCapabilities(level) {
				if RoleHas(level, roles[i-1], c) && !RoleHas(level, roles[i], c) {
					t.Errorf("%s: %s no hereda %s de %s", level, roles[i], c, roles[i-1])
				}
			}
			if Rank(level, roles[i]) != i {
				t.Errorf("rank %s = %d", roles[i], Rank(level, roles[i]))
			}
		}
	}
	if Rank(ScopePlatform, RoleNone) != Rank(ScopePlatform, RoleMiembro) {
		t.Fatal("en plataforma, ninguno = miembro")
	}
	if Rank(ScopeSpace, RoleNone) != -1 || Rank(ScopeSpace, RoleAdmin) != -1 {
		t.Fatal("rol ajeno al nivel debe tener rank -1")
	}
}

func TestLevelOfAndValidRoles(t *testing.T) {
	for level, caps := range oracle {
		for c := range caps {
			if got, ok := LevelOf(c); !ok || got != level {
				t.Errorf("LevelOf(%s) = %s,%v", c, got, ok)
			}
		}
	}
	for _, bad := range []Capability{"project.delete", "space", "platform.root", "", "self.write"} {
		if _, ok := LevelOf(bad); ok {
			t.Errorf("LevelOf(%q) debería ser desconocida", bad)
		}
	}
	if StorableRole(ScopePlatform, RoleMiembro) || !StorableRole(ScopePlatform, RoleAuditor) {
		t.Fatal("miembro no se guarda; auditor sí")
	}
	if ValidRole(ScopeProject, RoleAdmin) || ValidRole(ScopePlatform, RoleLector) || !ValidRole(ScopeSpace, RoleResponsable) {
		t.Fatal("combinaciones rol/nivel")
	}
	if NormalizeTokenScope("") != "write" || NormalizeTokenScope("read") != "read" {
		t.Fatal("NormalizeTokenScope")
	}
	for c := range readCaps {
		if !strings.HasSuffix(string(c), ".read") {
			t.Fatalf("%s", c)
		}
	}
}
