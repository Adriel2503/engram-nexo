package cloudserver

// SG-06 — continuidad del acceso de administración, por HTTP contra Postgres:
//
//   - crear el segundo superadmin con la API (alta con rol superadmin) y
//     emitirle su token;
//   - renovación sin corte: un superadmin se emite su propio token nuevo con
//     su token vigente, lo prueba y revoca el viejo con el nuevo;
//   - quién puede renovar qué: un admin se renueva solo a sí mismo, no a un
//     superadmin; un token de solo lectura no emite;
//   - la alerta de superadmin único aparece al arrancar y se apaga al crear
//     el segundo; los tokens por vencer avisan.

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

func TestNexoSecondSuperadminAndSelfRenewal(t *testing.T) {
	env := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	const ip = "10.99.0.1"
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "carla", "", ip); rec.Code != http.StatusOK {
		t.Fatalf("me de carla: %d", rec.Code)
	}

	// Segundo superadmin en un solo paso (rol superadmin en el alta) y su token.
	rec := env.req(http.MethodPost, "/admin/users", "carla", `{"username":"diego","email":"diego@rp.pe","display_name":"Diego Prueba","role":"superadmin"}`, ip)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("alta del segundo superadmin: %d %s", rec.Code, rec.Body.String())
	}
	created := nexoJSON(t, rec)
	if created["platform_role"] != "superadmin" {
		t.Fatalf("platform_role = %v", created["platform_role"])
	}
	if rec := env.req(http.MethodPost, "/nexo/authz/v1/tokens", "carla", `{"principal":"diego","name":"laptop-diego","expires_in_days":90,"scope":"write"}`, ip); rec.Code != http.StatusCreated {
		t.Fatalf("token del segundo superadmin: %d %s", rec.Code, rec.Body.String())
	} else if raw, _ := nexoJSON(t, rec)["raw_token"].(string); raw == "" {
		t.Fatal("la emisión devuelve el token una sola vez")
	} else {
		env.tokens["diego"] = raw
	}
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "diego", "", ip); rec.Code != http.StatusOK || nexoJSON(t, rec)["platform"] == nil {
		t.Fatalf("diego entra con su token: %d %s", rec.Code, rec.Body.String())
	}
	if n := env.count(`SELECT count(*) FROM nexo_role_bindings WHERE scope_type = 'platform' AND role = 'superadmin'`); n != 2 {
		t.Fatalf("superadmins: %d", n)
	}

	// Renovación sin corte de carla: emitir con el token vigente, probar, revocar el viejo con el nuevo.
	var oldID string
	if err := env.db.QueryRow(`SELECT id::text FROM cloud_principal_tokens WHERE principal_id = $1::bigint ORDER BY id LIMIT 1`, env.ids["carla"]).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	rec = env.req(http.MethodPost, "/nexo/authz/v1/tokens", "carla", `{"principal":"carla","name":"laptop-carla-nuevo","expires_in_days":90,"scope":"write"}`, ip)
	if rec.Code != http.StatusCreated {
		t.Fatalf("un superadmin se emite su propio token con el vigente: %d %s", rec.Code, rec.Body.String())
	}
	body := nexoJSON(t, rec)
	newRaw, _ := body["raw_token"].(string)
	if newRaw == "" || body["token"].(map[string]any)["expires_at"] == nil {
		t.Fatalf("respuesta de emisión: %s", rec.Body.String())
	}
	env.tokens["carla-nuevo"] = newRaw
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "carla-nuevo", "", ip); rec.Code != http.StatusOK {
		t.Fatalf("probar el token nuevo: %d", rec.Code)
	}
	if rec := env.req(http.MethodPost, "/admin/tokens/"+oldID+"/revoke", "carla-nuevo", `{"reason":"renovación"}`, ip); rec.Code != http.StatusOK {
		t.Fatalf("revocar el viejo con el nuevo: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "carla", "", ip); rec.Code != http.StatusUnauthorized {
		t.Fatalf("el token viejo ya no sirve: %d", rec.Code)
	}
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "carla-nuevo", "", ip); rec.Code != http.StatusOK {
		t.Fatalf("el nuevo sigue sirviendo: %d", rec.Code)
	}
	// Todo queda en la auditoría.
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = 'token.revoke' AND outcome = 'success'`); n < 1 {
		t.Fatalf("la revocación queda auditada: %d", n)
	}

	// Un admin se renueva solo a sí mismo; no puede emitir ni revocar tokens de un superadmin.
	if rec := env.req(http.MethodPost, "/nexo/authz/v1/tokens", "pedro", `{"principal":"pedro","name":"laptop-pedro-2","expires_in_days":90}`, ip); rec.Code != http.StatusCreated {
		t.Fatalf("un admin se emite su propio token: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.req(http.MethodPost, "/nexo/authz/v1/tokens", "pedro", `{"principal":"diego","name":"x","expires_in_days":90}`, ip); rec.Code != http.StatusForbidden {
		t.Fatalf("un admin no emite tokens de un superadmin: %d", rec.Code)
	}
	// Un token de solo lectura no emite tokens.
	env.issue("carla", "carla-lectura", security.TokenIssue{Days: 30, Scope: "read"})
	if rec := env.req(http.MethodPost, "/nexo/authz/v1/tokens", "carla-lectura", `{"principal":"carla","name":"x","expires_in_days":90}`, ip); rec.Code != http.StatusForbidden {
		t.Fatalf("un token read no emite: %d", rec.Code)
	}
}

// Si la renovación se hace tarde, el acceso no se pierde por el camino de
// la API mientras haya otro superadmin; y los avisos van apareciendo.
func TestNexoContinuityAlertsOverHTTP(t *testing.T) {
	env := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	const ip = "10.99.1.1"
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "carla", "", ip); rec.Code != http.StatusOK {
		t.Fatalf("me: %d", rec.Code)
	}
	rules := evidence.DefaultRules()
	alertsOf := func(rule string) []evidence.Alert {
		t.Helper()
		got, _, err := evidence.EvaluateOnce(context.Background(), env.db, rules, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		var out []evidence.Alert
		for _, a := range got {
			if a.Rule == rule {
				out = append(out, a)
			}
		}
		return out
	}
	// carla es la única superadmin: alerta alta.
	if got := alertsOf(evidence.RuleSingleSuperadmin); len(got) != 1 || got[0].Severity != "high" || !strings.Contains(got[0].Summary, "carla") {
		t.Fatalf("alerta de superadmin único: %+v", got)
	}
	// Se promueve a pedro: deja de avisar.
	if rec := env.req(http.MethodPut, "/nexo/authz/v1/platform/members/pedro", "carla", `{"role":"superadmin"}`, ip); rec.Code != http.StatusOK {
		t.Fatalf("promover a superadmin: %d %s", rec.Code, rec.Body.String())
	}
	if got := alertsOf(evidence.RuleSingleSuperadmin); len(got) != 0 {
		t.Fatalf("con dos superadmins no hay alerta: %+v", got)
	}
	// El token de carla vence en 20 días y el de pedro en 6: un aviso cada uno.
	mustExec(t, env.db, `UPDATE cloud_principal_tokens SET nexo_expires_at = now() + interval '20 days' WHERE principal_id = $1::bigint`, env.ids["carla"])
	mustExec(t, env.db, `UPDATE cloud_principal_tokens SET nexo_expires_at = now() + interval '6 days' WHERE principal_id = $1::bigint`, env.ids["pedro"])
	got := alertsOf(evidence.RulePrivilegedTokenExpiring)
	if len(got) != 2 {
		t.Fatalf("avisos de vencimiento: %+v", got)
	}
	// Se ven por la API de alertas con el filtro de la regla.
	rec := env.req(http.MethodGet, "/nexo/admin/alerts?rule=privileged_token_expiring", "carla", "", ip)
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `"rule":"privileged_token_expiring"`) != 2 {
		t.Fatalf("API de alertas: %d %s", rec.Code, rec.Body.String())
	}
	for name, raw := range env.tokens {
		if strings.Contains(rec.Body.String(), raw) {
			t.Fatalf("la API de alertas filtra el token de %s", name)
		}
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
