package service

// Nexo IA · NX-10 R2 — búsqueda por significado (design §20): proveedor de
// embeddings por configuración (RC-12), versión de índice vectorial con
// cambio atómico (RC-16), cola de vectorización en Postgres con trabajador en
// segundo plano (RC-20), modo hybrid con RRF y umbral recalibrado (RC-11,
// RC-07) y un solo modelo por consulta (RC-23).
//
// R2 es una mejora opcional que se degrada: con NEXO_KB_EMBEDDER=off (por
// defecto) o sin pgvector, el servicio es exactamente el de R1 (motor pgfts,
// sin trabajador). La copia local de la PC sigue solo con BM25 (RC-30).

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/embed"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// Variables de entorno de R2 (además de las de embed: NEXO_KB_EMBEDDER y
// NEXO_KB_EMBED_*).
const (
	EnvRRFK              = "NEXO_KB_RRF_K"
	EnvRRFVectorWeight   = "NEXO_KB_RRF_VECTOR_WEIGHT"
	EnvRRFTextWeight     = "NEXO_KB_RRF_TEXT_WEIGHT"
	EnvMinSimilarity     = "NEXO_KB_MIN_SIMILARITY"
	EnvMinTextSimilarity = "NEXO_KB_MIN_TEXT_SIMILARITY"
	EnvVectorExactMax    = "NEXO_KB_VECTOR_EXACT_MAX"
	EnvVectorCandidates  = "NEXO_KB_VECTOR_CANDIDATES"
	EnvVectorIterative   = "NEXO_KB_VECTOR_ITERATIVE_SCAN"
	EnvVectorWorker      = "NEXO_KB_VECTOR_WORKER"
	EnvVectorBatch       = "NEXO_KB_VECTOR_BATCH"
	EnvVectorPoll        = "NEXO_KB_VECTOR_POLL_SECONDS"
)

// R2Options son los parámetros del modo hybrid y del indexador.
type R2Options struct {
	Hybrid search.HybridOptions `json:"hybrid"`
	// Umbral del modo hybrid (search.SearcherOptions).
	MinSimilarity     float64 `json:"min_similarity"`
	MinTextSimilarity float64 `json:"min_text_similarity"`
	// Indexador.
	Worker bool          `json:"worker"`
	Batch  int           `json:"batch"`
	Poll   time.Duration `json:"poll"`
}

// DefaultR2 son los valores por defecto: pesos 0,7/0,3 y umbral medido con
// R0 sobre la wiki con gemini-embedding-001 a 768 dimensiones (design
// §20.6). Con otro modelo hay que recalibrar NEXO_KB_MIN_SIMILARITY.
var DefaultR2 = R2Options{Hybrid: search.DefaultHybridOptions, MinSimilarity: 0.70, MinTextSimilarity: 0.65, Worker: true, Batch: 100, Poll: 5 * time.Second}

// R2FromEnv lee los parámetros de R2 del entorno.
func R2FromEnv() R2Options {
	o := DefaultR2
	f := func(k string, lo, hi float64, dst *float64) {
		if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(k)), 64); err == nil && v >= lo && v <= hi {
			*dst = v
		}
	}
	i := func(k string, lo, hi int, dst *int) {
		if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(k))); err == nil && v >= lo && v <= hi {
			*dst = v
		}
	}
	i(EnvRRFK, 1, 1000, &o.Hybrid.Weights.K)
	f(EnvRRFVectorWeight, 0, 10, &o.Hybrid.Weights.Vector)
	f(EnvRRFTextWeight, 0, 10, &o.Hybrid.Weights.Text)
	f(EnvMinSimilarity, 0, 1, &o.MinSimilarity)
	f(EnvMinTextSimilarity, 0, 1, &o.MinTextSimilarity)
	i(EnvVectorExactMax, 0, 10_000_000, &o.Hybrid.Vector.ExactMax)
	i(EnvVectorCandidates, 10, 1000, &o.Hybrid.Vector.Candidates)
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvVectorIterative))); v {
	case "relaxed_order", "strict_order", "off":
		o.Hybrid.Vector.Iterative = v
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvVectorWorker))) {
	case "off", "0", "false", "no":
		o.Worker = false
	}
	i(EnvVectorBatch, 1, 1000, &o.Batch)
	secs := 0
	i(EnvVectorPoll, 1, 3600, &secs)
	if secs > 0 {
		o.Poll = time.Duration(secs) * time.Second
	}
	return o
}

// WithEmbedder fija el Embedder y su configuración (pruebas con un embedder
// simulado; en el servidor sale de NEXO_KB_EMBEDDER).
func WithEmbedder(e embed.Embedder, c embed.Config) Option {
	return func(s *Service) { s.emb, s.embCfg, s.embSet = e, c, true }
}

// WithR2 fija los parámetros de R2 (en lugar de los del entorno).
func WithR2(o R2Options) Option { return func(s *Service) { s.r2 = o; s.r2Set = true } }

// vectorState es el estado de R2 del servicio.
type vectorState struct {
	mu        sync.Mutex
	embedders map[string]embed.Embedder // firma -> Embedder (índice activo de otro modelo)
	active    *store.VectorIndex
	activeAt  time.Time
	ensured   bool
}

// setupR2 lee la configuración y, con un proveedor, monta el motor híbrido.
func (s *Service) setupR2() {
	if !s.r2Set {
		s.r2 = R2FromEnv()
	}
	if !s.embSet {
		c, err := embed.ConfigFromEnv()
		if err != nil {
			log.Printf("[nexo] kb: configuración de embeddings inválida (%v); búsqueda solo por texto", err)
			return
		}
		if !c.Enabled() {
			return
		}
		e, err := embed.New(c)
		if err != nil {
			log.Printf("[nexo] kb: %v; búsqueda solo por texto", err)
			return
		}
		s.emb, s.embCfg = e, c
	}
	if s.emb == nil {
		return
	}
	text, ok := s.searcher.Retriever().(*search.PGFTS)
	if !ok {
		return // motor reemplazado (pruebas): R2 no lo envuelve
	}
	s.hybrid = search.NewHybrid(text, s.st, s.resolveVector, s.r2.Hybrid)
	s.searcher = search.NewSearcher(s.hybrid)
	s.vec = &vectorState{embedders: map[string]embed.Embedder{}}
}

// VectorEnabled informa si hay un proveedor de embeddings configurado.
func (s *Service) VectorEnabled() bool { return s.hybrid != nil }

// R2 devuelve los parámetros de R2 en uso.
func (s *Service) R2() R2Options { return s.r2 }

// ApplyR2 cambia los pesos y umbrales del modo hybrid (medición de R0).
func (s *Service) ApplyR2(o R2Options) {
	s.r2 = o
	if s.hybrid != nil {
		s.hybrid.SetOptions(o.Hybrid)
	}
	s.ApplyR1(s.r1)
}

// resolveVector devuelve el índice activo y el Embedder de su modelo.
func (s *Service) resolveVector(ctx context.Context) (search.VectorBackend, string, bool) {
	vs, err := s.st.VectorSupport(ctx)
	if err != nil || !vs.Available {
		return search.VectorBackend{}, "la base no tiene la extensión pgvector", false
	}
	st := s.vec
	st.mu.Lock()
	active, fresh := st.active, time.Since(st.activeAt) < 10*time.Second
	st.mu.Unlock()
	if !fresh {
		vi, ok, err := s.st.ActiveVectorIndex(ctx)
		if err != nil {
			return search.VectorBackend{}, "no se pudo leer el índice vectorial", false
		}
		st.mu.Lock()
		st.active, st.activeAt = nil, time.Now()
		if ok {
			st.active = &vi
		}
		active = st.active
		st.mu.Unlock()
	}
	if active == nil {
		return search.VectorBackend{}, "el índice vectorial todavía se está construyendo", false
	}
	e, err := s.embedderFor(*active)
	if err != nil {
		return search.VectorBackend{}, err.Error(), false
	}
	return search.VectorBackend{Index: *active, Embedder: e, Support: vs}, "", true
}

// embedderFor devuelve el Embedder del modelo de una versión de índice: el
// configurado si la firma coincide; si no, uno del mismo proveedor con el
// modelo de la versión (transición entre modelos). Otro proveedor: no hay
// credenciales, la versión no se puede consultar.
func (s *Service) embedderFor(vi store.VectorIndex) (embed.Embedder, error) {
	if vi.Signature == store.VectorSignature(s.embCfg) {
		return s.emb, nil
	}
	if s.embSet || vi.Provider != s.embCfg.Provider {
		return nil, fmt.Errorf("la versión activa del índice vectorial usa %s %s y el servidor está configurado con %s %s; se usará la nueva al terminar de construirse",
			vi.Provider, vi.Model, s.embCfg.Provider, s.embCfg.Model)
	}
	s.vec.mu.Lock()
	defer s.vec.mu.Unlock()
	if e, ok := s.vec.embedders[vi.Signature]; ok {
		return e, nil
	}
	c := s.embCfg
	c.Model, c.Dims = vi.Model, vi.Dims
	c.QueryPrefix, _ = vi.Config["query_prefix"].(string)
	c.DocPrefix, _ = vi.Config["doc_prefix"].(string)
	e, err := embed.New(c)
	if err != nil {
		return nil, err
	}
	s.vec.embedders[vi.Signature] = e
	return e, nil
}

// ─── Indexador (RC-20) ───────────────────────────────────────────────────────

// EnsureVectorIndex registra la versión de índice de la configuración en
// uso si no existe (y encola todo). Sin proveedor o sin pgvector no hace
// nada.
func (s *Service) EnsureVectorIndex(ctx context.Context) (store.VectorIndex, bool, error) {
	if s.hybrid == nil {
		return store.VectorIndex{}, false, nil
	}
	vs, err := s.st.VectorSupport(ctx)
	if err != nil || !vs.Available {
		return store.VectorIndex{}, false, err
	}
	vi, created, err := s.st.EnsureVectorIndex(ctx, s.embCfg, false)
	if err == nil && created {
		logf("[nexo] kb: índice vectorial v%d en construcción (%s %s, %d dimensiones)", vi.Version, vi.Provider, vi.Model, vi.Dims)
	}
	return vi, created, err
}

// VectorDrain es el resultado de una pasada del indexador.
type VectorDrain struct {
	Embedded  int   `json:"embedded"`
	Reused    int   `json:"reused"`
	Failed    int   `json:"failed"`
	Dead      int   `json:"dead"`
	Enqueued  int64 `json:"enqueued"`
	Activated []int `json:"activated,omitempty"`
	Purged    int64 `json:"purged"`
	Calls     int   `json:"provider_calls"`
}

// errProviderDown corta la pasada: el proveedor no responde.
var errProviderDown = errors.New("proveedor de embeddings no disponible")

// DrainVectors procesa la cola hasta vaciarla (o hasta que el proveedor no
// responda o se acabe el contexto): encola los cambios nuevos, vectoriza por
// lotes, activa la versión en construcción si terminó y descarta el trabajo
// de versiones retiradas. Lo usan el trabajador y las pruebas.
func (s *Service) DrainVectors(ctx context.Context) (VectorDrain, error) {
	var res VectorDrain
	if s.hybrid == nil {
		return res, nil
	}
	if _, _, err := s.EnsureVectorIndex(ctx); err != nil {
		return res, err
	}
	vis, err := s.st.VectorIndexes(ctx)
	if err != nil {
		return res, err
	}
	worker := workerID()
	for _, vi := range vis {
		n, err := s.st.EnqueueChanges(ctx, vi.Version)
		if err != nil {
			return res, err
		}
		res.Enqueued += n
		e, err := s.embedderFor(vi)
		if err != nil {
			continue // otro proveedor: su trabajo se descarta al retirarse
		}
		for ctx.Err() == nil {
			jobs, err := s.st.ClaimVectorJobs(ctx, vi.Version, max(s.r2.Batch, 1), worker, 10*time.Minute)
			if err != nil {
				return res, err
			}
			if len(jobs) == 0 {
				break
			}
			if err := s.vectorizeJobs(ctx, vi, e, jobs, &res); err != nil {
				if errors.Is(err, errProviderDown) {
					return res, err
				}
				return res, err
			}
		}
		if vi.Status == "building" && ctx.Err() == nil {
			ok, err := s.st.TryActivateVectorIndex(ctx, vi.Version)
			if err != nil {
				return res, err
			}
			if ok {
				res.Activated = append(res.Activated, vi.Version)
				logf("[nexo] kb: índice vectorial v%d activo (%s %s)", vi.Version, vi.Provider, vi.Model)
				s.vec.mu.Lock()
				s.vec.activeAt = time.Time{}
				s.vec.mu.Unlock()
			}
		}
	}
	n, err := s.st.PurgeRetiredVectors(ctx, 5000)
	res.Purged = n
	return res, err
}

// vectorizeJobs resuelve un lote: reutiliza vectores de textos idénticos,
// vectoriza el resto (cada texto distinto una vez) y guarda.
func (s *Service) vectorizeJobs(ctx context.Context, vi store.VectorIndex, e embed.Embedder, jobs []store.VectorJob, res *VectorDrain) error {
	done, err := s.st.ReuseVectors(ctx, vi.Version, jobs)
	if err != nil {
		return err
	}
	var rest []store.VectorJob
	for _, j := range jobs {
		if done[j.ID] {
			res.Reused++
		} else {
			rest = append(rest, j)
		}
	}
	if len(rest) == 0 {
		return nil
	}
	idx := map[string]int{}
	var texts []string
	for _, j := range rest {
		if _, ok := idx[j.TextHash]; !ok {
			idx[j.TextHash] = len(texts)
			texts = append(texts, j.Text)
		}
	}
	res.Calls += (len(texts) + 99) / 100
	vecs, err := e.Embed(ctx, texts, embed.KindDocument)
	if err != nil {
		ids := jobIDs(rest)
		if embed.IsTransient(err) || ctx.Err() != nil {
			_ = s.st.ReleaseVectorJobs(context.WithoutCancel(ctx), ids)
			log.Printf("[nexo] kb: vectorización en pausa (%v)", err)
			return fmt.Errorf("%w: %v", errProviderDown, err)
		}
		// Error permanente: si el lote tiene varios textos, se aísla el que
		// falla vectorizando de a uno.
		if len(texts) > 1 {
			for _, j := range rest {
				if err := s.vectorizeJobs(ctx, vi, e, []store.VectorJob{j}, res); err != nil {
					return err
				}
			}
			return nil
		}
		dead, ferr := s.st.FailVectorJobs(ctx, ids, err.Error(), 5)
		res.Failed += len(ids)
		res.Dead += dead
		return ferr
	}
	out := make([][]float32, len(rest))
	for i, j := range rest {
		out[i] = vecs[idx[j.TextHash]]
	}
	if err := s.st.SaveVectors(ctx, vi.Version, rest, out); err != nil {
		return err
	}
	res.Embedded += len(rest)
	return nil
}

func jobIDs(jobs []store.VectorJob) []int64 {
	ids := make([]int64, len(jobs))
	for i, j := range jobs {
		ids[i] = j.ID
	}
	return ids
}

func workerID() string {
	h, _ := os.Hostname()
	return fmt.Sprintf("%s:%d", h, os.Getpid())
}

// RunVectorWorker atiende la cola en segundo plano hasta que ctx termine
// (engram cloud serve). Cada NEXO_KB_VECTOR_POLL_SECONDS encola los cambios
// y vectoriza; si el proveedor no responde espera cada vez más (hasta 5
// minutos). Nunca entra en pánico ni detiene el servidor.
func (s *Service) RunVectorWorker(ctx context.Context) {
	if s.hybrid == nil || !s.r2.Worker {
		return
	}
	logf("[nexo] kb: indexador de vectores en marcha (%s %s, %d dimensiones)", s.embCfg.Provider, s.embCfg.Model, s.embCfg.Dims)
	wait := s.r2.Poll
	for {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("[nexo] kb: indexador de vectores: panic recuperado: %v", rec)
				}
			}()
			res, err := s.DrainVectors(ctx)
			switch {
			case err == nil:
				wait = s.r2.Poll
				if res.Embedded+res.Reused+res.Failed > 0 {
					logf("[nexo] kb: vectores: %d calculados, %d reutilizados, %d fallidos (%d a cartas muertas)", res.Embedded, res.Reused, res.Failed, res.Dead)
				}
			case errors.Is(err, errProviderDown):
				wait = min(max(wait*2, 30*time.Second), 5*time.Minute)
			case ctx.Err() != nil:
			default:
				log.Printf("[nexo] kb: indexador de vectores: %v", err)
				wait = min(max(wait*2, 30*time.Second), 5*time.Minute)
			}
		}()
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// ─── Administración (platform.settings) ──────────────────────────────────────

// VectorIndexState es el estado del índice vectorial.
type VectorIndexState struct {
	Enabled  bool                     `json:"enabled"`
	Embedder map[string]any           `json:"embedder,omitempty"`
	Support  store.VectorSupport      `json:"pgvector"`
	Versions []store.VectorQueueStats `json:"versions"`
	Options  R2Options                `json:"options"`
	Reason   string                   `json:"reason,omitempty"`
}

// VectorState informa el estado del índice vectorial (platform.settings).
func (s *Service) VectorState(ctx context.Context, id authz.Identity) (VectorIndexState, error) {
	if err := s.requirePlatform(ctx, id, authz.CapPlatformSettings, "index.vector.state"); err != nil {
		return VectorIndexState{}, err
	}
	return s.vectorState(ctx)
}

func (s *Service) vectorState(ctx context.Context) (VectorIndexState, error) {
	st := VectorIndexState{Enabled: s.hybrid != nil, Options: s.r2, Versions: []store.VectorQueueStats{}}
	if s.hybrid == nil {
		st.Reason = "NEXO_KB_EMBEDDER no configurado: búsqueda solo por texto"
		return st, nil
	}
	st.Embedder = s.embCfg.Describe()
	vs, err := s.st.VectorSupport(ctx)
	if err != nil {
		return st, err
	}
	st.Support = vs
	if !vs.Available {
		st.Reason = "la base no tiene la extensión pgvector: búsqueda solo por texto"
		return st, nil
	}
	st.Versions, err = s.st.VectorStats(ctx)
	return st, err
}

// ReindexVectors crea una versión nueva del índice vectorial con la
// configuración en uso y encola todas las secciones; la búsqueda sigue con
// la versión activa hasta que la nueva termine (RC-16). Con requeueDead
// vuelve a encolar las cartas muertas de la versión indicada (o de la
// activa) en lugar de reconstruir.
func (s *Service) ReindexVectors(ctx context.Context, id authz.Identity, requeueDead bool, version int) (VectorIndexState, error) {
	if err := s.requirePlatform(ctx, id, authz.CapPlatformSettings, "index.vector.reindex"); err != nil {
		return VectorIndexState{}, err
	}
	if s.hybrid == nil {
		return s.vectorState(ctx)
	}
	vs, err := s.st.VectorSupport(ctx)
	if err != nil || !vs.Available {
		st, _ := s.vectorState(ctx)
		return st, err
	}
	if requeueDead {
		if version <= 0 {
			if vi, ok, err := s.st.ActiveVectorIndex(ctx); err == nil && ok {
				version = vi.Version
			}
		}
		n, err := s.st.RequeueDeadVectors(ctx, version)
		if err != nil {
			return VectorIndexState{}, err
		}
		logf("[nexo] kb: %d cartas muertas de v%d vueltas a la cola", n, version)
	} else {
		vi, _, err := s.st.EnsureVectorIndex(ctx, s.embCfg, true)
		if err != nil {
			return VectorIndexState{}, err
		}
		logf("[nexo] kb: reconstrucción del índice vectorial v%d pedida", vi.Version)
	}
	return s.vectorState(ctx)
}
