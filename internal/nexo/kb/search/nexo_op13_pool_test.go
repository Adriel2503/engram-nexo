package search

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// El driver provoca un error SQL real dentro de la transacción, con contexto
// vigente. La inyección vive solo en pruebas y no modifica PGVector.
type op13FaultConnector struct {
	driver.Connector
	target   string
	injected atomic.Int32
}

func (c *op13FaultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &op13FaultConn{Conn: conn, owner: c}, nil
}

type op13FaultConn struct {
	driver.Conn
	owner *op13FaultConnector
}

func (c *op13FaultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *op13FaultConn) CheckNamedValue(v *driver.NamedValue) error {
	return c.Conn.(driver.NamedValueChecker).CheckNamedValue(v)
}
func (c *op13FaultConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, c.owner.target) {
		c.owner.injected.Add(1)
		return c.Conn.(driver.ExecerContext).ExecContext(ctx, "SELECT 1/0", nil)
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func (c *op13FaultConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

func TestNexoOP13VectorFallbackSingleConnection(t *testing.T) {
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN no configurada")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("DSN de pruebas inválida")
	}
	admin := stdlib.OpenDB(*cfg)
	defer func() { _ = admin.Close() }()
	var vectorSchema string
	if err := admin.QueryRow(`SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector'`).Scan(&vectorSchema); err != nil {
		t.Fatal("esta regresión requiere pgvector en la base de pruebas")
	}
	schema := fmt.Sprintf("nexo_op13_vector_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE") }()
	cfg.RuntimeParams["search_path"] = schema
	setup := stdlib.OpenDB(*cfg)
	defer func() { _ = setup.Close() }()
	section, spaceID := store.NewID(), store.NewID()
	for _, stmt := range []string{
		`CREATE TABLE nexo_kb_sections (id uuid PRIMARY KEY, space_id uuid, is_current boolean, valid_from timestamptz, valid_until timestamptz, category text, tags text[])`,
		`CREATE TABLE nexo_kb_section_vectors (section_id uuid, space_id uuid, index_version int, dims int, embedding ` + quoteIdentifier(vectorSchema) + `.vector(3))`,
	} {
		if _, err := setup.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := setup.Exec(`INSERT INTO nexo_kb_sections VALUES ($1,$2,true,now()-interval '1 day',NULL,'lineamiento','{}')`, section, spaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(`INSERT INTO nexo_kb_section_vectors VALUES ($1,$2,1,3,'[1,0,0]')`, section, spaceID); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"SET LOCAL hnsw.ef_search", "SET LOCAL hnsw.iterative_scan"} {
		t.Run(target, func(t *testing.T) {
			connector := &op13FaultConnector{Connector: stdlib.GetConnector(*cfg), target: target}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1) // después de los datos
			p := &PGVector{st: store.New(db), opts: VectorOptions{Candidates: 40, Iterative: "relaxed_order"}}
			backend := VectorBackend{Index: store.VectorIndex{Version: 1, Dims: 3}, Support: store.VectorSupport{Available: true, Schema: vectorSchema, Iterative: true}}
			now := time.Now()
			f := Filter{spaceIDs: []string{spaceID}, at: now, knownAt: now, current: true}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				hits, exact, err := p.search(ctx, backend, []float32{1, 0, 0}, Query{}, f, 5)
				if err == nil && (exact || len(hits) != 1 || hits[0].id != section || hits[0].sim < .99) {
					err = fmt.Errorf("el fallback no devolvió el vector esperado")
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil || ctx.Err() != nil {
					t.Fatalf("fallback falló: %v", err)
				}
			case <-time.After(5 * time.Second):
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
				}
				t.Fatal("fallback bloqueado: venció el temporizador externo")
			}
			if connector.injected.Load() != 1 {
				t.Fatal("no se inyectó exactamente un error SQL")
			}
			if n := db.Stats().InUse; n != 0 {
				t.Fatalf("conexiones retenidas: %d", n)
			}
		})
	}
}
