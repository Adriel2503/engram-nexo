package security

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func TestParseLockout(t *testing.T) {
	def, err := ParseLockout("")
	if err != nil || def != DefaultLockout() || def.MaxFailures != 20 || def.Window != 5*time.Minute || def.Duration != 15*time.Minute {
		t.Fatalf("defaults: %+v %v", def, err)
	}
	if off, err := ParseLockout("off"); err != nil || off.Enabled {
		t.Fatalf("off: %+v %v", off, err)
	}
	cfg, err := ParseLockout(`{"max_failures":5,"user_max_failures":0,"window_seconds":60,"lockout_seconds":30}`)
	if err != nil || cfg.MaxFailures != 5 || cfg.UserMaxFailures != 0 || cfg.Window != time.Minute || cfg.Duration != 30*time.Second {
		t.Fatalf("json: %+v %v", cfg, err)
	}
	for _, bad := range []string{`{`, `{"max_failures":0}`, `{"otra":1}`, `{"window_seconds":0}`} {
		if cfg, err := ParseLockout(bad); err == nil || cfg != DefaultLockout() {
			t.Fatalf("%s: %+v %v", bad, cfg, err)
		}
	}
}

// CA-S05 (unidad): 20 fallos en 5 minutos bloquean 15 minutos; un único
// evento al iniciar el bloqueo; al vencer, se vuelve a contar desde cero.
func TestLockoutBlocksAfterThreshold(t *testing.T) {
	clk := newFakeClock()
	l := NewLockout(DefaultLockout(), clk.Now)
	key := LockoutIPKey("10.0.0.7")
	starts := 0
	for i := 1; i <= 25; i++ {
		lockedNow, failures, until := l.Fail(key)
		if lockedNow {
			starts++
			if i != 20 || failures != 20 || !until.Equal(clk.Now().Add(15*time.Minute)) {
				t.Fatalf("bloqueo en el fallo %d (%d) hasta %s", i, failures, until)
			}
		}
	}
	if starts != 1 {
		t.Fatalf("eventos de bloqueo = %d, want 1", starts)
	}
	if locked, wait := l.Locked(key); !locked || wait != 15*time.Minute {
		t.Fatalf("bloqueada = %v %s", locked, wait)
	}
	if locked, _ := l.Locked(LockoutIPKey("10.0.0.8")); locked {
		t.Fatal("otra IP no se bloquea")
	}
	clk.Add(15 * time.Minute)
	if locked, _ := l.Locked(key); locked {
		t.Fatal("el bloqueo vence")
	}
	if lockedNow, failures, _ := l.Fail(key); lockedNow || failures != 1 {
		t.Fatalf("después del bloqueo se cuenta desde cero: %d", failures)
	}
}

func TestLockoutWindowAndUserThreshold(t *testing.T) {
	clk := newFakeClock()
	cfg := DefaultLockout()
	cfg.UserMaxFailures = 0
	l := NewLockout(cfg, clk.Now)
	key := LockoutIPKey("10.0.0.9")
	for i := 0; i < 19; i++ {
		l.Fail(key)
	}
	clk.Add(5 * time.Minute) // la ventana vence: 19 fallos viejos no cuentan
	if lockedNow, failures, _ := l.Fail(key); lockedNow || failures != 1 {
		t.Fatalf("ventana: %d", failures)
	}
	for i := 0; i < 100; i++ {
		if lockedNow, _, _ := l.Fail(LockoutUserKey("7")); lockedNow {
			t.Fatal("user_max_failures=0 desactiva el bloqueo por usuario")
		}
	}
	off := NewLockout(LockoutConfig{Enabled: false}, clk.Now)
	for i := 0; i < 100; i++ {
		off.Fail(key)
	}
	if locked, _ := off.Locked(key); locked {
		t.Fatal("desactivado no bloquea")
	}
}

func TestLockoutExpiresStatesAndIsConcurrent(t *testing.T) {
	clk := newFakeClock()
	l := NewLockout(DefaultLockout(), clk.Now)
	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				l.Fail(LockoutIPKey(fmt.Sprintf("10.%d.%d.1", g, i)))
				l.Fail(LockoutIPKey("10.255.255.255"))
				l.Locked(LockoutIPKey("10.255.255.255"))
			}
		}(g)
	}
	wg.Wait()
	if locked, _ := l.Locked(LockoutIPKey("10.255.255.255")); !locked {
		t.Fatal("3200 fallos concurrentes deben bloquear")
	}
	clk.Add(16 * time.Minute)
	for g := 0; g < 2000; g++ { // un fallo nuevo por fragmento dispara la limpieza
		l.Fail(LockoutIPKey(fmt.Sprintf("172.16.%d.%d", g/250, g%250)))
	}
	if n := l.Len(); n > 2000 {
		t.Fatalf("estados vencidos no se borraron: %d", n)
	}
}

func TestClassifyAuthFailure(t *testing.T) {
	cases := map[string]string{
		"unknown_token": FailureIP, "malformed_bearer": FailureIP, "invalid_token": FailureIP,
		"token_revoked": FailureUser, "principal_disabled": FailureUser,
		"token_expired": FailureNone, "missing_header": FailureNone, "resolver_error": FailureNone, "pepper_missing": FailureNone,
	}
	for reason, want := range cases {
		if got := ClassifyAuthFailure(reason); got != want {
			t.Errorf("%s = %q, want %q", reason, got, want)
		}
	}
	if LockoutIPKey("") != "" || LockoutUserKey(" ") != "" || LockoutUserKey("7") != "user:7" {
		t.Fatal("claves")
	}
}

func TestResolveIssue(t *testing.T) {
	t.Setenv(TokenMaxDaysEnv, "")
	days := func(n int) *int { return &n }
	ok := func(req IssueRequest, kind string, super bool, wantDays int, wantScope string) {
		t.Helper()
		got, err := ResolveIssue(req, kind, super)
		if err != nil || got.Days != wantDays || got.Scope != wantScope {
			t.Fatalf("%+v: %+v %v", req, got, err)
		}
	}
	bad := func(req IssueRequest, kind string, super bool, status int, code string) {
		t.Helper()
		_, err := ResolveIssue(req, kind, super)
		if err == nil || err.Status != status || err.Code != code {
			t.Fatalf("%+v: %v", req, err)
		}
	}
	ok(IssueRequest{}, "human", false, 90, "write")
	ok(IssueRequest{Scope: "READ", ExpiresInDays: days(30)}, "human", false, 30, "read")
	ok(IssueRequest{ExpiresInDays: days(365)}, "human", false, 365, "write")
	ok(IssueRequest{ExpiresInDays: days(0)}, "service_account", true, 0, "write")
	bad(IssueRequest{ExpiresInDays: days(366)}, "human", true, 400, "invalid_request")
	bad(IssueRequest{ExpiresInDays: days(-1)}, "human", true, 400, "invalid_request")
	bad(IssueRequest{Scope: "admin"}, "human", true, 400, "invalid_request")
	bad(IssueRequest{ExpiresInDays: days(0)}, "human", true, 400, "invalid_request")
	bad(IssueRequest{ExpiresInDays: days(0)}, "service_account", false, 403, "no_expiry_forbidden")
	t.Setenv(TokenMaxDaysEnv, "30")
	ok(IssueRequest{}, "human", false, 30, "write") // el defecto se acota al máximo
	bad(IssueRequest{ExpiresInDays: days(31)}, "human", true, 400, "invalid_request")
	t.Setenv(TokenMaxDaysEnv, "abc")
	if TokenMaxDays() != DefaultTokenMaxDays {
		t.Fatal("valor inválido = 365")
	}
}

func TestTokenContextHelpers(t *testing.T) {
	ctx := context.Background()
	RecordTokenFacts(ctx, TokenFacts{TokenID: "1"}) // sin contenedor: no hace nada
	if _, ok := TokenFactsFrom(ctx); ok {
		t.Fatal("sin contenedor no hay datos")
	}
	ctx = WithTokenFactsHolder(ctx)
	if WithTokenFactsHolder(ctx) != ctx {
		t.Fatal("el contenedor no se duplica")
	}
	RecordTokenFacts(ctx, TokenFacts{TokenID: "1", Scope: "read"})
	if f, ok := TokenFactsFrom(ctx); !ok || f.Scope != "read" {
		t.Fatalf("datos: %+v", f)
	}
	if got := TokenIssueFrom(context.Background()); got.Days != DefaultIssueDays() || got.Scope != "write" {
		t.Fatalf("defecto: %+v", got)
	}
	if got := TokenIssueFrom(WithTokenIssue(ctx, TokenIssue{Days: 0, Scope: "read"})); got.Days != 0 || got.Scope != "read" {
		t.Fatalf("pedido: %+v", got)
	}
}

func TestParseCIDRs(t *testing.T) {
	al := ParseCIDRs("10.0.0.0/8, 192.168.1.5 ;2001:db8::/32 basura 300.1.1.1/8")
	if !al.Configured || len(al.Prefixes) != 3 || len(al.Invalid) != 2 {
		t.Fatalf("%+v", al)
	}
	if al.Prefixes[1] != netip.MustParsePrefix("192.168.1.5/32") {
		t.Fatalf("IP suelta = /32: %s", al.Prefixes[1])
	}
	if empty := ParseCIDRs("  "); empty.Configured || empty.String() != "sin restricción" {
		t.Fatalf("vacía: %+v", empty)
	}
	if onlyBad := ParseCIDRs("basura"); !onlyBad.Configured || len(onlyBad.Prefixes) != 0 {
		t.Fatalf("solo inválidas = configurada sin redes (niega todo): %+v", onlyBad)
	}
	t.Setenv(SuperadminAllowedCIDRsEnv, "10.1.0.0/16")
	if got := SuperadminAllowlist(); !got.Configured || got.Prefixes[0].String() != "10.1.0.0/16" {
		t.Fatalf("entorno: %+v", got)
	}
}
