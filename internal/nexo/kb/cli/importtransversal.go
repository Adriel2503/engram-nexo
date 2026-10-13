package cli

// engram nexo kb import-transversal (D-03, design §11): migra la memoria
// transversal de la fase 1 (proyecto de Engram nexo-transversal) a un
// espacio de conocimiento.
//
//   - Recuerdos con **Estado**: Oficial -> documentos publicados por lote
//     (origen import, doc_key transversal/<tema>). Idempotente por hash: la
//     segunda corrida responde "sin cambios".
//   - Recuerdos con Estado Propuesta (o sin Estado, que el protocolo trata
//     como Propuesta) -> propuestas del espacio, una por recuerdo. Idempotente:
//     no se crea si ya existe una propuesta propia con la misma clave.
//   - Obsoleto, Descartado u otros -> se omiten (se listan).
//
// La base de Engram se abre en SOLO LECTURA (mode=ro): este comando nunca
// escribe en ella ni cambia su esquema. Con --from se usa en su lugar un
// archivo de `engram export --project nexo-transversal`.

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

// TransversalProject es el proyecto de la memoria transversal de la fase 1.
const TransversalProject = "nexo-transversal"

// Memory es un recuerdo de Engram (solo los campos que usa la migración).
type Memory struct {
	ID        int64   `json:"id"`
	Title     string  `json:"title"`
	Content   string  `json:"content"`
	Type      string  `json:"type"`
	Project   *string `json:"project"`
	TopicKey  *string `json:"topic_key"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	DeletedAt *string `json:"deleted_at"`
}

// ReadMemoriesDB lee los recuerdos vivos de un proyecto desde engram.db en
// modo solo lectura.
func ReadMemoriesDB(dataDir, project string) ([]Memory, error) {
	path := filepath.Join(dataDir, "engram.db")
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("no se encontró la base de Engram en %s (use --from <export.json>): %w", path, err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, title, content, type, COALESCE(topic_key, ''), created_at, updated_at
		FROM observations WHERE project = ? AND deleted_at IS NULL ORDER BY id`, project)
	if err != nil {
		return nil, fmt.Errorf("leer recuerdos de %s: %w", project, err)
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		var tk string
		if err := rows.Scan(&m.ID, &m.Title, &m.Content, &m.Type, &tk, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		if tk != "" {
			m.TopicKey = &tk
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReadMemoriesExport lee los recuerdos vivos de un proyecto desde un archivo
// de `engram export`.
func ReadMemoriesExport(path, project string) ([]Memory, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var data struct {
		Observations []Memory `json:"observations"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("%s no es un export de Engram: %w", path, err)
	}
	var out []Memory
	for _, m := range data.Observations {
		if m.DeletedAt != nil && *m.DeletedAt != "" {
			continue
		}
		if m.Project != nil && *m.Project != "" && *m.Project != project {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

var fieldRe = regexp.MustCompile(`(?m)^\s*\*\*([^*]+)\*\*\s*:\s*(.*)$`)

// memoryFields lee los campos "**Campo**: valor" de la plantilla del
// protocolo (fase 1). Las claves se pliegan (sin tildes, minúsculas).
func memoryFields(content string) map[string]string {
	out := map[string]string{}
	for _, m := range fieldRe.FindAllStringSubmatch(content, -1) {
		k := strings.ToLower(ingest.Fold(strings.TrimSpace(m[1])))
		if _, dup := out[k]; !dup {
			out[k] = strings.TrimSpace(m[2])
		}
	}
	return out
}

var categoryMap = map[string]string{
	"estandar": "estandar", "convencion": "convencion", "lineamiento": "lineamiento", "glosario": "glosario",
	"regla de negocio": "regla_negocio", "regla_negocio": "regla_negocio", "catalogo": "catalogo",
}

// ImportItem es el plan para un recuerdo.
type ImportItem struct {
	MemoryID int64  `json:"memory_id"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Action   string `json:"action"` // document | proposal | skip
	DocKey   string `json:"doc_key,omitempty"`
	Category string `json:"category,omitempty"`
	Result   string `json:"result,omitempty"`
	Detail   string `json:"detail,omitempty"`

	content   string
	rationale string
	owner     string
	validFrom *time.Time
}

// PlanImport clasifica los recuerdos (sin red).
func PlanImport(mems []Memory) []ImportItem {
	var out []ImportItem
	keys := map[string]int{}
	for _, m := range mems {
		f := memoryFields(m.Content)
		state := strings.TrimSpace(f["estado"])
		it := ImportItem{MemoryID: m.ID, Title: strings.TrimSpace(m.Title), State: state}
		switch strings.ToLower(ingest.Fold(state)) {
		case "oficial":
			it.Action = "document"
		case "propuesta", "":
			it.Action = "proposal"
			if state == "" {
				it.State = "(sin Estado: se trata como Propuesta)"
			}
		default:
			it.Action, it.Result, it.Detail = "skip", "omitido", "estado "+state+": no se migra"
			out = append(out, it)
			continue
		}
		cat := categoryMap[strings.ToLower(ingest.Fold(f["categoria"]))]
		if cat == "" {
			cat = "lineamiento"
		}
		it.Category = cat
		topic := ""
		if m.TopicKey != nil {
			topic = strings.TrimPrefix(strings.TrimSpace(*m.TopicKey), "transversal/")
		}
		key := "transversal/" + slugPath(firstNonEmpty(topic, it.Title))
		if n := keys[key]; n > 0 {
			key = fmt.Sprintf("%s-%d", key, m.ID)
		}
		keys[key]++
		it.DocKey = key
		if owner := f["responsable"]; owner != "" && !strings.EqualFold(owner, "<por asignar>") && !strings.EqualFold(owner, "por asignar") {
			it.owner = owner
		}
		if t, err := time.Parse("2006-01-02", strings.TrimSpace(f["vigente desde"])); err == nil {
			it.validFrom = &t
		}
		it.rationale = firstNonEmpty(f["por que"], "Importada de la memoria "+TransversalProject+" (recuerdo #"+fmt.Sprint(m.ID)+").")
		it.content = memoryMarkdown(it.Title, f, m)
		out = append(out, it)
	}
	return out
}

func slugPath(s string) string {
	var parts []string
	for _, p := range strings.Split(s, "/") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, ingest.Slugify(p))
		}
	}
	if len(parts) == 0 {
		return "sin-titulo"
	}
	return strings.Join(parts, "/")
}

// memoryMarkdown arma el documento a partir de la plantilla de la fase 1.
func memoryMarkdown(title string, f map[string]string, m Memory) string {
	var b strings.Builder
	b.WriteString("# " + title + "\n\n")
	que := f["que"]
	if que == "" {
		// Sin plantilla: el contenido tal cual, sin las líneas de campos.
		que = strings.TrimSpace(fieldRe.ReplaceAllString(m.Content, ""))
	}
	b.WriteString(que + "\n")
	for _, s := range []struct{ key, head string }{{"por que", "Por qué"}, {"alcance", "Alcance"}, {"fuente", "Fuente"}, {"reemplaza a", "Reemplaza a"}, {"origen", "Origen"}} {
		if v := strings.TrimSpace(f[s.key]); v != "" {
			b.WriteString("\n## " + s.head + "\n\n" + v + "\n")
		}
	}
	b.WriteString(fmt.Sprintf("\n## Procedencia\n\nImportado de la memoria %s de Engram (recuerdo #%d, actualizado %s).\n", TransversalProject, m.ID, m.UpdatedAt))
	return b.String()
}

func runImportTransversal(args []string, dataDir string, stdout io.Writer) error {
	fsx := flag.NewFlagSet("import-transversal", flag.ContinueOnError)
	space := fsx.String("space", "general", "espacio destino")
	dryRun := fsx.Bool("dry-run", false, "mostrar el plan sin escribir")
	from := fsx.String("from", "", "archivo de engram export (por defecto: la base local de Engram, en solo lectura)")
	project := fsx.String("project", TransversalProject, "proyecto de Engram a migrar")
	server := fsx.String("server", "", "URL del servidor")
	asJSON := fsx.Bool("json", false, "salida JSON")
	if err := fsx.Parse(reorder(args, map[string]bool{"dry-run": true, "json": true})); err != nil {
		return exitError{2, err.Error()}
	}
	var mems []Memory
	var err error
	if *from != "" {
		mems, err = ReadMemoriesExport(*from, *project)
	} else {
		mems, err = ReadMemoriesDB(dataDir, *project)
	}
	if err != nil {
		return err
	}
	items := PlanImport(mems)
	c, err := newClient(dataDir, *server)
	if err != nil {
		return err
	}
	ctx := context.Background()
	slug := strings.TrimSpace(*space)

	// Documentos: un lote (idempotente por hash; dry-run del servidor).
	req := service.BatchRequest{DryRun: *dryRun}
	idx := map[string]int{}
	for i, it := range items {
		if it.Action != "document" {
			continue
		}
		idx[it.DocKey] = i
		req.Documents = append(req.Documents, service.PublishDoc{DocKey: it.DocKey, Path: it.DocKey + ".md", Content: it.content, Title: it.Title,
			Category: it.Category, Owner: it.owner, ValidFrom: it.validFrom, Tags: []string{TransversalProject},
			Origin:     domain.Origin{Kind: domain.OriginImport, Source: *project, Path: fmt.Sprintf("engram:%s#%d", *project, it.MemoryID)},
			ChangeNote: "import-transversal"})
	}
	rejected := 0
	for start := 0; start < len(req.Documents); start += 200 {
		part := service.BatchRequest{DryRun: *dryRun, Documents: req.Documents[start:min(start+200, len(req.Documents))]}
		var res service.BatchResult
		if err := c.Do(ctx, "POST", spacePath(slug)+"/documents:batch", part, &res); err != nil {
			return fmt.Errorf("publicar documentos en %s: %w", slug, err)
		}
		for _, r := range res.Results {
			if i, ok := idx[r.DocKey]; ok {
				items[i].Result, items[i].Detail = r.Status, r.Reason
				if r.Status == domain.ResultRejected {
					rejected++
				}
			}
		}
	}

	// Propuestas: idempotentes por clave entre las propias del espacio.
	existing := map[string]string{}
	var mine struct {
		Items []struct {
			ID     string `json:"id"`
			DocKey string `json:"doc_key"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := c.Do(ctx, "GET", "/nexo/kb/v1/proposals?mine=true&limit=200&space="+url.QueryEscape(slug), nil, &mine); err != nil {
		return fmt.Errorf("listar propuestas propias: %w", err)
	}
	for _, p := range mine.Items {
		existing[p.DocKey] = p.Status
	}
	for i := range items {
		it := &items[i]
		if it.Action != "proposal" {
			continue
		}
		if st, ok := existing[it.DocKey]; ok {
			it.Result, it.Detail = "sin cambios", "ya existe una propuesta propia ("+st+")"
			continue
		}
		if *dryRun {
			it.Result = "se propondría"
			continue
		}
		var p struct {
			ID string `json:"id"`
		}
		err := c.Do(ctx, "POST", spacePath(slug)+"/proposals", service.ProposalInput{DocKey: it.DocKey, Title: it.Title, Category: it.Category,
			Content: it.content, Rationale: it.rationale, ValidFrom: it.validFrom, Tags: []string{TransversalProject}}, &p)
		if err != nil {
			it.Result, it.Detail = "rechazado", err.Error()
			rejected++
			continue
		}
		it.Result, it.Detail = "propuesta creada", p.ID
		existing[it.DocKey] = domain.ProposalOpen
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].Action < items[b].Action })
	if *asJSON {
		if err := json.NewEncoder(stdout).Encode(map[string]any{"space": slug, "dry_run": *dryRun, "memories": len(mems), "items": items}); err != nil {
			return err
		}
	} else {
		mode := ""
		if *dryRun {
			mode = " (dry-run: no se escribió nada)"
		}
		fmt.Fprintf(stdout, "Migración de %s al espacio %s%s: %d recuerdos\n", *project, slug, mode, len(mems))
		count := map[string]int{}
		for _, it := range items {
			count[it.Action]++
			line := fmt.Sprintf("  %-9s %-16s #%-5d %s", it.Action, it.Result, it.MemoryID, it.Title)
			if it.DocKey != "" {
				line += "  [" + it.DocKey + "]"
			}
			if it.Detail != "" {
				line += "  (" + it.Detail + ")"
			}
			fmt.Fprintln(stdout, line)
		}
		fmt.Fprintf(stdout, "Resumen: documentos %d, propuestas %d, omitidos %d, rechazados %d\n", count["document"], count["proposal"], count["skip"], rejected)
	}
	if rejected > 0 {
		return exitError{1, fmt.Sprintf("%d elementos rechazados", rejected)}
	}
	return nil
}
