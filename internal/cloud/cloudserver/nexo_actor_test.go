package cloudserver

// Nexo IA · NX-01 — tests unitarios de nexoResolveActor (RF-02, RF-03, RF-04).

import (
	"context"
	"strings"
	"testing"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

func TestNexoResolveActor(t *testing.T) {
	managed := cloudauth.Principal{ID: "7", Kind: cloudauth.PrincipalKindHuman, DisplayName: "ana", Role: cloudauth.RoleMember, Enabled: true, Source: cloudauth.PrincipalSourceManagedToken, TokenID: "12"}
	service := cloudauth.Principal{ID: "9", Kind: cloudauth.PrincipalKindServiceAccount, DisplayName: "ci-pipeline", Role: cloudauth.RoleMember, Enabled: true, Source: cloudauth.PrincipalSourceManagedToken, TokenID: "13"}
	noName := managed
	noName.DisplayName = "  "
	legacySync := cloudauth.Principal{ID: "legacy:sync", Kind: cloudauth.PrincipalKindLegacy, DisplayName: "LEGACY_SYNC", Role: cloudauth.RoleMember, Enabled: true, Source: cloudauth.PrincipalSourceLegacyEnvSync}
	legacyAdmin := cloudauth.Principal{ID: "legacy:admin", Kind: cloudauth.PrincipalKindLegacy, DisplayName: "OPERATOR", Role: cloudauth.RoleAdmin, Enabled: true, Source: cloudauth.PrincipalSourceLegacyEnvAdmin}

	cases := []struct {
		name      string
		principal *cloudauth.Principal
		claim     string
		want      cloudstore.MutationActor
	}{
		{"token asignado (persona)", &managed, "Administrador", cloudstore.MutationActor{Name: "ana", PrincipalID: "7", TokenID: "12", Verified: true, ClientClaim: "Administrador"}},
		{"token asignado (cuenta de servicio)", &service, "", cloudstore.MutationActor{Name: "ci-pipeline", PrincipalID: "9", TokenID: "13", Verified: true}},
		{"sin nombre usa el ID", &noName, "", cloudstore.MutationActor{Name: "7", PrincipalID: "7", TokenID: "12", Verified: true}},
		{"token compartido de sync", &legacySync, "LUIS-PC", cloudstore.MutationActor{Name: cloudstore.NexoActorUnidentified, ClientClaim: "LUIS-PC"}},
		{"token compartido de admin", &legacyAdmin, "", cloudstore.MutationActor{Name: cloudstore.NexoActorUnidentified}},
		{"sin autenticación", nil, "  Administrador  ", cloudstore.MutationActor{Name: cloudstore.NexoActorUnidentified, ClientClaim: "Administrador"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.principal != nil {
				ctx = WithPrincipal(ctx, *tc.principal)
			}
			if got := nexoResolveActor(ctx, tc.claim); got != tc.want {
				t.Fatalf("nexoResolveActor = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestNexoResolveActorNeverUsesClaimAsIdentity(t *testing.T) {
	// CA-02: la PC declara "Administrador" con el token de Ana.
	ctx := WithPrincipal(context.Background(), cloudauth.Principal{ID: "7", Kind: cloudauth.PrincipalKindHuman, DisplayName: "ana", Role: cloudauth.RoleMember, Enabled: true, Source: cloudauth.PrincipalSourceManagedToken, TokenID: "12"})
	actor := nexoResolveActor(ctx, "Administrador")
	if actor.Name != "ana" {
		t.Fatalf("actor.Name = %q, want ana", actor.Name)
	}
	long := nexoResolveActor(context.Background(), strings.Repeat("x", 500))
	if len(long.ClientClaim) != 128 {
		t.Fatalf("claim length = %d, want 128", len(long.ClientClaim))
	}
}
