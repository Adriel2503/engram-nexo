// Package redact quita formatos de credencial de texto que viene de afuera
// (User-Agent, declaración del cliente, nombres, notas, claves). Es un paquete
// hoja, sin dependencias del resto de Nexo, para que lo usen el motor de roles,
// el almacén de conocimiento y la capa de seguridad sin ciclos de importación.
//
// NX-07. La detección es de formato (nxa_ de activación, egc_ de token, huellas
// hmac-sha256:v1:, "Bearer x", "Authorization: x" y cadenas postgres://), no de
// palabras sueltas: un título que diga "authorization" no se altera.
package redact

import (
	"regexp"
	"strings"
)

// Credential reconoce los formatos de credencial de Nexo y de cabeceras.
var Credential = regexp.MustCompile(`nxa_[A-Za-z0-9_-]{16,}|egc_[a-z0-9-]+_[0-9a-f]{8}_[A-Za-z0-9-]{20,}|hmac-sha256:v1:[A-Za-z0-9+/_=-]*|(?i:bearer\s+\S+|authorization\s*[:=]\s*\S+(?:\s+\S+)?)`)

// Connection reconoce cadenas de conexión a Postgres.
var Connection = regexp.MustCompile(`postgres(?:ql)?://[^\s"']+`)

// Marcas con las que se reemplaza el texto retirado.
const (
	CredentialMark = "[credencial oculta]"
	ConnectionMark = "[conexión oculta]"
)

// Has indica si el texto trae un formato de credencial.
func Has(text string) bool { return Credential.MatchString(text) }

// Text reemplaza credenciales y cadenas de conexión.
func Text(text string) string {
	text = Credential.ReplaceAllString(text, CredentialMark)
	return Connection.ReplaceAllString(text, ConnectionMark)
}

// External limpia un texto externo y recién después lo recorta (maxRunes <= 0
// no recorta), así un recorte no deja a medias una credencial.
func External(text string, maxRunes int) string {
	text = Text(strings.TrimSpace(text))
	if maxRunes > 0 {
		if r := []rune(text); len(r) > maxRunes {
			text = string(r[:maxRunes])
		}
	}
	return text
}

// Metadata limpia recursivamente claves y valores de texto de un mapa sin
// modificar el original.
func Metadata(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	out := make(map[string]any, len(meta))
	for key, value := range meta {
		out[Text(key)] = redactValue(value)
	}
	return out
}

func redactValue(value any) any {
	switch v := value.(type) {
	case string:
		return Text(v)
	case map[string]any:
		return Metadata(v)
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = redactValue(x)
		}
		return out
	case []string:
		out := make([]string, len(v))
		for i, x := range v {
			out[i] = Text(x)
		}
		return out
	}
	return value
}
