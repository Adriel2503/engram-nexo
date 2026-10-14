package cloudserver

// Nexo IA · SG-07 — exportación de la revisión de accesos (CSV y JSON) contra
// Postgres real: contenido, huella, inyección de fórmulas, auditoría, huella
// de la revisión registrada, token de solo lectura y acceso negado.

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

const (
	apiReviewPath   = "/nexo/admin/access-review"
	panelReviewPath = "/dashboard/admin/audit/access-review"
)

func exportSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// parseExportCSV lee el CSV sin el BOM y devuelve las filas como mapas.
func parseExportCSV(t *testing.T, body []byte) []map[string]string {
	t.Helper()
	if !bytes.HasPrefix(body, []byte("\xEF\xBB\xBF")) {
		t.Fatalf("el CSV debe empezar con BOM UTF-8")
	}
	rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(body, []byte("\xEF\xBB\xBF")))).ReadAll()
	if err != nil {
		t.Fatalf("CSV ilegible: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("CSV sin filas: %d", len(rows))
	}
	var out []map[string]string
	for _, r := range rows[1:] {
		if len(r) != len(rows[0]) {
			t.Fatalf("fila con %d columnas, títulos %d", len(r), len(rows[0]))
		}
		m := map[string]string{}
		for i, h := range rows[0] {
			m[h] = r[i]
		}
		out = append(out, m)
	}
	return out
}

// attachmentName saca el nombre del archivo de Content-Disposition.
func attachmentName(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	cd := rec.Header().Get("Content-Disposition")
	const prefix = `attachment; filename="`
	if !strings.HasPrefix(cd, prefix) || !strings.HasSuffix(cd, `"`) {
		t.Fatalf("Content-Disposition inesperado: %q", cd)
	}
	return strings.TrimSuffix(strings.TrimPrefix(cd, prefix), `"`)
}

func TestNexoAccessReviewExportAPI(t *testing.T) {
	env := newAuditPanelEnv(t)
	// Datos con aspecto de fórmula: nombre de persona y de token.
	if _, err := env.db.Exec(`UPDATE cloud_principals SET display_name = '=HYPERLINK("http://x","clic")' WHERE id = $1::bigint`, env.ids["ana"]); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`UPDATE cloud_principal_tokens SET name = '@SUM(A1)' WHERE principal_id = $1::bigint AND name = 'ana'`, env.ids["ana"]); err != nil {
		t.Fatal(err)
	}
	// La respuesta normal no cambia: sin format sigue siendo el reporte.
	plain := env.req(http.MethodGet, apiReviewPath, "luis", "", "10.91.0.9")
	if plain.Code != http.StatusOK || strings.Contains(plain.Body.String(), "content_sha256") || plain.Header().Get("Content-Disposition") != "" {
		t.Fatalf("respuesta por defecto: %d %.200s", plain.Code, plain.Body.String())
	}
	var base evidence.AccessReview
	if err := json.Unmarshal(plain.Body.Bytes(), &base); err != nil || len(base.People) != 5 {
		t.Fatalf("reporte por defecto: %v %d", err, len(base.People))
	}

	// JSON.
	rec := env.req(http.MethodGet, apiReviewPath+"?format=json&inactive_days=45", "luis", "", "10.91.0.9")
	env.bodies["EXPORT api json"] = rec.Body.String()
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("export JSON: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	verdict, err := evidence.VerifyReviewExportJSON(rec.Body.Bytes())
	if err != nil || !verdict.OK {
		t.Fatalf("la huella del JSON no verifica: %+v %v", verdict, err)
	}
	if rec.Header().Get("X-Nexo-Content-SHA256") != verdict.Declared {
		t.Errorf("cabecera %q, huella del archivo %q", rec.Header().Get("X-Nexo-Content-SHA256"), verdict.Declared)
	}
	if name := attachmentName(t, rec); !strings.HasPrefix(name, "nexo-revision-accesos-") || !strings.HasSuffix(name, "-"+verdict.Declared+".json") {
		t.Errorf("nombre del JSON: %s", name)
	}
	var doc evidence.ReviewExport
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.GeneratedBy.Name != "luis" || doc.GeneratedBy.PrincipalID != env.ids["luis"] || doc.Filters.InactiveDays != 45 || doc.Filters.OnlyFlagged ||
		doc.Report != "access_review" || len(doc.People) != 5 || doc.GeneratedAt.IsZero() || doc.Control == "" {
		t.Errorf("encabezado del JSON: %+v", doc)
	}

	// CSV: la huella es la de los bytes, y va en el nombre y en la cabecera.
	rec = env.req(http.MethodGet, apiReviewPath+"?format=csv", "luis", "", "10.91.0.9")
	env.bodies["EXPORT api csv"] = rec.Body.String()
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("export CSV: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	sha := exportSHA(rec.Body.Bytes())
	if rec.Header().Get("X-Nexo-Content-SHA256") != sha || !strings.HasSuffix(attachmentName(t, rec), "-"+sha+".csv") {
		t.Errorf("huella del CSV: cabecera %q, nombre %q, bytes %s", rec.Header().Get("X-Nexo-Content-SHA256"), rec.Header().Get("Content-Disposition"), sha)
	}
	rows := parseExportCSV(t, rec.Body.Bytes())
	var tokens, roles int
	var sawFormulaName, sawFormulaToken bool
	for _, row := range rows {
		switch row["tipo"] {
		case "token":
			tokens++
		case "rol":
			roles++
		}
		if row["generado_por"] != "luis" || row["umbral_inactividad_dias"] != "90" {
			t.Errorf("fila sin datos de la exportación: %v", row)
		}
		if row["nombre"] == `'=HYPERLINK("http://x","clic")` {
			sawFormulaName = true
		}
		if row["token_nombre"] == "'@SUM(A1)" {
			sawFormulaToken = true
		}
		for h, cell := range row {
			if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
				t.Errorf("columna %s: %q puede abrirse como fórmula", h, cell)
			}
		}
		if row["tipo"] == "token" && (row["token_id"] == "" || row["token_alcance"] == "") {
			t.Errorf("token sin id o alcance: %v", row)
		}
	}
	if tokens < 5 || roles < 5 || !sawFormulaName || !sawFormulaToken {
		t.Errorf("tokens=%d roles=%d formula-nombre=%v formula-token=%v", tokens, roles, sawFormulaName, sawFormulaToken)
	}

	// Solo con marcas: queda dicho en el archivo. Con un segundo superadmin (SG-06)
	// la persona deja de llevar la marca single_superadmin y vuelve a haber gente sin marcas.
	if _, err := env.db.Exec(`UPDATE nexo_role_bindings SET role = 'superadmin' WHERE principal_id = $1::bigint AND scope_type = 'platform'`, env.ids["pedro"]); err != nil {
		t.Fatal(err)
	}
	rec = env.req(http.MethodGet, apiReviewPath+"?format=json&only=flags", "luis", "", "10.91.0.9")
	var flagged evidence.ReviewExport
	if err := json.Unmarshal(rec.Body.Bytes(), &flagged); err != nil || !flagged.Filters.OnlyFlagged || len(flagged.People) >= len(doc.People) {
		t.Errorf("only=flags: %v %+v", err, flagged.Filters)
	}

	// Formato desconocido y días inválidos.
	for _, q := range []string{"?format=xml", "?format=csv&inactive_days=0", "?format=json&inactive_days=x"} {
		if rec := env.req(http.MethodGet, apiReviewPath+q, "luis", "", "10.91.0.9"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}

	// Cada descarga válida quedó en la auditoría con su huella.
	rowsDB, err := env.db.Query(`SELECT metadata->>'content_sha256', metadata->>'format', outcome FROM cloud_auth_audit_log WHERE action = $1 AND actor_principal_id = $2::bigint ORDER BY id`, nexoAuditActionAccessReviewExport, env.ids["luis"])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rowsDB.Close() }()
	var logged []string
	for rowsDB.Next() {
		var h, f, outcome string
		if err := rowsDB.Scan(&h, &f, &outcome); err != nil {
			t.Fatal(err)
		}
		if len(h) != 64 || outcome != "success" {
			t.Errorf("evento de descarga: %q %q", h, outcome)
		}
		logged = append(logged, f+":"+h)
	}
	if len(logged) != 3 || !strings.HasPrefix(logged[0], "json:"+verdict.Declared) || !strings.HasPrefix(logged[1], "csv:"+sha) {
		t.Errorf("descargas registradas: %v", logged)
	}
	env.assertNoLeaks()
}

// Una descarga rechazada no deja datos ni evento: miembro y sin token reciben
// el mismo rechazo que la lectura normal.
func TestNexoAccessReviewExportDenied(t *testing.T) {
	env := newAuditPanelEnv(t)
	for _, q := range []string{"?format=csv", "?format=json"} {
		rec := env.req(http.MethodGet, apiReviewPath+q, "ana", "", "10.91.0.9")
		if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "content_sha256") || rec.Header().Get("Content-Disposition") != "" {
			t.Errorf("API como miembro %s: %d %.200s", q, rec.Code, rec.Body.String())
		}
		if rec := env.req(http.MethodGet, apiReviewPath+q, "raw:no-es-un-token", "", "10.91.0.9"); rec.Code != http.StatusUnauthorized {
			t.Errorf("API sin token válido %s: %d", q, rec.Code)
		}
		rec = env.get("ana", panelReviewPath+"/export"+q)
		if rec.Code != http.StatusForbidden || rec.Header().Get("Content-Disposition") != "" || strings.Contains(rec.Body.String(), "content_sha256") {
			t.Errorf("panel como miembro %s: %d", q, rec.Code)
		}
		if rec := env.get("", panelReviewPath+"/export"+q); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/dashboard/login") {
			t.Errorf("panel sin sesión %s: %d", q, rec.Code)
		}
	}
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = $1`, nexoAuditActionAccessReviewExport); n != 0 {
		t.Errorf("los rechazos no son descargas: %d eventos", n)
	}
	if n := env.count(`SELECT count(*) FROM nexo_authz_events WHERE action = 'authz.denied' AND actor_principal_id = $1::bigint AND details->>'route' LIKE '%access-review%'`, env.ids["ana"]); n == 0 {
		t.Errorf("el rechazo del miembro debe quedar como authz.denied")
	}
}

// Un token de solo lectura puede bajar el archivo (es una lectura) pero no
// puede registrar la revisión.
func TestNexoAccessReviewExportReadOnlyToken(t *testing.T) {
	env := newAuditPanelEnv(t)
	for _, q := range []string{"?format=csv", "?format=json"} {
		rec := env.req(http.MethodGet, apiReviewPath+q, "luis-read", "", "10.91.0.9")
		if rec.Code != http.StatusOK || rec.Header().Get("X-Nexo-Content-SHA256") == "" {
			t.Errorf("API con token read %s: %d", q, rec.Code)
		}
		rec = env.get("luis-read", panelReviewPath+"/export"+q+"&inactive_days=30")
		if rec.Code != http.StatusOK || rec.Header().Get("X-Nexo-Content-SHA256") == "" {
			t.Errorf("panel con sesión read %s: %d", q, rec.Code)
		}
	}
	if rec := env.req(http.MethodPost, apiReviewPath+":complete", "luis-read", `{"notes":"no debe"}`, "10.91.0.9"); rec.Code != http.StatusForbidden {
		t.Errorf("completar con token read: %d", rec.Code)
	}
	if rec := env.post("luis-read", panelReviewPath+"/complete", "csrf_token="+env.csrfFor("luis-read")); rec.Code != http.StatusForbidden {
		t.Errorf("completar desde el panel con sesión read: %d", rec.Code)
	}
	if n := env.count(`SELECT count(*) FROM nexo_access_reviews`); n != 0 {
		t.Errorf("no debía registrarse ninguna revisión: %d", n)
	}
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = $1 AND actor_principal_id = $2::bigint`, nexoAuditActionAccessReviewExport, env.ids["luis"]); n != 4 {
		t.Errorf("descargas del token read en la auditoría: %d", n)
	}
	env.assertNoLeaks()
}

// El panel: botones, descarga con los mismos filtros y huella de la revisión
// registrada, que coincide con la de la exportación hecha justo antes.
func TestNexoAccessReviewExportPanel(t *testing.T) {
	env := newAuditPanelEnv(t)
	body := env.mustBody("luis", panelReviewPath+"?inactive_days=60&only=flags", http.StatusOK)
	for _, want := range []string{"Descargar CSV", "Descargar JSON", "Descargar como evidencia", "solo a las personas con marcas",
		`href="/dashboard/admin/audit/access-review/export?format=csv&amp;inactive_days=60&amp;only=flags"`,
		`href="/dashboard/admin/audit/access-review/export?format=json&amp;inactive_days=60&amp;only=flags"`} {
		if !strings.Contains(body, want) {
			t.Errorf("pantalla sin %q", want)
		}
	}
	if body = env.mustBody("luis", panelReviewPath, http.StatusOK); !strings.Contains(body, "a todas las personas y cuentas") || strings.Contains(body, "Huella del contenido revisado") {
		t.Errorf("sin revisión previa no hay huella que mostrar")
	}

	// Descarga del panel.
	rec := env.get("luis", panelReviewPath+"/export?format=json&inactive_days=60")
	env.bodies["EXPORT panel json"] = rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("export del panel: %d %.200s", rec.Code, rec.Body.String())
	}
	verdict, err := evidence.VerifyReviewExportJSON(rec.Body.Bytes())
	if err != nil || !verdict.OK {
		t.Fatalf("huella del JSON del panel: %+v %v", verdict, err)
	}
	var doc evidence.ReviewExport
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || doc.Filters.InactiveDays != 60 || doc.GeneratedBy.Name != "luis" {
		t.Fatalf("encabezado: %v %+v", err, doc.Filters)
	}
	for _, q := range []string{"/export", "/export?format=pdf", "/export?format=csv&inactive_days=0"} {
		if rec := env.get("luis", panelReviewPath+q); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Revisión de accesos") {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	rec = env.get("luis", panelReviewPath+"/export?format=csv")
	env.bodies["EXPORT panel csv"] = rec.Body.String()
	if sha := exportSHA(rec.Body.Bytes()); rec.Code != http.StatusOK || rec.Header().Get("X-Nexo-Content-SHA256") != sha || !strings.Contains(rec.Header().Get("Content-Disposition"), sha+".csv") {
		t.Errorf("CSV del panel: %d", rec.Code)
	}
	parseExportCSV(t, rec.Body.Bytes())

	// Registrar la revisión guarda la huella del contenido vigente.
	csrf := env.csrf("luis", panelReviewPath)
	exp := env.get("luis", panelReviewPath+"/export?format=json")
	var before evidence.ReviewExport
	if err := json.Unmarshal(exp.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	done := env.post("luis", panelReviewPath+"/complete", "csrf_token="+csrf+"&inactive_days=90&notes=con+archivo")
	if done.Code != http.StatusSeeOther {
		t.Fatalf("completar: %d", done.Code)
	}
	var stored string
	if err := env.db.QueryRow(`SELECT report_sha256 FROM nexo_access_reviews ORDER BY id DESC LIMIT 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 64 || stored != before.ReviewDigest {
		t.Errorf("huella guardada %q, review_digest de la exportación previa %q", stored, before.ReviewDigest)
	}
	// La pantalla la muestra y la API la expone.
	page := env.mustBody("luis", done.Header().Get("Location"), http.StatusOK)
	if !strings.Contains(page, "Huella del contenido revisado (SHA-256)") || !strings.Contains(page, stored) {
		t.Errorf("la última revisión no muestra la huella %s", stored)
	}
	api := env.req(http.MethodGet, apiReviewPath, "luis", "", "10.91.0.9")
	var rep evidence.AccessReview
	if err := json.Unmarshal(api.Body.Bytes(), &rep); err != nil || rep.LastReview == nil || rep.LastReview.ReportSHA256 != stored {
		t.Errorf("la API no expone la huella de la revisión: %v", err)
	}
	// Y la API de completar la devuelve y la audita.
	res := env.req(http.MethodPost, apiReviewPath+":complete", "luis", `{"notes":"por la API"}`, "10.91.0.9")
	var rec2 evidence.ReviewRecord
	if err := json.Unmarshal(res.Body.Bytes(), &rec2); err != nil || res.Code != http.StatusCreated || len(rec2.ReportSHA256) != 64 {
		t.Errorf("completar por la API: %d %v", res.Code, err)
	}
	// Las descargas del panel quedaron con su huella.
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = $1 AND metadata->>'content_sha256' = $2`, nexoAuditActionAccessReviewExport, verdict.Declared); n != 1 {
		t.Errorf("evento de la descarga del panel: %d", n)
	}
	env.assertNoLeaks()
}
