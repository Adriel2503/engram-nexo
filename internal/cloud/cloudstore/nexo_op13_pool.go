package cloudstore

import (
	"context"
	"fmt"
)

// NX-07 (OP-13): páginas por clave compuesta, sin OFFSET ni cursor abierto
// durante las inserciones. Solo se conservan los payloads de una página.
func (cs *CloudStore) nexoBackfillProjectSessions(ctx context.Context) error {
	const pageSize = 128
	type chunk struct {
		project, id string
		payload     []byte
	}
	first, project, id := true, "", ""
	for {
		rows, err := cs.db.QueryContext(ctx, `SELECT project_name, chunk_id, payload FROM cloud_chunks
   WHERE $1 OR (project_name, chunk_id) > ($2, $3)
   ORDER BY project_name, chunk_id LIMIT $4`, first, project, id, pageSize)
		if err != nil {
			return fmt.Errorf("cloudstore: backfill session index: %w", err)
		}
		page := make([]chunk, 0, pageSize)
		for rows.Next() {
			var c chunk
			if err := rows.Scan(&c.project, &c.id, &c.payload); err != nil {
				_ = rows.Close()
				return fmt.Errorf("cloudstore: backfill session index scan: %w", err)
			}
			page = append(page, c)
		}
		closeErr := rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("cloudstore: backfill session index iterate: %w", err)
		}
		if closeErr != nil {
			return fmt.Errorf("cloudstore: backfill session index close: %w", closeErr)
		}
		for _, c := range page {
			if err := cs.indexChunkSessions(ctx, c.project, c.payload); err != nil {
				return fmt.Errorf("cloudstore: backfill session index row: %w", err)
			}
		}
		if len(page) < pageSize {
			return nil
		}
		last := page[len(page)-1]
		first, project, id = false, last.project, last.id
	}
}
