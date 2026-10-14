// Package redact quita formatos de credencial de texto que viene de afuera
// (User-Agent, declaración del cliente, nombres, notas, claves). Es un paquete
// hoja, sin dependencias del resto de Nexo, para que lo usen el motor de roles,
// el almacén de conocimiento y la capa de seguridad sin ciclos de importación.
//
// NX-07. La detección es de formato (nxa_ de activación, egc_ de token, huellas
// hmac-sha256:v1:, "Bearer x", "Authorization: x", cadenas postgres:// y
// "password=x"), no de palabras sueltas: un título que diga "authorization" no
// se altera.
package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// Credential reconoce los formatos de credencial de Nexo y de cabeceras.
var Credential = regexp.MustCompile(`nxa_[A-Za-z0-9_-]{16,}|egc_[a-z0-9-]+_[0-9a-f]{8}_[A-Za-z0-9-]{20,}|hmac-sha256:v1:[A-Za-z0-9+/_=-]*|(?i:bearer\s+\S+|authorization\s*[:=]\s*\S+(?:\s+\S+)?|password\s*=\s*\S+)`)

// Connection reconoce cadenas de conexión a Postgres.
var Connection = regexp.MustCompile(`postgres(?:ql)?://[^\s"']+`)

// Marcas con las que se reemplaza el texto retirado.
const (
	CredentialMark = "[credencial oculta]"
	ConnectionMark = "[conexión oculta]"
)

// Has indica si el texto trae un formato de credencial o una cadena de conexión.
func Has(text string) bool { return Credential.MatchString(text) || Connection.MatchString(text) }

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
// modificar el original. Los valores tipados (structs, mapas y listas de otros
// tipos, fechas, tipos con nombre) se normalizan antes a JSON, conservando la
// precisión numérica (json.Number), y recién entonces se limpian: así ningún
// valor serializable evita la redacción por no ser un string, un mapa o una lista.
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

// Value limpia cualquier valor serializable (ver Metadata) y lo devuelve como
// árbol de mapas, listas, cadenas, json.Number, booleanos y nil.
func Value(value any) any { return redactValue(value) }

// JSON limpia claves y valores de un documento JSON (por ejemplo el payload de
// una memoria) y devuelve el JSON limpio. Si no es JSON válido devuelve nil.
func JSON(raw []byte) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil
	}
	out, err := json.Marshal(redactValue(tree))
	if err != nil {
		return nil
	}
	return out
}

func redactValue(value any) any {
	switch v := value.(type) {
	case nil, bool:
		return v
	case json.Number:
		return canonicalNumber(v)
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
	return normalize(value)
}

// canonicalNumber devuelve el número como lo daría json.Unmarshal (float64), que es lo que esperan los lectores
// de los metadatos, salvo que un entero no quepa exactamente en un float64: entonces int64, para no perder
// precisión (9007199254740993 sigue siendo 9007199254740993). Un número fuera de int64 queda como json.Number.
func canonicalNumber(n json.Number) any {
	if i, err := n.Int64(); err == nil {
		if int64(float64(i)) == i {
			return float64(i)
		}
		return i
	}
	if strings.ContainsAny(string(n), ".eE") {
		if f, err := n.Float64(); err == nil {
			return f
		}
	}
	return n // entero fuera de int64 (o ilegible): se conserva el texto exacto
}

// normalize pasa un valor tipado por JSON y limpia el resultado. Un valor que
// no se puede serializar se reemplaza por una marca, nunca se deja pasar.
func normalize(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return CredentialMark
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return CredentialMark
	}
	switch tree.(type) {
	case map[string]any, []any, string:
		return redactValue(tree)
	}
	if n, ok := tree.(json.Number); ok {
		return canonicalNumber(n)
	}
	return tree // booleano o nulo
}

// HasAny indica si algún texto (clave o valor, anidado a cualquier profundidad)
// de un valor serializable trae un formato de credencial o una cadena de
// conexión. Se usa para rechazar la escritura de campos de texto libre.
func HasAny(value any) bool {
	switch v := value.(type) {
	case nil, bool, json.Number:
		return false
	case string:
		return Has(v)
	case map[string]any:
		for k, x := range v {
			if Has(k) || HasAny(x) {
				return true
			}
		}
		return false
	case []any:
		for _, x := range v {
			if HasAny(x) {
				return true
			}
		}
		return false
	case []string:
		for _, x := range v {
			if Has(x) {
				return true
			}
		}
		return false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return true // no serializable: no se puede revisar, se rechaza
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if dec.Decode(&tree) != nil {
		return true
	}
	switch tree.(type) {
	case map[string]any, []any, string:
		return HasAny(tree)
	}
	return false
}
