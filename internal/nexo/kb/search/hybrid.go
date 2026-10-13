package search

// Nexo IA · NX-10 R2 (RC-11, RC-23, design §20) — modo hybrid: la misma
// consulta, con los mismos filtros, por texto (pgfts con R1) y por vectores
// (pgvector), fusionada con RRF. El vector de la pregunta se calcula una sola
// vez con el modelo de la versión de índice activa (nunca se mezclan
// modelos). Si no hay índice activo o el proveedor no responde, la respuesta
// es solo por texto y lo dice (Result.Vector, Result.Notice), sin error.

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/embed"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// ModeHybrid es el modo texto + vectores.
const ModeHybrid = "hybrid"

// MatchVector marca un resultado que solo encontró la búsqueda por vectores.
const MatchVector = "semantic"

// Estados de la búsqueda por significado en una consulta.
const (
	VectorUsed        = "used"
	VectorUnavailable = "unavailable"
	VectorFailed      = "failed"
)

// VectorInfo informa cómo participó la búsqueda por significado.
type VectorInfo struct {
	Status       string      `json:"status"`
	Reason       string      `json:"reason,omitempty"`
	IndexVersion int         `json:"index_version,omitempty"`
	Provider     string      `json:"provider,omitempty"`
	Model        string      `json:"model,omitempty"`
	Dims         int         `json:"dims,omitempty"`
	Weights      *RRFWeights `json:"weights,omitempty"`
	Scan         string      `json:"scan,omitempty"` // hnsw | exact
	// IndexLag: secciones de los espacios consultados que esperan su vector
	// (se encuentran solo por texto mientras tanto).
	IndexLag int64 `json:"index_lag,omitempty"`
}

// DefaultModer elige el modo cuando la consulta no lo pide.
type DefaultModer interface {
	DefaultMode(ctx context.Context) string
}

// InfoRetriever es un Retriever que informa el estado de la búsqueda por
// significado.
type InfoRetriever interface {
	SearchInfo(ctx context.Context, q Query, f Filter) ([]Hit, *VectorInfo, error)
}

// VectorResolver devuelve el índice activo y su Embedder; si no hay, el
// motivo (para el aviso de la respuesta).
type VectorResolver func(ctx context.Context) (VectorBackend, string, bool)

// HybridOptions son los parámetros del modo hybrid.
type HybridOptions struct {
	Weights RRFWeights    `json:"weights"`
	Vector  VectorOptions `json:"vector"`
	// QueryTimeout acota el cálculo del vector de la pregunta (incluidos los
	// reintentos): pasado ese tiempo se responde solo por texto.
	QueryTimeout time.Duration `json:"query_timeout"`
}

// DefaultHybridOptions: pesos 0,7 vector y 0,3 texto con k = 60 (los mejores
// en la medición de R0 sobre la wiki, design §20.6).
var DefaultHybridOptions = HybridOptions{Weights: RRFWeights{K: 60, Vector: 0.7, Text: 0.3}, Vector: DefaultVectorOptions, QueryTimeout: 6 * time.Second}

// Hybrid combina PGFTS y PGVector.
type Hybrid struct {
	text    *PGFTS
	vec     *PGVector
	st      *store.Store
	resolve VectorResolver
	opts    HybridOptions

	mu    sync.Mutex
	cache map[string]cachedVec
}

type cachedVec struct {
	at  time.Time
	vec []float32
}

// NewHybrid crea el motor híbrido sobre el de texto.
func NewHybrid(text *PGFTS, st *store.Store, resolve VectorResolver, o HybridOptions) *Hybrid {
	return &Hybrid{text: text, vec: &PGVector{st: st, opts: o.Vector}, st: st, resolve: resolve, opts: o, cache: map[string]cachedVec{}}
}

// Text devuelve el motor de texto (opciones de R1).
func (h *Hybrid) Text() *PGFTS { return h.text }

// SetOptions cambia los parámetros (antes de atender consultas).
func (h *Hybrid) SetOptions(o HybridOptions) {
	h.opts = o
	h.vec.opts = o.Vector
}

// Options devuelve los parámetros en uso.
func (h *Hybrid) Options() HybridOptions { return h.opts }

// Name implementa Retriever.
func (h *Hybrid) Name() string { return ModeHybrid }

// Config describe la configuración (corridas de evaluación).
func (h *Hybrid) Config() map[string]any {
	cfg := h.text.Config()
	cfg["hybrid"] = h.opts
	return cfg
}

// DefaultMode: hybrid si hay índice vectorial activo; si no, fts.
func (h *Hybrid) DefaultMode(ctx context.Context) string {
	if _, _, ok := h.resolve(ctx); ok {
		return ModeHybrid
	}
	return ModeFTS
}

// Search implementa Retriever.
func (h *Hybrid) Search(ctx context.Context, q Query, f Filter) ([]Hit, error) {
	hits, _, err := h.SearchInfo(ctx, q, f)
	return hits, err
}

// SearchInfo implementa InfoRetriever.
func (h *Hybrid) SearchInfo(ctx context.Context, q Query, f Filter) ([]Hit, *VectorInfo, error) {
	if q.Mode != ModeHybrid {
		hits, err := h.text.Search(ctx, q, f)
		return hits, nil, err
	}
	b, reason, ok := h.resolve(ctx)
	if !ok {
		hits, err := h.text.Search(ctx, q, f)
		return hits, &VectorInfo{Status: VectorUnavailable, Reason: reason}, err
	}
	info := &VectorInfo{Status: VectorUsed, IndexVersion: b.Index.Version, Provider: b.Index.Provider, Model: b.Index.Model, Dims: b.Index.Dims}
	w := h.opts.Weights
	info.Weights = &w
	qvec, err := h.queryVector(ctx, b, q.Text)
	if err != nil {
		log.Printf("[nexo] kb: vector de la consulta no disponible (%s %s): %v", b.Index.Provider, b.Index.Model, err)
		hits, terr := h.text.Search(ctx, q, f)
		return hits, &VectorInfo{Status: VectorFailed, Reason: "el proveedor de embeddings no respondió a tiempo", IndexVersion: b.Index.Version, Provider: b.Index.Provider, Model: b.Index.Model}, terr
	}
	// Texto y vectores en paralelo, con los mismos filtros.
	var (
		wg      sync.WaitGroup
		vHits   []vecHit
		exact   bool
		vErr    error
		tHits   []Hit
		tq      *textQuery
		tErr    error
		lag     int64
		lagDone bool
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		vHits, exact, vErr = h.vec.search(ctx, b, qvec, q, f, q.Limit)
		if vErr == nil {
			if n, err := h.st.PendingVectorJobs(ctx, b.Index.Version, f.SpaceIDs()); err == nil {
				lag, lagDone = n, true
			}
		}
	}()
	go func() {
		defer wg.Done()
		tHits, tq, tErr = h.text.search(ctx, q, f)
	}()
	wg.Wait()
	if tErr != nil {
		return nil, nil, tErr
	}
	if vErr != nil {
		log.Printf("[nexo] kb: búsqueda vectorial fallida, se responde por texto: %v", vErr)
		return tHits, &VectorInfo{Status: VectorFailed, Reason: "la búsqueda por significado falló en la base", IndexVersion: b.Index.Version, Provider: b.Index.Provider, Model: b.Index.Model}, nil
	}
	if lagDone {
		info.IndexLag = lag
	}
	info.Scan = "hnsw"
	if exact {
		info.Scan = "exact"
	}
	// Fusión RRF.
	vIDs := make([]string, len(vHits))
	sims := map[string]float64{}
	for i, v := range vHits {
		vIDs[i] = v.id
		sims[v.id] = v.sim
	}
	tIDs := make([]string, len(tHits))
	byID := map[string]Hit{}
	for i, t := range tHits {
		tIDs[i] = t.SectionID
		byID[t.SectionID] = t
	}
	fusedList := fuseRRF(vIDs, tIDs, w)
	if len(fusedList) > q.Limit {
		fusedList = fusedList[:q.Limit]
	}
	// Similitud de los que solo encontró el texto; detalle de los que solo
	// encontraron los vectores.
	var needSim, needDetail []string
	var detailRanks []float64
	for _, fu := range fusedList {
		if _, ok := sims[fu.id]; !ok {
			needSim = append(needSim, fu.id)
		}
		if _, ok := byID[fu.id]; !ok {
			needDetail = append(needDetail, fu.id)
			detailRanks = append(detailRanks, sims[fu.id])
		}
	}
	more, err := h.vec.similarities(ctx, b, qvec, needSim)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range more {
		sims[k] = v
	}
	if len(needDetail) > 0 && tq != nil {
		dHits, err := h.text.details(ctx, tq, needDetail, detailRanks, MatchVector)
		if err != nil {
			return nil, nil, err
		}
		for _, d := range dHits {
			byID[d.SectionID] = d
		}
	}
	out := make([]Hit, 0, len(fusedList))
	for _, fu := range fusedList {
		hit, ok := byID[fu.id]
		if !ok {
			continue // sección fuera de los filtros entre las dos consultas
		}
		hit.Rank = fu.score
		hit.Retriever = ModeHybrid
		hit.MatchedBy = nil
		if fu.textRank > 0 {
			hit.MatchedBy = append(hit.MatchedBy, "text")
		}
		if fu.vectorRank > 0 {
			hit.MatchedBy = append(hit.MatchedBy, "vector")
		}
		if s, ok := sims[fu.id]; ok {
			s := s
			hit.Similarity = &s
		}
		out = append(out, hit)
	}
	return out, info, nil
}

// queryVector calcula el vector de la pregunta con el modelo del índice
// activo, con tiempo máximo y en caché (una hora, 2 000 preguntas).
func (h *Hybrid) queryVector(ctx context.Context, b VectorBackend, text string) ([]float32, error) {
	key := b.Index.Signature + "\x00" + strings.TrimSpace(text)
	h.mu.Lock()
	if c, ok := h.cache[key]; ok && time.Since(c.at) < time.Hour {
		h.mu.Unlock()
		return c.vec, nil
	}
	h.mu.Unlock()
	timeout := h.opts.QueryTimeout
	if timeout <= 0 {
		timeout = DefaultHybridOptions.QueryTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	vecs, err := b.Embedder.Embed(cctx, []string{text}, embed.KindQuery)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if len(h.cache) >= 2000 {
		h.cache = map[string]cachedVec{}
	}
	h.cache[key] = cachedVec{at: time.Now(), vec: vecs[0]}
	h.mu.Unlock()
	return vecs[0], nil
}
