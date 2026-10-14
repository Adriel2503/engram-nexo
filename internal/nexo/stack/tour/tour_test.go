package tour

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/ui"
)

func TestStackTour(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	u := ui.New(strings.NewReader("sí\nsí\nsí\n"), &out, true)
	if e := Run(context.Background(), u, &out, dir, "http://127.0.0.1:18490", "demo", "prueba", true); e != nil {
		t.Fatal(e)
	}
	if !Seen(dir) {
		t.Fatal("falta marcador")
	}
	for _, s := range []string{"memoria local", "nexo_search", "--copiar proponente", "--copiar publicador", "--copiar auditor", "operaciones"} {
		if !strings.Contains(out.String(), s) {
			t.Fatal("guion incompleto")
		}
	}
}
func TestStackTourInterrupted(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if Run(context.Background(), ui.New(strings.NewReader("no\n"), &out, true), &out, dir, "http://localhost", "demo", "", false) == nil || Seen(dir) {
		t.Fatal("marcó recorrido incompleto")
	}
}
