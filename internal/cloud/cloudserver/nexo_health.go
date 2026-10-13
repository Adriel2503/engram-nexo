package cloudserver

// Nexo IA · NX-10 (operación) — disponibilidad real del servidor.
//
// GET /health de Engram responde siempre "ok" aunque la base no responda; su
// contrato no se cambia. GET /nexo/health/ready consulta la base Postgres (SELECT 1)
// (2 s como máximo) y responde 200 {"status":"ready"} o 503
// {"status":"unavailable"}. Sin autenticación y sin datos sensibles (ni el
// error ni la versión ni la base): sirve para el healthcheck de Docker, el
// balanceador o un monitor externo. Se registra desde registerNexoAdminRoutes
// (archivo Nexo), sin enganche nuevo en archivos de Engram.

import (
	"context"
	"database/sql"
	"net/http"
	"time"
)

const nexoReadyTimeout = 2 * time.Second

func (s *CloudServer) registerNexoHealthRoutes(mux *http.ServeMux) {
	s.nexoRoute(mux, "GET", "/nexo/health/ready", "public", s.handleNexoReady)
}

// nexoReadyDB devuelve la base del almacén (nil si el almacén no es Postgres).
func (s *CloudServer) nexoReadyDB() *sql.DB {
	for _, candidate := range []any{s.store, s.adminIdentity} {
		if p, ok := candidate.(nexoAuthzDB); ok && p != nil {
			if db := p.NexoDB(); db != nil {
				return db
			}
		}
	}
	return nil
}

func (s *CloudServer) handleNexoReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	db := s.nexoReadyDB()
	if db == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), nexoReadyTimeout)
	defer cancel()
	var one int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable"})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"status": "ready"})
}
