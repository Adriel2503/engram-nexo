package security

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// A2: el primero de cada clave se escribe, los repetidos de la ventana se
// cuentan y la ventana vencida deja un resumen; sin repetidos, la clave se
// olvida y el siguiente vuelve a escribirse completo.
func TestAuditDedupWindows(t *testing.T) {
	clk := newFakeClock()
	d := NewAuditDedup(10*time.Minute, clk.Now)
	k := DedupKey{Action: "sync.auth", Reason: "unknown_token", IP: "10.0.0.1", Fingerprint: "aa"}
	if w, p := d.Observe(k, DedupInfo{UserAgent: "a"}); !w || p != nil {
		t.Fatal("el primero se escribe")
	}
	for i := 0; i < 19; i++ {
		clk.Add(30 * time.Second)
		ua := "a"
		if i%2 == 0 {
			ua = "b"
		}
		if w, _ := d.Observe(k, DedupInfo{UserAgent: ua}); w {
			t.Fatalf("repetido %d se escribió", i)
		}
	}
	other := k
	other.IP = "10.0.0.2"
	if w, _ := d.Observe(other, DedupInfo{}); !w {
		t.Fatal("otra IP es otra clave")
	}
	if got := d.Flush(false); len(got) != 0 {
		t.Fatalf("antes de vencer no hay resumen: %+v", got)
	}
	clk.Add(31 * time.Second) // 10 min + 1 s desde el primero
	sums := d.Flush(false)
	if len(sums) != 1 || sums[0].Repeated != 19 || sums[0].Key != k || strings.Join(sums[0].UserAgents, ",") != "a,b" {
		t.Fatalf("resumen: %+v", sums)
	}
	// La siguiente ventana sigue contando sin otra fila completa.
	clk.Add(30 * time.Second)
	if w, p := d.Observe(k, DedupInfo{}); w || p != nil {
		t.Fatal("el episodio sigue: no hay fila completa nueva")
	}
	// Ventana vencida con repetidos: el próximo rechazo trae el resumen.
	clk.Add(11 * time.Minute)
	w, p := d.Observe(k, DedupInfo{})
	if w || p == nil || p.Repeated != 1 {
		t.Fatalf("resumen pendiente: %v %+v", w, p)
	}
	// Sin repetidos durante una ventana: se olvida y vuelve a escribirse.
	clk.Add(11 * time.Minute)
	d.Flush(false) // resumen de 1
	clk.Add(11 * time.Minute)
	d.Flush(false) // sin repetidos: se borra
	if w, _ := d.Observe(k, DedupInfo{}); !w {
		t.Fatal("después de un período sin rechazos vuelve a escribirse completo")
	}
	if off := NewAuditDedup(0, nil); off.Window() != 0 {
		t.Fatal("0 = desactivado")
	} else if w, _ := off.Observe(k, DedupInfo{}); !w {
		t.Fatal("desactivado: siempre se escribe")
	}
	if w, _ := d.Observe(DedupKey{Action: "sync.auth"}, DedupInfo{}); !w {
		t.Fatal("sin huella no se agrupa")
	}
}

func TestAuditDedupConcurrentAndEnv(t *testing.T) {
	d := NewAuditDedup(time.Hour, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	writes := 0
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				if w, _ := d.Observe(DedupKey{Action: "a", Reason: "r", IP: "ip", Fingerprint: "fp"}, DedupInfo{}); w {
					mu.Lock()
					writes++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	sums := d.Flush(true)
	if writes != 1 || len(sums) != 1 || sums[0].Repeated != 16*500-1 {
		t.Fatalf("concurrencia: %d escrituras, %+v", writes, sums)
	}
	t.Setenv(AuditDedupEnv, "")
	if AuditDedupWindow() != DefaultAuditDedupWindow {
		t.Fatal("por defecto 600 s")
	}
	t.Setenv(AuditDedupEnv, "0")
	if AuditDedupWindow() != 0 {
		t.Fatal("0 = sin agrupar")
	}
	t.Setenv(AuditDedupEnv, "abc")
	if AuditDedupWindow() != DefaultAuditDedupWindow {
		t.Fatal("inválido = por defecto")
	}
}

func TestCredentialFingerprint(t *testing.T) {
	a := CredentialFingerprint("egc_live_secreto")
	if len(a) != 16 || strings.Contains(a, "secreto") || a != CredentialFingerprint(" egc_live_secreto ") || a == CredentialFingerprint("egc_live_otro") {
		t.Fatalf("huella: %q", a)
	}
	if CredentialFingerprint("") != "" {
		t.Fatal("vacía")
	}
}

// A1: por defecto no hay bloqueo por persona.
func TestLockoutDefaultHasNoPerPersonLock(t *testing.T) {
	l := NewLockout(DefaultLockout(), newFakeClock().Now)
	for i := 0; i < 1000; i++ {
		if lockedNow, _, _ := l.Fail(LockoutUserKey("7")); lockedNow {
			t.Fatal("A1: el bloqueo por persona está desactivado por defecto")
		}
	}
	if locked, _ := l.Locked(LockoutUserKey("7")); locked {
		t.Fatal("persona bloqueada")
	}
	if !strings.Contains(DefaultLockout().String(), "sin bloqueo por persona") {
		t.Fatalf("log: %s", DefaultLockout())
	}
}
