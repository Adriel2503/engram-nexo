package security

// NX-07: metadatos de auditoría con proyección explícita. Los campos de texto
// desconocidos se omiten sin permiso de lectura del ámbito.
import (
	"errors"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
)

var nexoAuditTextFields = map[string]bool{}

func init() {
	for _, k := range strings.Fields("id action entity entity_key sync_id observation_sync_id prompt_sync_id session_id project space scope_type scope_key doc_key document_id principal_id target_principal_id actor_principal_id issuer_principal_id token_id activation_id credential_id token_prefix token_name name username owner role role_before role_after role_antes role_despues capability reason reason_code source via status outcome operation from to since cursor route format table date day created_at expires_at valid_until valid_from published_at occurred_at key_type layer severity group minute locked_until expires_in_days engram_role_before engram_role_before engram_role_after default_role visibility title_before title_after") {
		nexoAuditTextFields[k] = true
	}
}
func nexoAuditSecretKey(k string) bool {
	k = strings.ToLower(k)
	for _, s := range []string{"hash", "password", "secret", "pepper", "authorization", "cookie", "raw_token", "activation_code", "code_hash", "api_key"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}
func nexoAuditContentKey(k string) bool {
	k = strings.ToLower(k)
	for _, s := range []string{"title", "content", "payload", "body", "snippet", "excerpt", "heading", "description", "rationale", "question", "answer"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// NexoAuditMetadata deja metadatos y un marcador cuando retiene campos.
// La limpieza recursiva también se aplica con permiso: nunca devuelve credenciales.
func NexoAuditMetadata(meta map[string]any, contentAllowed bool) map[string]any {
	return nexoAuditMap(meta, contentAllowed, 0)
}
func nexoAuditMap(meta map[string]any, allowed bool, depth int) map[string]any {
	if meta == nil {
		return nil
	}
	out := map[string]any{}
	withheld := false
	for k, v := range meta {
		if nexoAuditSecretKey(k) || (!allowed && nexoAuditContentKey(k)) {
			withheld = true
			continue
		}
		val, ok := nexoAuditValue(v, allowed, depth+1, allowed || nexoAuditTextFields[k])
		if !ok {
			withheld = true
			continue
		}
		out[k] = val
	}
	if withheld {
		out["payload_withheld"] = true
	}
	return out
}
func nexoAuditValue(v any, allowed bool, depth int, textAllowed bool) (any, bool) {
	if depth > 12 {
		return nil, false
	}
	switch x := v.(type) {
	case string:
		if !textAllowed || redact.Has(x) {
			return nil, false
		}
		return x, true
	case map[string]any:
		return nexoAuditMap(x, allowed, depth), true
	case []any:
		out := []any{}
		for _, e := range x {
			y, ok := nexoAuditValue(e, allowed, depth+1, textAllowed)
			if !ok {
				return nil, false
			}
			out = append(out, y)
		}
		return out, true
	case bool, float64, int, int64, int32, uint64, nil:
		return v, true
	}
	return nil, false
}

// NexoAuditHasCredential identifica formatos de credencial que nunca deben
// aceptarse como etiquetas o devolverse en evidencia.
func NexoAuditHasCredential(text string) bool { return redact.Has(text) }

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
