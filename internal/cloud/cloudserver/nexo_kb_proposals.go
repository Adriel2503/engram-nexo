package cloudserver

// Nexo IA · K6 (KP-13, SG-07) — propuestas de cambio en el panel.
//
//	GET  /dashboard/kb/proposals[?status=]                        propuestas de todos tus espacios (identity)
//	GET  /dashboard/kb/proposals/badge                            número de pendientes para revisar (fragmento htmx)
//	GET  /dashboard/kb/spaces/{slug}/proposals[?status=]          propuestas del espacio (space.read)
//	GET  /dashboard/kb/spaces/{slug}/proposals/{id}               detalle y comparación (space.read)
//	POST /dashboard/kb/spaces/{slug}/proposals/{id}/approve       aprobar y publicar (space.review)
//	POST /dashboard/kb/spaces/{slug}/proposals/{id}/discard       descartar con motivo (space.review)
//
// Usa los mismos casos de uso que la API y la CLI (kbservice.ListProposals,
// GetProposal, ApproveProposal, DiscardProposal): la visibilidad (autor o
// space.review), las reglas y los eventos de auditoría (proposal.approved,
// proposal.discarded) son los de siempre. La sesión hereda el alcance del token
// (un token read no decide) y los formularios llevan el token CSRF del panel.
// El texto propuesto y la versión vigente se muestran con mdhtml (Markdown sin
// HTML crudo).

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/dashboard"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/mdhtml"
	kbservice "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	kbstore "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

func (s *CloudServer) registerNexoKBProposalsDashboard(mux *http.ServeMux) {
	s.nexoRoute(mux, "GET", "/dashboard/kb/proposals", "identity", s.requireDashboardSession(s.nexoKBPage(s.kbProposalsAll)))
	s.nexoRoute(mux, "GET", "/dashboard/kb/proposals/badge", "identity", s.requireDashboardSession(s.nexoKBPage(s.kbProposalsBadge)))
	s.nexoRoute(mux, "GET", "/dashboard/kb/spaces/{slug}/proposals", "space.read", s.requireDashboardSession(s.nexoKBPage(s.kbProposalsSpace)))
	s.nexoRoute(mux, "GET", "/dashboard/kb/spaces/{slug}/proposals/{id}", "space.read", s.requireDashboardSession(s.nexoKBPage(s.kbProposalDetail)))
	s.nexoRoute(mux, "POST", "/dashboard/kb/spaces/{slug}/proposals/{id}/approve", "space.review", s.requireDashboardSession(s.nexoKBPage(s.kbProposalApprove)))
	s.nexoRoute(mux, "POST", "/dashboard/kb/spaces/{slug}/proposals/{id}/discard", "space.review", s.requireDashboardSession(s.nexoKBPage(s.kbProposalDiscard)))
}

const kbProposalsLimit = 100

// ─── Presentación ────────────────────────────────────────────────────────────

func kbProposalStatus(status string) (label, class string) {
	switch status {
	case domain.ProposalOpen:
		return "Pendiente", "nx-chip-exp"
	case domain.ProposalApproved:
		return "Aprobada", "nx-chip-ok"
	case domain.ProposalDiscarded:
		return "Descartada", "nx-chip-ret"
	case domain.ProposalWithdrawn:
		return "Retirada por su autor", "nx-chip-ret"
	}
	return status, "nx-chip-ret"
}

// kbProposalTarget dice si la propuesta cambia un documento o crea uno nuevo.
func kbProposalTarget(p kbstore.Proposal) string {
	switch {
	case p.TargetDocID != "" && p.DocKey != "":
		return "Cambia " + p.DocKey + ", versión " + strconv.Itoa(p.BaseVersion)
	case p.DocKey != "":
		return "Documento nuevo (" + p.DocKey + ")"
	}
	return "Documento nuevo"
}

func kbProposalURL(slug, id string) string {
	return "/dashboard/kb/spaces/" + url.PathEscape(slug) + "/proposals/" + url.PathEscape(id)
}

func kbProposalRows(items []kbstore.Proposal, showSpace bool) []dashboard.NexoProposalRow {
	out := make([]dashboard.NexoProposalRow, 0, len(items))
	for _, p := range items {
		label, class := kbProposalStatus(p.Status)
		row := dashboard.NexoProposalRow{Href: kbProposalURL(p.Space, p.ID), Title: p.Title, Target: kbProposalTarget(p),
			Author: p.ProposedBy, Date: kbDay(p.CreatedAt), StatusLabel: label, StatusClass: class}
		if showSpace {
			row.Space = p.Space
		}
		out = append(out, row)
	}
	return out
}

// kbProposalFilters arma los filtros por estado; base es la ruta del listado.
func kbProposalFilters(base, status string) []dashboard.NexoFilter {
	var out []dashboard.NexoFilter
	for _, f := range []struct{ value, label string }{
		{domain.ProposalOpen, "Pendientes"}, {domain.ProposalApproved, "Aprobadas"},
		{domain.ProposalDiscarded, "Descartadas"}, {domain.ProposalWithdrawn, "Retiradas"},
	} {
		href := base
		if f.value != domain.ProposalOpen {
			href += "?status=" + f.value
		}
		out = append(out, dashboard.NexoFilter{Label: f.label, Href: href, Current: f.value == status})
	}
	return out
}

func kbProposalStatusParam(r *http.Request) string {
	switch st := r.URL.Query().Get("status"); st {
	case domain.ProposalApproved, domain.ProposalDiscarded, domain.ProposalWithdrawn:
		return st
	}
	return domain.ProposalOpen
}

func kbProposalEmpty(status string) string {
	switch status {
	case domain.ProposalApproved:
		return "Todavía no hay propuestas aprobadas."
	case domain.ProposalDiscarded:
		return "No hay propuestas descartadas."
	case domain.ProposalWithdrawn:
		return "No hay propuestas retiradas por su autor."
	}
	return "No hay propuestas pendientes."
}

const kbProposalHowTo = "Las propuestas las envían las personas con rol proponente o superior, desde su asistente (herramienta nexo_propose) o con la API. Ninguna es oficial hasta que alguien con rol publicador o responsable la aprueba."

// ─── Listados ────────────────────────────────────────────────────────────────

func (s *CloudServer) kbProposalsAll(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	status := kbProposalStatusParam(r)
	items, err := svc.ListProposals(r.Context(), id, kbservice.ProposalQuery{Status: status, Limit: kbProposalsLimit})
	if err != nil {
		s.kbError(w, r, err)
		return
	}
	page := dashboard.NexoProposalsPage{
		Title:   "Propuestas de cambio",
		Lead:    "Aquí ves las propuestas de los espacios donde puedes revisar y las que hiciste tú. Aprobar publica una versión nueva; descartar exige un motivo.",
		Filters: kbProposalFilters("/dashboard/kb/proposals", status),
		Items:   kbProposalRows(items, true),
		Empty:   kbProposalEmpty(status),
	}
	if status == domain.ProposalOpen {
		page.Hint = kbProposalHowTo
	}
	s.kbRenderComponent(w, r, http.StatusOK, "Propuestas", "proposals", dashboard.NexoProposalsPageView(page))
}

func (s *CloudServer) kbProposalsSpace(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	slug := r.PathValue("slug")
	status := kbProposalStatusParam(r)
	if _, err := svc.GetSpace(r.Context(), id, slug); err != nil {
		s.kbError(w, r, err)
		return
	}
	items, err := svc.ListProposals(r.Context(), id, kbservice.ProposalQuery{Space: slug, Status: status, Limit: kbProposalsLimit})
	if err != nil {
		s.kbError(w, r, err)
		return
	}
	az, _ := s.nexoAuthz()
	head := s.kbSpaceHead(r, svc, id, slug, "proposals", 0)
	if n, ok := s.kbOpenProposalCount(r, svc, id, slug); ok {
		head.OpenProposals = n
	}
	lead := "Ves las propuestas que hiciste tú en este espacio. Quien revisa las aprueba o las descarta."
	if az != nil {
		if d, err := az.Engine().Can(r.Context(), id, authz.SpaceScope(slug), authz.CapSpaceReview); err == nil && d.Allowed {
			lead = "Estas son las propuestas de cambio de este espacio. Aprobar publica una versión nueva; descartar exige un motivo."
		}
	}
	page := dashboard.NexoProposalsPage{
		Title:   "Propuestas de " + head.Name,
		Lead:    lead,
		Head:    &head,
		Filters: kbProposalFilters(kbProposalsSpaceURL(slug), status),
		Items:   kbProposalRows(items, false),
		Empty:   kbProposalEmpty(status),
	}
	if status == domain.ProposalOpen {
		page.Hint = kbProposalHowTo
	}
	s.kbRenderComponent(w, r, http.StatusOK, "Propuestas de "+head.Name, "kb", dashboard.NexoProposalsPageView(page))
}

func kbProposalsSpaceURL(slug string) string {
	return "/dashboard/kb/spaces/" + url.PathEscape(slug) + "/proposals"
}

// kbProposalsBadge es el fragmento de la insignia del menú: cuántas propuestas
// abiertas puede revisar quien mira. Vacío si no revisa nada o no hay pendientes.
func (s *CloudServer) kbProposalsBadge(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	n := 0
	if spaces, err := svc.ListSpaces(r.Context(), id); err == nil {
		reviewable := map[string]bool{}
		for _, sp := range spaces {
			if kbHasCap(sp.Capabilities, authz.CapSpaceReview) {
				reviewable[sp.Slug] = true
			}
		}
		if len(reviewable) > 0 {
			if items, err := svc.ListProposals(r.Context(), id, kbservice.ProposalQuery{Status: domain.ProposalOpen, Limit: 200}); err == nil {
				for _, p := range items {
					if reviewable[p.Space] {
						n++
					}
				}
			}
		}
	}
	if n == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, `<span class="nx-badge">%d<span class="nx-visually-hidden"> por revisar</span></span>`, n)
}

// ─── Detalle y decisión ──────────────────────────────────────────────────────

// kbProposalState es lo que una acción deja para la página.
type kbProposalState struct {
	notice, noticeLink, noticeLinkText, err string
	status                                  int
	forceNeeded                             bool
	note, reason                            string
}

// kbProposalErrText traduce un error del servicio de propuestas.
func kbProposalErrText(err error) (status int, msg string, force bool) {
	var de *domain.Error
	if !errors.As(err, &de) {
		nexoLogInternal("dashboard kb proposals", err)
		return http.StatusInternalServerError, "Ocurrió un error interno. Vuelve a intentarlo; si se repite, avisa al administrador.", false
	}
	switch {
	case de.Code == domain.CodeProposalState:
		return http.StatusConflict, "La propuesta ya no está abierta: otra persona la revisó mientras tanto. Recarga la página para ver cómo quedó.", false
	case de.Code == domain.CodeVersionConflict:
		return http.StatusConflict, "El documento cambió desde que se hizo la propuesta. Revisa la versión vigente; si el cambio sigue siendo válido, marca \"Aprobar aunque el documento cambió\" y vuelve a aprobar.", true
	case de.Code == domain.CodeSecret:
		return http.StatusUnprocessableEntity, "El texto contiene secretos evidentes (claves o contraseñas) y no se puede publicar. Pide al autor que los quite. Detalle: " + de.Message, false
	}
	switch de.Kind {
	case domain.KindForbidden:
		return http.StatusForbidden, "Tu rol o tu token no permiten decidir esta propuesta. Solo quien tiene rol publicador o responsable en el espacio, con un token de escritura, la aprueba o la descarta.", false
	case domain.KindNotFound:
		return http.StatusNotFound, "No encontramos la propuesta, o no tienes acceso.", false
	case domain.KindInvalid:
		return http.StatusUnprocessableEntity, "No se pudo completar: " + de.Message, false
	case domain.KindConflict:
		return http.StatusConflict, "No se pudo completar: " + de.Message, false
	}
	nexoLogInternal("dashboard kb proposals", err)
	return http.StatusInternalServerError, "Ocurrió un error interno. Vuelve a intentarlo; si se repite, avisa al administrador.", false
}

func (s *CloudServer) kbProposalDetail(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	s.kbProposalRender(w, r, svc, id, r.PathValue("slug"), r.PathValue("id"), kbProposalState{})
}

func (s *CloudServer) kbProposalRender(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity, slug, propID string, st kbProposalState) {
	ctx := r.Context()
	p, err := svc.GetProposal(ctx, id, propID)
	if err != nil {
		s.kbError(w, r, err)
		return
	}
	if p.Space != slug { // la propuesta es de otro espacio: no revelar nada
		s.kbError(w, r, domain.Errorf(domain.KindNotFound, domain.CodeNotFound, "proposal %q not found", propID))
		return
	}
	canReview := false
	if az, ok := s.nexoAuthz(); ok {
		if d, err := az.Engine().Can(ctx, id, authz.SpaceScope(p.Space), authz.CapSpaceReview); err == nil {
			canReview = d.Allowed
		}
	}
	label, class := kbProposalStatus(p.Status)
	open := p.Status == domain.ProposalOpen
	head := s.kbSpaceHead(r, svc, id, p.Space, "proposals", 0)
	page := dashboard.NexoProposalPage{
		Head: head, CSRF: s.kbCSRFToken(r), Notice: st.notice, NoticeLink: st.noticeLink, NoticeLinkT: st.noticeLinkText, Error: st.err,
		ID: p.ID, Title: p.Title, StatusLabel: label, StatusClass: class, Open: open,
		Author: p.ProposedBy, Date: kbDay(p.CreatedAt), Target: kbProposalTarget(p), Category: p.Category,
		Rationale: p.Rationale, ProposedHTML: mdhtml.Render(p.ContentMD),
		CanReview: canReview && open, NeedsKey: p.DocKey == "", DocKey: "",
		ApproveURL: kbProposalURL(p.Space, p.ID) + "/approve", DiscardURL: kbProposalURL(p.Space, p.ID) + "/discard",
		FormNote: st.note, FormReason: st.reason,
	}
	if p.ValidFrom != nil {
		page.ValidFrom = p.ValidFrom.UTC().Format("2006-01-02")
	}
	if p.ValidUntil != nil {
		page.ValidUntil = p.ValidUntil.UTC().Format("2006-01-02")
	}
	switch {
	case p.ValidFrom != nil && p.ValidUntil != nil:
		page.Vigencia = "Vigente desde " + page.ValidFrom + " hasta " + page.ValidUntil
	case p.ValidFrom != nil:
		page.Vigencia = "Vigente desde " + page.ValidFrom
	case p.ValidUntil != nil:
		page.Vigencia = "Vigente hasta " + page.ValidUntil
	}
	// Versión vigente del documento que la propuesta cambia (o que ya existe
	// con la clave elegida).
	if p.DocKey != "" {
		if view, err := svc.GetDocument(ctx, id, p.Space, p.DocKey, 0); err == nil {
			page.HasCurrent = true
			page.CurrentHTML = mdhtml.Render(view.Version.ContentMD)
			page.CurrentName = fmt.Sprintf("Versión vigente (versión %d)", view.Document.CurrentVersion)
			switch {
			case p.TargetDocID == "":
				page.Stale = "El documento " + p.DocKey + " se publicó después de esta propuesta. Compara con la versión vigente antes de aprobar."
			case view.Document.CurrentVersion != p.BaseVersion:
				page.Stale = fmt.Sprintf("El documento cambió desde que se hizo la propuesta: el autor partió de la versión %d y hoy es la %d. Compara con la versión vigente antes de aprobar.",
					p.BaseVersion, view.Document.CurrentVersion)
			}
		}
	}
	page.ShowForce = page.Stale != "" || st.forceNeeded
	if !open {
		page.Decision = kbProposalDecision(p)
	}
	status := st.status
	if status == 0 {
		status = http.StatusOK
	}
	s.kbRenderComponent(w, r, status, p.Title, "kb", dashboard.NexoProposalPageView(page))
}

// kbProposalDecision resume cómo se cerró una propuesta.
func kbProposalDecision(p kbstore.Proposal) string {
	when := ""
	if p.DecidedAt != nil {
		when = " el " + kbDay(p.DecidedAt.UTC())
	}
	by := p.ReviewedBy
	if by == "" {
		by = "una persona del equipo"
	}
	note := ""
	if p.ReviewNote != "" {
		note = " Nota: " + p.ReviewNote
	}
	switch p.Status {
	case domain.ProposalApproved:
		return "Aprobada por " + by + when + "; ya es una versión del documento." + note
	case domain.ProposalDiscarded:
		return "Descartada por " + by + when + "." + strings.Replace(note, " Nota: ", " Motivo: ", 1)
	case domain.ProposalWithdrawn:
		return "Retirada por su autor" + when + "." + note
	}
	return ""
}

// kbParseDay lee una fecha de un campo date (vacío = nil).
func kbParseDay(raw string) (*time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, false
	}
	return &t, true
}

func (s *CloudServer) kbProposalApprove(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	slug, propID := r.PathValue("slug"), r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulario inválido", http.StatusBadRequest)
		return
	}
	st := kbProposalState{note: strings.TrimSpace(r.PostForm.Get("note"))}
	fail := func(status int, msg string, force bool) {
		st.status, st.err, st.forceNeeded = status, msg, force
		s.kbProposalRender(w, r, svc, id, slug, propID, st)
	}
	vf, ok1 := kbParseDay(r.PostForm.Get("valid_from"))
	vu, ok2 := kbParseDay(r.PostForm.Get("valid_until"))
	if !ok1 || !ok2 {
		fail(http.StatusUnprocessableEntity, "Una de las fechas de vigencia no es válida. Usa el selector de fecha.", false)
		return
	}
	if vf != nil && vu != nil && !vu.After(*vf) {
		fail(http.StatusUnprocessableEntity, "La fecha \"hasta\" debe ser posterior a la fecha \"desde\".", false)
		return
	}
	// La propuesta tiene que ser de este espacio (si no, 404 sin detalle).
	if p, err := svc.GetProposal(r.Context(), id, propID); err != nil || p.Space != slug {
		if err == nil {
			err = domain.Errorf(domain.KindNotFound, domain.CodeNotFound, "proposal %q not found", propID)
		}
		s.kbError(w, r, err)
		return
	}
	res, err := svc.ApproveProposal(r.Context(), id, propID, kbservice.ApproveInput{
		Note: st.note, DocKey: strings.TrimSpace(r.PostForm.Get("doc_key")), ValidFrom: vf, ValidUntil: vu,
		Force: r.PostForm.Get("force") == "true",
	})
	if err != nil {
		status, msg, force := kbProposalErrText(err)
		fail(status, msg, force)
		return
	}
	title := res.Result.Title
	if title == "" {
		title = res.Proposal.Title
	}
	next := kbProposalState{
		notice:         fmt.Sprintf("Aprobada: publicamos la versión %d de %q.", res.Result.Version, title),
		noticeLink:     kbDocURL(slug, res.Result.DocKey, 0, ""),
		noticeLinkText: "Ver el documento",
	}
	if res.Result.Status == "unchanged" {
		next.notice = fmt.Sprintf("Aprobada. El texto es idéntico a la versión vigente de %q, así que no se creó otra versión.", title)
	}
	s.kbProposalRender(w, r, svc, id, slug, propID, next)
}

func (s *CloudServer) kbProposalDiscard(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	slug, propID := r.PathValue("slug"), r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulario inválido", http.StatusBadRequest)
		return
	}
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if p, err := svc.GetProposal(r.Context(), id, propID); err != nil || p.Space != slug {
		if err == nil {
			err = domain.Errorf(domain.KindNotFound, domain.CodeNotFound, "proposal %q not found", propID)
		}
		s.kbError(w, r, err)
		return
	}
	if reason == "" {
		s.kbProposalRender(w, r, svc, id, slug, propID, kbProposalState{status: http.StatusUnprocessableEntity, err: "Escribe el motivo del descarte: el autor lo verá."})
		return
	}
	if _, err := svc.DiscardProposal(r.Context(), id, propID, reason); err != nil {
		status, msg, _ := kbProposalErrText(err)
		s.kbProposalRender(w, r, svc, id, slug, propID, kbProposalState{status: status, err: msg, reason: reason})
		return
	}
	s.kbProposalRender(w, r, svc, id, slug, propID, kbProposalState{notice: "Descartada. La propuesta se conserva con tu motivo."})
}
