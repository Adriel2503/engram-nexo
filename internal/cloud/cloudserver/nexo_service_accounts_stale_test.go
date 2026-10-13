package cloudserver

// SG-10: marca de cuenta de servicio sin uso (NEXO_SA_STALE_DAYS) en la API
// y en el panel. Solo informa: la cuenta sigue habilitada.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

func TestNexoServiceAccountStaleMark(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	t.Setenv(evidence.SAStaleDaysEnv, "")
	if rec := env.req("POST", "/nexo/authz/v1/service-accounts", "pedro", `{"slug":"ci-trimestral","owner":"ana"}`, "10.0.0.40"); rec.Code != http.StatusCreated {
		t.Fatalf("alta: %d %s", rec.Code, rec.Body.String())
	}
	fresh := nexoJSON(t, env.req("GET", "/nexo/authz/v1/service-accounts/ci-trimestral", "pedro", "", "10.0.0.40"))
	if fresh["stale"] != false || fresh["last_activity_at"] != nil || fresh["stale_after_days"] != float64(180) {
		t.Fatalf("cuenta nueva: %v", fresh)
	}
	// Alta hace 200 días y nunca usada.
	if _, err := env.db.Exec(`UPDATE nexo_service_accounts SET created_at = now() - interval '200 days' WHERE slug = 'ci-trimestral'`); err != nil {
		t.Fatal(err)
	}
	detail := nexoJSON(t, env.req("GET", "/nexo/authz/v1/service-accounts/ci-trimestral", "pedro", "", "10.0.0.40"))
	if detail["stale"] != true || detail["idle_days"].(float64) < 199 || detail["enabled"] != true {
		t.Fatalf("detalle sin uso: %v", detail)
	}
	list := env.req("GET", "/nexo/authz/v1/service-accounts", "pedro", "", "10.0.0.40").Body.String()
	if !strings.Contains(list, `"stale":true`) {
		t.Fatalf("listado: %s", list)
	}
	cookie := managedDashboardLogin(t, env.srv, env.tokens["carla"], false)
	page := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/admin/service-accounts?sa=ci-trimestral", cookie).Body.String()
	for _, want := range []string{"Sin uso", "nunca usada (creada hace 200 días)", "no se deshabilita sola", "Último uso"} {
		if !strings.Contains(page, want) {
			t.Fatalf("panel sin %q: %s", want, page)
		}
	}
	// 0 = sin marca.
	t.Setenv(evidence.SAStaleDaysEnv, "0")
	off := nexoJSON(t, env.req("GET", "/nexo/authz/v1/service-accounts/ci-trimestral", "pedro", "", "10.0.0.40"))
	if off["stale"] != false || off["stale_after_days"] != float64(0) {
		t.Fatalf("NEXO_SA_STALE_DAYS=0: %v", off)
	}
	if page := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/admin/service-accounts", cookie).Body.String(); strings.Contains(page, ">Sin uso<") {
		t.Fatalf("panel con 0: %s", page)
	}
}
