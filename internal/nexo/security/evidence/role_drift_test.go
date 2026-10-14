package evidence_test

// NX-05 (unificación de roles): la desalineación entre el rol de Nexo y la
// columna role de Engram se informa en una alerta y en la revisión de accesos.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

func TestPlatformRoleDriftAlertAndAccessReviewFlag(t *testing.T) {
	env := kbtest.New(t)
	tm := env.NewTeam()
	ctx := context.Background()
	rules := evidence.DefaultRules()

	// Sin diferencias: ni alerta ni marca.
	if got := byRule(evaluate(t, env, rules), evidence.RulePlatformRoleDrift); len(got) != 0 {
		t.Fatalf("sin diferencias no hay alerta: %+v", got)
	}
	rep, err := evidence.BuildAccessReview(ctx, env.DB, 90, time.Now())
	if err != nil || rep.Summary.RoleDrift != 0 {
		t.Fatalf("revisión limpia: %+v %v", rep.Summary, err)
	}

	// Alguien evita los triggers: el admin de Nexo queda "member" y un miembro "admin".
	for _, q := range []string{
		`ALTER TABLE cloud_principals DISABLE TRIGGER nexo_principal_role_derive`,
		`UPDATE cloud_principals SET role = 'member' WHERE id = ` + tm.Adm,
		`UPDATE cloud_principals SET role = 'admin' WHERE id = ` + tm.Luis,
		`ALTER TABLE cloud_principals ENABLE TRIGGER nexo_principal_role_derive`,
	} {
		if _, err := env.Admin.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	alerts := byRule(evaluate(t, env, rules), evidence.RulePlatformRoleDrift)
	if len(alerts) != 2 {
		t.Fatalf("alertas de desalineación = %d, want 2", len(alerts))
	}
	for _, a := range alerts {
		if a.Severity != evidence.SeverityHigh || a.Layer != evidence.LayerPlatform || !strings.Contains(a.Summary, "reconcile-roles --apply") {
			t.Fatalf("alerta: %+v", a)
		}
	}
	// Una por persona y por estado: la pasada siguiente no la repite.
	if again := byRule(evaluate(t, env, rules), evidence.RulePlatformRoleDrift); len(again) != 0 {
		t.Fatalf("no se repite: %d", len(again))
	}
	// Se puede apagar con NEXO_ALERT_RULES.
	off := evidence.DefaultRules()
	cfg := off[evidence.RulePlatformRoleDrift]
	cfg.Enabled = false
	off[evidence.RulePlatformRoleDrift] = cfg
	if got := byRule(evaluate(t, env, off), evidence.RulePlatformRoleDrift); len(got) != 0 {
		t.Fatal("regla apagada")
	}

	// Revisión de accesos: marca y total; el rol de plataforma es el de Nexo.
	rep, err = evidence.BuildAccessReview(ctx, env.DB, 90, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.RoleDrift != 2 {
		t.Fatalf("resumen: %+v", rep.Summary)
	}
	adm, luis := person(rep, tm.Adm), person(rep, tm.Luis)
	if adm == nil || luis == nil || !slices.Contains(adm.Flags, "role_drift") || !slices.Contains(luis.Flags, "role_drift") {
		t.Fatalf("marcas: %+v %+v", adm, luis)
	}
	if adm.PlatformRole != "admin" || luis.PlatformRole != "miembro" || luis.EngramRole != "admin" {
		t.Fatalf("la columna de Engram no cambia el rol: adm=%s luis=%s/%s", adm.PlatformRole, luis.PlatformRole, luis.EngramRole)
	}
	if p := person(rep, tm.Aud); p == nil || slices.Contains(p.Flags, "role_drift") {
		t.Fatal("el auditor está alineado")
	}
}
