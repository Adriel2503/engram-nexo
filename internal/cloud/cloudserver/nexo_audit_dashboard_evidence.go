package cloudserver

// Nexo IA · SG-07 — pantallas de evidencia del panel del auditor: integridad
// de la cadena, alertas y revisión de accesos (docs/nexo/panel/design.md §9).
// Misma lógica que nexo_evidence.go (evidence.VerifyAll, ListAlerts,
// AckAlert, BuildAccessReview, CompleteReview) y las mismas acciones de
// auditoría (nexo.audit_verify.read, nexo.alert.ack, ...). Ver
// nexo_audit_dashboard.go para el acceso, el CSRF y el manejo de errores.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/dashboard"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

// ─── Integridad ──────────────────────────────────────────────────────────────

var auditChainNames = map[string]string{
	"cloud_auth_audit_log": "Auditoría de seguridad",
	"nexo_authz_events":    "Eventos de roles",
	"nexo_kb_events":       "Eventos de conocimiento",
}

func auditChainWhat(table string) string {
	if n, ok := auditChainNames[table]; ok {
		return n
	}
	return table
}

func auditLayerLabel(l string) string {
	switch l {
	case string(evidence.LayerPlatform):
		return "Plataforma"
	case string(evidence.LayerProjects):
		return "Proyectos"
	case string(evidence.LayerKnowledge):
		return "Conocimiento"
	}
	return l
}

// auditSentence deja el texto como oración: sin punto final y con mayúscula
// inicial.
func auditSentence(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), ". ")
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func auditGapWhere(where string) string {
	switch where {
	case "start":
		return "al inicio"
	case "middle":
		return "en el medio"
	case "end":
		return "al final"
	}
	return where
}

func auditInt(n int64) string { return strconv.FormatInt(n, 10) }

func (s *CloudServer) auditPanelIntegrity(w http.ResponseWriter, r *http.Request, _ *authz.Service, _ authz.Identity) {
	q := r.URL.Query()
	table := strings.TrimSpace(q.Get("table"))
	pairs := []string{"", "Todos los registros"}
	for _, t := range evidence.ChainedTables {
		pairs = append(pairs, t.Table, auditChainWhat(t.Table))
	}
	page := dashboard.NexoAuditIntegrityView{Tables: auditOptions(table, pairs...)}
	title := "Integridad de la evidencia"
	if q.Get("run") != "1" {
		s.auditPanelWrite(w, r, http.StatusOK, title, dashboard.NexoAuditIntegrityPage(page))
		return
	}
	var tables []string
	if table != "" {
		if _, ok := evidence.ChainSpecFor(table); !ok {
			page.Error = "Ese registro no tiene cadena de hashes. Elige uno de la lista."
			s.auditPanelWrite(w, r, http.StatusBadRequest, title, dashboard.NexoAuditIntegrityPage(page))
			return
		}
		tables = []string{table}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	rep, err := evidence.VerifyAll(ctx, s.nexoDB(), tables)
	if err != nil {
		nexoInternalError(w, "nexo audit verify", err)
		return
	}
	if !s.auditPanelRecord(w, r, nexoAuditActionChainVerifyRead, "", map[string]any{"tables": tables, "ok": rep.OK}) {
		return
	}
	page.Ran, page.OK = true, rep.OK
	page.VerifiedAt, page.VerifiedISO = auditTime(rep.VerifiedAt)
	var checked, altered, missing int64
	var problems int
	bad := 0
	for _, t := range rep.Tables {
		checked += t.Checked
		altered += t.Altered
		missing += t.MissingRows
		problems += len(t.Problems)
		if !t.OK {
			bad++
		}
		res := dashboard.NexoIntegrityTable{
			Name: t.Table, What: auditChainWhat(t.Table), Layer: auditLayerLabel(string(t.Layer)), OK: t.OK,
			Checked: auditInt(t.Checked), Unchained: auditInt(t.Unchained), Altered: auditInt(t.Altered), Missing: auditInt(t.MissingRows),
			Problems: t.Problems,
		}
		if t.FirstBroken != nil {
			res.Broken = &dashboard.NexoIntegrityBroken{
				ID: auditInt(t.FirstBroken.ID), Seq: auditInt(t.FirstBroken.Seq), Detail: auditSentence(t.FirstBroken.Detail),
			}
		}
		var listed int64
		for _, g := range t.Gaps {
			listed += g.Missing
			res.Gaps = append(res.Gaps, dashboard.NexoIntegrityGap{From: auditInt(g.FromSeq), To: auditInt(g.ToSeq), Missing: auditInt(g.Missing), Where: auditGapWhere(g.Where)})
		}
		if listed < t.MissingRows {
			res.GapsNote = "La lista muestra los primeros " + strconv.Itoa(len(t.Gaps)) + " tramos; en total faltan " + auditInt(t.MissingRows) + " filas."
		}
		page.Results = append(page.Results, res)
	}
	tablesWord := "registros"
	if len(rep.Tables) == 1 {
		tablesWord = "registro"
	}
	if rep.OK {
		page.Verdict = "La cadena está íntegra"
		page.VerdictDetail = strconv.Itoa(len(rep.Tables)) + " " + tablesWord + " verificados, " + auditInt(checked) + " filas. Ninguna fila alterada ni faltante."
	} else {
		var parts []string
		if altered > 0 {
			parts = append(parts, auditInt(altered)+" filas alteradas")
		}
		if missing > 0 {
			parts = append(parts, auditInt(missing)+" filas faltantes")
		}
		if problems > 0 {
			parts = append(parts, strconv.Itoa(problems)+" problemas de la cadena")
		}
		page.Verdict = "Se detectó una alteración"
		if altered == 0 && missing == 0 {
			page.Verdict = "La comprobación encontró problemas"
		}
		page.VerdictDetail = strconv.Itoa(bad) + " de " + strconv.Itoa(len(rep.Tables)) + " " + tablesWord + " con problemas: " + strings.Join(parts, ", ") + "."
	}
	s.auditPanelWrite(w, r, http.StatusOK, title, dashboard.NexoAuditIntegrityPage(page))
}

// ─── Alertas ─────────────────────────────────────────────────────────────────

var auditRuleLabels = map[string]string{
	evidence.RuleActivationRepeated:      "Canje de activación repetido",
	evidence.RuleActivationSameIP:        "Código canjeado desde la IP del emisor",
	evidence.RuleAuthzDeniedBurst:        "Ráfaga de acciones denegadas",
	evidence.RuleAuthFailuresBurst:       "Ráfaga de fallos de autenticación",
	evidence.RulePlatformRoleChanged:     "Cambio de rol de plataforma",
	evidence.RuleSharedTokenUsed:         "Uso del token compartido",
	evidence.RuleExpiredTokenUsed:        "Uso de un token vencido",
	evidence.RuleRevokedTokenUsed:        "Uso de un token revocado",
	evidence.RuleInvalidTokenRepeated:    "Token inválido repetido",
	evidence.RuleAuditChainBroken:        "Cadena de auditoría alterada",
	evidence.RuleLoggingStalled:          "Registro de auditoría detenido",
	evidence.RuleInactiveDisabled:        "Cuenta deshabilitada por inactividad",
	evidence.RuleAuditExportFailed:       "Falló la exportación de auditoría",
	evidence.RuleKBDocExpiring:           "Documento por vencer",
	evidence.RuleKBReviewDue:             "Documento con revisión pendiente",
	evidence.RuleSAStale:                 "Cuenta de servicio sin uso",
	evidence.RulePlatformRoleDrift:       "Rol de Engram desalineado del rol de Nexo",
	evidence.RuleSingleSuperadmin:        "Queda un solo superadmin",
	evidence.RulePrivilegedTokenExpiring: "Token privilegiado por vencer",
	evidence.RuleEmergencyAccessUsed:     "Acceso de emergencia usado",
}

func auditRuleLabel(rule string) string {
	if l, ok := auditRuleLabels[rule]; ok {
		return l
	}
	return rule
}

func auditSeverity(sev string) (label, class string) {
	switch sev {
	case evidence.SeverityInfo:
		return "Informativa", "nx-chip nx-chip-sched"
	case evidence.SeverityWarning:
		return "Advertencia", "nx-chip nx-chip-exp"
	case evidence.SeverityHigh:
		return "Alta", "nx-chip nx-chip-err"
	case evidence.SeverityCritical:
		return "Crítica", "nx-chip nx-chip-err"
	}
	return sev, "nx-chip nx-chip-ret"
}

// auditAlertState: "pendientes" (por defecto) o "todas".
func auditAlertState(v string) string {
	if v == "todas" {
		return "todas"
	}
	return "pendientes"
}

type auditAlertsState struct {
	notice, errText string
	status          int
	state           string
}

func (s *CloudServer) auditPanelAlerts(w http.ResponseWriter, r *http.Request, _ *authz.Service, id authz.Identity) {
	st := auditAlertsState{state: auditAlertState(r.URL.Query().Get("state"))}
	if r.URL.Query().Get("ok") == "ack" {
		st.notice = "Alerta reconocida. Quedó registrada con tu nombre."
	}
	s.auditPanelAlertsRender(w, r, id, st)
}

func (s *CloudServer) auditPanelAlertsRender(w http.ResponseWriter, r *http.Request, id authz.Identity, st auditAlertsState) {
	q := r.URL.Query()
	p := nexoParams{values: q}
	rule, layer, severity := p.str("rule"), p.str("layer"), p.str("severity")
	cursor := p.str("cursor")
	p.remember("limit", nexoAuditPanelPageSize)
	state := auditAlertState(st.state)
	if state == "pendientes" {
		p.remember("unacked", true)
	}
	statePairs := func(label, value string) dashboard.NexoAuditSourceLink {
		return dashboard.NexoAuditSourceLink{Label: label, URL: auditURL(dashboard.NexoAuditPathAlerts, url.Values{"state": {value}}), Current: state == value}
	}
	rulePairs := []string{"", "Todas las reglas"}
	for _, ru := range evidence.AllRules {
		rulePairs = append(rulePairs, ru, auditRuleLabel(ru))
	}
	page := dashboard.NexoAuditAlertsView{
		Notice: st.notice, Error: st.errText, CSRF: s.kbCSRFToken(r), CanAck: id.TokenScope != authz.TokenScopeRead,
		State:      state,
		StateLinks: []dashboard.NexoAuditSourceLink{statePairs("Pendientes", "pendientes"), statePairs("Todas", "todas")},
		Rules:      auditOptions(rule, rulePairs...),
		Severities: auditOptions(severity, "", "Todas", evidence.SeverityInfo, "Informativa", evidence.SeverityWarning, "Advertencia", evidence.SeverityHigh, "Alta", evidence.SeverityCritical, "Crítica"),
		Layers: auditOptions(layer, "", "Todas", string(evidence.LayerPlatform), "Plataforma", string(evidence.LayerProjects), "Proyectos",
			string(evidence.LayerKnowledge), "Conocimiento"),
		Filtered: rule != "" || layer != "" || severity != "",
		ClearURL: auditURL(dashboard.NexoAuditPathAlerts, url.Values{"state": {state}}),
	}
	write := func(status int) {
		if st.status != 0 {
			status = st.status
		}
		s.auditPanelWrite(w, r, status, "Alertas de seguridad", dashboard.NexoAuditAlertsPage(page))
	}
	if rule != "" && !containsString(evidence.AllRules, rule) {
		p.errs = append(p.errs, "rule")
	}
	if layer != "" && layer != string(evidence.LayerPlatform) && layer != string(evidence.LayerProjects) && layer != string(evidence.LayerKnowledge) {
		p.errs = append(p.errs, "layer")
	}
	if severity != "" && severity != evidence.SeverityInfo && severity != evidence.SeverityWarning && severity != evidence.SeverityHigh && severity != evidence.SeverityCritical {
		p.errs = append(p.errs, "severity")
	}
	if len(p.errs) > 0 {
		page.Error = "Algún filtro no es válido. Elige las opciones de las listas."
		write(http.StatusBadRequest)
		return
	}
	items, next, err := evidence.ListAlerts(r.Context(), s.nexoDB(), evidence.AlertQuery{
		Rule: rule, Layer: layer, Severity: severity, Unacked: state == "pendientes", Cursor: cursor, Limit: nexoAuditPanelPageSize,
	})
	if err != nil {
		s.auditPanelQueryError(w, r, err)
		return
	}
	if !s.auditPanelRecord(w, r, nexoAuditActionAlertsRead, "", map[string]any{"filters": p.filters(), "result_count": len(items)}) {
		return
	}
	if !s.nexoFilterAlerts(w, r, items) {
		return
	}
	for _, a := range items {
		when, iso := auditTime(a.OccurredAt)
		sev, class := auditSeverity(a.Severity)
		row := dashboard.NexoAlertRow{
			ID: strconv.FormatInt(a.ID, 10), When: when, WhenISO: iso, Severity: sev, SevClass: class,
			Rule: a.Rule, RuleLabel: auditRuleLabel(a.Rule), Layer: auditLayerLabel(string(a.Layer)),
			Subject: auditDash(auditTruncate(a.Subject, 200)), Summary: auditTruncate(a.Summary, 400),
		}
		if a.Ack != nil {
			row.Acked = true
			row.AckBy = a.Ack.Label
			row.AckWhen, row.AckWhenISO = auditTime(a.Ack.OccurredAt)
			row.AckNote = auditTruncate(a.Ack.Note, 300)
			if a.Ack.NoteWithheld {
				row.AckNote = "texto no mostrado: requiere rol de lectura en el ámbito de la alerta"
			}
		}
		page.Alerts = append(page.Alerts, row)
	}
	base := auditQuery(q, "rule", "layer", "severity")
	base.Set("state", state)
	page.NextURL, page.FirstURL = auditPageLinks(dashboard.NexoAuditPathAlerts, base, "cursor", next, cursor)
	write(http.StatusOK)
}

// auditPanelAlertAck reconoce una alerta con las mismas reglas que
// POST /nexo/admin/alerts/{id}:ack (una vez por alerta, con nota opcional).
func (s *CloudServer) auditPanelAlertAck(w http.ResponseWriter, r *http.Request, _ *authz.Service, id authz.Identity) {
	state := auditAlertState(r.FormValue("state"))
	st := auditAlertsState{state: state}
	alertID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || alertID <= 0 {
		st.status, st.errText = http.StatusNotFound, "No encontramos esa alerta."
		s.auditPanelAlertsRender(w, r, id, st)
		return
	}
	_, err = evidence.AckAlert(r.Context(), s.nexoDB(), alertID, evidence.AckActor{
		PrincipalID: id.PrincipalID, TokenID: id.TokenID, Label: id.Label, ClientIP: id.ClientIP, UserAgent: id.UserAgent,
	}, r.FormValue("note"))
	switch {
	case errors.Is(err, evidence.ErrAlertNotFound):
		st.status, st.errText = http.StatusNotFound, "No encontramos esa alerta. Recarga la lista."
		s.auditPanelAlertsRender(w, r, id, st)
		return
	case errors.Is(err, evidence.ErrAlertAcked):
		st.status, st.errText = http.StatusConflict, "Esa alerta ya fue reconocida por otra persona. Recarga la lista."
		s.auditPanelAlertsRender(w, r, id, st)
		return
	case err != nil:
		nexoInternalError(w, "nexo alert ack", err)
		return
	}
	if !s.auditPanelRecord(w, r, nexoAuditActionAlertAck, "", map[string]any{"alert_id": alertID}) {
		return
	}
	http.Redirect(w, r, auditURL(dashboard.NexoAuditPathAlerts, url.Values{"state": {state}, "ok": {"ack"}}), http.StatusSeeOther)
}

// ─── Revisión de accesos ─────────────────────────────────────────────────────

func (s *CloudServer) auditPanelReview(w http.ResponseWriter, r *http.Request, _ *authz.Service, id authz.Identity) {
	days, err := nexoReviewInactiveDays(r)
	if err != nil {
		s.auditPanelReviewRender(w, r, id, evidence.ReviewInactiveDays, false, "", "Los días sin uso deben ser un número entre 1 y 3650.", http.StatusBadRequest)
		return
	}
	notice := ""
	if r.URL.Query().Get("ok") == "review" {
		notice = "Revisión registrada con tu nombre. Aparece arriba como la última revisión."
	}
	s.auditPanelReviewRender(w, r, id, days, r.URL.Query().Get("only") == "flags", notice, "", http.StatusOK)
}

func (s *CloudServer) auditPanelReviewRender(w http.ResponseWriter, r *http.Request, id authz.Identity, days int, only bool, notice, errText string, status int) {
	title := "Revisión de accesos"
	rep, err := evidence.BuildAccessReview(r.Context(), s.nexoDB(), days, time.Now())
	if err != nil {
		nexoInternalError(w, "nexo access review", err)
		return
	}
	if !s.auditPanelRecord(w, r, nexoAuditActionAccessReview, "", map[string]any{"inactive_days": days, "people": rep.Summary.People}) {
		return
	}
	now := time.Now()
	daysText := strconv.Itoa(rep.InactiveDays)
	page := dashboard.NexoAuditReviewView{
		Notice: notice, Error: errText, CSRF: s.kbCSRFToken(r), CanComplete: id.TokenScope != authz.TokenScopeRead,
		InactiveDays: daysText, OnlyFlagged: only,
		AllURL:     auditURL(dashboard.NexoAuditPathReview, url.Values{"inactive_days": {daysText}}),
		FlaggedURL: auditURL(dashboard.NexoAuditPathReview, url.Values{"inactive_days": {daysText}, "only": {"flags"}}),
	}
	page.Generated, page.GeneratedISO = auditTime(rep.GeneratedAt)
	exportQuery := url.Values{"inactive_days": {daysText}}
	if only {
		exportQuery.Set("only", "flags")
	}
	for _, f := range []struct {
		format string
		dst    *string
	}{{nexoReviewFormatCSV, &page.ExportCSVURL}, {nexoReviewFormatJSON, &page.ExportJSONURL}} {
		q := url.Values{"format": {f.format}}
		for k, v := range exportQuery {
			q[k] = v
		}
		*f.dst = auditURL(dashboard.NexoAuditPathReview+"/export", q)
	}
	if rep.LastReview != nil {
		l := auditReviewLast(now, *rep.LastReview)
		page.Last = &l
	}
	for _, rec := range rep.RecentReviews {
		page.Recent = append(page.Recent, auditReviewLast(now, rec))
	}
	sm := rep.Summary
	page.Stats = []dashboard.NexoKV{
		{Key: "Personas y cuentas", Value: strconv.Itoa(sm.People)},
		{Key: "Habilitadas", Value: strconv.Itoa(sm.Enabled)},
		{Key: "Personas inactivas", Value: strconv.Itoa(sm.Inactive)},
		{Key: "Cuentas de servicio sin uso", Value: strconv.Itoa(sm.StaleServiceAccounts)},
		{Key: "Con rol de plataforma", Value: strconv.Itoa(sm.PlatformStaff)},
		{Key: "Superadmins habilitados", Value: strconv.Itoa(sm.SuperadminsEnabled)},
		{Key: "Tokens de superadmin, admin y auditor que vencen en 30 días", Value: strconv.Itoa(sm.PrivilegedTokensExpiring)},
		{Key: "Roles de Engram desalineados", Value: strconv.Itoa(sm.RoleDrift)},
		{Key: "Tokens activos", Value: strconv.Itoa(sm.ActiveTokens)},
		{Key: "Tokens sin caducidad", Value: strconv.Itoa(sm.TokensNoExpiry)},
		{Key: "Tokens nunca usados", Value: strconv.Itoa(sm.TokensNeverUse)},
		{Key: "Tokens que vencen en 30 días", Value: strconv.Itoa(sm.ExpiringIn30d)},
	}
	page.Policy = []string{"Una persona se marca inactiva si no usa su cuenta en " + saPanelDays(rep.InactiveDays) + " (umbral de esta pantalla)."}
	if rep.DisableInactive > 0 {
		page.Policy = append(page.Policy, "El servidor deshabilita solo a las personas sin uso en "+saPanelDays(rep.DisableInactive)+" (NEXO_DISABLE_INACTIVE_DAYS).")
	} else {
		page.Policy = append(page.Policy, "El servidor no deshabilita personas por inactividad (NEXO_DISABLE_INACTIVE_DAYS vacío): hay que hacerlo a mano.")
	}
	if rep.SAStaleDays > 0 {
		page.Policy = append(page.Policy, "Una cuenta de servicio sin uso en "+saPanelDays(rep.SAStaleDays)+" deja una alerta y la marca \"Sin uso\"; nunca se deshabilita sola (NEXO_SA_STALE_DAYS).")
	} else {
		page.Policy = append(page.Policy, "El aviso de cuentas de servicio sin uso está desactivado (NEXO_SA_STALE_DAYS=0).")
	}
	for _, pe := range rep.People {
		if only && len(pe.Flags) == 0 {
			continue
		}
		page.People = append(page.People, auditReviewPerson(now, pe))
		for _, t := range pe.Tokens {
			row := auditReviewToken(now, auditFirst(pe.DisplayName, pe.Username), t)
			if only && len(row.Flags) == 0 {
				continue
			}
			page.Tokens = append(page.Tokens, row)
		}
	}
	s.auditPanelWrite(w, r, status, title, dashboard.NexoAuditReviewPage(page))
}

func auditSummaryInt(m map[string]any, key string) (int, bool) {
	f, ok := m[key].(float64)
	return int(f), ok
}

func auditReviewLast(now time.Time, rec evidence.ReviewRecord) dashboard.NexoReviewLast {
	when, iso := auditTime(rec.OccurredAt)
	out := dashboard.NexoReviewLast{When: when, WhenISO: iso, Age: auditAgo(now, rec.OccurredAt), By: auditFirst(rec.Reviewer, "desconocido"), Notes: auditTruncate(rec.Notes, 600), Hash: rec.ReportSHA256}
	if rec.NoteWithheld { // NX-07: sin ámbito verificable, el texto libre no se muestra
		out.Notes = "guardadas; el texto no se muestra aquí porque una revisión de accesos no tiene un ámbito para verificar quién puede leerlo."
	}
	var parts []string
	for _, f := range []struct{ key, text string }{
		{"people", "personas y cuentas"}, {"inactive", "inactivas"}, {"tokens_no_expiry", "tokens sin caducidad"}, {"service_accounts_stale", "cuentas de servicio sin uso"},
	} {
		if n, ok := auditSummaryInt(rec.Summary, f.key); ok {
			parts = append(parts, strconv.Itoa(n)+" "+f.text)
		}
	}
	if len(parts) > 0 {
		out.Summary = "Lo que se revisó entonces: " + strings.Join(parts, ", ") + "."
	}
	return out
}

func auditFlag(flag string) dashboard.NexoFlag {
	switch flag {
	case "inactive":
		return dashboard.NexoFlag{Label: "Inactiva", Class: "nx-chip nx-chip-exp"}
	case "stale":
		return dashboard.NexoFlag{Label: "Sin uso", Class: "nx-chip nx-chip-exp"}
	case "single_superadmin":
		return dashboard.NexoFlag{Label: "Único superadmin", Class: "nx-chip nx-chip-err"}
	case "token_expiring":
		return dashboard.NexoFlag{Label: "Token por vencer", Class: "nx-chip nx-chip-exp"}
	case "role_drift":
		return dashboard.NexoFlag{Label: "Rol de Engram desalineado", Class: "nx-chip nx-chip-err"}
	case "token_no_expiry":
		return dashboard.NexoFlag{Label: "Token sin caducidad", Class: "nx-chip nx-chip-err"}
	case "token_never_used":
		return dashboard.NexoFlag{Label: "Token nunca usado", Class: "nx-chip nx-chip-exp"}
	}
	return dashboard.NexoFlag{Label: flag, Class: "nx-chip nx-chip-ret"}
}

func auditScopeLabel(t string) string {
	switch t {
	case "project":
		return "proyecto"
	case "space":
		return "espacio"
	case "platform":
		return "plataforma"
	}
	return t
}

func auditBinding(b evidence.ReviewBinding) string {
	s := b.Role + " en " + auditScopeLabel(b.ScopeType)
	if b.ScopeKey != "" && b.ScopeKey != "*" {
		s += " " + b.ScopeKey
	}
	s += ", desde " + auditDate(b.Since)
	if b.GrantedBy != "" {
		s += ", asignado por " + b.GrantedBy
	}
	return s
}

func auditReviewPerson(now time.Time, pe evidence.ReviewPerson) dashboard.NexoReviewPerson {
	out := dashboard.NexoReviewPerson{
		Name: auditFirst(pe.DisplayName, pe.Username, "sin nombre"), Sub: auditFirst(pe.Username, pe.Email), Kind: pe.Kind, Enabled: pe.Enabled,
		Platform: auditFirst(pe.PlatformRole, "miembro"), TokenCount: strconv.Itoa(len(pe.Tokens)),
	}
	if pe.Kind == "service_account" {
		out.Sub = strings.TrimSpace(out.Sub + " (cuenta de servicio)")
	}
	if pe.PlatformSince != nil {
		out.PlatformSub = "desde " + auditDate(*pe.PlatformSince)
		if pe.PlatformBy != "" {
			out.PlatformSub += ", asignado por " + pe.PlatformBy
		}
	}
	for _, b := range pe.Scopes {
		out.Scopes = append(out.Scopes, auditBinding(b))
	}
	for _, g := range pe.ProjectGrants {
		out.Grants = append(out.Grants, g.ScopeKey)
	}
	if pe.LastActivityAt == nil {
		out.LastActivity = "nunca"
	} else {
		out.LastActivity = auditDate(*pe.LastActivityAt) + " (" + auditAgo(now, *pe.LastActivityAt) + ")"
	}
	for _, f := range pe.Flags {
		out.Flags = append(out.Flags, auditFlag(f))
	}
	return out
}

func auditReviewToken(now time.Time, owner string, t evidence.ReviewToken) dashboard.NexoReviewToken {
	out := dashboard.NexoReviewToken{Owner: owner, Name: auditDash(t.Name), Scope: auditDash(t.Scope), Created: auditDate(t.CreatedAt), Expires: "sin caducidad", LastUsed: "nunca"}
	if t.ExpiresAt != nil {
		out.Expires = auditDate(*t.ExpiresAt)
	}
	if t.LastUsedAt != nil {
		out.LastUsed = auditDate(*t.LastUsedAt) + " (" + auditAgo(now, *t.LastUsedAt) + ")"
	}
	if t.NoExpiry {
		out.Flags = append(out.Flags, auditFlag("token_no_expiry"))
	}
	if t.Expired {
		out.Flags = append(out.Flags, dashboard.NexoFlag{Label: "Vencido", Class: "nx-chip nx-chip-ret"})
	} else if t.ExpiresAt != nil && t.ExpiresAt.Sub(now) < 30*24*time.Hour {
		out.Flags = append(out.Flags, auditFlag("token_expiring")) // SG-06
	}
	if t.LastUsedAt == nil {
		out.Flags = append(out.Flags, auditFlag("token_never_used"))
	}
	return out
}

// auditPanelReviewComplete registra la revisión con la misma lógica que
// POST /nexo/admin/access-review:complete.
func (s *CloudServer) auditPanelReviewComplete(w http.ResponseWriter, r *http.Request, _ *authz.Service, id authz.Identity) {
	days := 0
	if raw := strings.TrimSpace(r.FormValue("inactive_days")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 3650 {
			s.auditPanelReviewRender(w, r, id, evidence.ReviewInactiveDays, false, "", "Los días sin uso deben ser un número entre 1 y 3650.", http.StatusBadRequest)
			return
		}
		days = n
	}
	rep, err := evidence.BuildAccessReview(r.Context(), s.nexoDB(), days, time.Now())
	if err != nil {
		nexoInternalError(w, "nexo access review", err)
		return
	}
	rec, err := evidence.CompleteReview(r.Context(), s.nexoDB(), rep, evidence.Reviewer{
		PrincipalID: id.PrincipalID, TokenID: id.TokenID, Label: id.Label, ClientIP: id.ClientIP, UserAgent: id.UserAgent,
	}, r.FormValue("notes"))
	if err != nil {
		nexoInternalError(w, "nexo access review", err)
		return
	}
	if !s.auditPanelRecord(w, r, nexoAuditActionReviewComplete, "", map[string]any{"review_id": rec.ID, "report_sha256": rec.ReportSHA256}) {
		return
	}
	http.Redirect(w, r, auditURL(dashboard.NexoAuditPathReview, url.Values{"inactive_days": {strconv.Itoa(rep.InactiveDays)}, "ok": {"review"}}), http.StatusSeeOther)
}
