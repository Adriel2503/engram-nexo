package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// Fuerza el ciclo mutex/pool: una consulta fría espera la única conexión
// retenida por tx, mientras tx necesita IndexOpts y el modo del mismo store.
func TestNexoOP13ColdModeDoesNotLockPoolWait(t *testing.T) {
	env := kbtest.New(t)
	st := store.New(env.DB)
	env.DB.SetMaxOpenConns(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx, err := env.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	before := env.DB.Stats().WaitCount
	waiting := make(chan error, 1)
	go func() { _, err := st.FoldInApp(ctx); waiting <- err }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for env.DB.Stats().WaitCount == before {
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("la consulta fría no llegó a esperar el pool")
		}
	}
	done := make(chan error, 1)
	go func() {
		_ = st.IndexOpts()
		sqlText, err := st.UnaccentVector(ctx, tx, "'validación'", "'validacion'")
		if err == nil && sqlText == "" {
			err = fmt.Errorf("modo sin expresión SQL")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil || ctx.Err() != nil {
			t.Fatalf("modo en tx falló: %v", err)
		}
	case <-timer.C:
		cancel()
		_ = tx.Rollback()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		select {
		case <-waiting:
		case <-time.After(3 * time.Second):
		}
		t.Fatal("mutex retenido durante la espera del pool o consulta fuera de tx")
	}
	if env.DB.Stats().WaitCount != before+1 {
		t.Fatal("la resolución transaccional pidió otra conexión")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waiting:
		if err != nil {
			t.Fatalf("consulta fría falló después de liberar tx: %v", err)
		}
	case <-timer.C:
		t.Fatal("la consulta fría no terminó tras liberar tx")
	}
	if n := env.DB.Stats().InUse; n != 0 {
		t.Fatalf("conexiones retenidas: %d", n)
	}
}
