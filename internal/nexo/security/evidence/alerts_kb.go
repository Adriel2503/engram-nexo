package evidence

// Nexo IA · NX-10 entrega 2b (KP-09, HU-39) — avisos de la capa
// conocimiento: documento por vencer y documento sin revisión. Los calcula el
// mismo evaluador de S4 (una réplica a la vez); no cambian el contenido. Una
// alerta por documento y estado (la clave incluye la vigencia o la versión),
// así un documento no avisa cada minuto.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Reglas de la capa conocimiento.
const (
	RuleKBDocExpiring = "kb_doc_expiring"   // valid_until dentro de la ventana (por defecto 30 días)
	RuleKBReviewDue   = "kb_doc_review_due" // sin versión nueva en settings.review_every_days del espacio
)

func kbTablesExist(ctx context.Context, tx *sql.Tx) (bool, error) {
	var ok bool
	err := tx.QueryRowContext(ctx, `SELECT to_regclass('nexo_kb_documents') IS NOT NULL AND to_regclass('nexo_kb_spaces') IS NOT NULL`).Scan(&ok)
	return ok, err
}

// runKBExpiring: documentos vigentes cuyo valid_until cae dentro de la
// ventana. La ventana del espacio (settings.expiry_warn_days) manda sobre la
// de la regla.
func runKBExpiring(ctx context.Context, tx *sql.Tx, cfg RuleConfig) ([]Alert, error) {
	window := cfg.Window
	if window <= 0 {
		window = 30 * 24 * time.Hour
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT sp.slug, d.doc_key, d.title, d.owner_label, d.valid_until, d.current_version
		FROM nexo_kb_documents d
		JOIN nexo_kb_spaces sp ON sp.id = d.space_id
		JOIN nexo_scopes sc ON sc.scope_type = 'space' AND sc.scope_key = sp.slug AND sc.status = 'active'
		WHERE d.status = 'active' AND d.valid_until IS NOT NULL AND d.valid_until > now()
		  AND d.valid_until <= now() + CASE WHEN (sp.settings->>'expiry_warn_days') ~ '^[0-9]{1,4}$'
			THEN make_interval(days => (sp.settings->>'expiry_warn_days')::int) ELSE make_interval(secs => $1::bigint) END
		ORDER BY d.valid_until LIMIT 500`, int64(window.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("evidence: regla %s: %w", RuleKBDocExpiring, err)
	}
	type row struct {
		space, key, title, owner string
		until                    time.Time
		version                  int
	}
	var items []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.space, &r.key, &r.title, &r.owner, &r.until, &r.version); err != nil {
			_ = rows.Close()
			return nil, err
		}
		items = append(items, r)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var out []Alert
	for _, r := range items {
		a, err := InsertAlert(ctx, tx, NewAlert{
			Rule: RuleKBDocExpiring, Severity: cfg.Severity, Layer: LayerKnowledge, Subject: "space:" + r.space + "/doc:" + r.key,
			DedupKey: fmt.Sprintf("%s|%s|%s|%s", RuleKBDocExpiring, r.space, r.key, r.until.UTC().Format(time.RFC3339)),
			Summary:  fmt.Sprintf("el documento %s del espacio %s vence el %s (responsable: %s)", r.key, r.space, r.until.UTC().Format("2006-01-02"), labelOr(r.owner)),
			Details: map[string]any{"space": r.space, "doc_key": r.key, "title": r.title, "owner": r.owner, "valid_until": r.until.UTC().Format(time.RFC3339),
				"version": r.version},
		})
		if err != nil {
			return out, err
		}
		if a != nil {
			out = append(out, *a)
		}
	}
	return out, nil
}

// runKBReviewDue: documentos vigentes sin versión nueva en el plazo de
// revisión del espacio (settings.review_every_days; sin él, no aplica).
func runKBReviewDue(ctx context.Context, tx *sql.Tx, cfg RuleConfig) ([]Alert, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT sp.slug, d.doc_key, d.title, d.owner_label, v.published_at, d.current_version, (sp.settings->>'review_every_days')::int
		FROM nexo_kb_documents d
		JOIN nexo_kb_spaces sp ON sp.id = d.space_id
		JOIN nexo_scopes sc ON sc.scope_type = 'space' AND sc.scope_key = sp.slug AND sc.status = 'active'
		JOIN nexo_kb_versions v ON v.document_id = d.id AND v.version = d.current_version
		WHERE d.status = 'active' AND (d.valid_until IS NULL OR d.valid_until > now())
		  AND (sp.settings->>'review_every_days') ~ '^[0-9]{1,4}$' AND (sp.settings->>'review_every_days')::int > 0
		  AND v.published_at < now() - make_interval(days => (sp.settings->>'review_every_days')::int)
		ORDER BY v.published_at LIMIT 500`)
	if err != nil {
		return nil, fmt.Errorf("evidence: regla %s: %w", RuleKBReviewDue, err)
	}
	type row struct {
		space, key, title, owner string
		published                time.Time
		version, days            int
	}
	var items []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.space, &r.key, &r.title, &r.owner, &r.published, &r.version, &r.days); err != nil {
			_ = rows.Close()
			return nil, err
		}
		items = append(items, r)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var out []Alert
	for _, r := range items {
		a, err := InsertAlert(ctx, tx, NewAlert{
			Rule: RuleKBReviewDue, Severity: cfg.Severity, Layer: LayerKnowledge, Subject: "space:" + r.space + "/doc:" + r.key,
			DedupKey: fmt.Sprintf("%s|%s|%s|v%d", RuleKBReviewDue, r.space, r.key, r.version),
			Summary: fmt.Sprintf("el documento %s del espacio %s no se revisa desde el %s (plazo %d días; responsable: %s)", r.key, r.space,
				r.published.UTC().Format("2006-01-02"), r.days, labelOr(r.owner)),
			Details: map[string]any{"space": r.space, "doc_key": r.key, "title": r.title, "owner": r.owner, "version": r.version,
				"published_at": r.published.UTC().Format(time.RFC3339), "review_every_days": r.days},
		})
		if err != nil {
			return out, err
		}
		if a != nil {
			out = append(out, *a)
		}
	}
	return out, nil
}

// runKBRules corre las reglas de la capa conocimiento si sus tablas existen.
func runKBRules(ctx context.Context, tx *sql.Tx, rules RuleSet) ([]Alert, error) {
	exp, rev := rules[RuleKBDocExpiring], rules[RuleKBReviewDue]
	if !exp.Enabled && !rev.Enabled {
		return nil, nil
	}
	ok, err := kbTablesExist(ctx, tx)
	if err != nil || !ok {
		return nil, err
	}
	var out []Alert
	if exp.Enabled {
		got, err := runKBExpiring(ctx, tx, exp)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	if rev.Enabled {
		got, err := runKBReviewDue(ctx, tx, rev)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}
