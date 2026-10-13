package evidence

// S4-05 (RS-34, design §5.4 y §8.4): retención por tabla con funciones
// SECURITY DEFINER de la cuenta dueña, con mínimos que ninguna variable puede
// bajar, sin borrar nada antes del plazo y sin romper la cadena de hashes.
//
//   - nexo_purge_evidence(tabla, días): solo tablas de la lista; plazo menor
//     que el mínimo = error. En las tablas encadenadas borra solo un PREFIJO
//     de la cadena (las filas anteriores a la primera fila que todavía está
//     dentro del plazo) y guarda un ancla con la posición y el prev_hash de la
//     primera fila conservada: la verificación arranca desde ahí sin reportar
//     un hueco. Deja el evento retention.purged en nexo_authz_events.
//   - Guardas de inmutabilidad (triggers) en las tablas de evidencia: UPDATE y
//     TRUNCATE siempre se rechazan; DELETE solo dentro de la función (marca
//     nexo.purge_in_progress = tabla), por la dueña de la tabla y sobre filas
//     más antiguas que el mínimo de la tabla.
//   - nexo_activity_log mantiene su función de S1 (nexo_purge_activity).

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

// PurgeFunction es la función de depuración de evidencia (S4-05).
const PurgeFunction = "nexo_purge_evidence"

// RetentionRule es la retención de una tabla.
type RetentionRule struct {
	Table       string
	Env         string // variable que fija el plazo (vacía = no depurar)
	MinimumDays int
	Chained     bool
}

// RetentionRules: tablas que se pueden depurar y sus mínimos (RS-34).
// nexo_activity_log (mínimo 30) sigue en NEXO_ACTIVITY_RETENTION_DAYS con la
// función de S1.
var RetentionRules = []RetentionRule{
	{Table: "cloud_auth_audit_log", Env: "NEXO_RETENTION_AUDIT_DAYS", MinimumDays: 365, Chained: true},
	{Table: "nexo_authz_events", Env: "NEXO_RETENTION_AUTHZ_EVENTS_DAYS", MinimumDays: 365, Chained: true},
	{Table: "nexo_kb_events", Env: "NEXO_RETENTION_KB_EVENTS_DAYS", MinimumDays: 365, Chained: true},
	{Table: "nexo_alerts", Env: "NEXO_RETENTION_ALERTS_DAYS", MinimumDays: 365},
}

// guardedTables: tablas de evidencia de S4 (y cloud_auth_audit_log) con la
// guarda genérica. nexo_authz_events y nexo_kb_events conservan sus triggers
// de solo inserción (NX-05, NX-10), que llaman a la misma comprobación.
var guardedTables = []string{"cloud_auth_audit_log", "nexo_alerts", "nexo_alert_acks", "nexo_audit_exports", "nexo_access_reviews",
	CheckpointsTable, AnchorsTable}

// minimumDaysSQL es la tabla de mínimos que usa la base (función y guardas).
func minimumDaysSQL(column string) string {
	var b strings.Builder
	b.WriteString("CASE " + column)
	for _, r := range RetentionRules {
		fmt.Fprintf(&b, " WHEN %s THEN %d", quoteLiteral(r.Table), r.MinimumDays)
	}
	b.WriteString(" WHEN 'nexo_alert_acks' THEN 365 ELSE NULL END")
	return b.String()
}

func migrateRetention(ctx context.Context, tx *sql.Tx, schema string) error {
	s := quoteIdent(schema)
	stmts := []string{
		// ¿Se puede borrar esta fila? Solo dentro de la función de
		// depuración, como dueña de la tabla y si la fila supera el mínimo.
		`CREATE OR REPLACE FUNCTION nexo_evidence_purge_allowed(p_table text, p_relid oid, p_at timestamptz) RETURNS boolean
		LANGUAGE plpgsql STABLE AS $$
		DECLARE
			v_min integer := ` + minimumDaysSQL("p_table") + `;
		BEGIN
			RETURN v_min IS NOT NULL
				AND current_setting('nexo.purge_in_progress', true) = p_table
				AND (SELECT c.relowner FROM pg_class c WHERE c.oid = p_relid) = (SELECT r.oid FROM pg_roles r WHERE r.rolname = current_user)
				AND p_at < now() - make_interval(days => v_min);
		END
		$$`,
		`CREATE OR REPLACE FUNCTION nexo_evidence_guard() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			IF TG_OP = 'DELETE' AND current_setting('nexo.purge_in_progress', true) = TG_TABLE_NAME THEN
				IF nexo_evidence_purge_allowed(TG_TABLE_NAME, TG_RELID, OLD.occurred_at) THEN
					RETURN OLD;
				END IF;
			END IF;
			RAISE EXCEPTION '% es de solo inserción (% no permitido)', TG_TABLE_NAME, TG_OP
				USING ERRCODE = 'insufficient_privilege';
		END
		$$`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("evidence: retención: %w", err)
		}
	}
	for _, t := range guardedTables {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, t).Scan(&exists); err != nil {
			return fmt.Errorf("evidence: retención %s: %w", t, err)
		}
		if !exists {
			continue
		}
		q := `DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = ` + quoteLiteral("nexo_guard_"+t) + ` AND tgrelid = ` + quoteLiteral(t) + `::regclass) THEN
				CREATE TRIGGER ` + quoteIdent("nexo_guard_"+t) + ` BEFORE UPDATE OR DELETE ON ` + t + `
					FOR EACH ROW EXECUTE FUNCTION nexo_evidence_guard();
			END IF;
			IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = ` + quoteLiteral("nexo_guard_trunc_"+t) + ` AND tgrelid = ` + quoteLiteral(t) + `::regclass) THEN
				CREATE TRIGGER ` + quoteIdent("nexo_guard_trunc_"+t) + ` BEFORE TRUNCATE ON ` + t + `
					FOR EACH STATEMENT EXECUTE FUNCTION nexo_evidence_guard();
			END IF;
		END $$`
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("evidence: retención %s: %w", t, err)
		}
	}

	purge := `CREATE OR REPLACE FUNCTION ` + PurgeFunction + `(p_table text, retention_days integer) RETURNS bigint
	LANGUAGE plpgsql SECURITY DEFINER SET search_path = ` + s + `, pg_temp AS $$
	DECLARE
		v_min     integer := ` + minimumDaysSQL("p_table") + `;
		v_cut     timestamptz;
		v_first   bigint;
		v_prev    bytea;
		v_chained bigint := 0;
		v_legacy  bigint := 0;
		v_acks    bigint := 0;
		n         bigint := 0;
	BEGIN
		IF v_min IS NULL OR p_table = 'nexo_alert_acks' THEN
			RAISE EXCEPTION 'nexo_retention_table: % no admite depuración', p_table USING ERRCODE = 'invalid_parameter_value';
		END IF;
		IF retention_days IS NULL OR retention_days < v_min THEN
			RAISE EXCEPTION 'nexo_retention_minimum: la retención mínima de % es de % días (pedido: %)', p_table, v_min, retention_days
				USING ERRCODE = 'check_violation';
		END IF;
		v_cut := now() - make_interval(days => retention_days);
		PERFORM set_config('nexo.purge_in_progress', p_table, true);
		IF p_table IN ('cloud_auth_audit_log', 'nexo_authz_events', 'nexo_kb_events') THEN
			-- Mismo candado que las inserciones encadenadas de la tabla.
			PERFORM pg_advisory_xact_lock(hashtext('nexo_chain:' || p_table));
			EXECUTE format('SELECT min(nexo_chain_seq) FROM %I WHERE nexo_chain_seq IS NOT NULL AND occurred_at >= $1', p_table)
				INTO v_first USING v_cut;
			IF v_first IS NULL THEN
				-- Todas las filas encadenadas son viejas: el ancla queda justo
				-- después de la última (la próxima fila enlaza con ella).
				EXECUTE format('SELECT nexo_chain_seq + 1, nexo_hash FROM %I WHERE nexo_chain_seq IS NOT NULL ORDER BY nexo_chain_seq DESC LIMIT 1', p_table)
					INTO v_first, v_prev;
			ELSE
				EXECUTE format('SELECT nexo_prev_hash FROM %I WHERE nexo_chain_seq = $1', p_table) INTO v_prev USING v_first;
			END IF;
			IF v_first IS NOT NULL THEN
				-- Solo el prefijo de la cadena: todas esas filas son anteriores
				-- al plazo (v_first es la primera posición dentro del plazo).
				EXECUTE format('DELETE FROM %I WHERE nexo_chain_seq < $1', p_table) USING v_first;
				GET DIAGNOSTICS v_chained = ROW_COUNT;
			END IF;
			EXECUTE format('DELETE FROM %I WHERE nexo_chain_seq IS NULL AND occurred_at < $1', p_table) USING v_cut;
			GET DIAGNOSTICS v_legacy = ROW_COUNT;
			IF v_chained > 0 THEN
				INSERT INTO ` + AnchorsTable + ` (table_name, kind, seq, prev_hash, purged_rows, details)
				VALUES (p_table, 'purge', v_first, v_prev, v_chained,
					jsonb_build_object('retention_days', retention_days, 'cutoff', v_cut, 'legacy_rows', v_legacy));
			END IF;
			n := v_chained + v_legacy;
		ELSIF p_table = 'nexo_alerts' THEN
			-- Alertas viejas y sus reconocimientos viejos (un reconocimiento
			-- reciente conserva su alerta).
			PERFORM set_config('nexo.purge_in_progress', 'nexo_alert_acks', true);
			DELETE FROM nexo_alert_acks k USING nexo_alerts a
			WHERE k.alert_id = a.id AND a.occurred_at < v_cut AND k.occurred_at < v_cut;
			GET DIAGNOSTICS v_acks = ROW_COUNT;
			PERFORM set_config('nexo.purge_in_progress', 'nexo_alerts', true);
			DELETE FROM nexo_alerts a WHERE a.occurred_at < v_cut
				AND NOT EXISTS (SELECT 1 FROM nexo_alert_acks k WHERE k.alert_id = a.id);
			GET DIAGNOSTICS n = ROW_COUNT;
		END IF;
		PERFORM set_config('nexo.purge_in_progress', 'off', true);
		IF n > 0 OR v_acks > 0 THEN
			INSERT INTO nexo_authz_events (action, scope_type, scope_key, actor_label, details)
			VALUES ('retention.purged', 'platform', '*', 'sistema',
				jsonb_build_object('table', p_table, 'retention_days', retention_days, 'deleted', n,
					'chained_deleted', v_chained, 'legacy_deleted', v_legacy, 'acks_deleted', v_acks,
					'anchor_seq', CASE WHEN v_chained > 0 THEN v_first END, 'session_user', session_user));
		END IF;
		RETURN n;
	END
	$$`
	if _, err := tx.ExecContext(ctx, purge); err != nil {
		return fmt.Errorf("evidence: retención: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `REVOKE ALL ON FUNCTION `+PurgeFunction+`(text, integer) FROM PUBLIC`); err != nil {
		return fmt.Errorf("evidence: retención: %w", err)
	}
	return nil
}

// Purge llama a la función de depuración (con la cuenta de aplicación, que
// tiene EXECUTE). Devuelve las filas borradas.
func Purge(ctx context.Context, db *sql.DB, table string, days int) (int64, error) {
	var n int64
	if err := db.QueryRowContext(ctx, `SELECT `+PurgeFunction+`($1, $2)`, table, days).Scan(&n); err != nil {
		return 0, fmt.Errorf("evidence: depurar %s: %w", table, err)
	}
	return n, nil
}

// RetentionPlan es el plazo efectivo por tabla (0 = no depurar).
type RetentionPlan map[string]int

// RetentionFromEnv lee los plazos (vacío = no depurar: se conserva todo).
// Un plazo menor que el mínimo se eleva al mínimo con aviso (como
// NEXO_ACTIVITY_RETENTION_DAYS en S1); inválido = no depurar, con aviso.
func RetentionFromEnv() RetentionPlan {
	plan := RetentionPlan{}
	for _, r := range RetentionRules {
		raw := strings.TrimSpace(os.Getenv(r.Env))
		if raw == "" || raw == "0" {
			continue
		}
		days, err := strconv.Atoi(raw)
		if err != nil || days < 0 {
			log.Printf("[nexo] %s=%q inválido: %s no se depura", r.Env, raw, r.Table)
			continue
		}
		if days < r.MinimumDays {
			log.Printf("[nexo] %s=%d es menor que el mínimo; se usa %d", r.Env, days, r.MinimumDays)
			days = r.MinimumDays
		}
		plan[r.Table] = days
	}
	return plan
}

// String resume el plan para el log de arranque.
func (p RetentionPlan) String() string {
	parts := make([]string, 0, len(RetentionRules))
	for _, r := range RetentionRules {
		if d := p[r.Table]; d > 0 {
			parts = append(parts, fmt.Sprintf("%s=%dd", r.Table, d))
		} else {
			parts = append(parts, r.Table+"=sin depurar")
		}
	}
	return strings.Join(parts, " ")
}
