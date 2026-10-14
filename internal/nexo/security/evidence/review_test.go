package evidence_test

// S4-04 (RS-33, CA-S12): revisión de accesos y cuentas inactivas.

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

func person(rep evidence.AccessReview, id string) *evidence.ReviewPerson {
	for i := range rep.People {
		if rep.People[i].PrincipalID == id {
			return &rep.People[i]
		}
	}
	return nil
}

func TestAccessReviewReportAndCompletion(t *testing.T) {
	env := kbtest.New(t)
	ctx := context.Background()
	jefe := env.Principal("jefe", "human", "member") // el superadmin se asigna con la fila de abajo (la columna de Engram se deriva)
	ana := env.Principal("ana", "human", "member")
	viejo := env.Principal("viejo", "human", "member")
	ci := env.Principal("ci-bot", "service_account", "member")
	env.Exec(`INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role, granted_by_principal_id) VALUES ($1::bigint, 'platform', '*', 'superadmin', NULL)`, jefe)
	env.Exec(`INSERT INTO nexo_scopes (scope_type, scope_key) VALUES ('space', 'estandares') ON CONFLICT DO NOTHING`)
	env.Exec(`INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role, granted_by_principal_id) VALUES ($1::bigint, 'space', 'estandares', 'publicador', $2::bigint)`, ana, jefe)
	env.Exec(`INSERT INTO cloud_project_grants (principal_id, project, granted_by_principal_id) VALUES ($1::bigint, 'nexo-transversal', $2::bigint)`, ana, jefe)
	env.Exec(`UPDATE cloud_principal_tokens SET nexo_expires_at = NULL WHERE principal_id = $1::bigint`, ci)
	env.Exec(`UPDATE cloud_principal_tokens SET last_used_at = now() - interval '1 day' WHERE principal_id = $1::bigint`, ana)
	env.Exec(`UPDATE cloud_principal_tokens SET created_at = now() - interval '200 days', last_used_at = now() - interval '150 days' WHERE principal_id = $1::bigint`, viejo)
	env.Exec(`UPDATE cloud_principals SET created_at = now() - interval '300 days' WHERE id = $1::bigint`, viejo)

	rep, err := evidence.BuildAccessReview(ctx, env.DB, 90, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pj, pa, pv, pc := person(rep, jefe), person(rep, ana), person(rep, viejo), person(rep, ci)
	if pj == nil || pj.PlatformRole != "superadmin" || pa.PlatformRole != "miembro" {
		t.Fatalf("roles de plataforma: %+v %+v", pj, pa)
	}
	if len(pa.Scopes) != 1 || pa.Scopes[0].Role != "publicador" || pa.Scopes[0].GrantedBy != "jefe" || pa.Scopes[0].Layer != evidence.LayerKnowledge {
		t.Fatalf("roles por ámbito de ana: %+v", pa.Scopes)
	}
	if len(pa.ProjectGrants) != 1 || pa.ProjectGrants[0].ScopeKey != "nexo-transversal" || pa.ProjectGrants[0].Layer != evidence.LayerProjects {
		t.Fatalf("permisos de proyecto de ana: %+v", pa.ProjectGrants)
	}
	if !slices.Contains(pv.Flags, "inactive") || pv.InactiveDays < 149 || slices.Contains(pa.Flags, "inactive") {
		t.Fatalf("inactivos: viejo %+v ana %+v", pv.Flags, pa.Flags)
	}
	if !slices.Contains(pc.Flags, "token_no_expiry") || !pc.Tokens[0].NoExpiry || rep.Summary.TokensNoExpiry != 1 {
		t.Fatalf("token sin caducidad: %+v %+v", pc, rep.Summary)
	}
	if rep.Summary.Inactive != 1 || rep.Summary.People != 4 || rep.LastReview != nil {
		t.Fatalf("resumen: %+v", rep.Summary)
	}
	raw, _ := json.Marshal(rep)
	if strings.Contains(string(raw), "hash-") || strings.Contains(string(raw), "egc_kbtest") {
		t.Fatalf("el reporte no lleva hashes ni prefijos de token: %s", raw)
	}
	rec, err := evidence.CompleteReview(ctx, env.DB, rep, evidence.Reviewer{PrincipalID: jefe, Label: "jefe", ClientIP: "10.0.0.1"}, "revisión trimestral")
	if err != nil || rec.ID == 0 || rec.ReportSHA256 != evidence.ReportDigest(rep) || rec.Notes != "revisión trimestral" {
		t.Fatalf("revisión: %+v %v", rec, err)
	}
	rep2, _ := evidence.BuildAccessReview(ctx, env.DB, 90, time.Now())
	if rep2.LastReview == nil || rep2.LastReview.ID != rec.ID || rep2.LastReview.Reviewer != "jefe" {
		t.Fatalf("última revisión: %+v", rep2.LastReview)
	}
	if evidence.ReportDigest(rep2) != rec.ReportSHA256 {
		t.Fatal("el hash del reporte no depende de la hora ni de las revisiones previas")
	}
}

// DS-4: deshabilitar inactivos (con evento y alerta), nunca al último
// superadmin.
func TestDisableInactive(t *testing.T) {
	env := kbtest.New(t)
	ctx := context.Background()
	jefe := env.Principal("jefe", "human", "member") // el superadmin se asigna con la fila de abajo (la columna de Engram se deriva)
	viejo := env.Principal("viejo", "human", "member")
	nuevo := env.Principal("nuevo", "human", "member")
	env.Exec(`INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role) VALUES ($1::bigint, 'platform', '*', 'superadmin')`, jefe)
	for _, id := range []string{jefe, viejo} {
		env.Exec(`UPDATE cloud_principal_tokens SET created_at = now() - interval '200 days', last_used_at = NULL WHERE principal_id = $1::bigint`, id)
		env.Exec(`UPDATE cloud_principals SET created_at = now() - interval '200 days' WHERE id = $1::bigint`, id)
	}
	done, alerts, err := evidence.DisableInactive(ctx, env.DB, 90, time.Now(), evidence.DefaultRules()[evidence.RuleInactiveDisabled])
	if err != nil || len(done) != 1 || done[0].PrincipalID != viejo || len(alerts) != 1 || alerts[0].Rule != evidence.RuleInactiveDisabled {
		t.Fatalf("deshabilitar: %+v %+v %v", done, alerts, err)
	}
	var enabled bool
	if err := env.DB.QueryRow(`SELECT enabled FROM cloud_principals WHERE id = $1::bigint`, viejo).Scan(&enabled); err != nil || enabled {
		t.Fatalf("viejo debe quedar deshabilitado: %v %v", enabled, err)
	}
	for _, id := range []string{jefe, nuevo} {
		if err := env.DB.QueryRow(`SELECT enabled FROM cloud_principals WHERE id = $1::bigint`, id).Scan(&enabled); err != nil || !enabled {
			t.Fatalf("%s sigue habilitado (último superadmin o activo): %v %v", id, enabled, err)
		}
	}
	var ev, aud int
	_ = env.DB.QueryRow(`SELECT count(*) FROM nexo_authz_events WHERE action = 'principal.disabled_inactive' AND target_principal_id = $1::bigint`, viejo).Scan(&ev)
	_ = env.DB.QueryRow(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = 'user.disable' AND reason_code = 'inactive' AND target_principal_id = $1::bigint`, viejo).Scan(&aud)
	if ev != 1 || aud != 1 {
		t.Fatalf("evento %d, auditoría %d", ev, aud)
	}
	if done, _, _ := evidence.DisableInactive(ctx, env.DB, 90, time.Now(), evidence.DefaultRules()[evidence.RuleInactiveDisabled]); len(done) != 0 {
		t.Fatalf("idempotente: %+v", done)
	}
	t.Setenv(evidence.DisableInactiveEnv, "")
	if evidence.DisableInactiveDays() != 0 {
		t.Fatal("vacío = desactivado")
	}
	t.Setenv(evidence.DisableInactiveEnv, "3")
	if evidence.DisableInactiveDays() != 0 {
		t.Fatal("menos que el mínimo = desactivado")
	}
	t.Setenv(evidence.DisableInactiveEnv, "90")
	if evidence.DisableInactiveDays() != 90 {
		t.Fatal("90")
	}
}
