package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactFormatsNotWords(t *testing.T) {
	code := "nxa_" + strings.Repeat("Ab1-", 11)
	token := "egc_live_0123abcd_" + strings.Repeat("Zz9-", 11)
	for _, in := range []string{code, token, "x " + code + " y", "Bearer abcdef123", "Authorization: Basic Zm9vOmJhcg==", "hmac-sha256:v1:AAAA", "postgres://u:p@h/db"} {
		if got := Text(in); got == in || strings.Contains(got, code) || strings.Contains(got, token) {
			t.Fatalf("no se retiró: %q -> %q", in, got)
		}
	}
	// Un fragmento de código recortado a la mitad también se retira si queda el prefijo largo.
	if Text("nxa_"+strings.Repeat("a", 20)) == "nxa_"+strings.Repeat("a", 20) {
		t.Fatal("prefijo largo sin retirar")
	}
	for _, ok := range []string{"Authorization policy", "política de authorization", "nxa_corto", "Mozilla/5.0"} {
		if Text(ok) != ok {
			t.Fatalf("se alteró un texto legítimo: %q", ok)
		}
	}
	if External("  "+code+"  ", 10) != "[credencia" {
		t.Fatalf("redactar antes de recortar: %q", External(code, 10))
	}
	meta := map[string]any{"ua": code, "k" + code: []any{token, map[string]any{"x": "Bearer zzz"}}, "n": 3}
	raw, _ := json.Marshal(Metadata(meta))
	if strings.Contains(string(raw), code) || strings.Contains(string(raw), token) || strings.Contains(string(raw), "zzz") || meta["ua"] != code {
		t.Fatalf("metadata: %s", raw)
	}
}
