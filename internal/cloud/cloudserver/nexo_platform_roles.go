package cloudserver

// Nexo IA · NX-05 (unificación de roles de plataforma) — decisiones de
// plataforma de las pantallas y rutas de Engram que antes miraban la columna
// role de Engram. Ahora la columna es un valor derivado del rol de Nexo y
// ninguna decisión depende de ella: pasan por authz.Engine.Can con la
// capacidad que corresponde (docs/nexo/roles/design.md §11.3).
//
//	isDashboardAdmin, canManageManagedUsers -> platform.users.manage
//	requireManagedAdmin                     -> capacidad de la ruta (ya era así)
//	crear persona con un rol                -> platform.admins.manage (nexoCreateUserRole)
//
// El token heredado ENGRAM_CLOUD_ADMIN (identidad no administrada) no tiene
// roles de Nexo (R-15): sigue como antes, lo decide Engram.

import (
	"context"
	"net/http"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
)

// nexoDashboardAdmin decide si una sesión administrada del panel es de
// administración (ve y usa las pantallas de /dashboard/admin). handled = false
// deja que Engram decida: sin almacén Nexo o con una identidad no
// administrada (token heredado, modo sin autenticación).
//
// Es una decisión de vista: ignora el alcance del token (un token read de un
// admin ve las pantallas); toda petición que cambia algo la rechaza el
// guardia de alcance del panel (nexoDashboardScopeGuard) y cada ruta vuelve a
// comprobar su capacidad. Falla cerrado ante un error.
func (s *CloudServer) nexoDashboardAdmin(r *http.Request, principal cloudauth.Principal) (allowed, handled bool) {
	return s.nexoDashboardCan(r, principal, authz.CapPlatformUsersManage) // nexo_users_dashboard.go
}

// nexoFillPlatformRoles pone el rol de plataforma de Nexo en cada persona
// (campo platform_role de la API /admin/users). Si no se puede leer, deja el
// campo vacío: nunca inventa un rol.
func (s *CloudServer) nexoFillPlatformRoles(ctx context.Context, users []cloudstore.HumanUser) {
	roles, ok := s.nexoPlatformRoles(ctx)
	if !ok {
		return
	}
	for i := range users {
		if r, found := roles[users[i].PrincipalID]; found {
			users[i].PlatformRole = string(r)
		}
	}
}

// nexoPlatformRoles lee el rol de plataforma de todos (una consulta).
func (s *CloudServer) nexoPlatformRoles(ctx context.Context) (map[string]authz.Role, bool) {
	for _, candidate := range []any{s.store, s.adminIdentity} {
		if p, ok := candidate.(nexoAuthzDB); ok && p != nil {
			if db := p.NexoDB(); db != nil {
				roles, err := authz.PlatformRoleMap(ctx, db)
				if err != nil {
					nexoLogInternal("platform roles", err)
					return nil, false
				}
				return roles, true
			}
		}
	}
	return nil, false
}
