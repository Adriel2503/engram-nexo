package cloudserver

// Nexo IA · NX-10 — adaptador entre cloudserver y los espacios de
// conocimiento (internal/nexo/kb). Registra /nexo/kb/v1 con withAuth (401) y
// la identidad de la petición armada como en NX-05 (nexoWithAuthzIdentity:
// token asignado de NX-01, IP y User-Agent de NX-04). El módulo no importa
// cloudserver (design §4).

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/httpapi"
	kbservice "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

// nexoKBServices guarda un servicio por base (el servicio no tiene estado por
// petición; guarda el modo de búsqueda leído una vez del catálogo).
var nexoKBServices sync.Map // *sql.DB -> *kbservice.Service

func (s *CloudServer) nexoKBService(r *http.Request) (*kbservice.Service, error) {
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
	// OP-13: resolver fuera de toda transacción antes de entregar el servicio.
	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	svc := kbservice.New(db, roles)
	if err := svc.Initialize(ctx); err != nil {
		return nil, err
	}
	v, _ := nexoKBServices.LoadOrStore(db, svc)
	return v.(*kbservice.Service), nil
}

// registerNexoKBRoutes registra /nexo/kb/v1 (enganche // NX-10 en routes()).
func (s *CloudServer) registerNexoKBRoutes(mux *http.ServeMux) {
	httpapi.RegisterRoutes(s.nexo.routes.Registrar(mux), s.nexoKBService, func(next http.HandlerFunc) http.HandlerFunc {
		return s.withAuth(func(w http.ResponseWriter, r *http.Request) {
			r, _ = nexoWithAuthzIdentity(r)
			r = nexoWithKBPublicOrigin(r)
			next(w, r)
		})
	})
	s.registerNexoKBDashboard(mux) // entrega 2b: páginas del panel (nexo_kb_dashboard.go)
}

// nexoWithKBPublicOrigin pasa a los enlaces de /nexo/kb/v1 el esquema y el
// host públicos que informó un proxy confiable (X-Forwarded-Proto y
// X-Forwarded-Host, capturados por el middleware de NX-04 solo desde
// NEXO_TRUSTED_PROXIES). NEXO_KB_PUBLIC_URL, si está, sigue mandando.
func nexoWithKBPublicOrigin(r *http.Request) *http.Request {
	cc, ok := NexoClientContextFrom(r.Context())
	if !ok || (cc.ForwardedProto == "" && cc.ForwardedHost == "") {
		return r
	}
	return r.WithContext(httpapi.WithForwardedOrigin(r.Context(), cc.ForwardedProto, cc.ForwardedHost))
}

// nexoStartKBVectorWorker arranca el indexador de vectores de R2 en segundo
// plano (cola nexo_kb_index_jobs, design §20). Solo si hay proveedor de
// embeddings configurado (NEXO_KB_EMBEDDER) y NEXO_KB_VECTOR_WORKER no es
// off; sin pgvector, el indexador no hace nada. Lo llama
// nexoStartActivityRetention (sin un enganche nuevo en Start()).
func (s *CloudServer) nexoStartKBVectorWorker() {
	if s.nexoDB() == nil {
		return
	}
	svc, err := s.nexoKBService(nil)
	if err != nil || !svc.VectorEnabled() {
		return
	}
	go svc.RunVectorWorker(context.Background())
}
