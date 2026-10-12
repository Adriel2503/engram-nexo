package security

// Fase S2-02 (RS-11, design §3.2): bloqueo temporal por fallos repetidos de
// autenticación, por IP y por usuario.
//
// Qué cuenta cada clave (ver design §8, D-S2-3):
//   - ip:<ip>    fallos no atribuibles a una persona: token desconocido o mal
//                formado (adivinar tokens) y login del panel con un token
//                desconocido.
//   - user:<id>  fallos atribuibles a una persona: token revocado o persona
//                deshabilitada. Un token vencido NO cuenta: es el ciclo de vida
//                normal (el autosync de una PC con el token vencido reintenta
//                cada ~30 s y bloquearía a toda una oficina detrás de un NAT);
//                queda auditado como token_expired.
// Mientras una clave está bloqueada, la petición se rechaza con 429 sin
// resolver el token.

import (
	"encoding/json"
	"fmt"
	"hash/maphash"
	"strings"
	"sync"
	"time"
)

// AuthLockoutEnv configura el bloqueo: JSON o "off".
const AuthLockoutEnv = "NEXO_AUTH_LOCKOUT"

// LockoutConfig son los umbrales del bloqueo.
type LockoutConfig struct {
	Enabled         bool
	MaxFailures     int           // fallos por IP dentro de Window que bloquean
	UserMaxFailures int           // fallos por usuario dentro de Window (0 = sin bloqueo por usuario)
	Window          time.Duration // ventana de conteo
	Duration        time.Duration // duración del bloqueo
}

// DefaultLockout: 20 fallos en 5 minutos bloquean 15 minutos (design §3.2).
func DefaultLockout() LockoutConfig {
	return LockoutConfig{Enabled: true, MaxFailures: 20, UserMaxFailures: 20, Window: 5 * time.Minute, Duration: 15 * time.Minute}
}

func (c LockoutConfig) String() string {
	if !c.Enabled {
		return "desactivado"
	}
	return fmt.Sprintf("ip %d y usuario %d fallos en %s bloquean %s", c.MaxFailures, c.UserMaxFailures, c.Window, c.Duration)
}

type lockoutJSON struct {
	MaxFailures     *int `json:"max_failures"`
	UserMaxFailures *int `json:"user_max_failures"`
	WindowSeconds   *int `json:"window_seconds"`
	LockoutSeconds  *int `json:"lockout_seconds"`
}

// ParseLockout interpreta NEXO_AUTH_LOCKOUT: "" (valores por defecto), "off"
// o JSON {"max_failures":20,"user_max_failures":20,"window_seconds":300,
// "lockout_seconds":900}. Ante un valor inválido devuelve los valores por
// defecto y el error.
func ParseLockout(raw string) (LockoutConfig, error) {
	cfg := DefaultLockout()
	raw = strings.TrimSpace(raw)
	switch strings.ToLower(raw) {
	case "":
		return cfg, nil
	case "off", "false", "0", "disabled":
		cfg.Enabled = false
		return cfg, nil
	}
	var in lockoutJSON
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return DefaultLockout(), fmt.Errorf("%s: JSON inválido: %w", AuthLockoutEnv, err)
	}
	if in.MaxFailures != nil {
		cfg.MaxFailures = *in.MaxFailures
	}
	if in.UserMaxFailures != nil {
		cfg.UserMaxFailures = *in.UserMaxFailures
	}
	if in.WindowSeconds != nil {
		cfg.Window = time.Duration(*in.WindowSeconds) * time.Second
	}
	if in.LockoutSeconds != nil {
		cfg.Duration = time.Duration(*in.LockoutSeconds) * time.Second
	}
	if cfg.MaxFailures < 1 || cfg.UserMaxFailures < 0 || cfg.Window < time.Second || cfg.Duration < time.Second {
		return DefaultLockout(), fmt.Errorf("%s: max_failures >= 1, user_max_failures >= 0, window_seconds y lockout_seconds >= 1", AuthLockoutEnv)
	}
	return cfg, nil
}

type failState struct {
	windowStart int64
	count       int
	lockedUntil int64
}

type lockoutShard struct {
	mu        sync.Mutex
	states    map[string]*failState
	lastSweep int64
}

// Lockout cuenta fallos por clave y bloquea al superar el umbral. Seguro
// para uso concurrente; las claves vencidas se borran solas.
type Lockout struct {
	cfg    LockoutConfig
	seed   maphash.Seed
	shards [limiterShards]lockoutShard
	now    func() time.Time
}

// NewLockout crea el contador. now = nil usa time.Now.
func NewLockout(cfg LockoutConfig, now func() time.Time) *Lockout {
	if now == nil {
		now = time.Now
	}
	l := &Lockout{cfg: cfg, seed: maphash.MakeSeed(), now: now}
	for i := range l.shards {
		l.shards[i].states = map[string]*failState{}
	}
	return l
}

// Config devuelve la configuración vigente.
func (l *Lockout) Config() LockoutConfig { return l.cfg }

func (l *Lockout) shard(key string) *lockoutShard {
	return &l.shards[maphash.String(l.seed, key)%limiterShards]
}

func (l *Lockout) threshold(key string) int {
	if strings.HasPrefix(key, "user:") {
		return l.cfg.UserMaxFailures
	}
	return l.cfg.MaxFailures
}

// Locked informa si la clave está bloqueada y por cuánto tiempo más.
func (l *Lockout) Locked(key string) (bool, time.Duration) {
	if l == nil || !l.cfg.Enabled || key == "" {
		return false, 0
	}
	now := l.now().UnixNano()
	sh := l.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	st, ok := sh.states[key]
	if !ok || st.lockedUntil <= now {
		return false, 0
	}
	return true, time.Duration(st.lockedUntil - now)
}

// Fail suma un fallo. Devuelve lockedNow = true solo cuando este fallo
// inicia un bloqueo (para registrar un único evento auth.lockout), con la
// cantidad de fallos que lo dispararon y su fin.
func (l *Lockout) Fail(key string) (lockedNow bool, failures int, until time.Time) {
	if l == nil || !l.cfg.Enabled || key == "" {
		return false, 0, time.Time{}
	}
	limit := l.threshold(key)
	if limit <= 0 {
		return false, 0, time.Time{}
	}
	now := l.now().UnixNano()
	window := int64(l.cfg.Window)
	sh := l.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if now-sh.lastSweep >= window {
		for k, st := range sh.states {
			if st.lockedUntil <= now && now-st.windowStart >= window {
				delete(sh.states, k)
			}
		}
		sh.lastSweep = now
	}
	st, ok := sh.states[key]
	if !ok {
		if len(sh.states) >= limiterMaxKeysPerShard {
			// Tope de memoria: se descartan claves sin bloqueo vigente.
			n := limiterMaxKeysPerShard/10 + 1
			for k, s := range sh.states {
				if n <= 0 {
					break
				}
				if s.lockedUntil <= now {
					delete(sh.states, k)
					n--
				}
			}
		}
		st = &failState{windowStart: now}
		sh.states[key] = st
	}
	if st.lockedUntil > now {
		return false, st.count, time.Unix(0, st.lockedUntil)
	}
	if now-st.windowStart >= window {
		st.windowStart, st.count = now, 0
	}
	st.count++
	if st.count < limit {
		return false, st.count, time.Time{}
	}
	failures = st.count
	st.lockedUntil = now + int64(l.cfg.Duration)
	st.windowStart, st.count = now, 0
	return true, failures, time.Unix(0, st.lockedUntil)
}

// Len devuelve la cantidad de claves vivas (pruebas).
func (l *Lockout) Len() int {
	n := 0
	for i := range l.shards {
		sh := &l.shards[i]
		sh.mu.Lock()
		n += len(sh.states)
		sh.mu.Unlock()
	}
	return n
}

// Claves del bloqueo.
func LockoutIPKey(ip string) string {
	if strings.TrimSpace(ip) == "" {
		return ""
	}
	return "ip:" + IPKey(ip)
}

func LockoutUserKey(principalID string) string {
	if strings.TrimSpace(principalID) == "" {
		return ""
	}
	return "user:" + strings.TrimSpace(principalID)
}

// Clasificación de los motivos de rechazo de autenticación (los reason_code
// de cloud_auth_audit_log de Engram más token_expired de NX-07).
const (
	FailureNone = "" // no cuenta (falta la cabecera, error del servidor, token vencido)
	FailureIP   = "ip"
	FailureUser = "user"
)

// ClassifyAuthFailure decide qué contador suma un rechazo.
func ClassifyAuthFailure(reason string) string {
	switch reason {
	case "unknown_token", "malformed_bearer", "invalid_token", "authorize_error":
		return FailureIP
	case "token_revoked", "principal_disabled":
		return FailureUser
	}
	return FailureNone
}
