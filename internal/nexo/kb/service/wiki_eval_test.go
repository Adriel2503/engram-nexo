package service_test

// Medición R0/R1/R2 sobre un corpus real (wiki de Azure DevOps), fuera del
// CI: se activa con variables de entorno y nunca versiona el corpus.
//
//	NEXO_KB_WIKI_DIR      carpeta de la wiki (obligatoria)
//	NEXO_KB_EVAL_SET      archivo de preguntas (obligatorio salvo con NEXO_KB_EVAL_DUMP)
//	NEXO_KB_EVAL_OVERLAY  carpeta con versiones nuevas de algunas páginas (opcional)
//	NEXO_KB_EVAL_OUT      archivo JSON del informe (opcional)
//	NEXO_KB_EVAL_DUMP     archivo con documentos y anclas indexadas (sin cuerpos), para escribir preguntas
//	NEXO_KB_EVAL_BASELINE 1 = sin la consulta de cambios (línea base anterior a RC-27)
//
// R2 (búsqueda por significado), con NEXO_KB_EMBEDDER y NEXO_KB_EMBED_*:
//
//	NEXO_KB_EMBED_CACHE   archivo de caché de vectores (por modelo, dimensión, uso y texto):
//	                      la wiki se vectoriza una sola vez entre corridas
//	NEXO_KB_EVAL_CONFIGS  configuraciones a comparar, separadas por ";":
//	                      nombre=peso_vector:peso_texto:min_similitud:min_similitud_con_texto
//	                      (siempre se mide también fts, la línea base de R1)
//
// Las opciones de R1 se toman del entorno (NEXO_KB_R1 y afines, ver r1.go).

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/embed"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/eval"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/source"
)

// readTree lee una carpeta como archivos de subida: el contenido solo de lo
// indexable y de .order (los adjuntos van sin datos, como en un zip).
func readTree(t testing.TB, dir string, indexable func(string) bool) []source.File {
	t.Helper()
	var files []source.File
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = source.CleanPath(filepath.ToSlash(rel))
		if rel == "" {
			return nil
		}
		f := source.File{Path: rel}
		if d.Name() == ".order" || (indexable(d.Name()) && !strings.HasPrefix(rel, ".attachments/")) {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			f.Data = b
		}
		files = append(files, f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// cachedEmbedder guarda los vectores en un archivo (una línea JSON por
// vector, clave = hash de proveedor, modelo, dimensión, uso y texto) para no
// volver a pagar la vectorización de la wiki en cada corrida.
type cachedEmbedder struct {
	embed.Embedder
	path   string
	mu     sync.Mutex
	vecs   map[string][]float32
	Hits   int
	Misses int
	Calls  int
}

func newCachedEmbedder(t testing.TB, e embed.Embedder, path string) *cachedEmbedder {
	c := &cachedEmbedder{Embedder: e, path: path, vecs: map[string][]float32{}}
	if path == "" {
		return c
	}
	f, err := os.Open(path)
	if err != nil {
		return c
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var row struct {
			K string    `json:"k"`
			V []float32 `json:"v"`
		}
		if json.Unmarshal(sc.Bytes(), &row) == nil && len(row.V) == e.Dims() {
			c.vecs[row.K] = row.V
		}
	}
	t.Logf("caché de vectores: %d en %s", len(c.vecs), path)
	return c
}

func (c *cachedEmbedder) key(text string, kind embed.Kind) string {
	return ingest.SHA256Hex(strings.Join([]string{c.Provider(), c.Model(), strconv.Itoa(c.Dims()), string(kind), text}, "\x00"))
}

func (c *cachedEmbedder) Embed(ctx context.Context, texts []string, kind embed.Kind) ([][]float32, error) {
	out := make([][]float32, len(texts))
	var miss []string
	var missIdx []int
	c.mu.Lock()
	for i, t := range texts {
		if v, ok := c.vecs[c.key(t, kind)]; ok {
			out[i] = v
			c.Hits++
		} else {
			miss, missIdx = append(miss, t), append(missIdx, i)
		}
	}
	c.mu.Unlock()
	if len(miss) == 0 {
		return out, nil
	}
	vecs, err := c.Embedder.Embed(ctx, miss, kind)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Misses += len(miss)
	c.Calls += (len(miss) + 99) / 100
	var f *os.File
	if c.path != "" {
		f, _ = os.OpenFile(c.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	}
	for j, i := range missIdx {
		out[i] = vecs[j]
		k := c.key(miss[j], kind)
		c.vecs[k] = vecs[j]
		if f != nil {
			raw, _ := json.Marshal(map[string]any{"k": k, "v": vecs[j]})
			_, _ = f.Write(append(raw, '\n'))
		}
	}
	if f != nil {
		_ = f.Close()
	}
	return out, nil
}

// evalConfig es una configuración del modo hybrid a medir.
type evalConfig struct {
	name                  string
	vw, tw, minSim, minTS float64
}

func parseEvalConfigs(t testing.TB, raw string) []evalConfig {
	var out []evalConfig
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, spec, ok := strings.Cut(part, "=")
		nums := strings.Split(spec, ":")
		if !ok || len(nums) != 4 {
			t.Fatalf("NEXO_KB_EVAL_CONFIGS: %q (nombre=vector:texto:min_sim:min_sim_texto)", part)
		}
		var f [4]float64
		for i, n := range nums {
			v, err := strconv.ParseFloat(n, 64)
			if err != nil {
				t.Fatalf("NEXO_KB_EVAL_CONFIGS: %q", part)
			}
			f[i] = v
		}
		out = append(out, evalConfig{name: name, vw: f[0], tw: f[1], minSim: f[2], minTS: f[3]})
	}
	return out
}

func TestEvalWikiCorpus(t *testing.T) {
	wiki := os.Getenv("NEXO_KB_WIKI_DIR")
	if wiki == "" {
		t.Skip("NEXO_KB_WIKI_DIR no definido (medición sobre corpus real, fuera del CI)")
	}
	var opts []service.Option
	var cache *cachedEmbedder
	if c, err := embed.ConfigFromEnv(); err != nil {
		t.Fatal(err)
	} else if c.Enabled() {
		e, err := embed.New(c)
		if err != nil {
			t.Fatal(err)
		}
		cache = newCachedEmbedder(t, e, os.Getenv("NEXO_KB_EMBED_CACHE"))
		r2 := service.R2FromEnv()
		r2.Worker = false
		opts = append(opts, service.WithEmbedder(cache, c), service.WithR2(r2))
	}
	env := kbtest.New(t, opts...)
	tm := env.NewTeam()
	ctx := context.Background()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "arquitectura", Name: "Arquitectura", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	reg := env.KB.Registry()
	files := readTree(t, wiki, reg.Supported)
	start := time.Now()
	res, err := env.KB.Upload(env.Ctx(), env.ID(tm.Ana), "arquitectura", service.UploadRequest{Files: files, Collection: "Wiki-Arquitectura",
		WikiURL: "https://dev.azure.com/realplaza/arquitectura/_wiki/wikis/Wiki-Arquitectura"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("carga base: fuente %s, %v en %s, avisos %d", res.Source, res.Summary, time.Since(start).Round(time.Millisecond), len(res.Skipped))
	for _, r := range res.Results {
		if r.Status == "rejected" {
			reason := ""
			if r.Code == "internal_error" {
				reason = r.Reason
			}
			t.Logf("  rechazado %s: %s %s", r.DocKey, r.Code, reason)
		}
	}
	var secs int
	_ = env.DB.QueryRow(`SELECT count(*) FROM nexo_kb_sections`).Scan(&secs)
	t.Logf("secciones: %d", secs)
	base := time.Now().UTC()
	time.Sleep(1100 * time.Millisecond)
	if ov := os.Getenv("NEXO_KB_EVAL_OVERLAY"); ov != "" {
		ofiles := readTree(t, ov, reg.Supported)
		ores, err := env.KB.Upload(env.Ctx(), env.ID(tm.Ana), "arquitectura", service.UploadRequest{Files: ofiles, Source: source.KindADO, Collection: "Wiki-Arquitectura"})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("overlay: %v", ores.Summary)
		for _, r := range ores.Results {
			t.Logf("  overlay %s %s v%d %s", r.Status, r.DocKey, r.Version, r.Reason)
		}
	}
	// R2: vectorizar todo (la cola) antes de medir.
	if env.KB.VectorEnabled() {
		vstart := time.Now()
		var total service.VectorDrain
		for attempt := 0; ; attempt++ {
			d, err := env.KB.DrainVectors(ctx)
			total.Embedded += d.Embedded
			total.Reused += d.Reused
			total.Failed += d.Failed
			if err == nil && env.Count(`SELECT count(*) FROM nexo_kb_index_jobs`) == 0 {
				if _, err := env.KB.DrainVectors(ctx); err != nil { // activa la versión
					t.Fatal(err)
				}
				break
			}
			if attempt > 40 {
				t.Fatalf("la vectorización no terminó: %v", err)
			}
			t.Logf("vectorización en pausa (%v); pendientes %d; reintento en 30 s", err, env.Count(`SELECT count(*) FROM nexo_kb_index_jobs`))
			time.Sleep(30 * time.Second)
		}
		t.Logf("vectores: %d (calculados %d, reutilizados %d, fallidos %d, cartas muertas %d) en %s; caché: %d aciertos, %d textos nuevos, ~%d llamadas al proveedor",
			env.Count(`SELECT count(*) FROM nexo_kb_section_vectors`), total.Embedded, total.Reused, total.Failed, env.Count(`SELECT count(*) FROM nexo_kb_index_dead`),
			time.Since(vstart).Round(time.Second), cache.Hits, cache.Misses, cache.Calls)
	}
	if dump := os.Getenv("NEXO_KB_EVAL_DUMP"); dump != "" {
		rows, err := env.DB.Query(`SELECT d.doc_key, d.status, d.title, v.version, s.anchor, array_to_string(s.heading_path, ' > '), s.token_count, s.extraction,
				s.is_current, s.valid_from::date::text, COALESCE(s.valid_until::date::text, '')
			FROM nexo_kb_sections s JOIN nexo_kb_documents d ON d.id = s.document_id JOIN nexo_kb_versions v ON v.id = s.version_id
			ORDER BY d.doc_key, v.version, s.ordinal`)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for rows.Next() {
			var key, st, title, anchor, hp, extr, vf, vu string
			var ver, tok int
			var cur bool
			if err := rows.Scan(&key, &st, &title, &ver, &anchor, &hp, &tok, &extr, &cur, &vf, &vu); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%s\t%s\tv%d\t%s\t%s\t%s\t%d\t%s\tcurrent=%t\t%s..%s\n", key, st, ver, title, anchor, hp, tok, extr, cur, vf, vu)
		}
		_ = rows.Close()
		if err := os.WriteFile(dump, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("anclas escritas en %s", dump)
	}
	setPath := os.Getenv("NEXO_KB_EVAL_SET")
	if setPath == "" {
		return
	}
	set, err := eval.Load(setPath)
	if err != nil {
		t.Fatal(err)
	}
	set.Bind(map[string]time.Time{eval.MarkBase: base})
	runs := []evalConfig{{name: "default"}}
	if env.KB.VectorEnabled() {
		runs = append([]evalConfig{{name: "fts"}}, parseEvalConfigs(t, os.Getenv("NEXO_KB_EVAL_CONFIGS"))...)
	}
	// NEXO_KB_EVAL_DEBUG=1: cobertura y similitud de los primeros resultados
	// de las preguntas sin respuesta y de paráfrasis, sin umbral (para
	// calibrar NEXO_KB_MIN_SIMILARITY y NEXO_KB_MIN_TEXT_SIMILARITY).
	if os.Getenv("NEXO_KB_EVAL_DEBUG") == "1" && env.KB.VectorEnabled() {
		saved := env.KB.R2()
		o := saved
		o.MinSimilarity, o.MinTextSimilarity = 0, 0
		env.KB.ApplyR2(o)
		r1 := env.KB.R1()
		r1off := r1
		r1off.MinCoverage = 0 // calcula la cobertura sin filtrar
		env.KB.ApplyR1(r1off)
		var b strings.Builder
		for _, q := range set.Questions {
			if q.Class != eval.ClassNoAnswer && q.Class != eval.ClassParaphrase {
				continue
			}
			res, err := env.KB.Search(env.Ctx(), env.ID(tm.Luis), service.SearchRequest{Q: q.Question, Spaces: set.Spaces, Limit: 5, Mode: "hybrid"})
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%-10s %-32s", q.Class, q.ID)
			for i, h := range res.Hits {
				if i == 3 {
					break
				}
				sim := -1.0
				if h.Similarity != nil {
					sim = *h.Similarity
				}
				ok := ""
				for _, e := range q.Expected {
					if e.Matches(eval.Hit{Space: h.Space, DocKey: h.DocKey, Anchor: h.Anchor, Version: h.Version}) {
						ok = "*"
					}
				}
				fmt.Fprintf(&b, "  [cov %.2f sim %.3f %v%s]", h.Coverage, sim, h.MatchedBy, ok)
			}
			b.WriteString("\n")
		}
		t.Log("diagnóstico (sin umbral; * = esperado):\n" + b.String())
		env.KB.ApplyR1(r1)
		env.KB.ApplyR2(saved)
	}
	reports := map[string]any{}
	order := []string{}
	for _, rc := range runs {
		r := service.NewEvalRetriever(env.KB, env.ID(tm.Luis))
		r.NoChanges = os.Getenv("NEXO_KB_EVAL_BASELINE") == "1"
		switch rc.name {
		case "default":
		case "fts":
			r.Mode = "fts"
		default:
			r.Mode = "hybrid"
			o := env.KB.R2()
			o.Hybrid.Weights.Vector, o.Hybrid.Weights.Text, o.MinSimilarity, o.MinTextSimilarity = rc.vw, rc.tw, rc.minSim, rc.minTS
			env.KB.ApplyR2(o)
		}
		rep := eval.RunWith(ctx, set, r, eval.Options{})
		t.Logf("== %s: %s", rc.name, rep.Summary())
		t.Log("\n" + rep.Table())
		var misses []string
		for _, q := range rep.Results {
			if !q.Correct {
				top := []string{}
				for i, h := range q.Top {
					if i == 3 {
						break
					}
					top = append(top, fmt.Sprintf("%s#%s v%d", h.DocKey, h.Anchor, h.Version))
				}
				misses = append(misses, fmt.Sprintf("  %-30s %-10s rank=%d no_results=%t err=%s top=%v", q.ID, q.Class, q.Rank, q.NoResults, q.Error, top))
			}
		}
		sort.Strings(misses)
		t.Log("fallos:\n" + strings.Join(misses, "\n"))
		// RC-28: cada corrida queda en nexo_kb_eval_runs (del esquema de la prueba).
		run, err := env.KB.RecordEvalRun(env.Ctx(), env.ID(tm.Ana), service.RecordEvalRunInput{Report: rep, Config: env.KB.EvalConfig(), Mode: r.Mode,
			Spaces: []string{"arquitectura"}, Note: "R0 wiki: " + rc.name})
		if err != nil {
			t.Fatal(err)
		}
		reports[rc.name] = map[string]any{"report": rep, "config": env.KB.EvalConfig(), "eval_run_id": run.ID}
		order = append(order, rc.name)
	}
	if out := os.Getenv("NEXO_KB_EVAL_OUT"); out != "" {
		raw, _ := json.MarshalIndent(map[string]any{"runs": reports, "order": order, "load": res.Summary}, "", "  ")
		if err := os.WriteFile(out, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
