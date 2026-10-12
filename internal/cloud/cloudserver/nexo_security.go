package cloudserver

// Nexo IA · NX-07 fases S2 y S3 — lógica de los enganches // NX-07 del
// servidor (docs/nexo/seguridad/design.md §3, §4 y §8).
//
//   - S2-01 límite de peticiones: por IP en el middleware de Nexo (después de
//     resolver la IP real de NX-04) y por token después de autenticar; 429 con
//     Retry-After y error_code rate_limited; un evento nexo.rate_limited por
//     minuto y clave en cloud_auth_audit_log.
//   - S2-02 bloqueo por fallos de autenticación (por IP y por persona) con
//     evento auth.lockout; 429 genérico mientras dura.
//   - S2-03 nadie se da a sí mismo un permiso de proyecto de Engram.
//   - S3-02 un token de alcance read no sube cambios de proyecto.
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
)

// nexoState es el estado Nexo de un CloudServer (campo nexo, // NX-07).
type nexoState struct {
	once sync.Once
	sec  *nexoSecurity
}

type nexoSecurity struct {
	limits  security.RateLimits
	limiter *security.Limiter
	lockout *security.Lockout
	rejects *security.RejectionLog

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
	return &nexoSecurity{
		limits:     limits,
		limiter:    security.NewLimiter(nil),
		lockout:    security.NewLockout(lock, nil),
		rejects:    security.NewRejectionLog(nil),
		flushEvery: time.Minute,
	}
}

// nexoHandler arma la cadena de middlewares de Nexo: origen de la petición
// (NX-04) y, con la IP real ya resuelta, límite por IP y espacio para los
// datos del token (NX-07).
func (s *CloudServer) nexoHandler(next http.Handler) http.Handler {
	trusted := nexoParseTrustedProxies(os.Getenv(NexoTrustedProxiesEnv))
	return nexoClientContextMiddleware(trusted, s.nexoSecurityMiddleware(next))
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
		next.ServeHTTP(w, r.WithContext(security.WithTokenFactsHolder(r.Context())))
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
	s.nexoCountAuthFailure(r.Context(), nexoRequestIP(r), reason)
}

func (s *CloudServer) nexoCountAuthFailure(ctx context.Context, ip, reason string) {
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
	lockedNow, failures, until := sec.lockout.Fail(key)
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
			"window_seconds":  int(cfg.Window.Seconds()),
			"lockout_seconds": int(cfg.Duration.Seconds()),
			"locked_until":    until.UTC().Format(time.RFC3339),
		},
	}
	// El evento es raro (uno por bloqueo) y se escribe fuera de la respuesta.
	evCtx := context.WithoutCancel(ctx)
	go s.recordAuthAuditBestEffort(evCtx, ev, "auth lockout")
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
	if managed && nexoEngramWriteRoutes[r.Pattern] && nexoTokenScope(r.Context(), principal.TokenID) == security.TokenScopeRead {
		actor := auditActorPrincipalIDRef(principal)
		s.recordAuthAuditBestEffort(r.Context(), cloudstore.AuthAuditEvent{
			ActorPrincipalID: actor,
			ActorSource:      auditActorSource(principal),
			Action:           authAuditActionProjectAuthorize,
			Outcome:          authAuditOutcomeDenied,
			ReasonCode:       nexoCodeTokenScopeRead,
			Metadata:         map[string]any{"source": auditActorSource(principal), "route": r.Pattern, "credential_id": principal.TokenID},
		}, "token scope")
		writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden,
			"forbidden: this token is read-only (scope read) and cannot upload project changes; ask an administrator for a write token")
		return r, false
	}
	return r, true
}

// nexoEngramWriteRoutes: rutas de Engram que suben cambios de proyecto
// (design §4.2). Un token read no las usa.
var nexoEngramWriteRoutes = map[string]bool{
	"POST /sync/push":           true,
	"POST /sync/mutations/push": true,
}

// nexoTokenScope devuelve el alcance del token resuelto en la petición ("" si
// no se conoce: identidades no administradas, almacenes sin Nexo).
func nexoTokenScope(ctx context.Context, tokenID string) string {
	if f, ok := security.TokenFactsFrom(ctx); ok && tokenID != "" && f.TokenID == tokenID {
		return f.Scope
	}
	return ""
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
	if err := s.validateDashboardLoginTokenContext(ctx, token); err != nil {
		s.nexoCountAuthFailure(ctx, ip, requestAuthDenyReason(err))
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
