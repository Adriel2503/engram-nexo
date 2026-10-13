package store_test

// Inmutabilidad de nexo_kb_versions y nexo_kb_events en el modo de dos
// cuentas (NX-07): la cuenta de aplicación publica normalmente (INSERT en
// versiones y eventos, UPDATE en documentos y secciones) pero no puede
// modificar ni borrar versiones ni eventos; la cuenta dueña tampoco, por los
// triggers.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

func expectSQLState(t *testing.T, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, stmt, state string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), stmt)
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != state {
		t.Fatalf("%s: se esperaba SQLSTATE %s, got %v", stmt, state, err)
	}
}

func TestTwoAccountsVersionsAndEventsAreInsertOnly(t *testing.T) {
	env, ownerDSN := kbtest.TwoAccounts(t)
	ctx := context.Background()
	tm := env.NewTeam()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "lineamientos-frontend", Owner: "ana"}); err != nil {
		t.Fatalf("crear espacio con la cuenta de aplicación: %v", err)
	}
	doc := func(content string) service.BatchRequest {
		return service.BatchRequest{Documents: []service.PublishDoc{{DocKey: "frontend/estilos", Category: "convencion", Content: content}}}
	}
	if _, err := env.KB.PublishBatch(env.Ctx(), env.ID(tm.Ana), "lineamientos-frontend", doc("# Estilos\n\nUsar CSS Modules.\n")); err != nil {
		t.Fatal(err)
	}
	// Una versión nueva actualiza documentos y secciones (permitido) e
	// inserta versión y eventos (permitido).
	res, err := env.KB.PublishBatch(env.Ctx(), env.ID(tm.Ana), "lineamientos-frontend", doc("# Estilos\n\nUsar Tailwind con tokens.\n"))
	if err != nil || res.Results[0].Version != 2 {
		t.Fatalf("publicar v2 con la cuenta de aplicación: %+v %v", res, err)
	}
	if _, err := env.KB.RetireDocument(env.Ctx(), env.ID(tm.Ana), "lineamientos-frontend", "frontend/estilos", "prueba"); err != nil {
		t.Fatalf("retirar con la cuenta de aplicación: %v", err)
	}

	for _, table := range []string{"nexo_kb_versions", "nexo_kb_events"} {
		for _, stmt := range []string{
			`UPDATE ` + table + ` SET id = id`,
			`DELETE FROM ` + table,
			`TRUNCATE ` + table,
			`ALTER TABLE ` + table + ` DISABLE TRIGGER ALL`,
		} {
			expectSQLState(t, env.DB, stmt, "42501") // sin privilegio (RS-02)
		}
	}
	// La cuenta dueña tiene privilegios, pero el trigger lo impide.
	owner, err := sql.Open("pgx", ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	for _, table := range []string{"nexo_kb_versions", "nexo_kb_events"} {
		for _, stmt := range []string{`UPDATE ` + table + ` SET id = id`, `DELETE FROM ` + table, `TRUNCATE ` + table + ` CASCADE`} {
			_, err := owner.ExecContext(ctx, stmt)
			if err == nil || !strings.Contains(err.Error(), "solo inserción") {
				t.Fatalf("dueña %s: %v", stmt, err)
			}
		}
	}
	var protected []string
	for _, table := range security.EvidenceTables {
		if strings.HasPrefix(table, "nexo_kb_") {
			protected = append(protected, table)
		}
	}
	if len(protected) != 4 { // versiones, eventos y, desde 2b, ejecuciones de publicación y corridas de evaluación
		t.Fatalf("security.EvidenceTables incluye versiones, eventos, publish_runs y eval_runs de NX-10: %v", protected)
	}
	if n := env.Count(`SELECT COUNT(*) FROM nexo_kb_versions`); n != 2 {
		t.Fatalf("versiones = %d", n)
	}
}
