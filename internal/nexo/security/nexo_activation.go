package security

// NX-07: los códigos son credenciales de entrega, nunca tokens de acceso.
import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"
)

// ActivationTTLEnv es la variable con el plazo de los códigos de activación.
const ActivationTTLEnv = "NEXO_ACTIVATION_TTL"

// Plazos del código de activación.
const (
	DefaultActivationTTL = 24 * time.Hour
	MinActivationTTL     = time.Hour
	MaxActivationTTL     = 7 * 24 * time.Hour
)

// ErrActivationTTLInvalid se devuelve ante un NEXO_ACTIVATION_TTL inválido. El
// texto nombra la variable y nunca incluye el valor recibido.
var ErrActivationTTLInvalid = fmt.Errorf("%s no es válido: debe ser una duración de Go entre %s y %s (por ejemplo 24h); corrígela o quítala para usar %s", ActivationTTLEnv, MinActivationTTL, MaxActivationTTL, DefaultActivationTTL)

// ParseActivationTTL interpreta el plazo: vacío usa 24 horas; una duración
// fuera de 1 hora a 7 días, o que no se entiende, es un error.
func ParseActivationTTL(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultActivationTTL, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < MinActivationTTL || d > MaxActivationTTL {
		return 0, ErrActivationTTLInvalid
	}
	return d, nil
}

// ActivationTTLFromEnv lee NEXO_ACTIVATION_TTL. Se valida al arrancar el
// servidor; un valor inválido nunca se reemplaza en silencio por el de defecto.
func ActivationTTLFromEnv() (time.Duration, error) {
	return ParseActivationTTL(os.Getenv(ActivationTTLEnv))
}

// GenerateActivationCode crea un código aleatorio de 256 bits (prefijo nxa_).
func GenerateActivationCode() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "nxa_" + base64.RawURLEncoding.EncodeToString(b[:]), nil
}
