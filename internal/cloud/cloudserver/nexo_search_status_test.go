package cloudserver

// Nexo IA · NX-10 (panel) — pruebas de la pantalla "Estado de la búsqueda"
// contra Postgres real (helpers de nexo_security_test.go): se dibuja con y sin
// proveedor de embeddings, nunca contiene la clave (ni por valor ni por
// archivo) y solo la ve quien tiene platform.settings.

import (
	"context"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/embed"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/rerank"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
	kbservice "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	kbstore "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

const searchStatusPath = "/dashboard/kb/search-status"

// Clave de prueba con forma de clave de Gemini: si aparece en una página, el
// buscador de secretos lo marca.
const searchFakeKey = "AIzaSyFAKE-canary-0123456789abcdefghij"

func searchStatusEnv(t *testing.T, envs map[string]string) *nexoSecEnv {
	t.Helper()
	for k, v := range envs {
		t.Setenv(k, v)
	}
	env := newNexoSecEnv(t, nil)
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "carla", "", "10.91.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("me de carla: %d", rec.Code)
	}
	return env
}

// searchNoLeaks: la página no contiene tokens del panel ni la clave del
// proveedor (mismo buscador que S5, con la clave agregada).
func searchNoLeaks(t *testing.T, env *nexoSecEnv, body string, secrets ...string) {
	t.Helper()
	scan := newNexoLeakScanner()
	for name, raw := range env.tokens {
		scan.addToken(name, raw, env.hasher.Hash)
	}
	for _, s := range secrets {
		scan.values[s] = "clave del proveedor"
	}
	if got := scan.find(html.UnescapeString(body)); len(got) > 0 {
		t.Errorf("la pantalla filtra secretos: %v", got)
	}
}

func TestNexoSearchStatusWithProvider(t *testing.T) {
	env := searchStatusEnv(t, map[string]string{
		embed.EnvEmbedder: "gemini", embed.EnvAPIKey: searchFakeKey, embed.EnvDims: "768",
		// Las variables de respaldo del entorno de quien corre las pruebas no deben pesar.
		"GEMINI_API_KEY": "", "GOOGLE_API_KEY": "",
	})
	ctx := context.Background()
	svc, err := env.srv.nexoKBService(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !svc.VectorEnabled() {
		t.Fatal("el servicio debería tener búsqueda por significado con NEXO_KB_EMBEDDER=gemini")
	}
	// Una versión activa (v1), una en construcción (v2) y una retirada (v0), una
	// carta muerta con la clave dentro del mensaje (el indexador la borra, la
	// pantalla lo comprueba otra vez) y una corrida de evaluación.
	vi, _, err := svc.EnsureVectorIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	env.exec(`UPDATE nexo_kb_index_versions SET status = 'active', built_at = now() WHERE index_kind = 'vector' AND version = $1`, vi.Version)
	if _, _, err := svc.Store().EnsureVectorIndex(ctx, mustEmbedConfig(t), true); err != nil {
		t.Fatal(err)
	}
	env.exec(`INSERT INTO nexo_kb_index_versions (index_kind, version, config, status, built_at) VALUES ('vector', 0,
		'{"provider":"openai","model":"text-embedding-3-small","dims":1536}', 'retired', now() - interval '3 days')`)
	env.exec(`INSERT INTO nexo_kb_index_dead (index_version, section_id, space_id, last_error, fail_count)
		VALUES ($1, gen_random_uuid(), gen_random_uuid(), $2, 5)`, vi.Version,
		"gemini: 400 Bad Request: https://generativelanguage.googleapis.com/v1beta/models/x:embedContent?key="+searchFakeKey+" token "+searchFakeKey+" API key not valid")
	if err := kbstore.InsertEvalRun(ctx, env.db, kbstore.EvalRun{ID: kbstore.NewID(), Retriever: "hybrid", Mode: "hybrid", ConfigSHA256: "c", QuestionSetSHA256: "q",
		QuestionSetName: "wiki-rp", Spaces: []string{"arq"}, Accepted: true,
		Metrics: map[string]any{"questions": 120, "hit_at_5": 0.9167, "mrr": 0.8123, "no_answer_accuracy": 1.0}}, kbstore.Actor{Label: "test"}); err != nil {
		t.Fatal(err)
	}

	rec := env.panel("carla").get(searchStatusPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %.300s", searchStatusPath, rec.Code, rec.Body.String())
	}
	body := html.UnescapeString(rec.Body.String())
	for _, want := range []string{
		"Estado de la búsqueda", "Texto y significado", "gemini-embedding-001", "768", "Configurada",
		"Se lee de la variable de entorno NEXO_KB_EMBED_API_KEY", "pgvector", "Disponible",
		"Activa", "En construcción", "Retirada", "text-embedding-3-small",
		"engram nexo kb reindex --vectors --requeue-dead", "API key not valid", "[clave]",
		"wiki-rp", "0.917", "0.812", "1.000", "Aceptada",
		"Fusión de resultados (RRF)", "NEXO_KB_RRF_VECTOR_WEIGHT", "0.70", "Reordenamiento (R3)", "No configurado",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("falta %q en la pantalla", want)
		}
	}
	if strings.Contains(body, searchFakeKey) || strings.Contains(body, "key=AIza") {
		t.Errorf("la pantalla muestra la clave del proveedor")
	}
	searchNoLeaks(t, env, rec.Body.String(), searchFakeKey)
}

func TestNexoSearchStatusKeyFromFileNeverShown(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "gemini.key")
	if err := os.WriteFile(keyFile, []byte(searchFakeKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := searchStatusEnv(t, map[string]string{embed.EnvEmbedder: "gemini", embed.EnvAPIKeyFile: keyFile, embed.EnvAPIKey: "",
		"GEMINI_API_KEY": "", "GOOGLE_API_KEY": ""})
	rec := env.panel("carla").get(searchStatusPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %.300s", rec.Code, rec.Body.String())
	}
	body := html.UnescapeString(rec.Body.String())
	if !strings.Contains(body, "Configurada") || !strings.Contains(body, "Se lee del archivo que indica NEXO_KB_EMBED_API_KEY_FILE") {
		t.Errorf("debería decir que la clave sale de un archivo: %.200s", body)
	}
	if strings.Contains(body, searchFakeKey) || strings.Contains(body, keyFile) {
		t.Errorf("la pantalla muestra la clave o la ruta del archivo")
	}
	searchNoLeaks(t, env, rec.Body.String(), searchFakeKey)
}

func TestNexoSearchStatusWithoutProvider(t *testing.T) {
	env := searchStatusEnv(t, map[string]string{embed.EnvEmbedder: "", embed.EnvAPIKey: "", embed.EnvAPIKeyFile: ""})
	rec := env.panel("carla").get(searchStatusPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %.300s", rec.Code, rec.Body.String())
	}
	body := html.UnescapeString(rec.Body.String())
	for _, want := range []string{"La búsqueda usa solo texto.", "Sin proveedor", "NEXO_KB_EMBEDDER no está configurado", "No aplica",
		"Todavía no hay versiones", "Todavía no hay corridas registradas", "Reordenamiento (R3)", "No configurado", "Ninguna."} {
		if !strings.Contains(body, want) {
			t.Errorf("falta %q en la pantalla sin proveedor", want)
		}
	}
	searchNoLeaks(t, env, rec.Body.String())
}

func TestNexoSearchStatusInvalidConfigShowsReasonNotSecret(t *testing.T) {
	env := searchStatusEnv(t, map[string]string{embed.EnvEmbedder: "gemini", embed.EnvDims: "99999", embed.EnvAPIKey: searchFakeKey})
	rec := env.panel("carla").get(searchStatusPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %.300s", rec.Code, rec.Body.String())
	}
	body := html.UnescapeString(rec.Body.String())
	if !strings.Contains(body, "la configuración de embeddings no es válida") || !strings.Contains(body, embed.EnvDims) {
		t.Errorf("debería explicar la configuración inválida: %.300s", body)
	}
	searchNoLeaks(t, env, rec.Body.String(), searchFakeKey)
}

// Solo el superadmin (platform.settings): el administrador, un miembro y quien
// no inició sesión no ven datos; el intento queda como authz.denied.
func TestNexoSearchStatusAccess(t *testing.T) {
	env := searchStatusEnv(t, nil)
	for _, who := range []string{"pedro", "ana", "luis"} {
		rec := env.panel(who).get(searchStatusPath)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: GET = %d, quería 403", who, rec.Code)
			continue
		}
		body := html.UnescapeString(rec.Body.String())
		if !strings.Contains(body, "superadmin") || strings.Contains(body, "Configuración") || strings.Contains(body, "pgvector") {
			t.Errorf("%s: el 403 debe explicar el rol y no traer datos: %.300s", who, body)
		}
	}
	if rec := performDashboardRequest(env.srv, http.MethodGet, searchStatusPath, nil); rec.Code == http.StatusOK {
		t.Errorf("sin sesión no debe responder 200 (fue %d)", rec.Code)
	}
	if n := env.count(`SELECT count(*) FROM nexo_authz_events WHERE action = 'authz.denied' AND details->>'action' = 'index.search_status'`); n < 3 {
		t.Errorf("las tres denegaciones deben quedar auditadas, hay %d", n)
	}
	if rec := env.panel("carla").get(searchStatusPath); rec.Code != http.StatusOK {
		t.Errorf("carla (superadmin): GET = %d", rec.Code)
	}
	// El menú muestra la pestaña a quien administra.
	if body := env.panel("carla").get(searchStatusPath).Body.String(); !strings.Contains(body, `href="`+searchStatusPath+`"`) || !strings.Contains(body, "Estado de la búsqueda") {
		t.Error("el menú del superadmin debería traer la pestaña Estado de la búsqueda")
	}
}

func mustEmbedConfig(t *testing.T) embed.Config {
	t.Helper()
	c, err := embed.ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// ─── Armado de la página (sin base de datos) ─────────────────────────────────

func statusFixture() kbservice.SearchStatus {
	st := kbservice.SearchStatus{DefaultMode: "hybrid", Enabled: true, R2: kbservice.DefaultR2, R1: kbservice.DefaultR1, Sections: 1240,
		Embedder: kbservice.SearchEmbedder{Provider: "gemini", Model: "gemini-embedding-001", Dims: 768, Storage: "vector",
			Key: embed.KeyInfo{Required: true, Configured: true, Source: embed.KeyFromEnv, Variable: embed.EnvAPIKey}}}
	st.PGVector.Available, st.PGVector.Version, st.PGVector.Schema, st.PGVector.Iterative = true, "0.8.0", "public", true
	st.Queue = []kbstore.VectorQueueStats{{Version: 2, Status: "active", Provider: "gemini", Model: "gemini-embedding-001", Dims: 768, Vectors: 1240}}
	return st
}

func TestSearchStatusVerdicts(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*kbservice.SearchStatus)
		class string
		title string
	}{
		{"al día", func(*kbservice.SearchStatus) {}, "ok", "el índice está al día"},
		{"pendientes", func(s *kbservice.SearchStatus) { s.Queue[0].Pending, s.Queue[0].Vectors = 40, 1200 }, "info", "usa texto y significado."},
		{"retraso del registro", func(s *kbservice.SearchStatus) { s.Queue[0].FeedLag = 3 }, "info", "usa texto y significado."},
		{"cartas muertas", func(s *kbservice.SearchStatus) { s.DeadTotal = 2 }, "info", "usa texto y significado."},
		{"construyendo", func(s *kbservice.SearchStatus) {
			s.DefaultMode, s.Queue = "fts", []kbstore.VectorQueueStats{{Version: 1, Status: "building", Vectors: 620, Pending: 620}}
		}, "warn", "se está construyendo (50 %)"},
		{"sin pgvector", func(s *kbservice.SearchStatus) { s.DefaultMode, s.PGVector.Available = "fts", false }, "warn", "falta la extensión pgvector"},
		{"sin proveedor", func(s *kbservice.SearchStatus) { s.Enabled, s.DefaultMode, s.Queue = false, "fts", nil }, "info", "usa solo texto."},
		{"configuración inválida", func(s *kbservice.SearchStatus) {
			s.Enabled, s.DefaultMode, s.ConfigError = false, "fts", "falta la clave"
		}, "warn", "no es válida"},
		{"hybrid apagado con índice", func(s *kbservice.SearchStatus) { s.DefaultMode, s.Reason = "fts", "otro modelo" }, "warn", "pero la búsqueda usa solo texto"},
	}
	for _, c := range cases {
		st := statusFixture()
		c.edit(&st)
		page := buildSearchStatusPage(st)
		if page.Verdict.Class != c.class || !strings.Contains(page.Verdict.Title, c.title) {
			t.Errorf("%s: veredicto %q/%q, quería %s con %q", c.name, page.Verdict.Class, page.Verdict.Title, c.class, c.title)
		}
	}
}

func TestSearchStatusProgressAndFormats(t *testing.T) {
	st := statusFixture()
	st.Queue = []kbstore.VectorQueueStats{{Version: 3, Status: "building", Provider: "gemini", Model: "m", Vectors: 310, Pending: 930, Claimed: 100, Failing: 4, OldestSeconds: 7500, FeedLag: 12, Dead: 1}}
	page := buildSearchStatusPage(st)
	if len(page.Progress) != 1 {
		t.Fatalf("avance: %d", len(page.Progress))
	}
	pr := page.Progress[0]
	if pr.Percent != 25 || pr.Done != 310 || pr.Total != 1240 || !strings.Contains(pr.DoneText, "310 de 1 240 secciones con vector (25 %)") {
		t.Errorf("avance mal calculado: %+v", pr)
	}
	var seen = map[string]string{}
	for _, f := range pr.Facts {
		seen[f.Label] = f.Value
	}
	if seen["Pendientes"] != "930" || seen["En proceso"] != "100" || seen["Con fallos"] != "4" || seen["Cambios sin leer"] != "12" || seen["Espera más antigua"] != "2 h" {
		t.Errorf("hechos del avance: %v", seen)
	}
	if got := searchInt(1234567); got != "1 234 567" {
		t.Errorf("searchInt = %q", got)
	}
	if got := searchInt(-1200); got != "-1 200" {
		t.Errorf("searchInt negativo = %q", got)
	}
	// Total cero: sin división por cero.
	st.Sections = 0
	if p := buildSearchStatusPage(st).Progress[0]; p.Percent != 0 {
		t.Errorf("total 0: %d %%", p.Percent)
	}
}

// Sin reordenador, el bloque de R3 dice "no configurado" y cómo activarlo.
func TestSearchStatusRerankOff(t *testing.T) {
	r := buildSearchStatusPage(statusFixture()).Rerank
	if r.Configured || len(r.Rows) != 0 || !strings.Contains(r.Note, "NEXO_KB_RERANKER") {
		t.Errorf("sin reordenador: %+v", r)
	}
}

// Con reordenador, el bloque muestra proveedor, modelo y parámetros, nunca la
// URL del proveedor.
func TestSearchStatusRerankConfigured(t *testing.T) {
	st := statusFixture()
	st.Rerank = kbservice.RerankState{Enabled: true,
		Reranker: map[string]any{"provider": "llm/gemini", "model": "gemini-3.1-flash-lite", "base_url": "https://proveedor.example/secreto"},
		Options:  search.RerankOptions{TopN: 20, Blend: 0.7, Budget: 6 * time.Second},
		Stats:    &rerank.Stats{Calls: 3, Retries: 1}}
	r := buildSearchStatusPage(st).Rerank
	if !r.Configured {
		t.Fatalf("con reordenador: %+v", r)
	}
	all := ""
	for _, row := range r.Rows {
		all += row.Label + "=" + row.Value + "|" + row.Env + ";"
	}
	for _, want := range []string{"Proveedor=llm/gemini", "Modelo=gemini-3.1-flash-lite", "Candidatos que reordena=20", "Peso del reordenador=0.70",
		"Puntaje mínimo=Apagado", "Solo si la consulta lo pide", "NEXO_KB_RERANK_DEFAULT", "3 llamadas, 1 reintentos"} {
		if !strings.Contains(all, want) {
			t.Errorf("falta %q en %s", want, all)
		}
	}
	if strings.Contains(all, "proveedor.example") {
		t.Errorf("la URL del proveedor no debe mostrarse: %s", all)
	}
}

func TestSearchStatusParamsShowRRFAndThresholds(t *testing.T) {
	page := buildSearchStatusPage(statusFixture())
	all := map[string]string{}
	for _, g := range page.Params {
		for _, r := range g.Rows {
			all[r.Label] = r.Value
		}
	}
	for label, want := range map[string]string{"Constante k": "60", "Peso del significado": "0.70", "Peso del texto": "0.30",
		"Similitud mínima por significado": "0.70", "Similitud mínima por texto": "0.65", "Cobertura mínima de la pregunta": "0.30", "Secciones por documento": "2"} {
		if all[label] != want {
			t.Errorf("%s = %q, quería %q", label, all[label], want)
		}
	}
}

func (e *nexoSecEnv) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Exec(q, args...); err != nil {
		e.t.Fatalf("%.80s: %v", q, err)
	}
}
