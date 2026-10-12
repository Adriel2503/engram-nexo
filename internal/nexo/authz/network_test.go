package authz

// NX-07 S2-04 (RS-13): restricción por red de las capacidades de
// administración de plataforma, y alcance de token read (S3-02, CA-S08).

import (
	"context"
	"net/netip"
	"testing"
)

func TestEngineNetworkPolicy(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{platform: map[int64]PlatformFacts{
		1: {Found: true, Enabled: true, Kind: KindHuman, EngramRole: "admin", Binding: RoleSuperadmin},
	}}
	policy := NetworkPolicy{Restricted: true, Allowed: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}}
	eng := NewEngine(store, WithNetworkPolicy(policy))
	id := human("1")

	id.ClientIP = "10.20.3.4"
	for _, c := range []Capability{CapPlatformAdminsManage, CapPlatformSettings, CapPlatformUsersManage} {
		if d, _ := eng.Can(ctx, id, PlatformScope(), c); !d.Allowed {
			t.Fatalf("desde la red permitida %s: %+v", c, d)
		}
	}
	id.ClientIP = "192.168.9.9"
	for _, c := range []Capability{CapPlatformAdminsManage, CapPlatformSettings} {
		d, _ := eng.Can(ctx, id, PlatformScope(), c)
		if d.Allowed || d.Reason != ReasonNetwork || d.Role != RoleSuperadmin {
			t.Fatalf("CA-S07 fuera de la red %s: %+v", c, d)
		}
	}
	// El resto de capacidades no depende de la red.
	if d, _ := eng.Can(ctx, id, PlatformScope(), CapPlatformUsersManage); !d.Allowed {
		t.Fatalf("users.manage fuera de la red: %+v", d)
	}
	// Require deja el evento authz.denied con el motivo network.
	store.events = nil
	if d, _ := eng.Require(ctx, id, PlatformScope(), CapPlatformAdminsManage, map[string]any{"route": "POST /admin/users"}); d.Allowed {
		t.Fatal("debe negarse")
	}
	if len(store.events) != 1 || store.events[0].Details["reason"] != ReasonNetwork || store.events[0].Actor.ClientIP != "192.168.9.9" {
		t.Fatalf("evento: %+v", store.events)
	}
	// IP ilegible o vacía: fuera de la red (fallar cerrado).
	id.ClientIP = ""
	if d, _ := eng.Can(ctx, id, PlatformScope(), CapPlatformAdminsManage); d.Allowed {
		t.Fatal("IP vacía")
	}
	// Restringida sin redes válidas: niega siempre.
	deny := NewEngine(store, WithNetworkPolicy(NetworkPolicy{Restricted: true}))
	id.ClientIP = "10.20.3.4"
	if d, _ := deny.Can(ctx, id, PlatformScope(), CapPlatformAdminsManage); d.Allowed {
		t.Fatal("sin redes válidas se niega")
	}
	// Sin política: sin restricción (compatibilidad).
	if d, _ := NewEngine(store).Can(ctx, id, PlatformScope(), CapPlatformAdminsManage); !d.Allowed {
		t.Fatal("sin política")
	}
	// IPv4 mapeada en IPv6.
	if !policy.Allows("::ffff:10.20.1.1") || policy.Allows("10.21.0.1") {
		t.Fatal("Allows")
	}
}

// CA-S08 (motor): un token read no administra ni sube cambios aunque el rol
// lo permita; sí lee.
func TestEngineReadTokenScope(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{
		platform: map[int64]PlatformFacts{1: {Found: true, Enabled: true, Kind: KindHuman, EngramRole: "admin", Binding: RoleSuperadmin}},
		scope:    map[string]ScopeFacts{"1|project:p": {Found: true, Enabled: true, Kind: KindHuman, EngramGrant: true}},
	}
	eng := NewEngine(store)
	id := human("1")
	id.TokenScope = TokenScopeRead
	for _, c := range []Capability{CapPlatformUsersManage, CapPlatformTokensManage, CapPlatformAdminsManage} {
		if d, _ := eng.Can(ctx, id, PlatformScope(), c); d.Allowed || d.Reason != ReasonTokenScope {
			t.Fatalf("%s con token read: %+v", c, d)
		}
	}
	for _, c := range []Capability{CapPlatformAuditRead, CapPlatformDirectoryRead, CapSelfRead} {
		if d, _ := eng.Can(ctx, id, PlatformScope(), c); !d.Allowed {
			t.Fatalf("%s con token read: %+v", c, d)
		}
	}
	if d, _ := eng.Can(ctx, id, ProjectScope("p"), CapProjectWriteOwn); d.Allowed || d.Reason != ReasonTokenScope {
		t.Fatalf("project.write.own con token read: %+v", d)
	}
	if d, _ := eng.Can(ctx, id, ProjectScope("p"), CapProjectRead); !d.Allowed {
		t.Fatalf("project.read con token read: %+v", d)
	}
}
