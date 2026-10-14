package main

// NX-05 (unificación de roles de plataforma): `engram cloud bootstrap admin` y
// `recover-token` contra Postgres real. El primer administrador queda como
// superadmin de Nexo (rol único), la columna de Engram se deriva, el comando lo
// dice con claridad y la auditoría lo registra; recuperar el token no cambia el
// rol. Ninguna salida de la prueba imprime tokens.

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

func TestCloudBootstrapAdminBecomesNexoSuperadmin(t *testing.T) {
	dsn := openIsolatedCloudRuntimeSchema(t)
	stubExitWithPanic(t)
	old := newCloudBootstrapStore
	newCloudBootstrapStore = func(cfg cloud.Config) (cloudBootstrapStore, error) {
		cfg.DSN = dsn
		return cloudstore.New(cfg)
	}
	t.Cleanup(func() { newCloudBootstrapStore = old })
	t.Setenv("ENGRAM_CLOUD_TOKEN_PEPPER", strings.Repeat("p", 40))

	withArgs(t, "engram", "cloud", "bootstrap", "admin", "--username", "alicia")
	stdout, stderr, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)
	if recovered != nil {
		t.Fatalf("bootstrap falló: %v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Rol de plataforma de Nexo: superadmin") || !strings.Contains(stdout, "Engram lo ve como admin") {
		t.Fatalf("la salida debe decir que quedó como superadmin: %q", stdout)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var nexoRole, engramRole string
	if err := db.QueryRowContext(context.Background(), `
		SELECT b.role, p.role FROM cloud_principals p
		JOIN cloud_human_users h ON h.principal_id = p.id
		JOIN nexo_role_bindings b ON b.principal_id = p.id AND b.scope_type = 'platform' AND b.scope_key = '*'
		WHERE h.username = 'alicia'`).Scan(&nexoRole, &engramRole); err != nil {
		t.Fatalf("el primer admin tiene su asignación de Nexo: %v", err)
	}
	if nexoRole != "superadmin" || engramRole != "admin" {
		t.Fatalf("rol de Nexo %q, columna de Engram %q", nexoRole, engramRole)
	}
	var events int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM nexo_authz_events WHERE action = 'binding.granted' AND role_after = 'superadmin' AND details->>'source' = 'bootstrap'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("evento binding.granted del bootstrap: %d %v", events, err)
	}
	var meta string
	if err := db.QueryRowContext(context.Background(), `SELECT metadata::text FROM cloud_auth_audit_log WHERE action = 'bootstrap.cli' ORDER BY id LIMIT 1`).Scan(&meta); err != nil || !strings.Contains(meta, `"platform_role": "superadmin"`) {
		t.Fatalf("la auditoría del bootstrap lleva el rol de plataforma: %s %v", meta, err)
	}

	// Un segundo bootstrap se rechaza y no cambia nada.
	withArgs(t, "engram", "cloud", "bootstrap", "admin", "--username", "otra")
	if _, _, recovered := captureOutputAndRecover(t, cmdCloudBootstrap); recovered == nil {
		t.Fatal("el segundo bootstrap debe rechazarse")
	}
	var supers int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM nexo_role_bindings WHERE role = 'superadmin'`).Scan(&supers); err != nil || supers != 1 {
		t.Fatalf("superadmins = %d %v", supers, err)
	}

	// recover-token entrega un token al admin sin token y no cambia su rol (la salida no se imprime).
	withArgs(t, "engram", "cloud", "bootstrap", "recover-token")
	if out, errOut, recovered := captureOutputAndRecover(t, cmdCloudBootstrap); recovered != nil || !strings.Contains(out, "SHOWN ONCE") {
		t.Fatalf("recover-token: recovered=%v stderr=%q", recovered, errOut)
	}
	if err := db.QueryRowContext(context.Background(), `
		SELECT b.role, p.role FROM cloud_principals p JOIN nexo_role_bindings b ON b.principal_id = p.id AND b.scope_type = 'platform'
		JOIN cloud_human_users h ON h.principal_id = p.id WHERE h.username = 'alicia'`).Scan(&nexoRole, &engramRole); err != nil || nexoRole != "superadmin" || engramRole != "admin" {
		t.Fatalf("recover-token no cambia el rol: %s %s %v", nexoRole, engramRole, err)
	}
}
