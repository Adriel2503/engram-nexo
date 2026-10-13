package evidence_test

// S4-01 (RS-30, CA-S09, RNF-S2, RNF-S3) y S4-05 (RS-34) contra Postgres real.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

func insertAudit(t testing.TB, cs *cloudstore.CloudStore, n int, action string) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := cs.InsertAuthAuditEvent(context.Background(), cloudstore.AuthAuditEvent{
			ActorSource: "request", Action: action, Outcome: "success", Metadata: map[string]any{"i": i, "nota": "ñandú \"x\""},
		}); err != nil {
			t.Fatalf("insert audit: %v", err)
		}
	}
}

func adminExec(t testing.TB, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func verify(t testing.TB, db *sql.DB, table string) evidence.TableReport {
	t.Helper()
	rep, err := evidence.VerifyChain(context.Background(), db, table)
	if err != nil {
		t.Fatalf("verify %s: %v", table, err)
	}
	return rep
}

func idsOf(t testing.TB, db *sql.DB, table string) []int64 {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM ` + table + ` WHERE nexo_chain_seq IS NOT NULL ORDER BY nexo_chain_seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// La cadena se forma en las tres tablas y la verificación pasa.
func TestChainLinksAllTables(t *testing.T) {
	env := kbtest.New(t)
	insertAudit(t, env.CS, 5, "test.event")
	env.NewTeam() // eventos de roles
	env.Exec(`INSERT INTO nexo_kb_events (space_slug, action, actor_label, details) VALUES ('s', 'space.created', 'x', '{"b":1,"a":[1,2]}'), ('s', 'doc.published', 'y', NULL)`)
	if n, err := evidence.Checkpoint(context.Background(), env.DB, "test"); err != nil || n != 3 {
		t.Fatalf("puntos de control: %d %v", n, err)
	}
	rep, err := evidence.VerifyAll(context.Background(), env.DB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || len(rep.Tables) != 3 {
		t.Fatalf("verificación: %+v", rep)
	}
	for _, tr := range rep.Tables {
		if tr.Checked == 0 || tr.LastSeq != tr.CheckpointSeq || tr.FirstSeq != 1 || tr.Checkpoints != 1 {
			t.Fatalf("%s: %+v", tr.Table, tr)
		}
	}
	// La aplicación no puede imponer sus propios hashes ni su posición.
	last := verify(t, env.DB, "cloud_auth_audit_log").LastSeq
	env.Exec(`INSERT INTO cloud_auth_audit_log (actor_source, action, outcome, nexo_chain_seq, nexo_prev_hash, nexo_hash) VALUES ('x', 'y', 'z', 999, '\x00', '\x00')`)
	if r := verify(t, env.DB, "cloud_auth_audit_log"); !r.OK || r.LastSeq != last+1 {
		t.Fatalf("la cadena debe ignorar valores impuestos: %+v", r)
	}
	// Un segundo punto de control solo si la cadena avanzó.
	if n, _ := evidence.Checkpoint(context.Background(), env.DB, "test"); n != 1 {
		t.Fatalf("punto de control nuevo: %d", n)
	}
	if n, _ := evidence.Checkpoint(context.Background(), env.DB, "test"); n != 0 {
		t.Fatalf("sin cambios no hay punto de control: %d", n)
	}
}

// CA-S09: alterar una fila con la cuenta dueña (desactivando la guarda) hace
// fallar la verificación e indica la fila; borrar en el medio o al final
// deja huecos; insertar con el trigger desactivado deja filas sin cadena.
func TestChainDetectsTampering(t *testing.T) {
	env := kbtest.New(t)
	insertAudit(t, env.CS, 10, "test.event")
	ids := idsOf(t, env.DB, "cloud_auth_audit_log")
	if r := verify(t, env.DB, "cloud_auth_audit_log"); !r.OK {
		t.Fatalf("antes: %+v", r)
	}
	// La guarda impide UPDATE y DELETE incluso a la dueña.
	if _, err := env.DB.Exec(`UPDATE cloud_auth_audit_log SET reason_code = 'x' WHERE id = $1`, ids[3]); err == nil || !strings.Contains(err.Error(), "solo inserci") {
		t.Fatalf("UPDATE debe fallar por la guarda: %v", err)
	}
	adminExec(t, env.Admin, `ALTER TABLE cloud_auth_audit_log DISABLE TRIGGER nexo_guard_cloud_auth_audit_log`)
	adminExec(t, env.Admin, `UPDATE cloud_auth_audit_log SET reason_code = 'alterado' WHERE id = $1`, ids[3])
	r := verify(t, env.DB, "cloud_auth_audit_log")
	if r.OK || r.FirstBroken == nil || r.FirstBroken.ID != ids[3] || r.FirstBroken.Kind != "content" || r.Altered != 1 {
		t.Fatalf("alteración: %+v %+v", r, r.FirstBroken)
	}
	// Borrar una fila del medio: hueco.
	adminExec(t, env.Admin, `DELETE FROM cloud_auth_audit_log WHERE id = $1`, ids[6])
	r = verify(t, env.DB, "cloud_auth_audit_log")
	if len(r.Gaps) != 1 || r.Gaps[0].Where != "middle" || r.Gaps[0].FromSeq != 7 || r.MissingRows != 1 {
		t.Fatalf("hueco del medio: %+v", r.Gaps)
	}
	// Borrar las últimas: hueco final contra el punto de control.
	if _, err := evidence.Checkpoint(context.Background(), env.DB, "test"); err != nil {
		t.Fatal(err)
	}
	adminExec(t, env.Admin, `DELETE FROM cloud_auth_audit_log WHERE id IN ($1, $2)`, ids[8], ids[9])
	r = verify(t, env.DB, "cloud_auth_audit_log")
	if r.MissingRows != 3 || r.Gaps[len(r.Gaps)-1].Where != "end" {
		t.Fatalf("hueco final: %+v", r.Gaps)
	}
	// Borrar la primera sin ancla: hueco al inicio.
	adminExec(t, env.Admin, `DELETE FROM cloud_auth_audit_log WHERE id = $1`, ids[0])
	r = verify(t, env.DB, "cloud_auth_audit_log")
	if r.Gaps[0].Where != "start" || r.Gaps[0].FromSeq != 1 || r.Gaps[0].ToSeq != 1 {
		t.Fatalf("hueco inicial: %+v", r.Gaps)
	}
	// Insertar con la cadena desactivada.
	adminExec(t, env.Admin, `ALTER TABLE cloud_auth_audit_log DISABLE TRIGGER zz_nexo_chain_cloud_auth_audit_log`)
	insertAudit(t, env.CS, 2, "sin.cadena")
	adminExec(t, env.Admin, `ALTER TABLE cloud_auth_audit_log ENABLE TRIGGER ALL`)
	r = verify(t, env.DB, "cloud_auth_audit_log")
	if r.Unchained != 2 {
		t.Fatalf("filas sin cadena: %+v", r)
	}
}

// Las filas anteriores a la migración quedan cubiertas por el ancla inicial.
func TestChainGenesisWithLegacyRows(t *testing.T) {
	env := kbtest.New(t)
	// Simula una base con auditoría previa: sin cabeza, sin ancla, filas sin
	// cadena; la migración siguiente arma el ancla.
	adminExec(t, env.Admin, `ALTER TABLE cloud_auth_audit_log DISABLE TRIGGER ALL`)
	insertAudit(t, env.CS, 3, "legado")
	adminExec(t, env.Admin, `ALTER TABLE cloud_auth_audit_log ENABLE TRIGGER ALL`)
	adminExec(t, env.Admin, `ALTER TABLE nexo_audit_chain_anchors DISABLE TRIGGER USER`)
	adminExec(t, env.Admin, `DELETE FROM nexo_audit_chain_anchors WHERE table_name = 'cloud_auth_audit_log'`)
	adminExec(t, env.Admin, `ALTER TABLE nexo_audit_chain_anchors ENABLE TRIGGER USER`)
	tx, err := env.Admin.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := evidence.Migrate(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	insertAudit(t, env.CS, 4, "nuevo")
	r := verify(t, env.DB, "cloud_auth_audit_log")
	if !r.OK || r.LegacyRows != 3 || r.Checked != 4 || r.Unchained != 0 {
		t.Fatalf("ancla con filas previas: %+v", r)
	}
	var legacy int64
	if err := env.DB.QueryRow(`SELECT legacy_rows FROM nexo_audit_chain_anchors WHERE table_name = 'cloud_auth_audit_log' AND kind = 'genesis'`).Scan(&legacy); err != nil || legacy != 3 {
		t.Fatalf("ancla: %d %v", legacy, err)
	}
}

// S4-05: la depuración borra solo filas más viejas que el plazo, respeta los
// mínimos, deja un ancla y la cadena sigue verificando.
func TestPurgeKeepsChainVerifiable(t *testing.T) {
	env := kbtest.New(t)
	ctx := context.Background()
	insertAudit(t, env.CS, 8, "test.event")
	ids := idsOf(t, env.DB, "cloud_auth_audit_log")
	// Envejece las tres primeras (400 días) y una del medio (desordenada: no
	// debe borrarse porque hay filas recientes antes que ella en la cadena).
	adminExec(t, env.Admin, `ALTER TABLE cloud_auth_audit_log DISABLE TRIGGER nexo_guard_cloud_auth_audit_log`)
	adminExec(t, env.Admin, `UPDATE cloud_auth_audit_log SET occurred_at = now() - interval '400 days' WHERE id IN (`+joinIDs(append(append([]int64(nil), ids[:3]...), ids[5]))+`)`)
	adminExec(t, env.Admin, `ALTER TABLE cloud_auth_audit_log ENABLE TRIGGER nexo_guard_cloud_auth_audit_log`)
	// La verificación recalcula hashes: envejecer altera; se reconstruye la
	// cadena para la prueba recalculando con la función canónica.
	rebuildChain(t, env.Admin, "cloud_auth_audit_log")
	if r := verify(t, env.DB, "cloud_auth_audit_log"); !r.OK {
		t.Fatalf("cadena reconstruida: %+v", r)
	}

	if _, err := evidence.Purge(ctx, env.DB, "cloud_auth_audit_log", 100); err == nil || !strings.Contains(err.Error(), "nexo_retention_minimum") {
		t.Fatalf("plazo menor al mínimo: %v", err)
	}
	if _, err := evidence.Purge(ctx, env.DB, "nexo_kb_versions", 400); err == nil || !strings.Contains(err.Error(), "nexo_retention_table") {
		t.Fatalf("tabla no depurable: %v", err)
	}
	// Una variable de sesión no habilita el borrado de filas recientes.
	if err := deleteWithPurgeFlag(env.DB, "cloud_auth_audit_log", ids[7]); err == nil || !strings.Contains(err.Error(), "solo inserci") {
		t.Fatalf("DELETE de una fila reciente con la variable de sesión debe fallar: %v", err)
	}
	n, err := evidence.Purge(ctx, env.DB, "cloud_auth_audit_log", 365)
	if err != nil || n != 3 {
		t.Fatalf("depuración: %d %v", n, err)
	}
	r := verify(t, env.DB, "cloud_auth_audit_log")
	if !r.OK || r.FirstSeq != 4 || r.PurgedAnchors != 1 {
		t.Fatalf("después de depurar: %+v", r)
	}
	var ev string
	if err := env.DB.QueryRow(`SELECT (details->>'chained_deleted')||'/'||(details->>'anchor_seq') FROM nexo_authz_events WHERE action = 'retention.purged' ORDER BY id DESC LIMIT 1`).Scan(&ev); err != nil || ev != "3/4" {
		t.Fatalf("evento: %q %v", ev, err)
	}
	// Repetir no borra nada más (la fila vieja del medio sigue).
	if n, err := evidence.Purge(ctx, env.DB, "cloud_auth_audit_log", 365); err != nil || n != 0 {
		t.Fatalf("segunda depuración: %d %v", n, err)
	}
	// nexo_authz_events (trigger de NX-05) y nexo_kb_events (NX-10) también.
	adminExec(t, env.Admin, `ALTER TABLE nexo_authz_events DISABLE TRIGGER nexo_authz_events_no_update_delete`)
	adminExec(t, env.Admin, `UPDATE nexo_authz_events SET occurred_at = now() - interval '500 days'`)
	adminExec(t, env.Admin, `ALTER TABLE nexo_authz_events ENABLE TRIGGER nexo_authz_events_no_update_delete`)
	rebuildChain(t, env.Admin, "nexo_authz_events")
	if n, err := evidence.Purge(ctx, env.DB, "nexo_authz_events", 365); err != nil || n == 0 {
		t.Fatalf("depuración de eventos de roles: %d %v", n, err)
	}
	if r := verify(t, env.DB, "nexo_authz_events"); !r.OK {
		t.Fatalf("eventos de roles después de depurar: %+v", r)
	}
	env.Exec(`INSERT INTO nexo_kb_events (space_slug, action, actor_label) VALUES ('s', 'a', 'x'), ('s', 'b', 'x')`)
	adminExec(t, env.Admin, `ALTER TABLE nexo_kb_events DISABLE TRIGGER nexo_kb_events_no_update_delete`)
	adminExec(t, env.Admin, `UPDATE nexo_kb_events SET occurred_at = now() - interval '500 days'`)
	adminExec(t, env.Admin, `ALTER TABLE nexo_kb_events ENABLE TRIGGER nexo_kb_events_no_update_delete`)
	rebuildChain(t, env.Admin, "nexo_kb_events")
	if n, err := evidence.Purge(ctx, env.DB, "nexo_kb_events", 365); err != nil || n != 2 {
		t.Fatalf("depuración de eventos de conocimiento: %d %v", n, err)
	}
	if r := verify(t, env.DB, "nexo_kb_events"); !r.OK || r.Checked != 0 {
		t.Fatalf("conocimiento después de depurar todo: %+v", r)
	}
	// Después de depurar todo, la cadena sigue desde el ancla.
	env.Exec(`INSERT INTO nexo_kb_events (space_slug, action, actor_label) VALUES ('s', 'c', 'x')`)
	if r := verify(t, env.DB, "nexo_kb_events"); !r.OK || r.Checked != 1 || r.FirstSeq != 3 {
		t.Fatalf("conocimiento: nueva fila tras depurar todo: %+v", r)
	}
	// Alertas (con reconocimientos) tampoco se borran antes de 365 días.
	if _, err := evidence.Purge(ctx, env.DB, "nexo_alerts", 365); err != nil {
		t.Fatalf("depuración de alertas: %v", err)
	}
}

// deleteWithPurgeFlag intenta un DELETE fuera de la función de depuración
// con la marca de sesión puesta (como haría un atacante con la cuenta).
func deleteWithPurgeFlag(db *sql.DB, table string, id int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`SELECT set_config('nexo.purge_in_progress', $1, true)`, table); err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM `+table+` WHERE id = $1`, id)
	return err
}

func joinIDs(ids []int64) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(s, ",")
}

// rebuildChain recalcula la cadena (solo pruebas: simula filas que nacieron
// con esa hora).
func rebuildChain(t testing.TB, admin *sql.DB, table string) {
	t.Helper()
	adminExec(t, admin, fmt.Sprintf(`DO $$
	DECLARE r record; prev bytea; h bytea;
	BEGIN
		ALTER TABLE %[1]s DISABLE TRIGGER USER;
		SELECT prev_hash INTO prev FROM nexo_audit_chain_anchors WHERE table_name = '%[1]s' AND kind = 'genesis';
		FOR r IN SELECT x.id, x.nexo_chain_seq FROM %[1]s x WHERE x.nexo_chain_seq IS NOT NULL ORDER BY x.nexo_chain_seq LOOP
			SELECT sha256(prev || sha256(convert_to(nexo_chain_canon_%[1]s(x), 'UTF8'))) INTO h FROM %[1]s x WHERE x.id = r.id;
			UPDATE %[1]s SET nexo_prev_hash = prev, nexo_hash = h WHERE id = r.id;
			prev := h;
		END LOOP;
		ALTER TABLE %[1]s ENABLE TRIGGER USER;
	END $$`, table))
}

// Con dos cuentas la aplicación inserta en las tablas encadenadas y registra
// puntos de control, no puede tocar las anclas ni alterar la evidencia, y
// verifica con sus propios privilegios.
func TestChainTwoAccounts(t *testing.T) {
	env, _ := kbtest.TwoAccounts(t)
	insertAudit(t, env.CS, 3, "app.event")
	env.Exec(`INSERT INTO nexo_alerts (rule, severity, layer, subject, dedup_key, summary) VALUES ('x', 'info', 'plataforma', 's', 'k1', 'resumen')`)
	if n, err := evidence.Checkpoint(context.Background(), env.DB, "test"); err != nil || n == 0 {
		t.Fatalf("punto de control con la cuenta de aplicación: %d %v", n, err)
	}
	for _, q := range []string{
		`UPDATE nexo_audit_chain_checkpoints SET seq = 0`,
		`DELETE FROM nexo_audit_chain_checkpoints`,
		`INSERT INTO nexo_audit_chain_anchors (table_name, kind, seq, prev_hash) VALUES ('cloud_auth_audit_log', 'purge', 99, '\x00')`,
		`DELETE FROM nexo_audit_chain_anchors`,
		`UPDATE cloud_auth_audit_log SET action = 'x'`,
		`DELETE FROM cloud_auth_audit_log`,
		`UPDATE nexo_alerts SET summary = 'x'`,
		`DELETE FROM nexo_alerts`,
		`INSERT INTO nexo_alert_acks (alert_id, acknowledged_by_label) SELECT id, 'a' FROM nexo_alerts`, // permitido
		`UPDATE nexo_alert_acks SET note = 'x'`,
		`DELETE FROM nexo_access_reviews`,
		`UPDATE nexo_audit_exports SET rows = 0`,
		`ALTER TABLE cloud_auth_audit_log DISABLE TRIGGER zz_nexo_chain_cloud_auth_audit_log`,
	} {
		_, err := env.DB.Exec(q)
		allowed := strings.HasPrefix(q, "INSERT INTO nexo_alert_acks")
		if allowed && err != nil {
			t.Fatalf("%s debe estar permitido: %v", q, err)
		}
		if !allowed && (err == nil || !(strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "must be owner"))) {
			t.Fatalf("%s debe negarse por privilegios: %v", q, err)
		}
	}
	if _, err := evidence.Purge(context.Background(), env.DB, "cloud_auth_audit_log", 30); err == nil || !strings.Contains(err.Error(), "nexo_retention_minimum") {
		t.Fatalf("la aplicación ejecuta la función, pero con el mínimo: %v", err)
	}
	rep, err := evidence.VerifyAll(context.Background(), env.DB, nil)
	if err != nil || !rep.OK {
		t.Fatalf("verificación con la cuenta de aplicación: %+v %v", rep, err)
	}
}

// RNF-S2: sobrecosto de la cadena al insertar < 1 ms por evento.
func TestChainInsertOverhead(t *testing.T) {
	env := kbtest.New(t)
	const n = 1500
	q := `INSERT INTO cloud_auth_audit_log (actor_source, action, outcome, metadata) VALUES ('perf', 'perf.event', 'success', $1::jsonb)`
	// Inserciones sueltas dentro de una transacción (sin el costo del commit
	// en disco, que es igual con o sin cadena y domina en autocommit).
	measure := func() time.Duration {
		tx, err := env.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		for i := 0; i < n; i++ {
			if _, err := tx.Exec(q, fmt.Sprintf(`{"i":%d,"client_ip":"10.0.0.1","user_agent":"engram/3.3.2"}`, i)); err != nil {
				t.Fatal(err)
			}
		}
		d := time.Since(start) / n
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return d
	}
	// Costo puro en el servidor: una sentencia con muchas filas.
	bulk := func() time.Duration {
		const rows = 20000
		start := time.Now()
		adminExec(t, env.DB, `INSERT INTO cloud_auth_audit_log (actor_source, action, outcome, metadata)
			SELECT 'perf', 'perf.bulk', 'success', jsonb_build_object('i', g, 'client_ip', '10.0.0.1') FROM generate_series(1, $1::int) g`, rows)
		return time.Since(start) / rows
	}
	measure() // calentamiento
	chained, chainedBulk := measure(), bulk()
	adminExec(t, env.Admin, `ALTER TABLE cloud_auth_audit_log DISABLE TRIGGER zz_nexo_chain_cloud_auth_audit_log`)
	plain, plainBulk := measure(), bulk()
	adminExec(t, env.Admin, `ALTER TABLE cloud_auth_audit_log ENABLE TRIGGER zz_nexo_chain_cloud_auth_audit_log`)
	overhead, overheadBulk := chained-plain, chainedBulk-plainBulk
	t.Logf("RNF-S2: inserción suelta con cadena %s, sin cadena %s, sobrecosto %s por evento; en lote %s frente a %s, sobrecosto %s por fila",
		chained, plain, overhead, chainedBulk, plainBulk, overheadBulk)
	if overhead > time.Millisecond || overheadBulk > time.Millisecond {
		t.Fatalf("sobrecosto de la cadena > 1 ms por evento (RNF-S2): %s / %s", overhead, overheadBulk)
	}
	// Las filas sin cadena que dejó la medición se excluyen de la verificación
	// de esta prueba: la tabla queda con filas "sin cadena" a propósito.
}

// RNF-S3: verificar 1 millón de eventos en menos de 60 s. Se activa con
// NEXO_CHAIN_PERF_EVENTS=<n> (por ejemplo 1000000); sin la variable, se salta.
func TestChainVerifyPerf(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("NEXO_CHAIN_PERF_EVENTS"))
	if n <= 0 {
		t.Skip("NEXO_CHAIN_PERF_EVENTS no definido (prueba de rendimiento RNF-S3)")
	}
	env := kbtest.New(t)
	start := time.Now()
	for done := 0; done < n; done += 100000 {
		batch := min(100000, n-done)
		adminExec(t, env.DB, `INSERT INTO cloud_auth_audit_log (actor_source, action, outcome, reason_code, metadata)
			SELECT 'request', 'sync.auth', 'denied', 'unknown_token',
				jsonb_build_object('source', 'request', 'client_ip', '10.0.' || (g % 250) || '.' || (g % 200), 'user_agent', 'engram/3.3.2', 'credential_fp', md5(g::text))
			FROM generate_series($1::int, $2::int) g`, done+1, done+batch)
	}
	t.Logf("carga de %d eventos encadenados: %s (%.3f ms por evento)", n, time.Since(start).Round(time.Millisecond), float64(time.Since(start).Milliseconds())/float64(n))
	adminExec(t, env.DB, `ANALYZE cloud_auth_audit_log`)
	vstart := time.Now()
	r := verify(t, env.DB, "cloud_auth_audit_log")
	elapsed := time.Since(vstart)
	t.Logf("RNF-S3: verificación de %d eventos en %s (ok=%v)", r.Checked, elapsed.Round(time.Millisecond), r.OK)
	if !r.OK || r.Checked != int64(n) {
		t.Fatalf("verificación: %+v", r)
	}
	if elapsed > 60*time.Second*time.Duration(max(1, n/1000000)) {
		t.Fatalf("verificación de %d eventos en %s (RNF-S3: 1 millón en < 60 s)", n, elapsed)
	}
}
