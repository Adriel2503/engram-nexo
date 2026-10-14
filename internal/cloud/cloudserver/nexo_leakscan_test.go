package cloudserver

// Nexo IA · NX-07 fase S5 (CA-S13, RS-40, RS-43) — buscador de secretos que
// comparten las pruebas de respuestas (S5-01) y de registros (S5-03).
//
// Busca en un texto:
//   - cada token en claro conocido por la prueba y el token compartido;
//   - el hash con pepper de cada token (formato hmac-sha256:v1:..., también
//     sin el prefijo) y su sha256 en hex (por si alguien lo "anonimiza" así);
//   - cualquier token completo con la forma de Engram (egc_<entorno>_<8 hex>_<secreto>);
//     el prefijo visible (egc_live_<8 hex>) no es secreto y no coincide;
//   - cualquier valor con el formato del hash de Engram (hmac-sha256:v1:);
//   - el valor de una cabecera Authorization (Bearer/Basic) o un "Bearer <valor>".

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	nexoActivationCodeRe = regexp.MustCompile(`nxa_[A-Za-z0-9_-]{43}`)
	nexoFullTokenRe      = regexp.MustCompile(`egc_[a-z0-9-]+_[0-9a-f]{8}_[A-Za-z0-9-]{20,}`)
	nexoAuthzHdrRe       = regexp.MustCompile(`(?i)authorization["']?\s*[:=]\s*["']?(bearer|basic)\s+\S+`)
	nexoBearerRe         = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`)
)

const nexoManagedHashPrefix = "hmac-sha256:v1:"

type nexoLeakScanner struct {
	values map[string]string // valor secreto -> etiqueta
}

func newNexoLeakScanner() *nexoLeakScanner {
	return &nexoLeakScanner{values: map[string]string{}}
}

// addToken registra un token en claro, su hash con pepper (si hay hasher) y su
// sha256 en hex.
func (s *nexoLeakScanner) addToken(label, raw string, hash func(string) (string, error)) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	s.values[raw] = "token " + label
	sum := sha256.Sum256([]byte(raw))
	s.values[hex.EncodeToString(sum[:])] = "sha256 of token " + label
	if hash != nil {
		if h, err := hash(raw); err == nil {
			s.addHash(label, h)
		}
	}
}

// addHash registra un hash guardado en cloud_principal_tokens.token_hash.
func (s *nexoLeakScanner) addHash(label, h string) {
	if h = strings.TrimSpace(h); h == "" {
		return
	}
	s.values[h] = "token hash " + label
	if mac := strings.TrimPrefix(h, nexoManagedHashPrefix); len(mac) >= 16 {
		s.values[mac] = "token hash " + label
	}
}

// find devuelve los hallazgos en text. allow son valores esperados en ese
// texto (el token recién emitido en la respuesta de emisión).
func (s *nexoLeakScanner) find(text string, allow ...string) []string {
	for _, a := range allow {
		if a != "" {
			text = strings.ReplaceAll(text, a, "<allowed>")
		}
	}
	var out []string
	for v, label := range s.values {
		if strings.Contains(text, v) {
			out = append(out, label)
		}
	}
	if nexoActivationCodeRe.MatchString(text) {
		out = append(out, "código de activación oculto")
	}
	if m := nexoFullTokenRe.FindString(text); m != "" {
		out = append(out, "full managed token "+nexoRedact(m))
	}
	if strings.Contains(text, nexoManagedHashPrefix) {
		out = append(out, "managed token hash format ("+nexoManagedHashPrefix+")")
	}
	if m := nexoAuthzHdrRe.FindString(text); m != "" {
		out = append(out, "Authorization header value "+nexoRedact(m))
	}
	if m := nexoBearerRe.FindString(text); m != "" {
		out = append(out, "bearer credential "+nexoRedact(m))
	}
	sort.Strings(out)
	return out
}

// nexoRedact recorta un hallazgo para que el mensaje de la prueba no repita el
// secreto completo.
func nexoRedact(s string) string {
	if len(s) > 18 {
		return s[:18] + "..."
	}
	return s
}

// nexoForbiddenKeys recorre un JSON y devuelve las claves con nombre de
// secreto (las mismas reglas que los tipos de respuesta).
func nexoForbiddenKeys(body []byte) []string {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil
	}
	var out []string
	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				if bad, ok := nexoForbiddenJSONName(k); ok {
					out = append(out, fmt.Sprintf("%s.%s (%s)", path, k, bad))
				}
				walk(child, path+"."+k)
			}
		case []any:
			for _, child := range x {
				walk(child, path+"[]")
			}
		}
	}
	walk(v, "$")
	sort.Strings(out)
	return out
}

func TestNexoLeakScannerDetects(t *testing.T) {
	s := newNexoLeakScanner()
	raw := "egc_live_0123abcd_" + strings.Repeat("Ab-9", 11)
	s.addToken("t", raw, func(string) (string, error) { return nexoManagedHashPrefix + "AAAABBBBCCCCDDDDEEEE", nil })
	for name, text := range map[string]string{
		"raw":     "user sent " + raw,
		"hash":    `{"x":"AAAABBBBCCCCDDDDEEEE"}`,
		"format":  "hmac-sha256:v1:zzzz",
		"header":  "Authorization: Bearer abcdefgh12345678",
		"bearer":  "token=Bearer abcdefgh12345678",
		"generic": "egc_live_99999999_" + strings.Repeat("x", 30),
	} {
		if len(s.find(text)) == 0 {
			t.Errorf("%s: no hallazgo en %q", name, text)
		}
	}
	if got := s.find(`{"token_prefix":"egc_live_0123abcd","name":"Authorization"}`); len(got) != 0 {
		t.Errorf("el prefijo visible no es secreto: %v", got)
	}
	if got := s.find("issued "+raw, raw); len(got) != 0 {
		t.Errorf("valor permitido: %v", got)
	}
	if got := nexoForbiddenKeys([]byte(`{"items":[{"token_hash":"x","ok":{"password":1}}]}`)); len(got) != 2 {
		t.Errorf("claves prohibidas: %v", got)
	}
}
