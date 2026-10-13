package evidence_test

// SG-10: NEXO_DISABLE_INACTIVE_DAYS solo deshabilita personas; las cuentas de
// servicio sin uso dejan una alerta (NEXO_SA_STALE_DAYS) y nunca se
// deshabilitan.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

// serviceAccount crea una cuenta de servicio con su fila lateral y su
// responsable; created es la antigüedad del alta y del token (sin uso).
func serviceAccount(env *kbtest.Env, slug, owner string, created time.Duration) string {
	env.T.Helper()
	id := env.Principal(slug, "service_account", "member")
	env.Exec(`INSERT INTO nexo_service_accounts (principal_id, slug, owner_principal_id, created_at) VALUES ($1::bigint, $2, $3::bigint, now() - make_interval(secs => $4::double precision))`,
		id, slug, owner, created.Seconds())
	env.Exec(`UPDATE cloud_principals SET created_at = now() - make_interval(secs => $2::double precision) WHERE id = $1::bigint`, id, created.Seconds())
	env.Exec(`UPDATE cloud_principal_tokens SET created_at = now() - make_interval(secs => $2::double precision), last_used_at = NULL WHERE principal_id = $1::bigint`, id, created.Seconds())
	return id
}

const day = 24 * time.Hour

func TestDisableInactiveSkipsServiceAccounts(t *testing.T) {
	env := kbtest.New(t)
	ctx := context.Background()
	jefe := env.Principal("jefe", "human", "admin")
	env.Exec(`INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role) VALUES ($1::bigint, 'platform', '*', 'superadmin')`, jefe)
	viejo := env.Principal("viejo", "human", "member")
	env.Exec(`UPDATE cloud_principals SET created_at = now() - interval '200 days' WHERE id = $1::bigint`, viejo)
	env.Exec(`UPDATE cloud_principal_tokens SET created_at = now() - interval '200 days', last_used_at = now() - interval '150 days' WHERE principal_id = $1::bigint`, viejo)
	// Un pipeline que publica cada pocos meses: 150 días sin uso.
	ci := serviceAccount(env, "ci-trimestral", jefe, 300*day)
	env.Exec(`UPDATE cloud_principal_tokens SET last_used_at = now() - interval '150 days' WHERE principal_id = $1::bigint`, ci)
	// Una cuenta de servicio sin fila lateral (alta directa en Engram).
	bot := env.Principal("bot-suelto", "service_account", "member")
	env.Exec(`UPDATE cloud_principals SET created_at = now() - interval '300 days' WHERE id = $1::bigint`, bot)
	env.Exec(`UPDATE cloud_principal_tokens SET created_at = now() - interval '300 days', last_used_at = NULL WHERE principal_id = $1::bigint`, bot)

	done, alerts, err := evidence.DisableInactive(ctx, env.DB, 90, time.Now(), evidence.DefaultRules()[evidence.RuleInactiveDisabled])
	if err != nil || len(done) != 1 || done[0].PrincipalID != viejo || len(alerts) != 1 {
		t.Fatalf("solo la persona inactiva se deshabilita: %+v %+v %v", done, alerts, err)
	}
	for id, want := range map[string]bool{viejo: false, ci: true, bot: true, jefe: true} {
		var enabled bool
		if err := env.DB.QueryRow(`SELECT enabled FROM cloud_principals WHERE id = $1::bigint`, id).Scan(&enabled); err != nil || enabled != want {
			t.Fatalf("principal %s: enabled=%v, se esperaba %v (%v)", id, enabled, want, err)
		}
	}
	var ev int
	_ = env.DB.QueryRow(`SELECT count(*) FROM nexo_authz_events WHERE action = 'principal.disabled_inactive' AND target_principal_id IN ($1::bigint, $2::bigint)`, ci, bot).Scan(&ev)
	if ev != 0 {
		t.Fatalf("una cuenta de servicio no deja evento de deshabilitación: %d", ev)
	}
}

func TestServiceAccountStaleAlerts(t *testing.T) {
	env := kbtest.New(t)
	ctx := context.Background()
	rule := evidence.DefaultRules()[evidence.RuleSAStale]
	ana := env.Principal("ana", "human", "member")
	nunca := serviceAccount(env, "ci-nunca", ana, 200*day)       // nunca usada, alta hace 200 días
	vieja := serviceAccount(env, "ci-vieja", ana, 400*day)       // usada hace 190 días
	reciente := serviceAccount(env, "ci-reciente", ana, 400*day) // token usado ayer
	evento := serviceAccount(env, "ci-evento", ana, 400*day)     // sin last_used_at, pero con acción registrada hoy
	nueva := serviceAccount(env, "ci-nueva", ana, 10*day)        // alta reciente, nunca usada
	apagada := serviceAccount(env, "ci-apagada", ana, 400*day)   // deshabilitada: no avisa
	env.Exec(`UPDATE cloud_principal_tokens SET last_used_at = now() - interval '190 days' WHERE principal_id = $1::bigint`, vieja)
	env.Exec(`UPDATE cloud_principal_tokens SET last_used_at = now() - interval '1 day' WHERE principal_id = $1::bigint`, reciente)
	env.Exec(`INSERT INTO nexo_authz_events (action, scope_type, scope_key, actor_principal_id, actor_label, details) VALUES ('authz.denied', 'space', 'wiki', $1::bigint, 'ci-evento', '{}'::jsonb)`, evento)
	env.Exec(`UPDATE cloud_principals SET enabled = FALSE WHERE id = $1::bigint`, apagada)

	// NEXO_SA_STALE_DAYS=0: nada.
	if got, err := evidence.ServiceAccountStaleAlerts(ctx, env.DB, 0, time.Now(), rule); err != nil || len(got) != 0 {
		t.Fatalf("0 = desactivado: %+v %v", got, err)
	}
	got, err := evidence.ServiceAccountStaleAlerts(ctx, env.DB, 180, time.Now(), rule)
	if err != nil || len(got) != 2 {
		t.Fatalf("alertas: %+v %v", got, err)
	}
	subjects := map[string]evidence.Alert{}
	for _, a := range got {
		subjects[a.Subject] = a
		if a.Rule != evidence.RuleSAStale || a.Layer != evidence.LayerPlatform || a.Severity != evidence.SeverityWarning {
			t.Fatalf("regla, capa o severidad: %+v", a)
		}
		if !strings.Contains(a.Summary, "responsable: ana") || a.Details["owner"] != "ana" {
			t.Fatalf("la alerta nombra al responsable: %+v", a)
		}
	}
	an, av := subjects["principal:"+nunca], subjects["principal:"+vieja]
	if !strings.Contains(an.Summary, "ci-nunca") || !strings.Contains(an.Summary, "nunca se usó") || an.Details["slug"] != "ci-nunca" {
		t.Fatalf("cuenta nunca usada: %+v", an)
	}
	if !strings.Contains(av.Summary, "ci-vieja") || !strings.Contains(av.Summary, "desde hace 190 días") {
		t.Fatalf("cuenta sin uso: %+v", av)
	}
	for _, id := range []string{reciente, evento, nueva, apagada} {
		if _, ok := subjects["principal:"+id]; ok {
			t.Fatalf("la cuenta %s no debe avisar", id)
		}
	}
	// La pasada siguiente no repite.
	if again, err := evidence.ServiceAccountStaleAlerts(ctx, env.DB, 180, time.Now().Add(day), rule); err != nil || len(again) != 0 {
		t.Fatalf("sin repetir: %+v %v", again, err)
	}
	var n int
	_ = env.DB.QueryRow(`SELECT count(*) FROM nexo_alerts WHERE rule = $1`, evidence.RuleSAStale).Scan(&n)
	if n != 2 {
		t.Fatalf("filas en nexo_alerts: %d", n)
	}
	// Nunca se deshabilita.
	var enabled bool
	if err := env.DB.QueryRow(`SELECT enabled FROM cloud_principals WHERE id = $1::bigint`, nunca).Scan(&enabled); err != nil || !enabled {
		t.Fatalf("la cuenta sigue habilitada: %v %v", enabled, err)
	}
	// Se vuelve a usar y otra vez queda sin uso: avisa de nuevo (otro período).
	env.Exec(`UPDATE cloud_principal_tokens SET last_used_at = now() - interval '185 days' WHERE principal_id = $1::bigint`, vieja)
	again, err := evidence.ServiceAccountStaleAlerts(ctx, env.DB, 180, time.Now(), rule)
	if err != nil || len(again) != 1 || again[0].Subject != "principal:"+vieja {
		t.Fatalf("nuevo período sin uso: %+v %v", again, err)
	}
	// Regla desactivada (NEXO_ALERT_RULES): nada.
	off := rule
	off.Enabled = false
	env.Exec(`UPDATE cloud_principal_tokens SET last_used_at = now() - interval '200 days' WHERE principal_id = $1::bigint`, reciente)
	if got, err := evidence.ServiceAccountStaleAlerts(ctx, env.DB, 180, time.Now(), off); err != nil || len(got) != 0 {
		t.Fatalf("regla apagada: %+v %v", got, err)
	}

	// La revisión de accesos marca "stale" (no "inactive") a las cuentas de
	// servicio sin uso.
	t.Setenv(evidence.SAStaleDaysEnv, "180")
	rep, err := evidence.BuildAccessReview(ctx, env.DB, 90, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pn, pe := person(rep, nunca), person(rep, evento)
	if pn == nil || !slices.Contains(pn.Flags, "stale") || slices.Contains(pn.Flags, "inactive") || pn.Username != "ci-nunca" || pn.InactiveDays < 199 {
		t.Fatalf("revisión: cuenta nunca usada: %+v", pn)
	}
	if pe == nil || slices.Contains(pe.Flags, "stale") || slices.Contains(pe.Flags, "inactive") {
		t.Fatalf("revisión: cuenta con acción reciente: %+v", pe)
	}
	if rep.SAStaleDays != 180 || rep.Summary.StaleServiceAccounts != 3 { // ci-nunca, ci-vieja y ci-reciente (ahora 200 días)
		t.Fatalf("revisión: resumen: %d %+v", rep.SAStaleDays, rep.Summary)
	}
	t.Setenv(evidence.SAStaleDaysEnv, "0")
	if rep, _ := evidence.BuildAccessReview(ctx, env.DB, 90, time.Now()); rep.Summary.StaleServiceAccounts != 0 {
		t.Fatalf("revisión con 0: %+v", rep.Summary)
	}
}

func TestParseSAStaleDays(t *testing.T) {
	cases := []struct {
		raw  string
		want int
		bad  bool
	}{
		{"", evidence.DefaultSAStaleDays, false},
		{"  ", evidence.DefaultSAStaleDays, false},
		{"0", 0, false},
		{"30", 30, false},
		{" 365 ", 365, false},
		{"-1", evidence.DefaultSAStaleDays, true},
		{"abc", evidence.DefaultSAStaleDays, true},
		{"1.5", evidence.DefaultSAStaleDays, true},
		{"999999", evidence.DefaultSAStaleDays, true},
	}
	for _, c := range cases {
		got, err := evidence.ParseSAStaleDays(c.raw)
		if got != c.want || (err != nil) != c.bad {
			t.Errorf("ParseSAStaleDays(%q) = %d, %v; se esperaba %d (error %v)", c.raw, got, err, c.want, c.bad)
		}
	}
	t.Setenv(evidence.SAStaleDaysEnv, "")
	if evidence.SAStaleDays() != 180 {
		t.Fatal("vacío = 180")
	}
	t.Setenv(evidence.SAStaleDaysEnv, "0")
	if evidence.SAStaleDays() != 0 {
		t.Fatal("0 = desactivado")
	}
	t.Setenv(evidence.SAStaleDaysEnv, "nada")
	if evidence.SAStaleDays() != 180 {
		t.Fatal("inválido = 180")
	}
}
