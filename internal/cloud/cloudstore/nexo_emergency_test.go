package cloudstore

// Nexo IA · SG-06 — acceso de emergencia (NexoEmergencyToken) contra
// Postgres real. Casos: con un superadmin con token vigente se niega; con
// todos vencidos o revocados funciona; un usuario que no es superadmin (o no
// existe, o está deshabilitado) se niega; queda en la auditoría y dispara la
// alerta alta; no crea personas ni cambia roles; dos intentos a la vez solo
// dejan pasar uno. Ninguna prueba imprime tokens.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
)

type emergencyEnv struct {
	t   *testing.T
	cs  *CloudStore
	db  *sql.DB
	ids map[string]string
	n   int
}

func newEmergencyEnv(t *testing.T) *emergencyEnv {
	t.Helper()
	nexoDBClearEnv(t)
	cs, err := New(cloud.Config{DSN: nexoDBSchemaDSN(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	e := &emergencyEnv{t: t, cs: cs, db: cs.NexoDB(), ids: map[string]string{}}
	return e
}

// person crea una persona con su rol de plataforma de Nexo (sin rol = miembro).
func (e *emergencyEnv) person(name, platformRole string) string {
	e.t.Helper()
	var id string
	if err := e.db.QueryRow(`INSERT INTO cloud_principals (kind, display_name, role) VALUES ('human', $1, 'member') RETURNING id::text`, name).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO cloud_human_users (principal_id, username, email) VALUES ($1::bigint, $2, $3)`, id, name, name+"@rp.pe")
	if platformRole != "" {
		e.exec(`INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role) VALUES ($1::bigint, 'platform', '*', $2)`, id, platformRole)
	}
	e.ids[name] = id
	return id
}

func (e *emergencyEnv) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *emergencyEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

// token agrega un token a la persona: scope, y caducidad como intervalo desde
// ahora ("" = revocado, ya vencido hace un día).
func (e *emergencyEnv) token(name, scope, expiresIn string, revoked bool) {
	e.t.Helper()
	e.n++
	e.exec(`INSERT INTO cloud_principal_tokens (principal_id, token_prefix, token_hash, name, nexo_scope, nexo_expires_at, revoked_at)
		VALUES ($1::bigint, $2, $3, $4, $5, now() + $6::interval, CASE WHEN $7 THEN now() ELSE NULL END)`,
		e.ids[name], fmt.Sprintf("egc_t_%d", e.n), fmt.Sprintf("hash-prueba-%s-%d", name, e.n), "t-"+name, scope, expiresIn, revoked)
}

func (e *emergencyEnv) issue(user string) (NexoEmergencyResult, error) {
	e.t.Helper()
	e.n++
	return e.cs.NexoEmergencyToken(context.Background(), NexoEmergencyTokenParams{
		User: user, TokenPrefix: fmt.Sprintf("egc_em_%d", e.n), TokenHash: fmt.Sprintf("hash-emergencia-%d", e.n), Reason: "prueba",
	}, AuthAuditEvent{ActorSource: "bootstrap_cli"})
}

func wantEmergencyCode(t *testing.T, err error, code string) {
	t.Helper()
	var ee *NexoEmergencyError
	if !errors.As(err, &ee) || ee.Code != code {
		t.Fatalf("error = %v; want código %s", err, code)
	}
}

func TestNexoEmergencyTokenRefusals(t *testing.T) {
	e := newEmergencyEnv(t)
	e.person("alicia", "superadmin")
	e.person("beto", "superadmin")
	e.person("carla", "admin")
	e.person("dino", "")
	e.person("eva", "superadmin")
	e.exec(`UPDATE cloud_principals SET enabled = FALSE WHERE id = $1::bigint`, e.ids["eva"])
	// Beto tiene un token vigente con escritura; los de Alicia están vencidos.
	e.token("alicia", "write", "-1 day", false)
	e.token("beto", "write", "30 days", false)

	// Con un superadmin con token vigente: se niega, aunque el destino sea otro.
	for _, user := range []string{"alicia", "beto"} {
		_, err := e.issue(user)
		wantEmergencyCode(t, err, EmergencyHasValidToken)
		if !strings.Contains(err.Error(), "beto") {
			t.Fatalf("el mensaje dice quién tiene el token vigente: %v", err)
		}
	}
	// Usuario inexistente, que no es superadmin y deshabilitado.
	_, err := e.issue("nadie")
	wantEmergencyCode(t, err, EmergencyUserNotFound)
	_, err = e.issue("")
	wantEmergencyCode(t, err, EmergencyUserNotFound)
	_, err = e.issue("carla") // admin, no superadmin
	wantEmergencyCode(t, err, EmergencyNotSuperadmin)
	_, err = e.issue("dino") // miembro
	wantEmergencyCode(t, err, EmergencyNotSuperadmin)
	_, err = e.issue("eva") // superadmin deshabilitada
	wantEmergencyCode(t, err, EmergencyUserDisabled)

	// Las negativas no dejan tokens ni alertas ni cambios de roles o personas.
	if n := e.count(`SELECT count(*) FROM cloud_principal_tokens WHERE name LIKE 'emergency-%'`); n != 0 {
		t.Fatalf("tokens de emergencia tras negativas: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM nexo_alerts WHERE rule = 'emergency_access_used'`); n != 0 {
		t.Fatalf("alertas tras negativas: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM cloud_principals`); n != 5 {
		t.Fatalf("personas: %d", n)
	}
}

func TestNexoEmergencyTokenAllExpiredOrRevoked(t *testing.T) {
	e := newEmergencyEnv(t)
	e.person("alicia", "superadmin")
	e.person("beto", "superadmin")
	e.person("carla", "admin")
	// Todos los tokens de superadmin vencidos o revocados; uno de solo lectura
	// vigente no cuenta (no puede emitir ni revocar); el de un admin vigente
	// tampoco; el de un superadmin deshabilitado tampoco.
	e.token("alicia", "write", "-2 days", false)
	e.token("beto", "write", "20 days", true)
	e.token("beto", "read", "20 days", false)
	e.token("carla", "write", "20 days", false)
	e.person("eva", "superadmin")
	e.token("eva", "write", "20 days", false)
	e.exec(`UPDATE cloud_principals SET enabled = FALSE WHERE id = $1::bigint`, e.ids["eva"])

	rolesBefore := e.count(`SELECT count(*) FROM nexo_role_bindings`)
	peopleBefore := e.count(`SELECT count(*) FROM cloud_principals`)
	before := time.Now()
	res, err := e.issue("beto")
	if err != nil {
		t.Fatal(err)
	}
	if res.Username != "beto" || res.Token.PrincipalID != e.ids["beto"] || res.Token.ID == "" {
		t.Fatalf("resultado: %+v", res)
	}
	if res.Token.TokenHash != "" {
		t.Fatalf("el resultado no devuelve el hash del token")
	}

	// 24 horas, escritura, de la propia persona.
	var scope string
	var expires time.Time
	var createdBy string
	if err := e.db.QueryRow(`SELECT nexo_scope, nexo_expires_at, created_by_principal_id::text FROM cloud_principal_tokens WHERE id = $1::bigint`, res.Token.ID).Scan(&scope, &expires, &createdBy); err != nil {
		t.Fatal(err)
	}
	if scope != "write" || createdBy != e.ids["beto"] {
		t.Fatalf("alcance %q, creado por %q", scope, createdBy)
	}
	if d := expires.Sub(before); d < 23*time.Hour+59*time.Minute || d > 24*time.Hour+time.Minute {
		t.Fatalf("vence en %s, want 24h", d)
	}
	if !res.ExpiresAt.Equal(expires.UTC().Truncate(time.Microsecond)) && res.ExpiresAt.Sub(expires).Abs() > time.Second {
		t.Fatalf("expires_at devuelto %s != guardado %s", res.ExpiresAt, expires)
	}

	// No crea personas ni cambia roles.
	if n := e.count(`SELECT count(*) FROM nexo_role_bindings`); n != rolesBefore {
		t.Fatalf("roles: %d -> %d", rolesBefore, n)
	}
	if n := e.count(`SELECT count(*) FROM cloud_principals`); n != peopleBefore {
		t.Fatalf("personas: %d -> %d", peopleBefore, n)
	}

	// Auditoría: un evento de éxito con el destino y sin el token.
	var action, outcome, reason, meta, source string
	if err := e.db.QueryRow(`SELECT action, outcome, reason_code, metadata::text, actor_source FROM cloud_auth_audit_log WHERE action = $1`, NexoEmergencyAuditAction).
		Scan(&action, &outcome, &reason, &meta, &source); err != nil {
		t.Fatalf("auditoría: %v", err)
	}
	if outcome != "success" || reason != EmergencyIssued || source != "bootstrap_cli" ||
		!strings.Contains(meta, `"emergency": true`) || !strings.Contains(meta, `"username": "beto"`) || !strings.Contains(meta, `"reason": "prueba"`) {
		t.Fatalf("auditoría: %s %s %s %s", outcome, reason, source, meta)
	}
	if strings.Contains(meta, "hash-emergencia") {
		t.Fatalf("la auditoría no lleva el hash del token: %s", meta)
	}
	if n := e.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = $1 AND target_principal_id = $2::bigint`, NexoEmergencyAuditAction, e.ids["beto"]); n != 1 {
		t.Fatalf("eventos del destino: %d", n)
	}

	// Alerta alta, de la capa de plataforma, en la misma operación.
	var sev, layer, subject, summary string
	if err := e.db.QueryRow(`SELECT severity, layer, subject, summary FROM nexo_alerts WHERE rule = 'emergency_access_used'`).Scan(&sev, &layer, &subject, &summary); err != nil {
		t.Fatalf("alerta: %v", err)
	}
	if sev != "high" || layer != "plataforma" || subject != "principal:"+e.ids["beto"] || !strings.Contains(summary, "beto") || res.Alert == nil {
		t.Fatalf("alerta: %s %s %s %s (res=%v)", sev, layer, subject, summary, res.Alert)
	}

	// Con el token de emergencia vigente, un segundo intento se niega.
	_, err = e.issue("alicia")
	wantEmergencyCode(t, err, EmergencyHasValidToken)
	if n := e.count(`SELECT count(*) FROM nexo_alerts WHERE rule = 'emergency_access_used'`); n != 1 {
		t.Fatalf("alertas: %d", n)
	}
}

// Si el token de emergencia se revoca o vence, se puede volver a pedir.
func TestNexoEmergencyTokenCanBeReissuedAfterRevocation(t *testing.T) {
	e := newEmergencyEnv(t)
	e.person("alicia", "superadmin")
	res, err := e.issue("alicia")
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE cloud_principal_tokens SET revoked_at = now() WHERE id = $1::bigint`, res.Token.ID)
	if _, err := e.issue("alicia"); err != nil {
		t.Fatalf("tras revocar se puede pedir otro: %v", err)
	}
	if n := e.count(`SELECT count(*) FROM nexo_alerts WHERE rule = 'emergency_access_used'`); n != 2 {
		t.Fatalf("una alerta por emisión: %d", n)
	}
}

// Dos intentos a la vez: los candados dejan pasar solo uno.
func TestNexoEmergencyTokenConcurrentAttempts(t *testing.T) {
	e := newEmergencyEnv(t)
	e.person("alicia", "superadmin")
	e.person("beto", "superadmin")
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for i := 0; i < 6; i++ {
		user := []string{"alicia", "beto"}[i%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.cs.NexoEmergencyToken(context.Background(), NexoEmergencyTokenParams{
				User: user, TokenPrefix: fmt.Sprintf("egc_cc_%d", i), TokenHash: fmt.Sprintf("hash-cc-%d", i),
			}, AuthAuditEvent{ActorSource: "bootstrap_cli"})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		if err == nil {
			ok++
			continue
		}
		var ee *NexoEmergencyError
		if !errors.As(err, &ee) || ee.Code != EmergencyHasValidToken {
			t.Errorf("negativa inesperada: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("emisiones concurrentes exitosas = %d, want 1", ok)
	}
	if n := e.count(`SELECT count(*) FROM cloud_principal_tokens WHERE name LIKE 'emergency-%'`); n != 1 {
		t.Fatalf("tokens de emergencia: %d", n)
	}
}
