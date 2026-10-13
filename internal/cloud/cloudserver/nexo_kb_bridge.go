package cloudserver

// Nexo IA · NX-10 — adaptador entre cloudserver y los espacios de
// conocimiento (internal/nexo/kb). Registra /nexo/kb/v1 con withAuth (401) y
// la identidad de la petición armada como en NX-05 (nexoWithAuthzIdentity:
// token asignado de NX-01, IP y User-Agent de NX-04). El módulo no importa
// cloudserver (design §4).

import (
	"database/sql"
	"errors"
	"net/http"
	"sync"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/httpapi"
	kbservice "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

// nexoKBServices guarda un servicio por base (el servicio no tiene estado por
// petición; guarda el modo de búsqueda leído una vez del catálogo).
var nexoKBServices sync.Map // *sql.DB -> *kbservice.Service

func (s *CloudServer) nexoKBService(_ *http.Request) (*kbservice.Service, error) {
	roles, ok := s.nexoAuthz()
	if !ok {
		return nil, errors.New("nexo kb: store is not configured")
	}
	var db *sql.DB
	for _, candidate := range []any{s.store, s.adminIdentity} {
		if p, ok := candidate.(nexoAuthzDB); ok && p != nil && p.NexoDB() != nil {
			db = p.NexoDB()
			break
		}
	}
	if v, ok := nexoKBServices.Load(db); ok {
		return v.(*kbservice.Service), nil
	}
	v, _ := nexoKBServices.LoadOrStore(db, kbservice.New(db, roles))
	return v.(*kbservice.Service), nil
}

// registerNexoKBRoutes registra /nexo/kb/v1 (enganche // NX-10 en routes()).
func (s *CloudServer) registerNexoKBRoutes(mux *http.ServeMux) {
	httpapi.Register(mux, s.nexoKBService, func(next http.HandlerFunc) http.HandlerFunc {
		return s.withAuth(func(w http.ResponseWriter, r *http.Request) {
			r, _ = nexoWithAuthzIdentity(r)
			next(w, r)
		})
	})
}
