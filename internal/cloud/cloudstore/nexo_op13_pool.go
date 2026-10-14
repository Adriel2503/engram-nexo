package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// NX-07 (OP-13): relleno del índice de sesiones sin retener payloads.
//
// Un payload de chunk puede pesar hasta el límite de subida (8 MiB por
// defecto). La versión anterior guardaba una página de 128 payloads completos
// antes de procesar ninguno (cerca de 1 GiB con chunks grandes) y se ejecutaba
// en cada arranque. Ahora:
//
//   - la página recorre solo las claves (project_name, chunk_id), sin OFFSET y
//     con el cursor cerrado antes de tocar nada más (OP-13: con un pool de una
//     conexión no puede haber dos consultas abiertas);
//   - cada payload se lee solo, se indexa y se suelta antes del siguiente: la
//     memoria máxima es la de un chunk;
//   - al terminar una pasada completa se anota una marca (nexo_backfill_marks).
//     Las tres rutas que insertan en cloud_chunks (cloudstore.go) indexan sus
//     sesiones al escribir, así que después de una pasada completa ningún chunk
//     queda sin indexar y los arranques siguientes no vuelven a leer los
//     payloads. Una pasada interrumpida no deja marca y se repite entera (es
//     idempotente: INSERT ... ON CONFLICT DO NOTHING).
const (
	nexoBackfillKeyPage = 512
	// nexoSessionBackfillMark identifica la pasada. Si cambia la forma de extraer sesiones de un
	// payload, se sube la versión para que una base ya marcada se recorra otra vez.
	nexoSessionBackfillMark = "project_sessions_from_chunks_v1"
)

func (cs *CloudStore) nexoBackfillProjectSessions(ctx context.Context) error {
	if _, err := cs.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS nexo_backfill_marks (
  name TEXT PRIMARY KEY,
  done_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("cloudstore: backfill session index marks: %w", err)
	}
	var done bool
	if err := cs.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM nexo_backfill_marks WHERE name = $1)`, nexoSessionBackfillMark).Scan(&done); err != nil {
		return fmt.Errorf("cloudstore: backfill session index marks read: %w", err)
	}
	if done {
		return nil
	}
	type chunkKey struct{ project, id string }
	first, project, id := true, "", ""
	for {
		rows, err := cs.db.QueryContext(ctx, `SELECT project_name, chunk_id FROM cloud_chunks
   WHERE $1 OR (project_name, chunk_id) > ($2, $3)
   ORDER BY project_name, chunk_id LIMIT $4`, first, project, id, nexoBackfillKeyPage)
		if err != nil {
			return fmt.Errorf("cloudstore: backfill session index: %w", err)
		}
		keys := make([]chunkKey, 0, nexoBackfillKeyPage)
		for rows.Next() {
			var k chunkKey
			if err := rows.Scan(&k.project, &k.id); err != nil {
				_ = rows.Close()
				return fmt.Errorf("cloudstore: backfill session index scan: %w", err)
			}
			keys = append(keys, k)
		}
		closeErr := rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("cloudstore: backfill session index iterate: %w", err)
		}
		if closeErr != nil {
			return fmt.Errorf("cloudstore: backfill session index close: %w", closeErr)
		}
		for _, k := range keys {
			if err := cs.nexoBackfillChunk(ctx, k.project, k.id); err != nil {
				return err
			}
		}
		if len(keys) < nexoBackfillKeyPage {
			break
		}
		last := keys[len(keys)-1]
		first, project, id = false, last.project, last.id
	}
	if _, err := cs.db.ExecContext(ctx, `INSERT INTO nexo_backfill_marks (name) VALUES ($1) ON CONFLICT (name) DO NOTHING`, nexoSessionBackfillMark); err != nil {
		return fmt.Errorf("cloudstore: backfill session index mark: %w", err)
	}
	return nil
}

// nexoBackfillChunk lee un payload, indexa sus sesiones y lo suelta. La lectura
// termina (la fila se cierra en Scan) antes de insertar.
func (cs *CloudStore) nexoBackfillChunk(ctx context.Context, project, chunkID string) error {
	var payload []byte
	err := cs.db.QueryRowContext(ctx, `SELECT payload FROM cloud_chunks WHERE project_name = $1 AND chunk_id = $2`, project, chunkID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // lo borró una depuración entre la página y la lectura
	}
	if err != nil {
		return fmt.Errorf("cloudstore: backfill session index read: %w", err)
	}
	if err := cs.indexChunkSessions(ctx, project, payload); err != nil {
		return fmt.Errorf("cloudstore: backfill session index row: %w", err)
	}
	return nil
}
