package nexocli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/auditcli"
)

func runAuthzCLI(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(append([]string{"authz"}, args...), "", &out, &errb)
	return code, out.String(), errb.String()
}

// reconcile-roles: en seco por defecto, --apply corrige desde el rol de Nexo, idempotente y auditado.
func TestAuthzReconcileRoles(t *testing.T) {
	env := kbtest.New(t)
	tm := env.NewTeam() // root superadmin, adm admin, aud auditor, ana/luis miembros, ci cuenta de servicio
	t.Setenv(auditcli.DatabaseURLEnv, env.DSN)

	if code, out, _ := runAuthzCLI("reconcile-roles"); code != 0 || !strings.Contains(out, "Sin diferencias") {
		t.Fatalf("sin diferencias: %d %s", code, out)
	}
	// Se fabrican desalineaciones a propósito (alguien evitó los triggers).
	for _, q := range []string{
		`ALTER TABLE cloud_principals DISABLE TRIGGER nexo_principal_role_derive`,
		`UPDATE cloud_principals SET role = 'member' WHERE id = ` + tm.Adm, // admin de Nexo con columna member
		`UPDATE cloud_principals SET role = 'admin' WHERE id = ` + tm.Luis, // miembro con columna admin
		`UPDATE cloud_principals SET role = 'admin' WHERE id = ` + tm.Aud,  // auditor con columna admin
		`ALTER TABLE cloud_principals ENABLE TRIGGER nexo_principal_role_derive`,
	} {
		if _, err := env.Admin.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	column := func(id string) string {
		var r string
		if err := env.Admin.QueryRow(`SELECT role FROM cloud_principals WHERE id = $1::bigint`, id).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}

	// En seco: lista, no cambia nada y sale con 1.
	code, out, _ := runAuthzCLI("reconcile-roles")
	if code != 1 || !strings.Contains(out, "3 diferencias") || !strings.Contains(out, "En seco") || !strings.Contains(out, "adm") || !strings.Contains(out, "Administrador") {
		t.Fatalf("en seco: %d %s", code, out)
	}
	if column(tm.Adm) != "member" || column(tm.Luis) != "admin" || column(tm.Aud) != "admin" {
		t.Fatal("en seco no se cambia nada")
	}
	if code, out, _ := runAuthzCLI("reconcile-roles", "--json"); code != 1 || !strings.Contains(out, `"applied": false`) || !strings.Contains(out, `"expected": "admin"`) {
		t.Fatalf("json en seco: %d %s", code, out)
	}
	if env.Count(`SELECT count(*) FROM nexo_authz_events WHERE action = 'engram_role.synced'`) != 0 {
		t.Fatal("en seco no deja eventos")
	}

	// --apply: el rol de Nexo manda; la columna se recalcula; un evento por corrección.
	code, out, errOut := runAuthzCLI("reconcile-roles", "--apply")
	if code != 0 || !strings.Contains(out, "Corregidas 3 de 3") {
		t.Fatalf("apply: %d %s %s", code, out, errOut)
	}
	if column(tm.Adm) != "admin" || column(tm.Luis) != "member" || column(tm.Aud) != "member" || column(tm.Root) != "admin" {
		t.Fatal("la columna acompaña al rol de Nexo")
	}
	if n := env.Count(`SELECT count(*) FROM nexo_authz_events WHERE action = 'engram_role.synced' AND details->>'via' = 'cli'`); n != 3 {
		t.Fatalf("eventos engram_role.synced = %d", n)
	}
	// El rol de Nexo no se tocó.
	for id, want := range map[string]authz.Role{tm.Adm: authz.RoleAdmin, tm.Aud: authz.RoleAuditor, tm.Luis: authz.RoleMiembro, tm.Root: authz.RoleSuperadmin} {
		if got, _, _ := env.Roles.PlatformRoleOf(env.Ctx(), id); got != want {
			t.Fatalf("rol de Nexo de %s = %s, want %s", id, got, want)
		}
	}
	// Idempotente.
	if code, out, _ := runAuthzCLI("reconcile-roles", "--apply"); code != 0 || !strings.Contains(out, "Sin diferencias") {
		t.Fatalf("segunda pasada: %d %s", code, out)
	}
	if env.Count(`SELECT count(*) FROM nexo_authz_events WHERE action = 'engram_role.synced'`) != 3 {
		t.Fatal("idempotente: sin eventos nuevos")
	}
}

func TestAuthzCLIUsage(t *testing.T) {
	if code, out, _ := runAuthzCLI("help"); code != 0 || !strings.Contains(out, "reconcile-roles") || !strings.Contains(out, "--apply") {
		t.Fatalf("help: %d %s", code, out)
	}
	if code, _, errOut := runAuthzCLI("nada"); code != 2 || !strings.Contains(errOut, "desconocido") {
		t.Fatalf("comando desconocido: %d %s", code, errOut)
	}
	if code, _, _ := runAuthzCLI("reconcile-roles", "--fuera"); code != 2 {
		t.Fatalf("opción desconocida: %d", code)
	}
	t.Setenv(auditcli.DatabaseURLEnv, "")
	if code, _, errOut := runAuthzCLI("reconcile-roles"); code != 1 || !strings.Contains(errOut, "ENGRAM_DATABASE_URL") {
		t.Fatalf("sin conexión: %d %s", code, errOut)
	}
}

// Con dos cuentas de base de datos (dueña y aplicación) los roles unificados
// funcionan con la cuenta de aplicación: asignar, crear con rol, derivar la
// columna y corregir desalineaciones.
func TestAuthzUnifiedRolesWithTwoDatabaseAccounts(t *testing.T) {
	env, _ := kbtest.TwoAccounts(t)
	tm := env.NewTeam()
	t.Setenv(auditcli.DatabaseURLEnv, env.DSN)

	ctx := authz.WithCreateRequest(env.Ctx(), authz.CreateRequest{Role: authz.RoleAuditor, Actor: env.ID(tm.Root), Source: "api"})
	user, err := env.CS.CreateHumanUser(ctx, cloudstore.CreateHumanUserParams{Username: "auditora", Role: authz.EngramRoleFor(authz.RoleAuditor)})
	if err != nil {
		t.Fatalf("crear con rol con la cuenta de aplicación: %v", err)
	}
	if user.PlatformRole != "auditor" || user.Role != "member" {
		t.Fatalf("rol de Nexo %q, columna %q", user.PlatformRole, user.Role)
	}
	if _, err := env.Roles.SetPlatformRole(env.Ctx(), env.ID(tm.Root), user.PrincipalID, authz.RoleAdmin); err != nil {
		t.Fatalf("cambiar el rol con la cuenta de aplicación: %v", err)
	}
	var column string
	if err := env.Admin.QueryRow(`SELECT role FROM cloud_principals WHERE id = $1::bigint`, user.PrincipalID).Scan(&column); err != nil || column != "admin" {
		t.Fatalf("la columna acompaña al rol: %q %v", column, err)
	}
	// La cuenta de aplicación no puede desactivar el trigger de derivación (lo único que haría falta para desalinear).
	if _, err := env.DB.Exec(`ALTER TABLE cloud_principals DISABLE TRIGGER nexo_principal_role_derive`); err == nil {
		t.Fatal("la cuenta de aplicación no debe poder desactivar los triggers")
	}
	// Con la desalineación fabricada por la dueña, la CLI con la cuenta de aplicación la corrige.
	for _, q := range []string{
		`ALTER TABLE cloud_principals DISABLE TRIGGER nexo_principal_role_derive`,
		`UPDATE cloud_principals SET role = 'member' WHERE id = ` + user.PrincipalID,
		`ALTER TABLE cloud_principals ENABLE TRIGGER nexo_principal_role_derive`,
	} {
		if _, err := env.Admin.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if code, out, errOut := runAuthzCLI("reconcile-roles", "--apply"); code != 0 || !strings.Contains(out, "Corregidas 1 de 1") {
		t.Fatalf("apply con la cuenta de aplicación: %d %s %s", code, out, errOut)
	}
}
