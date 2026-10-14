package cloudstore

// NX-07 (revisión adversarial, punto 10): emisión, canje, revocación y auditoría
// con cuentas de base de datos SEPARADAS de migración y de aplicación. Se
// comprueba que la migración repetida es idempotente (incluida
// nexo_activation_codes y nexo_backfill_marks), los privilegios de cada cuenta y
// que el ciclo de la activación y los cruces del interbloqueo funcionan con la
// cuenta de aplicación, que no es dueña de nada. Postgres real.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

// nexoSchemaShape resume el esquema para comparar antes y después de migrar otra vez.
func nexoSchemaShape(t *testing.T, e *nexoTwoAccountEnv) string {
	t.Helper()
	rows, err := e.admin.QueryContext(context.Background(), `
		SELECT 'tabla ' || c.relname || ' dueña ' || pg_get_userbyid(c.relowner) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relkind = 'r'
		UNION ALL SELECT 'índice ' || indexname FROM pg_indexes WHERE schemaname = $1
		UNION ALL SELECT 'disparador ' || t.tgname || ' en ' || c.relname FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND NOT t.tgisinternal
		UNION ALL SELECT 'función ' || p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = $1
		UNION ALL SELECT 'columna ' || table_name || '.' || column_name || ' ' || data_type FROM information_schema.columns WHERE table_schema = $1
		ORDER BY 1`, e.schema)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var b strings.Builder
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		b.WriteString(s + "\n")
	}
	return b.String()
}

func TestNexoActivationTwoAccountsIdempotentMigrationAndPrivilegesPG(t *testing.T) {
	e := newNexoTwoAccountEnv(t)
	first := e.open(t)
	shape1 := nexoSchemaShape(t, e)
	for _, want := range []string{"tabla nexo_activation_codes dueña " + e.owner, "tabla nexo_backfill_marks dueña " + e.owner, "columna nexo_activation_codes.issued_ip text"} {
		if !strings.Contains(shape1, want) {
			t.Fatalf("la migración con dos cuentas no dejó %q", want)
		}
	}
	// Migración repetida: otro arranque con las mismas cuentas no cambia nada y no falla.
	_ = first.Close()
	second := e.open(t)
	if shape2 := nexoSchemaShape(t, e); shape2 != shape1 {
		t.Fatalf("la migración repetida cambió el esquema:\n%s", nexoShapeDiff(shape1, shape2))
	}
	// Una pasada marcada del relleno de sesiones no se repite en el segundo arranque.
	var marks int
	if err := e.admin.QueryRowContext(context.Background(), `SELECT count(*) FROM `+e.q("nexo_backfill_marks")).Scan(&marks); err != nil || marks != 1 {
		t.Fatalf("marca del relleno: %d %v", marks, err)
	}

	// Privilegios: la aplicación usa las tablas pero no las altera ni toma el rol de la dueña.
	ctx := context.Background()
	for _, table := range []string{"nexo_activation_codes", "nexo_backfill_marks"} {
		for _, priv := range []string{"SELECT", "INSERT", "UPDATE"} {
			var ok bool
			if err := e.admin.QueryRowContext(ctx, `SELECT has_table_privilege($1, $2, $3)`, e.app, e.q(table), priv).Scan(&ok); err != nil || !ok {
				t.Errorf("la aplicación debía tener %s sobre %s: %v %v", priv, table, ok, err)
			}
		}
		for _, stmt := range []string{`ALTER TABLE ` + table + ` ADD COLUMN intruso text`, `DROP TABLE ` + table, `ALTER TABLE ` + table + ` OWNER TO ` + e.app, `TRUNCATE ` + table} {
			if table == "nexo_backfill_marks" && strings.HasPrefix(stmt, "TRUNCATE") {
				continue
			}
			nexoExpectDenied(t, second.db, stmt)
		}
	}

	// Ciclo completo con la cuenta de aplicación: emisión, revocación, canje y la cadena de auditoría íntegra.
	env := newNexoLockEnvOn(t, second, 4)
	ctx2, cancel := context.WithTimeout(ctx, 60e9)
	defer cancel()
	idRevoke, _, err := env.issue(ctx2, env.b, env.a)
	if err != nil {
		t.Fatalf("emisión con la cuenta de aplicación: %v", err)
	}
	if ok, err := second.NexoRevokeActivation(ctx2, idRevoke, env.b.PrincipalID, AuthAuditEvent{ActorPrincipalID: env.a.PrincipalID, ActorSource: "test", TargetPrincipalID: env.b.PrincipalID, Outcome: "success"}); err != nil || !ok {
		t.Fatalf("revocación: %v %v", ok, err)
	}
	_, hash, err := env.issue(ctx2, env.a, env.b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.NexoRedeemActivation(ctx2, hash, CreatePrincipalTokenParams{TokenPrefix: "egc_live_" + nexoRandHex(4), TokenHash: "th-" + nexoRandHex(12)}, NexoRedeemCaller{}); err != nil {
		t.Fatalf("canje: %v", err)
	}
	if _, err := second.NexoRedeemActivation(ctx2, hash, CreatePrincipalTokenParams{TokenPrefix: "egc_live_" + nexoRandHex(4), TokenHash: "th-" + nexoRandHex(12)}, NexoRedeemCaller{}); err == nil {
		t.Fatal("un código usado no debía canjearse otra vez")
	}
	report, err := evidence.VerifyAll(ctx2, second.db, []string{"cloud_auth_audit_log"})
	if err != nil || !report.OK {
		t.Fatalf("la cadena de auditoría debía verificar tras el ciclo: %+v %v", report, err)
	}
	for _, action := range []string{"nexo.activation.issued", "nexo.activation.revoked", "nexo.activation.redeemed", "nexo.activation.failed"} {
		var n int
		if err := e.admin.QueryRowContext(ctx, `SELECT count(*) FROM `+e.q("cloud_auth_audit_log")+` WHERE action = $1`, action).Scan(&n); err != nil || n == 0 {
			t.Errorf("sin auditoría %s: %d %v", action, n, err)
		}
	}
	// Los cruces del interbloqueo, con la cuenta de aplicación y pool de 4 y 25.
	for _, pool := range []int{4, 25} {
		pe := newNexoLockEnvOn(t, second, pool)
		ops := pe.ops(ctx2)
		met, errs := pe.runPair(ops[0], ops[1], true)
		if !met || errs[0] != "" || errs[1] != "" {
			t.Fatalf("pool %d, emisiones recíprocas con cuenta de aplicación: met=%v %q %q", pool, met, errs[0], errs[1])
		}
		met, errs = pe.runPair(pe.ops(ctx2)[2], pe.ops(ctx2)[3], true)
		if !met || errs[0] != "" || errs[1] != "" {
			t.Fatalf("pool %d, revocaciones cruzadas con cuenta de aplicación: met=%v %q %q", pool, met, errs[0], errs[1])
		}
	}
}

func nexoShapeDiff(a, b string) string {
	seen := map[string]bool{}
	for _, l := range strings.Split(a, "\n") {
		seen[l] = true
	}
	var out []string
	for _, l := range strings.Split(b, "\n") {
		if !seen[l] {
			out = append(out, "+ "+l)
		}
	}
	seen = map[string]bool{}
	for _, l := range strings.Split(b, "\n") {
		seen[l] = true
	}
	for _, l := range strings.Split(a, "\n") {
		if !seen[l] {
			out = append(out, "- "+l)
		}
	}
	return fmt.Sprint(out)
}
