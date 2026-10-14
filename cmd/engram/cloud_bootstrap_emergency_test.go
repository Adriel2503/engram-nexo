package main

// SG-06: `engram cloud bootstrap emergency-token` contra Postgres real. Se
// niega mientras algún superadmin tenga un token vigente, se niega a quien no
// es superadmin, funciona cuando todos los tokens están revocados, deja
// auditoría y alerta, no cambia roles y el token entregado es válido por 24 h.
// La prueba nunca imprime tokens.

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

func TestParseCloudBootstrapEmergencyArgs(t *testing.T) {
	got, err := parseCloudBootstrapEmergencyArgs([]string{"--user", "beto", "--name", "vidrio", "--reason", "todos vencidos"})
	if err != nil || got.user != "beto" || got.name != "vidrio" || got.reason != "todos vencidos" {
		t.Fatalf("args: %+v %v", got, err)
	}
	for _, bad := range [][]string{{}, {"--user"}, {"--user", "--name", "x"}, {"--user", "a", "--zzz"}, {"--name", "x"}} {
		if _, err := parseCloudBootstrapEmergencyArgs(bad); err == nil {
			t.Errorf("%v debía fallar", bad)
		}
	}
}

func TestCloudBootstrapEmergencyToken(t *testing.T) {
	dsn := openIsolatedCloudRuntimeSchema(t)
	stubExitWithPanic(t)
	old := newCloudBootstrapStore
	newCloudBootstrapStore = func(cfg cloud.Config) (cloudBootstrapStore, error) {
		cfg.DSN = dsn
		return cloudstore.New(cfg)
	}
	t.Cleanup(func() { newCloudBootstrapStore = old })
	pepper := strings.Repeat("p", 40)
	t.Setenv("ENGRAM_CLOUD_TOKEN_PEPPER", pepper)

	run := func(args ...string) (stdout, stderr string, recovered any) {
		withArgs(t, append([]string{"engram", "cloud", "bootstrap"}, args...)...)
		return captureOutputAndRecover(t, cmdCloudBootstrap)
	}

	// Primer superadmin con token (la salida no se imprime).
	if _, errOut, rec := run("admin", "--username", "alicia", "--issue-token", "laptop"); rec != nil {
		t.Fatalf("bootstrap: %v %q", rec, errOut)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	// Segundo superadmin (beto) y un miembro (carlos), sin tokens.
	for _, p := range []struct{ name, role string }{{"beto", "superadmin"}, {"carlos", ""}} {
		var id string
		if err := db.QueryRowContext(context.Background(), `INSERT INTO cloud_principals (kind, display_name, role) VALUES ('human', $1, 'member') RETURNING id::text`, p.name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO cloud_human_users (principal_id, username, email) VALUES ($1::bigint, $2, $3)`, id, p.name, p.name+"@rp.pe")
		if p.role != "" {
			exec(`INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role) VALUES ($1::bigint, 'platform', '*', $2)`, id, p.role)
		}
	}

	// 1. Alicia tiene un token vigente: se niega (para cualquier superadmin).
	for _, user := range []string{"alicia", "beto"} {
		out, errOut, rec := run("emergency-token", "--user", user)
		if rec == nil || exitCodeOf(rec) != 1 {
			t.Fatalf("debía salir con 1: %v", rec)
		}
		if !strings.Contains(errOut, "superadmin_has_valid_token") || strings.Contains(out, "egc_") {
			t.Fatalf("negativa: stdout=%q stderr=%q", out, errOut)
		}
	}
	if n := count(`SELECT count(*) FROM cloud_principal_tokens WHERE name LIKE 'emergency-%'`); n != 0 {
		t.Fatalf("no debe haber tokens de emergencia: %d", n)
	}
	if n := count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = 'nexo.emergency_token' AND outcome = 'denied' AND reason_code = 'superadmin_has_valid_token'`); n != 2 {
		t.Fatalf("las negativas quedan en la auditoría: %d", n)
	}

	// 2. Todos los tokens revocados. Un miembro no puede recibir el acceso.
	exec(`UPDATE cloud_principal_tokens SET revoked_at = now()`)
	if _, errOut, rec := run("emergency-token", "--user", "carlos"); rec == nil || !strings.Contains(errOut, "not_superadmin") {
		t.Fatalf("miembro: rec=%v stderr=%q", rec, errOut)
	}
	if _, errOut, rec := run("emergency-token", "--user", "nadie"); rec == nil || !strings.Contains(errOut, "user_not_found") {
		t.Fatalf("inexistente: rec=%v stderr=%q", rec, errOut)
	}
	if _, _, rec := run("emergency-token"); rec == nil {
		t.Fatal("sin --user debe fallar")
	}

	// 3. Con todos revocados, beto recibe el token de emergencia.
	rolesBefore := count(`SELECT count(*) FROM nexo_role_bindings`)
	peopleBefore := count(`SELECT count(*) FROM cloud_principals`)
	out, errOut, rec := run("emergency-token", "--user", "beto", "--reason", "todos los tokens revocados")
	if rec != nil {
		t.Fatalf("emergency-token: %v stderr=%q", rec, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 4 || !strings.Contains(lines[0], "EMERGENCIA") || !strings.Contains(lines[0], "beto") || !strings.Contains(lines[1], "UNA SOLA VEZ") {
		t.Fatalf("salida inesperada (%d líneas)", len(lines))
	}
	raw := strings.TrimSpace(lines[2])
	if !strings.HasPrefix(raw, "egc_") {
		t.Fatalf("la tercera línea debe ser el token")
	}

	// El token funciona: resuelve a beto, escritura, 24 h.
	hasher, err := cloudauth.NewManagedTokenHasher([]byte(pepper))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash(raw)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := cloudstore.New(cloud.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	tok, principal, err := cs.FindPrincipalTokenByHash(context.Background(), hash)
	if err != nil {
		t.Fatalf("el token de emergencia debe resolver: %v", err)
	}
	if principal.DisplayName != "beto" || tok.NexoScope != "write" || tok.NexoExpiresAt == nil {
		t.Fatalf("token: principal=%s scope=%s expires=%v", principal.DisplayName, tok.NexoScope, tok.NexoExpiresAt)
	}
	if d := time.Until(*tok.NexoExpiresAt); d < 23*time.Hour+55*time.Minute || d > 24*time.Hour+time.Minute {
		t.Fatalf("vence en %s, want 24h", d)
	}

	// Sin cambios de roles ni de personas; auditoría y alerta.
	if n := count(`SELECT count(*) FROM nexo_role_bindings`); n != rolesBefore {
		t.Fatalf("roles %d -> %d", rolesBefore, n)
	}
	if n := count(`SELECT count(*) FROM cloud_principals`); n != peopleBefore {
		t.Fatalf("personas %d -> %d", peopleBefore, n)
	}
	if n := count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = 'nexo.emergency_token' AND outcome = 'success' AND metadata->>'reason' = 'todos los tokens revocados'`); n != 1 {
		t.Fatalf("auditoría del éxito: %d", n)
	}
	if n := count(`SELECT count(*) FROM nexo_alerts WHERE rule = 'emergency_access_used' AND severity = 'high'`); n != 1 {
		t.Fatalf("alerta alta: %d", n)
	}
	// El token en claro no está en ninguna fila de auditoría ni de alertas.
	if n := count(`SELECT count(*) FROM cloud_auth_audit_log WHERE metadata::text LIKE '%' || $1 || '%'`, raw); n != 0 {
		t.Fatalf("el token no debe estar en la auditoría")
	}
	if n := count(`SELECT count(*) FROM nexo_alerts WHERE summary LIKE '%' || $1 || '%' OR details::text LIKE '%' || $1 || '%'`, raw); n != 0 {
		t.Fatalf("el token no debe estar en las alertas")
	}

	// 4. Con el token de emergencia vigente, otro intento se niega.
	if _, errOut, rec := run("emergency-token", "--user", "alicia"); rec == nil || !strings.Contains(errOut, "superadmin_has_valid_token") {
		t.Fatalf("segundo intento: rec=%v stderr=%q", rec, errOut)
	}
}

// exitCodeOf lee el código de salida del pánico de stubExitWithPanic.
func exitCodeOf(rec any) int {
	if c, ok := rec.(exitCode); ok {
		return int(c)
	}
	return -1
}
