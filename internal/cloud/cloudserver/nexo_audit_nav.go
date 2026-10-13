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
	until   time.Time
}

// nexoAuditNavCache guarda, por persona y token, si la pestaña se muestra.
type nexoAuditNavCache struct {
	mu      sync.Mutex
	entries map[string]nexoAuditNavEntry
}

func (c *nexoAuditNavCache) get(key string, now time.Time) (allowed, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, found := c.entries[key]
	if !found || !now.Before(e.until) {
		return false, false
	}
	return e.allowed, true
}

func (c *nexoAuditNavCache) put(key string, allowed bool, now time.Time) {
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
	c.entries[key] = nexoAuditNavEntry{allowed: allowed, until: now.Add(nexoAuditNavTTL)}
}

// nexoAuditNavMiddleware marca en el contexto las peticiones del panel de
// quien tiene platform.audit.read. No cambia la respuesta ni el estado.
func (s *CloudServer) nexoAuditNavMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.nexoAuditNavWanted(r) && s.nexoAuditNavAllowed(r) {
			r = r.WithContext(dashboard.WithNexoAuditNav(r.Context()))
		}
		next.ServeHTTP(w, r)
	})
}

// nexoAuditNavWanted: solo las páginas HTML del panel con la marca de Nexo.
// Los recursos estáticos, el acceso y los POST no dibujan el menú.
func (s *CloudServer) nexoAuditNavWanted(r *http.Request) bool {
	if !dashboard.NexoBrand() || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	path := r.URL.Path
	if path != "/dashboard" && !strings.HasPrefix(path, "/dashboard/") {
		return false
	}
	return !strings.HasPrefix(path, "/dashboard/static/") && path != "/dashboard/login" && path != "/dashboard/logout"
}

// nexoAuditNavAllowed decide si la sesión de la petición ve la pestaña.
// Falla cerrado: sin sesión firmada válida, sin base de Nexo, con una
// identidad no administrada o ante cualquier error, la respuesta es no.
func (s *CloudServer) nexoAuditNavAllowed(r *http.Request) bool {
	cookie, err := r.Cookie(dashboardSessionCookieName)
	if err != nil {
		return false
	}
	principal, err := s.parseDashboardPrincipalSession(cookie.Value)
	if err != nil {
		return false
	}
	key := principal.ID + "|" + principal.TokenID
	now := time.Now()
	if allowed, ok := s.nexo.auditNav.get(key, now); ok {
		return allowed
	}
	svc, ok := s.nexoAuthz()
	if !ok {
		return false
	}
	ctx := WithPrincipal(r.Context(), principal)
	id := nexoAuthzIdentity(ctx)
	ctx = authz.WithRequestCache(authz.WithIdentity(ctx, id))
	if !id.Managed() {
		return false
	}
	// Can no registra denegaciones: esto solo decide qué se dibuja.
	d, err := svc.Engine().Can(ctx, id, authz.PlatformScope(), authz.CapPlatformAuditRead)
	if err != nil {
		return false
	}
	s.nexo.auditNav.put(key, d.Allowed, now)
	return d.Allowed
}
