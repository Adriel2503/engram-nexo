package cloudserver

// Nexo IA · NX-07 fases S2 y S3 — lógica de los enganches // NX-07 del
// servidor (docs/nexo/seguridad/design.md §3, §4 y §8).
//
//   - S2-01 límite de peticiones: por IP en el middleware de Nexo (después de
//     resolver la IP real de NX-04) y por token después de autenticar; 429 con
//     Retry-After y error_code rate_limited; un evento nexo.rate_limited por
//     minuto y clave en cloud_auth_audit_log.
//   - S2-02 bloqueo por fallos de autenticación (por IP, tokens distintos;
//     por persona, intentos) con evento auth.lockout; 429 genérico mientras
//     dura.
//   - S2-03 nadie se da a sí mismo un permiso de proyecto de Engram.
//   - S3-02 un token de alcance read solo lee en las rutas de Engram (negar
//     por defecto) y una sesión del panel abierta con él no cambia nada.
//
// El estado (baldes y contadores) vive en memoria de la réplica (DS-1).

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/routes"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// Códigos de error y acciones de auditoría de NX-07.
const (
	nexoCodeRateLimited      = "rate_limited"
	nexoCodeAuthLocked       = "auth_locked"
	nexoCodeSelfGrant        = "self_grant_forbidden"
	nexoCodeTokenScopeRead   = "token_scope_read"
	nexoAuditSourceSecurity  = "nexo_security"
	nexoAuditActionRateLimit = "nexo.rate_limited"
	nexoAuditActionLockout   = "auth.lockout"
	nexoAuthAuditReasonExp   = "token_expired"
	// NX-07 (D-S3-9): acción del panel negada a una sesión con token read.
	nexoAuditActionDashboardScope = "dashboard.authorize"
)

// nexoState es el estado Nexo de un CloudServer (campo nexo, // NX-07).
type nexoState struct {
	once sync.Once
	sec  *nexoSecurity
	// Fase S4: monitor de evidencia (alertas, cadena, exportación,
	// inactivos, retención), nexo_evidence.go.
	monitorOnce sync.Once
	monitor     *nexoMonitor
	// Fase S5: registro de rutas Nexo con su capacidad (nexo_routes.go).
	routes routes.Registry
	// SG-07: pestaña Auditoría del menú, por sesión (nexo_audit_nav.go).
	auditNav nexoAuditNavCache
}

type nexoSecurity struct {
	limits  security.RateLimits
	limiter *security.Limiter
	lockout *security.Lockout
	rejects *security.RejectionLog
	dedup   *security.AuditDedup // A2: rechazos repetidos (S4)

	flushMu    sync.Mutex
	flushing   bool
	flushEvery time.Duration
}

// nexoSecurity devuelve el estado de seguridad (se crea una vez por servidor
// a partir del entorno y deja en el log la configuración efectiva).
func (s *CloudServer) nexoSecurity() *nexoSecurity {
	s.nexo.once.Do(func() { s.nexo.sec = newNexoSecurityFromEnv() })
	return s.nexo.sec
}

func newNexoSecurityFromEnv() *nexoSecurity {
	limits, err := security.ParseRateLimits(os.Getenv(security.RateLimitsEnv))
	if err != nil {
		log.Printf("[nexo] seguridad: %v; se usan los valores por defecto", err)
	}
	lock, err := security.ParseLockout(os.Getenv(security.AuthLockoutEnv))
	if err != nil {
		log.Printf("[nexo] seguridad: %v; se usan los valores por defecto", err)
	}
	log.Printf("[nexo] seguridad: limite de peticiones: %s", limits)
	log.Printf("[nexo] seguridad: bloqueo por fallos de autenticacion: %s", lock)
	if al := security.SuperadminAllowlist(); !al.Configured {
		log.Printf("[nexo] seguridad: %s vacio: platform.admins.manage y platform.settings se pueden usar desde cualquier red (RS-13)", security.SuperadminAllowedCIDRsEnv)
	} else {
		log.Printf("[nexo] seguridad: administracion de plataforma solo desde: %s", al)
	}
	log.Printf("[nexo] seguridad: tokens nuevos vencen a los %d dias (maximo %d, %s)", security.DefaultIssueDays(), security.TokenMaxDays(), security.TokenMaxDaysEnv)
	dedupWindow := security.AuditDedupWindow()
	if dedupWindow > 0 {
		log.Printf("[nexo] seguridad: rechazos repetidos (misma huella, IP y motivo) se resumen cada %s (%s)", dedupWindow, security.AuditDedupEnv)
	} else {
		log.Printf("[nexo] seguridad: %s=0: cada rechazo de autenticacion deja una fila de auditoria", security.AuditDedupEnv)
	}
	return &nexoSecurity{
		limits:     limits,
		limiter:    security.NewLimiter(nil),
		lockout:    security.NewLockout(lock, nil),
		rejects:    security.NewRejectionLog(nil),
		dedup:      security.NewAuditDedup(dedupWindow, nil),
		flushEvery: time.Minute,
	}
}

// nexoHandler arma la cadena de middlewares de Nexo: origen de la petición
// (NX-04) y, con la IP real ya resuelta, límite por IP y espacio para los
// datos del token (NX-07).
func (s *CloudServer) nexoHandler(next http.Handler) http.Handler {
	trusted := nexoParseTrustedProxies(os.Getenv(NexoTrustedProxiesEnv))
	return nexoClientContextMiddleware(trusted, s.nexoSecurityMiddleware(s.nexoAuditNavMiddleware(nexoPanelErrorText(next)))) // OP-06: errores del panel en español
}

func (s *CloudServer) nexoSecurityMiddleware(next http.Handler) http.Handler {
	sec := s.nexoSecurity()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sec.limits.Enabled {
			group := security.ClassifyPath(r.Method, r.URL.Path)
			if lim := sec.limits.Limit(group); lim.IP > 0 {
				if ip := nexoRequestIP(r); ip != "" {
					key := security.IPKey(ip)
					if ok, wait := sec.limiter.Allow(string(group)+"|ip|"+key, lim.IP); !ok {
						s.nexoRateLimited(w, group, "ip", key, "", wait)
						return
					}
				}
			}
		}
		// Datos del token resuelto, huella de la credencial rechazada y
		// agrupador de rechazos repetidos (A2) para la auditoría.
		r = r.WithContext(cloudstore.WithNexoAuditDedup(security.WithTokenFactsHolder(r.Context()), sec.dedup))
		if !s.nexoDashboardScopeGuard(w, r) { // D-S3-9: sesión del panel con token read
			return
		}
		next.ServeHTTP(w, r)
	})
}

func nexoRequestIP(r *http.Request) string {
	if cc, ok := NexoClientContextFrom(r.Context()); ok {
		return cc.IP
	}
	return ""
}

// ─── 429 y eventos agregados ─────────────────────────────────────────────────

func nexoWrite429(w http.ResponseWriter, code, message string, wait time.Duration) {
	secs := security.RetryAfterSeconds(wait)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeActionableError(w, http.StatusTooManyRequests, constants.UpgradeErrorClassPolicy, code, fmt.Sprintf(message, secs))
}

func (s *CloudServer) nexoRateLimited(w http.ResponseWriter, group security.RateGroup, keyType, key, principal string, wait time.Duration) {
	sec := s.nexoSecurity()
	sec.rejects.Add(group, keyType, key, principal)
	s.nexoEnsureRejectionFlusher()
	nexoWrite429(w, nexoCodeRateLimited, "too many requests: retry in %d s (Nexo rate limit)", wait)
}

// nexoEnsureRejectionFlusher arranca (si no corre) la tarea que cada minuto
// registra los rechazos del minuto anterior. La tarea termina sola tras dos
// minutos sin rechazos, así no queda una goroutine por servidor inactivo.
func (s *CloudServer) nexoEnsureRejectionFlusher() {
	sec := s.nexoSecurity()
	sec.flushMu.Lock()
	if sec.flushing {
		sec.flushMu.Unlock()
		return
	}
	sec.flushing = true
	every := sec.flushEvery
	sec.flushMu.Unlock()
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		idle := 0
		for range t.C {
			if s.nexoFlushRateLimitEvents(false) == 0 {
				idle++
			} else {
				idle = 0
			}
			if idle < 2 {
				continue
			}
			sec.flushMu.Lock()
			if sec.rejects.Pending() == 0 {
				sec.flushing = false
				sec.flushMu.Unlock()
				return
			}
			sec.flushMu.Unlock()
			idle = 0
		}
	}()
}

// nexoFlushRateLimitEvents registra los resúmenes pendientes (all = true
// incluye el minuto en curso) y devuelve cuántos escribió.
func (s *CloudServer) nexoFlushRateLimitEvents(all bool) int {
	items := s.nexoSecurity().rejects.Flush(all)
	if len(items) == 0 {
		return 0
	}
	total := 0
	for _, it := range items {
		total += it.Count
		ctx := context.Background()
		meta := map[string]any{
			"group":    string(it.Group),
			"key_type": it.KeyType,
			"rejected": it.Count,
			"minute":   it.Minute.UTC().Format(time.RFC3339),
		}
		ev := cloudstore.AuthAuditEvent{
			ActorSource: nexoAuditSourceSecurity,
			Action:      nexoAuditActionRateLimit,
			Outcome:     authAuditOutcomeDenied,
			ReasonCode:  nexoCodeRateLimited,
			Metadata:    meta,
		}
		switch it.KeyType {
		case "ip":
			ctx = cloudstore.WithNexoRequestOrigin(ctx, cloudstore.NexoRequestOrigin{ClientIP: it.Key})
		case "token":
			meta["credential_id"] = it.Key
			ev.ActorPrincipalID = it.Principal
		}
		s.recordAuthAuditBestEffort(ctx, ev, "rate limit")
	}
	log.Printf("[nexo] seguridad: %d peticiones rechazadas por limite (429) en %d claves; eventos %s registrados", total, len(items), nexoAuditActionRateLimit)
	return len(items)
}

// ─── Bloqueo por fallos de autenticación (S2-02) ─────────────────────────────

// nexoAuthPrecheck es el enganche al comienzo de authenticateRequest: una IP
// bloqueada recibe 429 sin que se resuelva el token.
func (s *CloudServer) nexoAuthPrecheck(w http.ResponseWriter, r *http.Request) bool {
	sec := s.nexoSecurity()
	if locked, wait := sec.lockout.Locked(security.LockoutIPKey(nexoRequestIP(r))); locked {
		nexoWrite429(w, nexoCodeAuthLocked, "too many failed authentication attempts: retry in %d s", wait)
		return false
	}
	return true
}

// nexoNoteAuthFailure es el enganche en recordRequestAuthDeniedAudit: suma el
// fallo al contador que corresponda según el motivo.
func (s *CloudServer) nexoNoteAuthFailure(r *http.Request, reason string) {
	presented := nexoPresentedCredential(r)
	// A2: la huella (nunca el token) acompaña al evento de rechazo que Engram
	// escribe a continuación; el agrupador la usa como clave.
	security.RecordCredentialFingerprint(r.Context(), security.CredentialFingerprint(presented))
	s.nexoCountAuthFailure(r.Context(), nexoRequestIP(r), reason, presented)
}

// nexoPresentedCredential es lo que la petición presentó como credencial: el
// token Bearer, o la cabecera Authorization completa si está mal formada.
// Solo se usa para calcular su huella (nunca se guarda ni se registra).
func nexoPresentedCredential(r *http.Request) string {
	if token, err := bearerTokenFromRequest(r); err == nil {
		return token
	}
	return strings.TrimSpace(r.Header.Get("Authorization"))
}

// nexoCountAuthFailure suma el rechazo al contador que corresponda. Por IP
// cuenta tokens distintos (huella de presented), no intentos: el autosync de
// una PC con un token inválido fijo suma una sola vez (D-S2-8).
func (s *CloudServer) nexoCountAuthFailure(ctx context.Context, ip, reason, presented string) {
	sec := s.nexoSecurity()
	var key, principal string
	kind := security.ClassifyAuthFailure(reason)
	switch kind {
	case security.FailureIP:
		key = security.LockoutIPKey(ip)
	case security.FailureUser:
		if f, ok := security.TokenFactsFrom(ctx); ok {
			principal = f.PrincipalID
			key = security.LockoutUserKey(principal)
		}
	}
	if key == "" {
		return
	}
	var (
		lockedNow bool
		failures  int
		until     time.Time
	)
	if kind == security.FailureIP {
		lockedNow, failures, until = sec.lockout.FailToken(key, sec.lockout.Fingerprint(presented))
	} else {
		lockedNow, failures, until = sec.lockout.Fail(key)
	}
	if !lockedNow {
		return
	}
	cfg := sec.lockout.Config()
	log.Printf("[nexo] seguridad: bloqueo por fallos de autenticacion (%s, %d fallos) hasta %s", kind, failures, until.UTC().Format(time.RFC3339))
	ev := cloudstore.AuthAuditEvent{
		ActorSource:       nexoAuditSourceSecurity,
		TargetPrincipalID: principal,
		Action:            nexoAuditActionLockout,
		Outcome:           authAuditOutcomeDenied,
		ReasonCode:        "too_many_failures",
		Metadata: map[string]any{
			"key_type":        kind,
			"last_reason":     reason,
			"failures":        failures,
			"counts":          nexoLockoutCounts(kind),
			"window_seconds":  int(cfg.Window.Seconds()),
			"lockout_seconds": int(cfg.Duration.Seconds()),
			"locked_until":    until.UTC().Format(time.RFC3339),
		},
	}
	// El evento es raro (uno por bloqueo) y se escribe fuera de la respuesta.
	evCtx := context.WithoutCancel(ctx)
	go s.recordAuthAuditBestEffort(evCtx, ev, "auth lockout")
}

// nexoLockoutCounts dice qué cuenta "failures" en el evento auth.lockout.
func nexoLockoutCounts(kind string) string {
	if kind == security.FailureIP {
		return "distinct_tokens"
	}
	return "attempts"
}

// nexoAfterAuth es el enganche al final de authenticateRequest (petición ya
// autenticada): bloqueo de la persona, límite por token y alcance read en las
// rutas de Engram que suben cambios.
func (s *CloudServer) nexoAfterAuth(w http.ResponseWriter, r *http.Request, principal cloudauth.Principal) (*http.Request, bool) {
	sec := s.nexoSecurity()
	managed := principal.Source == cloudauth.PrincipalSourceManagedToken
	if managed {
		if locked, wait := sec.lockout.Locked(security.LockoutUserKey(principal.ID)); locked {
			nexoWrite429(w, nexoCodeAuthLocked, "too many failed authentication attempts: retry in %d s", wait)
			return r, false
		}
	}
	if sec.limits.Enabled && managed && principal.TokenID != "" {
		group := security.ClassifyPath(r.Method, r.URL.Path)
		if lim := sec.limits.Limit(group); lim.Token > 0 {
			if ok, wait := sec.limiter.Allow(string(group)+"|token|"+principal.TokenID, lim.Token); !ok {
				s.nexoRateLimited(w, group, "token", principal.TokenID, principal.ID, wait)
				return r, false
			}
		}
	}
	if managed && nexoReadScopeDenies(r) && s.nexoEffectiveTokenScope(r.Context(), principal) == security.TokenScopeRead {
		actor := auditActorPrincipalIDRef(principal)
		s.recordAuthAuditBestEffort(r.Context(), cloudstore.AuthAuditEvent{
			ActorPrincipalID: actor,
			ActorSource:      auditActorSource(principal),
			Action:           authAuditActionProjectAuthorize,
			Outcome:          authAuditOutcomeDenied,
			ReasonCode:       nexoCodeTokenScopeRead,
			Metadata:         map[string]any{"source": auditActorSource(principal), "route": nexoRouteLabel(r), "credential_id": principal.TokenID},
		}, "token scope")
		writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden,
			"forbidden: this token is read-only (scope read) and cannot upload or change project data; ask an administrator for a write token")
		return r, false
	}
	return r, true
}

// nexoReadOnlyPostRoutes: lista blanca mínima de rutas de Engram que usan POST
// pero solo leen (D-S3-8). Verificado en el código:
// handleVerifyPromptSourceAttestation solo hace un SELECT
// (CloudStore.VerifyPromptSourceAttestation) y no registra autoridad, no
// reclama pares ni agrega filas.
var nexoReadOnlyPostRoutes = map[string]bool{
	"POST /sync/prompt-source-attestations/verify": true,
}

// nexoReadScopeDenies aplica "negar por defecto" al alcance read en las rutas
// de Engram (OWASP A01, D-S3-8): un token read solo usa GET y HEAD, más la
// lista blanca anterior. Toda ruta de escritura de Engram, incluidas las que
// se agreguen en el futuro, queda bloqueada sin tocar ninguna lista. /nexo/*
// y /admin/* no se tocan aquí: las gobierna authz.Can con el alcance del
// token (Identity.TokenScope).
func nexoReadScopeDenies(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return false
	}
	if nexoReadOnlyPostRoutes[r.Pattern] {
		return false
	}
	path := r.URL.Path
	if _, p, ok := strings.Cut(r.Pattern, " "); ok {
		path = p // patrón con el que el mux eligió la ruta
	}
	if strings.HasPrefix(path, "/nexo/") || strings.HasPrefix(path, "/admin/") {
		return false
	}
	return true
}

func nexoRouteLabel(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return r.Method + " " + r.URL.Path
}

// nexoTokenScope devuelve el alcance del token resuelto en la petición ("" si
// no se conoce: identidades no administradas, almacenes sin Nexo).
func nexoTokenScope(ctx context.Context, tokenID string) string {
	if f, ok := security.TokenFactsFrom(ctx); ok && tokenID != "" && f.TokenID == tokenID {
		return f.Scope
	}
	return ""
}

// nexoEffectiveTokenScope es nexoTokenScope sin fallar abierto: si el
// almacén es de Nexo y el contexto no trae los datos del token (petición que
// no pasó por el middleware), los lee de la base. Un error de lectura cuenta
// como read (fallar cerrado).
func (s *CloudServer) nexoEffectiveTokenScope(ctx context.Context, principal cloudauth.Principal) string {
	if scope := nexoTokenScope(ctx, principal.TokenID); scope != "" {
		return scope
	}
	db := s.nexoDB()
	if db == nil || principal.Source != cloudauth.PrincipalSourceManagedToken || strings.TrimSpace(principal.TokenID) == "" {
		return ""
	}
	f, found, err := security.LoadTokenFacts(ctx, db, principal.TokenID)
	if err != nil || !found {
		return security.TokenScopeRead
	}
	return f.Scope
}

// ─── Sesiones del panel con token read (D-S3-9) ──────────────────────────────

// Rutas del panel que no exigen una sesión con alcance write.
var nexoDashboardScopeExempt = map[string]bool{
	"/dashboard/activate":  true,
	"/dashboard/login":     true,
	"/dashboard/logout":    true,
	"/dashboard/bootstrap": true,
}

// nexoDashboardScopeGuard niega por defecto toda petición del panel que no
// sea GET o HEAD cuando la sesión se inició con un token de alcance read: la
// sesión hereda el alcance del token. Las acciones de administración ya pasan
// por authz.Can con ese alcance (requireManagedAdmin); este guardia cubre
// además las acciones del panel que Engram autoriza solo por rol (pausar o
// reanudar la sincronización de un proyecto) y las que se agreguen.
func (s *CloudServer) nexoDashboardScopeGuard(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || s.auth == nil {
		return true
	}
	path := r.URL.Path
	if (path != "/dashboard" && !strings.HasPrefix(path, "/dashboard/")) || nexoDashboardScopeExempt[path] {
		return true
	}
	cookie, err := r.Cookie(dashboardSessionCookieName)
	if err != nil || s.nexoDB() == nil {
		return true // sin sesión: el panel redirige al login
	}
	principal, ok := s.dashboardPrincipalFromCookie(r.Context(), cookie.Value)
	if !ok || principal.Source != cloudauth.PrincipalSourceManagedToken {
		return true // sesión inválida o de legado: decide Engram
	}
	if s.nexoEffectiveTokenScope(r.Context(), principal) != security.TokenScopeRead {
		return true
	}
	s.recordAuthAuditBestEffort(r.Context(), cloudstore.AuthAuditEvent{
		ActorPrincipalID: auditActorPrincipalIDRef(principal),
		ActorSource:      auditActorSource(principal),
		Action:           nexoAuditActionDashboardScope,
		Outcome:          authAuditOutcomeDenied,
		ReasonCode:       nexoCodeTokenScopeRead,
		Metadata:         map[string]any{"source": auditActorSource(principal), "route": r.Method + " " + path, "credential_id": principal.TokenID},
	}, "dashboard token scope")
	if isDashboardHTMXRequest(r) {
		w.Header().Set("HX-Reswap", "none")
	}
	http.Error(w, "Esta sesión entró con un token de solo lectura y no puede cambiar nada. Entra con un token de escritura.", http.StatusForbidden)
	return false
}

var errNexoAuthLocked = errors.New("too many failed authentication attempts; retry later")

// nexoValidateDashboardLogin envuelve la validación del login del panel
// (enganche NX-04/NX-07 de cloudserver.go): IP o persona bloqueadas no
// validan, y cada rechazo suma al contador.
func (s *CloudServer) nexoValidateDashboardLogin(r *http.Request, token string) error {
	ctx := security.WithTokenFactsHolder(context.WithoutCancel(r.Context()))
	ip := nexoRequestIP(r)
	sec := s.nexoSecurity()
	if locked, _ := sec.lockout.Locked(security.LockoutIPKey(ip)); locked {
		return errNexoAuthLocked
	}
	// A2: la validación audita el rechazo por dentro; la huella va antes.
	security.RecordCredentialFingerprint(ctx, security.CredentialFingerprint(token))
	if err := s.validateDashboardLoginTokenContext(ctx, token); err != nil {
		s.nexoCountAuthFailure(ctx, ip, requestAuthDenyReason(err), strings.TrimSpace(token)) // por IP: tokens distintos
		return err
	}
	if f, ok := security.TokenFactsFrom(ctx); ok && f.PrincipalID != "" {
		if locked, _ := sec.lockout.Locked(security.LockoutUserKey(f.PrincipalID)); locked {
			return errNexoAuthLocked
		}
	}
	return nil
}

// ─── Sin auto-asignación de permisos de proyecto (S2-03) ─────────────────────

// nexoAllowProjectGrant es el enganche de handleAdminCreateGrant y
// handleDashboardCreateManagedGrant: exige platform.users.manage y rechaza
// que la persona se dé acceso a sí misma (RS-12, R-11). Un permiso de Engram
// equivale a publicador por compatibilidad, por eso darse uno es una
// auto-promoción.
func (s *CloudServer) nexoAllowProjectGrant(w http.ResponseWriter, r *http.Request, targetPrincipalID, project string, respond nexoRespond) bool {
	svc, available := s.nexoAuthz()
	if !available {
		return true
	}
	id := nexoAuthzIdentity(r.Context())
	if !id.Managed() {
		return true // Engram ya exigió un admin administrado
	}
	details := map[string]any{"route": r.Pattern, "action": "grant.create", "project": project, "target_principal_id": targetPrincipalID}
	d, err := svc.Engine().Require(r.Context(), id, authz.PlatformScope(), authz.CapPlatformUsersManage, details)
	if err != nil {
		nexoRespondAuthzError(w, respond, err)
		return false
	}
	if !d.Allowed {
		nexoRespondDenied(w, respond, authz.CapPlatformUsersManage, d)
		return false
	}
	if !nexoSamePrincipal(targetPrincipalID, id.PrincipalID) {
		return true
	}
	details["rule"] = nexoCodeSelfGrant
	svc.Engine().RecordDenied(r.Context(), id, authz.PlatformScope(), authz.CapPlatformUsersManage,
		authz.Decision{Role: d.Role, Reason: nexoCodeSelfGrant}, details)
	respond(w, http.StatusForbidden, nexoCodeSelfGrant, "forbidden: nobody can grant themselves access to a project; ask another administrator (RS-12)")
	return false
}

func nexoSamePrincipal(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	na, errA := strconv.ParseInt(a, 10, 64)
	nb, errB := strconv.ParseInt(b, 10, 64)
	if errA == nil && errB == nil {
		return na == nb
	}
	return a == b
}

// ─── Restricción por red (S2-04) ─────────────────────────────────────────────

// nexoNetworkPolicy arma la política de authz desde
// NEXO_SUPERADMIN_ALLOWED_CIDRS.
func nexoNetworkPolicy() authz.NetworkPolicy {
	al := security.SuperadminAllowlist()
	return authz.NetworkPolicy{Restricted: al.Configured, Allowed: al.Prefixes}
}
