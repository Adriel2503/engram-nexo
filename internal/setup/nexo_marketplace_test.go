package setup

// NX-10: un catálogo `engram` que apunta al Engram oficial (u otro origen) se
// reemplaza por el del fork antes de `marketplace add`; con el catálogo del
// fork, o sin catálogo, la instalación ejecuta los mismos comandos que antes.
// Los ejecutables `claude` y `codex` se simulan con runCommand.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// captureNexoStderr redirige los avisos de Nexo a un archivo temporal y
// devuelve una función que lee lo escrito.
func captureNexoStderr(t *testing.T) func() string {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	old := nexoStderr
	nexoStderr = tmp
	t.Cleanup(func() {
		nexoStderr = old
		_ = tmp.Close()
	})
	return func() string {
		data, err := os.ReadFile(tmp.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
}

func writeKnownMarketplaces(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "plugins")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "known_marketplaces.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runClaudeInstall(t *testing.T, removeErr error) []string {
	t.Helper()
	lookPathFn = func(string) (string, error) { return "claude", nil }
	writeClaudeCodeUserMCPFn = func() error { return nil }
	var commands []string
	runCommand = func(_ string, args ...string) ([]byte, error) {
		cmd := strings.Join(args, " ")
		commands = append(commands, cmd)
		if strings.HasPrefix(cmd, "plugin marketplace remove") && removeErr != nil {
			return []byte("remove denied"), removeErr
		}
		return []byte("ok"), nil
	}
	if _, err := installClaudeCode(); err != nil {
		t.Fatalf("installClaudeCode: %v", err)
	}
	return commands
}

func TestNexoClaudeMarketplaceForeignIsReplaced(t *testing.T) {
	resetSetupSeams(t)
	home := useTestHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	readNexoStderr := captureNexoStderr(t)
	writeKnownMarketplaces(t, home, `{"engram":{"source":{"source":"github","repo":"Gentleman-Programming/engram"}}}`)

	got := runClaudeInstall(t, nil)
	want := []string{"plugin marketplace remove engram", "plugin marketplace add " + claudeCodeMarketplace, "plugin install engram"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
	if out := readNexoStderr(); !strings.Contains(out, "Gentleman-Programming/engram") || !strings.Contains(out, "se reemplaza") {
		t.Fatalf("aviso inesperado: %q", out)
	}
}

func TestNexoClaudeMarketplaceForkOrMissingUnchanged(t *testing.T) {
	for name, body := range map[string]string{
		"fork github": `{"engram":{"source":{"source":"github","repo":"Adriel2503/engram-nexo"}}}`,
		"fork git":    `{"engram":{"source":{"source":"git","url":"https://github.com/Adriel2503/engram-nexo.git"}}}`,
		"otro nombre": `{"claude-plugins-official":{"source":{"source":"github","repo":"anthropics/claude-plugins-official"}}}`,
		"invalido":    `{`,
		"sin archivo": "",
	} {
		t.Run(name, func(t *testing.T) {
			resetSetupSeams(t)
			home := useTestHome(t)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			if body != "" {
				writeKnownMarketplaces(t, home, body)
			}
			got := runClaudeInstall(t, nil)
			want := []string{"plugin marketplace add " + claudeCodeMarketplace, "plugin install engram"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("commands = %v, want %v", got, want)
			}
		})
	}
}

func TestNexoClaudeMarketplaceRemoveFailureWarns(t *testing.T) {
	resetSetupSeams(t)
	home := useTestHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	readNexoStderr := captureNexoStderr(t)
	writeKnownMarketplaces(t, home, `{"engram":{"source":{"source":"github","repo":"Gentleman-Programming/engram"}}}`)

	got := runClaudeInstall(t, errors.New("exit 1"))
	if len(got) != 3 || got[0] != "plugin marketplace remove engram" {
		t.Fatalf("commands = %v", got)
	}
	out := readNexoStderr()
	for _, want := range []string{"remove denied", "claude plugin marketplace remove engram", "claude plugin marketplace add " + claudeCodeMarketplace, "engram setup claude-code"} {
		if !strings.Contains(out, want) {
			t.Errorf("aviso sin %q: %q", want, out)
		}
	}
}

func TestNexoCodexMarketplace(t *testing.T) {
	cases := []struct {
		name       string
		table      string
		wantRemove bool
	}{
		{"oficial", "[marketplaces.engram]\nsource_type = \"git\"\nsource = \"https://github.com/Gentleman-Programming/engram.git\"\n", true},
		{"fork otra rama", "[marketplaces.engram]\nsource_type = \"git\"\nsource = \"https://github.com/Adriel2503/engram-nexo.git\"\nref = \"develop\"\n", true},
		{"fork rama estable", "[marketplaces.engram]\nsource_type = \"git\"\nsource = \"https://github.com/Adriel2503/engram-nexo.git\"\nref = \"" + codexMarketplaceRef + "\"\n", false},
		{"sin catalogo", "[marketplaces.otro]\nsource = \"https://github.com/Gentleman-Programming/engram.git\"\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetSetupSeams(t)
			profile := useIsolatedProfile(t)
			captureNexoStderr(t)
			config := filepath.Join(profile, ".codex", "config.toml")
			if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config, []byte(tc.table), 0o600); err != nil {
				t.Fatal(err)
			}
			lookPathFn = func(string) (string, error) { return "codex", nil }
			var commands []string
			runCommand = func(_ string, args ...string) ([]byte, error) {
				commands = append(commands, strings.Join(args, " "))
				return []byte("ok"), nil
			}
			if _, err := Install("codex"); err != nil {
				t.Fatalf("Install(codex): %v", err)
			}
			want := []string{"plugin marketplace add " + codexMarketplace + " --ref " + codexMarketplaceRef, "plugin add engram@engram"}
			if tc.wantRemove {
				want = append([]string{"plugin marketplace remove engram"}, want...)
			}
			if !reflect.DeepEqual(commands, want) {
				t.Fatalf("commands = %v, want %v", commands, want)
			}
		})
	}
}

// TestNexoPluginVersionScheme: la versión del plugin del fork es SemVer sin
// prerelease (parche = parche de Engram * 100 + número de Nexo), cumple el
// piso de Claude Code y ordena después de la versión de Engram de la que sale.
func TestNexoPluginVersionScheme(t *testing.T) {
	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var manifest struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		return manifest.Version
	}
	for path, engramVersion := range map[string][3]int{
		"../../plugin/claude-code/.claude-plugin/plugin.json": {0, 1, 12},
		"../../plugin/codex/.codex-plugin/plugin.json":        {0, 1, 5},
	} {
		version := read(path)
		values, prerelease, ok := parseClaudeCodePluginVersion(version)
		if !ok || prerelease || !meetsClaudeCodeSlimPluginFloor(version) {
			t.Fatalf("%s: versión %q no válida para setup (ok=%v prerelease=%v)", path, version, ok, prerelease)
		}
		if values[0] != engramVersion[0] || values[1] != engramVersion[1] || values[2]/100 != engramVersion[2] || values[2]%100 == 0 {
			t.Fatalf("%s: versión %q no sigue <mayor>.<menor>.<parche de Engram * 100 + n> sobre %v", path, version, engramVersion)
		}
	}
}
