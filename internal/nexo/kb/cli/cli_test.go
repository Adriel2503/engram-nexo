package cli_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/cli"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/httpapi"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

// server levanta /nexo/kb/v1 con un middleware de prueba: el token
// "tok-<principal>" identifica al principal (en producción lo hace withAuth).
func server(t *testing.T, env *kbtest.Env) *httptest.Server {
	mux := http.NewServeMux()
	httpapi.Register(mux, func(*http.Request) (*service.Service, error) { return env.KB, nil }, func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !strings.HasPrefix(tok, "tok-") {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			id := env.ID(strings.TrimPrefix(tok, "tok-"))
			id.UserAgent = r.UserAgent()
			next(w, r.WithContext(authz.WithIdentity(r.Context(), id)))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cli.Run(args, t.TempDir(), &out, &errb)
	return code, out.String(), errb.String()
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCLIPublishSearchEval(t *testing.T) {
	env := kbtest.New(t)
	tm := env.NewTeam()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "lineamientos-frontend", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Roles.SetScopeMember(env.Ctx(), env.ID(tm.Ana), authz.SpaceScope("lineamientos-frontend"), tm.CI, authz.RolePublicador); err != nil {
		t.Fatal(err)
	}
	srv := server(t, env)
	t.Setenv("ENGRAM_CLOUD_SERVER", srv.URL)
	t.Setenv("ENGRAM_CLOUD_TOKEN", "tok-"+tm.CI)
	t.Setenv("GITHUB_REPOSITORY", "realplaza/lineamientos")
	t.Setenv("GITHUB_SHA", "c0ffee")
	t.Setenv("GITHUB_REF_NAME", "main")
	t.Setenv("GITHUB_ACTOR", "ana-gh")
	t.Setenv("GITHUB_RUN_ID", "42")

	repo := t.TempDir()
	copyTree(t, filepath.Join("..", "ingest", "testdata", "lineamientos-frontend"), repo)
	if err := os.WriteFile(filepath.Join(repo, "nexo-kb.yaml"), []byte("space: lineamientos-frontend\ncategory: glosario\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(repo, ".github", "ignorar.md"), []byte("# no"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Repo"), 0o644)

	// dry-run (opción después de la ruta).
	code, out, errOut := run(t, "kb", "publish", repo, "--dry-run")
	if code != 0 || !strings.Contains(out, "dry-run") || !strings.Contains(out, "created    frontend/formularios v1") {
		t.Fatalf("dry-run: %d %s %s", code, out, errOut)
	}
	if n := env.Count(`SELECT COUNT(*) FROM nexo_kb_documents`); n != 0 {
		t.Fatal("dry-run no escribe")
	}
	code, out, errOut = run(t, "kb", "publish", "--prune", repo)
	if code != 0 || !strings.Contains(out, "creados 3") {
		t.Fatalf("publish: %d %s %s", code, out, errOut)
	}
	var commit, run_ string
	if err := env.DB.QueryRow(`SELECT origin->>'commit', origin->>'ci_run' FROM nexo_kb_versions LIMIT 1`).Scan(&commit, &run_); err != nil || commit != "c0ffee" || run_ != "42" {
		t.Fatalf("origen desde el CI: %s %s %v", commit, run_, err)
	}
	// Borrar un archivo y podar.
	if err := os.Remove(filepath.Join(repo, "frontend", "componentes.md")); err != nil {
		t.Fatal(err)
	}
	code, out, _ = run(t, "kb", "publish", "--prune", repo)
	if code != 0 || !strings.Contains(out, "retired    frontend/componentes") || !strings.Contains(out, "sin cambios 2") {
		t.Fatalf("poda: %d %s", code, out)
	}
	// Paginación con poda (lotes de 1): sin cambios y sin poda nueva.
	code, out, _ = run(t, "kb", "publish", "--prune", "--batch-size", "1", repo)
	if code != 0 || !strings.Contains(out, "sin cambios 2") || strings.Contains(out, "poda omitida") {
		t.Fatalf("lotes paginados: %d %s", code, out)
	}

	// Buscar como Luis.
	t.Setenv("ENGRAM_CLOUD_TOKEN", "tok-"+tm.Luis)
	code, out, _ = run(t, "kb", "search", "validacion", "de", "formularios")
	if code != 0 || !strings.Contains(out, "1. lineamientos-frontend / Formularios en aplicaciones web") {
		t.Fatalf("search: %d %s", code, out)
	}
	code, out, _ = run(t, "kb", "search", "kubernetes")
	if code != 0 || !strings.Contains(out, "No existe un estándar oficial") {
		t.Fatalf("sin resultados: %d %s", code, out)
	}
	code, out, _ = run(t, "kb", "spaces")
	if code != 0 || !strings.Contains(out, "lineamientos-frontend") || !strings.Contains(out, "lector") {
		t.Fatalf("spaces: %d %s", code, out)
	}
	// Un lector no publica.
	code, _, errOut = run(t, "kb", "publish", repo)
	if code == 0 || !strings.Contains(errOut, "403") {
		t.Fatalf("lector publica: %d %s", code, errOut)
	}
	// eval contra el servidor.
	evalFile := filepath.Join("..", "eval", "testdata", "preguntas.yaml")
	code, out, errOut = run(t, "kb", "eval", evalFile)
	if code != 0 || !strings.Contains(out, "motor pgfts: 11 preguntas") {
		t.Fatalf("eval: %d %s %s", code, out, errOut)
	}
	t.Log(strings.SplitN(out, "\n", 2)[0])
	code, _, errOut = run(t, "kb", "eval", evalFile, "--retriever", "local-bm25")
	if code != 2 || !strings.Contains(errOut, "2b") {
		t.Fatalf("local-bm25 aún no: %d %s", code, errOut)
	}
	// Sin token.
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	if code, _, errOut = run(t, "kb", "spaces"); code == 0 || !strings.Contains(errOut, "falta el token") {
		t.Fatalf("sin token: %d %s", code, errOut)
	}
}

func TestCLIUsage(t *testing.T) {
	if code, out, _ := run(t, "kb", "help"); code != 0 || !strings.Contains(out, "publish --space") {
		t.Fatal(out)
	}
	if code, _, _ := run(t, "kb", "inventado"); code != 2 {
		t.Fatal("comando desconocido")
	}
	if code, _, _ := run(t, "otra"); code != 2 {
		t.Fatal("subcomando desconocido")
	}
}
