package service

// Nexo IA · NX-10 (panel) — estado de la búsqueda para la pantalla
// "Estado de la búsqueda" (docs/nexo/panel/design.md §11). Junta en una sola
// lectura lo que ya expone GET /nexo/kb/v1/admin/index (versiones y cola del
// índice vectorial) con lo que la pantalla necesita además: el modo por
// defecto, el proveedor sin su clave, las versiones retiradas con sus fechas,
// el avance sobre el total de secciones, las últimas cartas muertas, las
// últimas corridas de evaluación y los parámetros en uso.
//
// Es solo lectura y exige platform.settings, igual que la API. La clave del
// proveedor nunca sale de aquí: se informa si está configurada y de qué
// variable sale (embed.KeyStatus), y los mensajes de error se vuelven a
// limpiar antes de entregarse.

import (
	"context"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/embed"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

const (
	statusDeadLetters = 5
	statusEvalRuns    = 8
	statusVersions    = 12
	statusErrorRunes  = 300
)

// SearchEmbedder describe el proveedor de embeddings sin su clave.
type SearchEmbedder struct {
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	Dims     int           `json:"dims"`
	Storage  string        `json:"storage,omitempty"`
	Key      embed.KeyInfo `json:"key"`
}

// SearchStatus es todo lo que muestra la pantalla de estado.
type SearchStatus struct {
	// DefaultMode es el modo que usa una consulta que no pide uno (fts o
	// hybrid). Reason dice por qué es fts cuando el servidor tiene o pide
	// búsqueda por significado.
	DefaultMode string `json:"default_mode"`
	Reason      string `json:"reason,omitempty"`
	// ConfigError: el entorno pide un proveedor pero la configuración no es
	// válida (sin secretos).
	ConfigError string `json:"config_error,omitempty"`
	// Enabled: hay un proveedor de embeddings en uso.
	Enabled  bool                `json:"enabled"`
	Embedder SearchEmbedder      `json:"embedder"`
	PGVector store.VectorSupport `json:"pgvector"`
	// Versions: todas las versiones del índice vectorial, de la más nueva a
	// la más antigua. Queue: la cola de las activas y en construcción.
	Versions []store.VectorVersionInfo `json:"versions"`
	Queue    []store.VectorQueueStats  `json:"queue"`
	// Sections: secciones que el indexador debe vectorizar.
	Sections int64 `json:"sections"`
	// DeadTotal y Dead: cartas muertas de las versiones vigentes y las
	// últimas con su mensaje.
	DeadTotal int64              `json:"dead_total"`
	Dead      []store.DeadLetter `json:"dead"`
	EvalRuns  []store.EvalRun    `json:"eval_runs"`
	// Text: estado del índice de texto (el de R1), el mismo de GET /admin/index.
	Text store.IndexState `json:"text"`
	R1   R1Options        `json:"r1"`
	R2   R2Options        `json:"r2"`
	// Rerank: estado del reordenamiento (R3), el mismo de RerankState.
	Rerank RerankState `json:"rerank"`
}

// SearchStatus lee el estado de la búsqueda (platform.settings).
func (s *Service) SearchStatus(ctx context.Context, id authz.Identity) (SearchStatus, error) {
	if err := s.requirePlatform(ctx, id, authz.CapPlatformSettings, "index.search_status"); err != nil {
		return SearchStatus{}, err
	}
	out := SearchStatus{DefaultMode: search.ModeFTS, R1: s.r1, R2: s.r2, Versions: []store.VectorVersionInfo{}, Queue: []store.VectorQueueStats{},
		Dead: []store.DeadLetter{}, EvalRuns: []store.EvalRun{}}
	out.Rerank = s.rerankState()
	cfg, cfgErr := s.statusEmbedConfig()
	out.Enabled = s.hybrid != nil
	out.Embedder = SearchEmbedder{Provider: embed.ProviderOff}
	if cfg.Enabled() {
		out.Embedder = SearchEmbedder{Provider: cfg.Provider, Model: cfg.Model, Dims: cfg.Dims, Storage: embed.StorageType(cfg.Dims), Key: embed.KeyStatus(cfg, s.keyEnv())}
	}
	out.ConfigError = cfgErr
	switch {
	case cfgErr != "":
		out.Reason = "La configuración de embeddings no es válida: " + cfgErr + ". La búsqueda usa solo texto."
	case s.hybrid == nil && cfg.Enabled():
		out.Reason = "No se pudo crear el proveedor de embeddings. La búsqueda usa solo texto."
	case s.hybrid == nil:
		out.Reason = "NEXO_KB_EMBEDDER no está configurado: la búsqueda usa solo texto."
	}

	vs, err := s.st.VectorSupport(ctx)
	if err != nil {
		return out, err
	}
	out.PGVector = vs
	if s.hybrid != nil {
		if !vs.Available {
			out.Reason = "La base de datos no tiene la extensión pgvector: la búsqueda usa solo texto."
		} else if _, why, ok := s.resolveVector(ctx); ok {
			out.DefaultMode = search.ModeHybrid
		} else if why != "" {
			out.Reason = capitalizeFirst(strings.TrimSuffix(why, ".")) + ". Mientras tanto la búsqueda usa solo texto."
		}
	}

	if out.Text, err = s.st.GetIndexState(ctx); err != nil {
		return out, err
	}
	if out.Versions, err = s.st.VectorVersionHistory(ctx, statusVersions); err != nil {
		return out, err
	}
	if vs.Available || len(out.Versions) > 0 {
		if out.Queue, err = s.st.VectorStats(ctx); err != nil {
			return out, err
		}
	}
	if out.Sections, err = s.st.CountIndexableSections(ctx); err != nil {
		return out, err
	}
	dead, total, err := s.st.RecentDeadLetters(ctx, statusDeadLetters)
	if err != nil {
		return out, err
	}
	out.DeadTotal = total
	for _, d := range dead {
		d.Error = cleanProviderMessage(d.Error, cfg.APIKey)
		out.Dead = append(out.Dead, d)
	}
	if out.EvalRuns, err = store.ListEvalRuns(ctx, s.db, "", false, statusEvalRuns); err != nil {
		return out, err
	}
	return out, nil
}

// statusEmbedConfig devuelve la configuración de embeddings en uso: la del
// servicio si hay proveedor; si no, la que lee el entorno (con su error, para
// explicar por qué la búsqueda por significado no está activa).
func (s *Service) statusEmbedConfig() (embed.Config, string) {
	if s.embSet || s.hybrid != nil {
		return s.embCfg, ""
	}
	c, err := embed.ConfigFromEnv()
	if err != nil {
		return embed.Config{Provider: embed.ProviderOff}, cleanProviderMessage(err.Error(), "")
	}
	return c, ""
}

// keyEnv es el entorno para saber de dónde sale la clave: el del proceso, salvo
// con un embedder inyectado (pruebas), donde no se sabe.
func (s *Service) keyEnv() func(string) string {
	if s.embSet {
		return nil
	}
	return os.Getenv
}

var (
	reKeyParam = regexp.MustCompile(`(?i)\b(key|api[-_]?key|token|access_token)=[^&\s"']+`)
	reBearer   = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
	reLongKey  = regexp.MustCompile(`\b(AIza[0-9A-Za-z_-]{20,}|sk-[A-Za-z0-9_-]{16,}|egc_[a-z0-9-]+_[0-9a-f]{8}_[A-Za-z0-9-]{20,})`)
)

// cleanProviderMessage deja un mensaje de error del proveedor listo para
// mostrarse: sin la clave (ni por su valor ni por su forma), en una línea y
// de hasta 300 caracteres. Es una segunda defensa: el cliente de embeddings ya
// reemplaza la clave por "[clave]" antes de guardar el error.
func cleanProviderMessage(msg, key string) string {
	if key = strings.TrimSpace(key); len(key) >= 4 {
		msg = strings.ReplaceAll(msg, key, "[clave]")
	}
	msg = reKeyParam.ReplaceAllString(msg, "$1=[clave]")
	msg = reBearer.ReplaceAllString(msg, "$1 [clave]")
	msg = reLongKey.ReplaceAllString(msg, "[clave]")
	msg = strings.Join(strings.Fields(msg), " ")
	if utf8.RuneCountInString(msg) > statusErrorRunes {
		r := []rune(msg)
		msg = string(r[:statusErrorRunes]) + "..."
	}
	return msg
}

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return strings.ToUpper(string(r)) + s[n:]
}
