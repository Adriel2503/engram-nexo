package cloudserver

// NX-07 · interruptor NEXO_SERVICE_ACCOUNTS_ENABLED (punto 11 del coordinador).
// Postgres real. Apagado (el defecto): cada camino que crea o da acceso a una
// cuenta de servicio responde 403 service_accounts_disabled sin crear nada,
// los tokens existentes no autentican, y consultar, revocar y deshabilitar
// siguen funcionando. Un valor inválido impide arrancar.

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/features"
)

func TestNexoServiceAccountsSwitchOffClosesEveryPathPG(t *testing.T) {
	env := newNexoSecEnv(t, map[string]string{features.ServiceAccountsEnv: "false"})
	const ip = "10.0.0.41"
	nexoSeedSAProfile(t, env)
	count := func(q string, args ...any) int { return env.count(q, args...) }
	tokensBefore := count(`SELECT count(*) FROM cloud_principal_tokens`)
	accountsBefore := count(`SELECT count(*) FROM nexo_service_accounts`)
	codesBefore := count(`SELECT count(*) FROM nexo_activation_codes`)
	var tokenID string
	if err := env.db.QueryRow(`SELECT id::text FROM cloud_principal_tokens WHERE principal_id = $1::bigint`, env.ids["ci"]).Scan(&tokenID); err != nil {
		t.Fatal(err)
	}
	expectDisabled := func(label string, code int, body string) {
		t.Helper()
		if code != http.StatusForbidden || !strings.Contains(body, features.ServiceAccountsDisabledCode) {
			t.Fatalf("%s: %d %s", label, code, body)
		}
	}

	// API: crear, emitir, rotar, reactivar, y emitir por las rutas generales y las de Engram.
	cases := []struct{ label, method, path, who, body string }{
		{"crear", "POST", "/nexo/authz/v1/service-accounts", "carla", `{"slug":"ci-nueva","owner":"ana"}`},
		{"emitir token", "POST", "/nexo/authz/v1/service-accounts/ci-sembrada/tokens", "carla", `{"name":"x"}`},
		{"rotar token", "POST", "/nexo/authz/v1/service-accounts/ci-sembrada/tokens/" + tokenID + ":rotate", "carla", `{"grace_minutes":0}`},
		{"emitir por la ruta general", "POST", "/nexo/authz/v1/tokens", "carla", `{"principal":"` + env.ids["ci"] + `","name":"x"}`},
	}
	for _, c := range cases {
		rec := env.req(c.method, c.path, c.who, c.body, ip)
		expectDisabled(c.label, rec.Code, rec.Body.String())
		if strings.Contains(rec.Body.String(), "raw_token") {
			t.Fatalf("%s: la respuesta trae un token", c.label)
		}
	}
	// Deshabilitar sigue permitido; habilitar de nuevo no.
	if rec := env.req("POST", "/nexo/authz/v1/service-accounts/ci-sembrada:disable", "carla", "", ip); rec.Code != http.StatusOK {
		t.Fatalf("deshabilitar debe seguir permitido: %d %s", rec.Code, rec.Body.String())
	}
	rec := env.req("POST", "/nexo/authz/v1/service-accounts/ci-sembrada:enable", "carla", "", ip)
	expectDisabled("habilitar", rec.Code, rec.Body.String())
	if count(`SELECT count(*) FROM cloud_principals WHERE kind='service_account' AND enabled`) != 0 {
		t.Fatal("la cuenta no debía volver a habilitarse")
	}
	// Nada se creó.
	if count(`SELECT count(*) FROM cloud_principal_tokens`) != tokensBefore || count(`SELECT count(*) FROM nexo_service_accounts`) != accountsBefore ||
		count(`SELECT count(*) FROM nexo_activation_codes`) != codesBefore {
		t.Fatal("un camino cerrado creó filas")
	}
	// Cada rechazo quedó en la auditoría.
	if n := count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action='nexo.service_accounts.denied' AND reason_code=$1`, features.ServiceAccountsDisabledCode); n < len(cases)+1 {
		t.Fatalf("rechazos auditados: %d, esperaba al menos %d", n, len(cases)+1)
	}

	// El token existente de una cuenta de servicio no autentica (falla cerrado), aunque la cuenta esté habilitada.
	if _, err := env.db.Exec(`UPDATE cloud_principals SET enabled = TRUE WHERE id = $1::bigint`, env.ids["ci"]); err != nil {
		t.Fatal(err)
	}
	if r := env.req("GET", "/nexo/authz/v1/me", "ci", "", ip); r.Code != http.StatusUnauthorized && r.Code != http.StatusForbidden {
		t.Fatalf("un token de cuenta de servicio autenticó con el interruptor apagado: %d", r.Code)
	}
	// Una persona sigue entrando.
	if r := env.req("GET", "/nexo/authz/v1/me", "carla", "", ip); r.Code != http.StatusOK {
		t.Fatalf("una persona debía seguir autenticando: %d", r.Code)
	}

	// Consultar, revocar siguen permitidos.
	if r := env.req("GET", "/nexo/authz/v1/service-accounts", "carla", "", ip); r.Code != http.StatusOK {
		t.Fatalf("listar: %d", r.Code)
	}
	if r := env.req("GET", "/nexo/authz/v1/service-accounts/ci-sembrada", "carla", "", ip); r.Code != http.StatusOK {
		t.Fatalf("ver: %d", r.Code)
	}
	if r := env.req("POST", "/nexo/authz/v1/service-accounts/ci-sembrada/tokens/"+tokenID+":revoke", "carla", `{"reason":"limpieza"}`, ip); r.Code != http.StatusOK {
		t.Fatalf("revocar debe seguir permitido: %d %s", r.Code, r.Body.String())
	}
	if count(`SELECT count(*) FROM cloud_principal_tokens WHERE id=$1::bigint AND revoked_at IS NOT NULL`, tokenID) != 1 {
		t.Fatal("el token no quedó revocado")
	}
}

func TestNexoServiceAccountsSwitchOffPanelPG(t *testing.T) {
	env := newNexoSecEnv(t, map[string]string{features.ServiceAccountsEnv: "false"})
	nexoSeedSAProfile(t, env)
	cookie := managedDashboardLogin(t, env.srv, env.tokens["carla"], false)
	page := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/admin/service-accounts?sa=ci-sembrada", cookie)
	body := page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, "desactivadas en este servidor") || !strings.Contains(body, "NEXO_SERVICE_ACCOUNTS_ENABLED=true") {
		t.Fatalf("la página debe avisar que están desactivadas: %d", page.Code)
	}
	for _, form := range []string{"Crear cuenta de servicio", "Emitir token", "Rotar"} {
		if strings.Contains(body, form) {
			t.Fatalf("la página ofrece %q con el interruptor apagado", form)
		}
	}
	if !strings.Contains(body, "Deshabilitar") {
		t.Fatal("deshabilitar debe seguir disponible")
	}
	m := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("sin token CSRF")
	}
	csrf := url.QueryEscape(m[1])
	var tokenID string
	if err := env.db.QueryRow(`SELECT id::text FROM cloud_principal_tokens WHERE principal_id = $1::bigint`, env.ids["ci"]).Scan(&tokenID); err != nil {
		t.Fatal(err)
	}
	before := env.count(`SELECT count(*) FROM cloud_principal_tokens`)
	accounts := env.count(`SELECT count(*) FROM nexo_service_accounts`)
	for _, c := range []struct{ label, path, form string }{
		{"crear", "/dashboard/admin/service-accounts", "csrf_token=" + csrf + "&slug=ci-panel&owner=ana"},
		{"emitir", "/dashboard/admin/service-accounts/ci-sembrada/token", "csrf_token=" + csrf + "&name=x"},
		{"rotar", "/dashboard/admin/service-accounts/ci-sembrada/rotate", "csrf_token=" + csrf + "&token_id=" + tokenID},
		{"habilitar", "/dashboard/admin/service-accounts/ci-sembrada/enable", "csrf_token=" + csrf},
	} {
		rec := performDashboardForm(env.srv, http.MethodPost, c.path, c.form, cookie, false)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "desactivadas") || strings.Contains(rec.Body.String(), "nexo-sa-token") {
			t.Fatalf("panel %s: %d", c.label, rec.Code)
		}
	}
	// Panel de personas: emitir el token de una cuenta de servicio desde el detalle de usuario.
	rec := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/users/"+env.ids["ci"]+"/tokens", "csrf_token="+csrf+"&name=x", cookie, false)
	// Las rutas de personas de Engram no alcanzan una cuenta de servicio (404 "no es una persona"): tampoco crean nada.
	if rec.Code < 400 {
		t.Fatalf("emitir desde el panel de usuarios no debía prosperar: %d", rec.Code)
	}
	if env.count(`SELECT count(*) FROM cloud_principal_tokens`) != before || env.count(`SELECT count(*) FROM nexo_service_accounts`) != accounts {
		t.Fatal("el panel creó filas con el interruptor apagado")
	}
	// Revocar desde el panel sigue funcionando.
	if rec := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/service-accounts/ci-sembrada/revoke", "csrf_token="+csrf+"&token_id="+tokenID, cookie, false); rec.Code != http.StatusOK {
		t.Fatalf("revocar desde el panel: %d", rec.Code)
	}
}

// Valor inválido: el almacén no abre (el servidor no arranca) y el error nombra la variable sin el valor.
func TestNexoServiceAccountsSwitchInvalidValueStopsStartup(t *testing.T) {
	for _, bad := range []string{"yes", "2", "verdadero-secreto-xyz"} {
		t.Setenv(features.ServiceAccountsEnv, bad)
		_, err := cloudstore.New(cloud.Config{DSN: "postgres://nadie:clave-xyz@127.0.0.1:1/x?sslmode=disable"})
		if err == nil {
			t.Fatalf("%q: debía impedir arrancar", bad)
		}
		msg := err.Error()
		if !strings.Contains(msg, features.ServiceAccountsEnv) || strings.Contains(msg, bad) || strings.Contains(msg, "clave-xyz") {
			t.Fatalf("%q: el error debe nombrar la variable y no mostrar el valor: %s", bad, msg)
		}
	}
	for raw, want := range map[string]bool{"": false, "false": false, "0": false, "true": true, "1": true, " TRUE ": true} {
		got, err := features.ParseServiceAccountsEnabled(raw)
		if err != nil || got != want {
			t.Fatalf("%q: %v %v", raw, got, err)
		}
	}
}

// nexoSeedSAProfile completa la cuenta de servicio sembrada ("ci") con su fila lateral, para consultarla por nombre corto.
func nexoSeedSAProfile(t *testing.T, env *nexoSecEnv) {
	t.Helper()
	if _, err := env.db.Exec(`INSERT INTO nexo_service_accounts (principal_id, slug, description, owner_principal_id, created_by_principal_id) VALUES ($1::bigint, 'ci-sembrada', 'sembrada por la prueba', $2::bigint, $2::bigint)`,
		env.ids["ci"], env.ids["ana"]); err != nil {
		t.Fatal(err)
	}
}
