package cloudserver

// Nexo IA · SG-05 y pool de conexiones — estado de la base de datos.
//
//	GET /nexo/admin/database   platform.settings (superadmin)
//
// Devuelve el uso del pool de conexiones (database/sql: abiertas, en uso, en
// reposo y esperas), el TLS hacia la base (modo, si se verifica, si la
// conexión está cifrada, aviso) y datos de la base que importan para los
// requisitos (versión de Postgres, max_connections, versión de pgvector). No
// devuelve la cadena de conexión, el host ni el usuario. Se eligió una ruta de
// la API (y no la pantalla "Estado de la búsqueda") porque el estado de la
// base no es del conocimiento: la pantalla de búsqueda queda para el buscador.
//
// El monitor además deja una línea en el log cuando, en el último minuto,
// alguna petición tuvo que esperar una conexión libre (nexoMonitorPool).

import (
	"context"
	"log"
	"net/http"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
)

// nexoDBStatuser lo cumple *cloudstore.CloudStore (se detecta por type
// assertion, como nexoAuthzDB).
type nexoDBStatuser interface {
	NexoDBStatus(ctx context.Context) (cloudstore.NexoDBStatus, error)
	NexoPoolWaitCount() int64
}

func (s *CloudServer) registerNexoDBStatusRoutes(mux *http.ServeMux) {
	s.nexoRoute(mux, "GET", "/nexo/admin/database", "platform.settings", s.nexoAuthzRoute(s.handleNexoDBStatus))
}

func (s *CloudServer) nexoDBStatuser() nexoDBStatuser {
	for _, candidate := range []any{s.store, s.adminIdentity} {
		if p, ok := candidate.(nexoDBStatuser); ok && p != nil {
			return p
		}
	}
	return nil
}

func (s *CloudServer) handleNexoDBStatus(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	d, err := svc.Engine().Require(r.Context(), id, authz.PlatformScope(), authz.CapPlatformSettings, map[string]any{"route": r.Pattern})
	if err != nil {
		nexoAuthzWriteError(w, err)
		return
	}
	if !d.Allowed {
		nexoAuthzWriteError(w, &authz.DeniedError{Scope: authz.PlatformScope(), Capability: authz.CapPlatformSettings, Decision: d})
		return
	}
	st := s.nexoDBStatuser()
	if st == nil {
		nexoInternalError(w, "nexo database status", http.ErrNotSupported)
		return
	}
	status, err := st.NexoDBStatus(r.Context())
	if err != nil {
		nexoInternalError(w, "nexo database status", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonResponse(w, http.StatusOK, status)
}

// nexoMonitorPool avisa en el log si en el último minuto alguna petición
// esperó una conexión libre del pool (señal de que NEXO_DB_MAX_OPEN_CONNS es
// bajo para la carga). Lo llama el monitor en cada pasada.
func (s *CloudServer) nexoMonitorPool(m *nexoMonitor) {
	st := s.nexoDBStatuser()
	if st == nil {
		return
	}
	now := st.NexoPoolWaitCount()
	m.mu.Lock()
	prev := m.lastPoolWaits
	m.lastPoolWaits = now
	m.mu.Unlock()
	if now > prev {
		log.Printf("[nexo] base de datos: %d peticiones esperaron una conexion libre del pool en el ultimo minuto (total %d); si se repite, suba NEXO_DB_MAX_OPEN_CONNS sin pasar el max_connections de Postgres (GET /nexo/admin/database)", now-prev, now)
	}
}
