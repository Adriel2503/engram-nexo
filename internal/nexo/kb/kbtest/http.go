package kbtest

// Servidor HTTP de prueba con la API /nexo/kb/v1 real (httpapi) y una
// autenticación de prueba: el token es el id del principal. Sirve para
// probar el cliente, la copia local y las herramientas MCP (tanda 2) sin
// levantar cloudserver. SetDown simula un servidor caído (corta la conexión).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/httpapi"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/routes"
)

// Server es el servidor de prueba.
type Server struct {
	URL  string
	srv  *httptest.Server
	down atomic.Bool
}

// SetDown corta (true) o restablece (false) las conexiones.
func (s *Server) SetDown(v bool) { s.down.Store(v) }

// HTTPServer levanta la API con el servicio del entorno.
func (e *Env) HTTPServer() *Server {
	e.T.Helper()
	mux := http.NewServeMux()
	wrap := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if tok == "" || e.tokens[tok] == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error_code":"unauthorized","error":"invalid token"}`))
				return
			}
			id := e.ID(tok)
			id.UserAgent = r.UserAgent()
			next(w, r.WithContext(authz.WithIdentity(r.Context(), id)))
		}
	}
	httpapi.RegisterRoutes(new(routes.Registry).Registrar(mux), func(*http.Request) (*service.Service, error) { return e.KB, nil }, wrap)
	mux.HandleFunc("GET /nexo/authz/v1/me", wrap(func(w http.ResponseWriter, r *http.Request) {
		id, _ := authz.IdentityFrom(r.Context())
		me, err := e.Roles.Me(authz.WithRequestCache(r.Context()), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(me)
	}))
	s := &Server{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.down.Load() {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	s.URL = s.srv.URL
	e.T.Cleanup(s.srv.Close)
	return s
}
