package security

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNexoActivationTTL(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  time.Duration
		bad   bool
	}{{"", 24 * time.Hour, false}, {"  ", 24 * time.Hour, false}, {"1h", time.Hour, false}, {"168h", 168 * time.Hour, false},
		{"0", 0, true}, {"169h", 0, true}, {"59m", 0, true}, {"texto-secreto-ttl", 0, true}, {"-5h", 0, true}} {
		t.Setenv(ActivationTTLEnv, tt.value)
		d, err := ActivationTTLFromEnv()
		if tt.bad {
			if err == nil || !errors.Is(err, ErrActivationTTLInvalid) {
				t.Fatalf("%q debía rechazarse", tt.value)
			}
			if !strings.Contains(err.Error(), ActivationTTLEnv) || (len(tt.value) > 3 && strings.Contains(err.Error(), tt.value)) {
				t.Fatalf("el error debe nombrar la variable sin mostrar el valor: %v", err)
			}
			continue
		}
		if err != nil || d != tt.want {
			t.Fatalf("plazo inesperado para %q: %v %v", tt.value, d, err)
		}
	}
	a, err := GenerateActivationCode()
	if err != nil {
		t.Fatal("no se pudo generar")
	}
	b, err := GenerateActivationCode()
	if err != nil || a == b || len(a) != 47 {
		t.Fatal("códigos inválidos")
	}
}
