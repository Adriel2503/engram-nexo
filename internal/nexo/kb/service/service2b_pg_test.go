package service_test

// Pruebas de la entrega 2b contra Postgres real: subida (HU-29), wiki de
// Azure DevOps (IN-08, KP-11), Word, tope de la poda (KP-01), registro de
// ejecuciones (KP-02), propuestas (C-07, KP-08, CA-K07, CA-K08), "qué cambió"
// (RC-27), feed /sync (C-09, KP-07), modos exact y umbral (RC-22, RC-07),
// vecinas y cita (RC-10, IN-07), reindexado (RC-03) y avisos (KP-09).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/source"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

func wikiFiles() []source.File {
	return []source.File{
		{Path: ".order", Data: []byte("Lineamientos\n")},
		{Path: "Lineamientos.md", Data: []byte("[[_TOC_]]\n")},
		{Path: "Lineamientos/.order", Data: []byte("Logging\nDatos\n")},
		{Path: "Lineamientos/Logging.md", Data: []byte("[[_TOC_]]\n# 1. Niveles\nUsar Information para eventos de negocio y Error para fallas.\n\n# 2. Correlación\nPropagar X-Correlation-Id en cada llamada.\n")},
		{Path: "Lineamientos/Datos.md", Data: []byte("# Tablas\nNombres de tabla en singular.\n\n<table><tr><th>Objeto</th><th>Prefijo</th></tr><tr><td>Vista</td><td>vw_</td></tr></table>\n")},
		{Path: "Lineamientos/[TO_REMOVE]/Viejo.md", Data: []byte("# Viejo\nUsar log4net para registrar.\n")},
		{Path: "Lineamientos/≤-2022/Java.md", Data: []byte("# Java\nUsar Application Insights en Java 8.\n")},
		{Path: ".attachments/diagrama.png"},
	}
}

func TestUploadWikiAndDocx(t *testing.T) {
	f := setup(t)
	env, tm := f.env, f.tm
	// Ana es responsable: publica.
	res, err := env.KB.Upload(env.Ctx(), env.ID(tm.Ana), space, service.UploadRequest{Files: wikiFiles(), Collection: "wiki", WikiURL: "https://dev.azure.com/rp/arq/_wiki/wikis/W"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != source.KindADO || res.Summary["created"] != 3 || res.Summary["retired"] != 1 || res.RunID == "" {
		t.Fatalf("subida: %+v", res)
	}
	byKey := map[string]service.DocResult{}
	for _, r := range res.Results {
		byKey[r.DocKey] = r
	}
	if r := byKey["lineamientos/2022/java"]; r.Status != "created" || !strings.Contains(r.Note, "expired") {
		t.Fatalf("≤-2022 vencido: %+v", r)
	}
	// Idempotente: volver a subir no crea versiones ni retira de nuevo.
	versions := env.Count(`SELECT count(*) FROM nexo_kb_versions`)
	again, err := env.KB.Upload(env.Ctx(), env.ID(tm.Ana), space, service.UploadRequest{Files: wikiFiles(), Collection: "wiki", WikiURL: "https://dev.azure.com/rp/arq/_wiki/wikis/W"})
	if err != nil || again.Summary["unchanged"] != 4 || env.Count(`SELECT count(*) FROM nexo_kb_versions`) != versions {
		t.Fatalf("reintento: %+v %v", again.Summary, err)
	}
	// La búsqueda encuentra la página con su título de archivo, ruta y enlaces.
	sr := f.search(t, tm.Luis, "correlación entre llamadas")
	if sr.NoResults || sr.Hits[0].DocKey != "lineamientos/logging" || sr.Hits[0].Anchor != "correlacion" || sr.Hits[0].Title != "Logging" {
		t.Fatalf("búsqueda: %+v", sr.Hits)
	}
	h := sr.Hits[0]
	if !strings.Contains(h.SourceURL, "pagePath=%2FLineamientos%2FLogging") || !strings.HasPrefix(h.Link, "/dashboard/kb/spaces/"+space+"/doc?doc=") {
		t.Fatalf("enlaces: %s %s", h.SourceURL, h.Link)
	}
	if q, _ := h.SourceLocation["quote"].(string); q == "" || len([]rune(q)) > 300 {
		t.Fatalf("cita textual: %v", h.SourceLocation)
	}
	// El retirado no aparece; el vencido solo con at en 2022.
	if r := f.search(t, tm.Luis, "log4net"); !r.NoResults {
		t.Fatalf("retirado visible: %+v", r.Hits)
	}
	at := time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC)
	if r := f.search(t, tm.Luis, "Application Insights Java"); !r.NoResults {
		t.Fatalf("vencido visible hoy: %+v", r.Hits)
	}
	if r := f.search(t, tm.Luis, "Application Insights Java", func(q *service.SearchRequest) { q.At = &at }); r.NoResults || r.Hits[0].DocKey != "lineamientos/2022/java" {
		t.Fatalf("vencido en 2022: %+v", r)
	}
	// Modo exact (RC-22): literal y expresión regular.
	if r := f.search(t, tm.Luis, "vw_", func(q *service.SearchRequest) { q.Mode = "exact" }); r.NoResults || r.Hits[0].DocKey != "lineamientos/datos" {
		t.Fatalf("exact: %+v", r)
	}
	if r := f.search(t, tm.Luis, "/x-correlation-[a-z]+/", func(q *service.SearchRequest) { q.Mode = "exact" }); r.NoResults || r.Hits[0].Match != "exact" {
		t.Fatalf("regex: %+v", r)
	}
	if _, err := env.KB.Search(env.Ctx(), env.ID(tm.Luis), service.SearchRequest{Q: "/.*/", Mode: "exact"}); !domain.IsKind(err, domain.KindInvalid) {
		t.Fatalf("regex sin literal: %v", err)
	}
	// Vecinas (RC-10).
	if r := f.search(t, tm.Luis, "Information eventos de negocio", func(q *service.SearchRequest) { q.Neighbors = true }); len(r.Hits[0].Neighbors) != 1 || r.Hits[0].Neighbors[0].Position != "next" {
		t.Fatalf("vecinas: %+v", r.Hits[0].Neighbors)
	}
	// Umbral (RC-07): pregunta sin estándar con una palabra en común.
	if r := f.search(t, tm.Luis, "convención de nombres de tópicos Kafka"); !r.NoResults || r.Relevance == nil {
		t.Fatalf("no hay estándar: %+v", r)
	}
	// Word (.docx) suelto.
	docx, err := os.ReadFile(filepath.Join("..", "ingest", "docx", "testdata", "lineamiento-ejemplo.docx"))
	if err != nil {
		t.Fatal(err)
	}
	dres, err := env.KB.Upload(env.Ctx(), env.ID(tm.Ana), space, service.UploadRequest{Files: []source.File{{Path: "Lineamiento de Logging.docx", Data: docx}}, Collection: "word"})
	if err != nil || dres.Summary["created"] != 1 {
		t.Fatalf("docx: %+v %v", dres, err)
	}
	if r := f.search(t, tm.Luis, "nunca registrar tokens ni contraseñas en el log"); r.NoResults || r.Hits[0].DocKey != "lineamiento-de-logging" {
		t.Fatalf("búsqueda en docx: %+v", r.Hits)
	}
	// Un lector no sube (403) y la denegación queda auditada.
	if _, err := env.KB.Upload(env.Ctx(), env.ID(tm.Luis), space, service.UploadRequest{Files: wikiFiles()}); !domain.IsKind(err, domain.KindForbidden) {
		t.Fatalf("lector sube: %v", err)
	}
	// Token read: no sube aunque el rol lo permita.
	ro := env.ID(tm.Ana)
	ro.TokenScope = authz.TokenScopeRead
	if _, err := env.KB.Upload(env.Ctx(), ro, space, service.UploadRequest{Files: wikiFiles()}); !domain.IsKind(err, domain.KindForbidden) {
		t.Fatalf("token read sube: %v", err)
	}
	// KP-02: ejecuciones registradas (lote, subidas).
	runs, err := env.KB.ListPublishRuns(env.Ctx(), env.ID(tm.Ana), space, nil, 20)
	if err != nil || len(runs) != 3 || runs[0].Kind != "upload" {
		t.Fatalf("ejecuciones: %+v %v", runs, err)
	}
	if _, err := env.KB.ListPublishRuns(env.Ctx(), env.ID(tm.Luis), space, nil, 20); !domain.IsKind(err, domain.KindForbidden) {
		t.Fatalf("lector ve ejecuciones: %v", err)
	}
}

func TestPruneCapAndUploadPrune(t *testing.T) {
	f := setup(t)
	env, tm := f.env, f.tm
	t.Setenv(service.EnvPruneMinDocs, "1")
	var files []source.File
	for i := 0; i < 8; i++ {
		files = append(files, source.File{Path: "doc" + string(rune('a'+i)) + ".md", Data: []byte("# Doc\nContenido " + string(rune('a'+i)) + ".\n")})
	}
	if _, err := env.KB.Upload(env.Ctx(), env.ID(tm.Ana), space, service.UploadRequest{Files: files, Collection: "c"}); err != nil {
		t.Fatal(err)
	}
	// Subir solo 2 con poda: retiraría 6 de 8 (75 %): se detiene.
	res, err := env.KB.Upload(env.Ctx(), env.ID(tm.Ana), space, service.UploadRequest{Files: files[:2], Collection: "c", Prune: true})
	if err != nil || !strings.HasPrefix(res.PruneSkipped, domain.CodePruneCap) || env.Count(`SELECT count(*) FROM nexo_kb_documents WHERE status = 'retired'`) != 0 {
		t.Fatalf("tope: %q %v", res.PruneSkipped, err)
	}
	// Con confirmación, poda.
	res, err = env.KB.Upload(env.Ctx(), env.ID(tm.Ana), space, service.UploadRequest{Files: files[:2], Collection: "c", Prune: true, Confirm: true})
	if err != nil || len(res.Pruned) != 6 || env.Count(`SELECT count(*) FROM nexo_kb_documents WHERE status = 'retired'`) != 6 {
		t.Fatalf("poda confirmada: %+v %v", res.Pruned, err)
	}
}

func TestProposalsFlow(t *testing.T) {
	f := setup(t)
	env, tm := f.env, f.tm
	f.publish(t, tm.CI, ciBatch(t, "c1"))
	// CA-K07: Ana pone el rol implícito en proponente.
	role := "proponente"
	if _, err := env.KB.UpdateSpace(env.Ctx(), env.ID(tm.Ana), space, service.UpdateSpaceInput{DefaultRole: &role}); err != nil {
		t.Fatal(err)
	}
	other := env.Principal("marta", authz.KindHuman, "member")
	p, err := env.KB.CreateProposal(env.Ctx(), env.ID(tm.Luis), space, service.ProposalInput{DocKey: "frontend/formularios", Title: "Validar RUC",
		Content: "# Formularios\n\n## Validación\n\nValidar también el RUC con 11 dígitos.\n", Rationale: "lo pide el negocio"})
	if err != nil || p.Status != "open" || p.BaseVersion != 1 {
		t.Fatalf("propuesta: %+v %v", p, err)
	}
	if _, err := env.KB.CreateProposal(env.Ctx(), env.ID(tm.Luis), space, service.ProposalInput{Title: "x", Content: "token ghp_" + strings.Repeat("a", 36)}); !domain.IsKind(err, domain.KindInvalid) {
		t.Fatalf("secreto en propuesta: %v", err)
	}
	// No aparece en búsquedas; otro proponente no la ve (404) ni la lista.
	if r := f.search(t, tm.Luis, "RUC 11 dígitos"); !r.NoResults {
		t.Fatal("una propuesta no aparece en búsquedas (RF-31)")
	}
	if _, err := env.KB.GetProposal(env.Ctx(), env.ID(other), p.ID); !domain.IsKind(err, domain.KindNotFound) {
		t.Fatalf("propuesta ajena visible: %v", err)
	}
	if l, _ := env.KB.ListProposals(env.Ctx(), env.ID(other), service.ProposalQuery{}); len(l) != 0 {
		t.Fatalf("listado ajeno: %+v", l)
	}
	if l, _ := env.KB.ListProposals(env.Ctx(), env.ID(tm.Ana), service.ProposalQuery{Space: space}); len(l) != 1 {
		t.Fatalf("revisora: %+v", l)
	}
	// Un proponente no aprueba.
	if _, err := env.KB.ApproveProposal(env.Ctx(), env.ID(tm.Luis), p.ID, service.ApproveInput{}); err == nil {
		t.Fatal("un proponente no aprueba")
	}
	// Conflicto: el documento cambia después de la propuesta.
	f.publish(t, tm.CI, ciBatch(t, "c2")[:1], func(r *service.BatchRequest) {
		r.Documents[0].Content = strings.Replace(r.Documents[0].Content, "Validación", "Validación de datos", 1)
	})
	if _, err := env.KB.ApproveProposal(env.Ctx(), env.ID(tm.Ana), p.ID, service.ApproveInput{}); !domain.IsKind(err, domain.KindConflict) {
		t.Fatalf("conflicto de versión: %v", err)
	}
	// KP-08: con fuerza y fecha de entrada en vigencia futura.
	from := time.Now().UTC().AddDate(0, 0, 10)
	out, err := env.KB.ApproveProposal(env.Ctx(), env.ID(tm.Ana), p.ID, service.ApproveInput{Force: true, ValidFrom: &from, Note: "aprobado"})
	if err != nil || out.Proposal.Status != "approved" || out.Result.Version != 3 || out.Proposal.ResultVersionID == "" {
		t.Fatalf("aprobación: %+v %v", out, err)
	}
	var origin string
	_ = env.DB.QueryRow(`SELECT origin::text FROM nexo_kb_versions WHERE id = $1::uuid`, out.Proposal.ResultVersionID).Scan(&origin)
	if !strings.Contains(origin, `"kind": "proposal"`) || !strings.Contains(origin, `"author": "luis"`) || !strings.Contains(origin, `"approver": "ana"`) {
		t.Fatalf("origen propuesta con autor y aprobador: %s", origin)
	}
	// Programada: hoy no rige; en su fecha sí.
	if r := f.search(t, tm.Luis, "RUC 11 dígitos"); !r.NoResults {
		t.Fatal("la versión programada no rige todavía")
	}
	at := from.Add(time.Hour)
	if r := f.search(t, tm.Luis, "RUC 11 dígitos", func(q *service.SearchRequest) { q.At = &at }); r.NoResults || r.Hits[0].Version != 3 {
		t.Fatalf("programada en su fecha: %+v", r)
	}
	if _, err := env.KB.ApproveProposal(env.Ctx(), env.ID(tm.Ana), p.ID, service.ApproveInput{}); !domain.IsKind(err, domain.KindConflict) {
		t.Fatalf("aprobar dos veces: %v", err)
	}
	// CA-K08: descarte con motivo, visible a su autor.
	p2, _ := env.KB.CreateProposal(env.Ctx(), env.ID(tm.Luis), space, service.ProposalInput{Title: "Usar jQuery", Content: "# jQuery\nUsar jQuery.\n"})
	if _, err := env.KB.DiscardProposal(env.Ctx(), env.ID(tm.Ana), p2.ID, ""); !domain.IsKind(err, domain.KindInvalid) {
		t.Fatalf("descarte sin motivo: %v", err)
	}
	d, err := env.KB.DiscardProposal(env.Ctx(), env.ID(tm.Ana), p2.ID, "no usamos jQuery")
	if err != nil || d.Status != "discarded" {
		t.Fatal(err)
	}
	if mine, _ := env.KB.GetProposal(env.Ctx(), env.ID(tm.Luis), p2.ID); mine.ReviewNote != "no usamos jQuery" {
		t.Fatalf("el autor ve el motivo: %+v", mine)
	}
	// Retiro por el autor.
	p3, _ := env.KB.CreateProposal(env.Ctx(), env.ID(tm.Luis), space, service.ProposalInput{Title: "Borrador", Content: "# B\nb.\n"})
	if _, err := env.KB.WithdrawProposal(env.Ctx(), env.ID(tm.Ana), p3.ID, ""); !domain.IsKind(err, domain.KindForbidden) {
		t.Fatalf("solo el autor retira: %v", err)
	}
	if w, err := env.KB.WithdrawProposal(env.Ctx(), env.ID(tm.Luis), p3.ID, "ya no"); err != nil || w.Status != "withdrawn" {
		t.Fatal(err)
	}
	if n := env.Count(`SELECT count(*) FROM nexo_kb_events WHERE action LIKE 'proposal.%'`); n != 6 {
		t.Fatalf("eventos de propuestas: %d", n)
	}
}

func TestChangesAndSyncFeed(t *testing.T) {
	f := setup(t)
	env, tm := f.env, f.tm
	f.publish(t, tm.CI, ciBatch(t, "c1"))
	since := time.Now().UTC()
	time.Sleep(50 * time.Millisecond)
	// Seguimiento: cambia una sección, agrega otra y retira un documento.
	docs := ciBatch(t, "c2")
	docs[0].Content = strings.Replace(docs[0].Content, "## Accesibilidad", "## Accesibilidad\n\nTexto nuevo de accesibilidad.\n\n## Seguridad nueva\n\nSanitizar entradas.\n\n## Accesibilidad bis", 1)
	if r := f.publish(t, tm.CI, docs[:1]); r.Summary["updated"] != 1 {
		t.Fatalf("versión 2: %s", statuses(r))
	}
	if _, err := env.KB.RetireDocument(env.Ctx(), env.ID(tm.CI), space, "glosario", "obsoleto"); err != nil {
		t.Fatal(err)
	}
	// formularios declara valid_from 2026-09-01: con el reloj de vigencia la
	// versión 2 rige desde entonces (no es un cambio entre since y ahora); con
	// el reloj de registro, sí (se publicó después de since).
	if v, _ := env.KB.Changes(env.Ctx(), env.ID(tm.Luis), service.ChangesRequest{Since: since}); v.Summary["added"] != 0 || v.Summary["removed"] != 1 {
		t.Fatalf("reloj de vigencia: %+v", v.Summary)
	}
	ch, err := env.KB.Changes(env.Ctx(), env.ID(tm.Luis), service.ChangesRequest{Since: since, Clock: "recorded"})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Summary["added"] == 0 || ch.Summary["removed"] == 0 || ch.NoChanges {
		t.Fatalf("cambios: %+v", ch)
	}
	var sawGlossary bool
	for _, c := range ch.Changes {
		if c.DocKey == "glosario" && c.Change == "removed" {
			sawGlossary = true
		}
	}
	if !sawGlossary {
		t.Fatalf("retirado como removed: %+v", ch.Changes)
	}
	if _, err := env.KB.Changes(env.Ctx(), env.ID(tm.Luis), service.ChangesRequest{Since: time.Now().Add(time.Hour)}); !domain.IsKind(err, domain.KindInvalid) {
		t.Fatalf("since futuro: %v", err)
	}
	// Feed: espacios visibles, documentos con su versión vigente y las
	// programadas con su vigencia; el retirado como remove.
	future := time.Now().UTC().AddDate(0, 1, 0)
	sched := ciBatch(t, "c3")[1:2]
	sched[0].ValidFrom = &future
	sched[0].Content += "\n\nRegla futura.\n"
	f.publish(t, tm.CI, sched)
	feed, err := env.KB.Sync(env.Ctx(), env.ID(tm.Luis), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.VisibleSpaces) != 1 || feed.VisibleSpaces[0].Slug != space || feed.Seq == 0 || feed.HasMore {
		t.Fatalf("feed: %+v", feed)
	}
	kinds := map[string]string{}
	for _, c := range feed.Changes {
		kinds[c.DocKey] = c.Kind
		if c.DocKey == "frontend/componentes" {
			versions := map[int]bool{}
			for _, s := range c.Document.Sections {
				versions[s.Version] = true
				if s.Version == 2 && !s.ValidFrom.After(time.Now()) {
					t.Fatal("la programada lleva su vigencia futura")
				}
			}
			if !versions[1] || !versions[2] {
				t.Fatalf("vigente y programada: %v", versions)
			}
		}
	}
	if kinds["glosario"] != domain.ChangeRemove || kinds["frontend/formularios"] != domain.ChangeUpsert {
		t.Fatalf("clases: %v", kinds)
	}
	// Incremental sin cambios.
	again, err := env.KB.Sync(env.Ctx(), env.ID(tm.Luis), feed.Seq, 0)
	if err != nil || len(again.Changes) != 0 || again.Seq != feed.Seq {
		t.Fatalf("incremental: %+v %v", again, err)
	}
	if r, _ := env.KB.Sync(env.Ctx(), env.ID(tm.Luis), feed.Seq+1000, 0); !r.ResyncRequired {
		t.Fatal("seq del futuro: resync_required")
	}
	// Un espacio restringido sin rol no aparece en el feed (CA-K11).
	vis := "restricted"
	if _, err := env.KB.UpdateSpace(env.Ctx(), env.ID(tm.Ana), space, service.UpdateSpaceInput{Visibility: &vis}); err != nil {
		t.Fatal(err)
	}
	if r, _ := env.KB.Sync(env.Ctx(), env.ID(tm.Luis), 0, 0); len(r.VisibleSpaces) != 0 || len(r.Changes) != 0 {
		t.Fatalf("restringido: %+v", r)
	}
}

func TestReindexAndAlerts(t *testing.T) {
	f := setup(t, service.WithR1(service.R1Options{}))
	env, tm := f.env, f.tm
	f.publish(t, tm.CI, ciBatch(t, "c1"))
	// Sin contexto del documento: el título del archivo no está indexado.
	before := f.search(t, tm.Luis, "Componentes del sistema")
	// Activar RC-03 y reindexar (superadmin).
	env.KB.ApplyR1(service.R1Options{DocContext: true})
	st, err := env.KB.IndexState(env.Ctx(), env.ID(tm.Root))
	if err != nil {
		t.Fatal(err)
	}
	if !st.NeedsReindex {
		t.Fatalf("estado: %+v", st)
	}
	if _, err := env.KB.Reindex(env.Ctx(), env.ID(tm.Adm)); !domain.IsKind(err, domain.KindForbidden) {
		t.Fatalf("un admin no reindexa: %v", err)
	}
	res, err := env.KB.Reindex(env.Ctx(), env.ID(tm.Root))
	if err != nil || res.Sections == 0 || res.State.NeedsReindex || res.State.ActiveVersion != res.Version {
		t.Fatalf("reindexado: %+v %v", res, err)
	}
	if env.Count(`SELECT count(*) FROM nexo_kb_index_versions WHERE status = 'retired'`) != 1 {
		t.Fatal("la versión anterior queda retirada")
	}
	after := f.search(t, tm.Luis, "Componentes del sistema")
	if after.NoResults || (!before.NoResults && after.Hits[0].Rank <= 0) {
		t.Fatalf("búsqueda tras reindexar: %+v", after)
	}
	// KP-09: vencimiento cercano y revisión vencida.
	soon := time.Now().UTC().AddDate(0, 0, 5)
	docs := ciBatch(t, "c2")[2:]
	docs[0].ValidUntil = &soon
	f.publish(t, tm.CI, docs)
	if _, err := env.KB.UpdateSpace(env.Ctx(), env.ID(tm.Ana), space, service.UpdateSpaceInput{Settings: &map[string]any{"review_every_days": 1}}); err != nil {
		t.Fatal(err)
	}
	env.Exec(`ALTER TABLE nexo_kb_versions DISABLE TRIGGER nexo_kb_versions_no_update_delete`)
	env.Exec(`UPDATE nexo_kb_versions SET published_at = published_at - interval '3 days' WHERE title <> 'Glosario'`)
	env.Exec(`ALTER TABLE nexo_kb_versions ENABLE TRIGGER nexo_kb_versions_no_update_delete`)
	alerts, ran, err := evidence.EvaluateOnce(context.Background(), env.DB, evidence.DefaultRules(), time.Now())
	if err != nil || !ran {
		t.Fatalf("evaluador: %v %v", ran, err)
	}
	rules := map[string]int{}
	for _, a := range alerts {
		if a.Layer == evidence.LayerKnowledge {
			rules[a.Rule]++
		}
	}
	if rules[evidence.RuleKBDocExpiring] != 1 || rules[evidence.RuleKBReviewDue] < 1 {
		t.Fatalf("avisos: %v", rules)
	}
	again, _, _ := evidence.EvaluateOnce(context.Background(), env.DB, evidence.DefaultRules(), time.Now())
	for _, a := range again {
		if a.Layer == evidence.LayerKnowledge {
			t.Fatalf("aviso repetido: %+v", a)
		}
	}
}
