package cloudstore

// Nexo IA · SG-06 — acceso de emergencia ("romper el vidrio").
//
// `engram cloud bootstrap emergency-token --user <superadmin>` (cmd/engram/
// cloud_bootstrap_emergency.go) corre dentro del servidor y usa la conexión a
// la base de la configuración, no la red HTTP. Sirve para el caso en que
// NINGÚN superadmin habilitado tiene un token vigente con escritura (todos
// vencidos o revocados) y por la API ya nadie puede emitir uno.
//
// Reglas, todas dentro de una sola transacción con los mismos candados que la
// emisión normal de tokens y las reglas del último superadmin:
//
//   - el usuario existe, está habilitado y es superadmin; no se crea ninguna
//     persona ni se cambia ningún rol;
//   - se niega si algún superadmin habilitado tiene un token vigente (no
//     revocado, no vencido) con alcance de escritura: un token de solo lectura
//     no puede emitir ni revocar, así que no cuenta;
//   - el token vale 24 horas, con escritura, y se muestra una sola vez (quien
//     llama);
//   - la emisión queda en cloud_auth_audit_log (acción nexo.emergency_token,
//     cadena de hashes incluida) y crea la alerta alta emergency_access_used en
//     la misma transacción: no hay token sin rastro. Las negativas también
//     quedan en la auditoría (las escribe quien llama).
//
// Por qué un subcomando nuevo y no ampliar recover-token: recover-token
// repara un estado distinto y estrecho (el único administrador sin ningún
// token, o con un único token de arranque sin usar) y su elegibilidad no debe
// aflojarse; la emergencia tiene otra condición (varios superadmins con tokens
// vencidos o revocados), otro destino explícito, caducidad de 24 horas y su
// propia alerta.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

// EmergencyTokenTTL es la vida del token de emergencia.
const EmergencyTokenTTL = 24 * time.Hour

// Acción de auditoría del acceso de emergencia.
const NexoEmergencyAuditAction = "nexo.emergency_token"

// Códigos de motivo (reason_code) de la auditoría y de NexoEmergencyError.
const (
	EmergencyIssued           = "emergency_token_issued"
	EmergencyUserNotFound     = "user_not_found"
	EmergencyNotSuperadmin    = "not_superadmin"
	EmergencyUserDisabled     = "user_disabled"
	EmergencyHasValidToken    = "superadmin_has_valid_token"
	emergencyAdminGuardKey    = `SELECT pg_advisory_xact_lock(hashtext('engram_cloud_active_admin_guard'))`
	emergencySuperadminGuard  = `SELECT pg_advisory_xact_lock(hashtext('nexo_authz_superadmin_guard'))`
	emergencyUserMaxLen       = 200
	emergencyReasonMaxRunes   = 500
	emergencyAlertSummaryText = "acceso de emergencia: se emitió un token de %d horas a %s (superadmin) con `engram cloud bootstrap emergency-token`, dentro del servidor; revíselo en la auditoría, hágalo reemplazar por un token normal y revóquelo"
)

// NexoEmergencyError es una negativa de la emisión de emergencia.
type NexoEmergencyError struct {
	Code    string
	Message string
	// TargetPrincipalID se llena cuando se llegó a identificar al usuario.
	TargetPrincipalID string
}

func (e *NexoEmergencyError) Error() string { return e.Code + ": " + e.Message }

// NexoEmergencyTokenParams son los datos del token (ya generado y con hash).
type NexoEmergencyTokenParams struct {
	User        string // nombre de usuario (o id) del superadmin
	TokenPrefix string
	TokenHash   string
	Name        string
	Reason      string // motivo libre, opcional, para la auditoría
}

// NexoEmergencyResult es lo que devuelve una emisión.
type NexoEmergencyResult struct {
	Token     PrincipalToken
	Username  string
	ExpiresAt time.Time
	Alert     *evidence.Alert
}

// NexoEmergencyToken emite el token de emergencia si se cumplen las reglas.
// audit trae el origen (ActorSource); el resto lo fija esta función.
func (cs *CloudStore) NexoEmergencyToken(ctx context.Context, params NexoEmergencyTokenParams, audit AuthAuditEvent) (NexoEmergencyResult, error) {
	var res NexoEmergencyResult
	if cs == nil || cs.db == nil {
		return res, fmt.Errorf("cloudstore: not initialized")
	}
	user := strings.TrimSpace(params.User)
	if user == "" || len(user) > emergencyUserMaxLen {
		return res, &NexoEmergencyError{Code: EmergencyUserNotFound, Message: "--user es obligatorio (nombre de usuario del superadmin)"}
	}
	if strings.TrimSpace(params.TokenPrefix) == "" || strings.TrimSpace(params.TokenHash) == "" {
		return res, fmt.Errorf("cloudstore: token prefix and hash are required")
	}
	if _, err := security.NexoCheckTokenName(params.Name); err != nil { // NX-07: nombre sin credenciales
		return res, err
	}
	reason := security.NexoRedactExternal(params.Reason, emergencyReasonMaxRunes)

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("cloudstore: begin emergency token tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Identificar a la persona y bloquear su fila (mismo orden que la
	// emisión normal: fila primero, candados después).
	var principalID, username string
	err = tx.QueryRowContext(ctx, `
		SELECT p.id::text, h.username
		FROM cloud_human_users h JOIN cloud_principals p ON p.id = h.principal_id
		WHERE p.kind = 'human' AND (h.username = $1 OR p.id::text = $1)
		ORDER BY (h.username = $1) DESC LIMIT 1`, user).Scan(&principalID, &username)
	if errors.Is(err, sql.ErrNoRows) {
		return res, &NexoEmergencyError{Code: EmergencyUserNotFound, Message: fmt.Sprintf("no existe una persona con usuario %q", user)}
	}
	if err != nil {
		return res, fmt.Errorf("cloudstore: emergency token: buscar usuario: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM cloud_principals WHERE id = $1::bigint FOR UPDATE`, principalID); err != nil {
		return res, fmt.Errorf("cloudstore: emergency token: bloquear persona: %w", err)
	}
	if _, err := tx.ExecContext(ctx, emergencyAdminGuardKey); err != nil {
		return res, fmt.Errorf("cloudstore: emergency token: candado de administradores: %w", err)
	}
	if _, err := tx.ExecContext(ctx, emergencySuperadminGuard); err != nil {
		return res, fmt.Errorf("cloudstore: emergency token: candado de superadmins: %w", err)
	}

	// 2. Elegibilidad de la persona (con los candados puestos).
	var enabled, isSuper bool
	if err := tx.QueryRowContext(ctx, `
		SELECT p.enabled,
		       EXISTS (SELECT 1 FROM nexo_role_bindings b WHERE b.principal_id = p.id AND b.scope_type = 'platform' AND b.scope_key = '*' AND b.role = 'superadmin')
		FROM cloud_principals p WHERE p.id = $1::bigint`, principalID).Scan(&enabled, &isSuper); err != nil {
		return res, fmt.Errorf("cloudstore: emergency token: estado de la persona: %w", err)
	}
	switch {
	case !isSuper:
		return res, &NexoEmergencyError{Code: EmergencyNotSuperadmin, TargetPrincipalID: principalID,
			Message: fmt.Sprintf("%q no es superadmin: el acceso de emergencia solo se emite a un superadmin existente (no crea personas ni cambia roles)", username)}
	case !enabled:
		return res, &NexoEmergencyError{Code: EmergencyUserDisabled, TargetPrincipalID: principalID,
			Message: fmt.Sprintf("%q está deshabilitado: habilítelo antes (otro superadmin) o elija otro superadmin habilitado", username)}
	}

	// 3. Ningún superadmin habilitado puede tener un token vigente con escritura.
	var valid int
	var holders string
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*), COALESCE(string_agg(DISTINCT COALESCE(h.username, p.display_name), ', '), '')
		FROM cloud_principal_tokens t
		JOIN cloud_principals p ON p.id = t.principal_id AND p.enabled
		JOIN nexo_role_bindings b ON b.principal_id = p.id AND b.scope_type = 'platform' AND b.scope_key = '*' AND b.role = 'superadmin'
		LEFT JOIN cloud_human_users h ON h.principal_id = p.id
		WHERE t.revoked_at IS NULL AND t.nexo_scope = 'write' AND (t.nexo_expires_at IS NULL OR t.nexo_expires_at > NOW())`).Scan(&valid, &holders); err != nil {
		return res, fmt.Errorf("cloudstore: emergency token: tokens vigentes: %w", err)
	}
	if valid > 0 {
		return res, &NexoEmergencyError{Code: EmergencyHasValidToken, TargetPrincipalID: principalID,
			Message: fmt.Sprintf("hay %d token(s) vigente(s) con escritura de superadmin habilitado (%s): use esa cuenta para emitir tokens por la API; el acceso de emergencia es solo cuando todos están vencidos o revocados", valid, holders)}
	}

	// 4. El token: 24 horas, escritura, creado por la propia persona.
	expires := time.Now().UTC().Add(EmergencyTokenTTL)
	name := strings.TrimSpace(params.Name)
	if name == "" {
		name = "emergency-" + time.Now().UTC().Format("20060102-1504")
	}
	token, err := scanPrincipalToken(tx.QueryRowContext(ctx, `
		INSERT INTO cloud_principal_tokens (principal_id, token_prefix, token_hash, name, created_by_principal_id, nexo_expires_at, nexo_scope)
		VALUES ($1::bigint, $2, $3, $4, $1::bigint, $5, 'write')
		RETURNING id::text, principal_id::text, token_prefix, '' AS token_hash, name, COALESCE(created_by_principal_id::text, ''), created_at, last_used_at, revoked_at, COALESCE(revoked_by_principal_id::text, ''), COALESCE(revocation_reason, '')`,
		principalID, strings.TrimSpace(params.TokenPrefix), strings.TrimSpace(params.TokenHash), name, expires))
	if err != nil {
		return res, fmt.Errorf("cloudstore: emergency token: crear token: %w", err)
	}
	token.NexoExpiresAt, token.NexoScope = &expires, "write"

	// 5. Auditoría (cadena de hashes) y alerta, en la misma transacción.
	meta := map[string]any{
		"emergency": true, "username": username, "token_prefix": token.TokenPrefix, "credential_name": name,
		"expires_at": expires.Format(time.RFC3339), "ttl_hours": int(EmergencyTokenTTL / time.Hour), "scope": "write",
	}
	if reason != "" {
		meta["reason"] = reason
	}
	audit.ActorPrincipalID = ""
	audit.TargetPrincipalID = principalID
	audit.Project = ""
	audit.Action = NexoEmergencyAuditAction
	audit.Outcome = "success"
	audit.ReasonCode = EmergencyIssued
	audit.Metadata = meta
	if err := insertAuthAuditEvent(ctx, tx, audit); err != nil {
		return res, fmt.Errorf("%w: %w", ErrAuthAuditInsertFailed, err)
	}
	severity := evidence.SeverityHigh
	if rules, rerr := evidence.RulesFromEnv(); rerr == nil {
		if cfg, ok := rules[evidence.RuleEmergencyAccessUsed]; ok && cfg.Severity != "" {
			severity = cfg.Severity
		}
	}
	alertDetails := map[string]any{"principal_id": principalID, "username": username, "token_id": token.ID, "token_prefix": token.TokenPrefix,
		"expires_at": expires.Format(time.RFC3339), "ttl_hours": int(EmergencyTokenTTL / time.Hour)}
	if reason != "" {
		alertDetails["reason"] = reason
	}
	alert, err := evidence.InsertAlert(ctx, tx, evidence.NewAlert{
		Rule: evidence.RuleEmergencyAccessUsed, Severity: severity, Layer: evidence.LayerPlatform, Subject: "principal:" + principalID,
		DedupKey: fmt.Sprintf("%s|token:%s", evidence.RuleEmergencyAccessUsed, token.ID),
		Summary:  fmt.Sprintf(emergencyAlertSummaryText, int(EmergencyTokenTTL/time.Hour), username),
		Details:  alertDetails,
	})
	if err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("cloudstore: commit emergency token tx: %w", err)
	}
	res = NexoEmergencyResult{Token: token, Username: username, ExpiresAt: expires, Alert: alert}
	return res, nil
}
