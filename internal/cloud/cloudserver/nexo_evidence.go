package cloudserver

// Nexo IA · NX-07 fase S4 — evidencia y monitoreo (lado servidor,
// docs/nexo/seguridad/design.md §5 y §8.4). La lógica vive en
// internal/nexo/security/evidence; aquí solo rutas, permisos y el monitor.
//
//	GET  /nexo/admin/audit/verify[?table=]          platform.audit.read   S4-01
//	GET  /nexo/admin/alerts                         platform.audit.read   S4-03
//	POST /nexo/admin/alerts/{id}:ack     {note}     platform.audit.read + token write
//	GET  /nexo/admin/access-review[?inactive_days=] platform.audit.read   S4-04
//	POST /nexo/admin/access-review:complete {notes} platform.audit.read + token write
//
// platform.audit.read la tienen el auditor, el admin y el superadmin (el
// admin hereda del auditor, NX-05). Las lecturas quedan auditadas como las de
// NX-03 (nexo.<recurso>.read); si no se pueden registrar, no se entregan
// datos. Reconocer una alerta y registrar una revisión no cambian accesos:
// las puede hacer el auditor, pero exigen un token write.
//
// Monitor (una goroutine por servidor, cada minuto): escribe los resúmenes
// del agrupador de rechazos (A2, en cada réplica) y, en una sola réplica a la
// vez (advisory lock): evalúa las alertas y las envía al webhook, verifica la
// cadena cada NEXO_AUDIT_VERIFY_MINUTES y una vez por día exporta la
// auditoría, deshabilita cuentas inactivas (si está configurado) y depura por
// retención (si está configurada).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

// Acciones de auditoría de las lecturas de S4.
const (
	nexoAuditActionChainVerifyRead = "nexo.audit_verify.read"
	nexoAuditActionAlertsRead      = "nexo.alerts.read"
	nexoAuditActionAccessReview    = "nexo.access_review.read"
	nexoAuditActionAlertAck        = "nexo.alert.ack"
	nexoAuditActionReviewComplete  = "nexo.access_review.complete"
	// NexoAuditVerifyMinutesEnv: cada cuántos minutos se verifica la cadena
	// (por defecto 60; 0 = no verificar en segundo plano).
	NexoAuditVerifyMinutesEnv = "NEXO_AUDIT_VERIFY_MINUTES"
)

func (s *CloudServer) registerNexoEvidenceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /nexo/admin/audit/verify", s.nexoAuthzRoute(s.handleNexoChainVerify))
	mux.HandleFunc("GET /nexo/admin/alerts", s.nexoAuthzRoute(s.handleNexoAlerts))
	mux.HandleFunc("POST /nexo/admin/alerts/{idAction}", s.nexoAuthzRoute(s.handleNexoAlertAction))
	mux.HandleFunc("GET /nexo/admin/access-review", s.nexoAuthzRoute(s.handleNexoAccessReview))
	mux.HandleFunc("POST /nexo/admin/access-review:complete", s.nexoAuthzRoute(s.handleNexoAccessReviewComplete))
}

// nexoRequireAudit exige platform.audit.read (y, con write, un token write).
// La denegación queda en nexo_authz_events (authz.denied).
func (s *CloudServer) nexoRequireAudit(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity, write bool) bool {
	details := map[string]any{"route": r.Pattern}
	d, err := svc.Engine().Require(r.Context(), id, authz.PlatformScope(), authz.CapPlatformAuditRead, details)
	if err != nil {
		nexoAuthzWriteError(w, err)
		return false
	}
	if !d.Allowed {
		writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden,
			fmt.Sprintf("forbidden: %s is required (%s)", authz.CapPlatformAuditRead, d.Reason))
		return false
	}
	if write && id.TokenScope == authz.TokenScopeRead {
		details["rule"] = nexoCodeTokenScopeRead
		svc.Engine().RecordDenied(r.Context(), id, authz.PlatformScope(), authz.CapPlatformAuditRead, authz.Decision{Role: d.Role, Reason: nexoCodeTokenScopeRead}, details)
		writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden,
			"forbidden: this token is read-only (scope read); recording this action requires a write token")
		return false
	}
	if s.nexoDB() == nil {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, "nexo evidence store is not configured")
		return false
	}
	return true
}

// nexoRecordAuditAction deja en cloud_auth_audit_log una lectura o acción de
// S4 con el actor. false (y 500) si no se pudo registrar.
func (s *CloudServer) nexoRecordAuditAction(w http.ResponseWriter, r *http.Request, action string, metadata map[string]any) bool {
	store, ok := s.adminStore(w)
	if !ok {
		return false
	}
	principal, _ := PrincipalFromContext(r.Context())
	if err := s.recordAdminAudit(r.Context(), store, principal, action, "", "", metadata); err != nil {
		writeAuditFailure(w, err)
		return false
	}
	return true
}

// ─── S4-01: verificación de la cadena ────────────────────────────────────────

func (s *CloudServer) handleNexoChainVerify(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	if !s.nexoRequireAudit(w, r, svc, id, false) {
		return
	}
	var tables []string
	if t := strings.TrimSpace(r.URL.Query().Get("table")); t != "" {
		if _, ok := evidence.ChainSpecFor(t); !ok {
			nexoAuthzBadRequest(w, "table must be one of "+strings.Join(nexoChainTableNames(), ", "))
			return
		}
		tables = []string{t}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	rep, err := evidence.VerifyAll(ctx, s.nexoDB(), tables)
	if err != nil {
		nexoInternalError(w, "nexo audit verify", err)
		return
	}
	if !s.nexoRecordAuditAction(w, r, nexoAuditActionChainVerifyRead, map[string]any{"tables": tables, "ok": rep.OK}) {
		return
	}
	jsonResponse(w, http.StatusOK, rep)
}

func nexoChainTableNames() []string {
	out := make([]string, 0, len(evidence.ChainedTables))
	for _, t := range evidence.ChainedTables {
		out = append(out, t.Table)
	}
	return out
}

// ─── S4-03: alertas ──────────────────────────────────────────────────────────

func (s *CloudServer) handleNexoAlerts(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	if !s.nexoRequireAudit(w, r, svc, id, false) {
		return
	}
	p := nexoParams{values: r.URL.Query()}
	q := evidence.AlertQuery{
		Rule:     p.str("rule"),
		Layer:    p.str("layer"),
		Severity: p.str("severity"),
		Unacked:  p.boolean("unacked"),
		From:     p.time("from", false),
		To:       p.time("to", true),
		Cursor:   p.str("cursor"),
		Limit:    p.limit(),
	}
	if q.Rule != "" && !slices.Contains(evidence.AllRules, q.Rule) {
		p.errs = append(p.errs, "rule must be one of "+strings.Join(evidence.AllRules, ", "))
	}
	if q.Layer != "" && q.Layer != string(evidence.LayerPlatform) && q.Layer != string(evidence.LayerProjects) && q.Layer != string(evidence.LayerKnowledge) {
		p.errs = append(p.errs, "layer must be plataforma, proyectos or conocimiento")
	}
	if p.fail(w) {
		return
	}
	items, next, err := evidence.ListAlerts(r.Context(), s.nexoDB(), q)
	if errors.Is(err, evidence.ErrInvalidCursor) {
		nexoAuthzBadRequest(w, "invalid cursor")
		return
	}
	if err != nil {
		nexoInternalError(w, "nexo alerts", err)
		return
	}
	if !s.nexoRecordAuditAction(w, r, nexoAuditActionAlertsRead, map[string]any{"filters": p.filters(), "result_count": len(items)}) {
		return
	}
	jsonResponse(w, http.StatusOK, newNexoPage(items, next, cloudstore.NexoNormalizeLimit(q.Limit)))
}

func (s *CloudServer) handleNexoAlertAction(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	raw := r.PathValue("idAction")
	idText, action, ok := strings.Cut(raw, ":")
	alertID, err := strconv.ParseInt(idText, 10, 64)
	if !ok || action != "ack" || err != nil || alertID <= 0 {
		writeActionableError(w, http.StatusNotFound, constants.UpgradeErrorClassRepairable, authz.CodeNotFound, "unknown alert action: use /nexo/admin/alerts/{id}:ack")
		return
	}
	if !s.nexoRequireAudit(w, r, svc, id, true) {
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
		if err := dec.Decode(&body); err != nil {
			nexoAuthzBadRequest(w, "invalid JSON body: expected {\"note\": \"...\"}")
			return
		}
	}
	ack, err := evidence.AckAlert(r.Context(), s.nexoDB(), alertID, evidence.AckActor{
		PrincipalID: id.PrincipalID, TokenID: id.TokenID, Label: id.Label, ClientIP: id.ClientIP, UserAgent: id.UserAgent,
	}, body.Note)
	switch {
	case errors.Is(err, evidence.ErrAlertNotFound):
		writeActionableError(w, http.StatusNotFound, constants.UpgradeErrorClassRepairable, authz.CodeNotFound, "alert not found")
		return
	case errors.Is(err, evidence.ErrAlertAcked):
		writeActionableError(w, http.StatusConflict, constants.UpgradeErrorClassPolicy, "already_acknowledged", "alert already acknowledged")
		return
	case err != nil:
		nexoInternalError(w, "nexo alert ack", err)
		return
	}
	if !s.nexoRecordAuditAction(w, r, nexoAuditActionAlertAck, map[string]any{"alert_id": alertID}) {
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"alert_id": alertID, "ack": ack})
}

// ─── S4-04: revisión de accesos ──────────────────────────────────────────────

func nexoReviewInactiveDays(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("inactive_days"))
	if raw == "" {
		return evidence.ReviewInactiveDays, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 3650 {
		return 0, fmt.Errorf("inactive_days must be between 1 and 3650")
	}
	return n, nil
}

func (s *CloudServer) handleNexoAccessReview(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	if !s.nexoRequireAudit(w, r, svc, id, false) {
		return
	}
	days, err := nexoReviewInactiveDays(r)
	if err != nil {
		nexoAuthzBadRequest(w, err.Error())
		return
	}
	rep, err := evidence.BuildAccessReview(r.Context(), s.nexoDB(), days, time.Now())
	if err != nil {
		nexoInternalError(w, "nexo access review", err)
		return
	}
	if !s.nexoRecordAuditAction(w, r, nexoAuditActionAccessReview, map[string]any{"inactive_days": days, "people": rep.Summary.People}) {
		return
	}
	jsonResponse(w, http.StatusOK, rep)
}

func (s *CloudServer) handleNexoAccessReviewComplete(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	if !s.nexoRequireAudit(w, r, svc, id, true) {
		return
	}
	var body struct {
		Notes        string `json:"notes"`
		InactiveDays int    `json:"inactive_days"`
	}
	if r.ContentLength != 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		if err := dec.Decode(&body); err != nil {
			nexoAuthzBadRequest(w, "invalid JSON body: expected {\"notes\": \"...\"}")
			return
		}
	}
	if body.InactiveDays < 0 || body.InactiveDays > 3650 {
		nexoAuthzBadRequest(w, "inactive_days must be between 1 and 3650")
		return
	}
	rep, err := evidence.BuildAccessReview(r.Context(), s.nexoDB(), body.InactiveDays, time.Now())
	if err != nil {
		nexoInternalError(w, "nexo access review", err)
		return
	}
	rec, err := evidence.CompleteReview(r.Context(), s.nexoDB(), rep, evidence.Reviewer{
		PrincipalID: id.PrincipalID, TokenID: id.TokenID, Label: id.Label, ClientIP: id.ClientIP, UserAgent: id.UserAgent,
	}, body.Notes)
	if err != nil {
		nexoInternalError(w, "nexo access review", err)
		return
	}
	if !s.nexoRecordAuditAction(w, r, nexoAuditActionReviewComplete, map[string]any{"review_id": rec.ID, "report_sha256": rec.ReportSHA256}) {
		return
	}
	jsonResponse(w, http.StatusCreated, rec)
}

// ─── Monitor ─────────────────────────────────────────────────────────────────

type nexoMonitor struct {
	rules        evidence.RuleSet
	webhook      *evidence.Webhook
	export       evidence.ExportConfig
	exportOn     bool
	retention    evidence.RetentionPlan
	inactiveDays int
	verifyEvery  time.Duration

	mu             sync.Mutex
	lastVerify     time.Time
	lastCheckpoint time.Time
	lastDaily      string // fecha UTC de la última pasada diaria
	webhookWG      sync.WaitGroup
}

// nexoCheckpointEvery: cada cuánto se registra un punto de control de la
// cadena (si avanzó).
const nexoCheckpointEvery = 10 * time.Minute

// nexoMonitorConfig lee la configuración de S4 (una vez por servidor) y deja
// en el log la configuración efectiva.
func newNexoMonitorFromEnv() *nexoMonitor {
	m := &nexoMonitor{verifyEvery: time.Hour}
	rules, err := evidence.RulesFromEnv()
	if err != nil {
		log.Printf("[nexo] seguridad: %v; se usan las reglas por defecto", err)
	}
	m.rules = rules
	m.webhook = evidence.WebhookFromEnv()
	m.export, m.exportOn, _ = evidence.ExportConfigFromEnv()
	m.retention = evidence.RetentionFromEnv()
	m.inactiveDays = evidence.DisableInactiveDays()
	if raw := strings.TrimSpace(os.Getenv(NexoAuditVerifyMinutesEnv)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			m.verifyEvery = time.Duration(n) * time.Minute
		} else {
			log.Printf("[nexo] seguridad: %s=%q inválido; se usa 60", NexoAuditVerifyMinutesEnv, raw)
		}
	}
	m.logConfig()
	return m
}

func (m *nexoMonitor) logConfig() {
	log.Printf("[nexo] seguridad: alertas activas: %s", m.rules)
	if m.webhook != nil {
		log.Printf("[nexo] seguridad: webhook de alertas: %s", m.webhook.Host())
	} else {
		log.Printf("[nexo] seguridad: %s vacio: las alertas quedan en la base y en GET /nexo/admin/alerts", evidence.AlertWebhookEnv)
	}
	if m.exportOn {
		log.Printf("[nexo] seguridad: exportacion diaria firmada en %s (clave %s)", m.export.Dir, m.export.KeyID())
	} else {
		_, _, why := evidence.ExportConfigFromEnv()
		log.Printf("[nexo] seguridad: %s", why)
	}
	if m.verifyEvery > 0 {
		log.Printf("[nexo] seguridad: verificacion de la cadena de hashes cada %s", m.verifyEvery)
	} else {
		log.Printf("[nexo] seguridad: %s=0: la cadena solo se verifica a pedido", NexoAuditVerifyMinutesEnv)
	}
	log.Printf("[nexo] seguridad: retencion: %s", m.retention)
	if m.inactiveDays > 0 {
		log.Printf("[nexo] seguridad: cuentas sin uso por %d dias se deshabilitan (%s)", m.inactiveDays, evidence.DisableInactiveEnv)
	} else {
		log.Printf("[nexo] seguridad: %s vacio: las cuentas inactivas solo se informan en la revision de accesos", evidence.DisableInactiveEnv)
	}
}

// nexoFlushAuditDedup escribe los resúmenes vencidos del agrupador (A2).
type nexoDedupFlusher interface {
	NexoFlushAuditDedup(ctx context.Context, d *security.AuditDedup, all bool) (int, error)
}

func (s *CloudServer) nexoFlushAuditDedup(ctx context.Context, all bool) int {
	sec := s.nexoSecurity()
	for _, candidate := range []any{s.store, s.adminIdentity} {
		if f, ok := candidate.(nexoDedupFlusher); ok && f != nil {
			n, err := f.NexoFlushAuditDedup(ctx, sec.dedup, all)
			if err != nil {
				log.Printf("[nexo] seguridad: resumen de rechazos repetidos fallido: %v", err)
			}
			return n
		}
	}
	return 0
}

// nexoStartMonitor arranca el monitor (lo llama nexoStartActivityRetention
// desde Start()).
func (s *CloudServer) nexoStartMonitor() {
	if s.nexoDB() == nil {
		return
	}
	m := s.nexoMonitorState()
	go func() {
		// Primera pasada al minuto de arrancar (la depuración y la
		// exportación no compiten con el arranque).
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for now := range t.C {
			s.nexoMonitorTick(context.Background(), m, now)
		}
	}()
}

func (s *CloudServer) nexoMonitorState() *nexoMonitor {
	s.nexo.monitorOnce.Do(func() { s.nexo.monitor = newNexoMonitorFromEnv() })
	return s.nexo.monitor
}

// nexoMonitorTick es una pasada del monitor. Nunca entra en pánico ni
// devuelve error: cada parte registra sus fallos en el log.
func (s *CloudServer) nexoMonitorTick(ctx context.Context, m *nexoMonitor, now time.Time) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[nexo] seguridad: monitor: panic recuperado: %v", rec)
		}
	}()
	db := s.nexoDB()
	if db == nil {
		return
	}
	s.nexoFlushAuditDedup(ctx, false)

	evalCtx, cancel := context.WithTimeout(ctx, 50*time.Second)
	alerts, ran, err := evidence.EvaluateOnce(evalCtx, db, m.rules, now)
	cancel()
	if err != nil {
		log.Printf("[nexo] seguridad: evaluador de alertas: %v", err)
	}
	if !ran {
		return // otra réplica tiene el candado del evaluador
	}

	m.mu.Lock()
	verify := m.verifyEvery > 0 && (m.lastVerify.IsZero() || now.Sub(m.lastVerify) >= m.verifyEvery)
	if verify {
		m.lastVerify = now
	}
	checkpoint := m.lastCheckpoint.IsZero() || now.Sub(m.lastCheckpoint) >= nexoCheckpointEvery
	if checkpoint {
		m.lastCheckpoint = now
	}
	day := now.UTC().Format(time.DateOnly)
	daily := m.lastDaily != day
	if daily {
		m.lastDaily = day
	}
	m.mu.Unlock()

	if checkpoint {
		// Punto de control de la cadena (detecta el borrado de las últimas
		// filas); solo si la cadena avanzó.
		if _, err := evidence.Checkpoint(ctx, db, "monitor"); err != nil {
			log.Printf("[nexo] seguridad: punto de control de la cadena: %v", err)
		}
	}
	if verify {
		vctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		rep, err := evidence.VerifyAll(vctx, db, nil)
		cancel()
		if err != nil {
			log.Printf("[nexo] seguridad: verificacion de la cadena: %v", err)
		} else {
			if !rep.OK {
				log.Printf("[nexo] seguridad: verificacion de la cadena de hashes FALLIDA (ver GET /nexo/admin/audit/verify)")
			}
			got, err := evidence.ChainAlerts(ctx, db, rep, m.rules[evidence.RuleAuditChainBroken])
			if err != nil {
				log.Printf("[nexo] seguridad: alerta de cadena: %v", err)
			}
			alerts = append(alerts, got...)
		}
	}
	if daily {
		alerts = append(alerts, s.nexoDailyJobs(ctx, m, now)...)
	}
	s.nexoDeliverAlerts(m, alerts)
}

// nexoDailyJobs: exportación, cuentas inactivas y retención.
func (s *CloudServer) nexoDailyJobs(ctx context.Context, m *nexoMonitor, now time.Time) []evidence.Alert {
	db := s.nexoDB()
	var alerts []evidence.Alert
	if m.exportOn {
		from := s.nexoExportFrom(ctx, now)
		res, err := evidence.ExportRange(ctx, db, m.export, from, time.Time{}, now, "servidor", false)
		written := 0
		for _, r := range res {
			if !r.Skipped {
				written++
			}
		}
		if err != nil {
			log.Printf("[nexo] seguridad: exportacion de auditoria fallida: %v", err)
			cfg := m.rules[evidence.RuleAuditExportFailed]
			if cfg.Enabled {
				a, aerr := evidence.InsertAlert(ctx, db, evidence.NewAlert{
					Rule: evidence.RuleAuditExportFailed, Severity: cfg.Severity, Layer: evidence.LayerPlatform, Subject: "export",
					Summary: "la exportación diaria de la auditoría falló (ver el log del servidor)", Cooldown: cfg.Cooldown,
					Details: map[string]any{"day": now.UTC().AddDate(0, 0, -1).Format(time.DateOnly)},
				})
				if aerr == nil && a != nil {
					alerts = append(alerts, *a)
				}
			}
		} else if written > 0 {
			log.Printf("[nexo] seguridad: exportacion firmada: %d archivos nuevos en %s", written, m.export.Dir)
		}
	}
	if m.inactiveDays > 0 {
		done, got, err := evidence.DisableInactive(ctx, db, m.inactiveDays, now, m.rules[evidence.RuleInactiveDisabled])
		if err != nil {
			log.Printf("[nexo] seguridad: cuentas inactivas: %v", err)
		}
		if len(done) > 0 {
			log.Printf("[nexo] seguridad: %d cuentas deshabilitadas por inactividad (%d dias)", len(done), m.inactiveDays)
		}
		alerts = append(alerts, got...)
	}
	for _, rule := range evidence.RetentionRules {
		days := m.retention[rule.Table]
		if days <= 0 {
			continue
		}
		n, err := evidence.Purge(ctx, db, rule.Table, days)
		if err != nil {
			log.Printf("[nexo] seguridad: retencion de %s: %v", rule.Table, err)
			continue
		}
		if n > 0 {
			log.Printf("[nexo] seguridad: retencion: %d filas de %s con mas de %d dias depuradas", n, rule.Table, days)
		}
	}
	return alerts
}

// nexoExportFrom: desde el día siguiente al último exportado (hasta 31 días
// atrás) o, la primera vez, ayer.
func (s *CloudServer) nexoExportFrom(ctx context.Context, now time.Time) time.Time {
	yesterday := now.UTC().AddDate(0, 0, -1)
	var last *time.Time
	if err := s.nexoDB().QueryRowContext(ctx, `SELECT max(day)::timestamptz FROM nexo_audit_exports`).Scan(&last); err != nil || last == nil {
		return yesterday
	}
	from := last.UTC().AddDate(0, 0, 1)
	if limit := now.UTC().AddDate(0, 0, -31); from.Before(limit) {
		from = limit
	}
	return from
}

// nexoDeliverAlerts envía las alertas nuevas al webhook, fuera del monitor.
func (s *CloudServer) nexoDeliverAlerts(m *nexoMonitor, alerts []evidence.Alert) {
	if len(alerts) == 0 {
		return
	}
	log.Printf("[nexo] seguridad: %d alertas nuevas (GET /nexo/admin/alerts)", len(alerts))
	if m.webhook == nil {
		return
	}
	m.webhookWG.Add(1)
	go func() {
		defer m.webhookWG.Done()
		for _, a := range alerts {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			if err := m.webhook.Send(ctx, a); err != nil {
				log.Printf("[nexo] seguridad: %v", err)
			}
			cancel()
		}
	}()
}
