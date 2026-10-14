package evidence_test

// SG-06: alertas de continuidad del acceso de administración (un solo
// superadmin, token privilegiado por vencer) y sus marcas en la revisión de
// accesos.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

func TestSingleSuperadminAlertAndReviewFlag(t *testing.T) {
	env := kbtest.New(t)
	tm := env.NewTeam() // root es el único superadmin; adm es admin; aud, auditor
	ctx := context.Background()
	rules := evidence.DefaultRules()

	cfg := rules[evidence.RuleSingleSuperadmin]
	if !cfg.Enabled || cfg.Severity != evidence.SeverityHigh {
		t.Fatalf("la regla debe venir activa y alta: %+v", cfg)
	}

	alerts := byRule(evaluate(t, env, rules), evidence.RuleSingleSuperadmin)
	if len(alerts) != 1 {
		t.Fatalf("alertas de superadmin único = %d, want 1", len(alerts))
	}
	a := alerts[0]
	if a.Severity != evidence.SeverityHigh || a.Layer != evidence.LayerPlatform || a.Subject != "principal:"+tm.Root ||
		!strings.Contains(a.Summary, "un solo superadmin") || !strings.Contains(a.Summary, "root") {
		t.Fatalf("alerta: %+v", a)
	}
	// No se repite en la pasada siguiente (una por día y un recordatorio por semana).
	if again := byRule(evaluate(t, env, rules), evidence.RuleSingleSuperadmin); len(again) != 0 {
		t.Fatalf("no se repite: %+v", again)
	}

	// Revisión de accesos: la persona lleva la marca y el resumen cuenta uno.
	rep, err := evidence.BuildAccessReview(ctx, env.DB, 90, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.SuperadminsEnabled != 1 {
		t.Fatalf("resumen: %+v", rep.Summary)
	}
	if p := person(rep, tm.Root); p == nil || !slices.Contains(p.Flags, "single_superadmin") {
		t.Fatalf("root debe llevar la marca single_superadmin: %+v", p)
	}
	if p := person(rep, tm.Adm); p == nil || slices.Contains(p.Flags, "single_superadmin") {
		t.Fatalf("adm no es superadmin: %+v", p)
	}

	// Con un segundo superadmin, ni alerta nueva ni marca.
	if _, err := env.Roles.SetPlatformRole(env.Ctx(), env.ID(tm.Root), tm.Adm, authz.RoleSuperadmin); err != nil {
		t.Fatal(err)
	}
	if got := byRule(evaluate(t, env, rules), evidence.RuleSingleSuperadmin); len(got) != 0 {
		t.Fatalf("con dos superadmins no hay alerta: %+v", got)
	}
	rep, err = evidence.BuildAccessReview(ctx, env.DB, 90, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.SuperadminsEnabled != 2 {
		t.Fatalf("resumen: %+v", rep.Summary)
	}
	for _, id := range []string{tm.Root, tm.Adm} {
		if p := person(rep, id); p == nil || slices.Contains(p.Flags, "single_superadmin") {
			t.Fatalf("con dos superadmins no hay marca: %+v", p)
		}
	}

	// Se deshabilita al primero: queda solo adm y la alerta vuelve, ahora por él.
	env.Exec(`UPDATE cloud_principals SET enabled = FALSE WHERE id = $1::bigint`, tm.Root)
	again := byRule(evaluate(t, env, rules), evidence.RuleSingleSuperadmin)
	if len(again) != 1 || again[0].Subject != "principal:"+tm.Adm {
		t.Fatalf("alerta por el nuevo único superadmin: %+v", again)
	}
	// Se puede cambiar la severidad con NEXO_ALERT_RULES.
	custom, err := evidence.ParseRules(`{"single_superadmin":{"severity":"critical","cooldown_seconds":0}}`)
	if err != nil || custom[evidence.RuleSingleSuperadmin].Severity != evidence.SeverityCritical {
		t.Fatalf("NEXO_ALERT_RULES: %v", err)
	}
}

func setTokenExpiry(env *kbtest.Env, principal string, interval string) {
	env.T.Helper()
	env.Exec(`UPDATE cloud_principal_tokens SET nexo_expires_at = now() + $2::interval WHERE principal_id = $1::bigint`, principal, interval)
}

// Avisos a 30 y a 7 días para tokens de superadmin, admin y auditor; uno por
// token y por aviso; ni miembros, ni revocados, ni cuentas deshabilitadas.
func TestPrivilegedTokenExpiringAlert(t *testing.T) {
	env := kbtest.New(t)
	tm := env.NewTeam()
	ctx := context.Background()
	rules := evidence.DefaultRules()
	if !rules[evidence.RulePrivilegedTokenExpiring].Enabled {
		t.Fatal("la regla debe venir activa")
	}

	// Tokens recién creados (90 días): sin avisos.
	if got := byRule(evaluate(t, env, rules), evidence.RulePrivilegedTokenExpiring); len(got) != 0 {
		t.Fatalf("a 90 días no hay aviso: %+v", got)
	}

	setTokenExpiry(env, tm.Root, "20 days") // superadmin: aviso de 30
	setTokenExpiry(env, tm.Adm, "5 days")   // admin: aviso de 7
	setTokenExpiry(env, tm.Aud, "60 days")  // auditor lejos: nada
	setTokenExpiry(env, tm.Luis, "3 days")  // miembro: nada
	setTokenExpiry(env, tm.CI, "2 days")    // cuenta de servicio: nada
	got := byRule(evaluate(t, env, rules), evidence.RulePrivilegedTokenExpiring)
	if len(got) != 2 {
		t.Fatalf("avisos = %d, want 2: %+v", len(got), got)
	}
	byToken := map[string]evidence.Alert{}
	for _, a := range got {
		byToken[a.Subject] = a
	}
	root, adm := byToken["token:"+env.Token(tm.Root)], byToken["token:"+env.Token(tm.Adm)]
	if root.ID == 0 || adm.ID == 0 {
		t.Fatalf("avisos por token: %+v", byToken)
	}
	if root.Severity != evidence.SeverityWarning || root.Details["notice_days"] != float64(30) || !strings.Contains(root.Summary, "superadmin") {
		t.Fatalf("aviso de 30 días: %+v", root)
	}
	if adm.Severity != evidence.SeverityHigh || adm.Details["notice_days"] != float64(7) || !strings.Contains(adm.Summary, "admin") {
		t.Fatalf("aviso de 7 días sube a alta: %+v", adm)
	}
	for _, a := range got {
		if strings.Contains(a.Summary, "hash-") || strings.Contains(a.Summary, "egc_") {
			t.Fatalf("el aviso no lleva secretos: %s", a.Summary)
		}
	}

	// Sin duplicar: la pasada siguiente no repite.
	if again := byRule(evaluate(t, env, rules), evidence.RulePrivilegedTokenExpiring); len(again) != 0 {
		t.Fatalf("no se repite: %+v", again)
	}

	// Revisión de accesos: marca de la persona y total.
	rep, err := evidence.BuildAccessReview(ctx, env.DB, 90, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.PrivilegedTokensExpiring != 2 {
		t.Fatalf("resumen: %+v", rep.Summary)
	}
	for _, id := range []string{tm.Root, tm.Adm} {
		if p := person(rep, id); p == nil || !slices.Contains(p.Flags, "token_expiring") {
			t.Fatalf("falta la marca token_expiring: %+v", p)
		}
	}
	for _, id := range []string{tm.Aud, tm.Luis} {
		if p := person(rep, id); p == nil || slices.Contains(p.Flags, "token_expiring") {
			t.Fatalf("no debe llevar la marca: %+v", p)
		}
	}

	// El token del superadmin se acerca a los 7 días: aviso nuevo (otra etapa).
	setTokenExpiry(env, tm.Root, "6 days")
	next := byRule(evaluate(t, env, rules), evidence.RulePrivilegedTokenExpiring)
	if len(next) != 1 || next[0].Details["notice_days"] != float64(7) || next[0].Severity != evidence.SeverityHigh {
		t.Fatalf("aviso de 7 días del superadmin: %+v", next)
	}
	if again := byRule(evaluate(t, env, rules), evidence.RulePrivilegedTokenExpiring); len(again) != 0 {
		t.Fatalf("tampoco se repite el de 7 días: %+v", again)
	}

	// Un token revocado o de una cuenta deshabilitada no avisa.
	env.Exec(`UPDATE cloud_principal_tokens SET nexo_expires_at = now() + interval '1 day', revoked_at = now() WHERE principal_id = $1::bigint`, tm.Aud)
	env.Exec(`UPDATE cloud_principal_tokens SET nexo_expires_at = now() + interval '1 day' WHERE principal_id = $1::bigint`, tm.Adm)
	env.Exec(`UPDATE cloud_principals SET enabled = FALSE WHERE id = $1::bigint`, tm.Adm)
	if got := byRule(evaluate(t, env, rules), evidence.RulePrivilegedTokenExpiring); len(got) != 0 {
		t.Fatalf("revocado o deshabilitado no avisa: %+v", got)
	}
}

// Un servidor recién instalado (sin ninguna persona) no avisa de que falta un
// superadmin: todavía no se ejecutó `engram cloud bootstrap admin`.
func TestSingleSuperadminSilentBeforeBootstrap(t *testing.T) {
	env := kbtest.New(t)
	rules := evidence.DefaultRules()
	if got := byRule(evaluate(t, env, rules), evidence.RuleSingleSuperadmin); len(got) != 0 {
		t.Fatalf("sin personas no hay alerta: %+v", got)
	}
	// Con personas pero sin superadmin habilitado (solo por SQL), sí avisa.
	env.Principal("ana", "human", "member")
	got := byRule(evaluate(t, env, rules), evidence.RuleSingleSuperadmin)
	if len(got) != 1 || got[0].Subject != "platform" || !strings.Contains(got[0].Summary, "ningún superadmin") {
		t.Fatalf("sin superadmin habilitado: %+v", got)
	}
}
