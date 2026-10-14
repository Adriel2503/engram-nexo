package secrets

import (
	"bytes"
	"strings"
	"testing"
)

func TestStackSecrets(t *testing.T) {
	a, e := Hex(32)
	if e != nil || len(a) != 64 {
		t.Fatal("generación")
	}
	b, _ := Hex(32)
	if a == b {
		t.Fatal("secretos repetidos")
	}
	var out bytes.Buffer
	w := NewWriter(&out, []string{a})
	_, _ = w.Write([]byte("clave=" + a[:10]))
	_, _ = w.Write([]byte(a[10:] + "\n"))
	_ = w.Close()
	if strings.Contains(out.String(), a) || !strings.Contains(out.String(), "oculto") {
		t.Fatal("filtración")
	}
}
