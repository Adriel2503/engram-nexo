package cloudserver

// Nexo IA · SG-07 — pestaña "Auditoría" del menú para quien tiene
// platform.audit.read (docs/nexo/panel/design.md §10).
//
// La marca del panel no recibe datos de autorización de la petición, así que
// un middleware de Nexo (colgado de nexoHandler, el mismo enganche NX-04 /
// NX-07 de Handler()) calcula una sola vez por petición del panel si la
// sesión tiene platform.audit.read y deja la marca en el contexto
// (dashboard.WithNexoAuditNav). nexo_brand.templ solo la lee.
//
// Esto es solo interfaz: la autorización de verdad sigue en cada ruta de
// /dashboard/admin/audit (nexoAuditPanel). El cálculo nunca falla la página:
// ante cualquier error la pestaña se oculta.
//
// Costo: la cookie se valida con la firma (sin base de datos) y el resultado
// se guarda unos segundos por persona y token; una petición sin respuesta en
// caché hace una sola consulta de rol de plataforma. Un rol revocado deja de
// verse en la pestaña como mucho tras nexoAuditNavTTL; el servidor ya
// rechaza la pantalla desde ese momento.

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/dashboard"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
)

const (
	// nexoAuditNavTTL: cuánto vale la respuesta guardada por sesión.
	nexoAuditNavTTL = 30 * time.Second
	// nexoAuditNavMaxEntries: tope de personas guardadas; al llegar se
	// descartan las vencidas y, si siguen sobrando, todas.
	nexoAuditNavMaxEntries = 2048
)

type nexoAuditNavEntry struct {
	allowed bool
	role    string // rol de plataforma de Nexo para la cabecera (NX-05); "" = sin dato
	until   time.Time
}

// nexoAuditNavCache guarda, por persona y token, si la pestaña se muestra.
type nexoAuditNavCache struct {
	mu      sync.Mutex
	entries map[string]nexoAuditNavEntry
}

func (c *nexoAuditNavCache) get(key string, now time.Time) (allowed bool, role string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, found := c.entries[key]
	if !found || !now.Before(e.until) {
		return false, "", false
	}
	return e.allowed, e.role, true
}

// forget descarta lo guardado de una persona (todas sus sesiones): se llama
// al cambiar su rol de plataforma para que la cabecera no muestre el anterior.
func (c *nexoAuditNavCache) forget(principalID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if strings.HasPrefix(k, principalID+"|") {
			delete(c.entries, k)
		}
	}
}

func (c *nexoAuditNavCache) put(key string, allowed bool, role string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]nexoAuditNavEntry{}
	}
	if len(c.entries) >= nexoAuditNavMaxEntries {
		for k, e := range c.entries {
			if !now.Before(e.until) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= nexoAuditNavMaxEntries {
			clear(c.entries)
		}
	}
	c.entries[key] = nexoAuditNavEntry{allowed: allowed, role: role, until: now.Add(nexoAuditNavTTL)}
}

// nexoAuditNavMiddleware marca en el contexto las peticiones del panel de
// quien tiene platform.audit.read. No cambia la respuesta ni el estado.
func (s *CloudServer) nexoAuditNavMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.nexoAuditNavWanted(r) {
			allowed, role := s.nexoAuditNavInfo(r)
			ctx := r.Context()
			if allowed {
				ctx = dashboard.WithNexoAuditNav(ctx)
			}
			if role != "" {
				ctx = dashboard.WithNexoRole(ctx, role) // NX-05: el rol de plataforma junto al nombre
			}
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// nexoAuditNavWanted: solo las páginas HTML del panel con la marca de Nexo.
// Los recursos estáticos, el acceso y los POST no dibujan el menú, salvo los
// POST de Administración que responden con una página completa (confirmación
// y errores del cambio de rol, NX-05).
func (s *CloudServer) nexoAuditNavWanted(r *http.Request) bool {
	path := r.URL.Path
	adminPost := r.Method == http.MethodPost && strings.HasPrefix(path, "/dashboard/admin/")
	if !dashboard.NexoBrand() || (r.Method != http.MethodGet && r.Method != http.MethodHead && !adminPost) {
		return false
	}
	if path != "/dashboard" && !strings.HasPrefix(path, "/dashboard/") {
		return false
	}
	return !strings.HasPrefix(path, "/dashboard/static/") && path != "/dashboard/login" && path != "/dashboard/logout"
}

// nexoAuditNavInfo decide si la sesión de la petición ve la pestaña y qué rol
// de plataforma muestra la cabecera. Falla cerrado: sin sesión firmada
// válida, sin base de Nexo, con una identidad no administrada o ante
// cualquier error, la pestaña se oculta y no hay rol.
func (s *CloudServer) nexoAuditNavInfo(r *http.Request) (allowed bool, role string) {
	cookie, err := r.Cookie(dashboardSessionCookieName)
	if err != nil {
		return false, ""
	}
	principal, err := s.parseDashboardPrincipalSession(cookie.Value)
	if err != nil {
		return false, ""
	}
	key := principal.ID + "|" + principal.TokenID
	now := time.Now()
	if allowed, role, ok := s.nexo.auditNav.get(key, now); ok {
		return allowed, role
	}
	svc, ok := s.nexoAuthz()
	if !ok {
		return false, ""
	}
	ctx := WithPrincipal(r.Context(), principal)
	id := nexoAuthzIdentity(ctx)
	ctx = authz.WithRequestCache(authz.WithIdentity(ctx, id))
	if !id.Managed() {
		return false, ""
	}
	// Can no registra denegaciones: esto solo decide qué se dibuja. La misma
	// consulta trae el rol efectivo de plataforma para la cabecera.
	d, err := svc.Engine().Can(ctx, id, authz.PlatformScope(), authz.CapPlatformAuditRead)
	if err != nil {
		return false, ""
	}
	if d.Role != authz.RoleNone {
		role = string(d.Role)
	}
	s.nexo.auditNav.put(key, d.Allowed, role, now)
	return d.Allowed, role
}
