package security

// Arreglo A2 (revisión del 2026-10-12, design §8.4): volumen de auditoría por
// rechazos repetidos.
//
// Un token mal pegado en una PC con autosync se rechaza cada ~30 s y dejaba
// una fila por rechazo en cloud_auth_audit_log (~2 880 por día). El punto
// común de auditoría (insertAuthAuditEvent, enganche NX-04) consulta este
// agrupador: el primer rechazo de cada clave (huella del token, IP, motivo)
// se escribe completo y, durante la ventana siguiente (por defecto 10
// minutos), los repetidos solo se cuentan; al cerrar la ventana se escribe un
// evento resumen con el conteo, la primera y la última vez. No se pierde
// evidencia: el conteo queda en la base. La huella es un HMAC con una clave
// aleatoria del proceso (CredentialFingerprint): nunca el token en claro.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AuditDedupEnv fija la ventana de agrupación en segundos (0 = sin agrupar).
const AuditDedupEnv = "NEXO_AUDIT_DEDUP_SECONDS"

// DefaultAuditDedupWindow es la ventana por defecto (A2).
const DefaultAuditDedupWindow = 10 * time.Minute

// maxDedupKeys acota la memoria del agrupador: con el tope alcanzado, una
// clave nueva se escribe completa sin agrupar (nunca se pierde un evento).
const maxDedupKeys = 50000

// maxDedupUserAgents acota los clientes distintos que guarda un resumen.
const maxDedupUserAgents = 5

// AuditDedupWindow lee NEXO_AUDIT_DEDUP_SECONDS (vacío = 600; 0 = sin
// agrupar; inválido = 600 con aviso).
func AuditDedupWindow() time.Duration {
	raw := strings.TrimSpace(os.Getenv(AuditDedupEnv))
	if raw == "" {
		return DefaultAuditDedupWindow
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > 86400 {
		warnOnce("audit-dedup", fmt.Sprintf("[nexo] %s=%q inválido: se usan %d s", AuditDedupEnv, raw, int(DefaultAuditDedupWindow.Seconds())))
		return DefaultAuditDedupWindow
	}
	return time.Duration(n) * time.Second
}

var (
	credFPKey     [32]byte
	credFPKeyOnce sync.Once
)

// CredentialFingerprint devuelve la huella de una credencial presentada:
// HMAC-SHA256 con una clave aleatoria del proceso, truncada a 64 bits, en
// hexadecimal. Sirve para relacionar los rechazos de un mismo valor dentro de
// la vida del proceso; no permite recuperar el token ni compararlo con otro
// proceso. "" para una credencial vacía.
func CredentialFingerprint(presented string) string {
	presented = strings.TrimSpace(presented)
	if presented == "" {
		return ""
	}
	credFPKeyOnce.Do(func() {
		if _, err := rand.Read(credFPKey[:]); err != nil {
			panic(fmt.Sprintf("nexo: credential fingerprint key: %v", err))
		}
	})
	mac := hmac.New(sha256.New, credFPKey[:])
	_, _ = mac.Write([]byte(presented))
	return hex.EncodeToString(mac.Sum(nil)[:8])
}

// DedupKey identifica un rechazo repetido.
type DedupKey struct {
	Action      string // sync.auth, dashboard.login
	Reason      string // reason_code
	IP          string
	Fingerprint string // CredentialFingerprint
}

func (k DedupKey) String() string {
	return k.Action + "|" + k.Reason + "|" + k.IP + "|" + k.Fingerprint
}

// DedupSummary es el resumen de una ventana cerrada con repetidos.
type DedupSummary struct {
	Key           DedupKey
	Repeated      int       // rechazos no escritos durante la ventana
	FirstAt       time.Time // primer repetido de la ventana
	LastAt        time.Time // último repetido de la ventana
	WindowStart   time.Time
	Window        time.Duration
	UserAgents    []string // clientes distintos vistos (hasta 5)
	ActorSource   string
	Project       string
	PrincipalID   string // persona del token (revocado o persona deshabilitada), si se conoce
	CredentialID  string // id del token, si se conoce
	OverflowAgent bool   // hubo más clientes que los guardados
}

type dedupState struct {
	windowStart time.Time
	repeated    int
	firstAt     time.Time
	lastAt      time.Time
	agents      []string
	overflow    bool
	sample      DedupSummary // datos fijos de la clave (fuente, proyecto, persona)
}

// AuditDedup agrupa rechazos repetidos. Seguro para uso concurrente. Una
// instancia por servidor (el estado vive en memoria de la réplica).
type AuditDedup struct {
	window time.Duration
	now    func() time.Time

	mu     sync.Mutex
	states map[string]*dedupState
}

// NewAuditDedup crea el agrupador. window <= 0 lo desactiva (Observe siempre
// pide escribir). now = nil usa time.Now.
func NewAuditDedup(window time.Duration, now func() time.Time) *AuditDedup {
	if now == nil {
		now = time.Now
	}
	return &AuditDedup{window: window, now: now, states: map[string]*dedupState{}}
}

// Window devuelve la ventana configurada (0 = desactivado).
func (d *AuditDedup) Window() time.Duration {
	if d == nil {
		return 0
	}
	return d.window
}

// DedupInfo son los datos fijos de un rechazo que acompañan al resumen.
type DedupInfo struct {
	UserAgent    string
	ActorSource  string
	Project      string
	PrincipalID  string
	CredentialID string
}

// Observe decide qué hacer con un rechazo:
//   - write = true: escribir el evento completo (primero de la clave, o
//     agrupador desactivado o lleno);
//   - write = false: ya se contó como repetido;
//   - pending != nil: la ventana anterior de la clave se cerró con repetidos;
//     hay que escribir ese resumen antes (orden cronológico).
func (d *AuditDedup) Observe(key DedupKey, info DedupInfo) (write bool, pending *DedupSummary) {
	if d == nil || d.window <= 0 || key.Fingerprint == "" {
		return true, nil
	}
	now := d.now()
	k := key.String()
	d.mu.Lock()
	defer d.mu.Unlock()
	st, ok := d.states[k]
	if ok && now.Sub(st.windowStart) >= d.window {
		// Ventana vencida.
		if st.repeated > 0 {
			s := st.summary(key, d.window)
			pending = &s
			// El episodio sigue: este rechazo abre una ventana nueva y se cuenta.
			st.windowStart, st.repeated, st.agents, st.overflow = now, 0, nil, false
			st.add(now, info.UserAgent)
			return false, pending
		}
		delete(d.states, k)
		ok = false
	}
	if !ok {
		if len(d.states) >= maxDedupKeys {
			return true, nil
		}
		d.states[k] = &dedupState{windowStart: now, sample: DedupSummary{
			ActorSource: info.ActorSource, Project: info.Project, PrincipalID: info.PrincipalID, CredentialID: info.CredentialID,
		}}
		return true, nil
	}
	st.add(now, info.UserAgent)
	return false, nil
}

func (st *dedupState) add(now time.Time, ua string) {
	if st.repeated == 0 {
		st.firstAt = now
	}
	st.repeated++
	st.lastAt = now
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return
	}
	for _, a := range st.agents {
		if a == ua {
			return
		}
	}
	if len(st.agents) >= maxDedupUserAgents {
		st.overflow = true
		return
	}
	st.agents = append(st.agents, ua)
}

func (st *dedupState) summary(key DedupKey, window time.Duration) DedupSummary {
	s := st.sample
	s.Key = key
	s.Repeated = st.repeated
	s.FirstAt, s.LastAt = st.firstAt, st.lastAt
	s.WindowStart, s.Window = st.windowStart, window
	s.UserAgents = append([]string(nil), st.agents...)
	sort.Strings(s.UserAgents)
	s.OverflowAgent = st.overflow
	return s
}

// Flush devuelve los resúmenes de las ventanas vencidas (all = true incluye
// las abiertas, por ejemplo al apagar). Las claves sin repetidos y vencidas se
// olvidan; las que tenían repetidos siguen con una ventana nueva vacía.
func (d *AuditDedup) Flush(all bool) []DedupSummary {
	if d == nil || d.window <= 0 {
		return nil
	}
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []DedupSummary
	for k, st := range d.states {
		expired := now.Sub(st.windowStart) >= d.window
		if !expired && !all {
			continue
		}
		if st.repeated == 0 {
			if expired {
				delete(d.states, k)
			}
			continue
		}
		key := parseDedupKey(k)
		out = append(out, st.summary(key, d.window))
		st.windowStart, st.repeated, st.agents, st.overflow = now, 0, nil, false
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstAt.Before(out[j].FirstAt) })
	return out
}

// Len devuelve las claves vivas (pruebas).
func (d *AuditDedup) Len() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.states)
}

func parseDedupKey(s string) DedupKey {
	p := strings.SplitN(s, "|", 4)
	for len(p) < 4 {
		p = append(p, "")
	}
	return DedupKey{Action: p[0], Reason: p[1], IP: p[2], Fingerprint: p[3]}
}

// ─── Huella de la credencial rechazada en el contexto de la petición ─────────

// RecordCredentialFingerprint guarda en el contexto de la petición la huella
// de la credencial rechazada (la usa el agrupador de auditoría). Sin efecto si
// el contexto no tiene el contenedor del middleware.
func RecordCredentialFingerprint(ctx context.Context, fp string) {
	if ctx == nil {
		return
	}
	h, ok := ctx.Value(tokenFactsKey{}).(*tokenFactsHolder)
	if !ok {
		return
	}
	h.mu.Lock()
	h.credFP = fp
	h.mu.Unlock()
}

// CredentialFingerprintFrom devuelve la huella guardada ("" si no hay).
func CredentialFingerprintFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	h, ok := ctx.Value(tokenFactsKey{}).(*tokenFactsHolder)
	if !ok {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.credFP
}
