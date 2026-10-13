package service

// Nexo IA · NX-10 R3 — reordenamiento de los candidatos con un modelo externo
// (design §21): proveedor por configuración (RC-29), caída segura al orden de
// la búsqueda (RC-21) y registro de lo que cambió y de la latencia en cada
// consulta (search.RerankInfo) y en las corridas de evaluación.
//
// R3 es una mejora opcional que se degrada: con NEXO_KB_RERANKER=off (por
// defecto), el servicio es exactamente el de R2. Sin proveedor nunca se
// reordena, aunque se pida. La copia local de la PC no reordena (RC-30).

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/rerank"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
)

// Variables de entorno de R3 (además de las de rerank: NEXO_KB_RERANKER y
// NEXO_KB_RERANK_*).
const (
	EnvRerankTopN     = "NEXO_KB_RERANK_TOP_N"
	EnvRerankBudget   = "NEXO_KB_RERANK_BUDGET_SECONDS"
	EnvRerankBlend    = "NEXO_KB_RERANK_BLEND"
	EnvRerankMinScore = "NEXO_KB_RERANK_MIN_SCORE"
	EnvRerankDefault  = "NEXO_KB_RERANK_DEFAULT"
	EnvRerankCache    = "NEXO_KB_RERANK_CACHE"
)

// DefaultR3 son los valores por defecto del reordenamiento (design §21.6).
var DefaultR3 = search.DefaultRerankOptions

// R3FromEnv lee los parámetros del reordenamiento del entorno.
func R3FromEnv() search.RerankOptions {
	o := DefaultR3
	get := func(k string) string { return strings.TrimSpace(os.Getenv(k)) }
	if n, err := strconv.Atoi(get(EnvRerankTopN)); err == nil && n >= 2 && n <= search.MaxRerankTopN {
		o.TopN = n
	}
	if n, err := strconv.Atoi(get(EnvRerankBudget)); err == nil && n >= 1 && n <= 600 {
		o.Budget = time.Duration(n) * time.Second
	}
	if v, err := strconv.ParseFloat(get(EnvRerankBlend), 64); err == nil && v >= 0 && v <= 1 {
		o.Blend = v
	}
	if v, err := strconv.ParseFloat(get(EnvRerankMinScore), 64); err == nil && v >= 0 && v <= 1 {
		o.MinScore = v
	}
	switch strings.ToLower(get(EnvRerankDefault)) {
	case "on", "1", "true", "yes", "si", "sí":
		o.Default = true
	case "off", "0", "false", "no":
		o.Default = false
	}
	return o
}

// WithReranker fija el reordenador y su configuración (pruebas con uno
// simulado; en el servidor sale de NEXO_KB_RERANKER).
func WithReranker(r rerank.Reranker, c rerank.Config) Option {
	return func(s *Service) { s.rr, s.rrCfg, s.rrSet = r, c, true }
}

// WithR3 fija los parámetros del reordenamiento (en lugar de los del
// entorno).
func WithR3(o search.RerankOptions) Option { return func(s *Service) { s.r3 = o; s.r3Set = true } }

// setupR3 lee la configuración y, con un proveedor, activa el reordenamiento
// en el buscador. Se llama después de setupR2 (que puede reemplazar el
// buscador).
func (s *Service) setupR3() {
	if !s.r3Set {
		s.r3 = R3FromEnv()
	}
	if !s.rrSet {
		c, err := rerank.ConfigFromEnv()
		if err != nil {
			log.Printf("[nexo] kb: configuración del reordenamiento inválida (%v); sin reordenamiento", err)
			return
		}
		if !c.Enabled() {
			return
		}
		r, err := rerank.New(c)
		if err != nil {
			log.Printf("[nexo] kb: %v; sin reordenamiento", err)
			return
		}
		if strings.EqualFold(strings.TrimSpace(os.Getenv(EnvRerankCache)), "off") {
			s.rr = r
		} else {
			s.rr = rerank.WithCache(r, rerank.DefaultCache)
		}
		s.rrCfg = c
	}
	if s.rr == nil {
		return
	}
	s.searcher.SetReranker(s.rr, s.st.PassageTexts, s.r3)
}

// RerankEnabled informa si hay un reordenador configurado.
func (s *Service) RerankEnabled() bool { return s.rr != nil }

// R3 devuelve los parámetros del reordenamiento en uso.
func (s *Service) R3() search.RerankOptions { return s.r3 }

// ApplyR3 cambia los parámetros del reordenamiento (medición de R0).
func (s *Service) ApplyR3(o search.RerankOptions) {
	s.r3 = o
	if s.rr != nil {
		s.searcher.SetReranker(s.rr, s.st.PassageTexts, o)
	}
}

// RerankStats devuelve el uso acumulado del reordenador (llamadas, reintentos,
// lotes fallidos y tokens que informa el proveedor), si lo cuenta.
func (s *Service) RerankStats() (rerank.Stats, bool) {
	if sr, ok := s.rr.(rerank.StatsReporter); ok {
		return sr.Stats(), true
	}
	return rerank.Stats{}, false
}

// RerankState es el estado del reordenamiento.
type RerankState struct {
	Enabled  bool                 `json:"enabled"`
	Reranker map[string]any       `json:"reranker,omitempty"`
	Options  search.RerankOptions `json:"options"`
	Stats    *rerank.Stats        `json:"stats,omitempty"`
	Reason   string               `json:"reason,omitempty"`
}

// RerankState informa el estado del reordenamiento (platform.settings).
func (s *Service) RerankState(ctx context.Context, id authz.Identity) (RerankState, error) {
	if err := s.requirePlatform(ctx, id, authz.CapPlatformSettings, "index.rerank.state"); err != nil {
		return RerankState{}, err
	}
	return s.rerankState(), nil
}

// rerankState arma el estado sin comprobar permisos (quien llama ya lo hizo).
func (s *Service) rerankState() RerankState {
	st := RerankState{Enabled: s.rr != nil, Options: s.r3}
	if s.rr == nil {
		st.Reason = "NEXO_KB_RERANKER no configurado: la búsqueda no se reordena"
		return st
	}
	st.Reranker = s.rrCfg.Describe()
	if sr, ok := s.RerankStats(); ok {
		st.Stats = &sr
	}
	return st
}
