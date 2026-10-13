package cloudserver

// Nexo IA · K6 (KP-13) — propuestas en el panel, de punta a punta contra
// Postgres (CLOUDSTORE_TEST_DSN): listado global y por espacio, insignia del
// menú, detalle con la versión vigente y el texto propuesto (Markdown seguro),
// aprobar y descartar con permisos, CSRF, conflicto de versión con force y un
// evento de auditoría por acción.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// kbBT cambia ~ por comilla invertida (los cuerpos de prueba son cadenas crudas).
func kbBT(s string) string { return strings.ReplaceAll(s, "~", "`") }

func kbPublishDoc(t *testing.T, env *nexoSecEnv, who, path, title, body string) {
	t.Helper()
	body = kbBT(body)
	batch := map[string]any{"documents": []map[string]any{{"path": path, "content": "---\ncategory: lineamiento\ntitle: " + title + "\n---\n" + body}}}
	raw, _ := json.Marshal(batch)
	if rec := env.req(http.MethodPost, "/nexo/kb/v1/spaces/arq/documents:batch", who, string(raw), "10.0.0.5"); rec.Code != http.StatusOK {
		t.Fatalf("publicar %s: %d %s", path, rec.Code, rec.Body.String())
	}
}

func kbPropose(t *testing.T, env *nexoSecEnv, who, payload string) string {
	t.Helper()
	rec := env.req(http.MethodPost, "/nexo/kb/v1/spaces/arq/proposals", who, kbBT(payload), "10.0.0.5")
	if rec.Code != http.StatusCreated {
		t.Fatalf("proponer: %d %s", rec.Code, rec.Body.String())
	}
	var p struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return p.ID
}

func (e *nexoSecEnv) proposalStatus(id string) string {
	e.t.Helper()
	var st string
	if err := e.db.QueryRow(`SELECT status FROM nexo_kb_proposals WHERE id = $1::uuid`, id).Scan(&st); err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *nexoSecEnv) kbEvents(action, person string) int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM nexo_kb_events WHERE action = $1 AND space_slug = 'arq' AND actor_principal_id = $2::bigint`, action, e.ids[person])
}

func TestNexoKBProposalsPanel(t *testing.T) {
	env := kbPanelSetup(t)
	ana, luis, carla := env.panel("ana"), env.panel("luis"), env.panel("carla")
	if rec := env.req(http.MethodPut, "/nexo/authz/v1/scopes/space/arq/members/luis", "ana", `{"role":"proponente"}`, "10.0.0.5"); rec.Code != http.StatusOK {
		t.Fatalf("luis proponente: %d %s", rec.Code, rec.Body.String())
	}
	kbPublishDoc(t, env, "ana", "logging.md", "Logging", "# Logging\n\nUsar ~Information~ y ~Error~.\n")

	changeID := kbPropose(t, env, "luis", `{"doc_key":"logging","title":"Regla de trazas","rationale":"Falta el nivel Warning","content":"# Logging\n\nUsar ~Information~, ~Warning~ y ~Error~.\n","valid_from":"2030-01-01T00:00:00Z"}`)
	newID := kbPropose(t, env, "luis", `{"title":"Guía de despliegue","content":"# Despliegue\n\nPasos.\n\n<script>alert(1)</script>\n\n[x](javascript:alert(2))\n"}`)
	discardID := kbPropose(t, env, "luis", `{"title":"Cambio dudoso","content":"# Dudoso\n\nTexto.\n"}`)
	propURL := func(id string) string { return "/dashboard/kb/spaces/arq/proposals/" + id }
	anaCSRF, luisCSRF := ana.csrf(""), luis.csrf("")

	// ─── Menú, listado global y por espacio ──────────────────────────────────
	rec := ana.get("/dashboard/kb")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `href="/dashboard/kb/proposals"`) || !strings.Contains(rec.Body.String(), `hx-get="/dashboard/kb/proposals/badge"`) {
		t.Fatalf("la pestaña Propuestas con la insignia: %d", rec.Code)
	}
	rec = ana.get("/dashboard/kb/proposals/badge")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `class="nx-badge">3<`) {
		t.Fatalf("insignia del revisor: %d %q", rec.Code, rec.Body.String())
	}
	if rec = luis.get("/dashboard/kb/proposals/badge"); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("el proponente no revisa: %q", rec.Body.String())
	}
	rec = ana.get("/dashboard/kb/proposals")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Propuestas de cambio") || !strings.Contains(body, "Regla de trazas") || !strings.Contains(body, "Guía de despliegue") ||
		!strings.Contains(body, "arq: Cambia logging, versión 1. Propuso luis") || !strings.Contains(body, "Documento nuevo") || !strings.Contains(body, "Pendiente") {
		t.Fatalf("listado global del revisor: %d %s", rec.Code, body)
	}
	rec = luis.get("/dashboard/kb/proposals")
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), "/proposals/") < 3 || !strings.Contains(rec.Body.String(), "Regla de trazas") {
		t.Fatalf("el autor ve las suyas: %d", rec.Code)
	}
	rec = ana.get("/dashboard/kb/spaces/arq/proposals")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Propuestas de Arquitectura") || !strings.Contains(rec.Body.String(), "Propuestas (3)") {
		t.Fatalf("listado del espacio: %d %s", rec.Code, rec.Body.String())
	}
	if rec = ana.get("/dashboard/kb/spaces/arq/proposals?status=approved"); !strings.Contains(rec.Body.String(), "Todavía no hay propuestas aprobadas") {
		t.Fatalf("filtro de aprobadas vacío: %s", rec.Body.String())
	}
	if rec = ana.get("/dashboard/kb/spaces/arq"); !strings.Contains(rec.Body.String(), "Propuestas (3)") || !strings.Contains(rec.Body.String(), "/dashboard/kb/spaces/arq/members") {
		t.Fatalf("la página del espacio enlaza Miembros y Propuestas")
	}
	if rec = carla.get("/dashboard/kb/proposals"); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Regla de trazas") {
		t.Fatalf("un administrador sin rol de revisión no ve propuestas ajenas: %d", rec.Code)
	}
	if rec = carla.get(propURL(changeID)); rec.Code != http.StatusNotFound {
		t.Fatalf("propuesta ajena para quien no revisa: %d", rec.Code)
	}

	// ─── Detalle: comparación, Markdown seguro y controles según el rol ──────
	rec = ana.get(propURL(changeID))
	body = rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Regla de trazas") || !strings.Contains(body, "Cambia logging, versión 1") ||
		!strings.Contains(body, "Falta el nivel Warning") || !strings.Contains(body, "Versión vigente (versión 1)") ||
		!strings.Contains(body, "<code>Warning</code>") || !strings.Contains(body, "Vigente desde 2030-01-01") ||
		!strings.Contains(body, "Aprobar y publicar") || !strings.Contains(body, "Descartar propuesta") {
		t.Fatalf("detalle del revisor: %d %s", rec.Code, body)
	}
	if strings.Contains(body, "Aprobar aunque el documento cambió") {
		t.Fatal("la casilla de force solo aparece si el documento cambió")
	}
	rec = ana.get(propURL(newID))
	body = rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "<script>alert") || strings.Contains(body, "javascript:alert") || !strings.Contains(body, "&lt;script&gt;") ||
		strings.Contains(body, "Versión vigente") || !strings.Contains(body, "Clave del documento") {
		t.Fatalf("propuesta nueva y Markdown seguro: %d %s", rec.Code, body)
	}
	rec = luis.get(propURL(changeID))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Aprobar y publicar") || strings.Contains(rec.Body.String(), "csrf_token") {
		t.Fatalf("el autor ve su propuesta sin controles de decisión: %d", rec.Code)
	}
	if rec = ana.get("/dashboard/kb/spaces/otro/proposals/" + changeID); rec.Code != http.StatusNotFound {
		t.Fatalf("propuesta de otro espacio: %d", rec.Code)
	}
	if rec = ana.get(propURL("no-es-un-id")); rec.Code != http.StatusNotFound {
		t.Fatalf("id inválido: %d", rec.Code)
	}

	// ─── Denegaciones: autor, sin CSRF, token read ───────────────────────────
	rec = luis.post(propURL(changeID)+"/approve", kbForm(luisCSRF))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Tu rol o tu token no permiten decidir") || env.proposalStatus(changeID) != "open" {
		t.Fatalf("el autor aprueba su propuesta: %d %s", rec.Code, rec.Body.String())
	}
	if rec = luis.post(propURL(changeID)+"/discard", kbForm(luisCSRF, "reason", "no")); rec.Code != http.StatusForbidden || env.proposalStatus(changeID) != "open" {
		t.Fatalf("el autor descarta: %d", rec.Code)
	}
	if n := env.count(`SELECT count(*) FROM nexo_authz_events WHERE action = 'authz.denied' AND scope_key = 'arq' AND actor_principal_id = $1::bigint AND details->>'action' = 'proposal.approve'`, env.ids["luis"]); n != 1 {
		t.Fatalf("la denegación de aprobar queda auditada: %d", n)
	}
	if rec = ana.post(propURL(changeID)+"/approve", nil); rec.Code != http.StatusForbidden || env.proposalStatus(changeID) != "open" {
		t.Fatalf("aprobar sin CSRF: %d", rec.Code)
	}
	if rec = ana.post(propURL(discardID)+"/discard", kbForm("falso", "reason", "x")); rec.Code != http.StatusForbidden || env.proposalStatus(discardID) != "open" {
		t.Fatalf("descartar con CSRF inválido: %d", rec.Code)
	}
	env.issue("ana", "ana-read", security.TokenIssue{Days: 30, Scope: "read"})
	anaRead := env.panel("ana-read")
	// Token read: el guardia del panel (NX-07) niega todo POST y la sesión no revisa, así que la propuesta ajena queda oculta (404), igual que por la API.
	if rec = anaRead.get(propURL(changeID)); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "Aprobar y publicar") {
		t.Fatalf("sesión read no ve la decisión: %d", rec.Code)
	}
	if rec = anaRead.post(propURL(changeID)+"/approve", kbForm(anaRead.csrf(""))); rec.Code != http.StatusForbidden || env.proposalStatus(changeID) != "open" {
		t.Fatalf("token read aprueba: %d %s", rec.Code, rec.Body.String())
	}

	// ─── Descartar: el motivo es obligatorio ─────────────────────────────────
	rec = ana.post(propURL(discardID)+"/discard", kbForm(anaCSRF, "reason", "  "))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Escribe el motivo del descarte") || env.proposalStatus(discardID) != "open" {
		t.Fatalf("descartar sin motivo: %d %s", rec.Code, rec.Body.String())
	}
	rec = ana.post(propURL(discardID)+"/discard", kbForm(anaCSRF, "reason", "Ya está cubierto por el lineamiento de logging"))
	body = rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Descartada. La propuesta se conserva") || env.proposalStatus(discardID) != "discarded" ||
		!strings.Contains(body, "Motivo: Ya está cubierto") || strings.Contains(body, "Aprobar y publicar") {
		t.Fatalf("descartar: %d %s", rec.Code, body)
	}
	if n := env.kbEvents(domain.ActionProposalDiscarded, "ana"); n != 1 {
		t.Fatalf("evento proposal.discarded: %d", n)
	}
	if rec = ana.post(propURL(discardID)+"/discard", kbForm(anaCSRF, "reason", "otra vez")); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ya no está abierta") {
		t.Fatalf("descartar dos veces: %d %s", rec.Code, rec.Body.String())
	}
	if rec = ana.get("/dashboard/kb/spaces/arq/proposals?status=discarded"); !strings.Contains(rec.Body.String(), "Cambio dudoso") || !strings.Contains(rec.Body.String(), "Descartada") {
		t.Fatalf("filtro de descartadas: %s", rec.Body.String())
	}

	// ─── Aprobar una propuesta nueva ─────────────────────────────────────────
	rec = ana.post(propURL(newID)+"/approve", kbForm(anaCSRF, "note", "Revisado", "doc_key", "guias/despliegue"))
	body = rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Aprobada: publicamos la versión 1") || !strings.Contains(body, "Ver el documento") ||
		env.proposalStatus(newID) != "approved" || strings.Contains(body, "Aprobar y publicar") || !strings.Contains(body, "Aprobada por ana") {
		t.Fatalf("aprobar propuesta nueva: %d %s", rec.Code, body)
	}
	if n := env.count(`SELECT count(*) FROM nexo_kb_documents WHERE doc_key = 'guias/despliegue' AND current_version = 1`); n != 1 {
		t.Fatalf("el documento publicado: %d", n)
	}
	if n := env.kbEvents(domain.ActionProposalApproved, "ana"); n != 1 {
		t.Fatalf("evento proposal.approved: %d", n)
	}
	if rec = ana.get("/dashboard/kb/spaces/arq/doc?doc=guias/despliegue"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<h1>Despliegue</h1>") {
		t.Fatalf("el documento aprobado se lee: %d %s", rec.Code, rec.Body.String())
	}
	if rec = ana.post(propURL(newID)+"/approve", kbForm(anaCSRF)); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ya no está abierta") {
		t.Fatalf("aprobar una propuesta cerrada: %d", rec.Code)
	}

	// ─── Documento que cambió: exige force ───────────────────────────────────
	kbPublishDoc(t, env, "ana", "logging.md", "Logging", "# Logging\n\nUsar ~Information~, ~Error~ y ~Fatal~.\n")
	rec = ana.get(propURL(changeID))
	if body = rec.Body.String(); !strings.Contains(body, "El documento cambió desde que se hizo la propuesta: el autor partió de la versión 1 y hoy es la 2") ||
		!strings.Contains(body, "Aprobar aunque el documento cambió") || !strings.Contains(body, "Versión vigente (versión 2)") {
		t.Fatalf("aviso de documento cambiado: %s", body)
	}
	rec = ana.post(propURL(changeID)+"/approve", kbForm(anaCSRF, "note", "ok"))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "El documento cambió desde que se hizo la propuesta") || env.proposalStatus(changeID) != "open" {
		t.Fatalf("aprobar sin force: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `value="ok"`) {
		t.Fatal("la nota escrita se conserva tras el error")
	}
	if rec = ana.post(propURL(changeID)+"/approve", kbForm(anaCSRF, "valid_from", "2031-01-01", "valid_until", "2030-01-01")); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), `debe ser posterior`) {
		t.Fatalf("vigencia invertida: %d", rec.Code)
	}
	if rec = ana.post(propURL(changeID)+"/approve", kbForm(anaCSRF, "valid_from", "no-es-fecha")); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("fecha inválida: %d", rec.Code)
	}
	rec = ana.post(propURL(changeID)+"/approve", kbForm(anaCSRF, "note", "ok", "force", "true", "valid_from", "2030-06-01"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Aprobada: publicamos la versión 3") || env.proposalStatus(changeID) != "approved" {
		t.Fatalf("aprobar con force: %d %s", rec.Code, rec.Body.String())
	}
	if n := env.count(`SELECT count(*) FROM nexo_kb_events WHERE action = 'proposal.approved' AND space_slug = 'arq' AND details->>'forced' = 'true'`); n != 1 {
		t.Fatalf("el evento registra que se forzó: %d", n)
	}
	if n := env.kbEvents(domain.ActionProposalApproved, "ana"); n != 2 {
		t.Fatalf("un evento por aprobación: %d", n)
	}
	if rec = ana.get("/dashboard/kb/spaces/arq/proposals?status=approved"); !strings.Contains(rec.Body.String(), "Regla de trazas") || !strings.Contains(rec.Body.String(), "Aprobada") {
		t.Fatalf("filtro de aprobadas: %s", rec.Body.String())
	}
	if rec = ana.get("/dashboard/kb/proposals/badge"); strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("sin pendientes no hay insignia: %q", rec.Body.String())
	}
}

// Un secreto evidente en una propuesta ya creada por otra vía no se publica y
// el error sale en español sin detalle interno.
func TestNexoKBProposalErrorTexts(t *testing.T) {
	cases := []struct {
		err    *domain.Error
		status int
		want   string
		force  bool
	}{
		{domain.Errorf(domain.KindConflict, domain.CodeProposalState, "x"), http.StatusConflict, "ya no está abierta", false},
		{domain.Errorf(domain.KindConflict, domain.CodeVersionConflict, "x"), http.StatusConflict, "Aprobar aunque el documento cambió", true},
		{domain.Errorf(domain.KindInvalid, domain.CodeSecret, "line 3"), http.StatusUnprocessableEntity, "secretos evidentes", false},
		{domain.Errorf(domain.KindForbidden, domain.CodeForbidden, "x"), http.StatusForbidden, "no permiten decidir", false},
	}
	for _, c := range cases {
		status, msg, force := kbProposalErrText(c.err)
		if status != c.status || !strings.Contains(msg, c.want) || force != c.force {
			t.Errorf("%s: %d %q %v", c.err.Code, status, msg, force)
		}
	}
	status, msg, _ := kbProposalErrText(http.ErrAbortHandler)
	if status != http.StatusInternalServerError || strings.Contains(msg, "abort") {
		t.Fatalf("error interno: %d %q", status, msg)
	}
}
