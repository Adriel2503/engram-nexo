package dashboard

// Nexo IA · SG-07 — datos de las pantallas del auditor (nexo_audit.templ).
// Los arma cloudserver (nexo_audit_dashboard*.go) con los mismos servicios que
// la API JSON; aquí solo hay texto ya formateado y escapado por templ, sin
// tipos de Nexo. Diseño: docs/nexo/panel/design.md §9.

import (
	"context"
	"net/url"
)

type nexoAuditNavCtxKey struct{}

// WithNexoAuditNav marca la petición como de alguien con platform.audit.read:
// el menú muestra la pestaña "Auditoría" aunque la persona no sea admin de
// Engram (el auditor). Lo pone cloudserver después de comprobar el permiso.
func WithNexoAuditNav(ctx context.Context) context.Context {
	return context.WithValue(ctx, nexoAuditNavCtxKey{}, true)
}

func nexoAuditNavOn(ctx context.Context) bool {
	v, _ := ctx.Value(nexoAuditNavCtxKey{}).(bool)
	return v
}

// Rutas de las pantallas.
const (
	NexoAuditPathLog       = "/dashboard/admin/audit"
	NexoAuditPathActivity  = "/dashboard/admin/audit/activity"
	NexoAuditPathHistory   = "/dashboard/admin/audit/history"
	NexoAuditPathIntegrity = "/dashboard/admin/audit/integrity"
	NexoAuditPathAlerts    = "/dashboard/admin/audit/alerts"
	NexoAuditPathReview    = "/dashboard/admin/audit/access-review"
)

// Secciones del submenú.
const (
	NexoAuditSecLog       = "registro"
	NexoAuditSecActivity  = "actividad"
	NexoAuditSecHistory   = "historial"
	NexoAuditSecIntegrity = "integridad"
	NexoAuditSecAlerts    = "alertas"
	NexoAuditSecReview    = "revision"
)

type nexoAuditSection struct {
	Key, Label, Path string
}

var nexoAuditSections = []nexoAuditSection{
	{NexoAuditSecLog, "Registro", NexoAuditPathLog},
	{NexoAuditSecActivity, "Actividad", NexoAuditPathActivity},
	{NexoAuditSecHistory, "Historial", NexoAuditPathHistory},
	{NexoAuditSecIntegrity, "Integridad", NexoAuditPathIntegrity},
	{NexoAuditSecAlerts, "Alertas", NexoAuditPathAlerts},
	{NexoAuditSecReview, "Revisión de accesos", NexoAuditPathReview},
}

// NexoOption es una opción de un select.
type NexoOption struct {
	Value, Label string
	Selected     bool
}

// NexoKV es un par etiqueta y valor (detalle de un evento).
type NexoKV struct{ Key, Value string }

// NexoAuditForbidden es la pantalla de acceso denegado.
type NexoAuditForbidden struct {
	Message string
}

// ─── Registro ────────────────────────────────────────────────────────────────

// NexoAuditLogView es /dashboard/admin/audit.
type NexoAuditLogView struct {
	Notice, Error string
	// Source: "seguridad" (cloud_auth_audit_log) o "roles" (nexo_authz_events).
	Source      string
	ActorHint   string
	SourceLinks []NexoAuditSourceLink
	From, To    string
	Actor       string
	Action      string
	Outcomes    []NexoOption // solo "seguridad"
	Scope       string       // proyecto (seguridad)
	ScopeTypes  []NexoOption // solo "roles"
	ScopeKey    string       // solo "roles"
	KnownAction []string
	Rows        []NexoAuditRow
	NextURL     string // vacío = no hay más
	FirstURL    string // vacío = ya es la primera página
	Filtered    bool
	ClearURL    string
}

// NexoAuditSourceLink es una pestaña de origen.
type NexoAuditSourceLink struct {
	Label, URL string
	Current    bool
}

// NexoAuditRow es un evento.
type NexoAuditRow struct {
	When, WhenISO string
	Actor         string
	ActorSub      string
	Action        string
	Gloss         string
	Outcome       string
	OutcomeClass  string
	Scope         string
	Origin        string
	OriginSub     string
	Details       []NexoKV
	HistoryURL    string
}

// ─── Actividad y presencia ───────────────────────────────────────────────────

// NexoAuditActivityView es /dashboard/admin/audit/activity.
type NexoAuditActivityView struct {
	Notice, Error string
	From, To      string
	Actor         string
	Project       string
	Operations    []NexoOption
	Presence      []NexoPresenceRow
	PresenceNext  string
	Activity      []NexoActivityRow
	ActivityNext  string
	FirstURL      string
	Filtered      bool
}

// NexoPresenceRow es una persona con token conectada.
type NexoPresenceRow struct {
	Actor, ActorSub string
	Token           string
	LastSeen        string
	LastSeenISO     string
	LastSeenAge     string
	FirstSeen       string
	IP              string
	Client          string
	Project         string
	Pushes, Pulls   string
}

// NexoActivityRow es una operación de sincronización.
type NexoActivityRow struct {
	When, WhenISO string
	Actor         string
	ActorSub      string
	Token         string
	Operation     string
	Project       string
	Items         string
	IP            string
	Client        string
}

// ─── Historial de un elemento ────────────────────────────────────────────────

// NexoAuditHistoryView es /dashboard/admin/audit/history.
type NexoAuditHistoryView struct {
	Notice, Error string
	Key           string
	Project       string
	Entity        string
	Searched      bool
	Rows          []NexoHistoryRow
	NextURL       string
	FirstURL      string
}

// NexoHistoryRow es una versión de un elemento.
type NexoHistoryRow struct {
	Version       string
	When, WhenISO string
	Op            string
	OpClass       string
	Actor         string
	ActorSub      string
	Token         string
	Client        string
	Entity        string
	Project       string
	Title         string
}

// ─── Integridad ──────────────────────────────────────────────────────────────

// NexoAuditIntegrityView es /dashboard/admin/audit/integrity.
type NexoAuditIntegrityView struct {
	Notice, Error string
	Tables        []NexoOption
	Ran           bool
	OK            bool
	Verdict       string
	VerdictDetail string
	VerifiedAt    string
	VerifiedISO   string
	Results       []NexoIntegrityTable
}

// NexoIntegrityTable es el resultado de una tabla.
type NexoIntegrityTable struct {
	Name, Layer, What string
	OK                bool
	Checked           string
	Unchained         string
	Altered           string
	Missing           string
	Broken            *NexoIntegrityBroken
	Gaps              []NexoIntegrityGap
	GapsNote          string
	Problems          []string
}

// NexoIntegrityBroken es la primera fila alterada.
type NexoIntegrityBroken struct {
	ID, Seq, Detail string
}

// NexoIntegrityGap es un tramo sin filas.
type NexoIntegrityGap struct {
	From, To, Missing, Where string
}

// ─── Alertas ─────────────────────────────────────────────────────────────────

// NexoAuditAlertsView es /dashboard/admin/audit/alerts.
type NexoAuditAlertsView struct {
	Notice, Error string
	CSRF          string
	CanAck        bool
	StateLinks    []NexoAuditSourceLink
	Rules         []NexoOption
	Severities    []NexoOption
	Layers        []NexoOption
	State         string
	Alerts        []NexoAlertRow
	NextURL       string
	FirstURL      string
	Filtered      bool
	ClearURL      string
}

// NexoAlertRow es una alerta.
type NexoAlertRow struct {
	ID            string
	When, WhenISO string
	Severity      string
	SevClass      string
	Rule          string
	RuleLabel     string
	Layer         string
	Subject       string
	Summary       string
	Acked         bool
	AckBy         string
	AckWhen       string
	AckWhenISO    string
	AckNote       string
}

// ─── Revisión de accesos ─────────────────────────────────────────────────────

// NexoAuditReviewView es /dashboard/admin/audit/access-review.
type NexoAuditReviewView struct {
	Notice, Error string
	CSRF          string
	CanComplete   bool
	InactiveDays  string
	OnlyFlagged   bool
	AllURL        string
	FlaggedURL    string
	Generated     string
	GeneratedISO  string
	Last          *NexoReviewLast
	Recent        []NexoReviewLast
	Stats         []NexoKV
	Policy        []string
	People        []NexoReviewPerson
	Tokens        []NexoReviewToken
}

// NexoReviewLast es una revisión registrada.
type NexoReviewLast struct {
	When, WhenISO string
	Age           string
	By            string
	Notes         string
	Summary       string
}

// NexoReviewPerson es una persona o cuenta de servicio.
type NexoReviewPerson struct {
	Name, Sub    string
	Kind         string
	Enabled      bool
	Platform     string
	PlatformSub  string
	Scopes       []string
	Grants       []string
	TokenCount   string
	LastActivity string
	Flags        []NexoFlag
}

// NexoFlag es una marca con su explicación.
type NexoFlag struct {
	Label, Class string
}

// NexoReviewToken es un token sin datos secretos.
type NexoReviewToken struct {
	Owner, Name, Scope string
	Created, Expires   string
	LastUsed           string
	Flags              []NexoFlag
}

// nexoAuditAckAction es la ruta del formulario de reconocimiento.
func nexoAuditAckAction(id string) string {
	return NexoAuditPathAlerts + "/" + url.PathEscape(id) + "/ack"
}

func nexoAuditStatusChip(ok bool) string {
	if ok {
		return "nx-chip nx-chip-ok"
	}
	return "nx-chip nx-chip-err"
}

func nexoAuditEnabledChip(enabled bool) string {
	if enabled {
		return "nx-chip nx-chip-ok"
	}
	return "nx-chip nx-chip-ret"
}

func nexoAuditEnabledText(enabled bool) string {
	if enabled {
		return "Habilitada"
	}
	return "Deshabilitada"
}

func nexoAuditVerdictClass(ok bool) string {
	if ok {
		return "nxa-verdict nxa-verdict-ok"
	}
	return "nxa-verdict nxa-verdict-bad"
}

func nexoAuditVerdictRole(ok bool) string {
	if ok {
		return "status"
	}
	return "alert"
}
