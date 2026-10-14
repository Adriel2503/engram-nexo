package cloudstore

// NX-07 (OP-13) · memoria del relleno de sesiones (revisión adversarial, punto 7).
// La versión anterior retenía una página de 128 payloads completos: con subidas
// de hasta 8 MiB, cerca de 1 GiB en cada arranque. Estas pruebas miden el pico
// del montículo con chunks cercanos al máximo y comprueban la marca de pasada
// completa y el comportamiento con un pool de una conexión.

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// nexoBackfillDB abre un esquema propio con las dos tablas que usa el relleno.
func nexoBackfillDB(t *testing.T, maxConns int) *sql.DB {
	t.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN no configurada")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("DSN de pruebas inválida")
	}
	schema := fmt.Sprintf("nexo_op13_mem_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("no se pudo abrir la base de pruebas")
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err := admin.ExecContext(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal("no se pudo abrir el esquema de pruebas")
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(maxConns)
	for _, stmt := range []string{
		"CREATE TABLE cloud_chunks (project_name text NOT NULL, chunk_id text NOT NULL, payload jsonb NOT NULL, PRIMARY KEY(project_name, chunk_id))",
		"CREATE TABLE cloud_project_sessions (project_name text NOT NULL, session_id text NOT NULL, PRIMARY KEY(project_name, session_id))",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// nexoPeakHeap devuelve el mayor HeapAlloc visto mientras corre fn, menos el de partida.
func nexoPeakHeap(fn func()) uint64 {
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		var ms runtime.MemStats
		for {
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peak.Load() {
				peak.Store(ms.HeapAlloc)
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	fn()
	close(stop)
	<-finished
	if p := peak.Load(); p > base.HeapAlloc {
		return p - base.HeapAlloc
	}
	return 0
}

// Chunks de unos 7 MiB (cerca del máximo de subida de 8 MiB), con una sesión cada uno.
func TestNexoOP13BackfillPeakMemoryWithLargeChunks(t *testing.T) {
	const chunks, chunkBytes = 24, 7 << 20
	db := nexoBackfillDB(t, 4)
	if _, err := db.ExecContext(context.Background(), `INSERT INTO cloud_chunks
 SELECT 'proyecto', lpad(i::text, 8, '0'),
  jsonb_build_object('sessions', jsonb_build_array(jsonb_build_object('id', 's-' || i)),
   'observations', jsonb_build_array(jsonb_build_object('sync_id', 'o-' || i, 'content', (SELECT string_agg(md5(i::text || g::text), '') FROM generate_series(1, $1::int) g))))
 FROM generate_series(1, $2::int) i`, chunkBytes/32, chunks); err != nil {
		t.Fatal(err)
	}
	var biggest int
	if err := db.QueryRowContext(context.Background(), `SELECT max(length(payload::text)) FROM cloud_chunks`).Scan(&biggest); err != nil || biggest < 7<<20 {
		t.Fatalf("los chunks de prueba deben pesar cerca de 8 MiB: %d %v", biggest, err)
	}
	cs := &CloudStore{db: db}
	var runErr error
	peak := nexoPeakHeap(func() { runErr = cs.backfillProjectSessionsFromChunks(context.Background()) })
	if runErr != nil {
		t.Fatal(runErr)
	}
	var count int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM cloud_project_sessions").Scan(&count); err != nil || count != chunks {
		t.Fatalf("sesiones indexadas: %d (%v), esperado %d", count, err, chunks)
	}
	// Retener los 24 payloads costaría más de 170 MiB solo en bytes (sin buffers ni JSON). Con un
	// payload a la vez el pico es de unos pocos chunks; el límite (150 MiB, frente a 345 MiB con la versión anterior) deja margen para el recolector.
	const limit = 150 << 20
	if peak > limit {
		t.Fatalf("pico de memoria %d MiB con %d chunks de %d MiB (límite %d MiB)", peak>>20, chunks, biggest>>20, limit>>20)
	}
	t.Logf("pico del montículo: %d MiB con %d chunks de %d MiB", peak>>20, chunks, biggest>>20)
}

// Una pasada completa deja marca y el arranque siguiente no recorre los payloads; una pasada
// interrumpida no deja marca y se repite entera.
func TestNexoOP13BackfillMarkSkipsCompletedPass(t *testing.T) {
	db := nexoBackfillDB(t, 1) // pool de una conexión: sin cuelgues
	if _, err := db.ExecContext(context.Background(), `INSERT INTO cloud_chunks
 SELECT 'a', lpad(i::text, 8, '0'), jsonb_build_object('sessions', jsonb_build_array(jsonb_build_object('id', 's-' || i)))
 FROM generate_series(1, 700) i`); err != nil {
		t.Fatal(err)
	}
	cs := &CloudStore{db: db}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS nexo_backfill_marks (name TEXT PRIMARY KEY, done_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}

	// Interrumpida: contexto cancelado, sin marca.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cs.backfillProjectSessionsFromChunks(cancelled); err == nil {
		t.Fatal("una pasada con el contexto cancelado debía fallar")
	}
	var marks int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM nexo_backfill_marks").Scan(&marks); err != nil || marks != 0 {
		t.Fatalf("una pasada interrumpida no debe dejar marca: %d %v", marks, err)
	}

	// Completa: indexa los 700 (más de una página de claves) y deja la marca.
	done := make(chan error, 1)
	go func() { done <- cs.backfillProjectSessionsFromChunks(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("relleno bloqueado con un pool de una conexión")
	}
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM cloud_project_sessions").Scan(&n); err != nil || n != 700 {
		t.Fatalf("sesiones: %d %v", n, err)
	}
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM nexo_backfill_marks").Scan(&marks); err != nil || marks != 1 {
		t.Fatalf("marca de pasada completa: %d %v", marks, err)
	}

	// El arranque siguiente no relee: un chunk sin indexar insertado a mano se queda como está
	// (las rutas reales de escritura indexan al insertar, así que no existen chunks así en uso).
	if _, err := db.ExecContext(context.Background(), `INSERT INTO cloud_chunks VALUES ('a', 'zzzzzzzz', '{"sessions":[{"id":"s-nuevo"}]}')`); err != nil {
		t.Fatal(err)
	}
	if err := cs.backfillProjectSessionsFromChunks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM cloud_project_sessions WHERE session_id = 's-nuevo'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("el segundo arranque no debía recorrer los chunks: %d %v", n, err)
	}
	// Si cambia la versión de la marca, se recorre de nuevo.
	if _, err := db.ExecContext(context.Background(), "DELETE FROM nexo_backfill_marks"); err != nil {
		t.Fatal(err)
	}
	if err := cs.backfillProjectSessionsFromChunks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM cloud_project_sessions WHERE session_id = 's-nuevo'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("sin marca debe recorrer y completar: %d %v", n, err)
	}
}
