package service

// Nexo IA · NX-10 entrega 2b — opciones de R1 (recuperación sin modelos,
// design §17.3 y §17.5). Cada mejora es desactivable por configuración y se
// mide con R0 (eval) antes de quedar activa por defecto:
//
//	doc_context   RC-03  título del documento y ruta de carpetas en el texto indexado (exige reindexar)
//	split_tables  RC-04  tablas grandes partidas por filas con la cabecera repetida (al publicar)
//	synonyms      RC-06  sinónimos y siglas (diccionario base ES/EN y glosario del espacio)
//	threshold     RC-07  umbral de cobertura para responder "no hay estándar oficial"
//	diversity     RC-09  máximo de secciones por documento
//	quote         IN-07  cita textual (source_location.quote, 300 caracteres)
//
// NEXO_KB_R1: vacío = valores por defecto (DefaultR1), "off" = ninguna, "all"
// = todas, o una lista separada por comas. NEXO_KB_MIN_COVERAGE (0,3 por
// defecto) y NEXO_KB_MAX_PER_DOC (2) ajustan el umbral y la diversidad. RC-10
// (secciones vecinas) y RC-22 (modo exact) se piden por consulta.

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// Variables de entorno de R1.
const (
	EnvR1          = "NEXO_KB_R1"
	EnvMinCoverage = "NEXO_KB_MIN_COVERAGE"
	EnvMaxPerDoc   = "NEXO_KB_MAX_PER_DOC"
)

// R1Options son las mejoras de R1 activas.
type R1Options struct {
	DocContext  bool    `json:"doc_context"`
	SplitTables bool    `json:"split_tables"`
	Synonyms    bool    `json:"synonyms"`
	Threshold   bool    `json:"threshold"`
	Diversity   bool    `json:"diversity"`
	Quote       bool    `json:"quote"`
	MinCoverage float64 `json:"min_coverage"`
	MaxPerDoc   int     `json:"max_per_doc"`
}

// DefaultR1 son las mejoras activas por defecto (las que la medición de R0
// mostró útiles; ver docs/nexo/conocimiento/tasks.md).
var DefaultR1 = R1Options{DocContext: true, SplitTables: true, Synonyms: true, Threshold: true, Diversity: true, Quote: true, MinCoverage: 0.3, MaxPerDoc: 2}

// R1FromEnv lee las opciones de R1 del entorno.
func R1FromEnv() R1Options {
	o := DefaultR1
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(EnvR1)))
	switch raw {
	case "":
	case "off", "none", "0":
		o = R1Options{MinCoverage: DefaultR1.MinCoverage, MaxPerDoc: DefaultR1.MaxPerDoc}
	case "all":
		o.DocContext, o.SplitTables, o.Synonyms, o.Threshold, o.Diversity, o.Quote = true, true, true, true, true, true
	default:
		o = R1Options{MinCoverage: DefaultR1.MinCoverage, MaxPerDoc: DefaultR1.MaxPerDoc}
		for _, p := range strings.Split(raw, ",") {
			switch strings.TrimSpace(p) {
			case "doc_context":
				o.DocContext = true
			case "split_tables":
				o.SplitTables = true
			case "synonyms":
				o.Synonyms = true
			case "threshold":
				o.Threshold = true
			case "diversity":
				o.Diversity = true
			case "quote":
				o.Quote = true
			}
		}
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(EnvMinCoverage)), 64); err == nil && v >= 0 && v <= 1 {
		o.MinCoverage = v
	}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(EnvMaxPerDoc))); err == nil && n >= 0 {
		o.MaxPerDoc = n
	}
	return o
}

func applyR1FromEnv(s *Service) { s.ApplyR1(R1FromEnv()) }

// ApplyR1 aplica las opciones de R1 al servicio.
func (s *Service) ApplyR1(o R1Options) {
	s.r1 = o
	s.st.SetIndexOptions(store.IndexOptions{DocContext: o.DocContext})
	s.sections.SplitTables = o.SplitTables
	so := search.SearcherOptions{}
	if o.Threshold {
		so.MinCoverage = o.MinCoverage
	}
	if o.Diversity {
		so.MaxPerDoc = o.MaxPerDoc
	}
	s.searcher.SetOptions(so)
	if p, ok := s.searcher.Retriever().(*search.PGFTS); ok {
		po := search.PGFTSOptions{Coverage: o.Threshold, Quote: o.Quote}
		if o.Synonyms {
			po.Synonyms = s.synonyms
		}
		p.SetOptions(po)
	}
}

// R1 devuelve las opciones de R1 en uso.
func (s *Service) R1() R1Options { return s.r1 }

// ─── Sinónimos (RC-06) ───────────────────────────────────────────────────────

var baseSynonyms = search.NewSynonyms(search.BaseSynonyms)

type synCacheEntry struct {
	at  time.Time
	syn search.Synonyms
}

var (
	synMu    sync.Mutex
	synCache = map[string]synCacheEntry{}
)

// synonyms: diccionario base más el glosario de los espacios (documentos
// vigentes de categoría glosario), en caché 5 minutos por conjunto de
// espacios.
func (s *Service) synonyms(ctx context.Context, spaceIDs []string) map[string][]string {
	ids := append([]string(nil), spaceIDs...)
	sort.Strings(ids)
	key := strings.Join(ids, ",")
	synMu.Lock()
	if e, ok := synCache[key]; ok && time.Since(e.at) < 5*time.Minute {
		synMu.Unlock()
		return e.syn
	}
	synMu.Unlock()
	syn := search.Synonyms{}
	for k, v := range baseSynonyms {
		syn[k] = append([]string(nil), v...)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT s.body FROM nexo_kb_sections s WHERE s.space_id = ANY($1::uuid[]) AND s.is_current
		AND s.category = 'glosario' AND s.valid_from <= now() AND (s.valid_until IS NULL OR s.valid_until > now()) LIMIT 2000`, ids)
	if err == nil {
		for rows.Next() {
			var body string
			if rows.Scan(&body) == nil {
				for _, g := range search.ParseGlossary(body) {
					syn.AddGroup(g)
				}
			}
		}
		_ = rows.Close()
	}
	synMu.Lock()
	if len(synCache) > 1000 {
		synCache = map[string]synCacheEntry{}
	}
	synCache[key] = synCacheEntry{at: time.Now(), syn: syn}
	synMu.Unlock()
	return syn
}

// ─── Reindexado (RC-03, design §14.3) ────────────────────────────────────────

// ReindexResult es el resultado de un reindexado.
type ReindexResult struct {
	Version   int              `json:"version"`
	Documents int              `json:"documents_batches"`
	Sections  int              `json:"sections"`
	State     store.IndexState `json:"state"`
}

// IndexState informa si el índice necesita reindexarse (platform.settings).
func (s *Service) IndexState(ctx context.Context, id authz.Identity) (store.IndexState, error) {
	if err := s.requirePlatform(ctx, id, authz.CapPlatformSettings, "index.state"); err != nil {
		return store.IndexState{}, err
	}
	return s.st.GetIndexState(ctx)
}

// Reindex recalcula el texto indexado de todas las secciones con las
// opciones en uso (platform.settings: superadmin). Seguro con el servidor en
// marcha: lotes de 50 documentos en transacciones cortas.
func (s *Service) Reindex(ctx context.Context, id authz.Identity) (ReindexResult, error) {
	if err := s.requirePlatform(ctx, id, authz.CapPlatformSettings, "index.reindex"); err != nil {
		return ReindexResult{}, err
	}
	version, err := s.st.BeginReindex(ctx)
	if err != nil {
		return ReindexResult{}, err
	}
	res := ReindexResult{Version: version}
	after := ""
	for {
		last, n, err := s.st.ReindexDocuments(ctx, after, 50)
		if err != nil {
			return res, err
		}
		if last == "" || last == after {
			break
		}
		res.Documents++
		res.Sections += n
		after = last
	}
	if err := s.st.FinishReindex(ctx, version); err != nil {
		return res, err
	}
	res.State, err = s.st.GetIndexState(ctx)
	logf("[nexo] kb: reindexado pgfts v%d: %d secciones", version, res.Sections)
	return res, err
}

// requirePlatform exige una capacidad de plataforma (denegación auditada).
func (s *Service) requirePlatform(ctx context.Context, id authz.Identity, c authz.Capability, action string) error {
	d, err := s.eng.Require(ctx, id, authz.PlatformScope(), c, map[string]any{"action": action})
	if err != nil {
		return err
	}
	if !d.Allowed {
		return domain.Errorf(domain.KindForbidden, domain.CodeForbidden, "%s is required", c)
	}
	return nil
}
