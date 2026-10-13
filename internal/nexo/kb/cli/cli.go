// Package cli implementa el comando `engram nexo kb ...` (NX-10, design §6.1
// y §14.5) contra el servidor: publicar un repositorio de lineamientos,
// buscar, listar espacios y medir la búsqueda. El cliente HTTP es mínimo; en
// la entrega 2b pasa al paquete client junto con la copia local y las
// herramientas MCP.
//
// Servidor y token: los de `engram cloud config` (cloud.json) o
// ENGRAM_CLOUD_SERVER y ENGRAM_CLOUD_TOKEN; --server reemplaza el servidor.
// El token nunca se acepta como argumento (quedaría en el historial).
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

const usage = `Uso: engram nexo kb <comando> [opciones]

Comandos:
  spaces                                  Espacios visibles y rol efectivo
  publish --space S [--prune] [--dry-run] [--repo org/repo] [--commit SHA]
          [--branch B] [--category C] [--owner O] [--base DIR] <rutas...>
                                          Publica archivos .md y .txt (directorios recursivos)
  search [--space S]... [--category C] [--tag T] [--at FECHA] [--known-at FECHA]
         [--limit N] [--mode fts|exact] [--local] [--json] <consulta>
                                          Busca en los espacios visibles (--local: solo la copia local;
                                          si el servidor no responde, también la usa, con aviso)
  get --space S [--version N] [--anchor A] [--local] [--json] <doc_key>[#ancla]
                                          Lee un documento o una sección (sin conexión: la copia local)
  sync [--status] [--json]                Actualiza la copia local de solo lectura: NEXO_KB_LOCAL_DB o,
                                          si no está, $NEXO_HOME/kb.db (por defecto ~/.nexo/kb.db)
  import-transversal --space general [--dry-run] [--from export.json] [--project nexo-transversal]
                                          Migra la memoria nexo-transversal: Oficial -> documentos,
                                          Propuesta -> propuestas (idempotente)
  upload --space S [--dry-run] [--prune [--confirm-prune]] [--source auto|dir|azure-devops-wiki]
         [--collection N] [--wiki-url URL] [--category C] [--owner O] <archivos | carpeta | zip>
                                          Sube archivos sueltos (.md, .txt, .docx), una carpeta o un zip
  changes --since FECHA [--until FECHA] [--space S]... [--clock valid|recorded]
                                          Secciones agregadas, modificadas y retiradas (RC-27)
  eval <preguntas.yaml> [--retriever pgfts|local-bm25] [--k 5] [--validated-only] [--record [--accept]] [--min-hit 0.8] [--json]
                                          Mide la búsqueda por clase (Hit@5, Recall@5, MRR, nDCG, MAP, sin respuesta)
  reindex [--status]                      Reindexa el texto (superadmin) tras cambiar NEXO_KB_R1

Servidor y token: engram cloud config / ENGRAM_CLOUD_SERVER y ENGRAM_CLOUD_TOKEN.
En CI (GitHub Actions) el origen sale de GITHUB_REPOSITORY, GITHUB_SHA,
GITHUB_REF_NAME, GITHUB_ACTOR y GITHUB_RUN_ID; las opciones lo reemplazan.
Un nexo-kb.yaml en la carpeta base fija valores por defecto (space, category,
owner, tags, repo). Al recorrer carpetas se omiten README.md, archivos y
carpetas ocultos, node_modules y eval/ (el conjunto de evaluación).
`

// Run ejecuta `engram nexo <args>` y devuelve el código de salida.
func Run(args []string, dataDir string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "kb" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	args = args[1:]
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "spaces":
		err = runSpaces(rest, dataDir, stdout)
	case "publish":
		err = runPublish(rest, dataDir, stdout)
	case "search":
		err = runSearch(rest, dataDir, stdout)
	case "upload":
		err = runUpload(rest, dataDir, stdout)
	case "changes":
		err = runChanges(rest, dataDir, stdout)
	case "reindex":
		err = runReindex(rest, dataDir, stdout)
	case "eval":
		err = runEval(rest, dataDir, stdout)
	case "get": // tanda 2 (D-03)
		err = runGet(rest, dataDir, stdout)
	case "sync": // tanda 2 (D-01)
		err = runSync(rest, dataDir, stdout)
	case "import-transversal": // tanda 2 (D-03, design §11)
		err = runImportTransversal(rest, dataDir, stdout)
	default:
		fmt.Fprintf(stderr, "comando desconocido: %s\n\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		var ee exitError
		if errors.As(err, &ee) {
			fmt.Fprintln(stderr, ee.msg)
			return ee.code
		}
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

// ─── Cliente HTTP ────────────────────────────────────────────────────────────

// Client llama a /nexo/kb/v1. Desde la entrega 2b (tanda 2) vive en el
// paquete client, junto con la copia local y las herramientas MCP.
type Client = client.API

// APIError es un error de la API.
type APIError = client.APIError

func newClient(dataDir, server string) (*Client, error) {
	return client.New(dataDir, server, 120*time.Second)
}

func spacePath(slug string) string { return "/nexo/kb/v1/spaces/" + url.PathEscape(slug) }

// ─── spaces ──────────────────────────────────────────────────────────────────

func runSpaces(args []string, dataDir string, stdout io.Writer) error {
	fsx := flag.NewFlagSet("spaces", flag.ContinueOnError)
	server := fsx.String("server", "", "URL del servidor")
	asJSON := fsx.Bool("json", false, "salida JSON")
	if err := fsx.Parse(args); err != nil {
		return exitError{2, err.Error()}
	}
	c, err := newClient(dataDir, *server)
	if err != nil {
		return err
	}
	var out struct {
		Items []domain.Space `json:"items"`
	}
	if err := c.Do(context.Background(), "GET", "/nexo/kb/v1/spaces", nil, &out); err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(out)
	}
	if len(out.Items) == 0 {
		fmt.Fprintln(stdout, "No hay espacios visibles para este usuario.")
	}
	for _, s := range out.Items {
		fmt.Fprintf(stdout, "%-30s %-12s %-10s %s\n", s.Slug, s.Role, s.Visibility, s.Name)
	}
	return nil
}

// ─── publish ─────────────────────────────────────────────────────────────────

// Defaults es nexo-kb.yaml.
type Defaults struct {
	Space    string   `yaml:"space"`
	Category string   `yaml:"category"`
	Owner    string   `yaml:"owner"`
	Tags     []string `yaml:"tags"`
	Repo     string   `yaml:"repo"`
}

// LocalDoc es un archivo leído para publicar.
type LocalDoc struct {
	Rel    string
	DocKey string
	Doc    service.PublishDoc
}

// Collect recorre las rutas: archivos con conversor registrado; los
// directorios se recorren sin entrar en carpetas ocultas ni node_modules. Rel
// es relativo a base, con barras normales.
func Collect(paths []string, base string, reg *ingest.Registry) ([]LocalDoc, error) {
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() {
				if path != p && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "eval") {
					return filepath.SkipDir
				}
				return nil
			}
			if name == "nexo-kb.yaml" || strings.HasPrefix(name, ".") || strings.EqualFold(name, "README.md") {
				return nil
			}
			if reg.Supported(name) {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	absBase, err := filepath.Abs(base)
	if err != nil {
		return nil, err
	}
	var out []LocalDoc
	seen := map[string]string{}
	for _, f := range files {
		abs, err := filepath.Abs(f)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(absBase, abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = filepath.Base(f)
		}
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		res, err := reg.Convert(context.Background(), "", ingest.Input{Name: rel, Data: data}, ingest.Limits{})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		key := res.Meta.ID
		if key == "" {
			key = service.DocKeyFromPath(rel)
		}
		if prev, dup := seen[key]; dup {
			return nil, fmt.Errorf("doc_key %q repetido en %s y %s", key, prev, rel)
		}
		seen[key] = rel
		out = append(out, LocalDoc{Rel: rel, DocKey: key, Doc: service.PublishDoc{Path: rel, Content: string(data)}})
	}
	return out, nil
}

// OriginFromEnv toma el origen de GitHub Actions si corre en CI.
func OriginFromEnv() domain.Origin {
	o := domain.Origin{
		Repo:   os.Getenv("GITHUB_REPOSITORY"),
		Commit: os.Getenv("GITHUB_SHA"),
		Branch: os.Getenv("GITHUB_REF_NAME"),
		Author: os.Getenv("GITHUB_ACTOR"),
	}
	if id := os.Getenv("GITHUB_RUN_ID"); id != "" {
		o.CIRun = id
		if server := os.Getenv("GITHUB_SERVER_URL"); server != "" && o.Repo != "" {
			o.CIRun = server + "/" + o.Repo + "/actions/runs/" + id
		}
	}
	return o
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*m = append(*m, p)
		}
	}
	return nil
}

func runPublish(args []string, dataDir string, stdout io.Writer) error {
	fsx := flag.NewFlagSet("publish", flag.ContinueOnError)
	space := fsx.String("space", "", "espacio (slug)")
	prune := fsx.Bool("prune", false, "retirar los documentos del mismo repositorio que ya no están")
	dryRun := fsx.Bool("dry-run", false, "mostrar el resultado sin escribir")
	repo := fsx.String("repo", "", "repositorio de origen (org/repo)")
	commit := fsx.String("commit", "", "commit de origen")
	branch := fsx.String("branch", "", "rama de origen")
	approver := fsx.String("approver", "", "aprobador del cambio (referencia)")
	category := fsx.String("category", "", "categoría por defecto")
	owner := fsx.String("owner", "", "responsable por defecto")
	base := fsx.String("base", "", "carpeta base para doc_key (por defecto, la carpeta indicada o la actual)")
	batchSize := fsx.Int("batch-size", 200, "documentos por petición (máximo del servidor: 200)")
	server := fsx.String("server", "", "URL del servidor")
	asJSON := fsx.Bool("json", false, "salida JSON")
	if err := fsx.Parse(reorder(args, map[string]bool{"prune": true, "dry-run": true, "json": true})); err != nil {
		return exitError{2, err.Error()}
	}
	paths := fsx.Args()
	if len(paths) == 0 {
		return exitError{2, "indique al menos una ruta (archivo o carpeta)"}
	}
	root := *base
	if root == "" {
		root = "."
		if info, err := os.Stat(paths[0]); err == nil && info.IsDir() && len(paths) == 1 {
			root = paths[0]
		}
	}
	var defs Defaults
	if raw, err := os.ReadFile(filepath.Join(root, "nexo-kb.yaml")); err == nil {
		if err := yaml.Unmarshal(raw, &defs); err != nil {
			return fmt.Errorf("nexo-kb.yaml: %w", err)
		}
	}
	slug := firstNonEmpty(*space, defs.Space)
	if slug == "" {
		return exitError{2, "falta --space (o space: en nexo-kb.yaml)"}
	}
	origin := OriginFromEnv()
	origin.Repo = firstNonEmpty(*repo, origin.Repo, defs.Repo)
	origin.Commit = firstNonEmpty(*commit, origin.Commit)
	origin.Branch = firstNonEmpty(*branch, origin.Branch)
	origin.Approver = firstNonEmpty(*approver, origin.Approver)
	if origin.Repo != "" {
		origin.Kind = domain.OriginGit
	} else {
		origin.Kind = domain.OriginUpload
	}
	if *prune && origin.Kind != domain.OriginGit {
		return exitError{2, "--prune exige un repositorio de origen (--repo, GITHUB_REPOSITORY o repo: en nexo-kb.yaml)"}
	}
	docs, err := Collect(paths, root, ingest.DefaultRegistry())
	if err != nil {
		return err
	}
	if len(docs) == 0 && !*prune {
		return exitError{2, "no hay archivos .md ni .txt en las rutas indicadas"}
	}
	for i := range docs {
		d := &docs[i].Doc
		d.Category, d.Owner, d.Tags = firstNonEmpty(*category, defs.Category), firstNonEmpty(*owner, defs.Owner), defs.Tags
		d.Origin = origin
		d.Origin.Path = docs[i].Rel
		if origin.Kind == domain.OriginUpload {
			d.Origin.Filename = docs[i].Rel
		}
	}
	c, err := newClient(dataDir, *server)
	if err != nil {
		return err
	}
	if *batchSize <= 0 || *batchSize > 200 {
		*batchSize = 200
	}
	keys := make([]string, len(docs))
	for i, d := range docs {
		keys[i] = d.DocKey
	}
	total := service.BatchResult{Space: slug, DryRun: *dryRun, Summary: map[string]int{}}
	ctx := context.Background()
	send := func(req service.BatchRequest) error {
		var res service.BatchResult
		if err := c.Do(ctx, "POST", spacePath(slug)+"/documents:batch", req, &res); err != nil {
			return err
		}
		total.Results = append(total.Results, res.Results...)
		total.Pruned = append(total.Pruned, res.Pruned...)
		for k, v := range res.Summary {
			total.Summary[k] += v
		}
		if res.PruneSkipped != "" {
			total.PruneSkipped = res.PruneSkipped
		}
		return nil
	}
	pruneSpec := func() *service.PruneSpec {
		if !*prune {
			return nil
		}
		return &service.PruneSpec{OriginKind: domain.OriginGit, OriginRef: origin.Repo, Total: len(keys), Keys: keys}
	}
	if len(docs) <= *batchSize {
		req := service.BatchRequest{DryRun: *dryRun, Prune: pruneSpec()}
		for _, d := range docs {
			req.Documents = append(req.Documents, d.Doc)
		}
		if err := send(req); err != nil {
			return err
		}
	} else {
		for start := 0; start < len(docs); start += *batchSize {
			end := min(start+*batchSize, len(docs))
			req := service.BatchRequest{DryRun: *dryRun}
			for _, d := range docs[start:end] {
				req.Documents = append(req.Documents, d.Doc)
			}
			if err := send(req); err != nil {
				return err
			}
		}
		if *prune { // lote completo: todas las claves en una petición final
			if err := send(service.BatchRequest{DryRun: *dryRun, Prune: pruneSpec()}); err != nil {
				return err
			}
		}
	}
	if *asJSON {
		if err := json.NewEncoder(stdout).Encode(total); err != nil {
			return err
		}
	} else {
		printBatch(stdout, total)
	}
	if total.Summary[domain.ResultRejected] > 0 {
		return exitError{1, fmt.Sprintf("%d documentos rechazados", total.Summary[domain.ResultRejected])}
	}
	if *prune && total.PruneSkipped != "" {
		return exitError{1, "poda omitida: " + total.PruneSkipped}
	}
	return nil
}

func printBatch(w io.Writer, r service.BatchResult) {
	mode := ""
	if r.DryRun {
		mode = " (dry-run: no se escribió nada)"
	}
	fmt.Fprintf(w, "Espacio %s%s\n", r.Space, mode)
	for _, d := range r.Results {
		line := fmt.Sprintf("  %-10s %s", d.Status, d.DocKey)
		if d.Version > 0 {
			line += fmt.Sprintf(" v%d", d.Version)
		}
		if d.Reason != "" {
			line += "  (" + d.Code + ": " + d.Reason + ")"
		}
		fmt.Fprintln(w, line)
	}
	for _, d := range r.Pruned {
		fmt.Fprintf(w, "  %-10s %s  (%s)\n", d.Status, d.DocKey, d.Reason)
	}
	if r.PruneSkipped != "" {
		fmt.Fprintf(w, "  poda omitida: %s\n", r.PruneSkipped)
	}
	fmt.Fprintf(w, "Resumen: creados %d, actualizados %d, sin cambios %d, rechazados %d, retirados %d\n",
		r.Summary[domain.ResultCreated], r.Summary[domain.ResultUpdated], r.Summary[domain.ResultUnchanged], r.Summary[domain.ResultRejected], r.Summary[domain.ResultRetired])
}

// ─── search ──────────────────────────────────────────────────────────────────

// SearchResponse es la respuesta de /search.
type SearchResponse struct {
	Results []struct {
		Space          string                `json:"space"`
		DocKey         string                `json:"doc_key"`
		Title          string                `json:"title"`
		Anchor         string                `json:"anchor"`
		HeadingPath    []string              `json:"heading_path"`
		Excerpt        string                `json:"excerpt"`
		Version        int                   `json:"version"`
		Citation       string                `json:"citation"`
		Link           string                `json:"link"`
		Match          string                `json:"match"`
		SourceLocation domain.SourceLocation `json:"source_location"`
	} `json:"results"`
	NoResults bool   `json:"no_results"`
	Message   string `json:"message"`
}

// SearchQuery arma la URL de búsqueda.
func SearchQuery(q string, spaces []string, category, tag, at, knownAt string, limit int) string {
	v := url.Values{}
	v.Set("q", q)
	if len(spaces) > 0 {
		v.Set("spaces", strings.Join(spaces, ","))
	}
	for k, val := range map[string]string{"category": category, "tag": tag, "at": at, "known_at": knownAt} {
		if val != "" {
			v.Set(k, val)
		}
	}
	if limit > 0 {
		v.Set("limit", fmt.Sprint(limit))
	}
	return "/nexo/kb/v1/search?" + v.Encode()
}

func runSearch(args []string, dataDir string, stdout io.Writer) error {
	fsx := flag.NewFlagSet("search", flag.ContinueOnError)
	var spaces multiFlag
	fsx.Var(&spaces, "space", "espacio (repetible o separado por comas)")
	category := fsx.String("category", "", "categoría")
	tag := fsx.String("tag", "", "etiqueta")
	at := fsx.String("at", "", "vigente en esta fecha (YYYY-MM-DD)")
	knownAt := fsx.String("known-at", "", "publicado a esta fecha (YYYY-MM-DD)")
	limit := fsx.Int("limit", 10, "resultados (máximo 50)")
	mode := fsx.String("mode", "", "fts (por defecto) o exact (literal o /expresión/)")
	server := fsx.String("server", "", "URL del servidor")
	asJSON := fsx.Bool("json", false, "salida JSON")
	local := fsx.Bool("local", false, "buscar con la copia local (servidor primero si no se indica; tanda 2)")
	if err := fsx.Parse(reorder(args, map[string]bool{"json": true, "local": true})); err != nil {
		return exitError{2, err.Error()}
	}
	q := strings.TrimSpace(strings.Join(fsx.Args(), " "))
	if q == "" {
		return exitError{2, "falta la consulta"}
	}
	if *local { // tanda 2: copia local (D-01)
		return runSearchLocal(q, spaces, *category, *tag, *at, *mode, *limit, *asJSON, "", stdout)
	}
	c, err := newClient(dataDir, *server)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	path := SearchQuery(q, spaces, *category, *tag, *at, *knownAt, *limit)
	if *mode != "" {
		path += "&mode=" + url.QueryEscape(*mode)
	}
	if err := c.Do(context.Background(), "GET", path, nil, &raw); err != nil {
		// Sin respuesta del servidor: la copia local, avisando (como get y
		// nexo_search). --known-at necesita el historial del servidor.
		if client.Unreachable(err) && *knownAt == "" {
			return runSearchLocal(q, spaces, *category, *tag, *at, *mode, *limit, *asJSON, "El servidor de Nexo no respondió ("+client.FriendlyError(err)+").", stdout)
		}
		return err
	}
	if *asJSON {
		_, err := stdout.Write(append(raw, '\n'))
		return err
	}
	var res SearchResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	if res.NoResults {
		fmt.Fprintln(stdout, res.Message)
		return nil
	}
	for i, h := range res.Results {
		fmt.Fprintf(stdout, "%d. %s\n", i+1, h.Citation)
		if h.Match == "some_terms" {
			fmt.Fprintln(stdout, "   (coincidencia parcial: no contiene todos los términos)")
		}
		fmt.Fprintf(stdout, "   %s\n", strings.ReplaceAll(strings.TrimSpace(h.Excerpt), "\n", " "))
		if h.Link != "" {
			fmt.Fprintf(stdout, "   %s\n", h.Link)
		}
	}
	return nil
}

// ─── utilidades ──────────────────────────────────────────────────────────────

// reorder permite opciones después de los argumentos posicionales (el paquete
// flag se detiene en el primero). bools lista las opciones sin valor.
func reorder(args []string, bools map[string]bool) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") && !bools[name] && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}
