// Package domain define los tipos y errores del módulo de espacios de
// conocimiento de Nexo IA (NX-10). No depende de ningún otro paquete del
// módulo ni de Engram (docs/nexo/conocimiento/design.md §3).
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Categorías por defecto de un espacio (RF-10). Un espacio puede ampliarlas.
var DefaultCategories = []string{"estandar", "convencion", "lineamiento", "glosario", "regla_negocio", "catalogo"}

// Estados de un documento.
const (
	DocActive  = "active"
	DocRetired = "retired"
)

// Formatos de origen. md y txt en la etapa K1; los demás llegan con K2/K3.
const (
	FormatMarkdown = "md"
	FormatText     = "txt"
)

// Tipos de extracción de un bloque o sección (procedencia).
const (
	ExtractionText  = "text"
	ExtractionTable = "table"
	ExtractionCode  = "code"
	ExtractionMixed = "mixed"
	ExtractionOCR   = "ocr" // reservado: un PDF escaneado se rechaza en K1-K3 (RF-18)
)

// Clases de origen (RF-15).
const (
	OriginGit      = "git"
	OriginUpload   = "upload"
	OriginProposal = "proposal"
	OriginImport   = "import"
	OriginManual   = "manual"
)

// Acciones de nexo_kb_events (design §5.7).
const (
	ActionSpaceCreated       = "space.created"
	ActionSpaceUpdated       = "space.updated"
	ActionSpaceArchived      = "space.archived"
	ActionSpaceRestored      = "space.restored"
	ActionDocumentPublished  = "document.published"
	ActionDocumentRetired    = "document.retired"
	ActionDocumentSuperseded = "document.superseded"
	ActionAuthzDenied        = "authz.denied"
	// Entrega 2b.
	ActionProposalCreated   = "proposal.created"
	ActionProposalApproved  = "proposal.approved"
	ActionProposalDiscarded = "proposal.discarded"
	ActionProposalWithdrawn = "proposal.withdrawn"
	ActionPublishRun        = "publish.run" // resumen de un lote o una subida (KP-02)
	ActionEvalRun           = "eval.run"    // corrida de evaluación registrada (RC-28)
)

// Estados de una propuesta (C-07).
const (
	ProposalOpen      = "open"
	ProposalApproved  = "approved"
	ProposalDiscarded = "discarded"
	ProposalWithdrawn = "withdrawn"
)

// Clases de cambio de nexo_kb_changes (feed de la copia local, 2b).
const (
	ChangeUpsert        = "upsert"
	ChangeRemove        = "remove"
	ChangeSpaceArchived = "space_archived"
	ChangeSpaceRestored = "space_restored"
)

// Resultados por documento de una publicación por lotes (RF-12).
const (
	ResultCreated   = "created"
	ResultUpdated   = "updated"
	ResultUnchanged = "unchanged"
	ResultRejected  = "rejected"
	ResultRetired   = "retired" // poda
	ResultSkipped   = "skipped" // 2b: página vacía o archivo no indexable (aviso)
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// ValidSlug informa si s es un slug de espacio válido (RF-01).
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

var docKeyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

// ValidDocKey: identidad estable del documento (ruta relativa sin extensión o
// id del front matter). Sin "..", sin barras dobles ni al final.
func ValidDocKey(k string) bool {
	if !docKeyRe.MatchString(k) {
		return false
	}
	return !strings.Contains(k, "..") && !strings.Contains(k, "//") && !strings.HasSuffix(k, "/")
}

var categoryRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// ValidCategory valida el formato de una categoría.
func ValidCategory(c string) bool { return categoryRe.MatchString(c) }

// Space es un espacio de conocimiento (nexo_kb_spaces + nexo_scopes).
type Space struct {
	ID          string         `json:"id"`
	Slug        string         `json:"slug"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Categories  []string       `json:"categories"`
	Settings    map[string]any `json:"settings"`
	Visibility  string         `json:"visibility"`
	DefaultRole string         `json:"default_role"`
	Status      string         `json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	// Rol y capacidades efectivos de quien consulta (RF-05).
	Role         string   `json:"role,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// Origin es el origen de una versión (RF-15). Para git, Ref es el repositorio.
type Origin struct {
	Kind     string `json:"kind"`
	Repo     string `json:"repo,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Path     string `json:"path,omitempty"`
	Author   string `json:"author,omitempty"`
	Approver string `json:"approver,omitempty"`
	CIRun    string `json:"ci_run,omitempty"`
	Filename string `json:"filename,omitempty"`
	// ProposalID: origen "propuesta" (2b, C-07).
	ProposalID string `json:"proposal_id,omitempty"`
	// NX-10 2b. Source: nombre de la colección subida (zip o carpeta) que la
	// poda usa como referencia de los archivos subidos; PathTitles: ruta de
	// títulos de la carpeta (RC-03); SourceURL: enlace a la fuente (RC-08);
	// Order: posición según .order de la wiki.
	Source     string   `json:"source,omitempty"`
	PathTitles []string `json:"path_titles,omitempty"`
	SourceURL  string   `json:"source_url,omitempty"`
	Order      int      `json:"order,omitempty"`
}

// Ref devuelve la referencia usada por la poda: el repositorio para git; para
// upload, la colección subida (Source) o, si no viene, el nombre de archivo.
func (o Origin) Ref() string {
	switch o.Kind {
	case OriginGit:
		return o.Repo
	case OriginUpload:
		if o.Source != "" {
			return o.Source
		}
		return o.Filename
	}
	return ""
}

// Document es un documento con los datos de su versión actual.
type Document struct {
	ID             string     `json:"id"`
	SpaceID        string     `json:"space_id"`
	Space          string     `json:"space"`
	DocKey         string     `json:"doc_key"`
	Title          string     `json:"title"`
	Category       string     `json:"category"`
	Tags           []string   `json:"tags"`
	OwnerLabel     string     `json:"owner"`
	OwnerPrincipal string     `json:"owner_principal_id,omitempty"`
	Status         string     `json:"status"`
	ValidFrom      time.Time  `json:"valid_from"`
	ValidUntil     *time.Time `json:"valid_until,omitempty"`
	CurrentVersion int        `json:"current_version"`
	SupersededBy   string     `json:"superseded_by,omitempty"` // doc_key del reemplazo (HU-08)
	OriginKind     string     `json:"origin_kind,omitempty"`
	OriginRef      string     `json:"origin_ref,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	RetiredAt      *time.Time `json:"retired_at,omitempty"`
	RetirementNote string     `json:"retirement_note,omitempty"`
}

// Version es una versión inmutable (nexo_kb_versions).
type Version struct {
	ID              string     `json:"id"`
	DocumentID      string     `json:"document_id"`
	Version         int        `json:"version"`
	Title           string     `json:"title"`
	Category        string     `json:"category"`
	Tags            []string   `json:"tags"`
	OwnerLabel      string     `json:"owner"`
	ContentMD       string     `json:"content_md,omitempty"`
	ContentSHA256   string     `json:"content_sha256"`
	SourceFormat    string     `json:"source_format"`
	SourceMIME      string     `json:"source_mime,omitempty"`
	SourceSHA256    string     `json:"source_sha256,omitempty"`
	SourceSizeBytes int64      `json:"source_size_bytes"`
	SourceBlobRef   string     `json:"source_blob_ref,omitempty"`
	Origin          Origin     `json:"origin"`
	ChangeNote      string     `json:"change_note,omitempty"`
	ValidFrom       time.Time  `json:"valid_from"`
	ValidUntil      *time.Time `json:"valid_until,omitempty"`
	PublishedBy     string     `json:"published_by"`
	PublishedByID   string     `json:"published_by_principal_id,omitempty"`
	PublishedByTok  string     `json:"published_by_token_id,omitempty"`
	PublishedEmail  string     `json:"published_by_email,omitempty"`
	PublishedAt     time.Time  `json:"published_at"`
	SectionCount    int        `json:"section_count"`
}

// SourceLocation es la procedencia de un bloque o sección dentro del archivo
// original, para citar. Las claves dependen del formato:
//
//	md/txt: heading_path, line_start, line_end
//	pdf:    page_start, page_end            (K3)
//	xlsx:   sheet, row_start, row_end       (K2)
//	pptx:   slide_start, slide_end          (K3)
type SourceLocation map[string]any

// Section es una sección indexable y citable de una versión.
type Section struct {
	ID             string         `json:"id"`
	Ordinal        int            `json:"ordinal"`
	Anchor         string         `json:"anchor"`
	HeadingPath    []string       `json:"heading_path"`
	Body           string         `json:"body"`
	TokenCount     int            `json:"token_count"`
	SourceLocation SourceLocation `json:"source_location"`
	Extraction     string         `json:"extraction"`
	ValidFrom      time.Time      `json:"valid_from"`
	ValidUntil     *time.Time     `json:"valid_until,omitempty"`
	RecordedFrom   time.Time      `json:"recorded_from"`
	RecordedUntil  *time.Time     `json:"recorded_until,omitempty"`
	IsCurrent      bool           `json:"is_current"`
}

// Event es una fila de nexo_kb_events.
type Event struct {
	ID          int64          `json:"id"`
	OccurredAt  time.Time      `json:"occurred_at"`
	Space       string         `json:"space"`
	Action      string         `json:"action"`
	TargetKind  string         `json:"target_kind,omitempty"`
	TargetID    string         `json:"target_id,omitempty"`
	ActorID     string         `json:"actor_principal_id,omitempty"`
	ActorToken  string         `json:"actor_token_id,omitempty"`
	ActorLabel  string         `json:"actor"`
	ActorEmail  string         `json:"actor_email,omitempty"`
	Verified    bool           `json:"actor_verified"`
	ClientIP    string         `json:"client_ip,omitempty"`
	UserAgent   string         `json:"user_agent,omitempty"`
	ClientClaim string         `json:"client_claim,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

// ─── Errores de dominio ──────────────────────────────────────────────────────

// Error es un error de dominio con código estable (la API lo devuelve en
// error_code) y la clase que decide el estado HTTP.
type Error struct {
	Kind    Kind
	Code    string
	Message string
}

// Kind clasifica un error de dominio.
type Kind int

const (
	KindInvalid   Kind = iota + 1 // 422
	KindNotFound                  // 404 (también ámbitos ocultos)
	KindForbidden                 // 403
	KindConflict                  // 409
	KindTooLarge                  // 413
)

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf crea un error de dominio.
func Errorf(kind Kind, code, format string, args ...any) *Error {
	return &Error{Kind: kind, Code: code, Message: fmt.Sprintf(format, args...)}
}

// Códigos de error.
const (
	CodeInvalid       = "invalid_request"
	CodeNotFound      = "not_found"
	CodeForbidden     = "forbidden"
	CodeConflict      = "conflict"
	CodeSpaceExists   = "space_exists"
	CodeArchived      = "space_archived"
	CodeTooLarge      = "too_large"
	CodeSecret        = "secret_detected"
	CodeUnsupported   = "unsupported_format"
	CodePartialBatch  = "partial_batch"
	CodeCategory      = "invalid_category"
	CodeVersion       = "version_not_found"
	CodeAnchor        = "anchor_not_found"
	CodeInvalidCursor = "invalid_cursor"
	// Entrega 2b.
	CodeVersionConflict = "version_conflict" // la propuesta vio otra versión (409)
	CodeProposalState   = "proposal_state"   // la propuesta ya no está abierta (409)
	CodePruneCap        = "prune_cap"        // la poda superaría el tope (KP-01)
)

// IsKind informa si err es un error de dominio de la clase k.
func IsKind(err error, k Kind) bool {
	var de *Error
	return errors.As(err, &de) && de.Kind == k
}
