package evidence

// Nexo IA · cuentas de servicio sin uso (SG-10, docs/nexo/seguridad/cuentas-servicio.md).
//
// NEXO_DISABLE_INACTIVE_DAYS solo deshabilita personas (kind 'human'): una
// cuenta de servicio de CI puede publicar cada pocos meses y deshabilitarla
// en silencio rompería el pipeline. Las cuentas de servicio se protegen con
// la caducidad y la rotación de sus tokens y con un responsable obligatorio.
// Lo que sí se hace es avisar: con NEXO_SA_STALE_DAYS (por defecto 180, 0 =
// desactivado) una cuenta habilitada sin uso en ese plazo deja la alerta
// service_account_stale en nexo_alerts, con su nombre corto y su responsable.
//
// Último uso = el de la función nexo_principal_last_use (authz): último
// last_used_at de sus tokens o última acción registrada con su identidad; si
// nunca se usó, cuenta desde el alta. Es lo mismo que muestra el detalle de
// la cuenta en la API, la CLI y el panel.
//
// Una alerta por cuenta y último uso: la clave de deduplicación lleva la
// fecha del último uso (o del alta), así la pasada diaria no la repite. Si la
// cuenta se vuelve a usar y otra vez queda sin uso, avisa de nuevo.

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Variables y regla de las cuentas de servicio sin uso.
const (
	// SAStaleDaysEnv: días sin uso tras los cuales una cuenta de servicio
	// habilitada deja una alerta (0 = desactivado). Nunca la deshabilita.
	SAStaleDaysEnv = "NEXO_SA_STALE_DAYS"
	// DefaultSAStaleDays es el plazo por defecto (vacío).
	DefaultSAStaleDays = 180
	// maxSAStaleDays acota el valor (100 años).
	maxSAStaleDays = 36500
	// RuleSAStale es la regla de nexo_alerts.
	RuleSAStale = "service_account_stale"
	// principalLastUseSQL es la función de authz (serviceaccounts.go).
	principalLastUseSQL = "nexo_principal_last_use"
)

// ParseSAStaleDays interpreta NEXO_SA_STALE_DAYS: vacío = 180, 0 =
// desactivado, entero positivo = días. Ante un valor inválido devuelve 180 y
// el error (es un aviso: mejor avisar de más que dejar de avisar).
func ParseSAStaleDays(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultSAStaleDays, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > maxSAStaleDays {
		return DefaultSAStaleDays, fmt.Errorf("%s=%q inválido: debe ser un entero entre 0 y %d; se usa %d", SAStaleDaysEnv, raw, maxSAStaleDays, DefaultSAStaleDays)
	}
	return n, nil
}

var saStaleLogged sync.Map // valor inválido ya informado en el log

// SAStaleDays lee NEXO_SA_STALE_DAYS (ver ParseSAStaleDays). Un valor
// inválido se informa una sola vez en el log.
func SAStaleDays() int {
	raw := os.Getenv(SAStaleDaysEnv)
	n, err := ParseSAStaleDays(raw)
	if err != nil {
		if _, seen := saStaleLogged.LoadOrStore(raw, true); !seen {
			log.Printf("[nexo] %v", err)
		}
	}
	return n
}

// StaleServiceAccount es una cuenta de servicio habilitada sin uso.
type StaleServiceAccount struct {
	PrincipalID   string     `json:"principal_id"`
	Slug          string     `json:"slug"`
	OwnerID       string     `json:"owner_principal_id"`
	OwnerUsername string     `json:"owner_username,omitempty"`
	OwnerName     string     `json:"owner_display_name,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	LastUseAt     *time.Time `json:"last_activity_at"`
	IdleDays      int        `json:"idle_days"`
}

// since es la fecha desde la que cuenta el plazo: último uso o alta.
func (s StaleServiceAccount) since() time.Time {
	if s.LastUseAt != nil && s.LastUseAt.After(s.CreatedAt) {
		return *s.LastUseAt
	}
	return s.CreatedAt
}

// owner es el responsable para el texto de la alerta.
func (s StaleServiceAccount) owner() string {
	switch {
	case s.OwnerUsername != "":
		return s.OwnerUsername
	case s.OwnerName != "":
		return s.OwnerName
	}
	return "principal:" + s.OwnerID
}

// FindStaleServiceAccounts devuelve las cuentas de servicio habilitadas sin
// uso en days días (days <= 0: ninguna). Sin la tabla de cuentas de servicio
// devuelve nil.
func FindStaleServiceAccounts(ctx context.Context, db *sql.DB, days int, now time.Time) ([]StaleServiceAccount, error) {
	if days <= 0 {
		return nil, nil
	}
	var ok bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('nexo_service_accounts') IS NOT NULL AND to_regprocedure('`+principalLastUseSQL+`(bigint)') IS NOT NULL`).Scan(&ok); err != nil {
		return nil, fmt.Errorf("evidence: cuentas de servicio sin uso: %w", err)
	}
	if !ok {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT x.id, x.slug, x.owner_id, x.owner_username, x.owner_name, x.created_at, x.last_use
		FROM (
			SELECT sa.principal_id::text AS id, sa.slug, sa.owner_principal_id::text AS owner_id,
				COALESCE(ho.username, '') AS owner_username, COALESCE(po.display_name, '') AS owner_name,
				sa.created_at, `+principalLastUseSQL+`(p.id) AS last_use
			FROM nexo_service_accounts sa
			JOIN cloud_principals p ON p.id = sa.principal_id
			LEFT JOIN cloud_principals po ON po.id = sa.owner_principal_id
			LEFT JOIN cloud_human_users ho ON ho.principal_id = sa.owner_principal_id
			WHERE p.enabled AND p.kind = 'service_account'
		) x
		WHERE GREATEST(x.created_at, x.last_use) < $1
		ORDER BY x.slug`, now.Add(-time.Duration(days)*24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("evidence: cuentas de servicio sin uso: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []StaleServiceAccount
	for rows.Next() {
		var s StaleServiceAccount
		var last sql.NullTime
		if err := rows.Scan(&s.PrincipalID, &s.Slug, &s.OwnerID, &s.OwnerUsername, &s.OwnerName, &s.CreatedAt, &last); err != nil {
			return nil, fmt.Errorf("evidence: cuentas de servicio sin uso: %w", err)
		}
		if last.Valid {
			v := last.Time
			s.LastUseAt = &v
		}
		s.IdleDays = int(now.Sub(s.since()).Hours() / 24)
		out = append(out, s)
	}
	return out, rows.Err()
}

// ServiceAccountStaleAlerts inserta una alerta service_account_stale por
// cuenta de servicio habilitada sin uso en days días (days <= 0 o regla
// desactivada: nada). Nunca deshabilita la cuenta. Devuelve las alertas
// nuevas: la clave lleva la fecha del último uso, así una cuenta avisa una
// vez por período sin uso.
func ServiceAccountStaleAlerts(ctx context.Context, db *sql.DB, days int, now time.Time, rule RuleConfig) ([]Alert, error) {
	if days <= 0 || !rule.Enabled {
		return nil, nil
	}
	stale, err := FindStaleServiceAccounts(ctx, db, days, now)
	if err != nil {
		return nil, err
	}
	var out []Alert
	for _, s := range stale {
		since := s.since().UTC()
		what := fmt.Sprintf("no se usa desde hace %d días (último uso %s)", s.IdleDays, since.Format(time.DateOnly))
		if s.LastUseAt == nil {
			what = fmt.Sprintf("nunca se usó (creada hace %d días, el %s)", s.IdleDays, since.Format(time.DateOnly))
		}
		details := map[string]any{"principal_id": s.PrincipalID, "slug": s.Slug, "owner_principal_id": s.OwnerID,
			"owner": s.owner(), "idle_days": s.IdleDays, "threshold_days": days, "created_at": s.CreatedAt.UTC().Format(time.RFC3339),
			"last_activity_at": nil, "rule": SAStaleDaysEnv}
		if s.LastUseAt != nil {
			details["last_activity_at"] = s.LastUseAt.UTC().Format(time.RFC3339)
		}
		a, err := InsertAlert(ctx, db, NewAlert{
			Rule: RuleSAStale, Severity: rule.Severity, Layer: LayerPlatform, Subject: "principal:" + s.PrincipalID,
			DedupKey: fmt.Sprintf("%s|%s|%s", RuleSAStale, s.PrincipalID, since.Format(time.RFC3339Nano)),
			Summary: fmt.Sprintf("la cuenta de servicio %s %s (umbral %d días; responsable: %s). No se deshabilita: revise si sigue en uso, rote o revoque sus tokens o deshabilítela",
				s.Slug, what, days, s.owner()),
			Details: details,
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
