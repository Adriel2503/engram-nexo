package eval_test

// RC-02: prueba de calidad de la búsqueda en CI sobre un corpus sintético
// versionable (testdata/mini, no la wiki real). Falla si la calidad cae bajo
// el umbral o si empeora respecto de la última corrida aceptada guardada en
// nexo_kb_eval_runs (RC-28).

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/eval"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/source"
)

// Umbrales de la prueba (sobre el corpus sintético, con R1 por defecto).
const (
	minHitAt5    = 0.80 // medido: 0,818 (las 2 paráfrasis caen por el umbral de RC-07; las resuelve R2)
	minNoAnswer  = 0.66
	maxRegession = 0.02
)

func TestSearchQualityGate(t *testing.T) {
	env := kbtest.New(t)
	tm := env.NewTeam()
	ctx := context.Background()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "mini", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	var files []source.File
	root := filepath.Join("testdata", "mini")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files = append(files, source.File{Path: filepath.ToSlash(rel), Data: b})
		return nil
	})
	res, err := env.KB.Upload(env.Ctx(), env.ID(tm.Ana), "mini", service.UploadRequest{Files: files, Collection: "mini"})
	if err != nil || res.Summary["created"] != 5 {
		t.Fatalf("carga: %+v %v", res.Summary, err)
	}
	set, err := eval.Load(filepath.Join("testdata", "preguntas-mini.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rep := eval.RunWith(ctx, set, service.NewEvalRetriever(env.KB, env.ID(tm.Luis)), eval.Options{ValidatedOnly: true})
	t.Log(rep.Summary())
	t.Log("\n" + rep.Table())
	if rep.Total.HitAt5 < minHitAt5 || rep.Total.Errors > 0 {
		t.Fatalf("calidad bajo el umbral: Hit@5 %.3f < %.2f (errores %d)", rep.Total.HitAt5, minHitAt5, rep.Total.Errors)
	}
	if na := rep.Total.NoAnswerAccuracy; na == nil || *na < minNoAnswer {
		t.Fatalf("acierto de 'no hay respuesta' bajo el umbral: %v", na)
	}
	// RC-28: la corrida queda registrada; la siguiente se compara con la
	// última aceptada.
	run, err := env.KB.RecordEvalRun(env.Ctx(), env.ID(tm.Ana), service.RecordEvalRunInput{Report: rep, Config: env.KB.EvalConfig(), Accepted: true, Spaces: []string{"mini"}, CommitRef: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.KB.RecordEvalRun(env.Ctx(), env.ID(tm.Luis), service.RecordEvalRunInput{Report: rep, Spaces: []string{"mini"}}); err == nil {
		t.Fatal("un lector no registra corridas")
	}
	runs, err := env.KB.ListEvalRuns(env.Ctx(), env.ID(tm.Luis), rep.SetSHA256, true, 10)
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("última aceptada: %+v %v", runs, err)
	}
	base := map[string]eval.Metrics{}
	raw, _ := json.Marshal(runs[0].ByClass)
	_ = json.Unmarshal(raw, &base)
	tot := eval.Metrics{}
	raw, _ = json.Marshal(runs[0].Metrics)
	_ = json.Unmarshal(raw, &tot)
	base["total"] = tot
	again := eval.RunWith(ctx, set, service.NewEvalRetriever(env.KB, env.ID(tm.Luis)), eval.Options{ValidatedOnly: true})
	if reg := eval.Regressions(base, again.ClassMetrics(), maxRegession); len(reg) > 0 {
		t.Fatalf("regresión respecto de la última corrida aceptada: %v", reg)
	}
	// Sin R1 (línea base) la calidad es menor: la prueba detecta una regresión.
	envOff := kbtest.New(t, service.WithR1(service.R1Options{}))
	tmOff := envOff.NewTeam()
	_, _ = envOff.KB.CreateSpace(envOff.Ctx(), envOff.ID(tmOff.Adm), service.CreateSpaceInput{Slug: "mini", Owner: "ana"})
	if _, err := envOff.KB.Upload(envOff.Ctx(), envOff.ID(tmOff.Ana), "mini", service.UploadRequest{Files: files, Collection: "mini"}); err != nil {
		t.Fatal(err)
	}
	off := eval.RunWith(ctx, set, service.NewEvalRetriever(envOff.KB, envOff.ID(tmOff.Luis)), eval.Options{ValidatedOnly: true})
	t.Log("sin R1: " + off.Summary())
	if len(eval.Regressions(base, off.ClassMetrics(), maxRegession)) == 0 {
		t.Fatal("la comparación debía detectar la regresión sin R1")
	}
}

// TestSearchQualityGateHybrid (NX-10 R2): el modo hybrid con el embedder
// simulado y determinista (sin red) sobre el mismo corpus sintético. No mide
// la calidad de un modelo real (eso es la medición con Gemini, design §20.6):
// protege la tubería (cola, indexador, fusión RRF y umbral) contra
// regresiones. Exige no empeorar a fts y no bajar el acierto de "no hay
// respuesta".
func TestSearchQualityGateHybrid(t *testing.T) {
	fake := kbtest.NewFakeEmbedder()
	r2 := service.DefaultR2
	r2.Worker = false
	r2.MinSimilarity = hybridMinSimilarity
	env := kbtest.New(t, service.WithEmbedder(fake, fake.FakeConfig()), service.WithR2(r2))
	if !env.KB.VectorEnabled() {
		t.Fatal("R2 configurado")
	}
	tm := env.NewTeam()
	ctx := context.Background()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "mini", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	var files []source.File
	root := filepath.Join("testdata", "mini")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files = append(files, source.File{Path: filepath.ToSlash(rel), Data: b})
		return nil
	})
	if _, err := env.KB.Upload(env.Ctx(), env.ID(tm.Ana), "mini", service.UploadRequest{Files: files, Collection: "mini"}); err != nil {
		t.Fatal(err)
	}
	if d, err := env.KB.DrainVectors(ctx); err != nil || len(d.Activated) != 1 {
		t.Skipf("pgvector no disponible en la base de pruebas: %+v %v", d, err)
	}
	set, err := eval.Load(filepath.Join("testdata", "preguntas-mini.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	fts := service.NewEvalRetriever(env.KB, env.ID(tm.Luis))
	fts.Mode = "fts"
	base := eval.RunWith(ctx, set, fts, eval.Options{ValidatedOnly: true})
	hyb := service.NewEvalRetriever(env.KB, env.ID(tm.Luis))
	hyb.Mode = "hybrid"
	rep := eval.RunWith(ctx, set, hyb, eval.Options{ValidatedOnly: true})
	t.Log("fts:    " + base.Summary())
	t.Log("hybrid: " + rep.Summary())
	t.Log("\n" + rep.Table())
	if rep.Retriever != "hybrid" || rep.Total.Errors > 0 {
		t.Fatalf("motor %s, errores %d", rep.Retriever, rep.Total.Errors)
	}
	if rep.Total.HitAt5 < base.Total.HitAt5 {
		t.Fatalf("hybrid empeora a fts: Hit@5 %.3f < %.3f", rep.Total.HitAt5, base.Total.HitAt5)
	}
	if na, nb := rep.Total.NoAnswerAccuracy, base.Total.NoAnswerAccuracy; na == nil || nb == nil || *na < *nb || *na < minNoAnswer {
		t.Fatalf("acierto de 'no hay respuesta' del hybrid: %v (fts %v)", na, nb)
	}
	if p := rep.ByClass[eval.ClassParaphrase]; p.HitAt5 <= base.ByClass[eval.ClassParaphrase].HitAt5 {
		t.Fatalf("hybrid debe mejorar las paráfrasis: %.3f <= %.3f", p.HitAt5, base.ByClass[eval.ClassParaphrase].HitAt5)
	}
}

// hybridMinSimilarity es el umbral de similitud del embedder simulado (su
// escala no es la de Gemini; medido: 0,45 deja las paráfrasis y los 5 "no
// hay" del corpus sintético).
const hybridMinSimilarity = 0.45

// TestSearchQualityGateRerank (NX-10 R3): hybrid + reordenamiento con el
// reordenador simulado y determinista (sin red) sobre el mismo corpus
// sintético. Protege la tubería (candidatos, umbral, orden, rastro) contra
// regresiones; no mide la calidad de un modelo (design §21). Exige que el
// reordenamiento no empeore a hybrid, que no baje el acierto de "no hay
// respuesta" (el reordenador no puede rescatar lo que el umbral descartó) y
// que, si el reordenador cae, el resultado sea exactamente el de hybrid.
func TestSearchQualityGateRerank(t *testing.T) {
	fake := kbtest.NewFakeEmbedder()
	fr := kbtest.NewFakeReranker()
	r2 := service.DefaultR2
	r2.Worker = false
	r2.MinSimilarity = hybridMinSimilarity
	env := kbtest.New(t, service.WithEmbedder(fake, fake.FakeConfig()), service.WithR2(r2), service.WithReranker(fr, fr.Config()), service.WithR3(search.DefaultRerankOptions))
	tm := env.NewTeam()
	ctx := context.Background()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "mini", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	var files []source.File
	root := filepath.Join("testdata", "mini")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files = append(files, source.File{Path: filepath.ToSlash(rel), Data: b})
		return nil
	})
	if _, err := env.KB.Upload(env.Ctx(), env.ID(tm.Ana), "mini", service.UploadRequest{Files: files, Collection: "mini"}); err != nil {
		t.Fatal(err)
	}
	if d, err := env.KB.DrainVectors(ctx); err != nil || len(d.Activated) != 1 {
		t.Skipf("pgvector no disponible en la base de pruebas: %+v %v", d, err)
	}
	set, err := eval.Load(filepath.Join("testdata", "preguntas-mini.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	run := func(rerankSel string) eval.Report {
		r := service.NewEvalRetriever(env.KB, env.ID(tm.Luis))
		r.Mode, r.Rerank = "hybrid", rerankSel
		return eval.RunWith(ctx, set, r, eval.Options{ValidatedOnly: true})
	}
	// 1) Umbral de producción: el corpus sintético deja un solo candidato por
	// pregunta, así que el reordenador no tiene nada que reordenar y el
	// resultado (incluido "no hay respuesta") es idéntico al de hybrid. El
	// reordenador nunca rescata lo que el umbral descartó (RC-21).
	base := run("off")
	rep := run("on")
	t.Log("hybrid:         " + base.Summary())
	t.Log("hybrid+rerank:  " + rep.Summary())
	t.Log(rep.RerankLine())
	if rep.Retriever != "hybrid+rerank" || base.Retriever != "hybrid" || rep.Total.Errors > 0 || base.Rerank != nil {
		t.Fatalf("motores %s y %s, errores %d, rastro sin pedirlo: %+v", base.Retriever, rep.Retriever, rep.Total.Errors, base.Rerank)
	}
	if !sameMetrics(rep.Total, base.Total) || rep.Rerank == nil || rep.Rerank.Used != 0 || rep.Rerank.Failed != 0 {
		t.Fatalf("con un candidato el reordenamiento no cambia nada: %+v %+v", rep.Total, rep.Rerank)
	}
	if na := rep.Total.NoAnswerAccuracy; na == nil || *na < minNoAnswer || fr.Calls.Load() != 0 {
		t.Fatalf("acierto de 'no hay respuesta' %v; llamadas al proveedor %d (debían ser 0)", na, fr.Calls.Load())
	}

	// 2) Umbral laxo: varios candidatos por pregunta, el reordenador participa.
	// Con él no se mide el "no hay" (casi todo pasa el umbral) sino que la
	// tubería ordene sin empeorar a hybrid.
	loose := env.KB.R2()
	loose.MinSimilarity = looseMinSimilarity
	env.KB.ApplyR2(loose)
	base = run("off")
	rep = run("on")
	t.Log("umbral laxo, hybrid:         " + base.Summary())
	t.Log("umbral laxo, hybrid+rerank:  " + rep.Summary())
	t.Log(rep.RerankLine())
	t.Log("\n" + rep.Table())
	if s := rep.Rerank; s == nil || s.Used == 0 || s.Failed != 0 || s.Partial != 0 || rep.Total.Errors > 0 {
		t.Fatalf("el reordenador debe haber participado sin fallos: %+v", s)
	}
	if rep.Total.HitAt5 < base.Total.HitAt5 {
		t.Fatalf("hybrid+rerank empeora a hybrid: Hit@5 %.3f < %.3f", rep.Total.HitAt5, base.Total.HitAt5)
	}
	if rep.Total.MRR < base.Total.MRR-maxRerankMRRDrop {
		t.Fatalf("hybrid+rerank baja el MRR más de lo tolerado: %.3f < %.3f", rep.Total.MRR, base.Total.MRR)
	}
	if na, nb := rep.Total.NoAnswerAccuracy, base.Total.NoAnswerAccuracy; na == nil || nb == nil || *na < *nb {
		t.Fatalf("acierto de 'no hay respuesta' con reordenamiento: %v (hybrid %v)", na, nb)
	}
	// Caída del proveedor: el resultado es exactamente el de hybrid, sin errores.
	fr.SetFail(errors.New("proveedor caído"))
	down := run("on")
	if down.Total.Errors > 0 || !sameMetrics(down.Total, base.Total) || down.Rerank == nil || down.Rerank.Used != 0 || down.Rerank.Failed == 0 {
		t.Fatalf("con el reordenador caído: %s %+v", down.Summary(), down.Rerank)
	}
	for i, q := range down.Results {
		if q.Rank != base.Results[i].Rank || q.NoResults != base.Results[i].NoResults {
			t.Fatalf("%s: la caída segura conserva el orden de hybrid", q.ID)
		}
	}
}

// sameMetrics compara dos métricas por valor (NoAnswerAccuracy es un puntero).
func sameMetrics(a, b eval.Metrics) bool {
	pa, pb := a.NoAnswerAccuracy, b.NoAnswerAccuracy
	a.NoAnswerAccuracy, b.NoAnswerAccuracy = nil, nil
	return a == b && (pa == nil) == (pb == nil) && (pa == nil || *pa == *pb)
}

// looseMinSimilarity deja pasar varios candidatos por pregunta en el corpus
// sintético, para que el reordenador tenga algo que ordenar.
const looseMinSimilarity = 0.2

// maxRerankMRRDrop es la caída de MRR que tolera la compuerta con el
// reordenador simulado (un puntaje léxico, distinto del de la búsqueda).
const maxRerankMRRDrop = 0.05
