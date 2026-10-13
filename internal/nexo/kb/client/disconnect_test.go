package client

// `engram nexo disconnect` solo borra servidor y token de cloud.json si son
// los de la conexión Nexo (nexo.json con el mismo servidor); una
// configuración cloud ajena queda igual y se informa.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloudconfig"
)

func TestDisconnectKeepsForeignCloudConfig(t *testing.T) {
	cases := []struct {
		name        string
		nexoServer  string // "" = sin nexo.json
		cloudServer string
		wantCleared bool
	}{
		{"sin nexo.json", "", "https://cloud.otro.example", false},
		{"otro servidor", "https://nexo.example", "https://cloud.otro.example", false},
		{"mismo servidor", "https://nexo.example/", "https://nexo.example", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			t.Setenv(EnvLocalDB, filepath.Join(t.TempDir(), "kb.db"))
			if err := cloudconfig.Save(dataDir, &cloudconfig.Config{ServerURL: tc.cloudServer, Token: "tok-ajeno"}); err != nil {
				t.Fatal(err)
			}
			if tc.nexoServer != "" {
				if err := saveConnection(dataDir, Connection{Server: tc.nexoServer}); err != nil {
					t.Fatal(err)
				}
			}
			res, err := Disconnect(dataDir, true)
			if err != nil {
				t.Fatal(err)
			}
			cc, _ := cloudconfig.Load(dataDir)
			if tc.wantCleared {
				if !res.ClearedCloud || res.KeptCloudServer != "" || cc.Token != "" || cc.ServerURL != "" {
					t.Fatalf("debía borrar cloud.json: res=%+v cloud=%q", res, cc.ServerURL)
				}
			} else if res.ClearedCloud || res.KeptCloudServer != tc.cloudServer || cc.Token != "tok-ajeno" || cc.ServerURL != tc.cloudServer {
				t.Fatalf("no debía tocar cloud.json: res=%+v cloud=%q", res, cc.ServerURL)
			}
			if _, err := os.Stat(filepath.Join(dataDir, ConnectionFile)); !os.IsNotExist(err) {
				t.Fatalf("nexo.json sigue: %v", err)
			}
		})
	}
}
