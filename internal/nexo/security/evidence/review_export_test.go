package evidence

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func exportFixture() AccessReview {
	at := time.Date(2026, 10, 13, 15, 30, 0, 0, time.UTC)
	last := at.Add(-120 * 24 * time.Hour)
	exp := at.Add(30 * 24 * time.Hour)
	return AccessReview{
		GeneratedAt: at, InactiveDays: 90, DisableInactive: 0, SAStaleDays: 45,
		Summary: ReviewSummary{People: 3, Enabled: 3, Inactive: 1, ActiveTokens: 2, TokensNoExpiry: 1},
		People: []ReviewPerson{
			{PrincipalID: "1", Username: "carla", Email: "carla@rp.pe", DisplayName: "Carla Díaz", Kind: "human", Enabled: true, CreatedAt: last,
				EngramRole: "admin", PlatformRole: "superadmin", PlatformSince: &last, PlatformBy: "sistema",
				Scopes:         []ReviewBinding{{ScopeType: "space", ScopeKey: "arquitectura", Layer: LayerKnowledge, Role: "responsable", Since: last, GrantedBy: "Carla Díaz"}},
				ProjectGrants:  []ReviewBinding{{ScopeType: "project", ScopeKey: "erp", Layer: LayerProjects, Role: "publicador (permiso de Engram)", Since: last}},
				Tokens:         []ReviewToken{{ID: "7", Name: "portatil", Scope: "write", CreatedAt: last, ExpiresAt: &exp, LastUsedAt: &last}},
				LastActivityAt: &last, InactiveDays: 120, Flags: []string{"inactive"}},
			{PrincipalID: "2", Username: "=cmd", DisplayName: "+Pedro \"El\" Admin, Jr", Email: "@pedro", Kind: "human", Enabled: true, CreatedAt: last,
				EngramRole: "member", PlatformRole: "miembro", Scopes: []ReviewBinding{}, ProjectGrants: []ReviewBinding{},
				Tokens: []ReviewToken{{ID: "9", Name: "-malo", Scope: "read", CreatedAt: last, NoExpiry: true}}, Flags: []string{"token_no_expiry", "token_never_used"}},
			{PrincipalID: "3", Username: "ana", DisplayName: "Ana\nSalto Linea", Kind: "human", Enabled: true, CreatedAt: last,
				EngramRole: "member", PlatformRole: "miembro", Scopes: []ReviewBinding{}, ProjectGrants: []ReviewBinding{}, Tokens: []ReviewToken{}, Flags: []string{}},
		},
	}
}

var exportBy = ReviewExportActor{PrincipalID: "5", Name: "Luis Auditor"}

func TestReviewExportJSONVerifies(t *testing.T) {
	rep := exportFixture()
	before, _ := json.Marshal(rep)
	exp := NewReviewExport(rep, exportBy, false)
	body, sha, err := exp.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := json.Marshal(rep); !bytes.Equal(before, after) {
		t.Fatalf("NewReviewExport modificó el reporte")
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc[ReviewExportHashField] != sha || len(sha) != 64 {
		t.Fatalf("content_sha256 = %v, huella = %s", doc[ReviewExportHashField], sha)
	}
	for _, k := range []string{"report", "control", "format_version", "generated_at", "generated_by", "filters", "policy", "review_digest", "summary", "people"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("falta el campo %q", k)
		}
	}
	if doc["generated_at"] != "2026-10-13T15:30:00Z" || doc["review_digest"] != ReportDigest(rep) {
		t.Errorf("generated_at=%v review_digest=%v", doc["generated_at"], doc["review_digest"])
	}
	v, err := VerifyReviewExportJSON(body)
	if err != nil || !v.OK || v.Declared != sha || v.Computed != sha {
		t.Fatalf("verificación: %+v %v", v, err)
	}
	// Mismo contenido, misma huella.
	_, sha2, _ := NewReviewExport(exportFixture(), exportBy, false).JSON()
	if sha2 != sha {
		t.Errorf("la exportación no es determinista: %s != %s", sha, sha2)
	}
	// Cualquier cambio rompe la verificación.
	for _, edit := range [][2]string{{"Carla", "Karla"}, {`"inactive_days": 90`, `"inactive_days": 91`}, {"Luis Auditor", "Otro"}} {
		bad := strings.Replace(string(body), edit[0], edit[1], 1)
		if bad == string(body) {
			t.Fatalf("la edición %q no cambió nada", edit[0])
		}
		if v, err := VerifyReviewExportJSON([]byte(bad)); err != nil || v.OK {
			t.Errorf("cambiar %q no se detectó: %+v %v", edit[0], v, err)
		}
	}
	if _, err := VerifyReviewExportJSON([]byte(`{"a":1}`)); err == nil {
		t.Errorf("un JSON sin huella debe fallar")
	}
	// Los controles y el separador de línea de Unicode no entran al documento.
	if strings.Contains(string(body), " ") || strings.Contains(string(body), "Ana\\nSalto") {
		t.Errorf("los caracteres de control deben quedar como espacio")
	}
	// Forma canónica: se puede reproducir con cualquier lenguaje (claves
	// ordenadas, sin espacios, sin escapar < > &).
	var tree map[string]any
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	_ = d.Decode(&tree)
	delete(tree, ReviewExportHashField)
	canon, _ := canonicalJSON(tree)
	if sha256Hex(canon) != sha || bytes.ContainsAny(canon, "\n\t") || bytes.Contains(canon, []byte(`": `)) {
		t.Errorf("forma canónica inesperada: %.120s", canon)
	}
}

func TestReviewExportOnlyFlagged(t *testing.T) {
	exp := NewReviewExport(exportFixture(), exportBy, true)
	if len(exp.People) != 2 || exp.Summary.People != 3 || !exp.Filters.OnlyFlagged {
		t.Fatalf("personas=%d resumen=%d filtros=%+v", len(exp.People), exp.Summary.People, exp.Filters)
	}
	_, shaAll, _ := NewReviewExport(exportFixture(), exportBy, false).JSON()
	_, shaFlag, _ := exp.JSON()
	if shaAll == shaFlag {
		t.Errorf("los filtros forman parte de lo que se firma")
	}
}

func TestReviewExportCSV(t *testing.T) {
	exp := NewReviewExport(exportFixture(), exportBy, false)
	body, sha, err := exp.CSV()
	if err != nil {
		t.Fatal(err)
	}
	if sha != sha256Hex(body) {
		t.Fatalf("la huella del CSV es la de sus bytes")
	}
	if !bytes.HasPrefix(body, []byte("\xEF\xBB\xBF")) || !bytes.Contains(body, []byte("\r\n")) || bytes.Contains(bytes.ReplaceAll(body, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatalf("hace falta BOM y saltos CRLF para abrirlo en Excel")
	}
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(body, []byte("\xEF\xBB\xBF"))))
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(rows[0], ",") != strings.Join(ReviewCSVHeader, ",") {
		t.Fatalf("títulos: %v", rows[0])
	}
	for i, row := range rows {
		if len(row) != len(ReviewCSVHeader) {
			t.Fatalf("fila %d con %d columnas, quería %d", i, len(row), len(ReviewCSVHeader))
		}
	}
	col := map[string]int{}
	for i, h := range ReviewCSVHeader {
		col[h] = i
	}
	counts := map[string]int{}
	for _, row := range rows[1:] {
		counts[row[col["tipo"]]]++
		if row[col["generado_en"]] != "2026-10-13T15:30:00Z" || row[col["generado_por"]] != "Luis Auditor" || row[col["umbral_inactividad_dias"]] != "90" || row[col["solo_con_marcas"]] != "no" {
			t.Errorf("fila sin los datos de la exportación: %v", row)
		}
	}
	// 3 roles de plataforma + 1 de espacio, 1 permiso de Engram, 2 tokens.
	if counts[CSVRowRole] != 4 || counts[CSVRowGrant] != 1 || counts[CSVRowToken] != 2 {
		t.Errorf("filas por tipo: %v", counts)
	}
	// Inyección de fórmulas: =, +, - y @ llevan una comilla delante y el
	// resto del texto queda igual.
	text := string(body)
	for _, want := range []string{"'=cmd", `"'+Pedro ""El"" Admin, Jr"`, "'@pedro", "'-malo"} {
		if !strings.Contains(text, want) {
			t.Errorf("falta la celda protegida %q", want)
		}
	}
	for i, row := range rows[1:] {
		for j, cell := range row {
			if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
				t.Errorf("fila %d columna %s: la celda %q puede ser una fórmula", i+1, ReviewCSVHeader[j], cell)
			}
		}
	}
	if CSVSafe("") != "" || CSVSafe("normal") != "normal" || CSVSafe("\tx") != "'\tx" || CSVSafe("2026-10-13") != "2026-10-13" {
		t.Errorf("CSVSafe")
	}
}

func TestReviewExportFilename(t *testing.T) {
	got := ReviewExportFilename(time.Date(2026, 10, 13, 15, 30, 5, 0, time.UTC), strings.Repeat("a", 64), "csv")
	want := "nexo-revision-accesos-20261013T153005Z-" + strings.Repeat("a", 64) + ".csv"
	if got != want {
		t.Errorf("nombre = %q", got)
	}
}
