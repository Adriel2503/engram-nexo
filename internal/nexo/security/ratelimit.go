package security

// Fase S2-01 (RS-10, design §3.1): límite de peticiones en memoria por IP y
// por token, con grupos de rutas.
//
// Algoritmo: token bucket por clave. La capacidad es el límite por minuto y
// se recarga a límite/60 por segundo, así un cliente puede gastar de golpe lo
// de un minuto y después sigue al ritmo sostenido. Una clave sin uso durante
// un minuto ya tiene el balde lleno: borrarla no cambia ninguna decisión, por
// eso la limpieza de claves inactivas es exacta y la memoria queda acotada
// por las claves activas del último minuto (más un tope duro por fragmento).
//
// El estado vive en la réplica (DS-1): con N réplicas el límite efectivo es N
// veces el configurado.

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/maphash"
	"math"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// RateLimitsEnv configura los límites: JSON por grupo o "off".
const RateLimitsEnv = "NEXO_RATE_LIMITS"

// RateGroup es un grupo de rutas con sus propios límites.
type RateGroup string

const (
	GroupSync  RateGroup = "sync"  // /sync/*
	GroupAdmin RateGroup = "admin" // /admin/*, /nexo/admin/*, /nexo/authz/*
	GroupLogin RateGroup = "login" // POST /dashboard/login y /dashboard/bootstrap
	GroupNexo  RateGroup = "nexo"  // el resto de /nexo/* (NX-10: /nexo/kb/*)
	GroupOther RateGroup = "other" // panel, estáticos, /health
)

// RateGroups lista los grupos en orden fijo (configuración y registros).
var RateGroups = []RateGroup{GroupSync, GroupAdmin, GroupLogin, GroupNexo, GroupOther}

// GroupLimit son los límites por minuto de un grupo. 0 = sin límite en esa
// dimensión.
type GroupLimit struct {
	IP    int `json:"ip"`
	Token int `json:"token"`
}

// RateLimits es la configuración completa.
type RateLimits struct {
	Enabled bool
	Groups  map[RateGroup]GroupLimit
}

// DefaultRateLimits son los valores de design §3.1.
func DefaultRateLimits() RateLimits {
	return RateLimits{Enabled: true, Groups: map[RateGroup]GroupLimit{
		GroupSync:  {IP: 600, Token: 600},
		GroupAdmin: {IP: 120, Token: 120},
		GroupLogin: {IP: 20},
		GroupNexo:  {IP: 300, Token: 300},
		GroupOther: {IP: 600},
	}}
}

// Limit devuelve los límites de un grupo (cero si está desactivado).
func (c RateLimits) Limit(g RateGroup) GroupLimit {
	if !c.Enabled {
		return GroupLimit{}
	}
	return c.Groups[g]
}

// String resume la configuración para el log de arranque.
func (c RateLimits) String() string {
	if !c.Enabled {
		return "desactivado"
	}
	parts := make([]string, 0, len(RateGroups))
	for _, g := range RateGroups {
		l := c.Groups[g]
		parts = append(parts, fmt.Sprintf("%s=%s/%s", g, perMinute(l.IP), perMinute(l.Token)))
	}
	return strings.Join(parts, " ") + " (ip/token por minuto)"
}

func perMinute(n int) string {
	if n <= 0 {
		return "-"
	}
	return fmt.Sprint(n)
}

// ParseRateLimits interpreta NEXO_RATE_LIMITS:
//
//	""     valores por defecto
//	"off"  sin límite de peticiones
//	JSON   {"sync":{"ip":600,"token":600},"admin":{"ip":240}} (los grupos y
//	       campos omitidos conservan el valor por defecto; 0 = sin límite)
//
// Ante un valor inválido devuelve los valores por defecto y el error (el
// llamador lo registra): un error de configuración nunca desactiva la
// protección.
func ParseRateLimits(raw string) (RateLimits, error) {
	cfg := DefaultRateLimits()
	raw = strings.TrimSpace(raw)
	switch strings.ToLower(raw) {
	case "":
		return cfg, nil
	case "off", "false", "0", "disabled":
		return RateLimits{Enabled: false, Groups: map[RateGroup]GroupLimit{}}, nil
	}
	var in map[string]map[string]*int
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return DefaultRateLimits(), fmt.Errorf("%s: JSON inválido: %w", RateLimitsEnv, err)
	}
	for name, fields := range in {
		g := RateGroup(strings.ToLower(strings.TrimSpace(name)))
		cur, ok := cfg.Groups[g]
		if !ok {
			return DefaultRateLimits(), fmt.Errorf("%s: grupo desconocido %q (sync, admin, login, nexo, other)", RateLimitsEnv, name)
		}
		for field, v := range fields {
			if v == nil || *v < 0 {
				return DefaultRateLimits(), fmt.Errorf("%s: %s.%s debe ser un entero >= 0", RateLimitsEnv, name, field)
			}
			switch strings.ToLower(field) {
			case "ip":
				cur.IP = *v
			case "token":
				cur.Token = *v
			default:
				return DefaultRateLimits(), fmt.Errorf("%s: campo desconocido %s.%s (ip, token)", RateLimitsEnv, name, field)
			}
		}
		cfg.Groups[g] = cur
	}
	return cfg, nil
}

// ClassifyPath asigna el grupo de una petición.
func ClassifyPath(method, path string) RateGroup {
	switch {
	case strings.HasPrefix(path, "/sync/"):
		return GroupSync
	case strings.HasPrefix(path, "/admin/"), path == "/admin",
		strings.HasPrefix(path, "/nexo/admin/"), strings.HasPrefix(path, "/nexo/authz/"):
		return GroupAdmin
	case method == "POST" && (path == "/dashboard/login" || path == "/dashboard/bootstrap"):
		return GroupLogin
	case strings.HasPrefix(path, "/nexo/"):
		return GroupNexo
	}
	return GroupOther
}

// IPKey normaliza una IP para usarla como clave: IPv4 tal cual, IPv6 por su
// prefijo /64 (una sola máquina suele tener todo un /64).
func IPKey(ip string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return strings.TrimSpace(ip)
	}
	addr = addr.Unmap()
	if addr.Is6() {
		if p, err := addr.Prefix(64); err == nil {
			return p.String()
		}
	}
	return addr.String()
}

// ─── Token bucket ────────────────────────────────────────────────────────────

const (
	limiterShards = 32
	// limiterIdle: una clave sin uso por este tiempo tiene el balde lleno y
	// se borra sin efecto en las decisiones.
	limiterIdle = time.Minute
	// limiterMaxKeysPerShard acota la memoria ante muchas IP distintas
	// (32 fragmentos x 32 768 claves, unas 100 B por clave: < 110 MB en el
	// peor caso). Superado el tope se descartan claves al azar: esas IP
	// vuelven a empezar con el balde lleno (falla abierta solo para ellas).
	limiterMaxKeysPerShard = 1 << 15
)

type bucket struct {
	tokens float64
	last   int64 // UnixNano de la última actualización
}

type limiterShard struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep int64
}

// Limiter es un conjunto de token buckets por clave, seguro para uso
// concurrente.
type Limiter struct {
	seed    maphash.Seed
	shards  [limiterShards]limiterShard
	now     func() time.Time
	evicted uint64 // claves descartadas por el tope (solo diagnóstico)
	maxKeys int
}

// NewLimiter crea un limitador. now = nil usa time.Now.
func NewLimiter(now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	l := &Limiter{seed: maphash.MakeSeed(), now: now, maxKeys: limiterMaxKeysPerShard}
	for i := range l.shards {
		l.shards[i].buckets = map[string]*bucket{}
	}
	return l
}

func (l *Limiter) shard(key string) *limiterShard {
	return &l.shards[maphash.String(l.seed, key)%limiterShards]
}

// Allow consume una unidad del balde de la clave con un límite por minuto.
// Si no queda, devuelve false y cuánto esperar hasta la siguiente unidad.
// perMinute <= 0 siempre permite.
func (l *Limiter) Allow(key string, perMinute int) (bool, time.Duration) {
	if perMinute <= 0 {
		return true, 0
	}
	now := l.now().UnixNano()
	capacity := float64(perMinute)
	rate := capacity / float64(time.Minute) // unidades por nanosegundo
	sh := l.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if now-sh.lastSweep >= int64(limiterIdle) {
		sh.sweep(now)
	}
	b, ok := sh.buckets[key]
	if !ok {
		if len(sh.buckets) >= l.maxKeys {
			sh.evict(l.maxKeys/10 + 1)
			l.evicted++
		}
		b = &bucket{tokens: capacity, last: now}
		sh.buckets[key] = b
	} else if elapsed := now - b.last; elapsed > 0 {
		b.tokens = math.Min(capacity, b.tokens+float64(elapsed)*rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration(math.Ceil((1 - b.tokens) / rate))
	return false, wait
}

// sweep borra las claves inactivas (balde lleno). Se llama con el candado.
func (sh *limiterShard) sweep(now int64) {
	for k, b := range sh.buckets {
		if now-b.last >= int64(limiterIdle) {
			delete(sh.buckets, k)
		}
	}
	sh.lastSweep = now
}

// evict descarta n claves cualesquiera (el orden de un map es aleatorio).
func (sh *limiterShard) evict(n int) {
	for k := range sh.buckets {
		if n <= 0 {
			return
		}
		delete(sh.buckets, k)
		n--
	}
}

// Len devuelve la cantidad de claves vivas (pruebas y diagnóstico).
func (l *Limiter) Len() int {
	n := 0
	for i := range l.shards {
		sh := &l.shards[i]
		sh.mu.Lock()
		n += len(sh.buckets)
		sh.mu.Unlock()
	}
	return n
}

// Sweep fuerza la limpieza de claves inactivas en todos los fragmentos.
func (l *Limiter) Sweep() {
	now := l.now().UnixNano()
	for i := range l.shards {
		sh := &l.shards[i]
		sh.mu.Lock()
		sh.sweep(now)
		sh.mu.Unlock()
	}
}

// RetryAfterSeconds redondea hacia arriba a segundos enteros (mínimo 1) para
// la cabecera Retry-After.
func RetryAfterSeconds(d time.Duration) int {
	s := int(math.Ceil(d.Seconds()))
	if s < 1 {
		return 1
	}
	return s
}

// ─── Rechazos agregados por minuto y clave ───────────────────────────────────

// Rejection es el resumen de los rechazos de una clave en un minuto.
type Rejection struct {
	Minute    time.Time // inicio del minuto (UTC)
	Group     RateGroup
	KeyType   string // ip | token | user
	Key       string // IP, id del token o id de la persona
	Principal string // persona dueña del token (si se conoce)
	Count     int
}

// rejectionMaxKeys acota las claves distintas por minuto; el resto se suma
// en una fila "otros".
const rejectionMaxKeys = 5000

// RejectionLog cuenta rechazos sin registrar cada 429 (design §3.1): se
// entrega un resumen por minuto y clave.
type RejectionLog struct {
	mu      sync.Mutex
	now     func() time.Time
	pending map[rejectionKey]*Rejection
}

type rejectionKey struct {
	minute  int64
	group   RateGroup
	keyType string
	key     string
}

// NewRejectionLog crea el contador. now = nil usa time.Now.
func NewRejectionLog(now func() time.Time) *RejectionLog {
	if now == nil {
		now = time.Now
	}
	return &RejectionLog{now: now, pending: map[rejectionKey]*Rejection{}}
}

// Add suma un rechazo.
func (r *RejectionLog) Add(group RateGroup, keyType, key, principal string) {
	minute := r.now().UTC().Truncate(time.Minute)
	k := rejectionKey{minute: minute.Unix(), group: group, keyType: keyType, key: key}
	r.mu.Lock()
	defer r.mu.Unlock()
	if rej, ok := r.pending[k]; ok {
		rej.Count++
		return
	}
	if len(r.pending) >= rejectionMaxKeys {
		k = rejectionKey{minute: minute.Unix(), group: group, keyType: keyType, key: "otros"}
		principal = ""
		if rej, ok := r.pending[k]; ok {
			rej.Count++
			return
		}
	}
	r.pending[k] = &Rejection{Minute: minute, Group: group, KeyType: keyType, Key: k.key, Principal: principal, Count: 1}
}

// Flush devuelve y quita los resúmenes de minutos ya cerrados (all = true
// incluye el minuto en curso). Orden estable por minuto, grupo y clave.
func (r *RejectionLog) Flush(all bool) []Rejection {
	current := r.now().UTC().Truncate(time.Minute).Unix()
	r.mu.Lock()
	out := make([]Rejection, 0, len(r.pending))
	for k, rej := range r.pending {
		if all || k.minute < current {
			out = append(out, *rej)
			delete(r.pending, k)
		}
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.Minute.Equal(b.Minute) {
			return a.Minute.Before(b.Minute)
		}
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.KeyType != b.KeyType {
			return a.KeyType < b.KeyType
		}
		return a.Key < b.Key
	})
	return out
}

// Pending informa si quedan resúmenes sin entregar.
func (r *RejectionLog) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}

// ErrRateLimited es el error de un rechazo por límite (registro y pruebas).
var ErrRateLimited = errors.New("rate_limited")
