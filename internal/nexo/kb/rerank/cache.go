package rerank

// Caché de puntajes en memoria (servidor): una misma pregunta con los mismos
// pasajes no vuelve a pagar la llamada al proveedor. Clave = hash del
// proveedor, el modelo, la pregunta y el texto del pasaje (el contenido de una
// sección es inmutable, así que un texto igual tiene el mismo puntaje). Solo
// se guardan puntajes válidos; si todo está en caché la respuesta es
// inmediata y si falta algo solo se piden los pasajes que faltan.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// CacheOptions ajusta la caché.
type CacheOptions struct {
	TTL time.Duration
	Max int
}

// DefaultCache: una hora y 5 000 puntajes.
var DefaultCache = CacheOptions{TTL: time.Hour, Max: 5000}

type cacheEntry struct {
	at    time.Time
	value float64
}

type cached struct {
	inner Reranker
	opts  CacheOptions

	mu      sync.Mutex
	entries map[string]cacheEntry
}

// WithCache envuelve un Reranker con la caché de puntajes.
func WithCache(r Reranker, o CacheOptions) Reranker {
	if o.TTL <= 0 || o.Max <= 0 {
		o = DefaultCache
	}
	return &cached{inner: r, opts: o, entries: map[string]cacheEntry{}}
}

func (c *cached) Name() string  { return c.inner.Name() }
func (c *cached) Model() string { return c.inner.Model() }

// Stats reenvía los contadores del reordenador interno.
func (c *cached) Stats() Stats {
	if s, ok := c.inner.(StatsReporter); ok {
		return s.Stats()
	}
	return Stats{}
}

func (c *cached) key(query, text string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{c.inner.Name(), c.inner.Model(), strings.TrimSpace(query), text}, "\x00")))
	return hex.EncodeToString(h[:])
}

func (c *cached) Rerank(ctx context.Context, query string, passages []Passage) ([]Score, error) {
	out := make([]Score, len(passages))
	keys := make([]string, len(passages))
	var miss []Passage
	var missIdx []int
	now := time.Now()
	c.mu.Lock()
	for i, p := range passages {
		keys[i] = c.key(query, p.Text)
		if e, ok := c.entries[keys[i]]; ok && now.Sub(e.at) < c.opts.TTL {
			out[i] = Score{Value: e.value, OK: true}
			continue
		}
		miss, missIdx = append(miss, p), append(missIdx, i)
	}
	c.mu.Unlock()
	if len(miss) == 0 {
		return out, nil
	}
	got, err := c.inner.Rerank(ctx, query, miss)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries)+len(miss) > c.opts.Max {
		c.entries = map[string]cacheEntry{}
	}
	for j, i := range missIdx {
		if j < len(got) && got[j].OK {
			out[i] = got[j]
			c.entries[keys[i]] = cacheEntry{at: now, value: got[j].Value}
		}
	}
	return out, err
}
