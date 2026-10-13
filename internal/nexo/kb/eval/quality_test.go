package eval_test

// RC-02: prueba de calidad de la búsqueda en CI sobre un corpus sintético
// versionable (testdata/mini, no la wiki real). Falla si la calidad cae bajo
// el umbral o si empeora respecto de la última corrida aceptada guardada en
// nexo_kb_eval_runs (RC-28).

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/eval"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
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
