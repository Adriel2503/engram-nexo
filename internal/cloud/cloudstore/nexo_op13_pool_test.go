package cloudstore

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestNexoOP13BackfillSingleConnection(t *testing.T) {
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN no configurada")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("DSN de pruebas inválida")
	}
	schema := fmt.Sprintf("nexo_op13_sessions_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("no se pudo abrir la base de pruebas")
	}
	defer func() { _ = admin.Close() }()
	if _, err := admin.ExecContext(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal("no se pudo abrir el esquema de pruebas")
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range []string{
		"CREATE TABLE cloud_chunks (project_name text NOT NULL, chunk_id text NOT NULL, payload jsonb NOT NULL, PRIMARY KEY(project_name, chunk_id))",
		"CREATE TABLE cloud_project_sessions (project_name text NOT NULL, session_id text NOT NULL, PRIMARY KEY(project_name, session_id))",
		// Más de dos páginas; misma clave de chunk en proyectos distintos.
		// Referencias en prompts/observaciones no crean sesiones implícitas.
		`INSERT INTO cloud_chunks SELECT p, lpad(i::text, 8, '0'),
   jsonb_build_object('sessions', jsonb_build_array(jsonb_build_object('id', 's-' || i)),
    'observations', jsonb_build_array(jsonb_build_object('session_id', 'o-' || i)),
    'prompts', jsonb_build_array(jsonb_build_object('session_id', 'p-' || i)))
   FROM generate_series(1, 257) i CROSS JOIN (VALUES ('a'), ('b')) projects(p)`,
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	db.SetMaxOpenConns(1)
	cs := &CloudStore{db: db}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cs.backfillProjectSessionsFromChunks(ctx) }()
	select {
	case err := <-done:
		if err != nil || ctx.Err() != nil {
			t.Fatalf("relleno falló: %v", err)
		}
	case <-time.After(15 * time.Second):
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		t.Fatal("relleno bloqueado: venció el temporizador externo")
	}
	var count int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM cloud_project_sessions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2*257 {
		t.Fatalf("sesiones indexadas: %d, esperado %d", count, 2*257)
	}
	if err := cs.backfillProjectSessionsFromChunks(ctx); err != nil {
		t.Fatal(err)
	}
	if n := db.Stats().InUse; n != 0 {
		t.Fatalf("conexiones retenidas: %d", n)
	}
}
