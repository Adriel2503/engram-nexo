package setup

// NX-10 tanda 2: `engram setup` instala el protocolo Nexo solo con la PC
// conectada (nexo.json), en claude-code y codex como skill propia y en los
// agentes con archivo de reglas como bloque administrado o archivo propio.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	nexosetup "github.com/Gentleman-Programming/engram/v3/internal/nexo/setup"
)

// connectNexo simula `engram nexo connect`: el marcador en la carpeta de datos.
func connectNexo(t *testing.T, home string) {
	t.Helper()
	dataDir := filepath.Join(home, ".engram")
	t.Setenv("ENGRAM_DATA_DIR", dataDir)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "nexo.json"), []byte(`{"server":"https://nexo.example","label":"luis"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readOrEmpty(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func TestNexoProtocolNotInstalledWithoutConnection(t *testing.T) {
	home := stubRegistryEnv(t)
	t.Setenv("ENGRAM_DATA_DIR", "")
	if _, err := Install("windsurf"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readOrEmpty(windsurfRulesPath()), nexosetup.BlockBegin) {
		t.Fatal("sin conexión, setup no debe tocar el protocolo Nexo")
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor", "nexo-protocol.md")); err == nil {
		t.Fatal("archivo Nexo inesperado")
	}
}

func TestNexoProtocolRulesFileAgents(t *testing.T) {
	home := stubRegistryEnv(t)
	connectNexo(t, home)
	// Windsurf: bloque junto al de Engram, idempotente.
	for i := 0; i < 2; i++ {
		if _, err := Install("windsurf"); err != nil {
			t.Fatal(err)
		}
	}
	rules := readOrEmpty(windsurfRulesPath())
	if !strings.Contains(rules, engramMarkerBegin) || strings.Count(rules, nexosetup.BlockBegin) != 1 || !strings.Contains(rules, "nexo_search") {
		t.Fatalf("windsurf: bloques Engram y Nexo:\n%s", rules)
	}
	// Cursor: archivo informativo propio, sin front matter.
	if _, err := Install("cursor"); err != nil {
		t.Fatal(err)
	}
	cur := readOrEmpty(filepath.Join(home, ".cursor", "nexo-protocol.md"))
	if !strings.Contains(cur, "nexo_search") || strings.HasPrefix(cur, "---") {
		t.Fatalf("cursor: %q", cur[:min(len(cur), 80)])
	}
	// VS Code: instrucciones propias con applyTo.
	if _, err := Install("vscode-copilot"); err != nil {
		t.Fatal(err)
	}
	vs := readOrEmpty(filepath.Join(filepath.Dir(vscodePromptPath()), "nexo.instructions.md"))
	if !strings.HasPrefix(vs, "---\napplyTo:") {
		t.Fatalf("vscode: %q", vs[:min(len(vs), 80)])
	}
	// Todos los agentes declarativos y gemini/pi resuelven destinos.
	for _, slug := range supportedSlugs() {
		tg, err := nexoTargets(slug)
		if err != nil || len(tg.SkillDirs)+len(tg.BlockFiles)+len(tg.OwnFiles) == 0 {
			t.Errorf("%s sin destino Nexo: %+v %v", slug, tg, err)
		}
	}
}

func TestNexoProtocolSkillAgents(t *testing.T) {
	home := stubRegistryEnv(t)
	connectNexo(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	files, err := NexoInstallProtocol("claude-code")
	if err != nil || len(files) != 1 {
		t.Fatalf("claude-code: %v %v", files, err)
	}
	skill := readOrEmpty(filepath.Join(home, ".claude", "skills", "nexo-protocol", "SKILL.md"))
	if skill != nexosetup.Skill() || !strings.HasPrefix(skill, "---\nname: nexo-protocol") {
		t.Fatal("claude-code: skill distinta de la fuente")
	}
	files, err = NexoInstallProtocol("codex")
	if err != nil || len(files) != 2 {
		t.Fatalf("codex: %v %v", files, err)
	}
	for _, p := range []string{filepath.Join(home, ".codex", "skills", "nexo-protocol", "SKILL.md"), filepath.Join(home, ".agents", "skills", "nexo-protocol", "SKILL.md")} {
		if readOrEmpty(p) != nexosetup.Skill() {
			t.Fatalf("codex: falta %s", p)
		}
	}
	// El enganche de Install también la instala aunque falle el CLI del
	// agente (claude no está en el PATH de la prueba).
	_ = os.RemoveAll(filepath.Join(home, ".claude"))
	lookPathFn = func(string) (string, error) { return "", os.ErrNotExist }
	_, _ = Install("claude-code")
	if readOrEmpty(filepath.Join(home, ".claude", "skills", "nexo-protocol", "SKILL.md")) == "" {
		t.Fatal("Install(claude-code) no dejó la skill de Nexo")
	}
}

func TestNexoSkillPluginCopiesMatchSource(t *testing.T) {
	for _, p := range []string{"claude-code", "codex"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "plugin", p, "skills", "nexo-protocol", "SKILL.md"))
		if err != nil || string(b) != nexosetup.Skill() {
			t.Fatalf("plugin/%s/skills/nexo-protocol/SKILL.md diverge de internal/nexo/setup/SKILL.md (go generate ./internal/nexo/setup/): %v", p, err)
		}
	}
}
