package cli_test

// Tanda 2 (D-01, D-03): engram nexo kb sync, get, search --local e
// import-transversal contra la API real (Postgres, CLOUDSTORE_TEST_DSN).

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/cli"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

const transversalExport = `{"version":"1","observations":[
 {"id":11,"type":"decision","title":"Logging estructurado con trace_id","project":"nexo-transversal","topic_key":"transversal/observabilidad/logging",
  "content":"**Estado**: Oficial\n**Categoría**: Estándar\n**Qué**: Todos los servicios registran en JSON con trace_id.\n**Por qué**: Correlacionar llamadas.\n**Alcance**: todos\n**Responsable**: Ana\n**Vigente desde**: 2026-01-15\n**Fuente**: ADR-007",
  "created_at":"2026-01-10","updated_at":"2026-01-15"},
 {"id":12,"type":"decision","title":"Reintentos con backoff exponencial","project":"nexo-transversal","topic_key":"transversal/resiliencia/reintentos",
  "content":"**Estado**: Propuesta\n**Categoría**: Lineamiento\n**Qué**: Reintentar con backoff exponencial y jitter.\n**Por qué**: Evitar tormentas de reintentos.",
  "created_at":"2026-02-01","updated_at":"2026-02-01"},
 {"id":13,"type":"decision","title":"Usar log4net","project":"nexo-transversal",
  "content":"**Estado**: Obsoleto\n**Qué**: log4net.","created_at":"2025-01-01","updated_at":"2025-01-01"},
 {"id":14,"type":"decision","title":"Otro proyecto","project":"ms-factura","content":"**Estado**: Oficial\n**Qué**: x","created_at":"2026-01-01","updated_at":"2026-01-01"}
]}`

func TestPlanImport(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "export.json")
	if err := os.WriteFile(p, []byte(transversalExport), 0o600); err != nil {
		t.Fatal(err)
	}
	mems, err := cli.ReadMemoriesExport(p, cli.TransversalProject)
	if err != nil || len(mems) != 3 {
		t.Fatalf("export: %d %v", len(mems), err)
	}
	items := cli.PlanImport(mems)
	got := map[int64]cli.ImportItem{}
	for _, it := range items {
		got[it.MemoryID] = it
	}
	if it := got[11]; it.Action != "document" || it.DocKey != "transversal/observabilidad/logging" || it.Category != "estandar" {
		t.Fatalf("oficial: %+v", it)
	}
	if it := got[12]; it.Action != "proposal" || it.Category != "lineamiento" {
		t.Fatalf("propuesta: %+v", it)
	}
	if it := got[13]; it.Action != "skip" {
		t.Fatalf("obsoleto: %+v", it)
	}
}

func runKB(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cli.Run(append([]string{"kb"}, args...), t.TempDir(), &out, &errb)
	return out.String() + errb.String(), code
}

func TestSyncGetAndImportTransversal(t *testing.T) {
	env := kbtest.New(t)
	tm := env.NewTeam()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "general", Name: "General", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.KB.PublishBatch(env.Ctx(), env.ID(tm.Ana), "general", service.BatchRequest{Documents: []service.PublishDoc{{DocKey: "apis/errores",
		Path: "apis/errores.md", Category: "estandar", Origin: domain.Origin{Kind: domain.OriginUpload, Filename: "errores.md"},
		Content: "# Errores de API\n\n## Formato\n\nLas APIs responden errores con el formato RFC 7807 (problem+json).\n"}}}); err != nil {
		t.Fatal(err)
	}
	srv := env.HTTPServer()
	t.Setenv("ENGRAM_CLOUD_SERVER", srv.URL)
	t.Setenv(client.EnvLocalDB, filepath.Join(t.TempDir(), "kb.db"))

	// sync del lector y get desde la terminal (servidor y copia local).
	t.Setenv("ENGRAM_CLOUD_TOKEN", tm.Luis)
	if out, code := runKB(t, "sync"); code != 0 || !strings.Contains(out, "1 documentos") {
		t.Fatalf("sync: %d %s", code, out)
	}
	var anchor string
	if err := env.DB.QueryRow(`SELECT anchor FROM nexo_kb_sections WHERE anchor LIKE '%formato'`).Scan(&anchor); err != nil {
		t.Fatal(err)
	}
	if out, code := runKB(t, "get", "--space", "general", "apis/errores#"+anchor); code != 0 || !strings.Contains(out, "RFC 7807") || !strings.Contains(out, "Enlace: "+srv.URL) {
		t.Fatalf("get: %d %s", code, out)
	}
	srv.SetDown(true)
	if out, code := runKB(t, "get", "--space", "general", "apis/errores"); code != 0 || !strings.Contains(out, "copia local") || !strings.Contains(out, "RFC 7807") {
		t.Fatalf("get sin servidor: %d %s", code, out)
	}
	if out, code := runKB(t, "sync"); code != 0 || !strings.Contains(out, "Sin conexión") {
		t.Fatalf("sync sin servidor debe terminar en 0: %d %s", code, out)
	}
	if out, code := runKB(t, "search", "--local", "formato de errores de las APIs"); code != 0 || !strings.Contains(out, "apis/errores") {
		t.Fatalf("search --local: %d %s", code, out)
	}
	srv.SetDown(false)

	// import-transversal: dry-run, real y repetido (idempotente).
	t.Setenv("ENGRAM_CLOUD_TOKEN", tm.Ana)
	exp := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(exp, []byte(transversalExport), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runKB(t, "import-transversal", "--space", "general", "--from", exp, "--dry-run")
	if code != 0 || !strings.Contains(out, "dry-run") || !strings.Contains(out, "se propondría") || env.Count(`SELECT count(*) FROM nexo_kb_proposals`) != 0 ||
		env.Count(`SELECT count(*) FROM nexo_kb_documents WHERE doc_key LIKE 'transversal/%'`) != 0 {
		t.Fatalf("dry-run: %d %s", code, out)
	}
	out, code = runKB(t, "import-transversal", "--space", "general", "--from", exp)
	if code != 0 || !strings.Contains(out, "created") || !strings.Contains(out, "propuesta creada") {
		t.Fatalf("import: %d %s", code, out)
	}
	var origin string
	if err := env.DB.QueryRow(`SELECT v.origin->>'kind' FROM nexo_kb_versions v JOIN nexo_kb_documents d ON d.id = v.document_id WHERE d.doc_key = 'transversal/observabilidad/logging'`).Scan(&origin); err != nil || origin != "import" {
		t.Fatalf("origen: %q %v", origin, err)
	}
	out, code = runKB(t, "import-transversal", "--space", "general", "--from", exp, "--json")
	var rep struct {
		Items []cli.ImportItem `json:"items"`
	}
	_ = json.Unmarshal([]byte(out), &rep)
	if code != 0 || len(rep.Items) != 3 || env.Count(`SELECT count(*) FROM nexo_kb_proposals`) != 1 ||
		env.Count(`SELECT count(*) FROM nexo_kb_versions v JOIN nexo_kb_documents d ON d.id = v.document_id WHERE d.doc_key LIKE 'transversal/%'`) != 1 {
		t.Fatalf("repetido no idempotente: %d %s", code, out)
	}
	for _, it := range rep.Items {
		if it.Action == "document" && it.Result != "unchanged" {
			t.Fatalf("documento repetido: %+v", it)
		}
	}
}

// TestReadMemoriesDBReadOnly: la base de Engram se lee en solo lectura.
func TestReadMemoriesDBReadOnly(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "engram.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, title TEXT, content TEXT, type TEXT, project TEXT, topic_key TEXT, created_at TEXT, updated_at TEXT, deleted_at TEXT)`,
		`INSERT INTO observations VALUES (1, 'Uno', '**Estado**: Oficial', 'decision', 'nexo-transversal', NULL, 'a', 'b', NULL)`,
		`INSERT INTO observations VALUES (2, 'Borrado', '**Estado**: Oficial', 'decision', 'nexo-transversal', NULL, 'a', 'b', 'c')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	before, _ := os.ReadFile(filepath.Join(dir, "engram.db"))
	mems, err := cli.ReadMemoriesDB(dir, cli.TransversalProject)
	if err != nil || len(mems) != 1 || mems[0].Title != "Uno" {
		t.Fatalf("lectura: %+v %v", mems, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "engram.db"))
	if !bytes.Equal(before, after) {
		t.Fatal("la base de Engram cambió al leerla")
	}
}
