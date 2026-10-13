package security

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestParseRateLimits(t *testing.T) {
	def, err := ParseRateLimits("")
	if err != nil || !def.Enabled || def.Limit(GroupSync) != (GroupLimit{IP: 600, Token: 600}) || def.Limit(GroupAdmin) != (GroupLimit{IP: 120, Token: 120}) ||
		def.Limit(GroupLogin) != (GroupLimit{IP: 20}) || def.Limit(GroupNexo) != (GroupLimit{IP: 300, Token: 300}) || def.Limit(GroupOther) != (GroupLimit{IP: 600}) {
		t.Fatalf("defaults: %+v %v", def, err)
	}
	off, err := ParseRateLimits(" OFF ")
	if err != nil || off.Enabled || off.Limit(GroupSync) != (GroupLimit{}) {
		t.Fatalf("off: %+v %v", off, err)
	}
	cfg, err := ParseRateLimits(`{"admin":{"ip":2400},"sync":{"token":0}}`)
	if err != nil || cfg.Limit(GroupAdmin) != (GroupLimit{IP: 2400, Token: 120}) || cfg.Limit(GroupSync) != (GroupLimit{IP: 600, Token: 0}) {
		t.Fatalf("parcial: %+v %v", cfg, err)
	}
	for _, bad := range []string{`{`, `{"nada":{"ip":1}}`, `{"sync":{"ip":-1}}`, `{"sync":{"rate":1}}`} {
		cfg, err := ParseRateLimits(bad)
		if err == nil || !cfg.Enabled || cfg.Limit(GroupSync).IP != 600 {
			t.Fatalf("%s: un valor inválido debe dar error y conservar los valores por defecto: %+v %v", bad, cfg, err)
		}
	}
	if !strings.Contains(def.String(), "sync=600/600") || off.String() != "desactivado" {
		t.Fatalf("String: %q %q", def.String(), off.String())
	}
}

func TestClassifyPath(t *testing.T) {
	cases := map[string]RateGroup{
		"GET /sync/pull":                  GroupSync,
		"POST /sync/mutations/push":       GroupSync,
		"GET /admin/users":                GroupAdmin,
		"GET /nexo/admin/audit":           GroupAdmin,
		"GET /nexo/authz/v1/me":           GroupAdmin,
		"POST /dashboard/login":           GroupLogin,
		"POST /dashboard/bootstrap":       GroupLogin,
		"GET /dashboard/login":            GroupOther,
		"GET /nexo/kb/v1/spaces":          GroupNexo,
		"GET /health":                     GroupOther,
		"POST /dashboard/admin/users/1/x": GroupOther,
	}
	for in, want := range cases {
		parts := strings.SplitN(in, " ", 2)
		if got := ClassifyPath(parts[0], parts[1]); got != want {
			t.Errorf("%s = %s, want %s", in, got, want)
		}
	}
}

func TestIPKey(t *testing.T) {
	if IPKey("10.0.0.1") != "10.0.0.1" || IPKey("::ffff:10.0.0.1") != "10.0.0.1" {
		t.Fatal("IPv4")
	}
	if a, b := IPKey("2001:db8:1:2:aaaa::1"), IPKey("2001:db8:1:2:bbbb::9"); a != b || a != "2001:db8:1:2::/64" {
		t.Fatalf("IPv6 por /64: %s %s", a, b)
	}
}

// CA-S04 (unidad): con el reloj quieto se permiten exactamente "límite"
// peticiones; luego 429 con espera; otra clave no se ve afectada; el balde se
// recarga al ritmo límite/60 por segundo.
func TestLimiterTokenBucket(t *testing.T) {
	clk := newFakeClock()
	l := NewLimiter(clk.Now)
	allowed := 0
	var wait time.Duration
	for i := 0; i < 1000; i++ {
		ok, w := l.Allow("sync|ip|10.0.0.1", 600)
		if ok {
			allowed++
		} else {
			wait = w
		}
	}
	if allowed != 600 {
		t.Fatalf("permitidas = %d, want 600", allowed)
	}
	if wait <= 0 || wait > 100*time.Millisecond+time.Millisecond {
		t.Fatalf("espera = %s, want ~100ms (600/min)", wait)
	}
	if RetryAfterSeconds(wait) != 1 {
		t.Fatalf("Retry-After = %d", RetryAfterSeconds(wait))
	}
	if ok, _ := l.Allow("sync|ip|10.0.0.2", 600); !ok {
		t.Fatal("otra IP no debe verse afectada")
	}
	clk.Add(time.Second) // 10 unidades
	n := 0
	for i := 0; i < 50; i++ {
		if ok, _ := l.Allow("sync|ip|10.0.0.1", 600); ok {
			n++
		}
	}
	if n != 10 {
		t.Fatalf("recarga en 1 s = %d, want 10", n)
	}
	if ok, _ := l.Allow("x", 0); !ok {
		t.Fatal("límite 0 = sin límite")
	}
}

// Las claves inactivas se borran (sin fugas de memoria) y el tope por
// fragmento acota la memoria ante muchas IP distintas.
func TestLimiterExpiresIdleKeysAndCapsMemory(t *testing.T) {
	clk := newFakeClock()
	l := NewLimiter(clk.Now)
	for i := 0; i < 5000; i++ {
		l.Allow(fmt.Sprintf("other|ip|10.1.%d.%d", i/250, i%250), 600)
	}
	if l.Len() != 5000 {
		t.Fatalf("claves = %d", l.Len())
	}
	clk.Add(limiterIdle)
	l.Sweep()
	if l.Len() != 0 {
		t.Fatalf("claves inactivas no expiraron: %d", l.Len())
	}
	// Expiración perezosa en el propio Allow (sin Sweep explícito).
	for i := 0; i < 1000; i++ {
		l.Allow(fmt.Sprintf("other|ip|10.2.%d.%d", i/250, i%250), 600)
	}
	clk.Add(2 * limiterIdle)
	for i := 0; i < limiterShards*20; i++ {
		l.Allow(fmt.Sprintf("other|ip|10.3.%d.%d", i/250, i%250), 600)
	}
	if got := l.Len(); got > limiterShards*20 {
		t.Fatalf("la expiración perezosa dejó %d claves", got)
	}
	// Tope duro.
	capped := NewLimiter(clk.Now)
	capped.maxKeys = 50
	for i := 0; i < 20000; i++ {
		capped.Allow(fmt.Sprintf("k%d", i), 600)
	}
	if got := capped.Len(); got > limiterShards*50 {
		t.Fatalf("tope de memoria superado: %d", got)
	}
}

// Concurrencia (go test -race): con el reloj quieto, 64 goroutines sobre la
// misma clave obtienen exactamente la capacidad.
func TestLimiterConcurrent(t *testing.T) {
	clk := newFakeClock()
	l := NewLimiter(clk.Now)
	var ok int64
	var wg sync.WaitGroup
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if allowed, _ := l.Allow("admin|token|42", 120); allowed {
					atomic.AddInt64(&ok, 1)
				}
				l.Allow(fmt.Sprintf("admin|ip|10.9.%d.%d", g, i), 120)
			}
		}(g)
	}
	wg.Wait()
	if ok != 120 {
		t.Fatalf("permitidas en concurrencia = %d, want 120", ok)
	}
}

func TestRejectionLogAggregatesPerMinuteAndKey(t *testing.T) {
	clk := newFakeClock()
	r := NewRejectionLog(clk.Now)
	for i := 0; i < 300; i++ {
		r.Add(GroupSync, "ip", "10.0.0.1", "")
	}
	r.Add(GroupAdmin, "token", "12", "7")
	if got := r.Flush(false); len(got) != 0 {
		t.Fatalf("el minuto en curso no se entrega: %v", got)
	}
	clk.Add(time.Minute)
	r.Add(GroupSync, "ip", "10.0.0.1", "")
	got := r.Flush(false)
	if len(got) != 2 || got[0].Group != GroupAdmin || got[0].Count != 1 || got[0].Principal != "7" || got[1].Count != 300 {
		t.Fatalf("resumen: %+v", got)
	}
	if rest := r.Flush(true); len(rest) != 1 || rest[0].Count != 1 || r.Pending() != 0 {
		t.Fatalf("minuto siguiente: %+v", rest)
	}
	// Tope de claves por minuto: el resto se suma en "otros".
	for i := 0; i < rejectionMaxKeys+10; i++ {
		r.Add(GroupSync, "ip", fmt.Sprintf("k%d", i), "")
	}
	all := r.Flush(true)
	if len(all) != rejectionMaxKeys+1 {
		t.Fatalf("claves = %d", len(all))
	}
}

// RNF-S1: el costo de una decisión del limitador es de microsegundos.
func BenchmarkLimiterAllow(b *testing.B) {
	l := NewLimiter(nil)
	keys := make([]string, 1024)
	for i := range keys {
		keys[i] = fmt.Sprintf("sync|ip|10.0.%d.%d", i/250, i%250)
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			l.Allow(keys[i%len(keys)], 1_000_000)
			i++
		}
	})
}

func TestLimiterOverheadBelowBudget(t *testing.T) {
	l := NewLimiter(nil)
	const n = 200000
	start := time.Now()
	for i := 0; i < n; i++ {
		l.Allow("sync|ip|10.0.0."+fmt.Sprint(i%200), 1_000_000)
	}
	per := time.Since(start) / n
	t.Logf("costo por decisión: %s", per)
	if per > 200*time.Microsecond {
		t.Fatalf("RNF-S1: %s por decisión supera 0,2 ms", per)
	}
}
