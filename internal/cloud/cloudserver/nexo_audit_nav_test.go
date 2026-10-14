package cloudserver

// Nexo IA · SG-07 — la pestaña "Auditoría" sale para quien tiene
// platform.audit.read, en todas las páginas del panel, y solo para esa gente.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
)

const auditNavLink = `href="/dashboard/admin/audit"`

func auditNavVisible(body string) bool { return strings.Contains(body, auditNavLink) }

func TestNexoAuditNavTab(t *testing.T) {
	env := newAuditPanelEnv(t)
	env.srv.principalProject = nexoNavNoProjects{}
	pages := []string{"/dashboard/", "/dashboard/kb", "/dashboard/browser", "/dashboard/admin/audit"}

	// El auditor no es admin de Engram y ve la pestaña en todo el panel; el
	// token de solo lectura también (leer no exige escribir).
	for _, who := range []string{"luis", "luis-read"} {
		for _, p := range pages {
			if body := env.mustBody(who, p, http.StatusOK); !auditNavVisible(body) || !strings.Contains(body, ">Auditoría</a>") {
				t.Errorf("%s en %s: falta la pestaña Auditoría", who, p)
			}
		}
	}
	// Superadmin y admin de Engram (rol admin de plataforma incluye al
	// auditor) la ven por su rol, no por ser admin de Engram.
	for _, who := range []string{"carla", "pedro"} {
		for _, p := range pages[:3] {
			if body := env.mustBody(who, p, http.StatusOK); !auditNavVisible(body) {
				t.Errorf("%s en %s: falta la pestaña Auditoría", who, p)
			}
		}
	}
	// Una persona miembro no la ve, aunque la página cargue.
	for _, p := range pages[:3] {
		if body := env.mustBody("ana", p, http.StatusOK); auditNavVisible(body) {
			t.Errorf("miembro en %s: no debe ver la pestaña Auditoría", p)
		}
	}
	// La pestaña es solo interfaz: el miembro sigue sin entrar a la pantalla.
	if rec := env.get("ana", "/dashboard/admin/audit"); rec.Code != http.StatusForbidden {
		t.Errorf("miembro en la pantalla de auditoría: %d", rec.Code)
	}
	// Quitar el rol se nota sin esperar: la caché de la pestaña es corta y el
	// servidor niega la pantalla desde el primer momento.
	if _, err := env.db.Exec(`DELETE FROM nexo_role_bindings WHERE principal_id = $1::bigint AND scope_type = 'platform'`, env.ids["luis"]); err != nil {
		t.Fatal(err)
	}
	if rec := env.get("luis", "/dashboard/admin/audit"); rec.Code != http.StatusForbidden {
		t.Errorf("auditor sin rol en la pantalla: %d", rec.Code)
	}
	env.srv.nexo.auditNav = nexoAuditNavCache{}
	if body := env.mustBody("luis", "/dashboard/kb", http.StatusOK); auditNavVisible(body) {
		t.Errorf("sin rol de auditor la pestaña debe desaparecer")
	}
	env.assertNoLeaks()
}

// Un admin de Engram con token heredado (sin persona administrada) no tiene
// roles de Nexo: las rutas de auditoría lo rechazan y la pestaña no sale.
func TestNexoAuditNavLegacyAdminHidden(t *testing.T) {
	env := newAuditPanelEnv(t)
	srv := New(env.cs, legacyAdminOnlyAuthService(t), 0, WithAdminIdentityStore(env.cs), WithDashboardAdminToken("legacy-admin-token"))
	cookie := managedDashboardLogin(t, srv, "legacy-admin-token", false)
	if !srv.isDashboardAdmin(httptestRequestWithCookie(cookie)) {
		t.Fatalf("la sesión heredada debe ser admin de Engram")
	}
	rec := performDashboardRequest(srv, http.MethodGet, "/dashboard/", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin heredado en /dashboard/: %d %.200s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `href="/dashboard/admin"`) {
		t.Errorf("el admin heredado sí ve la pestaña Administración de Engram")
	}
	if auditNavVisible(rec.Body.String()) {
		t.Errorf("el admin heredado no tiene platform.audit.read: no debe ver la pestaña")
	}
	if rec := performDashboardRequest(srv, http.MethodGet, "/dashboard/admin/audit", cookie); rec.Code != http.StatusForbidden {
		t.Errorf("admin heredado en la pantalla de auditoría: %d", rec.Code)
	}
}

func httptestRequestWithCookie(c *http.Cookie) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/dashboard/kb", nil)
	req.AddCookie(c)
	return req
}

// Si la consulta de roles falla, la página carga igual y la pestaña se oculta
// (falla cerrado); nada se guarda en la caché.
func TestNexoAuditNavAuthzErrorHides(t *testing.T) {
	env := newAuditPanelEnv(t)
	env.mustBody("luis", "/dashboard/kb", http.StatusOK)
	cookie := env.cookie("luis")
	if _, err := env.db.Exec(`ALTER TABLE nexo_role_bindings RENAME TO nexo_role_bindings_off`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = env.db.Exec(`ALTER TABLE nexo_role_bindings_off RENAME TO nexo_role_bindings`) })
	env.srv.nexo.auditNav = nexoAuditNavCache{}
	rec := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb", cookie)
	if auditNavVisible(rec.Body.String()) {
		t.Errorf("con la consulta de roles caída la pestaña debe estar oculta (código %d)", rec.Code)
	}
	if len(env.srv.nexo.auditNav.entries) != 0 {
		t.Errorf("un error no se guarda en la caché: %d entradas", len(env.srv.nexo.auditNav.entries))
	}
}

func TestNexoAuditNavCacheExpires(t *testing.T) {
	var c nexoAuditNavCache
	now := time.Now()
	c.put("1|2", true, "admin", now)
	if v, role, ok := c.get("1|2", now.Add(nexoAuditNavTTL-time.Second)); !ok || !v || role != "admin" {
		t.Errorf("la respuesta guardada debe valer dentro del plazo")
	}
	if _, _, ok := c.get("1|2", now.Add(nexoAuditNavTTL)); ok {
		t.Errorf("la respuesta guardada debe vencer")
	}
	for i := 0; i < nexoAuditNavMaxEntries+10; i++ {
		c.put(time.Duration(i).String(), false, "", now)
	}
	if len(c.entries) > nexoAuditNavMaxEntries {
		t.Errorf("la caché pasó el tope: %d", len(c.entries))
	}
}

// nexoNavNoProjects hace que las páginas de datos del panel carguen en las
// pruebas (sin proyectos inscritos).
type nexoNavNoProjects struct{}

func (nexoNavNoProjects) EnrolledProjectsForPrincipal(context.Context, cloudauth.Principal) ([]string, error) {
	return nil, nil
}

func (nexoNavNoProjects) AuthorizeProjectForPrincipal(context.Context, cloudauth.Principal, string) error {
	return errors.New("sin proyectos")
}
