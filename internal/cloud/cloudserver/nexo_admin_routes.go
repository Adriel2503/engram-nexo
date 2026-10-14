package cloudserver

// Nexo IA · NX-03 — auditoría consultable (API JSON de solo lectura).
//
//	GET /nexo/admin/audit                  cloud_auth_audit_log (Engram)
//	GET /nexo/admin/activity               nexo_activity_log
//	GET /nexo/admin/presence               nexo_actor_presence
//	GET /nexo/admin/history/{entityKey}    vista nexo_mutation_history
//
// Control de acceso igual que /admin/* de Engram: withAuth (401 sin token o
// con token inválido) + requireManagedAdmin (403 si no es un usuario con rol
// admin autenticado con token asignado; también para el token compartido y
// para el modo sin autenticación) (RF-30). Con NX-05, requireManagedAdmin
// exige platform.audit.read en estas rutas: también entra el auditor
// (nexo_authz_bridge.go).
//
// Cada consulta exitosa se registra en cloud_auth_audit_log como
// nexo.<recurso>.read, con el admin como actor, los filtros usados y la
// cantidad de filas devueltas (RF-36). Si la auditoría no se puede escribir,
// la consulta falla (500) y no se entregan datos: no hay lectura sin rastro.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/constants"
)

// Acciones de auditoría de las lecturas (RF-36).
const (
	nexoAuditActionAuditRead    = "nexo.audit.read"
	nexoAuditActionActivityRead = "nexo.activity.read"
	nexoAuditActionPresenceRead = "nexo.presence.read"
	nexoAuditActionHistoryRead  = "nexo.history.read"
)

// nexoTraceStore se detecta por type assertion (como nexoActivityRecorder)
// para no ampliar las interfaces de Engram.
type nexoTraceStore interface {
	ListAuthAudit(ctx context.Context, q cloudstore.NexoAuditQuery) ([]cloudstore.NexoAuditRecord, string, error)
	ListActivity(ctx context.Context, q cloudstore.NexoActivityQuery) ([]cloudstore.NexoActivityRecord, string, error)
	ListPresence(ctx context.Context, q cloudstore.NexoPresenceQuery) ([]cloudstore.NexoPresenceRecord, string, error)
	MutationHistory(ctx context.Context, q cloudstore.NexoHistoryQuery) ([]cloudstore.NexoHistoryRecord, string, error)
}

// nexoPage es la forma común de las respuestas paginadas. next_cursor es null
// cuando no hay más páginas.
type nexoPage[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
	Limit      int     `json:"limit"`
}

func newNexoPage[T any](items []T, next string, limit int) nexoPage[T] {
	if items == nil {
		items = []T{}
	}
	page := nexoPage[T]{Items: items, Limit: limit}
	if next != "" {
		page.NextCursor = &next
	}
	return page
}

// registerNexoAdminRoutes registra las rutas de NX-03. Se llama una vez desde
// routes() (// NX-03).
func (s *CloudServer) registerNexoAdminRoutes(mux *http.ServeMux) {
	s.nexoRoute(mux, "GET", "/nexo/admin/audit", "platform.audit.read", s.withAuth(s.handleNexoAdminAudit))
	s.nexoRoute(mux, "GET", "/nexo/admin/activity", "platform.audit.read", s.withAuth(s.handleNexoAdminActivity))
	s.nexoRoute(mux, "GET", "/nexo/admin/presence", "platform.audit.read", s.withAuth(s.handleNexoAdminPresence))
	s.nexoRoute(mux, "GET", "/nexo/admin/history/{entityKey}", "platform.audit.read", s.withAuth(s.handleNexoAdminHistory))
	// NX-05: eventos de roles (también en /nexo/authz/v1/events) y API de roles.
	s.nexoRoute(mux, "GET", "/nexo/admin/authz-events", "platform.audit.read", s.withAuth(s.handleNexoAuthzEvents))
	s.registerNexoAuthzRoutes(mux)
	s.registerNexoEvidenceRoutes(mux) // NX-07 S4: verificación de la cadena, alertas y revisión de accesos
	s.registerNexoAuditDashboard(mux) // SG-07: pantallas del panel para el auditor (nexo_audit_dashboard.go)
	s.registerNexoHealthRoutes(mux)   // NX-10: /nexo/health/ready (disponibilidad con la base; sin autenticación)
	s.registerNexoDBStatusRoutes(mux) // SG-05: GET /nexo/admin/database (pool y TLS hacia la base; nexo_db_status.go)
}

// ─── Handlers ────────────────────────────────────────────────────────────────

func (s *CloudServer) handleNexoAdminAudit(w http.ResponseWriter, r *http.Request) {
	actor, audit, store, ok := s.nexoAdminPrelude(w, r)
	if !ok {
		return
	}
	p := nexoParams{values: r.URL.Query()}
	q := cloudstore.NexoAuditQuery{
		From:    p.time("from", false),
		To:      p.time("to", true),
		Action:  p.str("action"),
		Actor:   p.str("actor"),
		Outcome: p.str("outcome"),
		Cursor:  p.str("cursor"),
		Limit:   p.limit(),
	}
	if p.fail(w) {
		return
	}
	items, next, err := store.ListAuthAudit(r.Context(), q)
	if nexoWriteQueryError(w, err) {
		return
	}
	if !s.nexoFilterSecurityAudit(w, r, items) {
		return
	}
	limit := cloudstore.NexoNormalizeLimit(q.Limit)
	if !s.nexoAuditRead(w, r, audit, actor, nexoAuditActionAuditRead, "", p.filters(), len(items)) {
		return
	}
	jsonResponse(w, http.StatusOK, newNexoPage(items, next, limit))
}

func (s *CloudServer) handleNexoAdminActivity(w http.ResponseWriter, r *http.Request) {
	actor, audit, store, ok := s.nexoAdminPrelude(w, r)
	if !ok {
		return
	}
	p := nexoParams{values: r.URL.Query()}
	q := cloudstore.NexoActivityQuery{
		From:      p.time("from", false),
		To:        p.time("to", true),
		Actor:     p.str("actor"),
		Token:     p.str("token"),
		Project:   p.str("project"),
		Operation: p.str("operation"),
		Cursor:    p.str("cursor"),
		Limit:     p.limit(),
	}
	if q.Operation != "" && !slices.Contains(cloudstore.NexoActivityOperations, q.Operation) {
		p.errs = append(p.errs, fmt.Sprintf("operation must be one of %s", strings.Join(cloudstore.NexoActivityOperations, ", ")))
	}
	if p.fail(w) {
		return
	}
	items, next, err := store.ListActivity(r.Context(), q)
	if nexoWriteQueryError(w, err) {
		return
	}
	if !s.nexoAuditRead(w, r, audit, actor, nexoAuditActionActivityRead, q.Project, p.filters(), len(items)) {
		return
	}
	jsonResponse(w, http.StatusOK, newNexoPage(items, next, cloudstore.NexoNormalizeLimit(q.Limit)))
}

func (s *CloudServer) handleNexoAdminPresence(w http.ResponseWriter, r *http.Request) {
	actor, audit, store, ok := s.nexoAdminPrelude(w, r)
	if !ok {
		return
	}
	p := nexoParams{values: r.URL.Query()}
	q := cloudstore.NexoPresenceQuery{
		Actor:  p.str("actor"),
		Since:  p.time("since", false),
		Cursor: p.str("cursor"),
		Limit:  p.limit(),
	}
	if p.fail(w) {
		return
	}
	items, next, err := store.ListPresence(r.Context(), q)
	if nexoWriteQueryError(w, err) {
		return
	}
	if !s.nexoAuditRead(w, r, audit, actor, nexoAuditActionPresenceRead, "", p.filters(), len(items)) {
		return
	}
	jsonResponse(w, http.StatusOK, newNexoPage(items, next, cloudstore.NexoNormalizeLimit(q.Limit)))
}

func (s *CloudServer) handleNexoAdminHistory(w http.ResponseWriter, r *http.Request) {
	actor, audit, store, ok := s.nexoAdminPrelude(w, r)
	if !ok {
		return
	}
	entityKey := strings.TrimSpace(r.PathValue("entityKey"))
	p := nexoParams{values: r.URL.Query()}
	q := cloudstore.NexoHistoryQuery{
		EntityKey:      entityKey,
		Project:        p.str("project"),
		Entity:         p.str("entity"),
		IncludePayload: p.boolean("include_payload"),
		Cursor:         p.str("cursor"),
		Limit:          p.limit(),
	}
	if entityKey == "" {
		p.errs = append(p.errs, "entity key is required")
	}
	if p.fail(w) {
		return
	}
	items, next, err := store.MutationHistory(r.Context(), q)
	if nexoWriteQueryError(w, err) {
		return
	}
	if !s.nexoFilterHistory(w, r, items) {
		return
	}
	filters := p.filters()
	filters["entity_key"] = entityKey
	if !s.nexoAuditRead(w, r, audit, actor, nexoAuditActionHistoryRead, q.Project, filters, len(items)) {
		return
	}
	jsonResponse(w, http.StatusOK, newNexoPage(items, next, cloudstore.NexoNormalizeLimit(q.Limit)))
}

// ─── Comunes ─────────────────────────────────────────────────────────────────

// nexoAdminPrelude verifica el admin (403), el almacén de auditoría y el de
// consultas (500 si faltan).
func (s *CloudServer) nexoAdminPrelude(w http.ResponseWriter, r *http.Request) (cloudauth.Principal, AdminIdentityStore, nexoTraceStore, bool) {
	actor, ok := s.requireManagedAdmin(w, r)
	if !ok {
		return cloudauth.Principal{}, nil, nil, false
	}
	audit, ok := s.adminStore(w)
	if !ok {
		return cloudauth.Principal{}, nil, nil, false
	}
	store, ok := s.nexoTraceStore()
	if !ok {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, "nexo trace store is not configured")
		return cloudauth.Principal{}, nil, nil, false
	}
	return actor, audit, store, true
}

func (s *CloudServer) nexoTraceStore() (nexoTraceStore, bool) {
	if store, ok := s.store.(nexoTraceStore); ok && store != nil {
		return store, true
	}
	if store, ok := s.adminIdentity.(nexoTraceStore); ok && store != nil {
		return store, true
	}
	return nil, false
}

// nexoAuditRead registra la lectura (RF-36). Devuelve false (y responde 500)
// si no se pudo registrar.
func (s *CloudServer) nexoAuditRead(w http.ResponseWriter, r *http.Request, store AdminIdentityStore, actor cloudauth.Principal, action, project string, filters map[string]any, count int) bool {
	metadata := map[string]any{"filters": filters, "result_count": count}
	if err := s.recordAdminAudit(r.Context(), store, actor, action, "", project, metadata); err != nil {
		writeAuditFailure(w, err)
		return false
	}
	return true
}

func nexoWriteQueryError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, cloudstore.ErrNexoInvalidCursor) {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "invalid cursor")
		return true
	}
	nexoInternalError(w, "nexo query", err)
	return true
}

// nexoParams lee y valida los parámetros de consulta, acumulando errores.
type nexoParams struct {
	values map[string][]string
	errs   []string
	used   map[string]any
}

func (p *nexoParams) raw(name string) string {
	if vs := p.values[name]; len(vs) > 0 {
		return strings.TrimSpace(vs[0])
	}
	return ""
}

func (p *nexoParams) remember(name string, value any) {
	if p.used == nil {
		p.used = map[string]any{}
	}
	p.used[name] = value
}

func (p *nexoParams) str(name string) string {
	v := p.raw(name)
	if len(v) > 256 {
		p.errs = append(p.errs, fmt.Sprintf("%s is too long", name))
		return ""
	}
	if v != "" {
		p.remember(name, v)
	}
	return v
}

// time acepta RFC 3339 o una fecha AAAA-MM-DD (UTC). Con endOfDay, una fecha
// sin hora cubre el día completo (límite exclusivo al día siguiente).
func (p *nexoParams) time(name string, endOfDay bool) time.Time {
	v := p.raw(name)
	if v == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		p.remember(name, v)
		return t
	}
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		p.remember(name, v)
		if endOfDay {
			return t.AddDate(0, 0, 1)
		}
		return t
	}
	p.errs = append(p.errs, fmt.Sprintf("%s must be RFC 3339 or YYYY-MM-DD", name))
	return time.Time{}
}

func (p *nexoParams) limit() int {
	v := p.raw("limit")
	if v == "" {
		return cloudstore.NexoQueryDefaultLimit
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		p.errs = append(p.errs, "limit must be a positive integer")
		return 0
	}
	n = cloudstore.NexoNormalizeLimit(n)
	p.remember("limit", n)
	return n
}

func (p *nexoParams) boolean(name string) bool {
	v := p.raw(name)
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Sprintf("%s must be true or false", name))
		return false
	}
	p.remember(name, b)
	return b
}

// fail responde 400 si hubo errores de validación.
func (p *nexoParams) fail(w http.ResponseWriter) bool {
	if len(p.errs) == 0 {
		return false
	}
	writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "invalid query: "+strings.Join(p.errs, "; "))
	return true
}

// filters devuelve los filtros usados, para la auditoría de la lectura.
func (p *nexoParams) filters() map[string]any {
	out := make(map[string]any, len(p.used))
	for k, v := range p.used {
		out[k] = v
	}
	return out
}
