package cloudstore

// NX-07 · orden de bloqueos de la activación (revisión adversarial, punto 6).
//
// Emisión, revocación y canje bloquean a la persona destinataria y después
// insertan filas cuya clave foránea necesita KEY SHARE sobre OTRA persona (el
// emisor o el actor de la auditoría). Con FOR UPDATE, dos administradores que
// se emiten códigos recíprocos (o revocaciones y canjes cruzados) esperan cada
// uno el bloqueo del otro: PostgreSQL aborta una con 40P01. Con FOR NO KEY
// UPDATE el KEY SHARE no espera y no hay ciclo.
//
// Las pruebas son deterministas: nexoActivationLockedHook retiene a cada
// operación, con su bloqueo ya tomado, hasta que la otra también lo tiene.
// Después se sueltan juntas. Se repiten con pool de 1, 4 y 25 conexiones: con 1
// no pueden coincidir dos transacciones, y la prueba comprueba que nada se cuelga.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type nexoLockEnv struct {
	t    *testing.T
	cs   *CloudStore
	a, b nexoSeededUser
}

func newNexoLockEnv(t *testing.T, pool int) *nexoLockEnv {
	t.Helper()
	return newNexoLockEnvOn(t, openIsolatedCloudStore(t), pool)
}

// newNexoLockEnvOn arma el entorno sobre un almacén ya abierto (por ejemplo con dos cuentas de base de datos).
func newNexoLockEnvOn(t *testing.T, cs *CloudStore, pool int) *nexoLockEnv {
	t.Helper()
	cs.db.SetMaxOpenConns(pool)
	sfx := nexoRandHex(3)
	return &nexoLockEnv{t: t, cs: cs, a: nexoSeedUser(t, cs, "adm-a-"+sfx, "a-"+sfx+"@rp.pe"), b: nexoSeedUser(t, cs, "adm-b-"+sfx, "b-"+sfx+"@rp.pe")}
}

func nexoRandHex(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

// issue emite un código para target firmado por actor y devuelve su id y su hash.
func (e *nexoLockEnv) issue(ctx context.Context, target, actor nexoSeededUser) (id, hash string, err error) {
	hash = "lockhash-" + nexoRandHex(12)
	a, err := e.cs.NexoIssueActivation(ctx, target.PrincipalID, hash, "bloqueos", "", AuthAuditEvent{ActorPrincipalID: actor.PrincipalID, ActorSource: "test", TargetPrincipalID: target.PrincipalID, Outcome: "success"})
	return a.ID, hash, err
}

// nexoBarrier retiene a "need" operaciones hasta que todas llegan (met) o vence el plazo.
type nexoBarrier struct {
	need    int32
	arrived atomic.Int32
	done    chan struct{}
	once    sync.Once
	met     atomic.Bool
	timeout time.Duration
}

func newNexoBarrier(need int, timeout time.Duration) *nexoBarrier {
	return &nexoBarrier{need: int32(need), done: make(chan struct{}), timeout: timeout}
}

func (b *nexoBarrier) arrive() {
	if b.arrived.Add(1) >= b.need {
		b.met.Store(true)
		b.once.Do(func() { close(b.done) })
		return
	}
	select {
	case <-b.done:
	case <-time.After(b.timeout):
	}
}

// nexoLockOp es una operación lista para ejecutarse (los códigos que consume ya existen).
type nexoLockOp struct {
	name string
	hook bool // la operación llama a nexoActivationLockedHook
	run  func(ctx context.Context) error
}

func (e *nexoLockEnv) ops(ctx context.Context) []nexoLockOp {
	t := e.t
	must := func(id, hash string, err error) (string, string) {
		t.Helper()
		if err != nil {
			t.Fatalf("preparar código: %v", err)
		}
		return id, hash
	}
	idAB, _ := must(e.issue(ctx, e.b, e.a)) // para B, emitido por A (se revoca)
	idBA, _ := must(e.issue(ctx, e.a, e.b)) // para A, emitido por B (se revoca)
	_, hashB := must(e.issue(ctx, e.b, e.a))
	_, hashA := must(e.issue(ctx, e.a, e.b))
	_, hashBSelf := must(e.issue(ctx, e.b, e.a))
	redeem := func(hash string, caller NexoRedeemCaller) func(context.Context) error {
		return func(ctx context.Context) error {
			_, err := e.cs.NexoRedeemActivation(ctx, hash, CreatePrincipalTokenParams{TokenPrefix: "egc_live_" + nexoRandHex(4), TokenHash: "th-" + nexoRandHex(12)}, caller)
			return err
		}
	}
	revoke := func(id string, target, actor nexoSeededUser) func(context.Context) error {
		return func(ctx context.Context) error {
			_, err := e.cs.NexoRevokeActivation(ctx, id, target.PrincipalID, AuthAuditEvent{ActorPrincipalID: actor.PrincipalID, ActorSource: "test", TargetPrincipalID: target.PrincipalID, Outcome: "success"})
			return err
		}
	}
	issue := func(target, actor nexoSeededUser) func(context.Context) error {
		return func(ctx context.Context) error { _, _, err := e.issue(ctx, target, actor); return err }
	}
	return []nexoLockOp{
		{"emitir A->B", true, issue(e.b, e.a)},
		{"emitir B->A", true, issue(e.a, e.b)},
		{"revocar de B (emisor A)", true, revoke(idAB, e.b, e.a)},
		{"revocar de A (emisor B)", true, revoke(idBA, e.a, e.b)},
		{"canjear de B", true, redeem(hashB, NexoRedeemCaller{})},
		{"canjear de A", true, redeem(hashA, NexoRedeemCaller{})},
		// El emisor A presenta el código de B: se rechaza y la auditoría lo atribuye a A (KEY SHARE sobre A).
		{"canje de B presentado por A", true, redeem(hashBSelf, NexoRedeemCaller{PrincipalID: e.a.PrincipalID})},
		{"habilitar A", false, func(ctx context.Context) error { return e.cs.SetHumanUserEnabled(ctx, e.a.PrincipalID, true) }},
		{"habilitar B", false, func(ctx context.Context) error { return e.cs.SetHumanUserEnabled(ctx, e.b.PrincipalID, true) }},
		{"token de Engram para A creado por B", false, func(ctx context.Context) error {
			_, err := e.cs.CreatePrincipalTokenWithAudit(ctx, CreatePrincipalTokenParams{PrincipalID: e.a.PrincipalID, TokenPrefix: "egc_live_" + nexoRandHex(4), TokenHash: "tk-" + nexoRandHex(12), Name: "t", CreatedByPrincipalID: e.b.PrincipalID},
				AuthAuditEvent{ActorPrincipalID: e.b.PrincipalID, ActorSource: "test", TargetPrincipalID: e.a.PrincipalID, Action: "token.create", Outcome: "success"})
			return err
		}},
	}
}

// nexoLockErr clasifica un error: los rechazos de negocio son válidos; un
// interbloqueo (40P01), un fallo de serialización o cualquier otro no lo son.
func nexoLockErr(err error) string {
	if err == nil {
		return ""
	}
	var denial *NexoActivationError
	if errors.As(err, &denial) {
		return ""
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return "SQLSTATE " + pg.Code + ": " + pg.Message
	}
	return err.Error()
}

// runPair ejecuta dos operaciones a la vez. barrier: ambas con el bloqueo tomado antes de seguir.
func (e *nexoLockEnv) runPair(a, b nexoLockOp, barrier bool) (met bool, errs [2]string) {
	bar := newNexoBarrier(2, 400*time.Millisecond)
	if barrier {
		nexoActivationLockedHook = func(string) { bar.arrive() }
		defer func() { nexoActivationLockedHook = nil }()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, op := range []nexoLockOp{a, b} {
		wg.Add(1)
		go func(i int, op nexoLockOp) {
			defer wg.Done()
			errs[i] = nexoLockErr(op.run(ctx))
			if errs[i] == "" && ctx.Err() != nil {
				errs[i] = "plazo vencido"
			}
		}(i, op)
	}
	wg.Wait()
	return bar.met.Load(), errs
}

// Emisiones recíprocas de dos superadmins: caso reportado por las dos revisiones.
func TestNexoActivationReciprocalIssueNoDeadlockPG(t *testing.T) {
	for _, pool := range []int{4, 25} {
		t.Run(fmt.Sprintf("pool_%d", pool), func(t *testing.T) {
			e := newNexoLockEnv(t, pool)
			ops := e.ops(context.Background())
			for round := 0; round < 5; round++ {
				met, errs := e.runPair(ops[0], ops[1], true)
				if !met {
					t.Fatal("la prueba no es determinista: las dos emisiones no retuvieron su bloqueo a la vez")
				}
				if errs[0] != "" || errs[1] != "" {
					t.Fatalf("ronda %d: emisiones recíprocas fallaron: %q / %q", round, errs[0], errs[1])
				}
			}
		})
	}
}

// Revocaciones cruzadas y la combinación emisión/revocación con actores y destinatarios cruzados.
func TestNexoActivationCrossRevokeAndMixedNoDeadlockPG(t *testing.T) {
	for _, pool := range []int{4, 25} {
		t.Run(fmt.Sprintf("pool_%d", pool), func(t *testing.T) {
			e := newNexoLockEnv(t, pool)
			cases := []struct{ a, b int }{{2, 3}, {0, 3}, {1, 2}, {0, 2}, {1, 3}, {4, 5}, {4, 1}, {5, 0}, {6, 1}, {6, 3}, {6, 5}}
			for _, c := range cases {
				ops := e.ops(context.Background())
				met, errs := e.runPair(ops[c.a], ops[c.b], true)
				if !met {
					t.Fatalf("%s / %s: no retuvieron el bloqueo a la vez", ops[c.a].name, ops[c.b].name)
				}
				if errs[0] != "" || errs[1] != "" {
					t.Fatalf("%s / %s: %q / %q", ops[c.a].name, ops[c.b].name, errs[0], errs[1])
				}
			}
		})
	}
}

// Todas las parejas posibles (también con operaciones de Engram sobre la misma persona).
func TestNexoActivationAllPairsNoDeadlockPG(t *testing.T) {
	if testing.Short() {
		t.Skip("matriz completa")
	}
	for _, pool := range []int{1, 4, 25} {
		t.Run(fmt.Sprintf("pool_%d", pool), func(t *testing.T) {
			e := newNexoLockEnv(t, pool)
			n := len(e.ops(context.Background()))
			for i := 0; i < n; i++ {
				for j := i; j < n; j++ {
					ops := e.ops(context.Background())
					// Con pool 1 no pueden coincidir dos transacciones: el gancho esperaría en vano.
					_, errs := e.runPair(ops[i], ops[j], pool > 1)
					if errs[0] != "" || errs[1] != "" {
						t.Errorf("%s / %s: %q / %q", ops[i].name, ops[j].name, errs[0], errs[1])
					}
				}
			}
		})
	}
}

// Ráfaga con actores y destinatarios cruzados: emisiones, revocaciones y canjes mezclados.
func TestNexoActivationMixedBurstPG(t *testing.T) {
	for _, pool := range []int{1, 4, 25} {
		t.Run(fmt.Sprintf("pool_%d", pool), func(t *testing.T) {
			e := newNexoLockEnv(t, pool)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			var wg sync.WaitGroup
			var mu sync.Mutex
			var failures []string
			for r := 0; r < 6; r++ {
				for _, op := range e.ops(ctx) {
					wg.Add(1)
					go func(op nexoLockOp) {
						defer wg.Done()
						if msg := nexoLockErr(op.run(ctx)); msg != "" {
							mu.Lock()
							failures = append(failures, op.name+": "+msg)
							mu.Unlock()
						}
					}(op)
				}
			}
			wg.Wait()
			if ctx.Err() != nil {
				t.Fatal("la ráfaga no terminó a tiempo (posible bloqueo)")
			}
			if len(failures) > 0 {
				t.Fatalf("%d operaciones fallaron, p. ej.: %s", len(failures), strings.Join(failures[:min(3, len(failures))], " | "))
			}
		})
	}
}
