package ingest

import (
	"strings"
	"sync"
	"testing"
)

// Fold se llama desde varias rutas del servidor a la vez (publicación,
// importación, búsqueda). Una cadena de transformadores compartida de x/text
// tiene estado y fallaba con "slice bounds out of range" bajo concurrencia.
func TestFoldConcurrentUseIsSafe(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Política de reintentos y niveles de log", "Politica de reintentos y niveles de log"},
		{"Año nuevo: cigüeña, pingüino y canción", "Ano nuevo: ciguena, pinguino y cancion"},
		{"sin acentos", "sin acentos"},
		{strings.Repeat("Ñandú éxito ", 40), strings.Repeat("Nandu exito ", 40)},
	}
	const workers = 32
	const rounds = 400
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				c := cases[(w+i)%len(cases)]
				if got := Fold(c.in); got != c.want {
					select {
					case errs <- got:
					default:
					}
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for got := range errs {
		t.Fatalf("Fold concurrente devolvió un resultado distinto al esperado: %.80q", got)
	}
}
