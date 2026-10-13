package cloudserver

// Nexo IA · NX-10 (panel) — buscador del panel, Markdown renderizado de forma
// segura, espacios restringidos invisibles, marca y textos en español.
// Postgres real (CLOUDSTORE_TEST_DSN).

import (
	"bytes"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func kbPanelUpload(t *testing.T, env *nexoSecEnv, cookie *http.Cookie, slug string, files []kbPart) string {
	t.Helper()
	page := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb/spaces/"+slug, cookie)
	m := regexp.MustCompile(`upload\?csrf_token=([A-Za-z0-9_%-]+)`).FindStringSubmatch(page.Body.String())
	if page.Code != http.StatusOK || m == nil {
		t.Fatalf("página del espacio sin formulario de subida: %d", page.Code)
	}
	csrf, _ := url.QueryUnescape(m[1])
	form, ctype := kbMultipart(t, files, map[string]string{"category": "lineamiento"})
	up := kbSend(env.h, http.MethodPost, "/dashboard/kb/spaces/"+slug+"/upload?csrf_token="+url.QueryEscape(csrf), "", bytes.NewBuffer(form.Bytes()), ctype, cookie)
	if up.Code != http.StatusOK || !strings.Contains(up.Body.String(), "Resultado de la subida") {
		t.Fatalf("subida por el panel: %d %s", up.Code, up.Body.String())
	}
	return csrf
}

func TestNexoKBPanelSearchMarkdownAndVisibility(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	const ip = "10.77.0.2"
	if rec := env.req(http.MethodPost, "/nexo/kb/v1/spaces", "carla", `{"slug":"arquitectura","name":"Arquitectura","owner":"ana"}`, ip); rec.Code != http.StatusCreated {
		t.Fatalf("crear espacio: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.req(http.MethodPost, "/nexo/kb/v1/spaces", "carla", `{"slug":"privado","name":"Privado","visibility":"restricted","owner":"ana"}`, ip); rec.Code != http.StatusCreated {
		t.Fatalf("crear espacio restringido: %d %s", rec.Code, rec.Body.String())
	}
	anaCookie := managedDashboardLogin(t, env.srv, env.tokens["ana"], false)
	xss := "# Seguridad\n\n## Entradas\n\nValidar siempre las entradas del usuario con **listas blancas**.\n\n" +
		"<script>alert('x')</script>\n\n[clic aquí](javascript:alert(1))\n\n<img src=x onerror=alert(2)>\n\n" +
		"```js\nconsole.log('<b>hola</b>')\n```\n\n| Campo | Regla |\n|---|---|\n| nombre | requerido |\n"
	kbPanelUpload(t, env, anaCookie, "arquitectura", []kbPart{
		{"file", "backend/logging.md", "# Logging\n\n## Niveles\n\nUsar Information para eventos de negocio y Error para fallas.\n"},
		{"file", "backend/seguridad.md", xss},
	})
	kbPanelUpload(t, env, anaCookie, "privado", []kbPart{{"file", "secreto.md", "# Secreto\n\n## Clave\n\nTema confidencial de presupuesto.\n"}})

	// Marca y textos en español en el marco y el menú.
	home := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb", anaCookie)
	body := home.Body.String()
	for _, want := range []string{"Nexo IA", "Memoria y estándares del equipo", `lang="es"`, "nexo_theme.css", `href="/dashboard/kb"`, ">Conocimiento</a>", "Cómo funciona", "Crear un espacio", "Subir documentos", "Consultar", "Hecho", "Cerrar sesión"} {
		if !strings.Contains(body, want) {
			t.Errorf("/dashboard/kb sin %q", want)
		}
	}
	if strings.Contains(body, "An elephant never forgets") || strings.Contains(body, "CLOUD ACTIVE") {
		t.Errorf("quedaron textos de Engram en el marco")
	}

	// Buscador en el espacio: respuesta con cita, estado, vigencia y enlace.
	sr := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb/spaces/arquitectura?q="+url.QueryEscape("nivel Information eventos de negocio"), anaCookie)
	sb := sr.Body.String()
	if sr.Code != http.StatusOK {
		t.Fatalf("búsqueda: %d", sr.Code)
	}
	for _, want := range []string{"nx-answer", "Logging &gt; Niveles", "Vigente", "Versión 1", "/dashboard/kb/spaces/arquitectura/doc?doc=backend%2Flogging", "<mark>"} {
		if !strings.Contains(sb, want) {
			t.Errorf("respuesta sin %q", want)
		}
	}
	// Sin estándar: mensaje literal.
	none := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb/spaces/arquitectura?q="+url.QueryEscape("kubernetes helm charts"), anaCookie)
	if !strings.Contains(none.Body.String(), kbNoStandard) {
		t.Errorf("sin resultados no muestra el mensaje literal")
	}
	// Fecha inválida: aviso claro, sin 500.
	if bad := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb/spaces/arquitectura?q=logging&at=ayer", anaCookie); bad.Code != http.StatusOK || !strings.Contains(bad.Body.String(), "AAAA-MM-DD") {
		t.Errorf("fecha inválida: %d", bad.Code)
	}
	// Antes de la publicación no había estándar (bitemporal por "en fecha").
	if old := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb/spaces/arquitectura?q=Information&at=2000-01-01", anaCookie); !strings.Contains(old.Body.String(), kbNoStandard) {
		t.Errorf("en fecha 2000-01-01 debería no haber estándar")
	}

	// Lista por carpeta con el título como texto principal y estado en español.
	space := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb/spaces/arquitectura", anaCookie).Body.String()
	for _, want := range []string{`class="nx-folder"`, "backend <span", ">Logging</a>", "nx-doc-path", "Vigente", "Probar sin publicar", `aria-current="page"`} {
		if !strings.Contains(space, want) {
			t.Errorf("página del espacio sin %q", want)
		}
	}
	if strings.Contains(space, ">active<") || strings.Contains(space, "ACTIVE") {
		t.Errorf("estado en inglés en la página del espacio")
	}

	// Markdown renderizado y seguro.
	doc := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb/spaces/arquitectura/doc?doc=backend/seguridad", anaCookie)
	db := doc.Body.String()
	if doc.Code != http.StatusOK {
		t.Fatalf("documento: %d", doc.Code)
	}
	for _, want := range []string{"<h2>Entradas</h2>", "<strong>listas blancas</strong>", `<pre><code class="language-js">`, "<table>", "&lt;script&gt;", "Vigente", "Versiones"} {
		if !strings.Contains(db, want) {
			t.Errorf("documento sin %q: %s", want, db[strings.Index(db, "nx-prose"):])
		}
	}
	for _, bad := range []string{"<script>alert", "<img src=x", `href="javascript:`, "## Entradas", "```"} {
		if strings.Contains(db, bad) {
			t.Errorf("documento con contenido inseguro o crudo %q", bad)
		}
	}

	// Una segunda versión muestra "qué cambió".
	kbPanelUpload(t, env, anaCookie, "arquitectura", []kbPart{{"file", "backend/logging.md", "# Logging\n\n## Niveles\n\nUsar Warning para reintentos y Error para fallas.\n\n## Formato\n\nJSON.\n"}})
	ch := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb/spaces/arquitectura/doc?doc=backend/logging&v=2&changes=1", anaCookie).Body.String()
	for _, want := range []string{"Qué cambió de la versión 1 a la 2", "Sección cambiada: Logging &gt; Niveles", "Sección nueva: Logging &gt; Formato"} {
		if !strings.Contains(ch, want) {
			t.Errorf("qué cambió sin %q: %s", want, ch[strings.Index(ch, "nx-prose"):])
		}
	}

	// Un usuario sin acceso no ve el espacio restringido: ni en la lista, ni en
	// la búsqueda global, ni por URL directa.
	luis := managedDashboardLogin(t, env.srv, env.tokens["luis"], false)
	lh := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb", luis).Body.String()
	if strings.Contains(lh, "/dashboard/kb/spaces/privado") || strings.Contains(lh, "Privado") {
		t.Errorf("el lector ve el espacio restringido en la lista")
	}
	if !strings.Contains(lh, "/dashboard/kb/spaces/arquitectura") {
		t.Errorf("el lector no ve el espacio abierto")
	}
	if !strings.Contains(lh, "No aplica a tu rol") {
		t.Errorf("el lector debería ver pasos que no aplican a su rol")
	}
	ls := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb?q="+url.QueryEscape("presupuesto confidencial"), luis).Body.String()
	if strings.Contains(ls, "Secreto") || strings.Contains(ls, "presupuesto</mark>") || !strings.Contains(ls, kbNoStandard) {
		t.Errorf("la búsqueda global del lector filtra el espacio restringido")
	}
	if rec := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb/spaces/privado", luis); rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "Secreto") {
		t.Errorf("el lector abre el espacio restringido por URL: %d", rec.Code)
	}
	if rec := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb/spaces/privado/doc?doc=secreto", luis); rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "presupuesto") {
		t.Errorf("el lector abre un documento restringido: %d", rec.Code)
	}
	// El lector busca en el espacio abierto y no ve el formulario de subida.
	lsp := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb/spaces/arquitectura?q=logging", luis).Body.String()
	if !strings.Contains(lsp, "nx-answer") || strings.Contains(lsp, "Probar sin publicar") || strings.Contains(lsp, "Retirar") {
		t.Errorf("lector: búsqueda o permisos de escritura incorrectos")
	}
	// La responsable sí ve el restringido en la búsqueda global.
	as := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb?q="+url.QueryEscape("presupuesto confidencial"), anaCookie).Body.String()
	if !strings.Contains(as, "privado &gt; Secreto") {
		t.Errorf("la responsable no encuentra su espacio restringido")
	}

	// Login en español.
	login := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/login", nil).Body.String()
	for _, want := range []string{"Iniciar sesión", "Token de acceso", "Busca la regla vigente"} {
		if !strings.Contains(login, want) {
			t.Errorf("login sin %q", want)
		}
	}
}
