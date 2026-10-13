package cloudserver

// Nexo IA · NX-10 entrega 2b — subida por la API (multipart) y por el panel
// (sesión, CSRF, alcance del token), feed, cambios y propuestas por HTTP,
// contra Postgres (CLOUDSTORE_TEST_DSN) con tokens reales.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

type kbPart struct{ field, name, body string }

func kbMultipart(t *testing.T, parts []kbPart, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		w, err := mw.CreateFormFile(p.field, p.name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(p.body))
	}
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func kbZip(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	return buf.String()
}

func kbSend(h http.Handler, method, path, token string, body *bytes.Buffer, ctype string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = "10.77.0.1:40000"
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNexoKBUploadAPIAndDashboard(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	const ip = "10.77.0.1"
	if rec := env.req(http.MethodPost, "/nexo/kb/v1/spaces", "carla", `{"slug":"arquitectura","name":"Arquitectura","owner":"ana"}`, ip); rec.Code != http.StatusCreated {
		t.Fatalf("crear espacio: %d %s", rec.Code, rec.Body.String())
	}
	env.issue("ana", "ana-read", security.TokenIssue{Days: 30, Scope: "read"})
	wiki := kbZip(t, map[string]string{
		"Wiki/.order":               "Logging\n",
		"Wiki/Logging.md":           "# Niveles\nUsar Information y Error.\n",
		"Wiki/[TO_REMOVE]/Viejo.md": "# Viejo\nlog4net.\n",
		"Wiki/.attachments/a.png":   "png",
	})
	body, ctype := kbMultipart(t, []kbPart{{"zip", "Wiki.zip", wiki}, {"file", "guia.md", "# Guia\nTexto de guia.\n"}}, map[string]string{"category": "lineamiento"})
	// Lector (rol implícito) y token read: 403 antes de leer el cuerpo.
	if rec := kbSend(env.h, http.MethodPost, "/nexo/kb/v1/spaces/arquitectura/uploads", env.tokens["luis"], bytes.NewBuffer(body.Bytes()), ctype, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("lector sube: %d %s", rec.Code, rec.Body.String())
	}
	if rec := kbSend(env.h, http.MethodPost, "/nexo/kb/v1/spaces/arquitectura/uploads", env.tokens["ana-read"], bytes.NewBuffer(body.Bytes()), ctype, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("token read sube: %d %s", rec.Code, rec.Body.String())
	}
	rec := kbSend(env.h, http.MethodPost, "/nexo/kb/v1/spaces/arquitectura/uploads", env.tokens["ana"], bytes.NewBuffer(body.Bytes()), ctype, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("subida: %d %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Source  string         `json:"source"`
		Summary map[string]int `json:"summary"`
		RunID   string         `json:"run_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Source != "azure-devops-wiki" || res.Summary["created"] != 2 || res.Summary["retired"] != 1 || res.RunID == "" {
		t.Fatalf("resultado: %s", rec.Body.String())
	}
	// Zip-slip y no multipart.
	evil, ectype := kbMultipart(t, []kbPart{{"zip", "x.zip", kbZip(t, map[string]string{"../../fuera.md": "# x\n"})}}, nil)
	if rec := kbSend(env.h, http.MethodPost, "/nexo/kb/v1/spaces/arquitectura/uploads", env.tokens["ana"], evil, ectype, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("zip-slip: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.req(http.MethodPost, "/nexo/kb/v1/spaces/arquitectura/uploads", "ana", `{"x":1}`, ip); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("no multipart: %d", rec.Code)
	}
	// Búsqueda con enlace absoluto, cambios, feed y ejecuciones.
	sr := env.req(http.MethodGet, "/nexo/kb/v1/search?q="+url.QueryEscape("niveles Information"), "luis", "", ip)
	if !strings.Contains(sr.Body.String(), `"link":"http://example.com/dashboard/kb/spaces/arquitectura/doc?doc=logging`) {
		t.Fatalf("enlace: %s", sr.Body.String())
	}
	if rec := env.req(http.MethodGet, "/nexo/kb/v1/changes?since=2020-01-01", "luis", "", ip); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"change":"added"`) {
		t.Fatalf("cambios: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.req(http.MethodGet, "/nexo/kb/v1/changes", "luis", "", ip); rec.Code != http.StatusBadRequest {
		t.Fatalf("cambios sin since: %d", rec.Code)
	}
	if rec := env.req(http.MethodGet, "/nexo/kb/v1/sync?since=0", "luis", "", ip); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"visible_spaces":[{`) {
		t.Fatalf("feed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.req(http.MethodGet, "/nexo/kb/v1/spaces/arquitectura/publish-runs", "ana", "", ip); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"kind":"upload"`) {
		t.Fatalf("ejecuciones: %d %s", rec.Code, rec.Body.String())
	}
	// Propuestas por HTTP: lector no propone (rol implícito lector).
	if rec := env.req(http.MethodPost, "/nexo/kb/v1/spaces/arquitectura/proposals", "luis", `{"title":"x","content":"# x\ny\n"}`, ip); rec.Code != http.StatusForbidden {
		t.Fatalf("lector propone: %d", rec.Code)
	}
	prop := env.req(http.MethodPost, "/nexo/kb/v1/spaces/arquitectura/proposals", "ana", `{"title":"Nueva regla","content":"# Regla\nUsar trazas.\n","valid_from":"2030-01-01T00:00:00Z"}`, ip)
	if prop.Code != http.StatusCreated {
		t.Fatalf("propuesta: %d %s", prop.Code, prop.Body.String())
	}
	var p struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(prop.Body.Bytes(), &p)
	if rec := env.req(http.MethodGet, "/nexo/kb/v1/proposals/"+p.ID, "luis", "", ip); rec.Code != http.StatusNotFound {
		t.Fatalf("propuesta ajena: %d", rec.Code)
	}
	if rec := env.req(http.MethodPost, "/nexo/kb/v1/proposals/"+p.ID+":approve", "ana", `{"note":"ok"}`, ip); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"approved"`) {
		t.Fatalf("aprobación: %d %s", rec.Code, rec.Body.String())
	}

	// ─── Panel ───────────────────────────────────────────────────────────────
	cookie := managedDashboardLogin(t, env.srv, env.tokens["ana"], false)
	home := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb", cookie)
	if home.Code != http.StatusOK || !strings.Contains(home.Body.String(), "/dashboard/kb/spaces/arquitectura") {
		t.Fatalf("panel: %d %s", home.Code, home.Body.String())
	}
	page := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb/spaces/arquitectura", cookie)
	m := regexp.MustCompile(`upload\?csrf_token=([A-Za-z0-9_%-]+)`).FindStringSubmatch(page.Body.String())
	if page.Code != http.StatusOK || m == nil || !strings.Contains(page.Body.String(), "logging") {
		t.Fatalf("página del espacio: %d", page.Code)
	}
	csrf, _ := url.QueryUnescape(m[1])
	form, fctype := kbMultipart(t, []kbPart{{"file", "carpeta/nueva.md", "# Nueva\nContenido nuevo.\n"}}, map[string]string{"category": "lineamiento"})
	if rec := kbSend(env.h, http.MethodPost, "/dashboard/kb/spaces/arquitectura/upload", "", bytes.NewBuffer(form.Bytes()), fctype, cookie); rec.Code != http.StatusForbidden {
		t.Fatalf("subida sin CSRF: %d", rec.Code)
	}
	up := kbSend(env.h, http.MethodPost, "/dashboard/kb/spaces/arquitectura/upload?csrf_token="+url.QueryEscape(csrf), "", bytes.NewBuffer(form.Bytes()), fctype, cookie)
	if up.Code != http.StatusOK || !strings.Contains(up.Body.String(), "RESULTADO DE LA SUBIDA") || !strings.Contains(up.Body.String(), "carpeta/nueva") {
		t.Fatalf("subida por el panel: %d %s", up.Code, up.Body.String())
	}
	doc := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb/spaces/arquitectura/doc?doc=logging", cookie)
	if doc.Code != http.StatusOK || !strings.Contains(doc.Body.String(), `id="niveles"`) {
		t.Fatalf("documento: %d %s", doc.Code, doc.Body.String())
	}
	// Retirar con CSRF.
	retire := performDashboardForm(env.srv, http.MethodPost, "/dashboard/kb/spaces/arquitectura/retire", "csrf_token="+url.QueryEscape(csrf)+"&doc_key=guia&note=x", cookie, false)
	if retire.Code != http.StatusSeeOther || env.count(`SELECT count(*) FROM nexo_kb_documents WHERE doc_key = 'guia' AND status = 'retired'`) != 1 {
		t.Fatalf("retiro: %d %s", retire.Code, retire.Body.String())
	}
	// Sesión abierta con token read: mira pero no sube.
	readCookie := managedDashboardLogin(t, env.srv, env.tokens["ana-read"], false)
	if rec := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb/spaces/arquitectura", readCookie); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Subir documentos") {
		t.Fatalf("sesión read ve el formulario de subida: %d", rec.Code)
	}
	if rec := kbSend(env.h, http.MethodPost, "/dashboard/kb/spaces/arquitectura/upload?csrf_token=x", "", bytes.NewBuffer(form.Bytes()), fctype, readCookie); rec.Code != http.StatusForbidden {
		t.Fatalf("sesión read sube: %d", rec.Code)
	}
	// Un lector entra al panel pero no ve el formulario.
	luisCookie := managedDashboardLogin(t, env.srv, env.tokens["luis"], false)
	if rec := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/kb/spaces/arquitectura", luisCookie); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Subir documentos") {
		t.Fatalf("lector en el panel: %d", rec.Code)
	}
}
