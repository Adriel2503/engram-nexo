package service_test

// Medición R0/R1 sobre un corpus real (wiki de Azure DevOps), fuera del CI:
// se activa con variables de entorno y nunca versiona el corpus.
//
//	NEXO_KB_WIKI_DIR      carpeta de la wiki (obligatoria)
//	NEXO_KB_EVAL_SET      archivo de preguntas (obligatorio salvo con NEXO_KB_EVAL_DUMP)
//	NEXO_KB_EVAL_OVERLAY  carpeta con versiones nuevas de algunas páginas (opcional)
//	NEXO_KB_EVAL_OUT      archivo JSON del informe (opcional)
//	NEXO_KB_EVAL_DUMP     archivo con documentos y anclas indexadas (sin cuerpos), para escribir preguntas
//	NEXO_KB_EVAL_BASELINE 1 = sin la consulta de cambios (línea base anterior a RC-27)
//
// Las opciones de R1 se toman del entorno (NEXO_KB_R1 y afines, ver r1.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/eval"
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

func TestEvalWikiCorpus(t *testing.T) {
	wiki := os.Getenv("NEXO_KB_WIKI_DIR")
	if wiki == "" {
		t.Skip("NEXO_KB_WIKI_DIR no definido (medición sobre corpus real, fuera del CI)")
	}
	env := kbtest.New(t)
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
	r := service.NewEvalRetriever(env.KB, env.ID(tm.Luis))
	r.NoChanges = os.Getenv("NEXO_KB_EVAL_BASELINE") == "1"
	rep := eval.RunWith(ctx, set, r, eval.Options{})
	t.Log(rep.Summary())
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
	if out := os.Getenv("NEXO_KB_EVAL_OUT"); out != "" {
		raw, _ := json.MarshalIndent(map[string]any{"report": rep, "config": env.KB.EvalConfig(), "load": res.Summary}, "", "  ")
		if err := os.WriteFile(out, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
