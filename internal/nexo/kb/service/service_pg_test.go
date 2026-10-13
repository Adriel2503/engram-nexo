package service_test

// Pruebas de aceptación de la entrega 2a contra Postgres real
// (CLOUDSTORE_TEST_DSN): CA-K01..CA-K06, CA-K09, CA-K13, bitemporalidad
// (at, known_at), vigencia futura de una versión, idempotencia y búsqueda en
// español con y sin unaccent.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

const space = "lineamientos-frontend"

func readDoc(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "ingest", "testdata", "lineamientos-frontend", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func gitOrigin(path, commit string) domain.Origin {
	return domain.Origin{Kind: domain.OriginGit, Repo: "realplaza/lineamientos", Branch: "main", Commit: commit, Path: path,
		Author: "ana@rp.pe", Approver: "jefe@rp.pe", CIRun: "gh-123"}
}

// ciBatch es el lote del repositorio de lineamientos (3 documentos).
func ciBatch(t *testing.T, commit string) []service.PublishDoc {
	return []service.PublishDoc{
		{Path: "frontend/formularios.md", Content: readDoc(t, "frontend/formularios.md"), Origin: gitOrigin("frontend/formularios.md", commit)},
		{Path: "frontend/componentes.md", Content: readDoc(t, "frontend/componentes.md"), Origin: gitOrigin("frontend/componentes.md", commit)},
		{Path: "glosario.txt", Content: readDoc(t, "glosario.txt"), Category: "glosario", Origin: gitOrigin("glosario.txt", commit)},
	}
}

type fixture struct {
	env *kbtest.Env
	tm  kbtest.Team
}

// setup: CA-K01 parte 1. Un admin crea el espacio abierto con Ana como
// responsable y Ana nombra publicadora a la cuenta de servicio del CI.
func setup(t *testing.T, opts ...service.Option) fixture {
	t.Helper()
	env := kbtest.New(t, opts...)
	tm := env.NewTeam()
	sp, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: space, Name: "Lineamientos de frontend",
		Description: "Estándares y convenciones de frontend", Owner: "ana"})
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if sp.Visibility != authz.VisibilityOpen || sp.DefaultRole != string(authz.RoleLector) || len(sp.Categories) != 6 {
		t.Fatalf("espacio por defecto abierto con lector y 6 categorías: %+v", sp)
	}
	if _, err := env.Roles.SetScopeMember(env.Ctx(), env.ID(tm.Ana), authz.SpaceScope(space), tm.CI, authz.RolePublicador); err != nil {
		t.Fatalf("CI publicador: %v", err)
	}
	return fixture{env: env, tm: tm}
}

func (f fixture) publish(t *testing.T, who string, docs []service.PublishDoc, opts ...func(*service.BatchRequest)) service.BatchResult {
	t.Helper()
	req := service.BatchRequest{Documents: docs}
	for _, o := range opts {
		o(&req)
	}
	res, err := f.env.KB.PublishBatch(f.env.Ctx(), f.env.ID(who), space, req)
	if err != nil {
		t.Fatalf("PublishBatch: %v", err)
	}
	return res
}

func (f fixture) search(t *testing.T, who, q string, mod ...func(*service.SearchRequest)) search.Result {
	t.Helper()
	req := service.SearchRequest{Q: q}
	for _, m := range mod {
		m(&req)
	}
	res, err := f.env.KB.Search(f.env.Ctx(), f.env.ID(who), req)
	if err != nil {
		t.Fatalf("Search %q: %v", q, err)
	}
	if res.Dropped != 0 {
		t.Fatalf("pgfts no debe devolver resultados fuera de los filtros (descartados %d)", res.Dropped)
	}
	return res
}

func statuses(res service.BatchResult) string {
	var parts []string
	for _, r := range res.Results {
		parts = append(parts, r.DocKey+"="+r.Status+r.Reason)
	}
	return strings.Join(parts, ",")
}

func wantKind(t *testing.T, err error, k domain.Kind) {
	t.Helper()
	if !domain.IsKind(err, k) {
		t.Fatalf("esperaba error de clase %d, got %v", k, err)
	}
}

// ─── CA-K01 ──────────────────────────────────────────────────────────────────

func TestCAK01OpenSpaceVisibleToNonMembers(t *testing.T) {
	f := setup(t)
	env, tm := f.env, f.tm
	f.publish(t, tm.CI, ciBatch(t, "c1"))

	spaces, err := env.KB.ListSpaces(env.Ctx(), env.ID(tm.Luis))
	if err != nil {
		t.Fatal(err)
	}
	if len(spaces) != 1 || spaces[0].Slug != space || spaces[0].Role != string(authz.RoleLector) {
		t.Fatalf("Luis ve el espacio abierto como lector: %+v", spaces)
	}
	if strings.Join(spaces[0].Capabilities, ",") != "space.read" {
		t.Fatalf("capacidades de lector: %v", spaces[0].Capabilities)
	}
	res := f.search(t, tm.Luis, "validación de formularios")
	if res.NoResults || res.Hits[0].DocKey != "frontend/formularios" {
		t.Fatalf("Luis busca como lector: %+v", res)
	}
	h := res.Hits[0]
	if h.Space != space || h.Version != 1 || h.Category != "lineamiento" || h.Owner != "ana" || h.Anchor == "" || h.Excerpt == "" || h.Citation == "" {
		t.Fatalf("resultado citable incompleto (RF-21): %+v", h)
	}
	if !strings.Contains(h.Excerpt, "**") {
		t.Fatalf("extracto con resaltado de ts_headline: %q", h.Excerpt)
	}
	if h.SourceLocation["line_start"] == nil {
		t.Fatalf("procedencia en el resultado: %v", h.SourceLocation)
	}
	// Ana es responsable; el CI publicador; Luis lector.
	for who, role := range map[string]authz.Role{tm.Ana: authz.RoleResponsable, tm.CI: authz.RolePublicador} {
		sp, err := env.KB.GetSpace(env.Ctx(), env.ID(who), space)
		if err != nil || sp.Role != string(role) {
			t.Fatalf("rol de %s = %q, %v; want %s", who, sp.Role, err, role)
		}
	}
	// Un admin sin rol en el espacio no tiene contenido... pero lo ve abierto como lector.
	if sp, err := env.KB.GetSpace(env.Ctx(), env.ID(tm.Adm), space); err != nil || sp.Role != string(authz.RoleLector) {
		t.Fatalf("admin en espacio abierto: %+v %v", sp, err)
	}
	// Crear requiere platform.scope.create: Luis no puede.
	_, err = env.KB.CreateSpace(env.Ctx(), env.ID(tm.Luis), service.CreateSpaceInput{Slug: "otro-espacio", Owner: "ana"})
	wantKind(t, err, domain.KindForbidden)
	// Slug duplicado: 409.
	_, err = env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: space, Owner: "ana"})
	wantKind(t, err, domain.KindConflict)
	// La cuenta de servicio nunca es responsable (R-10).
	_, err = env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "ci-owner", Owner: tm.CI})
	wantKind(t, err, domain.KindInvalid)
}

// ─── CA-K02 ──────────────────────────────────────────────────────────────────

func TestCAK02RestrictedSpaceIsHidden(t *testing.T) {
	f := setup(t)
	env, tm := f.env, f.tm
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "arquitectura-privada", Visibility: "restricted", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	doc := service.PublishDoc{DocKey: "decisiones/pagos", Category: "estandar", Content: "# Pagos\n\nEl motor de pagos usa colas idempotentes con reintentos.\n"}
	if _, err := env.KB.PublishBatch(env.Ctx(), env.ID(tm.Ana), "arquitectura-privada", service.BatchRequest{Documents: []service.PublishDoc{doc}}); err != nil {
		t.Fatal(err)
	}
	// Ana sí lo encuentra.
	if res := f.search(t, tm.Ana, "colas idempotentes"); res.NoResults {
		t.Fatal("Ana (responsable) debe encontrar el documento restringido")
	}
	// Luis: 404 al pedirlo, ausente en listados y búsquedas, incluso pidiéndolo explícitamente.
	_, err := env.KB.GetSpace(env.Ctx(), env.ID(tm.Luis), "arquitectura-privada")
	wantKind(t, err, domain.KindNotFound)
	_, err = env.KB.GetDocument(env.Ctx(), env.ID(tm.Luis), "arquitectura-privada", "decisiones/pagos", 0)
	wantKind(t, err, domain.KindNotFound)
	_, err = env.KB.PublishBatch(env.Ctx(), env.ID(tm.Luis), "arquitectura-privada", service.BatchRequest{Documents: []service.PublishDoc{doc}})
	wantKind(t, err, domain.KindNotFound)
	// Un espacio inexistente responde igual (no se distingue).
	_, err = env.KB.GetSpace(env.Ctx(), env.ID(tm.Luis), "no-existe")
	wantKind(t, err, domain.KindNotFound)
	spaces, _ := env.KB.ListSpaces(env.Ctx(), env.ID(tm.Luis))
	for _, sp := range spaces {
		if sp.Slug == "arquitectura-privada" {
			t.Fatal("el espacio restringido no aparece en el listado de Luis")
		}
	}
	res := f.search(t, tm.Luis, "colas idempotentes", func(r *service.SearchRequest) { r.Spaces = []string{"arquitectura-privada"} })
	if !res.NoResults {
		t.Fatalf("Luis no encuentra contenido restringido: %+v", res.Hits)
	}
	// La denegación queda registrada como oculta.
	if n := env.Count(`SELECT COUNT(*) FROM nexo_kb_events WHERE action = 'authz.denied' AND space_slug = 'arquitectura-privada' AND actor_principal_id = $1::bigint AND details->>'hidden' = 'true'`, tm.Luis); n < 2 {
		t.Fatalf("denegaciones ocultas registradas = %d", n)
	}
}

// ─── CA-K03 y CA-K13 ─────────────────────────────────────────────────────────

func TestCAK03BatchIsIdempotent(t *testing.T) {
	f := setup(t)
	env, tm := f.env, f.tm
	res := f.publish(t, tm.CI, ciBatch(t, "c1"))
	if res.Summary["created"] != 3 {
		t.Fatalf("primer lote: %s", statuses(res))
	}
	versions := env.Count(`SELECT COUNT(*) FROM nexo_kb_versions`)
	events := env.Count(`SELECT COUNT(*) FROM nexo_kb_events`)
	changes := env.Count(`SELECT COUNT(*) FROM nexo_kb_changes`)
	sections := env.Count(`SELECT COUNT(*) FROM nexo_kb_sections`)

	again := f.publish(t, tm.CI, ciBatch(t, "c1-reintento"))
	if again.Summary["unchanged"] != 3 {
		t.Fatalf("reintento: %s", statuses(again))
	}
	if env.Count(`SELECT COUNT(*) FROM nexo_kb_versions`) != versions || env.Count(`SELECT COUNT(*) FROM nexo_kb_events`) != events ||
		env.Count(`SELECT COUNT(*) FROM nexo_kb_changes`) != changes || env.Count(`SELECT COUNT(*) FROM nexo_kb_sections`) != sections {
		t.Fatal("volver a publicar el mismo lote no crea versiones, eventos, cambios ni secciones (RF-11)")
	}
	// CA-K13: cada publicación tiene actor, token, IP y cliente.
	var actor, token, ip, ua, claim string
	var verified bool
	if err := env.DB.QueryRow(`SELECT actor_principal_id::text, actor_token_id::text, client_ip, user_agent, client_claim, actor_verified
		FROM nexo_kb_events WHERE action = 'document.published' AND target_id = 'frontend/formularios'`).Scan(&actor, &token, &ip, &ua, &claim, &verified); err != nil {
		t.Fatal(err)
	}
	if actor != tm.CI || token != env.Token(tm.CI) || ip == "" || ua != "nexo-kb-test/1.0" || claim == "" || !verified {
		t.Fatalf("evento sin actor completo: %s %s %s %s %s %v", actor, token, ip, ua, claim, verified)
	}
	if n := env.Count(`SELECT COUNT(*) FROM nexo_kb_events WHERE action = 'space.created' AND actor_principal_id = $1::bigint`, tm.Adm); n != 1 {
		t.Fatalf("evento space.created = %d", n)
	}
	// La versión guarda autor del token, origen y procedencia del archivo.
	var by, origin, mime, srcSHA string
	var size int64
	var blob *string
	if err := env.DB.QueryRow(`SELECT published_by_label, origin->>'commit', source_mime, source_sha256, source_size_bytes, source_blob_ref
		FROM nexo_kb_versions WHERE title = 'Formularios en aplicaciones web'`).Scan(&by, &origin, &mime, &srcSHA, &size, &blob); err != nil {
		t.Fatal(err)
	}
	if by != "ci-lineamientos" || origin != "c1" || mime != "text/markdown" || len(srcSHA) != 64 || size == 0 || blob != nil {
		t.Fatalf("versión: %s %s %s %s %d %v", by, origin, mime, srcSHA, size, blob)
	}
	// Front matter: id, categoría y responsable; el txt usa la categoría del pedido.
	docs, err := env.KB.ListDocuments(env.Ctx(), env.ID(tm.Luis), space, store.DocQuery{Limit: 10})
	if err != nil || len(docs) != 3 {
		t.Fatalf("documentos: %v %v", docs, err)
	}
	if docs[0].DocKey != "frontend/componentes" || docs[0].Category != "catalogo" || docs[2].DocKey != "glosario" || docs[2].Category != "glosario" {
		t.Fatalf("claves y categorías: %+v", docs)
	}
}

// ─── CA-K04 ──────────────────────────────────────────────────────────────────

func TestCAK04NewVersionReplacesTextAndKeepsHistory(t *testing.T) {
	f := setup(t)
	env, tm := f.env, f.tm
	f.publish(t, tm.CI, ciBatch(t, "c1"))
	t1 := time.Now().UTC()
	time.Sleep(20 * time.Millisecond)

	docs := ciBatch(t, "c2")
	docs[0].Content = strings.NewReplacer("React Hook Form con esquemas de Zod", "Formik con esquemas de Yup", "zod", "yup", "react-hook-form", "formik", "useForm", "useFormik").Replace(docs[0].Content)
	res := f.publish(t, tm.CI, docs)
	if res.Results[0].Status != "updated" || res.Results[0].Version != 2 || res.Summary["unchanged"] != 2 {
		t.Fatalf("segunda publicación: %s", statuses(res))
	}
	if r := f.search(t, tm.Luis, "Formik Yup"); r.NoResults || r.Hits[0].Version != 2 {
		t.Fatalf("la búsqueda devuelve el texto nuevo: %+v", r)
	}
	if r := f.search(t, tm.Luis, "Zod"); !r.NoResults {
		t.Fatalf("el texto anterior ya no está vigente: %+v", r.Hits)
	}
	// Bitemporal: lo que estaba publicado en t1 (known_at) era la versión 1.
	if r := f.search(t, tm.Luis, "Zod", func(q *service.SearchRequest) { q.KnownAt = &t1 }); r.NoResults || r.Hits[0].Version != 1 {
		t.Fatalf("known_at antes de v2 devuelve v1: %+v", r)
	}
	// El historial muestra ambas con autor y commit.
	versions, err := env.KB.ListVersions(env.Ctx(), env.ID(tm.Luis), space, "frontend/formularios", 0, 10)
	if err != nil || len(versions) != 2 {
		t.Fatalf("historial: %v %v", versions, err)
	}
	if versions[0].Version != 2 || versions[0].Origin.Commit != "c2" || versions[1].Origin.Commit != "c1" ||
		versions[0].PublishedBy != "ci-lineamientos" || versions[0].PublishedByID != tm.CI || versions[0].PublishedByTok != env.Token(tm.CI) {
		t.Fatalf("historial con autor y commit: %+v", versions)
	}
	// Leer la versión 1 completa y una sección de la versión 1.
	v1, err := env.KB.GetDocument(env.Ctx(), env.ID(tm.Luis), space, "frontend/formularios", 1)
	if err != nil || !strings.Contains(v1.Version.ContentMD, "Zod") || !strings.Contains(v1.Notice, "histórica") {
		t.Fatalf("versión 1: %v %q", err, v1.Notice)
	}
	sec, err := env.KB.GetSection(env.Ctx(), env.ID(tm.Luis), space, "frontend/formularios", "formularios-en-aplicaciones-web.validacion.libreria", 0)
	if err != nil || !strings.Contains(sec.Section.Body, "Formik") || sec.Version != 2 || !strings.Contains(sec.Citation, "v2") {
		t.Fatalf("sección vigente por ancla: %+v %v", sec, err)
	}
	if _, err := env.KB.GetSection(env.Ctx(), env.ID(tm.Luis), space, "frontend/formularios", "no-existe", 0); !domain.IsKind(err, domain.KindNotFound) {
		t.Fatalf("ancla inexistente: %v", err)
	}
}

// ─── CA-K05 ──────────────────────────────────────────────────────────────────

func TestCAK05PruneOnlyTouchesSameOrigin(t *testing.T) {
	f := setup(t)
	env, tm := f.env, f.tm
	f.publish(t, tm.CI, ciBatch(t, "c1"))
	// Ana sube a mano un documento al mismo espacio.
	manual := service.PublishDoc{DocKey: "manual/checklist-release", Category: "convencion", Content: "# Checklist de release\n\nRevisar accesibilidad y rendimiento.\n"}
	f.publish(t, tm.Ana, []service.PublishDoc{manual})

	// Se borra componentes.md del repositorio.
	batch := ciBatch(t, "c3")
	batch = append(batch[:1], batch[2])
	prune := func(total int, keys ...string) func(*service.BatchRequest) {
		return func(r *service.BatchRequest) {
			r.Prune = &service.PruneSpec{OriginKind: "git", OriginRef: "realplaza/lineamientos", Total: total, Keys: keys}
		}
	}
	// Lote parcial (el total no coincide): no se poda.
	res := f.publish(t, tm.CI, batch, prune(5))
	if res.PruneSkipped == "" || len(res.Pruned) != 0 {
		t.Fatalf("un lote parcial no poda: %+v", res)
	}
	// dry-run: informa sin escribir.
	events := env.Count(`SELECT COUNT(*) FROM nexo_kb_events`)
	res = f.publish(t, tm.CI, batch, prune(2), func(r *service.BatchRequest) { r.DryRun = true })
	if len(res.Pruned) != 1 || res.Pruned[0].DocKey != "frontend/componentes" || !res.DryRun {
		t.Fatalf("dry-run: %+v", res.Pruned)
	}
	if env.Count(`SELECT COUNT(*) FROM nexo_kb_events`) != events {
		t.Fatal("dry-run no escribe")
	}
	res = f.publish(t, tm.CI, batch, prune(2))
	if len(res.Pruned) != 1 || res.Pruned[0].Status != "retired" || res.Summary["retired"] != 1 {
		t.Fatalf("poda: %+v", res)
	}
	docs, _ := env.KB.ListDocuments(env.Ctx(), env.ID(tm.Luis), space, store.DocQuery{Limit: 10})
	got := map[string]string{}
	for _, d := range docs {
		got[d.DocKey] = d.Status
	}
	if got["frontend/componentes"] != "retired" || got["manual/checklist-release"] != "active" || got["frontend/formularios"] != "active" {
		t.Fatalf("estados tras la poda: %v", got)
	}
	if r := f.search(t, tm.Luis, "botón primario"); !r.NoResults {
		t.Fatalf("un documento podado no aparece como vigente: %+v", r.Hits)
	}
	// Poda paginada con keys: total = len(keys).
	res = f.publish(t, tm.CI, nil, prune(2, "frontend/formularios", "glosario"))
	if res.PruneSkipped != "" || len(res.Pruned) != 0 {
		t.Fatalf("poda con keys sin cambios: %+v", res)
	}
	if n := env.Count(`SELECT COUNT(*) FROM nexo_kb_events WHERE action = 'document.retired' AND details->>'reason' = 'prune'`); n != 1 {
		t.Fatalf("evento de poda = %d", n)
	}
}

// ─── CA-K06 ──────────────────────────────────────────────────────────────────

func TestCAK06ReaderCannotPublishAndIsAudited(t *testing.T) {
	f := setup(t)
	env, tm := f.env, f.tm
	_, err := env.KB.PublishBatch(env.Ctx(), env.ID(tm.Luis), space, service.BatchRequest{Documents: ciBatch(t, "x")})
	wantKind(t, err, domain.KindForbidden)
	_, err = env.KB.RetireDocument(env.Ctx(), env.ID(tm.Luis), space, "frontend/formularios", "")
	wantKind(t, err, domain.KindForbidden)
	_, err = env.KB.UpdateSpace(env.Ctx(), env.ID(tm.CI), space, service.UpdateSpaceInput{Name: ptr("x")})
	wantKind(t, err, domain.KindForbidden)
	_, err = env.KB.ListEvents(env.Ctx(), env.ID(tm.Luis), space, 0, "", 10)
	wantKind(t, err, domain.KindForbidden)
	if n := env.Count(`SELECT COUNT(*) FROM nexo_kb_events WHERE action = 'authz.denied' AND actor_principal_id = $1::bigint AND details->>'capability' = 'space.publish'`, tm.Luis); n != 2 {
		t.Fatalf("denegaciones de publicación de Luis = %d", n)
	}
	if n := env.Count(`SELECT COUNT(*) FROM nexo_authz_events WHERE action = 'authz.denied' AND actor_principal_id = $1::bigint AND scope_key = $2`, tm.Luis, space); n < 3 {
		t.Fatalf("también en nexo_authz_events = %d", n)
	}
	// El auditor de plataforma lee los eventos aunque no tenga rol en el espacio (RF-51).
	evs, err := env.KB.ListEvents(env.Ctx(), env.ID(tm.Aud), space, 0, "authz.denied", 50)
	if err != nil || len(evs) < 4 {
		t.Fatalf("auditor lee eventos: %d %v", len(evs), err)
	}
	if evs[0].ActorLabel == "" || evs[0].ClientIP == "" || evs[0].UserAgent == "" || evs[0].ClientClaim == "" {
		t.Fatalf("evento sin origen (CA-K13): %+v", evs[0])
	}
	// Token de solo lectura (NX-06): sin space.publish aunque el rol lo tenga.
	ro := env.ID(tm.CI)
	ro.TokenScope = authz.TokenScopeRead
	_, err = env.KB.PublishBatch(env.Ctx(), ro, space, service.BatchRequest{Documents: ciBatch(t, "x")})
	wantKind(t, err, domain.KindForbidden)
}

func ptr[T any](v T) *T { return &v }

// ─── CA-K09 y vigencia ───────────────────────────────────────────────────────

func TestCAK09FutureValidityAndRetirement(t *testing.T) {
	f := setup(t)
	env, tm := f.env, f.tm
	f.publish(t, tm.CI, ciBatch(t, "c1"))
	future := time.Now().UTC().AddDate(0, 1, 0).Truncate(time.Second)
	doc := service.PublishDoc{DocKey: "frontend/tipografia", Category: "estandar", ValidFrom: &future,
		Content: "# Tipografía\n\nLa fuente corporativa es Montserrat en titulares y Open Sans en cuerpo.\n"}
	f.publish(t, tm.Ana, []service.PublishDoc{doc})
	if r := f.search(t, tm.Luis, "Montserrat"); !r.NoResults || r.Message == "" {
		t.Fatalf("vigencia futura no aparece hoy y el aviso es explícito: %+v", r)
	}
	at := future.Add(24 * time.Hour)
	if r := f.search(t, tm.Luis, "Montserrat", func(q *service.SearchRequest) { q.At = &at }); r.NoResults {
		t.Fatal("con at= en esa fecha sí aparece")
	}
	view, err := env.KB.GetDocument(env.Ctx(), env.ID(tm.Luis), space, "frontend/tipografia", 0)
	if err != nil || !strings.Contains(view.Notice, "todavía no rige") {
		t.Fatalf("aviso de vigencia futura: %q %v", view.Notice, err)
	}

	// Reemplazo: componentes queda reemplazado por un documento nuevo.
	before := time.Now().UTC()
	time.Sleep(20 * time.Millisecond)
	nuevo := service.PublishDoc{DocKey: "frontend/sistema-de-diseno", Category: "catalogo", Content: "# Sistema de diseño\n\nUsar el paquete @realplaza/ds (antes @realplaza/ui).\n"}
	f.publish(t, tm.Ana, []service.PublishDoc{nuevo})
	if _, err := env.KB.SupersedeDocument(env.Ctx(), env.ID(tm.Ana), space, "frontend/componentes", "frontend/sistema-de-diseno", "migración al nuevo sistema de diseño"); err != nil {
		t.Fatal(err)
	}
	if r := f.search(t, tm.Luis, "botón primario"); !r.NoResults {
		t.Fatalf("un reemplazado nunca aparece como vigente: %+v", r.Hits)
	}
	view, err = env.KB.GetDocument(env.Ctx(), env.ID(tm.Luis), space, "frontend/componentes", 0)
	if err != nil || view.Document.Status != "retired" || view.Document.SupersededBy != "frontend/sistema-de-diseno" || !strings.Contains(view.Notice, "reemplazado por frontend/sistema-de-diseno") {
		t.Fatalf("el retirado indica su reemplazo (HU-08): %+v %v", view, err)
	}
	// Con at= anterior al retiro, lo que regía aparece, marcado como retirado hoy.
	r := f.search(t, tm.Luis, "botón primario", func(q *service.SearchRequest) { q.At = &before })
	if r.NoResults || r.Hits[0].DocStatus != "retired" || r.Hits[0].SupersededBy != "frontend/sistema-de-diseno" {
		t.Fatalf("at= anterior al retiro: %+v", r)
	}
	// Retirar dos veces: idempotente si es el mismo reemplazo; si no, 409.
	if _, err := env.KB.SupersedeDocument(env.Ctx(), env.ID(tm.Ana), space, "frontend/componentes", "frontend/sistema-de-diseno", ""); err != nil {
		t.Fatalf("reemplazo idempotente: %v", err)
	}
	_, err = env.KB.RetireDocument(env.Ctx(), env.ID(tm.Ana), space, "frontend/componentes", "")
	wantKind(t, err, domain.KindConflict)
	if n := env.Count(`SELECT COUNT(*) FROM nexo_kb_events WHERE action = 'document.superseded'`); n != 1 {
		t.Fatalf("eventos de reemplazo = %d", n)
	}
	// Volver a publicar un retirado lo reactiva con una versión nueva.
	res := f.publish(t, tm.CI, ciBatch(t, "c9")[1:2])
	if res.Results[0].Status != "updated" || res.Results[0].Version != 2 {
		t.Fatalf("reactivación: %s", statuses(res))
	}
	if r := f.search(t, tm.Luis, "botón primario"); r.NoResults {
		t.Fatal("reactivado vuelve a aparecer")
	}
}

func TestFutureVersionKeepsCurrentUntilItStarts(t *testing.T) {
	f := setup(t)
	env, tm := f.env, f.tm
	f.publish(t, tm.CI, ciBatch(t, "c1"))
	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	docs := ciBatch(t, "c2")
	docs[0].Content = strings.Replace(docs[0].Content, "valid_from: 2026-09-01", "valid_from: "+start.Format(time.RFC3339), 1)
	docs[0].Content = strings.NewReplacer("React Hook Form con esquemas de Zod", "TanStack Form con esquemas de Valibot", "zod", "valibot").Replace(docs[0].Content)
	res := f.publish(t, tm.CI, docs[:1])
	if res.Results[0].Version != 2 {
		t.Fatalf("v2 futura: %s", statuses(res))
	}
	// Hoy rige v1; desde start, v2.
	if r := f.search(t, tm.Luis, "Zod"); r.NoResults || r.Hits[0].Version != 1 {
		t.Fatalf("v1 sigue vigente hasta que v2 empiece: %+v", r)
	}
	if r := f.search(t, tm.Luis, "Valibot"); !r.NoResults {
		t.Fatal("v2 todavía no rige")
	}
	at := start.Add(time.Hour)
	if r := f.search(t, tm.Luis, "Zod", func(q *service.SearchRequest) { q.At = &at }); !r.NoResults {
		t.Fatal("desde start, v1 ya no rige")
	}
	if r := f.search(t, tm.Luis, "Valibot", func(q *service.SearchRequest) { q.At = &at }); r.NoResults || r.Hits[0].Version != 2 {
		t.Fatal("desde start rige v2")
	}
	_ = env
}

// ─── Espacios: configurar, archivar, restaurar ───────────────────────────────

func TestSpaceConfigureArchiveRestore(t *testing.T) {
	f := setup(t)
	env, tm := f.env, f.tm
	f.publish(t, tm.CI, ciBatch(t, "c1"))
	sp, err := env.KB.UpdateSpace(env.Ctx(), env.ID(tm.Ana), space, service.UpdateSpaceInput{
		Name: ptr("Frontend RP"), Categories: ptr([]string{"lineamiento", "catalogo", "glosario", "estandar", "convencion", "regla_negocio", "patron"}),
		DefaultRole: ptr("proponente")})
	if err != nil || sp.Name != "Frontend RP" || sp.DefaultRole != "proponente" || len(sp.Categories) != 7 {
		t.Fatalf("configurar: %+v %v", sp, err)
	}
	if luis, _ := env.KB.GetSpace(env.Ctx(), env.ID(tm.Luis), space); luis.Role != "proponente" {
		t.Fatalf("rol implícito proponente: %+v", luis)
	}
	// Categoría no permitida: rechazada por documento.
	res := f.publish(t, tm.CI, []service.PublishDoc{{DocKey: "x", Category: "inventada", Content: "# X\n\ntexto"}})
	if res.Results[0].Status != "rejected" || res.Results[0].Code != domain.CodeCategory {
		t.Fatalf("categoría: %+v", res.Results)
	}
	// Archivar: solo plataforma; desaparece de listados y búsquedas; se conserva.
	_, err = env.KB.SetArchived(env.Ctx(), env.ID(tm.Ana), space, true)
	wantKind(t, err, domain.KindForbidden)
	if _, err := env.KB.SetArchived(env.Ctx(), env.ID(tm.Adm), space, true); err != nil {
		t.Fatal(err)
	}
	if spaces, _ := env.KB.ListSpaces(env.Ctx(), env.ID(tm.Ana)); len(spaces) != 0 {
		t.Fatalf("archivado no aparece: %+v", spaces)
	}
	if r := f.search(t, tm.Ana, "validación"); !r.NoResults {
		t.Fatal("archivado no aparece en búsquedas")
	}
	_, err = env.KB.PublishBatch(env.Ctx(), env.ID(tm.CI), space, service.BatchRequest{Documents: ciBatch(t, "c2")})
	wantKind(t, err, domain.KindForbidden)
	// Los publicadores siguen leyendo eventos de un espacio archivado.
	if _, err := env.KB.ListEvents(env.Ctx(), env.ID(tm.Ana), space, 0, "", 10); err != nil {
		t.Fatalf("eventos de un archivado: %v", err)
	}
	if _, err := env.KB.SetArchived(env.Ctx(), env.ID(tm.Adm), space, false); err != nil {
		t.Fatal(err)
	}
	if r := f.search(t, tm.Ana, "validación"); r.NoResults {
		t.Fatal("restaurado vuelve con todo su contenido")
	}
	if n := env.Count(`SELECT COUNT(*) FROM nexo_kb_changes WHERE kind IN ('space_archived', 'space_restored')`); n != 2 {
		t.Fatalf("cambios de archivo = %d", n)
	}
	for _, a := range []string{"space.updated", "space.archived", "space.restored"} {
		if n := env.Count(`SELECT COUNT(*) FROM nexo_kb_events WHERE action = $1`, a); n < 1 {
			t.Fatalf("falta el evento %s", a)
		}
	}
}

// Un espacio creado por la API de roles (/nexo/authz/v1/scopes) obtiene su
// fila de conocimiento en el primer uso.
func TestSpaceCreatedThroughRolesAPIIsCompleted(t *testing.T) {
	env := kbtest.New(t)
	tm := env.NewTeam()
	if _, err := env.Roles.CreateScope(env.Ctx(), env.ID(tm.Adm), authz.CreateScopeInput{Type: authz.ScopeSpace, Key: "general", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	spaces, err := env.KB.ListSpaces(env.Ctx(), env.ID(tm.Luis))
	if err != nil || len(spaces) != 1 || spaces[0].Slug != "general" || len(spaces[0].Categories) != 6 {
		t.Fatalf("espacio completado: %+v %v", spaces, err)
	}
}

// ─── Validaciones de publicación ─────────────────────────────────────────────

func TestPublishValidations(t *testing.T) {
	f := setup(t)
	tm := f.tm
	secret := "# Config\n\nDATABASE_URL=postgres://app:Sup3rS3cret@db.interno:5432/ventas\n"
	res := f.publish(t, tm.Ana, []service.PublishDoc{
		{DocKey: "ok", Category: "convencion", Content: "# Ok\n\nTexto"},
		{DocKey: "secreto", Category: "convencion", Content: secret},
		{DocKey: "../fuera", Category: "convencion", Content: "# X\n\nx"},
		{DocKey: "vacio", Category: "convencion", Content: "   \n"},
		{DocKey: "ok", Category: "convencion", Content: "# Otro\n\nduplicado"},
		{Path: "informe.pdf", Category: "convencion", Content: "%PDF-1.7"},
		{DocKey: "fechas", Category: "convencion", Content: "---\nvalid_from: 2026-10-10\nvalid_until: 2026-01-01\n---\n# F\n\nx"},
	})
	want := []string{"created", "rejected", "rejected", "rejected", "rejected", "rejected", "rejected"}
	for i, r := range res.Results {
		if r.Status != want[i] {
			t.Fatalf("doc %d: %+v", i, r)
		}
	}
	if res.Results[1].Code != domain.CodeSecret || strings.Contains(res.Results[1].Reason, "Sup3rS3cret") {
		t.Fatalf("secreto rechazado sin repetir el valor: %+v", res.Results[1])
	}
	if res.Results[5].Code != domain.CodeUnsupported {
		t.Fatalf("PDF en K1: %+v", res.Results[5])
	}
}

// ─── Español con y sin unaccent ──────────────────────────────────────────────

func spanishSearchChecks(t *testing.T, f fixture) {
	t.Helper()
	tm := f.tm
	f.publish(t, tm.CI, ciBatch(t, "c1"))
	for _, q := range []string{"validacion", "validación", "VALIDACIONES", "formulario", "accesibilidad", "\"mensajes de error\"", "correo -zod"} {
		if r := f.search(t, tm.Luis, q); r.NoResults {
			t.Errorf("sin resultados para %q", q)
		}
	}
	if r := f.search(t, tm.Luis, "kubernetes helm"); !r.NoResults || r.Message != search.NoResultsMessage {
		t.Fatalf("no_results explícito (HU-06): %+v", r)
	}
	// Los títulos pesan más: la sección cuyo título es la consulta va primero.
	r := f.search(t, tm.Luis, "librería")
	if r.NoResults || r.Hits[0].Anchor != "formularios-en-aplicaciones-web.validacion.libreria" {
		t.Fatalf("peso de títulos: %+v", r.Hits)
	}
	// Filtros de categoría y etiqueta.
	if r := f.search(t, tm.Luis, "componentes", func(q *service.SearchRequest) { q.Category = "lineamiento" }); !r.NoResults {
		for _, h := range r.Hits {
			if h.Category != "lineamiento" {
				t.Fatalf("filtro de categoría: %+v", h)
			}
		}
	}
	if r := f.search(t, tm.Luis, "formularios", func(q *service.SearchRequest) { q.Tag = "accesibilidad" }); r.NoResults || r.Hits[0].DocKey != "frontend/formularios" {
		t.Fatalf("filtro de etiqueta: %+v", r)
	}
}

func TestSpanishSearchWithUnaccentWhenAvailable(t *testing.T) {
	f := setup(t)
	fold, err := f.env.KB.Store().FoldInApp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var cfg string
	_ = f.env.DB.QueryRow(`SELECT config->>'accent_folding' FROM nexo_kb_index_versions WHERE index_kind = 'pgfts' AND status = 'active'`).Scan(&cfg)
	t.Logf("modo de tildes: fold_in_app=%v, registrado=%s", fold, cfg)
	if (fold && cfg != "go-fold") || (!fold && cfg != "postgres-unaccent") {
		t.Fatalf("el modo registrado en nexo_kb_index_versions no coincide: %v %s", fold, cfg)
	}
	spanishSearchChecks(t, f)
}

func TestSpanishSearchWithoutUnaccentFoldsInGo(t *testing.T) {
	schema, dsn, admin := kbtest.NewSchema(t, "nexo_kb_nounacc")
	// Sin unaccent (forzado): la variante sin tildes se calcula en Go.
	t.Setenv(store.EnvDisableUnaccent, "1")
	env := kbtest.Open(t, schema, dsn, admin)
	if fold, err := env.KB.Store().FoldInApp(context.Background()); err != nil || !fold {
		t.Fatalf("modo sin unaccent: %v %v", fold, err)
	}
	tm := env.NewTeam()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: space, Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Roles.SetScopeMember(env.Ctx(), env.ID(tm.Ana), authz.SpaceScope(space), tm.CI, authz.RolePublicador); err != nil {
		t.Fatal(err)
	}
	spanishSearchChecks(t, fixture{env: env, tm: tm})
}

// ─── Migración ───────────────────────────────────────────────────────────────

func TestMigrationIsIdempotentAndCreatesEverything(t *testing.T) {
	schema, dsn, admin := kbtest.NewSchema(t, "nexo_kb_mig")
	env := kbtest.Open(t, schema, dsn, admin)
	_ = kbtest.Open(t, schema, dsn, admin) // segunda migración sobre el mismo esquema
	tx, err := env.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"nexo_kb_spaces", "nexo_kb_documents", "nexo_kb_versions", "nexo_kb_sections", "nexo_kb_events",
		"nexo_kb_changes", "nexo_kb_index_versions", "nexo_kb_proposals"} {
		if n := env.Count(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2`, schema, table); n != 1 {
			t.Fatalf("falta la tabla %s", table)
		}
	}
	for _, col := range []string{"source_location", "extraction", "recorded_from", "recorded_until", "is_current", "tsv", "valid_from", "valid_until"} {
		if n := env.Count(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'nexo_kb_sections' AND column_name = $2`, schema, col); n != 1 {
			t.Fatalf("falta nexo_kb_sections.%s", col)
		}
	}
	for _, col := range []string{"source_mime", "source_size_bytes", "source_blob_ref", "source_sha256"} {
		if n := env.Count(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'nexo_kb_versions' AND column_name = $2`, schema, col); n != 1 {
			t.Fatalf("falta nexo_kb_versions.%s", col)
		}
	}
	if n := env.Count(`SELECT COUNT(*) FROM nexo_kb_index_versions WHERE index_kind = 'pgfts'`); n != 1 {
		t.Fatalf("índice registrado %d veces", n)
	}
	for _, trig := range []string{"nexo_kb_versions_no_update_delete", "nexo_kb_versions_no_truncate", "nexo_kb_events_no_update_delete", "nexo_kb_events_no_truncate"} {
		if n := env.Count(`SELECT COUNT(*) FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE t.tgname = $1 AND n.nspname = $2`, trig, schema); n != 1 {
			t.Fatalf("falta el trigger %s", trig)
		}
	}
}
