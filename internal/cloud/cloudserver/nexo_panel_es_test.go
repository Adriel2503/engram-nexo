package cloudserver

// Nexo IA · K6 (OP-06) — las páginas del panel de Engram salen en español con
// la marca de Nexo (usuarios, tokens, permisos de proyecto, administración,
// memorias, proyectos, colaboradores, salud y auditoría) y sin los textos en
// inglés que antes mostraban. Contra Postgres (CLOUDSTORE_TEST_DSN).

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
)

func TestNexoPanelPagesAreInSpanish(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	// Servidor con la autorización de proyectos para que las páginas de datos
	// (memorias, proyectos, colaboradores) se dibujen.
	resolver := cloudauth.NewPrincipalResolver(cloudauth.ResolverConfig{Hasher: env.hasher, ManagedTokens: nexoTestTokenLookup{cs: env.cs},
		Legacy: cloudauth.LegacyCredentials{SyncToken: "shared-sync-token-e2e"}})
	env.srv = New(env.cs, nexoRealAuth{r: resolver}, 0, WithAdminIdentityStore(env.cs), WithManagedTokenHasher(env.hasher),
		WithPrincipalStateStore(env.cs), WithPrincipalProjectAuthorizer(nexoNoProjects{}))
	carla := env.panel("carla")
	pages := []struct {
		path    string
		want    []string
		notWant []string
	}{
		{"/dashboard/browser", []string{"Memorias del equipo", "Todos los proyectos", `placeholder="Buscar..."`}, []string{"Knowledge Browser", "All Projects", "Search..."}},
		{"/dashboard/projects", []string{"Proyectos del equipo", `placeholder="Buscar proyectos..."`}, []string{"PROJECT ATLAS", "Search projects..."}},
		{"/dashboard/contributors", []string{"Colaboradores del equipo", `placeholder="Buscar colaboradores..."`}, []string{"CONTRIBUTOR SIGNAL", "Search contributors..."}},
		{"/dashboard/admin", []string{"Resumen de administración", "Base de datos", "Proyectos en pausa"}, []string{"Admin Overview", "Paused Projects"}},
		{"/dashboard/admin/projects", []string{"Control de proyectos"}, []string{"Project Controls"}},
		{"/dashboard/admin/health", []string{"Salud del servidor"}, []string{"Admin Health"}},
		{"/dashboard/admin/audit-log", []string{"Auditoría", "Cargando la auditoría..."}, []string{"Loading audit log..."}},
		{"/dashboard/admin/audit-log/list", []string{"Filtrar", "Todos los resultados", `placeholder="Desde (RFC3339)"`, "Sin registros de auditoría"}, []string{"All Outcomes", ">Filter<", "From (RFC3339)", "No Audit Entries"}},
		{"/dashboard/admin/users", []string{"Crear usuario", `placeholder="Usuario"`, `placeholder="Nombre para mostrar (opcional)"`, "Un usuario nuevo parte sin permisos"},
			[]string{"Create User", "Display name (optional)", "deny-by-default"}},
		{"/dashboard/admin/users/" + env.ids["ana"], []string{"Crear token", "Días de vida (por defecto 90)", "Alcance: escritura", "Permisos de proyecto", "Dar permiso", "Nombre del proyecto",
			"El token se muestra una sola vez", "Deshabilitar usuario"},
			[]string{"Create Token", "Expires in days", "Scope: write", "Project Grants", "Grant Project", "Tokens are shown only once", "Disable User"}},
	}
	for _, p := range pages {
		rec := carla.get(p.path)
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", p.path, rec.Code, rec.Body.String())
			continue
		}
		if strings.Contains(body, "<html") && !strings.Contains(body, `<html lang="es"`) {
			t.Errorf("%s: el documento no está en español", p.path)
		}
		for _, w := range p.want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: falta %q", p.path, w)
			}
		}
		for _, n := range p.notWant {
			if strings.Contains(body, n) {
				t.Errorf("%s: sigue en inglés %q", p.path, n)
			}
		}
	}
	// La emisión de un token muestra la página de token creado en español.
	rec := carla.post("/dashboard/admin/users/"+env.ids["ana"]+"/tokens", kbForm(carla.csrf(""), "name", "panel", "scope", "read", "expires_in_days", "15"))
	for _, w := range []string{"Token creado", "Guarda este token ahora", "Este token se muestra una sola vez", "alcance lectura", "Volver a ana"} {
		if !strings.Contains(rec.Body.String(), w) {
			t.Errorf("token creado: falta %q", w)
		}
	}
	// Las partes que cargan por htmx también.
	for path, want := range map[string]string{
		"/dashboard/admin/users/list": "Todavía no hay usuarios",
	} {
		if rec := carla.get(path); rec.Code == http.StatusOK && !strings.Contains(rec.Body.String(), want) && strings.Contains(rec.Body.String(), "No Managed Users Yet") {
			t.Errorf("%s: sin traducir", path)
		}
	}
}

// nexoNoProjects: una sesión de panel sin proyectos asignados (basta para que
// las páginas de datos se dibujen vacías).
type nexoNoProjects struct{}

func (nexoNoProjects) AuthorizeProjectForPrincipal(context.Context, cloudauth.Principal, string) error {
	return errors.New("sin proyectos")
}

func (nexoNoProjects) EnrolledProjectsForPrincipal(context.Context, cloudauth.Principal) ([]string, error) {
	return []string{}, nil
}
