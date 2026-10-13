package embed

import "testing"

func TestKeyStatusReportsOriginNotValue(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		name string
		cfg  Config
		env  map[string]string
		want KeyInfo
	}{
		{"variable propia", Config{Provider: ProviderGemini, APIKey: "k", AuthHeader: ""}, map[string]string{EnvAPIKey: "k"}, KeyInfo{Required: true, Configured: true, Source: KeyFromEnv, Variable: EnvAPIKey}},
		{"archivo", Config{Provider: ProviderGemini, APIKey: "k"}, map[string]string{EnvAPIKeyFile: "/run/secrets/k"}, KeyInfo{Required: true, Configured: true, Source: KeyFromFile, Variable: EnvAPIKeyFile}},
		{"variable del proveedor", Config{Provider: ProviderOpenAI, APIKey: "k", AuthHeader: AuthBearer}, map[string]string{"OPENAI_API_KEY": "k"}, KeyInfo{Required: true, Configured: true, Source: KeyFromEnv, Variable: "OPENAI_API_KEY"}},
		{"sin clave", Config{Provider: ProviderGemini}, nil, KeyInfo{Required: true}},
		{"local sin autenticación", Config{Provider: ProviderOpenAICompatible, AuthHeader: AuthNone}, nil, KeyInfo{}},
		{"apagado", Config{Provider: ProviderOff}, nil, KeyInfo{}},
	}
	for _, c := range cases {
		if got := KeyStatus(c.cfg, env(c.env)); got != c.want {
			t.Errorf("%s: %+v, quería %+v", c.name, got, c.want)
		}
	}
	if got := KeyStatus(Config{Provider: ProviderGemini, APIKey: "k"}, nil); !got.Configured || got.Source != "" {
		t.Errorf("sin entorno no se sabe el origen: %+v", got)
	}
}
