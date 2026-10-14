package cloudstore

// NX-07: interruptor NEXO_SERVICE_ACCOUNTS_ENABLED en el almacén. Postgres real.

import (
	"bytes"
	"errors"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/features"
)

func nexoCaptureStdLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func nexoSeedServiceAccount(t *testing.T, cs *CloudStore, name string) string {
	t.Helper()
	var id string
	if err := cs.db.QueryRowContext(t.Context(), `INSERT INTO cloud_principals (kind, display_name, role) VALUES ('service_account', $1, 'member') RETURNING id::text`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// Apagado (el defecto), un token de cuenta de servicio ya no se emite ni autentica; el de una persona sí.
func TestNexoServiceAccountTokensClosedWhenSwitchOffPG(t *testing.T) {
	t.Setenv(features.ServiceAccountsEnv, "true") // se siembra con el interruptor encendido
	cs := openIsolatedCloudStore(t)
	sa := nexoSeedServiceAccount(t, cs, "ci-secreto")
	ana := nexoSeedUser(t, cs, "ana", "ana@rp.pe", "laptop")
	if _, err := cs.db.ExecContext(t.Context(), `INSERT INTO cloud_principal_tokens (principal_id, token_prefix, token_hash, name) VALUES ($1::bigint, 'egc_live_sa', 'hash-sa', 'ci')`, sa); err != nil {
		t.Fatal(err)
	}
	if _, principal, err := cs.FindPrincipalTokenByHash(t.Context(), "hash-sa"); err != nil || !principal.Enabled {
		t.Fatalf("con el interruptor encendido el token de la cuenta autentica: %+v %v", principal, err)
	}
	t.Setenv(features.ServiceAccountsEnv, "false")
	if _, principal, err := cs.FindPrincipalTokenByHash(t.Context(), "hash-sa"); err != nil || principal.Enabled {
		t.Fatalf("apagado, el principal de la cuenta debe volver deshabilitado (falla cerrado): %+v %v", principal, err)
	}
	if _, principal, err := cs.FindPrincipalTokenByHash(t.Context(), "hash-"+"ana-laptop"); err != nil || !principal.Enabled {
		t.Fatalf("una persona sigue autenticando: %+v %v", principal, err)
	}
	// Respaldo del almacén: ninguna emisión para una cuenta de servicio, ni con el helper común.
	_, err := cs.CreatePrincipalTokenWithAudit(t.Context(), CreatePrincipalTokenParams{PrincipalID: sa, TokenPrefix: "egc_live_x", TokenHash: "h2", Name: "x"},
		AuthAuditEvent{ActorSource: "test", Action: "token.create", Outcome: "success"})
	if !errors.Is(err, ErrServiceAccountsDisabled) {
		t.Fatalf("la emisión para una cuenta de servicio debía rechazarse: %v", err)
	}
	if n := nexoCountRows(t, cs, "cloud_principal_tokens WHERE token_hash = 'h2'"); n != 0 {
		t.Fatalf("no debía crearse el token: %d", n)
	}
	if _, err := cs.CreatePrincipalTokenWithAudit(t.Context(), CreatePrincipalTokenParams{PrincipalID: ana.PrincipalID, TokenPrefix: "egc_live_y", TokenHash: "h3", Name: "y"},
		AuthAuditEvent{ActorSource: "test", Action: "token.create", Outcome: "success"}); err != nil {
		t.Fatalf("una persona sigue recibiendo tokens: %v", err)
	}
}

// El arranque avisa cuántas cuentas habilitadas hay, sin nombres ni tokens.
func TestNexoServiceAccountsStartupWarningPG(t *testing.T) {
	t.Setenv(features.ServiceAccountsEnv, "true")
	cs := openIsolatedCloudStore(t)
	nexoSeedServiceAccount(t, cs, "ci-nombre-secreto")
	nexoSeedServiceAccount(t, cs, "ci-otro-secreto")
	buf := nexoCaptureStdLog(t)

	t.Setenv(features.ServiceAccountsEnv, "false")
	nexoReportServiceAccounts(t.Context(), cs.db)
	out := buf.String()
	if !strings.Contains(out, "ADVERTENCIA") || !strings.Contains(out, "2 cuenta(s) de servicio habilitada(s)") || strings.Contains(out, "secreto") || !strings.Contains(out, features.ServiceAccountsEnv) {
		t.Fatalf("aviso de arranque: %q", out)
	}
	buf.Reset()
	t.Setenv(features.ServiceAccountsEnv, "true")
	nexoReportServiceAccounts(t.Context(), cs.db)
	if strings.Contains(buf.String(), "ADVERTENCIA") || !strings.Contains(buf.String(), "activas") {
		t.Fatalf("encendido no debe advertir de tokens que no autentican: %q", buf.String())
	}
}

// Un valor inválido impide abrir el almacén (arrancar) y el error no repite el valor.
func TestNexoServiceAccountsEnvInvalidFailsToStartPG(t *testing.T) {
	for _, bad := range []string{"quizas", "2", "valor-secreto-xyz"} {
		t.Setenv(features.ServiceAccountsEnv, bad)
		_, err := New(cloud.Config{DSN: "postgres://nadie:clave-xyz@127.0.0.1:1/x?sslmode=disable"})
		if err == nil || !strings.Contains(err.Error(), features.ServiceAccountsEnv) || strings.Contains(err.Error(), bad) || strings.Contains(err.Error(), "clave-xyz") {
			t.Fatalf("%q: debía impedir arrancar nombrando la variable sin mostrar el valor: %v", bad, err)
		}
	}
}
