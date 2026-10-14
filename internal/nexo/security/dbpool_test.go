package security

import (
	"strings"
	"testing"
	"time"
)

func poolEnv(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestParsePoolConfigDefaults(t *testing.T) {
	cfg, err := ParsePoolConfig(poolEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg != (PoolConfig{MaxOpen: 0, MaxIdle: 10, MaxLifetime: 30 * time.Minute, MaxIdleTime: 5 * time.Minute}) {
		t.Fatalf("por defecto: %+v", cfg)
	}
}

func TestParsePoolConfigValues(t *testing.T) {
	cfg, err := ParsePoolConfig(poolEnv(map[string]string{
		DBMaxOpenEnv: "40", DBMaxIdleEnv: "20", DBMaxLifetimeEnv: "1h", DBMaxIdleTimeEnv: "90s",
	}))
	if err != nil || cfg != (PoolConfig{MaxOpen: 40, MaxIdle: 20, MaxLifetime: time.Hour, MaxIdleTime: 90 * time.Second}) {
		t.Fatalf("%+v, %v", cfg, err)
	}
	// Solo se baja el máximo: el reposo por defecto se ajusta sin error.
	cfg, err = ParsePoolConfig(poolEnv(map[string]string{DBMaxOpenEnv: "4"}))
	if err != nil || cfg.MaxOpen != 4 || cfg.MaxIdle != 4 {
		t.Fatalf("máximo bajo: %+v, %v", cfg, err)
	}
	// 0 explícito = sin límite (el valor por defecto, OP-13); el reposo no se
	// compara con el máximo.
	cfg, err = ParsePoolConfig(poolEnv(map[string]string{DBMaxOpenEnv: "0", DBMaxIdleEnv: "30"}))
	if err != nil || !cfg.Unlimited() || cfg.MaxIdle != 30 {
		t.Fatalf("sin límite: %+v, %v", cfg, err)
	}
}

func TestParsePoolConfigInvalid(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"texto", map[string]string{DBMaxOpenEnv: "muchas"}, DBMaxOpenEnv},
		{"negativo", map[string]string{DBMaxOpenEnv: "-1"}, DBMaxOpenEnv},
		{"reposo cero", map[string]string{DBMaxIdleEnv: "0"}, DBMaxIdleEnv},
		{"reposo negativo", map[string]string{DBMaxIdleEnv: "-3"}, DBMaxIdleEnv},
		{"decimal", map[string]string{DBMaxOpenEnv: "2.5"}, DBMaxOpenEnv},
		{"enorme", map[string]string{DBMaxOpenEnv: "999999"}, DBMaxOpenEnv},
		{"duración sin unidad", map[string]string{DBMaxLifetimeEnv: "30"}, DBMaxLifetimeEnv},
		{"duración cero", map[string]string{DBMaxIdleTimeEnv: "0s"}, DBMaxIdleTimeEnv},
		{"duración negativa", map[string]string{DBMaxLifetimeEnv: "-5m"}, DBMaxLifetimeEnv},
		{"reposo mayor que el máximo", map[string]string{DBMaxOpenEnv: "5", DBMaxIdleEnv: "8"}, DBMaxIdleEnv},
		// Regresión (revisión adversarial): con 1 conexión el relleno de
		// sesiones al migrar espera una conexión que él mismo retiene.
		{"una conexión bloquea el arranque", map[string]string{DBMaxOpenEnv: "1"}, DBMaxOpenEnv},
		{"debajo del mínimo", map[string]string{DBMaxOpenEnv: "3"}, DBMaxOpenEnv},
	}
	for _, c := range cases {
		_, err := ParsePoolConfig(poolEnv(c.env))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v; debe nombrar %s", c.name, err, c.want)
		}
	}
	// Varios errores se informan juntos.
	_, err := ParsePoolConfig(poolEnv(map[string]string{DBMaxOpenEnv: "x", DBMaxLifetimeEnv: "y"}))
	if err == nil || !strings.Contains(err.Error(), DBMaxOpenEnv) || !strings.Contains(err.Error(), DBMaxLifetimeEnv) {
		t.Fatalf("todos los errores: %v", err)
	}
}

func TestPoolConfigString(t *testing.T) {
	s := DefaultPoolConfig().String()
	for _, want := range []string{"sin limite", "10", "30m0s", "5m0s", DBMaxOpenEnv} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q; falta %q", s, want)
		}
	}
}
