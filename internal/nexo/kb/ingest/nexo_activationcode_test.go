package ingest

import (
	"strings"
	"testing"
)

// NX-07: el código de activación (nxa_ + 256 bits) se detecta en contenido, título y
// claves (pegado a letras, guion bajo o barras, donde un \b inicial no lo vería).
func TestDetectSecretsActivationCode(t *testing.T) {
	code := fakeSecret("nxa", "_", strings.Repeat("Ab1-", 11))
	for _, in := range []string{code, "texto " + code + " fin", "guias/" + code, "clave_" + code, "# Título " + code, "token=" + code} {
		found := DetectSecrets(in)
		ok := false
		for _, f := range found {
			ok = ok || f.Kind == "activation_code"
		}
		if !ok {
			t.Errorf("no se detectó el código en %q", strings.ReplaceAll(in, code, "<codigo>"))
		}
	}
	for _, in := range []string{"nxa_corto", "el prefijo nxa_ solo", "guias/autorizacion"} {
		if len(DetectSecrets(in)) != 0 {
			t.Errorf("falso positivo en %q", in)
		}
	}
}
