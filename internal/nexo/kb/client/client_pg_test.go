package client_test

// Pruebas de contrato de las herramientas MCP nexo_* (D-02) y de la copia
// local (D-01) contra la API real (httpapi + Postgres, CLOUDSTORE_TEST_DSN),
// llamando a las herramientas como un cliente MCP (mcp-go en proceso).

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

type world struct {
	env *kbtest.Env
	tm  kbtest.Team
	srv *kbtest.Server
}

func newWorld(t *testing.T) world {
	t.Helper()
	env := kbtest.New(t)
	tm := env.NewTeam()
	ctx := env.Ctx()
	if _, err := env.KB.CreateSpace(ctx, env.ID(tm.Adm), service.CreateSpaceInput{Slug: "arquitectura", Name: "Arquitectura", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "seguridad", Name: "Seguridad", Owner: "ana",
		Visibility: authz.VisibilityRestricted}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Roles.SetScopeMember(env.Ctx(), env.ID(tm.Ana), authz.SpaceScope("seguridad"), tm.Luis, authz.RoleLector); err != nil {
		t.Fatal(err)
	}
	w := world{env: env, tm: tm}
	w.publish(t, "arquitectura", service.PublishDoc{DocKey: "datos/tablas", Path: "datos/tablas.md", Category: "estandar",
		Content: "---\ntitle: Nomenclatura de base de datos\nsource_url: https://wiki.example/datos\n---\n# Nomenclatura de base de datos\n\n## Tablas\n\nLas tablas se nombran en singular con el prefijo del módulo, por ejemplo VTA_CLIENTE.\n"})
	w.publish(t, "arquitectura", service.PublishDoc{DocKey: "logging", Path: "logging.md",
		Content: "# Logging\n\n## Niveles\n\nUsar Serilog con nivel Information para eventos de negocio y Error para fallas.\n"})
	w.publish(t, "seguridad", service.PublishDoc{DocKey: "tokens", Path: "tokens.md",
		Content: "# Tokens\n\n## Caducidad\n\nLos tokens de integración caducan a los 90 días y se rotan con el gestor de secretos.\n"})
	w.srv = env.HTTPServer()
	return w
}

func (w world) publish(t *testing.T, space string, docs ...service.PublishDoc) {
	t.Helper()
	for i := range docs {
		if docs[i].Category == "" {
			docs[i].Category = "lineamiento"
		}
		if docs[i].Origin.Kind == "" {
			docs[i].Origin = domain.Origin{Kind: domain.OriginUpload, Filename: docs[i].Path, Source: "prueba"}
		}
	}
	res, err := w.env.KB.PublishBatch(w.env.Ctx(), w.env.ID(w.tm.Ana), space, service.BatchRequest{Documents: docs})
	if err != nil || res.Summary[domain.ResultRejected] > 0 {
		t.Fatalf("publicar: %+v %v", res, err)
	}
}

// kbFor arma la KB de una persona (token = id del principal en el servidor
// de prueba) con su propia copia local.
func (w world) kbFor(t *testing.T, who string) *client.KB {
	t.Helper()
	l, err := client.OpenLocal(filepath.Join(t.TempDir(), "kb.db"))
	if err != nil {
		t.Fatal(err)
	}
	kb := client.NewKB(client.NewAPI(client.Settings{Server: w.srv.URL, Token: who}, 5*time.Second), l)
	t.Cleanup(func() { _ = kb.Close() })
	return kb
}

// mcpFor registra las herramientas sobre la KB y devuelve un cliente MCP
// en proceso, ya inicializado.
func mcpFor(t *testing.T, kb *client.KB, allowlist map[string]bool) *mcpclient.Client {
	t.Helper()
	srv := server.NewMCPServer("engram-test", "1", server.WithToolCapabilities(true))
	client.RegisterToolsWith(srv, kb, allowlist)
	c, err := mcpclient.NewInProcessClient(srv)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "prueba", Version: "1"}
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// call llama a una herramienta y devuelve el JSON y si fue error.
func call(t *testing.T, c *mcpclient.Client, name string, args map[string]any) (map[string]any, string, bool) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	text := ""
	for _, ct := range res.Content {
		if tc, ok := ct.(mcp.TextContent); ok {
			text += tc.Text
		}
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(text), &m)
	return m, text, res.IsError
}

func results(m map[string]any) []map[string]any {
	var out []map[string]any
	rs, _ := m["results"].([]any)
	for _, r := range rs {
		out = append(out, r.(map[string]any))
	}
	return out
}

func TestToolContractsListAndAllowlist(t *testing.T) {
	w := newWorld(t)
	kb := w.kbFor(t, w.tm.Luis)
	c := mcpFor(t, kb, nil)
	lt, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range lt.Tools {
		names = append(names, tl.Name)
		if tl.Description == "" {
			t.Fatalf("%s sin descripción", tl.Name)
		}
	}
	sort.Strings(names)
	want := append([]string(nil), client.ToolNames...)
	sort.Strings(want)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("herramientas: %v", names)
	}
	// Con allowlist: solo las nombradas, o todas con el token "nexo".
	c2 := mcpFor(t, kb, map[string]bool{"mem_search": true, "nexo_search": true})
	lt, _ = c2.ListTools(context.Background(), mcp.ListToolsRequest{})
	if len(lt.Tools) != 1 || lt.Tools[0].Name != "nexo_search" {
		t.Fatalf("allowlist explícita: %+v", lt.Tools)
	}
	c3 := mcpFor(t, kb, map[string]bool{"mem_search": true, client.ProfileToken: true})
	lt, _ = c3.ListTools(context.Background(), mcp.ListToolsRequest{})
	if len(lt.Tools) != len(client.ToolNames) {
		t.Fatalf("perfil nexo: %d herramientas", len(lt.Tools))
	}
	// Las de solo lectura lo declaran.
	for _, tl := range lt.Tools {
		ro := tl.Annotations.ReadOnlyHint != nil && *tl.Annotations.ReadOnlyHint
		if (tl.Name == "nexo_propose" || tl.Name == "nexo_review") == ro {
			t.Fatalf("%s readOnly=%v", tl.Name, ro)
		}
	}
}

func TestToolsEndToEnd(t *testing.T) {
	w := newWorld(t)
	luis := w.kbFor(t, w.tm.Luis)
	c := mcpFor(t, luis, nil)

	// D-01: el dev con token lector sincroniza.
	rep, err := luis.SyncNow(context.Background())
	if err != nil || rep.Status.Spaces != 2 || rep.Status.Documents != 3 {
		t.Fatalf("sync: %+v %v", rep, err)
	}

	// Búsqueda con cita, enlace y source_url.
	m, raw, isErr := call(t, c, "nexo_search", map[string]any{"query": "cómo se nombran las tablas"})
	rs := results(m)
	if isErr || m["source"] != client.SourceServer || len(rs) == 0 {
		t.Fatalf("search: %s", raw)
	}
	h := rs[0]
	if h["doc_key"] != "datos/tablas" || h["status"] != client.StatusOfficial || !strings.Contains(h["citation"].(string), "arquitectura / Nomenclatura de base de datos") ||
		!strings.HasPrefix(h["link"].(string), w.srv.URL+"/dashboard/kb/spaces/arquitectura/doc?") || h["source_url"] != "https://wiki.example/datos" || h["quote"] == "" {
		t.Fatalf("cita: %s", raw)
	}

	// Sin estándar: lo dice.
	m, raw, _ = call(t, c, "nexo_search", map[string]any{"query": "esquema GraphQL de un servicio"})
	if m["no_results"] != true || m["message"] != client.NoStandardMessage {
		t.Fatalf("sin estándar: %s", raw)
	}

	// Versión nueva: at pasado y qué cambió.
	// Reloj de la base (el de la fuente de verdad), no el de la PC.
	var before time.Time
	if err := w.env.DB.QueryRow(`SELECT now()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	before = before.UTC()
	time.Sleep(1100 * time.Millisecond)
	w.publish(t, "arquitectura", service.PublishDoc{DocKey: "logging", Path: "logging.md",
		Content: "# Logging\n\n## Niveles\n\nUsar OpenTelemetry con nivel Information para eventos de negocio y Error para fallas.\n"})
	m, raw, _ = call(t, c, "nexo_search", map[string]any{"query": "nivel de logging para eventos de negocio", "at": before.Format(time.RFC3339Nano)})
	if rs = results(m); len(rs) == 0 || rs[0]["version"] != float64(1) || !strings.HasPrefix(rs[0]["status"].(string), "Vigente en la fecha consultada") {
		t.Fatalf("at pasado: %s", raw)
	}
	m, raw, isErr = call(t, c, "nexo_changes", map[string]any{"since": before.Format(time.RFC3339Nano)})
	if isErr || !strings.Contains(raw, `"modified"`) || !strings.Contains(raw, "logging") {
		t.Fatalf("qué cambió: %s", raw)
	}

	// Vigencia futura y vencimiento en la copia local, con el reloj real y
	// sin volver a sincronizar.
	now := time.Now().UTC()
	from, until := now.Add(3*time.Second), now.Add(3*time.Second)
	w.publish(t, "arquitectura",
		service.PublishDoc{DocKey: "politica-futura", Path: "politica-futura.md", ValidFrom: &from,
			Content: "# Politica futura\n\n## Regla\n\nLos microservicios exponen el endpoint healthz con el estado de Kubernetes.\n"},
		service.PublishDoc{DocKey: "politica-temporal", Path: "politica-temporal.md", ValidUntil: &until,
			Content: "# Politica temporal\n\n## Regla\n\nDurante la migracion se congela el despliegue de los viernes.\n"})
	if _, err := luis.SyncNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	local := func(q string) client.SearchResult {
		r, err := luis.Search(context.Background(), client.SearchParams{Query: q, LocalOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := local("endpoint healthz Kubernetes"); !r.NoResults {
		t.Fatalf("la futura aparece antes de su fecha: %+v", r.Results)
	}
	if r := local("congela el despliegue de los viernes"); r.NoResults {
		t.Fatalf("la temporal debía estar vigente todavía")
	}
	time.Sleep(time.Until(from) + 1500*time.Millisecond)
	if r := local("endpoint healthz Kubernetes"); r.NoResults || r.Source != client.SourceLocal {
		t.Fatalf("la futura no entró sola en su fecha: %+v", r)
	}
	if r := local("congela el despliegue de los viernes"); !r.NoResults {
		t.Fatalf("la vencida sigue en la copia local: %+v", r.Results)
	}

	// Servidor caído: responde la copia local, marcada como tal.
	w.srv.SetDown(true)
	m, raw, isErr = call(t, c, "nexo_search", map[string]any{"query": "cómo se nombran las tablas"})
	if isErr || m["source"] != client.SourceLocal || !strings.Contains(m["notice"].(string), "copia local") || len(results(m)) == 0 {
		t.Fatalf("sin servidor: %s", raw)
	}
	if !strings.Contains(results(m)[0]["citation"].(string), "[copia local]") || !strings.HasPrefix(results(m)[0]["link"].(string), w.srv.URL) {
		t.Fatalf("cita local: %s", raw)
	}
	_, raw, isErr = call(t, c, "nexo_get", map[string]any{"space": "arquitectura", "doc_key": "datos/tablas"})
	if isErr || !strings.Contains(raw, `"source": "local_copy"`) || !strings.Contains(raw, "VTA_CLIENTE") {
		t.Fatalf("get sin servidor: %s", raw)
	}
	_, raw, isErr = call(t, c, "nexo_propose", map[string]any{"space": "arquitectura", "title": "x", "content": "y", "rationale": "z"})
	if !isErr || !strings.Contains(raw, "no se realizó") {
		t.Fatalf("propuesta sin servidor: %s", raw)
	}
	w.srv.SetDown(false)

	// Al quitarle el acceso, el espacio desaparece de la copia local.
	if _, err := w.env.Roles.SetScopeMember(w.env.Ctx(), w.env.ID(w.tm.Ana), authz.SpaceScope("seguridad"), w.tm.Luis, authz.RoleNone); err != nil {
		t.Fatal(err)
	}
	rep, err = luis.SyncNow(context.Background())
	if err != nil || len(rep.SpacesRemoved) != 1 || rep.SpacesRemoved[0] != "seguridad" {
		t.Fatalf("acceso quitado: %+v %v", rep, err)
	}
	if r := local("tokens de integración caducan"); !r.NoResults {
		t.Fatalf("contenido del espacio quitado: %+v", r.Results)
	}

	// Propuesta por MCP y revisión del responsable.
	open := "proponente"
	if _, err := w.env.KB.UpdateSpace(w.env.Ctx(), w.env.ID(w.tm.Ana), "arquitectura", service.UpdateSpaceInput{DefaultRole: &open}); err != nil {
		t.Fatal(err)
	}
	m, raw, isErr = call(t, c, "nexo_propose", map[string]any{"space": "arquitectura", "title": "Trazas distribuidas",
		"content": "# Trazas\n\nTodo microservicio propaga traceparent (W3C Trace Context).\n", "rationale": "unificar trazas", "category": "lineamiento"})
	if isErr || m["status"] != domain.ProposalOpen {
		t.Fatalf("proponer: %s", raw)
	}
	pid := m["id"].(string)
	m, raw, _ = call(t, c, "nexo_proposals", map[string]any{"mine": true})
	if items, _ := m["items"].([]any); len(items) != 1 {
		t.Fatalf("mis propuestas: %s", raw)
	}
	ana := mcpFor(t, w.kbFor(t, w.tm.Ana), nil)
	_, raw, isErr = call(t, c, "nexo_review", map[string]any{"proposal_id": pid, "decision": "approve"})
	if !isErr || !strings.Contains(raw, "permiso") {
		t.Fatalf("un proponente no revisa: %s", raw)
	}
	_, raw, isErr = call(t, ana, "nexo_review", map[string]any{"proposal_id": pid, "decision": "approve", "note": "ok"})
	if isErr || !strings.Contains(raw, `"approved"`) {
		t.Fatalf("aprobar: %s", raw)
	}
	m, raw, _ = call(t, c, "nexo_search", map[string]any{"query": "traceparent", "mode": "exact"})
	if len(results(m)) == 0 {
		t.Fatalf("la aprobada no aparece: %s", raw)
	}

	// Espacios e identidad.
	m, raw, _ = call(t, c, "nexo_spaces", nil)
	if sp, _ := m["spaces"].([]any); len(sp) != 1 {
		t.Fatalf("espacios: %s", raw)
	}
	m, raw, _ = call(t, c, "nexo_whoami", nil)
	if id, _ := m["identity"].(map[string]any); id == nil || id["label"] != "luis" {
		t.Fatalf("whoami: %s", raw)
	}
}

func TestSyncDetectsNewSpaceAndIdentityChange(t *testing.T) {
	w := newWorld(t)
	marta := w.env.Principal("marta", authz.KindHuman, "member")
	kb := w.kbFor(t, marta)
	kb.Syncer.OverlapSeqs, kb.Syncer.Overlap = 0, 0 // sin solape: obliga a detectar el espacio nuevo
	rep, err := kb.SyncNow(context.Background())
	if err != nil || rep.Status.Spaces != 1 || rep.Status.Documents != 2 {
		t.Fatalf("marta sin seguridad: %+v %v", rep, err)
	}
	// Ana le da acceso a seguridad: sus documentos (con seq antiguos) llegan.
	if _, err := w.env.Roles.SetScopeMember(w.env.Ctx(), w.env.ID(w.tm.Ana), authz.SpaceScope("seguridad"), marta, authz.RoleLector); err != nil {
		t.Fatal(err)
	}
	rep, err = kb.SyncNow(context.Background())
	if err != nil || rep.Status.Spaces != 2 || rep.Status.Documents != 3 || len(rep.SpacesAdded) != 1 || !rep.FullResync || !strings.Contains(rep.Reason, "nuevos espacios") {
		t.Fatalf("nuevo espacio visible: %+v %v", rep, err)
	}
	// Otra persona con la misma copia: se reconstruye.
	kb.Syncer.API.Token = w.tm.Luis
	if rep, err = kb.SyncNow(context.Background()); err != nil || !strings.Contains(rep.Reason, "cambió el usuario") {
		t.Fatalf("cambio de usuario: %+v %v", rep, err)
	}
	// Token inválido: el servidor responde 401 (no es "sin conexión").
	kb.Syncer.API.Token = "no-existe"
	if _, err := kb.SyncNow(context.Background()); err == nil || client.Unreachable(err) || !client.IsStatus(err, 401) {
		t.Fatalf("401: %v", err)
	}
}
