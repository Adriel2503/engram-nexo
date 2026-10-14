package security

// NX-07: redacción de texto externo (la proyección por esquema está en nexo_projection.go).
import (
	"errors"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
)

// NexoRedactAuditText protege registros frente a credenciales recibidas como
// texto libre (por ejemplo, un agente o filtro mal enviado).
func NexoRedactAuditText(text string) string { return redact.Text(text) }

// NexoRedactExternal limpia un texto que viene de afuera (User-Agent,
// declaración del cliente, nombre, nota) antes de guardarlo o devolverlo: quita
// formatos de credencial y cadenas de conexión, y recién después recorta. Así
// un recorte no deja a medias una credencial.
func NexoRedactExternal(text string, maxRunes int) string { return redact.External(text, maxRunes) }

// NexoRedactAuditMetadata limpia recursivamente claves y valores de texto.
func NexoRedactAuditMetadata(meta map[string]any) map[string]any { return redact.Metadata(meta) }

// NexoMaxTokenNameRunes es el largo máximo del nombre de un token.
const NexoMaxTokenNameRunes = 256

// NexoCheckTokenName valida el nombre de un token en el punto común de
// creación: largo acotado, sin caracteres de control y sin formatos de
// credencial. Devuelve el nombre recortado o un error con texto para quien
// lo envía (sin repetir el valor).
func NexoCheckTokenName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if len([]rune(name)) > NexoMaxTokenNameRunes {
		return "", ErrTokenNameInvalid
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", ErrTokenNameInvalid
		}
	}
	if redact.Has(name) || redact.Connection.MatchString(name) {
		return "", ErrTokenNameInvalid
	}
	return name, nil
}

// ErrTokenNameInvalid es el rechazo de un nombre de token no válido.
var ErrTokenNameInvalid = errors.New("token_name_invalid: el nombre del token no es válido; usa hasta 256 caracteres sin credenciales")
