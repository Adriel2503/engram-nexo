package cloudserver

// Nexo IA · SG-07 — pantallas del panel para el auditor (docs/nexo/panel/design.md §9).
//
//	GET  /dashboard/admin/audit[?src=roles&...]               registro (platform.audit.read)
//	GET  /dashboard/admin/audit/activity                       actividad y presencia (platform.audit.read)
//	GET  /dashboard/admin/audit/history?key=                   historial de un elemento (platform.audit.read)
//	GET  /dashboard/admin/audit/integrity[?run=1&table=]       verificación de la cadena (platform.audit.read)
//	GET  /dashboard/admin/audit/alerts                         alertas (platform.audit.read)
//	POST /dashboard/admin/audit/alerts/{id}/ack                reconocer (platform.audit.read + token.write)
//	GET  /dashboard/admin/audit/access-review                  revisión de accesos (platform.audit.read)
//	GET  /dashboard/admin/audit/access-review/export?format=   descarga CSV o JSON con huella (platform.audit.read)
//	POST /dashboard/admin/audit/access-review/complete         registrar la revisión (platform.audit.read + token.write)
//
// Reutilizan los mismos servicios y funciones que la API JSON de
// nexo_admin_routes.go y nexo_evidence.go (cloudstore, authz, evidence); no
// llaman a su propia API HTTP ni repiten la lógica. La identidad sale del
// token de la sesión del panel, la sesión hereda su alcance (un token read
// puede leer, no reconocer ni registrar) y los POST llevan el token CSRF del
// panel de conocimiento. Cada lectura queda en cloud_auth_audit_log con la
// misma acción que la API (nexo.audit.read, nexo.alerts.read, ...) y, si no
// se puede registrar, no se entregan datos. Los errores internos pasan por
// nexoInternalError: nunca se muestra texto de la base de datos. El detalle
// de un evento solo muestra lo que la API ya expone, filtrado otra vez al
// dibujarlo (auditPanelDetails).

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/dashboard"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

// nexoAuditPanelPageSize: filas por página en las pantallas (la API admite
// hasta 200).
const nexoAuditPanelPageSize = 50

func (s *CloudServer) registerNexoAuditDashboard(mux *http.ServeMux) {
	const read = "platform.audit.read"
	const write = "platform.audit.read+token.write"
	s.nexoRoute(mux, "GET", dashboard.NexoAuditPathLog, read, s.nexoAuditPanel(false, s.auditPanelLog))
	s.nexoRoute(mux, "GET", dashboard.NexoAuditPathActivity, read, s.nexoAuditPanel(false, s.auditPanelActivity))
	s.nexoRoute(mux, "GET", dashboard.NexoAuditPathHistory, read, s.nexoAuditPanel(false, s.auditPanelHistory))
	s.nexoRoute(mux, "GET", dashboard.NexoAuditPathIntegrity, read, s.nexoAuditPanel(false, s.auditPanelIntegrity))
	s.nexoRoute(mux, "GET", dashboard.NexoAuditPathAlerts, read, s.nexoAuditPanel(false, s.auditPanelAlerts))
	s.nexoRoute(mux, "POST", dashboard.NexoAuditPathAlerts+"/{id}/ack", write, s.nexoAuditPanel(true, s.auditPanelAlertAck))
	s.nexoRoute(mux, "GET", dashboard.NexoAuditPathReview, read, s.nexoAuditPanel(false, s.auditPanelReview))
	s.nexoRoute(mux, "GET", dashboard.NexoAuditPathReview+"/export", read, s.nexoAuditPanel(false, s.auditPanelReviewExport))
	s.nexoRoute(mux, "POST", dashboard.NexoAuditPathReview+"/complete", write, s.nexoAuditPanel(true, s.auditPanelReviewComplete))
}

type auditPanelHandler func(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity)

// nexoAuditPanel arma la identidad desde la sesión, exige platform.audit.read
// (y, con write, una sesión que pueda escribir) y comprueba CSRF en los POST.
// Negar por defecto: sin sesión administrada o sin la capacidad, 403.
func (s *CloudServer) nexoAuditPanel(write bool, fn auditPanelHandler) http.HandlerFunc {
	return s.requireDashboardSession(func(w http.ResponseWriter, r *http.Request) {
		svc, ok := s.nexoAuthz()
		if !ok || s.nexoDB() == nil {
			s.auditPanelDenied(w, r, http.StatusServiceUnavailable, "La auditoría no está disponible en este servidor.")
			return
		}
		principal, ok := s.dashboardPrincipalFromRequest(r)
		if !ok {
			s.auditPanelDenied(w, r, http.StatusForbidden, auditPanelNoRole)
			return
		}
		r = r.WithContext(WithPrincipal(r.Context(), principal))
		r, id := nexoWithAuthzIdentity(r)
		id.TokenScope = s.nexoEffectiveTokenScope(r.Context(), principal)
		r = r.WithContext(authz.WithIdentity(r.Context(), id))
		if !id.Managed() {
			s.auditPanelDenied(w, r, http.StatusForbidden, auditPanelNoRole)
			return
		}
		d, err := svc.Engine().Require(r.Context(), id, authz.PlatformScope(), authz.CapPlatformAuditRead, map[string]any{"route": r.Pattern})
		if err != nil {
			nexoInternalError(w, "nexo audit panel", err)
			return
		}
		if !d.Allowed {
			s.auditPanelDenied(w, r, http.StatusForbidden, auditPanelNoRole)
			return
		}
		if write && id.TokenScope == authz.TokenScopeRead {
			svc.Engine().RecordDenied(r.Context(), id, authz.PlatformScope(), authz.CapPlatformAuditRead,
				authz.Decision{Role: d.Role, Reason: nexoCodeTokenScopeRead}, map[string]any{"route": r.Pattern, "rule": nexoCodeTokenScopeRead})
			s.auditPanelDenied(w, r, http.StatusForbidden, "Esta sesión entró con un token de solo lectura y no puede cambiar nada. Entra con un token de escritura para esta acción.")
			return
		}
		if r.Method == http.MethodPost && !s.kbCSRFOK(r) {
			s.auditPanelDenied(w, r, http.StatusForbidden, "El formulario venció o no es válido. Vuelve a abrir la página y repite la acción.")
			return
		}
		r = r.WithContext(dashboard.WithNexoAuditNav(r.Context()))
		fn(w, r, svc, id)
	})
}

const auditPanelNoRole = "Esta sección es para el auditor, el administrador y el superadmin de la plataforma. Si la necesitas, pide el rol de auditor a un superadmin."

func (s *CloudServer) auditPanelDenied(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.auditPanelWrite(w, r, status, "Auditoría", dashboard.NexoAuditForbiddenPage(dashboard.NexoAuditForbidden{Message: msg}))
}

func (s *CloudServer) auditPanelWrite(w http.ResponseWriter, r *http.Request, status int, title string, body templ.Component) {
	layout := dashboard.Layout(title, s.dashboardDisplayName(r), "audit", s.isDashboardAdmin(r), body)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
	w.WriteHeader(status)
	if err := layout.Render(r.Context(), w); err != nil {
		fmt.Printf("dashboard audit: render: %v\n", err)
	}
}

// auditPanelRecord deja la lectura o acción en cloud_auth_audit_log con el
// actor (como nexoAuditRead y nexoRecordAuditAction). false (y 500) si no se
// pudo registrar.
func (s *CloudServer) auditPanelRecord(w http.ResponseWriter, r *http.Request, action, project string, metadata map[string]any) bool {
	store := s.adminIdentity
	if store == nil {
		nexoInternalError(w, "nexo audit panel", fmt.Errorf("admin identity store is not configured"))
		return false
	}
	principal, _ := PrincipalFromContext(r.Context())
	if err := s.recordAdminAudit(r.Context(), store, principal, action, "", project, metadata); err != nil {
		nexoInternalError(w, "nexo audit panel", err)
		return false
	}
	return true
}

// ─── Formato ─────────────────────────────────────────────────────────────────

func auditTime(t time.Time) (text, iso string) {
	t = t.UTC()
	return t.Format("2006-01-02 15:04:05"), t.Format(time.RFC3339)
}

// auditDate: AAAA-MM-DD.
func auditDate(t time.Time) string { return t.UTC().Format("2006-01-02") }

// auditAgo dice cuánto hace de t en palabras sencillas.
func auditAgo(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "hace un momento"
	case d < time.Hour:
		return "hace " + strconv.Itoa(int(d.Minutes())) + " min"
	case d < 48*time.Hour:
		return "hace " + strconv.Itoa(int(d.Hours())) + " h"
	}
	return "hace " + saPanelDays(int(d.Hours()/24))
}

func auditFirst(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func auditDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

func auditTruncate(v string, n int) string {
	r := []rune(v)
	if len(r) <= n {
		return v
	}
	return string(r[:n]) + "..."
}

// auditQuery copia solo las claves pedidas con valor.
func auditQuery(q url.Values, keys ...string) url.Values {
	out := url.Values{}
	for _, k := range keys {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			out.Set(k, v)
		}
	}
	return out
}

func auditURL(path string, v url.Values, extra ...string) string {
	c := url.Values{}
	for k, vs := range v {
		c[k] = append([]string(nil), vs...)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		if extra[i+1] == "" {
			c.Del(extra[i])
			continue
		}
		c.Set(extra[i], extra[i+1])
	}
	if len(c) == 0 {
		return path
	}
	return path + "?" + c.Encode()
}

func auditOptions(selected string, pairs ...string) []dashboard.NexoOption {
	out := make([]dashboard.NexoOption, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, dashboard.NexoOption{Value: pairs[i], Label: pairs[i+1], Selected: pairs[i] == selected})
	}
	return out
}

const auditBadFilters = "Algún filtro no es válido. Las fechas van como AAAA-MM-DD y los textos no pasan de 256 caracteres."

// ─── Glosas de acciones ──────────────────────────────────────────────────────

var auditActionGloss = map[string]string{
	"user.create":                          "Alta de persona",
	"user.enable":                          "Persona habilitada",
	"user.disable":                         "Persona deshabilitada",
	"token.create":                         "Token emitido",
	"token.revoke":                         "Token revocado",
	"grant.create":                         "Permiso de proyecto dado",
	"grant.revoke":                         "Permiso de proyecto quitado",
	"dashboard.login":                      "Ingreso al panel",
	"bootstrap.dashboard":                  "Primer ingreso del administrador",
	"bootstrap.cli":                        "Recuperación del administrador por la CLI",
	"sync.auth":                            "Autenticación de la sincronización",
	"sync.authorize":                       "Autorización de proyecto en la sincronización",
	"mutation_push":                        "Subida de cambios",
	"chunk_push":                           "Subida de un bloque",
	"nexo.rate_limited":                    "Límite de peticiones alcanzado",
	"auth.lockout":                         "Bloqueo por intentos fallidos",
	"dashboard.authorize":                  "Acción del panel rechazada por un token de lectura",
	"nexo.audit.read":                      "Consulta del registro de auditoría",
	"nexo.activity.read":                   "Consulta de la actividad",
	"nexo.presence.read":                   "Consulta de la presencia",
	"nexo.history.read":                    "Consulta del historial de un elemento",
	"nexo.authz_events.read":               "Consulta de los eventos de roles",
	"nexo.audit_verify.read":               "Verificación de la integridad",
	"nexo.alerts.read":                     "Consulta de las alertas",
	"nexo.access_review.read":              "Consulta de la revisión de accesos",
	"nexo.access_review.export":            "Descarga de la revisión de accesos",
	"nexo.alert.ack":                       "Alerta reconocida",
	"nexo.access_review.complete":          "Revisión de accesos registrada",
	authz.ActionBindingGranted:             "Rol asignado",
	authz.ActionBindingChanged:             "Rol cambiado",
	authz.ActionBindingRevoked:             "Rol quitado",
	authz.ActionScopeCreated:               "Espacio o proyecto creado",
	authz.ActionScopeUpdated:               "Espacio o proyecto actualizado",
	authz.ActionScopeArchived:              "Espacio o proyecto archivado",
	authz.ActionScopeRestored:              "Espacio o proyecto restaurado",
	authz.ActionScopeRecovered:             "Responsable repuesto",
	authz.ActionDenied:                     "Acción denegada por falta de permiso",
	authz.ActionRetentionPurged:            "Depuración por retención",
	authz.ActionPrincipalDisabledInactive:  "Cuenta deshabilitada por inactividad",
	authz.ActionServiceAccountCreated:      "Cuenta de servicio creada",
	authz.ActionServiceAccountUpdated:      "Cuenta de servicio actualizada",
	authz.ActionServiceAccountDisabled:     "Cuenta de servicio deshabilitada",
	authz.ActionServiceAccountEnabled:      "Cuenta de servicio habilitada",
	authz.ActionServiceAccountTokenIssued:  "Token de cuenta de servicio emitido",
	authz.ActionServiceAccountTokenRotated: "Token de cuenta de servicio rotado",
	authz.ActionServiceAccountTokenRevoked: "Token de cuenta de servicio revocado",
}

func auditSecurityActions() []string {
	roles := map[string]bool{}
	for _, a := range authz.EventActions {
		roles[a] = true
	}
	out := make([]string, 0, len(auditActionGloss))
	for a := range auditActionGloss {
		if !roles[a] {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return append(out, "nexo.*", "token.*", "user.*", "grant.*")
}

func auditRolesActions() []string {
	out := append([]string(nil), authz.EventActions...)
	sort.Strings(out)
	return append(out, "binding.*", "scope.*", "service_account.*")
}

// ─── Detalle de un evento: solo lo que la API ya expone, filtrado otra vez ───

var (
	auditForbiddenKeys = []string{
		"token_hash", "tokenhash", "password", "passwd", "secret", "pepper", "verifier", "cookie",
		"authorization", "api_key", "apikey", "private_key", "raw_token", "session_token", "hash",
	}
	auditFullTokenRe = regexp.MustCompile(`egc_[a-z0-9-]+_[0-9a-f]{8}_[A-Za-z0-9-]{20,}`)
	auditAuthzHdrRe  = regexp.MustCompile(`(?i)authorization["']?\s*[:=]\s*["']?(bearer|basic)\s+\S+`)
	auditBearerRe    = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`)
)

func auditSecretKey(key string) bool {
	lower := strings.ToLower(key)
	if lower == "token" || strings.HasSuffix(lower, "_token") || lower == "raw" {
		return true
	}
	for _, f := range auditForbiddenKeys {
		if strings.Contains(lower, f) {
			return true
		}
	}
	return false
}

func auditSecretValue(v string) bool {
	return strings.Contains(v, "hmac-sha256:v1:") || auditFullTokenRe.MatchString(v) || auditAuthzHdrRe.MatchString(v) || auditBearerRe.MatchString(v)
}

// auditPanelDetails convierte la metadata de un evento en pares legibles. Se
// omiten las claves con nombre de secreto y los valores con forma de token,
// hash de token o cabecera Authorization.
func auditPanelDetails(meta map[string]any, skip ...string) []dashboard.NexoKV {
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []dashboard.NexoKV
	for _, k := range keys {
		if auditSecretKey(k) || containsString(skip, k) {
			continue
		}
		if v, ok := auditSafeValue(meta[k], 0); ok {
			out = append(out, dashboard.NexoKV{Key: k, Value: auditTruncate(v, 300)})
		}
	}
	return out
}

func auditSafeValue(v any, depth int) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		if x == "" || auditSecretValue(x) {
			return "", false
		}
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case int, int64, uint64, int32:
		return fmt.Sprint(x), true
	case []any:
		if depth > 2 {
			return "", false
		}
		parts := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := auditSafeValue(e, depth+1); ok {
				parts = append(parts, s)
			}
		}
		if len(parts) == 0 {
			return "", false
		}
		return strings.Join(parts, ", "), true
	case map[string]any:
		if depth > 2 {
			return "", false
		}
		kv := auditPanelDetails(x)
		if len(kv) == 0 {
			return "", false
		}
		parts := make([]string, 0, len(kv))
		for _, e := range kv {
			parts = append(parts, e.Key+"="+e.Value)
		}
		return strings.Join(parts, "; "), true
	}
	return "", false
}

// ─── Registro ────────────────────────────────────────────────────────────────

func (s *CloudServer) auditPanelLog(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	q := r.URL.Query()
	src := "seguridad"
	if q.Get("src") == "roles" {
		src = "roles"
	}
	p := nexoParams{values: q}
	from, to := p.time("from", false), p.time("to", true)
	actor, action := p.str("actor"), p.str("action")
	cursor := p.str("cursor")
	p.remember("limit", nexoAuditPanelPageSize)

	page := dashboard.NexoAuditLogView{
		Source: src, From: strings.TrimSpace(q.Get("from")), To: strings.TrimSpace(q.Get("to")), Actor: actor, Action: action,
		Filtered: false,
	}
	keep := []string{"actor", "action", "from", "to"}
	page.ActorHint = "usuario, correo, nombre o id"
	if src == "roles" {
		page.ActorHint = "usuario o id; incluye a quien recibió el cambio"
		keep = append(keep, "scope_type", "scope_key")
	} else {
		keep = append(keep, "outcome", "project")
	}
	base := auditQuery(q, keep...)
	if src == "roles" {
		base.Set("src", "roles")
	}
	page.Filtered = len(auditQuery(q, keep...)) > 0
	page.ClearURL = dashboard.NexoAuditPathLog
	if src == "roles" {
		page.ClearURL = dashboard.NexoAuditPathLog + "?src=roles"
	}
	page.SourceLinks = []dashboard.NexoAuditSourceLink{
		{Label: "Seguridad", URL: dashboard.NexoAuditPathLog, Current: src == "seguridad"},
		{Label: "Roles y permisos", URL: dashboard.NexoAuditPathLog + "?src=roles", Current: src == "roles"},
	}

	renderErr := func(status int, msg string) {
		page.Error = msg
		s.auditPanelWrite(w, r, status, "Registro de auditoría", dashboard.NexoAuditLogPage(page))
	}
	if src == "roles" {
		page.KnownAction = auditRolesActions()
		scopeType, scopeKey := p.str("scope_type"), p.str("scope_key")
		page.ScopeKey = scopeKey
		page.ScopeTypes = auditOptions(scopeType, "", "Todos", "platform", "Plataforma", "project", "Proyecto", "space", "Espacio")
		if scopeType != "" && !authz.ScopeType(scopeType).Valid() {
			p.errs = append(p.errs, "scope_type")
		}
		if a := action; a != "" && !strings.HasSuffix(a, "*") && !containsString(authz.EventActions, a) {
			p.errs = append(p.errs, "action")
		}
		if len(p.errs) > 0 {
			renderErr(http.StatusBadRequest, auditBadFilters)
			return
		}
		items, next, err := svc.ListEvents(r.Context(), id, authz.EventQuery{
			ScopeType: scopeType, ScopeKey: scopeKey, Action: action, Principal: actor, From: from, To: to, Cursor: cursor, Limit: nexoAuditPanelPageSize,
		})
		if err != nil {
			s.auditPanelQueryError(w, r, err)
			return
		}
		if !s.auditPanelRecord(w, r, nexoAuditActionAuthzEventsRead, "", map[string]any{"filters": p.filters(), "result_count": len(items)}) {
			return
		}
		for _, e := range items {
			page.Rows = append(page.Rows, auditRolesRow(e))
		}
		page.NextURL, page.FirstURL = auditPageLinks(dashboard.NexoAuditPathLog, base, "cursor", next, cursor)
		s.auditPanelWrite(w, r, http.StatusOK, "Registro de auditoría", dashboard.NexoAuditLogPage(page))
		return
	}

	page.KnownAction = auditSecurityActions()
	outcome, project := p.str("outcome"), p.str("project")
	page.Scope = project
	page.Outcomes = auditOptions(outcome, "", "Todos", "success", "Correcto", "denied", "Denegado")
	if len(p.errs) > 0 {
		renderErr(http.StatusBadRequest, auditBadFilters)
		return
	}
	store, ok := s.nexoTraceStore()
	if !ok {
		nexoInternalError(w, "nexo audit panel", fmt.Errorf("nexo trace store is not configured"))
		return
	}
	items, next, err := store.ListAuthAudit(r.Context(), cloudstore.NexoAuditQuery{
		From: from, To: to, Action: action, Actor: actor, Outcome: outcome, Project: project, Cursor: cursor, Limit: nexoAuditPanelPageSize,
	})
	if err != nil {
		s.auditPanelQueryError(w, r, err)
		return
	}
	if !s.auditPanelRecord(w, r, nexoAuditActionAuditRead, "", map[string]any{"filters": p.filters(), "result_count": len(items)}) {
		return
	}
	for _, e := range items {
		page.Rows = append(page.Rows, auditSecurityRow(e))
	}
	page.NextURL, page.FirstURL = auditPageLinks(dashboard.NexoAuditPathLog, base, "cursor", next, cursor)
	s.auditPanelWrite(w, r, http.StatusOK, "Registro de auditoría", dashboard.NexoAuditLogPage(page))
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// auditPageLinks devuelve "ver más antiguos" y "volver al inicio".
func auditPageLinks(path string, base url.Values, cursorKey, next, current string) (nextURL, firstURL string) {
	if next != "" {
		nextURL = auditURL(path, base, cursorKey, next)
	}
	if current != "" {
		firstURL = auditURL(path, base)
	}
	return nextURL, firstURL
}

func (s *CloudServer) auditPanelQueryError(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, cloudstore.ErrNexoInvalidCursor) || errors.Is(err, authz.ErrInvalidCursor) || errors.Is(err, evidence.ErrInvalidCursor) {
		s.auditPanelWrite(w, r, http.StatusBadRequest, "Auditoría", dashboard.NexoAuditForbiddenPage(dashboard.NexoAuditForbidden{Message: "El enlace a la página siguiente no es válido. Vuelve al inicio de la lista."}))
		return
	}
	var denied *authz.DeniedError
	if errors.As(err, &denied) {
		s.auditPanelDenied(w, r, http.StatusForbidden, auditPanelNoRole)
		return
	}
	nexoInternalError(w, "nexo audit panel", err)
}

func auditSecurityRow(e cloudstore.NexoAuditRecord) dashboard.NexoAuditRow {
	when, iso := auditTime(e.OccurredAt)
	actor := auditFirst(e.ActorDisplayName, e.ActorUsername, e.ActorEmail, e.ActorSource, "sistema")
	row := dashboard.NexoAuditRow{
		When: when, WhenISO: iso,
		Actor: actor, Action: e.Action, Gloss: auditActionGloss[e.Action],
		Scope: auditDash(e.Project), Origin: auditDash(e.ClientIP), OriginSub: auditTruncate(e.UserAgent, 120),
	}
	if e.ActorUsername != "" && e.ActorUsername != actor {
		row.ActorSub = e.ActorUsername
	} else if e.ActorSource != "" && e.ActorSource != actor {
		row.ActorSub = e.ActorSource
	}
	switch e.Outcome {
	case "success":
		row.Outcome, row.OutcomeClass = "Correcto", "nx-chip nx-chip-ok"
	case "denied":
		row.Outcome, row.OutcomeClass = "Denegado", "nx-chip nx-chip-err"
	default:
		row.Outcome, row.OutcomeClass = auditDash(e.Outcome), "nx-chip nx-chip-ret"
	}
	row.Details = append(row.Details, dashboard.NexoKV{Key: "id", Value: e.ID})
	if t := auditFirst(e.TargetUsername, e.TargetPrincipalID); t != "" {
		row.Details = append(row.Details, dashboard.NexoKV{Key: "afectado", Value: t})
	}
	if e.ReasonCode != "" {
		row.Details = append(row.Details, dashboard.NexoKV{Key: "motivo", Value: e.ReasonCode})
	}
	row.Details = append(row.Details, auditPanelDetails(e.Metadata, cloudstore.NexoAuditMetaClientIP, cloudstore.NexoAuditMetaUserAgent)...)
	row.HistoryURL = auditHistoryLink(e.Metadata)
	return row
}

// auditHistoryLink: la consulta de un historial deja la clave en sus filtros.
func auditHistoryLink(meta map[string]any) string {
	if f, ok := meta["filters"].(map[string]any); ok {
		if k, ok := f["entity_key"].(string); ok && strings.TrimSpace(k) != "" && !auditSecretValue(k) {
			return auditURL(dashboard.NexoAuditPathHistory, url.Values{"key": {k}})
		}
	}
	for _, name := range []string{"entity_key", "sync_id"} {
		if k, ok := meta[name].(string); ok && strings.TrimSpace(k) != "" && !auditSecretValue(k) {
			return auditURL(dashboard.NexoAuditPathHistory, url.Values{"key": {k}})
		}
	}
	return ""
}

func auditRolesRow(e authz.EventRecord) dashboard.NexoAuditRow {
	when, iso := auditTime(e.OccurredAt)
	row := dashboard.NexoAuditRow{
		When: when, WhenISO: iso,
		Actor: auditFirst(e.Actor, "sistema"), Action: e.Action, Gloss: auditActionGloss[e.Action],
		Scope: e.ScopeType, Origin: auditDash(e.ClientIP), OriginSub: auditTruncate(e.UserAgent, 120),
	}
	switch {
	case e.ActorEmail != "":
		row.ActorSub = e.ActorEmail
	case !e.ActorVerified && e.Actor != authz.SystemActorLabel:
		row.ActorSub = "identidad no verificada"
	}
	if e.ScopeKey != "" && e.ScopeKey != "*" {
		row.Scope = e.ScopeType + ": " + e.ScopeKey
	} else if e.ScopeType == "platform" {
		row.Scope = "plataforma"
	}
	if e.Action == authz.ActionDenied {
		row.Outcome, row.OutcomeClass = "Denegado", "nx-chip nx-chip-err"
	} else {
		row.Outcome, row.OutcomeClass = "Aplicado", "nx-chip nx-chip-ok"
	}
	row.Details = append(row.Details, dashboard.NexoKV{Key: "id", Value: e.ID})
	if t := auditFirst(e.TargetUsername, e.TargetPrincipalID); t != "" {
		row.Details = append(row.Details, dashboard.NexoKV{Key: "afectado", Value: t})
	}
	if e.RoleBefore != "" || e.RoleAfter != "" {
		row.Details = append(row.Details, dashboard.NexoKV{Key: "rol", Value: auditDash(e.RoleBefore) + " a " + auditDash(e.RoleAfter)})
	}
	if e.ClientClaim != "" && !auditSecretValue(e.ClientClaim) {
		row.Details = append(row.Details, dashboard.NexoKV{Key: "cliente declarado", Value: auditTruncate(e.ClientClaim, 200)})
	}
	row.Details = append(row.Details, auditPanelDetails(e.Details)...)
	return row
}

// ─── Actividad y presencia ───────────────────────────────────────────────────

var auditOperationLabels = map[string]string{
	cloudstore.NexoOpPushChunk:     "Subida de un bloque",
	cloudstore.NexoOpPushMutations: "Subida de cambios",
	cloudstore.NexoOpPullChunk:     "Descarga de un bloque",
	cloudstore.NexoOpPullMutations: "Descarga de cambios",
	cloudstore.NexoOpPullManifest:  "Consulta del manifiesto",
}

func (s *CloudServer) auditPanelActivity(w http.ResponseWriter, r *http.Request, _ *authz.Service, _ authz.Identity) {
	q := r.URL.Query()
	p := nexoParams{values: q}
	from, to := p.time("from", false), p.time("to", true)
	actor, project, operation := p.str("actor"), p.str("project"), p.str("operation")
	pcursor, acursor := p.str("pcursor"), p.str("acursor")
	p.remember("limit", nexoAuditPanelPageSize)

	opPairs := []string{"", "Todas"}
	for _, op := range cloudstore.NexoActivityOperations {
		opPairs = append(opPairs, op, auditOperationLabels[op])
	}
	page := dashboard.NexoAuditActivityView{
		From: strings.TrimSpace(q.Get("from")), To: strings.TrimSpace(q.Get("to")), Actor: actor, Project: project,
		Operations: auditOptions(operation, opPairs...),
		Filtered:   len(auditQuery(q, "actor", "project", "operation", "from", "to")) > 0,
	}
	if operation != "" && !containsString(cloudstore.NexoActivityOperations, operation) {
		p.errs = append(p.errs, "operation")
	}
	if len(p.errs) > 0 {
		page.Error = auditBadFilters
		s.auditPanelWrite(w, r, http.StatusBadRequest, "Actividad y presencia", dashboard.NexoAuditActivityPage(page))
		return
	}
	store, ok := s.nexoTraceStore()
	if !ok {
		nexoInternalError(w, "nexo audit panel", fmt.Errorf("nexo trace store is not configured"))
		return
	}
	base := auditQuery(q, "actor", "project", "operation", "from", "to")
	now := time.Now()

	presence, pnext, err := store.ListPresence(r.Context(), cloudstore.NexoPresenceQuery{Actor: actor, Since: from, Cursor: pcursor, Limit: nexoAuditPanelPageSize})
	if err != nil {
		s.auditPanelQueryError(w, r, err)
		return
	}
	activity, anext, err := store.ListActivity(r.Context(), cloudstore.NexoActivityQuery{
		From: from, To: to, Actor: actor, Project: project, Operation: operation, Cursor: acursor, Limit: nexoAuditPanelPageSize,
	})
	if err != nil {
		s.auditPanelQueryError(w, r, err)
		return
	}
	if !s.auditPanelRecord(w, r, nexoAuditActionPresenceRead, "", map[string]any{"filters": p.filters(), "result_count": len(presence)}) ||
		!s.auditPanelRecord(w, r, nexoAuditActionActivityRead, project, map[string]any{"filters": p.filters(), "result_count": len(activity)}) {
		return
	}
	for _, e := range presence {
		seen, iso := auditTime(e.LastSeenAt)
		first := auditDate(e.FirstSeenAt)
		page.Presence = append(page.Presence, dashboard.NexoPresenceRow{
			Actor: auditFirst(e.Actor, "no identificado"), ActorSub: auditActorSub(e.ActorUsername, e.ActorEmail, e.ActorVerified),
			Token: auditDash(e.TokenName), LastSeen: seen, LastSeenISO: iso, LastSeenAge: auditAgo(now, e.LastSeenAt), FirstSeen: first,
			IP: auditDash(e.LastIP), Client: auditDash(auditTruncate(auditFirst(e.LastUserAgent, e.LastClientClaim), 160)), Project: auditDash(e.LastProject),
			Pushes: strconv.FormatInt(e.PushCount, 10), Pulls: strconv.FormatInt(e.PullCount, 10),
		})
	}
	for _, e := range activity {
		when, iso := auditTime(e.OccurredAt)
		op := auditOperationLabels[e.Operation]
		if op == "" {
			op = e.Operation
		}
		page.Activity = append(page.Activity, dashboard.NexoActivityRow{
			When: when, WhenISO: iso, Actor: auditFirst(e.Actor, "no identificado"), ActorSub: auditActorSub(e.ActorUsername, e.ActorEmail, e.ActorVerified),
			Token: auditDash(e.TokenName), Operation: op, Project: auditDash(e.Project), Items: strconv.Itoa(e.ItemCount),
			IP: auditDash(e.ClientIP), Client: auditDash(auditTruncate(auditFirst(e.UserAgent, e.ClientClaim), 160)),
		})
	}
	if pnext != "" {
		page.PresenceNext = auditURL(dashboard.NexoAuditPathActivity, base, "pcursor", pnext)
	}
	if anext != "" {
		page.ActivityNext = auditURL(dashboard.NexoAuditPathActivity, base, "acursor", anext)
	}
	if acursor != "" || pcursor != "" {
		page.FirstURL = auditURL(dashboard.NexoAuditPathActivity, base)
	}
	s.auditPanelWrite(w, r, http.StatusOK, "Actividad y presencia", dashboard.NexoAuditActivityPage(page))
}

// auditActorSub: usuario o correo y, si la persona se identificó sin que el
// servidor lo comprobara, el aviso.
func auditActorSub(username, email string, verified bool) string {
	sub := auditFirst(username, email)
	if !verified {
		if sub != "" {
			return sub + ", identidad no verificada"
		}
		return "identidad no verificada"
	}
	return sub
}

// ─── Historial de un elemento ────────────────────────────────────────────────

func (s *CloudServer) auditPanelHistory(w http.ResponseWriter, r *http.Request, _ *authz.Service, _ authz.Identity) {
	q := r.URL.Query()
	p := nexoParams{values: q}
	key := p.str("key")
	project, entity := p.str("project"), p.str("entity")
	cursor := p.str("cursor")
	p.remember("limit", nexoAuditPanelPageSize)
	page := dashboard.NexoAuditHistoryView{Key: key, Project: project, Entity: entity}
	if len(p.errs) > 0 {
		page.Error = auditBadFilters
		s.auditPanelWrite(w, r, http.StatusBadRequest, "Historial de un elemento", dashboard.NexoAuditHistoryPage(page))
		return
	}
	if key == "" {
		s.auditPanelWrite(w, r, http.StatusOK, "Historial de un elemento", dashboard.NexoAuditHistoryPage(page))
		return
	}
	store, ok := s.nexoTraceStore()
	if !ok {
		nexoInternalError(w, "nexo audit panel", fmt.Errorf("nexo trace store is not configured"))
		return
	}
	items, next, err := store.MutationHistory(r.Context(), cloudstore.NexoHistoryQuery{
		EntityKey: key, Project: project, Entity: entity, IncludePayload: false, Cursor: cursor, Limit: nexoAuditPanelPageSize,
	})
	if err != nil {
		s.auditPanelQueryError(w, r, err)
		return
	}
	filters := p.filters()
	filters["entity_key"] = key
	if !s.auditPanelRecord(w, r, nexoAuditActionHistoryRead, project, map[string]any{"filters": filters, "result_count": len(items)}) {
		return
	}
	page.Searched = true
	for _, e := range items {
		when, iso := auditTime(e.OccurredAt)
		row := dashboard.NexoHistoryRow{
			Version: strconv.FormatInt(e.Version, 10), When: when, WhenISO: iso,
			Actor: auditFirst(e.Actor, "no identificado"), ActorSub: auditActorSub(e.ActorUsername, e.CurrentEmail, e.ActorVerified),
			Token: auditDash(e.TokenName), Client: auditDash(auditTruncate(e.ClientClaim, 160)),
			Entity: auditDash(e.Entity), Project: auditDash(e.Project), Title: auditDash(auditTruncate(e.Title, 200)),
		}
		switch e.Op {
		case "upsert":
			row.Op, row.OpClass = "Creación o cambio", "nx-chip nx-chip-sched"
		case "delete":
			row.Op, row.OpClass = "Eliminación", "nx-chip nx-chip-err"
		default:
			row.Op, row.OpClass = auditDash(e.Op), "nx-chip nx-chip-ret"
		}
		page.Rows = append(page.Rows, row)
	}
	base := auditQuery(q, "key", "project", "entity")
	page.NextURL, page.FirstURL = auditPageLinks(dashboard.NexoAuditPathHistory, base, "cursor", next, cursor)
	s.auditPanelWrite(w, r, http.StatusOK, "Historial de un elemento", dashboard.NexoAuditHistoryPage(page))
}
