package cloudserver

// Nexo IA · NX-10 entrega 2b — páginas del panel para los espacios de
// conocimiento (HU-29, HU-33): listar espacios, crear un espacio (admin),
// subir archivos sueltos, una carpeta o un zip, ver el resultado del lote,
// listar documentos y versiones, leer un documento (destino de los enlaces
// permanentes de las citas, RC-08) y retirar.
//
//	GET  /dashboard/kb                               espacios visibles (+ formulario de creación)
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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/dashboard"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/httpapi"
	kbservice "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	kbstore "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// registerNexoKBDashboard registra las páginas (enganche // NX-10 en routes()).
func (s *CloudServer) registerNexoKBDashboard(mux *http.ServeMux) {
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

func (s *CloudServer) kbError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := http.StatusInternalServerError, "internal error"
	var de *domain.Error
	if errors.As(err, &de) {
		msg = de.Message
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
	s.kbRender(w, r, status, "Conocimiento", `<section class="frame-section"><p class="section-kicker">ERROR</p><div class="data-frame policy-note danger-note"><p>`+
		esc(msg)+`</p></div><p><a href="/dashboard/kb">Volver a los espacios</a></p></section>`)
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

func kbBadge(status string) string {
	variant := "success"
	switch status {
	case domain.ResultRejected:
		variant = "danger"
	case domain.ResultUnchanged, domain.ResultSkipped:
		variant = "muted"
	case domain.ResultRetired: // también domain.DocRetired
		variant = "warning"
	}
	return `<span class="badge badge-` + variant + `">` + esc(status) + `</span>`
}

// ─── Páginas ─────────────────────────────────────────────────────────────────

func (s *CloudServer) kbHome(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	spaces, err := svc.ListSpaces(r.Context(), id)
	if err != nil {
		s.kbError(w, r, err)
		return
	}
	var b strings.Builder
	b.WriteString(`<section class="frame-section"><div class="section-header"><p class="section-kicker">NEXO / CONOCIMIENTO</p><h2>Espacios de conocimiento</h2></div>`)
	if len(spaces) == 0 {
		b.WriteString(`<div class="data-frame"><p class="muted">No hay espacios visibles para este usuario.</p></div>`)
	} else {
		b.WriteString(`<div class="data-frame table-scroll"><table class="data-table"><thead><tr><th>Espacio</th><th>Nombre</th><th>Visibilidad</th><th>Tu rol</th></tr></thead><tbody>`)
		for _, sp := range spaces {
			fmt.Fprintf(&b, `<tr><td><a href="/dashboard/kb/spaces/%s">%s</a></td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				url.PathEscape(sp.Slug), esc(sp.Slug), esc(sp.Name), esc(sp.Visibility), esc(sp.Role))
		}
		b.WriteString(`</tbody></table></div>`)
	}
	if d, err := svc.CanCreateSpace(r.Context(), id); err == nil && d {
		b.WriteString(`<h3 class="section-block-title">Crear espacio</h3><div class="data-frame"><form method="post" action="/dashboard/kb/spaces" class="managed-user-form">`)
		b.WriteString(`<input type="hidden" name="csrf_token" value="` + esc(s.kbCSRFToken(r)) + `"/>`)
		b.WriteString(`<input type="text" name="slug" placeholder="slug (arquitectura)" required pattern="[a-z0-9][a-z0-9-]{1,62}" autocomplete="off"/>`)
		b.WriteString(`<input type="text" name="name" placeholder="Nombre" autocomplete="off"/>`)
		b.WriteString(`<input type="text" name="description" placeholder="Descripción" autocomplete="off"/>`)
		b.WriteString(`<input type="text" name="owner" placeholder="Responsable (usuario)" required autocomplete="off"/>`)
		b.WriteString(`<select name="visibility"><option value="open" selected>Abierto</option><option value="restricted">Restringido</option></select>`)
		b.WriteString(`<button type="submit">Crear espacio</button></form>`)
		b.WriteString(`<p class="muted">El responsable gestiona miembros y puede subir documentos. Los espacios abiertos los leen todos los usuarios.</p></div>`)
	}
	b.WriteString(`</section>`)
	s.kbRender(w, r, http.StatusOK, "Conocimiento", b.String())
}

func (s *CloudServer) kbCreateSpace(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
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
	docs, err := svc.ListDocuments(r.Context(), id, slug, kbstore.DocQuery{Status: status, Limit: 500})
	if err != nil {
		s.kbError(w, r, err)
		return
	}
	canPublish := kbHasCap(sp.Capabilities, authz.CapSpacePublish)
	var b strings.Builder
	fmt.Fprintf(&b, `<section class="frame-section"><div class="section-header"><p class="section-kicker">ESPACIO / %s</p><h2>%s</h2><p class="muted">%s</p></div>`,
		esc(strings.ToUpper(sp.Slug)), esc(sp.Name), esc(sp.Description))
	fmt.Fprintf(&b, `<div class="metric-strip"><span class="badge badge-muted">visibilidad: %s</span> <span class="badge badge-muted">tu rol: %s</span> <span class="badge badge-muted">documentos: %d</span></div>`,
		esc(sp.Visibility), esc(sp.Role), len(docs))
	if canPublish {
		b.WriteString(`<h3 class="section-block-title">Subir documentos</h3><div class="data-frame">`)
		fmt.Fprintf(&b, `<form method="post" action="/dashboard/kb/spaces/%s/upload?csrf_token=%s" enctype="multipart/form-data" class="managed-user-form">`,
			url.PathEscape(sp.Slug), url.QueryEscape(s.kbCSRFToken(r)))
		b.WriteString(`<label>Archivos sueltos (.md, .txt, .docx) <input type="file" name="file" multiple accept=".md,.markdown,.txt,.docx"/></label>`)
		b.WriteString(`<label>Una carpeta <input type="file" name="file" webkitdirectory directory multiple/></label>`)
		b.WriteString(`<label>O un .zip con la carpeta <input type="file" name="zip" accept=".zip"/></label>`)
		b.WriteString(`<input type="text" name="category" placeholder="Categoría por defecto (lineamiento)" autocomplete="off"/>`)
		b.WriteString(`<input type="text" name="collection" placeholder="Colección (referencia de la poda)" autocomplete="off"/>`)
		b.WriteString(`<select name="source"><option value="auto" selected>Detectar fuente</option><option value="dir">Archivos</option><option value="azure-devops-wiki">Wiki de Azure DevOps</option></select>`)
		b.WriteString(`<label><input type="checkbox" name="dry_run" value="true"/> Solo probar (no publica)</label>`)
		b.WriteString(`<label><input type="checkbox" name="prune" value="true"/> Retirar lo que ya no viene en esta colección</label>`)
		b.WriteString(`<button type="submit">Subir</button></form>`)
		b.WriteString(`<p class="muted">Volver a subir el mismo archivo no crea una versión nueva. Los originales no se guardan: para reprocesar un archivo hay que volver a subirlo.</p></div>`)
	}
	b.WriteString(`<h3 class="section-block-title">Documentos</h3><p><a href="?">Todos</a> | <a href="?status=active">Vigentes</a> | <a href="?status=retired">Retirados</a></p>`)
	if len(docs) == 0 {
		b.WriteString(`<div class="data-frame"><p class="muted">Sin documentos.</p></div>`)
	} else {
		b.WriteString(`<div class="data-frame table-scroll"><table class="data-table"><thead><tr><th>Documento</th><th>Título</th><th>Categoría</th><th>Versión</th><th>Estado</th><th>Vigencia</th>`)
		if canPublish {
			b.WriteString(`<th></th>`)
		}
		b.WriteString(`</tr></thead><tbody>`)
		for _, d := range docs {
			vig := "desde " + d.ValidFrom.Format("2006-01-02")
			if d.ValidUntil != nil {
				vig += " hasta " + d.ValidUntil.Format("2006-01-02")
			}
			fmt.Fprintf(&b, `<tr><td><a href="/dashboard/kb/spaces/%s/doc?doc=%s">%s</a></td><td>%s</td><td>%s</td><td>v%d</td><td>%s</td><td>%s</td>`,
				url.PathEscape(sp.Slug), url.QueryEscape(d.DocKey), esc(d.DocKey), esc(d.Title), esc(d.Category), d.CurrentVersion, kbBadge(d.Status), esc(vig))
			if canPublish {
				if d.Status == domain.DocActive {
					fmt.Fprintf(&b, `<td><form method="post" action="/dashboard/kb/spaces/%s/retire" style="margin:0"><input type="hidden" name="csrf_token" value="%s"/><input type="hidden" name="doc_key" value="%s"/><input type="hidden" name="note" value="retirado desde el panel"/><button type="submit" class="page-btn">Retirar</button></form></td>`,
						url.PathEscape(sp.Slug), esc(s.kbCSRFToken(r)), esc(d.DocKey))
				} else {
					b.WriteString(`<td></td>`)
				}
			}
			b.WriteString(`</tr>`)
		}
		b.WriteString(`</tbody></table></div>`)
	}
	if runs, err := svc.ListPublishRuns(r.Context(), id, slug, nil, 10); err == nil && len(runs) > 0 {
		b.WriteString(`<h3 class="section-block-title">Últimas ejecuciones</h3><div class="data-frame table-scroll"><table class="data-table"><thead><tr><th>Fecha</th><th>Tipo</th><th>Quién</th><th>Resultado</th><th>Prueba</th></tr></thead><tbody>`)
		for _, run := range runs {
			keys := make([]string, 0, len(run.Summary))
			for k := range run.Summary {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var parts []string
			for _, k := range keys {
				parts = append(parts, k+" "+strconv.Itoa(run.Summary[k]))
			}
			dry := ""
			if run.DryRun {
				dry = "sí"
			}
			fmt.Fprintf(&b, `<tr><td>%s</td><td>%s %s</td><td>%s</td><td>%s</td><td>%s</td></tr>`, kbDate(run.FinishedAt), esc(run.Kind), esc(run.Source),
				esc(run.Actor), esc(strings.Join(parts, ", ")), dry)
		}
		b.WriteString(`</tbody></table></div>`)
	}
	b.WriteString(`<p><a href="/dashboard/kb">Volver a los espacios</a></p></section>`)
	s.kbRender(w, r, http.StatusOK, "Espacio "+sp.Slug, b.String())
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
	mode := ""
	if res.DryRun {
		mode = " (prueba: no se publicó nada)"
	}
	fmt.Fprintf(&b, `<section class="frame-section"><div class="section-header"><p class="section-kicker">RESULTADO DE LA SUBIDA</p><h2>%s%s</h2><p class="muted">fuente %s, ejecución %s</p></div>`,
		esc(res.Space), esc(mode), esc(res.Source), esc(res.RunID))
	keys := make([]string, 0, len(res.Summary))
	for k := range res.Summary {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString(`<div class="metric-strip">`)
	for _, k := range keys {
		fmt.Fprintf(&b, `%s <strong>%d</strong> &nbsp; `, kbBadge(k), res.Summary[k])
	}
	b.WriteString(`</div><div class="data-frame table-scroll"><table class="data-table"><thead><tr><th>Documento</th><th>Archivo</th><th>Resultado</th><th>Versión</th><th>Secciones</th><th>Detalle</th></tr></thead><tbody>`)
	for _, d := range append(append([]kbservice.DocResult{}, res.Results...), res.Pruned...) {
		detail := d.Reason
		if d.Note != "" {
			detail = strings.TrimSpace(d.Note + " " + detail)
		}
		fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%d</td><td>%d</td><td>%s</td></tr>`, esc(d.DocKey), esc(d.Path), kbBadge(d.Status), d.Version, d.Sections, esc(detail))
	}
	b.WriteString(`</tbody></table></div>`)
	if res.PruneSkipped != "" {
		b.WriteString(`<div class="data-frame policy-note danger-note"><p>Poda omitida: ` + esc(res.PruneSkipped) + `</p></div>`)
	}
	if len(res.Skipped) > 0 {
		b.WriteString(`<h3 class="section-block-title">Avisos</h3><div class="data-frame"><ul>`)
		for _, sk := range res.Skipped {
			b.WriteString(`<li>` + esc(sk.Path) + `: ` + esc(sk.Reason) + `</li>`)
		}
		b.WriteString(`</ul></div>`)
	}
	fmt.Fprintf(&b, `<p><a href="/dashboard/kb/spaces/%s">Volver al espacio</a></p></section>`, url.PathEscape(slug))
	s.kbRender(w, r, http.StatusOK, "Subida "+slug, b.String())
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
	var b strings.Builder
	fmt.Fprintf(&b, `<section class="frame-section"><div class="section-header"><p class="section-kicker">%s / %s</p><h2>%s</h2></div>`,
		esc(strings.ToUpper(slug)), esc(d.DocKey), esc(v.Title))
	fmt.Fprintf(&b, `<div class="metric-strip">%s <span class="badge badge-muted">v%d de %d</span> <span class="badge badge-muted">%s</span> <span class="badge badge-muted">vigente desde %s</span>`,
		kbBadge(d.Status), v.Version, d.CurrentVersion, esc(v.Category), v.ValidFrom.Format("2006-01-02"))
	if v.ValidUntil != nil {
		fmt.Fprintf(&b, ` <span class="badge badge-warning">hasta %s</span>`, v.ValidUntil.Format("2006-01-02"))
	}
	b.WriteString(`</div>`)
	if view.Notice != "" {
		b.WriteString(`<div class="data-frame policy-note danger-note"><p>` + esc(view.Notice) + `</p></div>`)
	}
	fmt.Fprintf(&b, `<p class="muted">Publicado por %s el %s; origen %s; formato %s.`, esc(v.PublishedBy), kbDate(v.PublishedAt), esc(v.Origin.Kind), esc(v.SourceFormat))
	if v.Origin.SourceURL != "" {
		fmt.Fprintf(&b, ` <a href="%s" rel="noopener noreferrer">Ver la fuente</a>`, esc(v.Origin.SourceURL))
	}
	b.WriteString(`</p>`)
	// Secciones con su ancla (destino de los enlaces permanentes).
	secs := splitSections(v.ContentMD, view.Sections)
	for _, sec := range secs {
		fmt.Fprintf(&b, `<div class="data-frame" id="%s"><p class="section-kicker">%s <a href="#%s">#</a></p><pre class="observation-content-block" style="white-space:pre-wrap">%s</pre></div>`,
			esc(sec.anchor), esc(strings.Join(sec.path, " > ")), esc(sec.anchor), esc(sec.body))
	}
	if len(versions) > 0 {
		b.WriteString(`<h3 class="section-block-title">Versiones</h3><div class="data-frame table-scroll"><table class="data-table"><thead><tr><th>Versión</th><th>Publicada</th><th>Por</th><th>Vigencia</th><th>Origen</th></tr></thead><tbody>`)
		for _, ver := range versions {
			vig := ver.ValidFrom.Format("2006-01-02")
			if ver.ValidUntil != nil {
				vig += " a " + ver.ValidUntil.Format("2006-01-02")
			}
			fmt.Fprintf(&b, `<tr><td><a href="/dashboard/kb/spaces/%s/doc?doc=%s&amp;v=%d">v%d</a></td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				url.PathEscape(slug), url.QueryEscape(d.DocKey), ver.Version, ver.Version, kbDate(ver.PublishedAt), esc(ver.PublishedBy), esc(vig), esc(ver.Origin.Kind))
		}
		b.WriteString(`</tbody></table></div>`)
	}
	fmt.Fprintf(&b, `<p><a href="/dashboard/kb/spaces/%s">Volver al espacio</a></p></section>`, url.PathEscape(slug))
	s.kbRender(w, r, http.StatusOK, d.DocKey, b.String())
}

type kbSec struct {
	anchor string
	path   []string
	body   string
}

// splitSections reparte el contenido por las líneas de procedencia de cada
// sección (md); sin líneas, muestra el documento completo en una sola.
func splitSections(content string, refs []kbservice.SectionRef) []kbSec {
	lines := strings.Split(content, "\n")
	var out []kbSec
	// Las líneas de procedencia cuentan desde el inicio del archivo original
	// (front matter incluido); el contenido guardado empieza en la primera
	// línea de la primera sección.
	offset := -1
	for _, ref := range refs {
		if ls, ok := ref.SourceLocation["line_start"].(float64); ok && (offset < 0 || int(ls)-1 < offset) {
			offset = int(ls) - 1
		}
	}
	for _, ref := range refs {
		ls, ok1 := ref.SourceLocation["line_start"].(float64)
		le, ok2 := ref.SourceLocation["line_end"].(float64)
		if !ok1 || !ok2 {
			continue
		}
		out = append(out, kbSec{anchor: ref.Anchor, path: ref.HeadingPath, body: sliceLines(lines, int(ls)-offset, int(le)-offset)})
	}
	if len(out) == 0 {
		anchor := "intro"
		if len(refs) > 0 {
			anchor = refs[0].Anchor
		}
		return []kbSec{{anchor: anchor, body: content}}
	}
	return out
}

func sliceLines(lines []string, from, to int) string {
	// Las líneas de procedencia cuentan el front matter; el contenido
	// guardado no lo tiene: se toma el tramo que exista.
	if from < 1 {
		from = 1
	}
	if to > len(lines) {
		to = len(lines)
	}
	if from > to {
		return ""
	}
	return strings.Join(lines[from-1:to], "\n")
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
