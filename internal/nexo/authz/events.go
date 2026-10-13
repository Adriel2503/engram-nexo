package authz

import "time"

// Acciones de nexo_authz_events (design §2.3).
const (
	ActionBindingGranted = "binding.granted"
	ActionBindingChanged = "binding.changed"
	ActionBindingRevoked = "binding.revoked"
	ActionScopeCreated   = "scope.created"
	ActionScopeUpdated   = "scope.updated"
	ActionScopeArchived  = "scope.archived"
	ActionScopeRestored  = "scope.restored"
	ActionScopeRecovered = "scope.recovered"
	ActionDenied         = "authz.denied"
	// NX-07: depuración de nexo_activity_log por la función segura
	// nexo_purge_activity (internal/nexo/security).
	ActionRetentionPurged = "retention.purged"
	// NX-07 S4-04: cuenta deshabilitada por inactividad
	// (NEXO_DISABLE_INACTIVE_DAYS, internal/nexo/security/evidence).
	ActionPrincipalDisabledInactive = "principal.disabled_inactive"
)

// EventActions lista las acciones válidas (filtro de la API).
var EventActions = []string{
	ActionBindingGranted, ActionBindingChanged, ActionBindingRevoked,
	ActionScopeCreated, ActionScopeUpdated, ActionScopeArchived, ActionScopeRestored, ActionScopeRecovered,
	ActionDenied, ActionRetentionPurged, ActionPrincipalDisabledInactive,
	// SG-10: cuentas de servicio (serviceaccounts.go).
	ActionServiceAccountCreated, ActionServiceAccountUpdated, ActionServiceAccountDisabled, ActionServiceAccountEnabled,
	ActionServiceAccountTokenIssued, ActionServiceAccountTokenRotated, ActionServiceAccountTokenRevoked,
}

// SystemActorLabel es la etiqueta del actor en eventos que genera el propio
// servidor (arranque del superadmin).
const SystemActorLabel = "sistema"

// Event es un evento a registrar. El actor sale de la identidad (token), con
// copia de nombre y email al momento (patrón NX-01).
type Event struct {
	Action            string
	Scope             Scope
	TargetPrincipalID string
	RoleBefore        Role
	RoleAfter         Role
	Actor             Identity // vacío = sistema
	Details           map[string]any
}

// EventRecord es un evento leído.
type EventRecord struct {
	ID                string         `json:"id"`
	OccurredAt        time.Time      `json:"occurred_at"`
	Action            string         `json:"action"`
	ScopeType         string         `json:"scope_type"`
	ScopeKey          string         `json:"scope_key"`
	TargetPrincipalID string         `json:"target_principal_id,omitempty"`
	TargetUsername    string         `json:"target_username,omitempty"`
	RoleBefore        string         `json:"role_before,omitempty"`
	RoleAfter         string         `json:"role_after,omitempty"`
	ActorPrincipalID  string         `json:"actor_principal_id,omitempty"`
	ActorTokenID      string         `json:"actor_token_id,omitempty"`
	Actor             string         `json:"actor"`
	ActorEmail        string         `json:"actor_email,omitempty"`
	ActorVerified     bool           `json:"actor_verified"`
	ClientIP          string         `json:"client_ip,omitempty"`
	UserAgent         string         `json:"user_agent,omitempty"`
	ClientClaim       string         `json:"client_claim,omitempty"`
	Details           map[string]any `json:"details,omitempty"`
}

// EventQuery filtra la lista de eventos. Paginación por llave (id
// descendente), como NX-03.
type EventQuery struct {
	ScopeType string
	ScopeKey  string
	Action    string // exacta o prefijo con "*" (por ejemplo "binding.*")
	Principal string // id de actor u objetivo
	From      time.Time
	To        time.Time // exclusivo
	Cursor    string
	Limit     int
}

// Límites de paginación (iguales a NX-03).
const (
	DefaultLimit = 100
	MaxLimit     = 200
)

// NormalizeLimit aplica el valor por defecto y el máximo.
func NormalizeLimit(n int) int {
	if n <= 0 {
		return DefaultLimit
	}
	if n > MaxLimit {
		return MaxLimit
	}
	return n
}
