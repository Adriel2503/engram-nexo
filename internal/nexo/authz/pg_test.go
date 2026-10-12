package authz_test

// Tests del motor contra Postgres real (CLOUDSTORE_TEST_DSN): migraciones,
// triggers, arranque del superadmin, reglas R-1..R-15 en el servicio,
// visibilidad, eventos y costo por petición (RNF-R1).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
)

type pgEnv struct {
	t      *testing.T
	dsn    string
	cs     *cloudstore.CloudStore
	db     *sql.DB
	eng    *authz.Engine
	svc    *authz.Service
	seq    int
	tokens map[string]string
}

func newPGEnv(t *testing.T) *pgEnv {
	t.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set (requires Postgres)")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("nexo_authz_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	env := &pgEnv{t: t, dsn: dsn + sep + "search_path=" + schema, tokens: map[string]string{}}
	env.open()
	return env
}

func (e *pgEnv) open() {
	e.t.Helper()
	cs, err := cloudstore.New(cloud.Config{DSN: e.dsn})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = cs.Close() })
	e.cs, e.db = cs, cs.NexoDB()
	e.eng = authz.NewPostgres(e.db, authz.WithProjectNormalizer(cloudstore.NormalizeProjectGrant))
	e.svc = authz.NewService(e.db, e.eng)
}

func (e *pgEnv) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

// principal crea una persona (con usuario y token) o una cuenta de servicio.
func (e *pgEnv) principal(name, kind, engramRole string) string {
	e.t.Helper()
	var id string
	if err := e.db.QueryRow(`INSERT INTO cloud_principals (kind, display_name, role) VALUES ($1, $2, $3) RETURNING id::text`, kind, name, engramRole).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	if kind == authz.KindHuman {
		e.exec(`INSERT INTO cloud_human_users (principal_id, username, email) VALUES ($1::bigint, $2, $3)`, id, name, name+"@rp.pe")
	}
	e.seq++
	var tok string
	if err := e.db.QueryRow(`INSERT INTO cloud_principal_tokens (principal_id, token_prefix, token_hash, name) VALUES ($1::bigint, $2, $3, $4) RETURNING id::text`,
		id, fmt.Sprintf("egc_test_%d", e.seq), fmt.Sprintf("hash-%s-%d", name, e.seq), "laptop-"+name).Scan(&tok); err != nil {
		e.t.Fatal(err)
	}
	e.tokens[id] = tok
	return id
}

func (e *pgEnv) id(principalID, kind string) authz.Identity {
	return authz.Identity{PrincipalID: principalID, TokenID: e.tokens[principalID], Kind: kind, Label: "p" + principalID,
		Verified: true, Enabled: true, ClientIP: "10.1.1.1", UserAgent: "nexo-test/1.0", ClientClaim: "PC-" + principalID}
}

func (e *pgEnv) ctx() context.Context { return authz.WithRequestCache(context.Background()) }

func (e *pgEnv) can(id authz.Identity, s authz.Scope, c authz.Capability) authz.Decision {
	e.t.Helper()
	d, err := e.eng.Can(context.Background(), id, s, c)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

func (e *pgEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func wantRule(t *testing.T, err error, code string) {
	t.Helper()
	var re *authz.RuleError
	if !errors.As(err, &re) || re.Code != code {
		t.Fatalf("esperaba regla %s, got %v", code, err)
	}
}

func wantDenied(t *testing.T, err error, hidden bool) *authz.DeniedError {
	t.Helper()
	var de *authz.DeniedError
	if !errors.As(err, &de) || de.Decision.Hidden != hidden {
		t.Fatalf("esperaba denegación (hidden=%v), got %v", hidden, err)
	}
	return de
}

// ─── Migraciones y triggers ──────────────────────────────────────────────────

func TestPGMigrationIsIdempotent(t *testing.T) {
	env := newPGEnv(t)
	env.open() // segunda migración sobre el mismo esquema
	tx, err := env.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := authz.Migrate(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := env.count(`SELECT COUNT(*) FROM nexo_scopes WHERE scope_type = 'platform' AND scope_key = '*'`); n != 1 {
		t.Fatalf("fila de plataforma = %d", n)
	}
	for _, trig := range []string{"nexo_role_bindings_validate", "nexo_role_bindings_guard", "nexo_authz_guard_principal", "nexo_authz_events_no_update_delete", "nexo_authz_events_no_truncate"} {
		if n := env.count(`SELECT COUNT(*) FROM pg_trigger WHERE tgname = $1`, trig); n < 1 {
			t.Fatalf("falta el trigger %s", trig)
		}
	}
	if n := env.count(`SELECT COUNT(*) FROM nexo_scopes WHERE scope_type <> 'platform'`); n != 0 {
		t.Fatal("la migración no crea filas para proyectos existentes (design §6.4)")
	}
}

func TestPGTriggerValidatesRoleCombinations(t *testing.T) {
	env := newPGEnv(t)
	ana := env.principal("ana", authz.KindHuman, "member")
	ci := env.principal("ci", authz.KindServiceAccount, "member")
	env.exec(`INSERT INTO nexo_scopes (scope_type, scope_key, visibility) VALUES ('space', 'general', 'open'), ('project', 'alpha', 'restricted')`)
	bad := []struct {
		pid, typ, key, role string
	}{
		{ana, "platform", "*", "lector"},
		{ana, "platform", "*", "miembro"},
		{ana, "project", "alpha", "admin"},
		{ana, "space", "general", "superadmin"},
		{ci, "platform", "*", "auditor"},        // R-10
		{ci, "space", "general", "responsable"}, // R-10
	}
	for _, b := range bad {
		if _, err := env.db.Exec(`INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role) VALUES ($1::bigint, $2, $3, $4)`, b.pid, b.typ, b.key, b.role); err == nil {
			t.Fatalf("debía rechazar %v", b)
		}
	}
	env.exec(`INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role) VALUES ($1::bigint, 'space', 'general', 'publicador')`, ci)
	if _, err := env.db.Exec(`UPDATE nexo_role_bindings SET role = 'responsable' WHERE principal_id = $1::bigint`, ci); err == nil {
		t.Fatal("UPDATE a responsable de una cuenta de servicio debía fallar")
	}
	if _, err := env.db.Exec(`INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role) VALUES ($1::bigint, 'space', 'no-existe', 'lector')`, ana); err == nil {
		t.Fatal("FK a nexo_scopes")
	}
}

func TestPGEventsAreImmutable(t *testing.T) {
	env := newPGEnv(t)
	carla := env.principal("carla", authz.KindHuman, "admin")
	_ = env.can(env.id(carla, authz.KindHuman), authz.PlatformScope(), authz.CapSelfRead) // arranque: genera un evento
	if n := env.count(`SELECT COUNT(*) FROM nexo_authz_events`); n != 1 {
		t.Fatalf("eventos = %d", n)
	}
	for _, q := range []string{`UPDATE nexo_authz_events SET action = 'x'`, `DELETE FROM nexo_authz_events`, `TRUNCATE nexo_authz_events`} {
		if _, err := env.db.Exec(q); err == nil || !strings.Contains(err.Error(), "solo inserción") {
			t.Fatalf("%s debía fallar: %v", q, err)
		}
	}
}

// ─── Arranque del superadmin (RF-01, CA-R01) ─────────────────────────────────

func TestPGBootstrapSuperadminAtMigration(t *testing.T) {
	env := newPGEnv(t)
	sa := env.principal("svc-admin", authz.KindServiceAccount, "admin")
	off := env.principal("disabled-admin", authz.KindHuman, "admin")
	env.exec(`UPDATE cloud_principals SET enabled = FALSE WHERE id = $1::bigint`, off)
	carla := env.principal("carla", authz.KindHuman, "admin")
	pedro := env.principal("pedro", authz.KindHuman, "admin")
	_ = sa
	env.open() // reinicio del servidor: la migración asigna al candidato
	var got string
	if err := env.db.QueryRow(`SELECT principal_id::text FROM nexo_role_bindings WHERE scope_type = 'platform' AND role = 'superadmin'`).Scan(&got); err != nil || got != carla {
		t.Fatalf("superadmin de arranque = %q (%v), want carla %s", got, err, carla)
	}
	var actor string
	var verified bool
	if err := env.db.QueryRow(`SELECT actor_label, actor_verified FROM nexo_authz_events WHERE action = 'binding.granted' AND target_principal_id = $1::bigint`, carla).Scan(&actor, &verified); err != nil || actor != authz.SystemActorLabel || verified {
		t.Fatalf("evento de arranque: %q %v %v", actor, verified, err)
	}
	if role, _ := env.eng.EffectiveRole(context.Background(), env.id(pedro, authz.KindHuman), authz.PlatformScope()); role != authz.RoleAdmin {
		t.Fatalf("otro admin de Engram = %s, want admin (compatibilidad)", role)
	}
	env.open()
	if n := env.count(`SELECT COUNT(*) FROM nexo_role_bindings WHERE role = 'superadmin'`); n != 1 {
		t.Fatalf("idempotente: superadmins = %d", n)
	}
}

func TestPGBootstrapSuperadminLazily(t *testing.T) {
	env := newPGEnv(t)
	// `engram cloud bootstrap admin` después de arrancar el servidor.
	carla, err := env.cs.CreateFirstAdminHumanUser(context.Background(), cloudstore.CreateHumanUserParams{Username: "carla", Email: "carla@rp.pe"})
	if err != nil {
		t.Fatal(err)
	}
	id := env.id(carla.PrincipalID, authz.KindHuman)
	d := env.can(id, authz.PlatformScope(), authz.CapPlatformAdminsManage)
	if !d.Allowed || d.Role != authz.RoleSuperadmin {
		t.Fatalf("CA-R01: el usuario de bootstrap es superadmin: %+v", d)
	}
	if n := env.count(`SELECT COUNT(*) FROM nexo_role_bindings WHERE principal_id = $1::bigint AND role = 'superadmin'`, carla.PrincipalID); n != 1 {
		t.Fatal("el arranque en caliente persiste la asignación")
	}
	pedro := env.principal("pedro", authz.KindHuman, "member")
	if _, err := env.svc.SetPlatformRole(env.ctx(), id, pedro, authz.RoleAdmin); err != nil {
		t.Fatalf("CA-R01: la superadmin crea a Pedro como admin: %v", err)
	}
	if d := env.can(env.id(pedro, authz.KindHuman), authz.PlatformScope(), authz.CapPlatformUsersManage); !d.Allowed || d.Role != authz.RoleAdmin {
		t.Fatalf("Pedro admin: %+v", d)
	}
	var engramRole string
	_ = env.db.QueryRow(`SELECT role FROM cloud_principals WHERE id = $1::bigint`, pedro).Scan(&engramRole)
	if engramRole != "admin" {
		t.Fatalf("el rol de Engram acompaña al de plataforma: %s", engramRole)
	}
}

// ─── Reglas de plataforma (R-7, R-8, R-10, R-11, CA-R02, CA-R03, CA-R04) ─────

type platformCast struct {
	carla, pedro, sofia, luis, ci string
}

func (e *pgEnv) platformCast() platformCast {
	e.t.Helper()
	c := platformCast{
		carla: e.principal("carla", authz.KindHuman, "admin"),
		pedro: e.principal("pedro", authz.KindHuman, "admin"),
		sofia: e.principal("sofia", authz.KindHuman, "member"),
		luis:  e.principal("luis", authz.KindHuman, "member"),
		ci:    e.principal("ci-lineamientos", authz.KindServiceAccount, "member"),
	}
	if _, err := e.svc.SetPlatformRole(e.ctx(), e.id(c.carla, authz.KindHuman), c.sofia, authz.RoleAuditor); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func TestPGPlatformRules(t *testing.T) {
	env := newPGEnv(t)
	c := env.platformCast()
	ctx := env.ctx()
	carla, pedro, sofia := env.id(c.carla, authz.KindHuman), env.id(c.pedro, authz.KindHuman), env.id(c.sofia, authz.KindHuman)

	// R-7 / CA-R02: un admin no crea ni promueve admins.
	_, err := env.svc.SetPlatformRole(ctx, pedro, c.luis, authz.RoleAdmin)
	if de := wantDenied(t, err, false); de.Decision.Reason != authz.ReasonCapability {
		t.Fatalf("%+v", de.Decision)
	}
	// R-11: nadie se sube a sí mismo.
	_, err = env.svc.SetPlatformRole(ctx, pedro, c.pedro, authz.RoleSuperadmin)
	wantDenied(t, err, false) // ni siquiera tiene admins.manage
	// R-10: una cuenta de servicio solo es miembro.
	_, err = env.svc.SetPlatformRole(ctx, carla, c.ci, authz.RoleAuditor)
	wantRule(t, err, authz.CodeServiceAccountRole)
	// CA-R04: la auditora lee la auditoría y las asignaciones; no escribe.
	if d := env.can(sofia, authz.PlatformScope(), authz.CapPlatformAuditRead); !d.Allowed {
		t.Fatal("auditor lee la auditoría")
	}
	members, _, err := env.svc.ListPlatformMembers(ctx, sofia, authz.MemberQuery{})
	if err != nil || len(members) != 5 {
		t.Fatalf("auditor lista asignaciones: %d %v", len(members), err)
	}
	roles := map[string]authz.Role{}
	for _, m := range members {
		roles[m.Username] = m.Role
	}
	if roles["carla"] != authz.RoleSuperadmin || roles["pedro"] != authz.RoleAdmin || roles["sofia"] != authz.RoleAuditor || roles["luis"] != authz.RoleMiembro {
		t.Fatalf("roles de plataforma: %v", roles)
	}
	if _, err := env.svc.SetPlatformRole(ctx, sofia, c.luis, authz.RoleAuditor); err == nil {
		t.Fatal("CA-R04: escritura del auditor = 403")
	}
	if _, err := env.svc.CreateScope(ctx, sofia, authz.CreateScopeInput{Type: authz.ScopeSpace, Key: "x-space", Owner: c.luis}); err == nil {
		t.Fatal("CA-R04: el auditor no crea ámbitos")
	}
	if _, _, err := env.svc.ListEvents(ctx, sofia, authz.EventQuery{}); err != nil {
		t.Fatalf("auditor lee eventos: %v", err)
	}
	if _, _, err := env.svc.ListEvents(ctx, env.id(c.luis, authz.KindHuman), authz.EventQuery{}); err == nil {
		t.Fatal("un miembro no lee eventos")
	}

	// R-8 / CA-R03: no se degrada ni deshabilita al último superadmin.
	_, err = env.svc.SetPlatformRole(ctx, carla, c.carla, authz.RoleAdmin)
	wantRule(t, err, authz.CodeLastSuperadmin)
	if _, err := env.db.Exec(`DELETE FROM nexo_role_bindings WHERE principal_id = $1::bigint AND scope_type = 'platform'`, c.carla); err == nil || !strings.Contains(err.Error(), "nexo_last_superadmin") {
		t.Fatalf("trigger de bindings: %v", err)
	}
	if err := env.cs.SetHumanUserEnabled(context.Background(), c.carla, false); err == nil || !strings.Contains(err.Error(), "nexo_last_superadmin") {
		t.Fatalf("Engram no puede deshabilitar al último superadmin: %v", err)
	}
	if _, err := env.db.Exec(`DELETE FROM cloud_principals WHERE id = $1::bigint`, c.carla); err == nil {
		t.Fatal("no se borra al último superadmin")
	}
	// Con un segundo superadmin sí.
	if _, err := env.svc.SetPlatformRole(ctx, carla, c.pedro, authz.RoleSuperadmin); err != nil {
		t.Fatal(err)
	}
	if err := env.cs.SetHumanUserEnabled(context.Background(), c.pedro, false); err != nil {
		t.Fatalf("hay otro superadmin: %v", err)
	}
	// Pedro deshabilitado: Carla vuelve a ser la última habilitada.
	_, err = env.svc.SetPlatformRole(ctx, carla, c.carla, authz.RoleMiembro)
	wantRule(t, err, authz.CodeLastSuperadmin)
	if err := env.cs.SetHumanUserEnabled(context.Background(), c.pedro, true); err != nil {
		t.Fatal(err)
	}
	change, err := env.svc.SetPlatformRole(ctx, carla, c.carla, authz.RoleAdmin)
	if err != nil || !change.Changed || change.RoleBefore != authz.RoleSuperadmin {
		t.Fatalf("auto-degradación con otro superadmin: %+v %v", change, err)
	}
	// R-1: repetir la misma asignación no cambia nada ni genera evento.
	before := env.count(`SELECT COUNT(*) FROM nexo_authz_events`)
	change, err = env.svc.SetPlatformRole(ctx, env.id(c.pedro, authz.KindHuman), c.sofia, authz.RoleAuditor)
	if err != nil || change.Changed {
		t.Fatalf("sin cambios: %+v %v", change, err)
	}
	if env.count(`SELECT COUNT(*) FROM nexo_authz_events`) != before {
		t.Fatal("sin cambios no hay evento")
	}
	// Quitar el rol de plataforma (DELETE) deja miembro también en Engram.
	if _, err := env.svc.SetPlatformRole(ctx, env.id(c.pedro, authz.KindHuman), c.sofia, authz.RoleNone); err != nil {
		t.Fatal(err)
	}
	if role, _ := env.eng.EffectiveRole(context.Background(), sofia, authz.PlatformScope()); role != authz.RoleMiembro {
		t.Fatalf("sofia = %s", role)
	}
}

// ─── Ámbitos (R-3, R-4, R-5, R-6, R-9, R-10, R-11, CA-R05, CA-R06, CA-R08, CA-R09) ─

func TestPGScopesMembersAndVisibility(t *testing.T) {
	env := newPGEnv(t)
	c := env.platformCast()
	ana := env.principal("ana", authz.KindHuman, "member")
	marta := env.principal("marta", authz.KindHuman, "member")
	ctx := env.ctx()
	carla, pedro := env.id(c.carla, authz.KindHuman), env.id(c.pedro, authz.KindHuman)
	anaID, luisID, ciID := env.id(ana, authz.KindHuman), env.id(c.luis, authz.KindHuman), env.id(c.ci, authz.KindServiceAccount)

	// CA-R05: Pedro crea el espacio con Ana responsable (abierto por defecto, R-5).
	info, err := env.svc.CreateScope(ctx, pedro, authz.CreateScopeInput{Type: authz.ScopeSpace, Key: "Lineamientos-Frontend", Owner: "ana"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Scope.Key != "lineamientos-frontend" || info.Visibility != authz.VisibilityOpen || info.DefaultRole != authz.RoleLector {
		t.Fatalf("espacio: %+v", info)
	}
	if _, err := env.svc.CreateScope(ctx, pedro, authz.CreateScopeInput{Type: authz.ScopeSpace, Key: "lineamientos-frontend", Owner: "ana"}); err == nil {
		t.Fatal("duplicado")
	} else {
		wantRule(t, err, authz.CodeScopeExists)
	}
	// Proyecto restringido por defecto (R-5); sin responsable no se crea (R-9).
	if _, err := env.svc.CreateScope(ctx, pedro, authz.CreateScopeInput{Type: authz.ScopeProject, Key: "ms-factura"}); err == nil {
		t.Fatal("sin responsable")
	} else {
		wantRule(t, err, authz.CodeInvalid)
	}
	// R-11: el admin no se nombra a sí mismo; R-10: la CI no es responsable.
	_, err = env.svc.CreateScope(ctx, pedro, authz.CreateScopeInput{Type: authz.ScopeProject, Key: "ms-factura", Owner: c.pedro})
	wantRule(t, err, authz.CodeSelfPromotion)
	_, err = env.svc.CreateScope(ctx, pedro, authz.CreateScopeInput{Type: authz.ScopeProject, Key: "ms-factura", Owner: c.ci})
	wantRule(t, err, authz.CodeServiceAccountRole)
	proj, err := env.svc.CreateScope(ctx, pedro, authz.CreateScopeInput{Type: authz.ScopeProject, Key: "MS Factura", Owner: ana})
	if err != nil || proj.Visibility != authz.VisibilityRestricted || proj.Scope.Key != cloudstore.NormalizeProjectGrant("MS Factura") {
		t.Fatalf("proyecto: %+v %v", proj, err)
	}
	space := authz.SpaceScope("lineamientos-frontend")
	// CA-R05: Ana agrega a la CI como publicadora; la CI no puede ser responsable.
	if _, err := env.svc.SetScopeMember(ctx, anaID, space, c.ci, authz.RolePublicador); err != nil {
		t.Fatal(err)
	}
	_, err = env.svc.SetScopeMember(ctx, anaID, space, c.ci, authz.RoleResponsable)
	wantRule(t, err, authz.CodeServiceAccountRole)
	if d := env.can(ciID, space, authz.CapSpacePublish); !d.Allowed {
		t.Fatalf("CI publica: %+v", d)
	}
	// CA-R06: Luis lee el espacio abierto sin asignación; con proponente propone.
	if d := env.can(luisID, space, authz.CapSpaceRead); !d.Allowed || d.Role != authz.RoleLector {
		t.Fatalf("Luis lector implícito: %+v", d)
	}
	if d := env.can(luisID, space, authz.CapSpacePropose); d.Allowed {
		t.Fatal("lector no propone")
	}
	prop := authz.RoleProponente
	if _, err := env.svc.UpdateScope(ctx, anaID, space, authz.UpdateScopeInput{DefaultRole: &prop}); err != nil {
		t.Fatal(err)
	}
	if d := env.can(luisID, space, authz.CapSpacePropose); !d.Allowed {
		t.Fatalf("CA-R06: Luis propone: %+v", d)
	}
	bad := authz.RolePublicador
	_, err = env.svc.UpdateScope(ctx, anaID, space, authz.UpdateScopeInput{DefaultRole: &bad})
	wantRule(t, err, authz.CodeInvalid) // R-3: nunca publicador implícito
	_, err = env.svc.UpdateScope(ctx, luisID, space, authz.UpdateScopeInput{DefaultRole: &prop})
	wantDenied(t, err, false)

	// Espacio restringido de seguridad con Marta publicadora.
	if _, err := env.svc.CreateScope(ctx, carla, authz.CreateScopeInput{Type: authz.ScopeSpace, Key: "seguridad", Visibility: authz.VisibilityRestricted, Owner: marta}); err != nil {
		t.Fatal(err)
	}
	sec := authz.SpaceScope("seguridad")
	// CA-R09 / R-6: ser admin (o superadmin) no da acceso a un espacio restringido (404).
	for _, who := range []authz.Identity{pedro, carla, luisID} {
		if d := env.can(who, sec, authz.CapSpaceRead); d.Allowed || !d.Hidden {
			t.Fatalf("CA-R09: %s ve seguridad: %+v", who.PrincipalID, d)
		}
	}
	_, err = env.svc.ListScopeMembers(ctx, luisID, sec, authz.MemberQuery{})
	wantDenied(t, err, true)
	// El auditor y el admin listan sus miembros (RF-06) aunque no lo vean.
	view, err := env.svc.ListScopeMembers(ctx, pedro, sec, authz.MemberQuery{})
	if err != nil || len(view.Members) != 1 || view.Members[0].Role != authz.RoleResponsable {
		t.Fatalf("miembros de seguridad: %+v %v", view, err)
	}
	// R-9: no se quita al último responsable.
	_, err = env.svc.SetScopeMember(ctx, env.id(marta, authz.KindHuman), sec, marta, authz.RoleLector)
	wantRule(t, err, authz.CodeLastOwner)
	// R-11: Ana (responsable) se cambia a lectora: permitido (baja); Luis no se sube.
	if _, err := env.svc.SetScopeMember(ctx, env.id(marta, authz.KindHuman), sec, ana, authz.RoleResponsable); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.SetScopeMember(ctx, env.id(marta, authz.KindHuman), sec, marta, authz.RolePublicador); err != nil {
		t.Fatalf("hay otro responsable: %v", err)
	}
	// R-1: un rol por persona y ámbito; cambiar reemplaza.
	if n := env.count(`SELECT COUNT(*) FROM nexo_role_bindings WHERE principal_id = $1::bigint AND scope_type = 'space' AND scope_key = 'seguridad'`, marta); n != 1 {
		t.Fatalf("filas de marta = %d", n)
	}
	// R-9 recuperación: Ana deshabilitada deja el espacio sin responsable habilitado.
	env.exec(`UPDATE cloud_principals SET enabled = FALSE WHERE id = $1::bigint`, ana)
	if d := env.can(anaID, sec, authz.CapSpaceRead); d.Allowed || d.Reason != authz.ReasonDisabled {
		t.Fatalf("R-13: deshabilitada pierde acceso en la siguiente petición: %+v", d)
	}
	_, err = env.svc.RecoverScope(ctx, pedro, sec, marta)
	wantDenied(t, err, false) // solo superadmin
	_, err = env.svc.RecoverScope(ctx, carla, sec, c.carla)
	wantRule(t, err, authz.CodeSelfPromotion)
	rec, err := env.svc.RecoverScope(ctx, carla, sec, marta)
	if err != nil || rec.RoleBefore != authz.RolePublicador {
		t.Fatalf("recuperación: %+v %v", rec, err)
	}
	_, err = env.svc.RecoverScope(ctx, carla, sec, marta)
	wantRule(t, err, authz.CodeScopeHasOwner)
	env.exec(`UPDATE cloud_principals SET enabled = TRUE WHERE id = $1::bigint`, ana)

	// CA-R08: un token read de Ana (responsable) no asigna roles ni publica.
	readAna := anaID
	readAna.TokenScope = authz.TokenScopeRead
	if d := env.can(readAna, space, authz.CapSpacePublish); d.Allowed || d.Reason != authz.ReasonTokenScope {
		t.Fatalf("CA-R08 publicar: %+v", d)
	}
	_, err = env.svc.SetScopeMember(ctx, readAna, space, c.luis, authz.RolePublicador)
	if de := wantDenied(t, err, false); de.Decision.Reason != authz.ReasonTokenScope {
		t.Fatalf("CA-R08 asignar: %+v", de.Decision)
	}
	if d := env.can(readAna, space, authz.CapSpaceRead); !d.Allowed {
		t.Fatal("el token read sí lee")
	}

	// Archivar: el espacio desaparece de los visibles y bloquea escrituras.
	if _, err := env.svc.SetScopeArchived(ctx, anaID, space, true); err == nil {
		t.Fatal("solo plataforma archiva")
	}
	if _, err := env.svc.SetScopeArchived(ctx, pedro, space, true); err != nil {
		t.Fatal(err)
	}
	if d := env.can(ciID, space, authz.CapSpacePublish); d.Allowed || d.Reason != authz.ReasonArchived {
		t.Fatalf("archivado: %+v", d)
	}
	if d := env.can(luisID, space, authz.CapSpaceRead); d.Allowed || !d.Hidden {
		t.Fatalf("archivado y sin asignación: oculto: %+v", d)
	}
	vis, _ := env.eng.VisibleScopes(context.Background(), ciID, authz.ScopeSpace)
	if len(vis) != 0 {
		t.Fatalf("archivado no visible: %+v", vis)
	}
	if _, err := env.svc.SetScopeArchived(ctx, pedro, space, false); err != nil {
		t.Fatal(err)
	}
}

func TestPGProjectCompatibilityAndVisibleScopes(t *testing.T) {
	env := newPGEnv(t)
	c := env.platformCast()
	ana := env.principal("ana", authz.KindHuman, "member")
	marta := env.principal("marta", authz.KindHuman, "member")
	ctx := env.ctx()
	luisID, martaID := env.id(c.luis, authz.KindHuman), env.id(marta, authz.KindHuman)
	// Permisos de Engram previos (requisitos §9): publicador sin fila Nexo.
	for _, p := range []string{c.luis, marta} {
		if _, err := env.cs.CreateProjectGrant(context.Background(), cloudstore.CreateProjectGrantParams{PrincipalID: p, Project: "MS-Factura"}); err != nil {
			t.Fatal(err)
		}
	}
	project := authz.ProjectScope("MS-Factura")
	if d := env.can(luisID, project, authz.CapProjectWriteAny); !d.Allowed || d.Role != authz.RolePublicador {
		t.Fatalf("compatibilidad: %+v", d)
	}
	if d := env.can(env.id(ana, authz.KindHuman), project, authz.CapProjectRead); d.Allowed || !d.Hidden {
		t.Fatalf("proyecto restringido sin rol: %+v", d)
	}
	if d := env.can(env.id(c.pedro, authz.KindHuman), project, authz.CapProjectRead); d.Allowed {
		t.Fatal("R-6: un admin sin permiso no lee el proyecto")
	}
	// El responsable configura el proyecto: Marta lectora (CA-R07 a nivel motor).
	if _, err := env.svc.CreateScope(ctx, env.id(c.pedro, authz.KindHuman), authz.CreateScopeInput{Type: authz.ScopeProject, Key: "ms-factura", Owner: ana}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.SetScopeMember(ctx, env.id(ana, authz.KindHuman), project, marta, authz.RoleLector); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.SetScopeMember(ctx, env.id(ana, authz.KindHuman), project, c.luis, authz.RoleProponente); err != nil {
		t.Fatal(err)
	}
	if d := env.can(martaID, project, authz.CapProjectRead); !d.Allowed {
		t.Fatal("Marta descarga")
	}
	if d := env.can(martaID, project, authz.CapProjectWriteOwn); d.Allowed {
		t.Fatal("CA-R07: la subida de Marta se rechaza")
	}
	if d := env.can(luisID, project, authz.CapProjectWriteOwn); !d.Allowed {
		t.Fatal("CA-R07: Luis sube")
	}
	view, err := env.svc.ListScopeMembers(ctx, env.id(ana, authz.KindHuman), project, authz.MemberQuery{})
	if err != nil || len(view.Members) != 3 {
		t.Fatalf("miembros: %+v %v", view.Members, err)
	}
	// Proyecto de Engram sin fila: aparece en VisibleScopes por el permiso.
	if _, err := env.cs.CreateProjectGrant(context.Background(), cloudstore.CreateProjectGrantParams{PrincipalID: c.luis, Project: "legacy-app"}); err != nil {
		t.Fatal(err)
	}
	env.exec(`INSERT INTO nexo_scopes (scope_type, scope_key, visibility, status) VALUES ('project', 'abierto', 'open', 'active'), ('project', 'archivado', 'open', 'archived'), ('space', 'general', 'open', 'active')`)
	vis, err := env.eng.VisibleScopes(context.Background(), luisID, authz.ScopeProject)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]authz.Role{}
	for _, sr := range vis {
		got[sr.Scope.Key] = sr.Role
	}
	want := map[string]authz.Role{"ms-factura": authz.RoleProponente, "legacy-app": authz.RolePublicador, "abierto": authz.RoleLector}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("visibles de Luis = %v, want %v", got, want)
	}
	me, err := env.svc.Me(ctx, luisID)
	if err != nil || me.Platform.Role != authz.RoleMiembro || len(me.Projects) != 3 || len(me.Spaces) != 1 || me.TokenScope != "write" {
		t.Fatalf("me: %+v %v", me, err)
	}
}

// ─── Auditoría (R-14, CA-R10) ────────────────────────────────────────────────

func TestPGEventsCarryActorTargetBeforeAfter(t *testing.T) {
	env := newPGEnv(t)
	c := env.platformCast()
	ana := env.principal("ana", authz.KindHuman, "member")
	ctx := env.ctx()
	pedro := env.id(c.pedro, authz.KindHuman)
	if _, err := env.svc.CreateScope(ctx, pedro, authz.CreateScopeInput{Type: authz.ScopeSpace, Key: "general", Owner: ana}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.SetScopeMember(ctx, env.id(ana, authz.KindHuman), authz.SpaceScope("general"), c.luis, authz.RolePublicador); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.SetScopeMember(ctx, env.id(ana, authz.KindHuman), authz.SpaceScope("general"), c.luis, authz.RoleLector); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.SetScopeMember(ctx, env.id(ana, authz.KindHuman), authz.SpaceScope("general"), c.luis, authz.RoleNone); err != nil {
		t.Fatal(err)
	}
	_, _ = env.svc.SetPlatformRole(ctx, pedro, c.luis, authz.RoleAdmin) // denegado
	items, next, err := env.svc.ListEvents(ctx, env.id(c.sofia, authz.KindHuman), authz.EventQuery{ScopeType: "space", Limit: 200})
	if err != nil || next != "" {
		t.Fatal(err)
	}
	var actions []string
	for _, it := range items {
		actions = append(actions, it.Action)
	}
	sort.Strings(actions)
	want := []string{"binding.changed", "binding.granted", "binding.granted", "binding.revoked", "scope.created"}
	if !slices.Equal(actions, want) {
		t.Fatalf("acciones = %v", actions)
	}
	changed := items[1] // orden id DESC: revoked, changed, granted(luis), granted(ana), created
	if changed.Action != "binding.changed" || changed.RoleBefore != "publicador" || changed.RoleAfter != "lector" ||
		changed.TargetPrincipalID != c.luis || changed.TargetUsername != "luis" || changed.ActorPrincipalID != ana ||
		changed.Actor != "p"+ana || changed.ActorEmail != "ana@rp.pe" || !changed.ActorVerified || changed.ActorTokenID != env.tokens[ana] ||
		changed.ClientIP != "10.1.1.1" || changed.UserAgent != "nexo-test/1.0" || changed.ClientClaim != "PC-"+ana {
		t.Fatalf("CA-R10: evento incompleto: %+v", changed)
	}
	denied, _, err := env.svc.ListEvents(ctx, env.id(c.sofia, authz.KindHuman), authz.EventQuery{Action: "authz.denied", Principal: "pedro"})
	if err != nil || len(denied) == 0 || denied[0].Details["capability"] != "platform.admins.manage" || denied[0].RoleBefore != "admin" {
		t.Fatalf("CA-R10: denegación auditada: %+v %v", denied, err)
	}
	// Paginación por cursor y filtros.
	page1, cur, err := env.svc.ListEvents(ctx, env.id(c.sofia, authz.KindHuman), authz.EventQuery{Action: "binding.*", Limit: 2})
	if err != nil || len(page1) != 2 || cur == "" {
		t.Fatalf("página 1: %v %q %v", page1, cur, err)
	}
	page2, _, err := env.svc.ListEvents(ctx, env.id(c.sofia, authz.KindHuman), authz.EventQuery{Action: "binding.*", Limit: 2, Cursor: cur})
	if err != nil || len(page2) == 0 {
		t.Fatalf("página 2: %v %v", page2, err)
	}
	for _, a := range page1 {
		for _, b := range page2 {
			if a.ID == b.ID {
				t.Fatalf("páginas solapadas: %s", a.ID)
			}
		}
	}
	if _, _, err := env.svc.ListEvents(ctx, env.id(c.sofia, authz.KindHuman), authz.EventQuery{Cursor: "x"}); !errors.Is(err, authz.ErrInvalidCursor) {
		t.Fatalf("cursor inválido: %v", err)
	}
}

// ─── Rendimiento (RNF-R1, RNF-R2) ────────────────────────────────────────────

// TestPGAuthorizationCostWith100kBindings siembra la escala de diseño (2 000
// personas, 5 000 ámbitos, 100 000 asignaciones) y mide Can y VisibleScopes.
func TestPGAuthorizationCostWith100kBindings(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	env := newPGEnv(t)
	start := time.Now()
	env.exec(`INSERT INTO cloud_principals (kind, display_name, role) SELECT 'human', 'u' || g, 'member' FROM generate_series(1, 2000) g`)
	env.exec(`INSERT INTO nexo_scopes (scope_type, scope_key, visibility, status)
		SELECT CASE WHEN g % 2 = 0 THEN 'project' ELSE 'space' END, 's' || g, CASE WHEN g % 10 = 0 THEN 'open' ELSE 'restricted' END, 'active'
		FROM generate_series(1, 5000) g`)
	// La siembra masiva no necesita la validación fila a fila (los datos son
	// válidos por construcción); el trigger se reactiva antes de medir.
	env.exec(`ALTER TABLE nexo_role_bindings DISABLE TRIGGER nexo_role_bindings_validate`)
	env.exec(`INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role)
		SELECT p.id, CASE WHEN s % 2 = 0 THEN 'project' ELSE 'space' END, 's' || s,
		       (ARRAY['lector','proponente','publicador','responsable'])[1 + (p.id + s) % 4]
		FROM (SELECT id FROM cloud_principals ORDER BY id LIMIT 2000) p
		CROSS JOIN LATERAL (SELECT 1 + ((p.id * 97 + k * 101) % 5000) AS s FROM generate_series(1, 50) k) x
		ON CONFLICT DO NOTHING`)
	env.exec(`ALTER TABLE nexo_role_bindings ENABLE TRIGGER nexo_role_bindings_validate`)
	env.exec(`ANALYZE cloud_principals`)
	env.exec(`ANALYZE nexo_role_bindings`)
	env.exec(`ANALYZE nexo_scopes`)
	n := env.count(`SELECT COUNT(*) FROM nexo_role_bindings`)
	t.Logf("siembra: %d asignaciones en %s", n, time.Since(start).Round(time.Millisecond))
	if n < 95000 {
		t.Fatalf("asignaciones = %d", n)
	}
	var ids []string
	rows, err := env.db.Query(`SELECT id::text FROM cloud_principals ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	measure := func(name string, iters int, fn func(i int)) time.Duration {
		for i := 0; i < 20; i++ {
			fn(i)
		}
		ds := make([]time.Duration, iters)
		for i := 0; i < iters; i++ {
			s := time.Now()
			fn(i)
			ds[i] = time.Since(s)
		}
		slices.Sort(ds)
		med, p95 := ds[iters/2], ds[iters*95/100]
		t.Logf("%s: mediana %s, p95 %s (%d llamadas)", name, med, p95, iters)
		return med
	}
	ctx := context.Background()
	medCan := measure("Can (ámbito)", 1000, func(i int) {
		id := env.id(ids[(i*37)%len(ids)], authz.KindHuman)
		s := authz.Scope{Type: authz.ScopeSpace, Key: fmt.Sprintf("s%d", 1+2*((i*53)%2500))}
		if _, err := env.eng.Can(ctx, id, s, authz.CapSpaceRead); err != nil {
			t.Fatal(err)
		}
	})
	medPlat := measure("Can (plataforma)", 1000, func(i int) {
		if _, err := env.eng.Can(ctx, env.id(ids[(i*41)%len(ids)], authz.KindHuman), authz.PlatformScope(), authz.CapPlatformAuditRead); err != nil {
			t.Fatal(err)
		}
	})
	medVis := measure("VisibleScopes (espacios)", 300, func(i int) {
		if _, err := env.eng.VisibleScopes(ctx, env.id(ids[(i*43)%len(ids)], authz.KindHuman), authz.ScopeSpace); err != nil {
			t.Fatal(err)
		}
	})
	if medCan > 2*time.Millisecond || medPlat > 2*time.Millisecond {
		t.Fatalf("RNF-R1: autorización por petición %s / %s, objetivo < 2 ms", medCan, medPlat)
	}
	if medVis > 20*time.Millisecond {
		t.Fatalf("VisibleScopes %s", medVis)
	}
}
