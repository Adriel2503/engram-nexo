package evidence

// SG-07 — exportación de la revisión de accesos (ISO/IEC 27001:2022 A.5.18,
// derechos de acceso). El reporte de BuildAccessReview se baja como JSON o
// CSV con quién lo generó, cuándo, con qué filtros y una huella SHA-256 para
// comprobar después que el archivo no cambió.
//
// JSON: un documento con las claves ordenadas. Su campo content_sha256 es el
// SHA-256 del mismo documento sin ese campo, en forma canónica: claves
// ordenadas en todos los niveles, sin espacios, UTF-8 sin escapar < > &,
// sin salto de línea final y con los caracteres de control cambiados por un
// espacio. Se comprueba con jq o con cualquier lenguaje (ver
// VerifyReviewExportJSON y docs/nexo/manuales/administracion.md §9.5).
//
// CSV: UTF-8 con BOM, comas, saltos CRLF y una fila de títulos. La huella es el
// SHA-256 de los bytes del archivo tal cual se entrega. Una celda de texto que
// empieza con = + - @ tabulador o retorno lleva una comilla simple delante
// (inyección de fórmulas, OWASP).
//
// Nunca llevan valores ni hashes de token: solo el id, el nombre, el alcance
// y las fechas que ya muestra el reporte.

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Datos fijos del documento exportado.
const (
	ReviewExportReport  = "access_review"
	ReviewExportControl = "ISO/IEC 27001:2022 A.5.18"
	// ReviewExportVersion sube si cambia el formato (columnas o campos).
	ReviewExportVersion = 1
	// ReviewExportHashField es el campo del JSON con la huella.
	ReviewExportHashField = "content_sha256"
)

// ReviewExportActor es quien generó la exportación.
type ReviewExportActor struct {
	PrincipalID string `json:"principal_id"`
	Name        string `json:"name"`
}

// ReviewExportFilters son los filtros con los que se generó.
type ReviewExportFilters struct {
	InactiveDays int  `json:"inactive_days"`
	OnlyFlagged  bool `json:"only_flagged"`
}

// ReviewExportPolicy es la política del servidor al generar el reporte.
type ReviewExportPolicy struct {
	DisableInactiveDays int `json:"disable_inactive_days"`
	ServiceAccountStale int `json:"service_account_stale_days"`
}

// ReviewExport es el contenido de la exportación. ContentSHA256 solo lo
// llena JSON(); no forma parte de lo que se firma.
type ReviewExport struct {
	Report        string              `json:"report"`
	Control       string              `json:"control"`
	FormatVersion int                 `json:"format_version"`
	GeneratedAt   time.Time           `json:"generated_at"`
	GeneratedBy   ReviewExportActor   `json:"generated_by"`
	Filters       ReviewExportFilters `json:"filters"`
	Policy        ReviewExportPolicy  `json:"policy"`
	// ReviewDigest es la misma huella (ReportDigest) que guarda "Marcar
	// revisión como hecha" en report_sha256: permite enlazar el archivo con
	// el registro de la revisión si los accesos no cambiaron entre ambos.
	ReviewDigest  string         `json:"review_digest"`
	Summary       ReviewSummary  `json:"summary"`
	People        []ReviewPerson `json:"people"`
	ContentSHA256 string         `json:"content_sha256,omitempty"`
}

// NewReviewExport arma la exportación a partir del reporte. onlyFlagged deja
// solo a las personas con marcas (el resumen sigue siendo el del reporte
// completo). No modifica rep.
func NewReviewExport(rep AccessReview, by ReviewExportActor, onlyFlagged bool) ReviewExport {
	people := make([]ReviewPerson, 0, len(rep.People))
	for _, p := range rep.People {
		if onlyFlagged && len(p.Flags) == 0 {
			continue
		}
		people = append(people, utcPerson(p))
	}
	return ReviewExport{
		Report: ReviewExportReport, Control: ReviewExportControl, FormatVersion: ReviewExportVersion,
		GeneratedAt:  rep.GeneratedAt.UTC().Truncate(time.Second),
		GeneratedBy:  by,
		Filters:      ReviewExportFilters{InactiveDays: rep.InactiveDays, OnlyFlagged: onlyFlagged},
		Policy:       ReviewExportPolicy{DisableInactiveDays: rep.DisableInactive, ServiceAccountStale: rep.SAStaleDays},
		ReviewDigest: ReportDigest(rep),
		Summary:      rep.Summary,
		People:       people,
	}
}

func utcTime(t time.Time) time.Time { return t.UTC() }

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}

func utcBindings(in []ReviewBinding) []ReviewBinding {
	out := make([]ReviewBinding, len(in))
	for i, b := range in {
		b.Since = utcTime(b.Since)
		out[i] = b
	}
	return out
}

// utcPerson copia a la persona con todas las fechas en UTC.
func utcPerson(p ReviewPerson) ReviewPerson {
	p.CreatedAt = utcTime(p.CreatedAt)
	p.PlatformSince = utcPtr(p.PlatformSince)
	p.LastActivityAt = utcPtr(p.LastActivityAt)
	p.Scopes = utcBindings(p.Scopes)
	p.ProjectGrants = utcBindings(p.ProjectGrants)
	tokens := make([]ReviewToken, len(p.Tokens))
	for i, t := range p.Tokens {
		t.CreatedAt = utcTime(t.CreatedAt)
		t.ExpiresAt = utcPtr(t.ExpiresAt)
		t.LastUsedAt = utcPtr(t.LastUsedAt)
		tokens[i] = t
	}
	p.Tokens = tokens
	p.Flags = append([]string{}, p.Flags...)
	return p
}

// ─── JSON ────────────────────────────────────────────────────────────────────

// reviewTree convierte v en un árbol de mapas (claves ordenadas al escribir)
// con los caracteres de control de los textos cambiados por un espacio.
func reviewTree(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree map[string]any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	return sanitizeTree(tree).(map[string]any), nil
}

func sanitizeTree(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = sanitizeTree(x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = sanitizeTree(x)
		}
		return t
	case string:
		return strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || r == ' ' || r == ' ' {
				return ' '
			}
			return r
		}, t)
	}
	return v
}

// canonicalJSON escribe el árbol en forma canónica (ver el encabezado).
func canonicalJSON(tree map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tree); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// JSON devuelve el documento (legible, con sangría y las claves ordenadas) y
// la huella de su contenido, que también va dentro en content_sha256.
func (e ReviewExport) JSON() (body []byte, sha string, err error) {
	e.ContentSHA256 = ""
	tree, err := reviewTree(e)
	if err != nil {
		return nil, "", fmt.Errorf("evidence: exportación JSON: %w", err)
	}
	canon, err := canonicalJSON(tree)
	if err != nil {
		return nil, "", fmt.Errorf("evidence: exportación JSON: %w", err)
	}
	sha = sha256Hex(canon)
	tree[ReviewExportHashField] = sha
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(tree); err != nil {
		return nil, "", fmt.Errorf("evidence: exportación JSON: %w", err)
	}
	return buf.Bytes(), sha, nil
}

// ReviewExportVerdict es el resultado de comprobar un JSON exportado.
type ReviewExportVerdict struct {
	OK       bool   `json:"ok"`
	Declared string `json:"declared"`
	Computed string `json:"computed"`
}

// VerifyReviewExportJSON recalcula la huella de un JSON exportado (sin el
// campo content_sha256) y la compara con la que declara.
func VerifyReviewExportJSON(raw []byte) (ReviewExportVerdict, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree map[string]any
	if err := dec.Decode(&tree); err != nil {
		return ReviewExportVerdict{}, fmt.Errorf("evidence: JSON ilegible: %w", err)
	}
	declared, ok := tree[ReviewExportHashField].(string)
	if !ok || declared == "" {
		return ReviewExportVerdict{}, errors.New("evidence: el archivo no trae " + ReviewExportHashField)
	}
	delete(tree, ReviewExportHashField)
	canon, err := canonicalJSON(tree)
	if err != nil {
		return ReviewExportVerdict{}, err
	}
	computed := sha256Hex(canon)
	return ReviewExportVerdict{OK: strings.EqualFold(declared, computed), Declared: declared, Computed: computed}, nil
}

// ─── CSV ─────────────────────────────────────────────────────────────────────

// Tipos de fila del CSV (primera columna).
const (
	CSVRowRole    = "rol"            // rol de plataforma, de proyecto o de espacio
	CSVRowGrant   = "permiso_engram" // permiso de proyecto de Engram
	CSVRowToken   = "token"
	csvBOM        = "\xEF\xBB\xBF"
	csvTimeFormat = time.RFC3339
)

// ReviewCSVHeader son los títulos del CSV, en orden.
var ReviewCSVHeader = []string{
	"tipo", "persona_id", "usuario", "nombre", "correo", "clase", "habilitada", "rol_engram", "rol_plataforma",
	"ultima_actividad", "dias_sin_uso", "marcas",
	"ambito_tipo", "ambito", "rol", "desde", "otorgado_por",
	"token_id", "token_nombre", "token_alcance", "token_creado", "token_creado_por", "token_vence", "token_sin_vencimiento", "token_vencido", "token_ultimo_uso",
	"generado_en", "generado_por", "umbral_inactividad_dias", "solo_con_marcas",
}

// CSVSafe antepone una comilla simple a un texto que una hoja de cálculo
// podría tomar por fórmula (empieza con = + - @ tabulador o retorno).
func CSVSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

func csvYesNo(b bool) string {
	if b {
		return "si"
	}
	return "no"
}

func csvTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(csvTimeFormat)
}

// CSV devuelve el archivo y su huella (SHA-256 de esos mismos bytes). Una fila
// por rol (el de plataforma siempre, aunque sea "miembro"), una por permiso de
// proyecto de Engram y una por token; cada fila repite los datos de la persona
// y de la exportación para poder filtrar y ordenar sin perder el contexto.
func (e ReviewExport) CSV() (body []byte, sha string, err error) {
	var buf bytes.Buffer
	buf.WriteString(csvBOM)
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	if err := w.Write(ReviewCSVHeader); err != nil {
		return nil, "", fmt.Errorf("evidence: exportación CSV: %w", err)
	}
	generated := e.GeneratedAt.UTC().Format(csvTimeFormat)
	by := e.GeneratedBy.Name
	threshold := strconv.Itoa(e.Filters.InactiveDays)
	only := csvYesNo(e.Filters.OnlyFlagged)
	for _, p := range e.People {
		days := p.InactiveDays
		if days < 0 || p.LastActivityAt == nil {
			days = 0
		}
		person := []string{p.PrincipalID, p.Username, p.DisplayName, p.Email, p.Kind, csvYesNo(p.Enabled), p.EngramRole, p.PlatformRole,
			csvTime(p.LastActivityAt), strconv.Itoa(days), strings.Join(p.Flags, " ")}
		row := func(kind string, scope, token []string) error {
			cells := make([]string, 0, len(ReviewCSVHeader))
			cells = append(cells, kind)
			cells = append(cells, person...)
			cells = append(cells, scope...)
			cells = append(cells, token...)
			cells = append(cells, generated, by, threshold, only)
			for i := range cells {
				cells[i] = CSVSafe(cells[i])
			}
			return w.Write(cells)
		}
		noScope := []string{"", "", "", "", ""}
		noToken := []string{"", "", "", "", "", "", "", "", ""}
		// Rol de plataforma: una fila siempre.
		platform := []string{"plataforma", "*", p.PlatformRole, csvTime(p.PlatformSince), p.PlatformBy}
		if err := row(CSVRowRole, platform, noToken); err != nil {
			return nil, "", fmt.Errorf("evidence: exportación CSV: %w", err)
		}
		for _, b := range p.Scopes {
			if err := row(CSVRowRole, bindingCells(b), noToken); err != nil {
				return nil, "", fmt.Errorf("evidence: exportación CSV: %w", err)
			}
		}
		for _, b := range p.ProjectGrants {
			if err := row(CSVRowGrant, bindingCells(b), noToken); err != nil {
				return nil, "", fmt.Errorf("evidence: exportación CSV: %w", err)
			}
		}
		for _, t := range p.Tokens {
			tok := []string{t.ID, t.Name, t.Scope, t.CreatedAt.UTC().Format(csvTimeFormat), t.CreatedBy, csvTime(t.ExpiresAt),
				csvYesNo(t.NoExpiry), csvYesNo(t.Expired), csvTime(t.LastUsedAt)}
			if err := row(CSVRowToken, noScope, tok); err != nil {
				return nil, "", fmt.Errorf("evidence: exportación CSV: %w", err)
			}
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, "", fmt.Errorf("evidence: exportación CSV: %w", err)
	}
	return buf.Bytes(), sha256Hex(buf.Bytes()), nil
}

func bindingCells(b ReviewBinding) []string {
	return []string{b.ScopeType, b.ScopeKey, b.Role, b.Since.UTC().Format(csvTimeFormat), b.GrantedBy}
}

// ReviewExportFilename es el nombre del archivo: lleva la hora de generación
// y la huella completa para poder comprobarlo.
func ReviewExportFilename(generatedAt time.Time, sha, ext string) string {
	return "nexo-revision-accesos-" + generatedAt.UTC().Format("20060102T150405Z") + "-" + sha + "." + ext
}
