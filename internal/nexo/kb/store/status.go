package store

// Nexo IA · NX-10 (panel) — lecturas para la pantalla "Estado de la búsqueda"
// (docs/nexo/panel/design.md §11). Solo consultas de lectura sobre las tablas
// de R2 que ya existen; no hay tablas nuevas ni migración.

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// VectorVersionInfo es una versión del índice vectorial de cualquier estado
// (VectorIndexes solo devuelve las activas y en construcción).
type VectorVersionInfo struct {
	Version  int    `json:"version"`
	Status   string `json:"status"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Dims     int    `json:"dims"`
	// CreatedAt: cuándo se registró la versión. ActivatedAt: cuándo terminó
	// de construirse y pasó a responder (vacío si nunca se activó).
	// RetiredAt: cuándo se retiró (vacío si no está retirada).
	CreatedAt   time.Time  `json:"created_at"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
	RetiredAt   *time.Time `json:"retired_at,omitempty"`
}

// VectorVersionHistory lista las versiones del índice vectorial de todos los
// estados, de la más nueva a la más antigua.
func (s *Store) VectorVersionHistory(ctx context.Context, limit int) ([]VectorVersionInfo, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+vectorIndexColumns+`, created_at, updated_at FROM nexo_kb_index_versions
		WHERE index_kind = $1 ORDER BY version DESC LIMIT $2`, IndexKindVector, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []VectorVersionInfo{}
	for rows.Next() {
		var raw string
		var vi VectorIndex
		var seq sql.NullInt64
		var built sql.NullTime
		var created, updated time.Time
		if err := rows.Scan(&vi.Version, &vi.Status, &raw, &seq, &built, &created, &updated); err != nil {
			return nil, err
		}
		cfg := decodeConfig(raw)
		v := VectorVersionInfo{Version: vi.Version, Status: vi.Status, CreatedAt: created.UTC(), ActivatedAt: timePtr(built)}
		v.Provider, _ = cfg["provider"].(string)
		v.Model, _ = cfg["model"].(string)
		if d, ok := cfg["dims"].(float64); ok {
			v.Dims = int(d)
		}
		if vi.Status == "retired" {
			u := updated.UTC()
			v.RetiredAt = &u
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CountIndexableSections cuenta las secciones que el indexador debe
// vectorizar (las mismas que encola enqueueSQL: se omiten las que nunca
// estuvieron publicadas).
func (s *Store) CountIndexableSections(ctx context.Context) (int64, error) {
	var n int64
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM nexo_kb_sections WHERE recorded_until IS NULL OR recorded_until > recorded_from`).Scan(&n)
	return n, err
}

// DeadLetter es una carta muerta: una sección que no se pudo vectorizar tras
// varios intentos. Error es el mensaje del proveedor tal como lo guardó el
// indexador (ya sin la clave); el llamador lo vuelve a limpiar al mostrarlo.
type DeadLetter struct {
	Version   int       `json:"index_version"`
	FailCount int       `json:"fail_count"`
	FailedAt  time.Time `json:"failed_at"`
	Error     string    `json:"error"`
}

// RecentDeadLetters devuelve las últimas cartas muertas (de todas las
// versiones no retiradas) y cuántas hay en total.
func (s *Store) RecentDeadLetters(ctx context.Context, limit int) ([]DeadLetter, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 5
	}
	const live = `x.index_version IN (SELECT version FROM nexo_kb_index_versions WHERE index_kind = $1 AND status IN ('active', 'building'))`
	var total int64
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM nexo_kb_index_dead x WHERE `+live, IndexKindVector).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT x.index_version, x.fail_count, x.failed_at, x.last_error FROM nexo_kb_index_dead x
		WHERE `+live+` ORDER BY x.failed_at DESC, x.id DESC LIMIT $2`, IndexKindVector, limit)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := []DeadLetter{}
	for rows.Next() {
		var d DeadLetter
		if err := rows.Scan(&d.Version, &d.FailCount, &d.FailedAt, &d.Error); err != nil {
			return nil, 0, err
		}
		d.FailedAt = d.FailedAt.UTC()
		out = append(out, d)
	}
	return out, total, rows.Err()
}

func decodeConfig(raw string) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}
