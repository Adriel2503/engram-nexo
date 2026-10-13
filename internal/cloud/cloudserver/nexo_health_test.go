package cloudserver

// NX-10: GET /nexo/health/ready responde 200 solo si la base responde; 503 sin
// base o con la base caída. Sin autenticación y sin detalles del error.
// GET /health de Engram no cambia.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// nexoDownDBStore es un almacén cuya base no responde.
type nexoDownDBStore struct {
	fakeStore
	db *sql.DB
}

func (s *nexoDownDBStore) NexoDB() *sql.DB { return s.db }

func nexoGetReady(t *testing.T, srv *CloudServer) (int, map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nexo/health/ready", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("respuesta no JSON: %q", rec.Body.String())
	}
	return rec.Code, body, rec.Body.String()
}

func TestNexoReadyWithoutDatabase(t *testing.T) {
	code, body, _ := nexoGetReady(t, New(&fakeStore{}, nil, 0))
	if code != http.StatusServiceUnavailable || body["status"] != "unavailable" {
		t.Fatalf("sin base: %d %v", code, body)
	}
}

func TestNexoReadyDatabaseDown(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://nadie:x@127.0.0.1:1/nada?connect_timeout=1&sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := New(&nexoDownDBStore{db: db}, nil, 0)
	code, body, raw := nexoGetReady(t, srv)
	if code != http.StatusServiceUnavailable || body["status"] != "unavailable" || len(body) != 1 {
		t.Fatalf("base caída: %d %s", code, raw)
	}
	if strings.Contains(raw, "127.0.0.1") || strings.Contains(raw, "nadie") {
		t.Fatalf("la respuesta expone datos de la base: %s", raw)
	}
	// /health de Engram sigue respondiendo ok.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("/health: %d %s", rec.Code, rec.Body.String())
	}
}

func TestNexoReadyWithPostgres(t *testing.T) {
	env := newNexoRealEnv(t) // se salta sin CLOUDSTORE_TEST_DSN
	code, body, _ := nexoGetReady(t, env.srv)
	if code != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("con base: %d %v", code, body)
	}
}
