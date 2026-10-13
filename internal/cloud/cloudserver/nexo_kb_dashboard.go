package cloudserver

// Nexo IA · NX-10 entrega 2b — páginas del panel para los espacios de
// conocimiento (HU-29, HU-33): listar espacios, crear un espacio (admin),
// subir archivos sueltos, una carpeta o un zip, ver el resultado del lote,
// listar documentos y versiones, leer un documento (destino de los enlaces
// permanentes de las citas, RC-08) y retirar. Rediseño para la presentación
// (NX-10, panel; docs/nexo/panel/design.md): pasos de "Cómo funciona",
// tarjetas de espacios, buscador con respuesta citada (?q=&at=, mismo
// servicio que /nexo/kb/v1 search), documentos por carpeta, Markdown
// renderizado de forma segura (internal/nexo/kb/mdhtml) y "qué cambió" entre
// versiones (&changes=1).
//
//	GET  /dashboard/kb?q=&at=                        espacios visibles, pasos, buscador (+ creación)
//	POST /dashboard/kb/spaces                        crear espacio (platform.scope.create)
//	GET  /dashboard/kb/spaces/{slug}                 documentos, subida y últimas ejecuciones
//	POST /dashboard/kb/spaces/{slug}/upload          subida (multipart; space.publish)
//	GET  /dashboard/kb/spaces/{slug}/doc?doc=&v=     documento, versiones y secciones
//	POST /dashboard/kb/spaces/{slug}/retire          retirar (space.publish)
//
// Reutiliza la sesión del panel (cookie de Engram revalidada en cada
// petición) y el mismo servicio que la API /nexo/kb/v1: la identidad sale del
// token de la sesión y la sesión hereda su alcance (un token read no sube ni
// retira: lo niega el guardia del panel de NX-07 y, además, authz.Can). Los
// formularios llevan un token CSRF derivado de la sesión (HMAC con la clave
// de sesiones del panel) y se comprueba Origin cuando viene. Todo el
// contenido se escapa (nunca se interpreta HTML de los documentos).

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/dashboard"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/httpapi"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/mdhtml"
	kbservice "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	kbstore "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// registerNexoKBDashboard registra las páginas (enganche // NX-10 en routes()).
func (s *CloudServer) registerNexoKBDashboard(mux *http.ServeMux) {
	// Panel con la marca, los textos y el tema de Nexo IA (dashboard/nexo_brand.go).
	dashboard.EnableNexoBrand()
	mux.HandleFunc("GET /dashboard/kb", s.requireDashboardSession(s.nexoKBPage(s.kbHome)))
	mux.HandleFunc("POST /dashboard/kb/spaces", s.requireDashboardSession(s.nexoKBPage(s.kbCreateSpace)))
	mux.HandleFunc("GET /dashboard/kb/spaces/{slug}", s.requireDashboardSession(s.nexoKBPage(s.kbSpace)))
	mux.HandleFunc("POST /dashboard/kb/spaces/{slug}/upload", s.requireDashboardSession(s.nexoKBPage(s.kbUpload)))
	mux.HandleFunc("GET /dashboard/kb/spaces/{slug}/doc", s.requireDashboardSession(s.nexoKBPage(s.kbDocument)))
	mux.HandleFunc("POST /dashboard/kb/spaces/{slug}/retire", s.requireDashboardSession(s.nexoKBPage(s.kbRetire)))
}

type kbPageHandler func(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity)

// nexoKBPage arma la identidad desde la sesión y comprueba CSRF en los POST.
func (s *CloudServer) nexoKBPage(fn kbPageHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc, err := s.nexoKBService(r)
		if err != nil {
			http.Error(w, "knowledge spaces are not available on this server", http.StatusServiceUnavailable)
			return
		}
		principal, ok := s.dashboardPrincipalFromRequest(r)
		if !ok {
			http.Error(w, "forbidden: knowledge spaces require a managed user session", http.StatusForbidden)
			return
		}
		r = r.WithContext(WithPrincipal(r.Context(), principal))
		r, id := nexoWithAuthzIdentity(r)
		// La sesión hereda el alcance del token (fallar cerrado: read si no se
		// puede leer).
		id.TokenScope = s.nexoEffectiveTokenScope(r.Context(), principal)
		r = r.WithContext(authz.WithIdentity(r.Context(), id))
		if !id.Managed() {
			http.Error(w, "forbidden: knowledge spaces require a managed user token", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost && !s.kbCSRFOK(r) {
			http.Error(w, "forbidden: invalid or missing CSRF token; reload the page", http.StatusForbidden)
			return
		}
		fn(w, r, svc, id)
	}
}

// ─── CSRF ────────────────────────────────────────────────────────────────────

func (s *CloudServer) kbCSRFToken(r *http.Request) string {
	cookie, err := r.Cookie(dashboardSessionCookieName)
	if err != nil {
		return ""
	}
	key, err := s.dashboardPrincipalSessionKey()
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("nexo-kb-csrf:v1:"))
	_, _ = mac.Write([]byte(cookie.Value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// kbCSRFOK compara el token del formulario (o del encabezado X-CSRF-Token) y,
// si viene Origin, que sea el mismo host.
func (s *CloudServer) kbCSRFOK(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		u, err := url.Parse(o)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	want := s.kbCSRFToken(r)
	if want == "" {
		return false
	}
	got := r.Header.Get("X-CSRF-Token")
	if got == "" {
		got = r.URL.Query().Get("csrf_token")
	}
	if got == "" && strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		r.Body = http.MaxBytesReader(nil, r.Body, maxDashboardLoginBodyBytes)
		if err := r.ParseForm(); err == nil {
			got = r.PostForm.Get("csrf_token")
		}
	}
	return got != "" && hmac.Equal([]byte(got), []byte(want))
}

// ─── Utilidades de presentación ──────────────────────────────────────────────

var esc = html.EscapeString

func kbRaw(markup string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, markup)
		return err
	})
}

func (s *CloudServer) kbRender(w http.ResponseWriter, r *http.Request, status int, title, body string) {
	page := dashboard.Layout(title, s.dashboardDisplayName(r), "kb", s.isDashboardAdmin(r), kbRaw(body))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
	w.WriteHeader(status)
	if err := page.Render(r.Context(), w); err != nil {
		fmt.Printf("dashboard kb: render: %v\n", err)
	}
}

// kbErrorText traduce los errores de dominio más comunes a un texto que dice
// qué pasó y qué hacer.
func kbErrorText(de *domain.Error) string {
	switch de.Kind {
	case domain.KindNotFound:
		return "No encontramos lo que buscas, o no tienes acceso. Revisa el enlace o pide acceso al responsable del espacio. Detalle: " + de.Message
	case domain.KindForbidden:
		return "Tu rol o tu token no permiten esta acción. Pide el permiso al responsable del espacio o entra con un token de escritura. Detalle: " + de.Message
	case domain.KindTooLarge:
		return "El archivo supera el tamaño permitido. Divide la subida en partes más pequeñas. Detalle: " + de.Message
	}
	return de.Message
}

func (s *CloudServer) kbError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := http.StatusInternalServerError, "Ocurrió un error interno. Vuelve a intentarlo; si se repite, avisa al administrador."
	var de *domain.Error
	if errors.As(err, &de) {
		msg = kbErrorText(de)
		switch de.Kind {
		case domain.KindInvalid:
			status = http.StatusUnprocessableEntity
		case domain.KindNotFound:
			status = http.StatusNotFound
		case domain.KindForbidden:
			status = http.StatusForbidden
		case domain.KindConflict:
			status = http.StatusConflict
		case domain.KindTooLarge:
			status = http.StatusRequestEntityTooLarge
		}
	} else {
		fmt.Printf("dashboard kb: error: %v\n", err)
	}
	s.kbRender(w, r, status, "Conocimiento", `<div class="nx-page"><header class="nx-head"><h1>No se pudo completar</h1></header><div class="nx-note nx-note-error" role="alert"><p>`+
		esc(msg)+`</p></div><p><a href="/dashboard/kb">Volver a Conocimiento</a></p></div>`)
}

func kbDate(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") }

func kbHasCap(caps []string, c authz.Capability) bool {
	for _, x := range caps {
		if x == string(c) {
			return true
		}
	}
	return false
}

// ─── Páginas ─────────────────────────────────────────────────────────────────

type kbSpaceStats struct {
	active, retired int
	last            time.Time
}

func (s *CloudServer) kbStats(r *http.Request, svc *kbservice.Service, id authz.Identity, slug string) kbSpaceStats {
	var st kbSpaceStats
	docs, err := svc.ListDocuments(r.Context(), id, slug, kbstore.DocQuery{Limit: 2000})
	if err != nil {
		return st
	}
	for _, d := range docs {
		if d.Status == domain.DocRetired {
			st.retired++
		} else {
			st.active++
		}
		if d.UpdatedAt.After(st.last) {
			st.last = d.UpdatedAt
		}
	}
	return st
}

func kbStep(title, text, state, stateClass, action string) string {
	return `<li><div><p class="nx-step-title">` + esc(title) + `</p><p class="nx-step-text">` + text + `</p></div><div class="nx-step-side"><span class="` + stateClass + `">` +
		esc(state) + `</span>` + action + `</div></li>`
}

func (s *CloudServer) kbHome(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	spaces, err := svc.ListSpaces(r.Context(), id)
	if err != nil {
		s.kbError(w, r, err)
		return
	}
	canCreate, _ := svc.CanCreateSpace(r.Context(), id)
	stats := make([]kbSpaceStats, len(spaces))
	totalDocs := 0
	var publishSlug, publishEmptySlug string
	canMembers := false
	for i, sp := range spaces {
		stats[i] = s.kbStats(r, svc, id, sp.Slug)
		totalDocs += stats[i].active
		if kbHasCap(sp.Capabilities, authz.CapSpacePublish) {
			if publishSlug == "" {
				publishSlug = sp.Slug
			}
			if stats[i].active == 0 && publishEmptySlug == "" {
				publishEmptySlug = sp.Slug
			}
		}
		if kbHasCap(sp.Capabilities, authz.CapSpaceMembersManage) {
			canMembers = true
		}
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	atRaw := r.URL.Query().Get("at")

	var b strings.Builder
	b.WriteString(`<div class="nx-page">`)
	b.WriteString(`<header class="nx-head"><h1>Conocimiento</h1><p>Los estándares, lineamientos y guías oficiales del equipo, con su versión y vigencia. Tu asistente consulta lo mismo que ves aquí.</p></header>`)

	// Buscador en todos los espacios visibles.
	if len(spaces) > 0 {
		b.WriteString(kbSearchForm("/dashboard/kb", q, atRaw, "en todos tus espacios", kbMode(r)))
		if q != "" {
			b.WriteString(s.kbSearchBlock(r, svc, id, nil, q, atRaw, true))
		}
		b.WriteString(`</section>`)
	}

	// Cómo funciona: cuatro pasos con su estado para quien mira.
	b.WriteString(`<section aria-labelledby="nx-how"><h2 class="nx-h2" id="nx-how">Cómo funciona</h2><ol class="nx-steps">`)
	// 1. Crear un espacio.
	switch {
	case len(spaces) > 0:
		act := ""
		if canCreate {
			act = `<a href="#nx-crear">Crear otro espacio</a>`
		}
		b.WriteString(kbStep("Crear un espacio", "Un administrador crea el espacio de un tema (por ejemplo, arquitectura o frontend) y decide si es abierto o restringido.", "Hecho", "nx-step-done", act))
	case canCreate:
		b.WriteString(kbStep("Crear un espacio", "Crea el espacio de un tema (por ejemplo, arquitectura o frontend) y decide si es abierto o restringido.", "Pendiente", "nx-step-todo", `<a href="#nx-crear">Crear espacio</a>`))
	default:
		b.WriteString(kbStep("Crear un espacio", "Un administrador crea el espacio de un tema. Pídeselo si tu equipo necesita uno.", "No aplica a tu rol", "nx-step-na", ""))
	}
	// 2. Nombrar responsable y miembros.
	switch {
	case len(spaces) > 0 && (canMembers || canCreate):
		b.WriteString(kbStep("Nombrar responsable y miembros", "El responsable se nombra al crear el espacio. Después agrega publicadores y, en los espacios restringidos, lectores, con la API <code>/nexo/authz/v1</code>.", "Hecho", "nx-step-done", ""))
	case len(spaces) > 0:
		b.WriteString(kbStep("Nombrar responsable y miembros", "Cada espacio tiene un responsable que decide quién publica. Si necesitas publicar, pídeselo.", "No aplica a tu rol", "nx-step-na", ""))
	case canCreate:
		b.WriteString(kbStep("Nombrar responsable y miembros", "Al crear el espacio, indica el usuario responsable. Él decide quién publica.", "Pendiente", "nx-step-todo", ""))
	default:
		b.WriteString(kbStep("Nombrar responsable y miembros", "Cada espacio tiene un responsable que decide quién publica.", "No aplica a tu rol", "nx-step-na", ""))
	}
	// 3. Subir documentos.
	switch {
	case publishSlug == "":
		b.WriteString(kbStep("Subir documentos", "El responsable y los publicadores suben Markdown, Word, una carpeta o un zip (por ejemplo, la wiki exportada).", "No aplica a tu rol", "nx-step-na", ""))
	case publishEmptySlug != "":
		b.WriteString(kbStep("Subir documentos", "Sube Markdown, Word, una carpeta o un zip. Antes de publicar puedes probar la subida sin publicar nada.", "Pendiente", "nx-step-todo",
			`<a href="/dashboard/kb/spaces/`+url.PathEscape(publishEmptySlug)+`#nx-subir">Ir a subir</a>`))
	default:
		b.WriteString(kbStep("Subir documentos", "Sube Markdown, Word, una carpeta o un zip. Volver a subir el mismo archivo no crea otra versión.", "Hecho", "nx-step-done",
			`<a href="/dashboard/kb/spaces/`+url.PathEscape(publishSlug)+`#nx-subir">Subir más</a>`))
	}
	// 4. Consultar.
	consult := "Desde tu asistente: <code>engram nexo connect</code> y luego <code>engram setup &lt;agente&gt;</code>. Desde aquí: con el buscador."
	if totalDocs > 0 {
		b.WriteString(kbStep("Consultar", consult, "Disponible", "nx-step-done", `<a href="#nx-q">Buscar ahora</a>`))
	} else {
		b.WriteString(kbStep("Consultar", consult, "Pendiente", "nx-step-todo", `<span class="nx-step-na">Aún no hay documentos publicados</span>`))
	}
	b.WriteString(`</ol></section>`)

	// Espacios.
	b.WriteString(`<section aria-labelledby="nx-spaces"><h2 class="nx-h2" id="nx-spaces">Tus espacios</h2>`)
	if len(spaces) == 0 {
		b.WriteString(`<p class="nx-empty">Todavía no ves ningún espacio. Cuando un administrador cree uno abierto, o te agreguen a uno restringido, aparecerá aquí.</p>`)
	} else {
		b.WriteString(`<div class="nx-spaces">`)
		for i, sp := range spaces {
			name := sp.Name
			if name == "" {
				name = sp.Slug
			}
			last := "sin documentos"
			if !stats[i].last.IsZero() {
				last = "última actualización " + kbDay(stats[i].last)
			}
			fmt.Fprintf(&b, `<article class="nx-space"><h3><a href="/dashboard/kb/spaces/%s">%s</a></h3>`, url.PathEscape(sp.Slug), esc(name))
			if sp.Description != "" {
				fmt.Fprintf(&b, `<p>%s</p>`, esc(sp.Description))
			}
			fmt.Fprintf(&b, `<p>%s. Tu rol: %s.</p>`, esc(kbVisibility(sp.Visibility)), esc(kbRole(sp.Role)))
			fmt.Fprintf(&b, `<dl><div><dt>Vigentes</dt><dd>%d</dd></div><div><dt>Retirados</dt><dd>%d</dd></div></dl><p>%s</p></article>`,
				stats[i].active, stats[i].retired, esc(last))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</section>`)

	if canCreate {
		b.WriteString(`<section aria-labelledby="nx-crear-t" id="nx-crear"><h2 class="nx-h2" id="nx-crear-t">Crear espacio</h2><div class="nx-panel"><form method="post" action="/dashboard/kb/spaces" class="nx-create">`)
		b.WriteString(`<input type="hidden" name="csrf_token" value="` + esc(s.kbCSRFToken(r)) + `"/>`)
		b.WriteString(`<label for="nx-slug">Identificador <span class="nx-hint">minúsculas y guiones, va en la URL</span><input id="nx-slug" type="text" name="slug" placeholder="arquitectura" required pattern="[a-z0-9][a-z0-9-]{1,62}" autocomplete="off"/></label>`)
		b.WriteString(`<label for="nx-name">Nombre<input id="nx-name" type="text" name="name" placeholder="Arquitectura" autocomplete="off"/></label>`)
		b.WriteString(`<label for="nx-desc">Descripción<input id="nx-desc" type="text" name="description" placeholder="Estándares de arquitectura de software" autocomplete="off"/></label>`)
		b.WriteString(`<label for="nx-owner">Responsable <span class="nx-hint">usuario del panel</span><input id="nx-owner" type="text" name="owner" required autocomplete="off"/></label>`)
		b.WriteString(`<label for="nx-vis">Visibilidad<select id="nx-vis" name="visibility"><option value="open" selected>Abierto a todo el equipo</option><option value="restricted">Restringido a sus miembros</option></select></label>`)
		b.WriteString(`<div><button type="submit">Crear espacio</button></div></form>`)
		b.WriteString(`<p class="nx-hint">El responsable decide quién publica. Los espacios abiertos los leen todos los usuarios con token.</p></div></section>`)
	}
	b.WriteString(`</div>`)
	s.kbRender(w, r, http.StatusOK, "Conocimiento", b.String())
}

// kbMode es el modo pedido en el buscador (NX-10 R2); vacío = el del servidor.
func kbMode(r *http.Request) string {
	switch m := r.URL.Query().Get("mode"); m {
	case "fts", "exact", "hybrid":
		return m
	}
	return ""
}

// kbSearchBlock ejecuta la búsqueda con el mismo servicio que /nexo/kb/v1
// (permisos, vigencia y R1) y dibuja la respuesta.
func (s *CloudServer) kbSearchBlock(r *http.Request, svc *kbservice.Service, id authz.Identity, spaces []string, q, atRaw string, showSpace bool) string {
	at, msg := kbParseAt(atRaw)
	if msg != "" {
		return `<div class="nx-answers"><div class="nx-note nx-note-error" role="alert"><p>` + esc(msg) + `</p></div></div>`
	}
	res, err := svc.Search(r.Context(), id, kbservice.SearchRequest{Q: q, Spaces: spaces, At: at, Limit: 5, Mode: kbMode(r)})
	if err != nil {
		text := "No se pudo buscar. Vuelve a intentarlo."
		var de *domain.Error
		if errors.As(err, &de) {
			text = "No se pudo buscar: " + de.Message
		} else {
			fmt.Printf("dashboard kb: search: %v\n", err)
		}
		return `<div class="nx-answers"><div class="nx-note nx-note-error" role="alert"><p>` + esc(text) + `</p></div></div>`
	}
	return kbSearchResults(res, at, showSpace)
}

func (s *CloudServer) kbCreateSpace(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulario inválido", http.StatusBadRequest)
		return
	}
	sp, err := svc.CreateSpace(r.Context(), id, kbservice.CreateSpaceInput{Slug: r.PostForm.Get("slug"), Name: r.PostForm.Get("name"),
		Description: r.PostForm.Get("description"), Owner: r.PostForm.Get("owner"), Visibility: r.PostForm.Get("visibility")})
	if err != nil {
		s.kbError(w, r, err)
		return
	}
	http.Redirect(w, r, "/dashboard/kb/spaces/"+url.PathEscape(sp.Slug), http.StatusSeeOther)
}

func (s *CloudServer) kbSpace(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	slug := r.PathValue("slug")
	sp, err := svc.GetSpace(r.Context(), id, slug)
	if err != nil {
		s.kbError(w, r, err)
		return
	}
	status := r.URL.Query().Get("status")
	if status != domain.DocActive && status != domain.DocRetired {
		status = ""
	}
	docs, err := svc.ListDocuments(r.Context(), id, slug, kbstore.DocQuery{Status: status, Limit: 2000})
	if err != nil {
		s.kbError(w, r, err)
		return
	}
	st := s.kbStats(r, svc, id, slug)
	canPublish := kbHasCap(sp.Capabilities, authz.CapSpacePublish)
	base := "/dashboard/kb/spaces/" + url.PathEscape(sp.Slug)
	name := sp.Name
	if name == "" {
		name = sp.Slug
	}
	now := time.Now()

	var b strings.Builder
	b.WriteString(`<div class="nx-page">`)
	fmt.Fprintf(&b, `<header class="nx-head"><p class="nx-crumbs"><a href="/dashboard/kb">Conocimiento</a> / %s</p><h1>%s</h1>`, esc(sp.Slug), esc(name))
	if sp.Description != "" {
		fmt.Fprintf(&b, `<p>%s</p>`, esc(sp.Description))
	}
	fmt.Fprintf(&b, `<ul class="nx-meta"><li>%s</li><li>Tu rol: <strong>%s</strong></li><li><strong>%d</strong> vigentes</li><li><strong>%d</strong> retirados</li>`,
		esc(kbVisibility(sp.Visibility)), esc(kbRole(sp.Role)), st.active, st.retired)
	if !st.last.IsZero() {
		fmt.Fprintf(&b, `<li>Última actualización %s</li>`, kbDay(st.last))
	}
	b.WriteString(`</ul></header>`)

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	atRaw := r.URL.Query().Get("at")
	b.WriteString(kbSearchForm(base, q, atRaw, "en "+name, kbMode(r)))
	if q != "" {
		b.WriteString(s.kbSearchBlock(r, svc, id, []string{sp.Slug}, q, atRaw, false))
	}
	b.WriteString(`</section>`)

	grid := "nx-space-grid"
	if !canPublish {
		grid += " nx-single"
	}
	b.WriteString(`<div class="` + grid + `"><section aria-labelledby="nx-docs-t"><h2 class="nx-h2" id="nx-docs-t">Documentos</h2>`)
	b.WriteString(`<nav class="nx-filters" aria-label="Filtrar documentos">`)
	for _, f := range []struct{ value, label string }{{"", "Todos"}, {domain.DocActive, "Vigentes"}, {domain.DocRetired, "Retirados"}} {
		href := base
		if f.value != "" {
			href += "?status=" + f.value
		}
		cur := ""
		if f.value == status {
			cur = ` aria-current="page"`
		}
		fmt.Fprintf(&b, `<a href="%s"%s>%s</a>`, esc(href), cur, f.label)
	}
	b.WriteString(`</nav>`)
	if len(docs) == 0 {
		msg := "Este espacio todavía no tiene documentos."
		if status != "" {
			msg = "No hay documentos con este filtro."
		}
		if canPublish && status == "" {
			msg += " Usa el formulario de subida para publicar los primeros."
		} else if status == "" {
			msg += " El responsable del espacio los publica."
		}
		b.WriteString(`<p class="nx-empty">` + esc(msg) + `</p>`)
	} else {
		csrf := ""
		if canPublish {
			csrf = s.kbCSRFToken(r)
		}
		docHTML := func(d domain.Document) string {
			var li strings.Builder
			title := d.Title
			if title == "" {
				title = path.Base(d.DocKey)
			}
			fmt.Fprintf(&li, `<li class="nx-doc"><span class="nx-doc-title"><a href="%s">%s</a></span><span class="nx-doc-path">%s, versión %d, %s</span><span class="nx-doc-side">%s`,
				esc(kbDocURL(sp.Slug, d.DocKey, 0, "")), esc(title), esc(d.DocKey), d.CurrentVersion, esc(kbVigencia(d.ValidFrom, d.ValidUntil)), kbDocChip(d, now))
			if canPublish && d.Status == domain.DocActive {
				fmt.Fprintf(&li, `<form method="post" action="%s/retire"><input type="hidden" name="csrf_token" value="%s"/><input type="hidden" name="doc_key" value="%s"/><input type="hidden" name="note" value="retirado desde el panel"/><button type="submit" class="nexo-btn-secondary" aria-label="Retirar %s">Retirar</button></form>`,
					esc(base), esc(csrf), esc(d.DocKey), esc(title))
			}
			li.WriteString(`</span></li>`)
			return li.String()
		}
		b.WriteString(`<div class="nx-tree">` + kbTreeHTML(kbTree(docs), 0, docHTML) + `</div>`)
	}
	b.WriteString(`</section>`)

	if canPublish {
		b.WriteString(`<aside class="nx-aside">`)
		b.WriteString(`<section class="nx-panel" id="nx-subir" aria-labelledby="nx-subir-t"><h2 class="nx-h2" id="nx-subir-t">Subir documentos</h2>`)
		fmt.Fprintf(&b, `<form method="post" action="%s/upload?csrf_token=%s" enctype="multipart/form-data" class="nx-upload">`, esc(base), url.QueryEscape(s.kbCSRFToken(r)))
		b.WriteString(`<div class="nx-drop"><label for="nx-files">Archivos <span class="nx-hint">.md, .txt o .docx; arrástralos aquí o elígelos</span><input id="nx-files" type="file" name="file" multiple accept=".md,.markdown,.txt,.docx"/></label>`)
		b.WriteString(`<label for="nx-dir">Una carpeta <span class="nx-hint">con sus subcarpetas</span><input id="nx-dir" type="file" name="file" webkitdirectory directory multiple/></label>`)
		b.WriteString(`<label for="nx-zip">Un zip <span class="nx-hint">por ejemplo, la wiki exportada</span><input id="nx-zip" type="file" name="zip" accept=".zip"/></label></div>`)
		b.WriteString(`<label for="nx-cat">Categoría <span class="nx-hint">si el documento no la trae</span><input id="nx-cat" type="text" name="category" placeholder="lineamiento" autocomplete="off"/></label>`)
		b.WriteString(`<details><summary>Opciones avanzadas</summary>`)
		b.WriteString(`<label for="nx-col">Colección <span class="nx-hint">nombre del conjunto, para retirar lo que ya no viene</span><input id="nx-col" type="text" name="collection" autocomplete="off"/></label>`)
		b.WriteString(`<label for="nx-src">Tipo de fuente<select id="nx-src" name="source"><option value="auto" selected>Detectar</option><option value="dir">Archivos</option><option value="azure-devops-wiki">Wiki de Azure DevOps</option></select></label>`)
		b.WriteString(`<label class="nx-check"><input type="checkbox" name="prune" value="true"/> <span>Retirar los documentos de esta colección que ya no vienen en la subida</span></label></details>`)
		b.WriteString(`<div class="nx-actions"><button type="submit" name="dry_run" value="true" class="nexo-btn-secondary">Probar sin publicar</button><button type="submit">Publicar</button></div>`)
		b.WriteString(`<p class="nx-hint">Volver a subir el mismo archivo no crea otra versión. Los originales no se guardan: para reprocesar un archivo, súbelo de nuevo.</p></form></section>`)
		if kbHasCap(sp.Capabilities, authz.CapSpaceAuditRead) {
			if runs, err := svc.ListPublishRuns(r.Context(), id, slug, nil, 5); err == nil && len(runs) > 0 {
				b.WriteString(`<section class="nx-panel" aria-labelledby="nx-runs-t"><h2 class="nx-h2" id="nx-runs-t">Últimas subidas</h2><ul class="nx-runs">`)
				for _, run := range runs {
					keys := make([]string, 0, len(run.Summary))
					for k := range run.Summary {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					var parts []string
					for _, k := range keys {
						parts = append(parts, strconv.Itoa(run.Summary[k])+" "+kbResultWord(k))
					}
					kind := "Publicación"
					if run.DryRun {
						kind = "Prueba sin publicar"
					}
					fmt.Fprintf(&b, `<li>%s de %s<span>%s, %s</span></li>`, esc(kind), esc(run.Actor), kbDate(run.FinishedAt), esc(strings.Join(parts, ", ")))
				}
				b.WriteString(`</ul></section>`)
			}
		}
		b.WriteString(`</aside>`)
	}
	b.WriteString(`</div></div>`)
	s.kbRender(w, r, http.StatusOK, name, b.String())
}

func (s *CloudServer) kbUpload(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	slug := r.PathValue("slug")
	if _, err := svc.CanPublish(r.Context(), id, slug); err != nil {
		s.kbError(w, r, err)
		return
	}
	req, _, err := httpapi.ParseUpload(w, r, svc)
	if err != nil {
		s.kbError(w, r, err)
		return
	}
	res, err := svc.Upload(r.Context(), id, slug, req)
	if err != nil {
		s.kbError(w, r, err)
		return
	}
	var b strings.Builder
	title := "Resultado de la subida"
	lead := "Los documentos ya están publicados y se pueden buscar."
	if res.DryRun {
		title = "Resultado de la prueba"
		lead = "Es una prueba: no se publicó nada. Si el resultado es el esperado, vuelve a subir con Publicar."
	}
	fmt.Fprintf(&b, `<div class="nx-page"><header class="nx-head"><p class="nx-crumbs"><a href="/dashboard/kb">Conocimiento</a> / <a href="/dashboard/kb/spaces/%s">%s</a></p><h1>%s</h1><p>%s</p></header>`,
		url.PathEscape(slug), esc(res.Space), title, lead)
	keys := make([]string, 0, len(res.Summary))
	for k := range res.Summary {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString(`<ul class="nx-meta">`)
	for _, k := range keys {
		fmt.Fprintf(&b, `<li>%s <strong>%d</strong></li>`, kbResultChip(k), res.Summary[k])
	}
	b.WriteString(`</ul>`)
	b.WriteString(`<div class="nx-scroll"><table><thead><tr><th>Documento</th><th>Resultado</th><th>Versión</th><th>Secciones</th><th>Detalle</th></tr></thead><tbody>`)
	for _, d := range append(append([]kbservice.DocResult{}, res.Results...), res.Pruned...) {
		detail := d.Reason
		if d.Note != "" {
			detail = strings.TrimSpace(d.Note + " " + detail)
		}
		name := d.DocKey
		if d.Path != "" && d.Path != d.DocKey {
			name += " (" + d.Path + ")"
		}
		fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%d</td><td>%d</td><td>%s</td></tr>`, esc(name), kbResultChip(d.Status), d.Version, d.Sections, esc(detail))
	}
	b.WriteString(`</tbody></table></div>`)
	if res.PruneSkipped != "" {
		b.WriteString(`<div class="nx-note"><p>No se retiró nada: ` + esc(res.PruneSkipped) + `</p></div>`)
	}
	if len(res.Skipped) > 0 {
		b.WriteString(`<section><h2 class="nx-h2">Archivos no publicados</h2><ul>`)
		for _, sk := range res.Skipped {
			b.WriteString(`<li>` + esc(sk.Path) + `: ` + esc(sk.Reason) + `</li>`)
		}
		b.WriteString(`</ul></section>`)
	}
	fmt.Fprintf(&b, `<p><a href="/dashboard/kb/spaces/%s">Volver al espacio</a></p></div>`, url.PathEscape(slug))
	s.kbRender(w, r, http.StatusOK, title, b.String())
}

func (s *CloudServer) kbDocument(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	slug, key := r.PathValue("slug"), r.URL.Query().Get("doc")
	version := 0
	if v := r.URL.Query().Get("v"); v != "" {
		version, _ = strconv.Atoi(v)
	}
	view, err := svc.GetDocument(r.Context(), id, slug, key, version)
	if err != nil {
		s.kbError(w, r, err)
		return
	}
	versions, _ := svc.ListVersions(r.Context(), id, slug, key, 0, 50)
	d, v := view.Document, view.Version
	secs := splitSections(v.ContentMD, view.Sections)
	showDiff := r.URL.Query().Get("changes") != "" && v.Version > 1

	var b strings.Builder
	dir := path.Dir(d.DocKey)
	crumbs := `<a href="/dashboard/kb">Conocimiento</a> / <a href="/dashboard/kb/spaces/` + url.PathEscape(slug) + `">` + esc(slug) + `</a>`
	if dir != "." {
		crumbs += " / " + esc(kbFolderLabel(dir))
	}
	fmt.Fprintf(&b, `<div class="nx-page"><header class="nx-head"><p class="nx-crumbs">%s</p><h1>%s</h1>`, crumbs, esc(v.Title))
	label, class := kbState(d.Status, v.ValidFrom, v.ValidUntil, time.Now())
	fmt.Fprintf(&b, `<ul class="nx-meta"><li>%s</li><li>Versión <strong>%d</strong> de %d</li><li>Vigente %s</li>`,
		kbChip(label, class), v.Version, d.CurrentVersion, esc(kbVigencia(v.ValidFrom, v.ValidUntil)))
	if v.Category != "" {
		fmt.Fprintf(&b, `<li>Categoría: %s</li>`, esc(v.Category))
	}
	fmt.Fprintf(&b, `<li>Publicado por %s el %s</li>`, esc(v.PublishedBy), kbDay(v.PublishedAt))
	if v.Origin.SourceURL != "" && kbSafeHTTP(v.Origin.SourceURL) {
		fmt.Fprintf(&b, `<li><a href="%s" rel="noopener noreferrer">Ver la fuente original</a></li>`, esc(v.Origin.SourceURL))
	}
	b.WriteString(`</ul></header>`)
	if view.Notice != "" {
		b.WriteString(`<div class="nx-note" role="note"><p>Atención: ` + esc(view.Notice) + `.</p></div>`)
	}

	b.WriteString(`<div class="nx-doc-layout"><article class="nx-prose">`)
	if showDiff {
		fmt.Fprintf(&b, `<h2>Qué cambió de la versión %d a la %d</h2>`, v.Version-1, v.Version)
		prev, err := svc.GetDocument(r.Context(), id, slug, key, v.Version-1)
		if err != nil {
			b.WriteString(`<p class="nx-empty">No se pudo leer la versión anterior.</p>`)
		} else {
			b.WriteString(kbDiffHTML(kbDiff(splitSections(prev.Version.ContentMD, prev.Sections), secs)))
		}
		fmt.Fprintf(&b, `<p><a href="%s">Leer la versión %d completa</a></p>`, esc(kbDocURL(slug, d.DocKey, v.Version, "")), v.Version)
	} else {
		for _, sec := range secs {
			fmt.Fprintf(&b, `<section class="nx-sec" id="%s">%s</section>`, esc(sec.anchor), mdhtml.Render(sec.body))
		}
	}
	b.WriteString(`</article><aside class="nx-side">`)
	if !showDiff && len(secs) > 1 {
		b.WriteString(`<nav aria-labelledby="nx-toc-t"><h2 id="nx-toc-t">En este documento</h2><ol>`)
		for _, sec := range secs {
			t := sec.anchor
			if n := len(sec.path); n > 0 {
				t = sec.path[n-1]
			}
			fmt.Fprintf(&b, `<li><a href="#%s">%s</a></li>`, esc(sec.anchor), esc(t))
		}
		b.WriteString(`</ol></nav>`)
	}
	if len(versions) > 0 {
		b.WriteString(`<section aria-labelledby="nx-ver-t"><h2 id="nx-ver-t">Versiones</h2><ol>`)
		for _, ver := range versions {
			cur := ""
			if ver.Version == v.Version {
				cur = ` class="nx-current" aria-current="page"`
			}
			fmt.Fprintf(&b, `<li%s><a href="%s">Versión %d</a>, %s, %s`, cur, esc(kbDocURL(slug, d.DocKey, ver.Version, "")), ver.Version, kbDay(ver.PublishedAt), esc(ver.PublishedBy))
			if ver.Version > 1 {
				fmt.Fprintf(&b, `. <a href="%s">Qué cambió</a>`, esc(kbDocURL(slug, d.DocKey, ver.Version, "changes")))
			}
			b.WriteString(`</li>`)
		}
		b.WriteString(`</ol></section>`)
	}
	b.WriteString(`</aside></div></div>`)
	s.kbRender(w, r, http.StatusOK, v.Title, b.String())
}

type kbSec struct {
	anchor string
	path   []string
	body   string
}

var kbHeadingRe = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$`)

// kbNormHeading compara títulos sin formato, mayúsculas ni espacios extra.
func kbNormHeading(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '*', '_', '`', '[', ']':
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// splitSections reparte el contenido por sus títulos y asigna a cada tramo la
// sección indexada (ancla y ruta de títulos) en orden de documento. (NX-10,
// panel: antes se cortaba por las líneas de procedencia, que no coinciden con
// el contenido guardado cuando el archivo tenía front matter o títulos sin
// cuerpo.) Un tramo sin sección propia (por ejemplo, un título sin texto o un
// subtítulo que el seccionador no corta) se une al anterior o, al inicio, al
// siguiente. Sin coincidencias, muestra el documento completo en una sola.
func splitSections(content string, refs []kbservice.SectionRef) []kbSec {
	type chunk struct {
		heading string
		lines   []string
	}
	var chunks []chunk
	cur := chunk{}
	fence := ""
	for _, l := range strings.Split(content, "\n") {
		t := strings.TrimSpace(l)
		if fence == "" && (strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")) {
			fence = t[:3]
		} else if fence != "" && strings.HasPrefix(t, fence) {
			fence = ""
		} else if fence == "" {
			if m := kbHeadingRe.FindStringSubmatch(l); m != nil {
				chunks = append(chunks, cur)
				cur = chunk{heading: m[2]}
			}
		}
		cur.lines = append(cur.lines, l)
	}
	chunks = append(chunks, cur)

	var out []kbSec
	var pending []string
	j := 0
	for _, c := range chunks {
		if strings.TrimSpace(strings.Join(c.lines, "\n")) == "" {
			continue
		}
		match := -1
		for k := j; k < len(refs); k++ {
			hp := refs[k].HeadingPath
			if (c.heading == "" && len(hp) == 0) || (c.heading != "" && len(hp) > 0 && kbNormHeading(hp[len(hp)-1]) == kbNormHeading(c.heading)) {
				match = k
				break
			}
		}
		switch {
		case match >= 0:
			out = append(out, kbSec{anchor: refs[match].Anchor, path: refs[match].HeadingPath, body: strings.Join(append(pending, c.lines...), "\n")})
			pending = nil
			j = match + 1
		case len(out) > 0:
			out[len(out)-1].body += "\n" + strings.Join(c.lines, "\n")
		default:
			pending = append(pending, c.lines...)
		}
	}
	if len(out) == 0 {
		anchor := "intro"
		if len(refs) > 0 {
			anchor = refs[0].Anchor
		}
		return []kbSec{{anchor: anchor, body: content}}
	}
	if len(pending) > 0 {
		out[len(out)-1].body += "\n" + strings.Join(pending, "\n")
	}
	return out
}

func (s *CloudServer) kbRetire(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	slug := r.PathValue("slug")
	if _, err := svc.RetireDocument(r.Context(), id, slug, r.PostForm.Get("doc_key"), r.PostForm.Get("note")); err != nil {
		s.kbError(w, r, err)
		return
	}
	http.Redirect(w, r, "/dashboard/kb/spaces/"+url.PathEscape(slug), http.StatusSeeOther)
}
