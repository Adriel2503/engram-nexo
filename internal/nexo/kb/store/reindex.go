package store

// Nexo IA · NX-10 entrega 2b — reindexado seguro del índice de texto (design
// §14.3 regla 2 y 3). Cambiar la composición del texto indexado (RC-03) es una
// versión nueva del índice pgfts en nexo_kb_index_versions: se registra como
// building, se recalcula tsv documento por documento (una transacción por
// lote, la búsqueda sigue atendiendo con filas viejas o nuevas, ambas válidas)
// y al terminar pasa a active y la anterior a retired. Las secciones nuevas se
// indexan siempre con la composición en uso.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
)

// IndexState es el estado del índice pgfts.
type IndexState struct {
	ActiveVersion   int            `json:"active_version"`
	ActiveConfig    map[string]any `json:"active_config"`
	BuildingVersion int            `json:"building_version,omitempty"`
	Wanted          IndexOptions   `json:"wanted"`
	NeedsReindex    bool           `json:"needs_reindex"`
}

func docContextOf(cfg map[string]any) bool {
	v, _ := cfg["doc_context"].(bool)
	return v
}

// GetIndexState compara la versión activa con las opciones en uso.
func (s *Store) GetIndexState(ctx context.Context) (IndexState, error) {
	st := IndexState{Wanted: s.IndexOpts()}
	rows, err := s.DB.QueryContext(ctx, `SELECT version, status, config::text FROM nexo_kb_index_versions WHERE index_kind = $1 AND status IN ('active', 'building') ORDER BY version`, IndexKindPGFTS)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		var status, raw string
		if err := rows.Scan(&v, &status, &raw); err != nil {
			return st, err
		}
		cfg := map[string]any{}
		_ = json.Unmarshal([]byte(raw), &cfg)
		if status == "active" {
			st.ActiveVersion, st.ActiveConfig = v, cfg
		} else {
			st.BuildingVersion = v
		}
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	st.NeedsReindex = docContextOf(st.ActiveConfig) != st.Wanted.DocContext
	return st, nil
}

// BeginReindex registra (o reutiliza) la versión building con las opciones en
// uso. Devuelve su número.
func (s *Store) BeginReindex(ctx context.Context) (int, error) {
	opts := s.IndexOpts()
	var version int
	err := s.DB.QueryRowContext(ctx, `
		WITH cur AS (SELECT config FROM nexo_kb_index_versions WHERE index_kind = $1 AND status = 'active' ORDER BY version DESC LIMIT 1),
		b AS (SELECT version FROM nexo_kb_index_versions WHERE index_kind = $1 AND status = 'building' ORDER BY version DESC LIMIT 1)
		SELECT COALESCE((SELECT version FROM b), 0)`, IndexKindPGFTS).Scan(&version)
	if err != nil {
		return 0, err
	}
	if version > 0 {
		_, err = s.DB.ExecContext(ctx, `UPDATE nexo_kb_index_versions SET config = config || jsonb_build_object('doc_context', $3::boolean), updated_at = now()
			WHERE index_kind = $1 AND version = $2`, IndexKindPGFTS, version, opts.DocContext)
		return version, err
	}
	err = s.DB.QueryRowContext(ctx, `
		INSERT INTO nexo_kb_index_versions (index_kind, version, config, status)
		SELECT $1, COALESCE(max(version), 0) + 1,
			COALESCE((SELECT config FROM nexo_kb_index_versions WHERE index_kind = $1 AND status = 'active' ORDER BY version DESC LIMIT 1), '{}'::jsonb)
				|| jsonb_build_object('doc_context', $2::boolean, 'weights', jsonb_build_object('A', CASE WHEN $2 THEN 'path_titles + title + heading_path' ELSE 'heading_path' END, 'B', 'body')),
			'building'
		FROM nexo_kb_index_versions WHERE index_kind = $1
		RETURNING version`, IndexKindPGFTS, opts.DocContext).Scan(&version)
	return version, err
}

// FinishReindex activa la versión y retira las anteriores.
func (s *Store) FinishReindex(ctx context.Context, version int) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE nexo_kb_index_versions SET status = 'retired', updated_at = now() WHERE index_kind = $1 AND version <> $2 AND status <> 'retired'`, IndexKindPGFTS, version); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nexo_kb_index_versions SET status = 'active', built_at = now(), updated_at = now() WHERE index_kind = $1 AND version = $2`, IndexKindPGFTS, version); err != nil {
		return err
	}
	return tx.Commit()
}

// ReindexDocuments recalcula tsv (y body_sha256 faltante) de todas las
// secciones, vigentes e históricas, de los documentos con id > after, hasta
// limit documentos. Devuelve el último id y la cantidad de secciones.
func (s *Store) ReindexDocuments(ctx context.Context, after string, limit int) (string, int, error) {
	opts := s.IndexOpts()
	rows, err := s.DB.QueryContext(ctx, `
		SELECT s.id::text, s.document_id::text, array_to_json(s.heading_path)::text, s.body, v.title, COALESCE(v.origin->'path_titles', '[]'::jsonb)::text
		FROM nexo_kb_sections s JOIN nexo_kb_versions v ON v.id = s.version_id
		WHERE s.document_id IN (SELECT id FROM nexo_kb_documents WHERE id::text > $1 ORDER BY id LIMIT $2)
		ORDER BY s.document_id`, after, limit)
	if err != nil {
		return "", 0, err
	}
	var ids, its, ibs, fts, fbs, hashes []string
	last := ""
	for rows.Next() {
		var id, doc, hp, body, title, pt string
		if err := rows.Scan(&id, &doc, &hp, &body, &title, &pt); err != nil {
			_ = rows.Close()
			return "", 0, err
		}
		var heading, pathTitles []string
		_ = json.Unmarshal([]byte(hp), &heading)
		_ = json.Unmarshal([]byte(pt), &pathTitles)
		it := IndexedTitles(opts, title, pathTitles, heading)
		ib := capIndexed(body)
		ids, its, ibs = append(ids, id), append(its, it), append(ibs, ib)
		fts, fbs = append(fts, ingest.Fold(it)), append(fbs, ingest.Fold(ib))
		hashes = append(hashes, ingest.SHA256Hex(body))
		if doc > last {
			last = doc
		}
	}
	if err := rows.Close(); err != nil {
		return "", 0, err
	}
	if len(ids) == 0 {
		// Documentos sin secciones: avanzar igual.
		err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(max(id::text), '') FROM (SELECT id FROM nexo_kb_documents WHERE id::text > $1 ORDER BY id LIMIT $2) d`, after, limit).Scan(&last)
		return last, 0, err
	}
	ut, err := s.UnaccentVector(ctx, "x.it", "x.ft")
	if err != nil {
		return "", 0, err
	}
	ub, err := s.UnaccentVector(ctx, "x.ib", "x.fb")
	if err != nil {
		return "", 0, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		UPDATE nexo_kb_sections s SET
			tsv = setweight(to_tsvector('`+TSConfig+`', x.it) || `+ut+`, 'A') || setweight(to_tsvector('`+TSConfig+`', x.ib) || `+ub+`, 'B'),
			body_sha256 = COALESCE(s.body_sha256, x.bh)
		FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[]) AS x(id, it, ib, ft, fb, bh)
		WHERE s.id = x.id::uuid`, ids, its, ibs, fts, fbs, hashes); err != nil {
		return "", 0, fmt.Errorf("kb reindex: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", 0, err
	}
	// El último documento del lote (aunque no tenga secciones al final).
	var maxDoc string
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(max(id::text), '') FROM (SELECT id FROM nexo_kb_documents WHERE id::text > $1 ORDER BY id LIMIT $2) d`, after, limit).Scan(&maxDoc); err == nil && maxDoc > last {
		last = maxDoc
	}
	return last, len(ids), nil
}
