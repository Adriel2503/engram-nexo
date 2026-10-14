package render

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
	"gopkg.in/yaml.v3"
)

func TestStackTemplates(t *testing.T) {
	for _, mode := range []string{"demo", "dev", "prod"} {
		s := state.State{Mode: mode, Dir: t.TempDir(), Project: state.Project(mode, "prueba"), Port: 18490, PGPort: 55490, Subnet: "172.20.1.0/24", ProxyIP: "172.20.1.10"}
		d := Data{State: s, TwoAccounts: mode != "demo", Env: map[string]string{"ENGRAM_DATABASE_URL": "valor", "ENGRAM_PORT": "18080"}}
		b, e := Template("compose.yaml", d)
		if e != nil {
			t.Fatal(e)
		}
		var tree map[string]any
		if e = yaml.Unmarshal(b, &tree); e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(string(b), "/var/lib/postgresql") || !strings.Contains(string(b), s.Project+"-pg") {
			t.Fatal("volumen")
		}
		if mode == "prod" && strings.Contains(string(b), "55490") {
			t.Fatal("Postgres expuesto")
		}
		if e = Files(d); e != nil {
			t.Fatal(e)
		}
		m, e := ReadEnv(s.Dir)
		if e != nil || m["ENGRAM_DATABASE_URL"] != "valor" {
			t.Fatal("env")
		}
	}
}
func TestStackEnvRejectsLineInjection(t *testing.T) {
	if WriteEnv(t.TempDir(), map[string]string{"KEY": "x\nOTHER=y"}) == nil {
		t.Fatal("inyección")
	}
}
