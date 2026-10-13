package setup

// NX-10 tanda 2: `engram setup` instala el protocolo Nexo solo con la PC
// conectada (nexo.json): en claude-code y codex llega con el plugin del fork
// (opción A de D-K2b-35); en opencode como skill propia y en los agentes con
// archivo de reglas como bloque administrado o archivo propio.

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
		if err != nil || len(tg.SkillDirs)+len(tg.BlockFiles)+len(tg.OwnFiles)+len(tg.PluginSkillDirs) == 0 {
			t.Errorf("%s sin destino Nexo: %+v %v", slug, tg, err)
		}
	}
}

// Claude Code y Codex reciben la skill dentro del plugin del fork: setup no
// la copia aparte y borra copias directas de versiones anteriores.
func TestNexoProtocolSkillAgentsViaPlugin(t *testing.T) {
	home := stubRegistryEnv(t)
	connectNexo(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	if claudeCodeMarketplace != "Adriel2503/engram-nexo" || codexMarketplace != "Adriel2503/engram-nexo" || codexMarketplaceRef != "develop" {
		t.Fatalf("marketplace del plugin: %s %s %s", claudeCodeMarketplace, codexMarketplace, codexMarketplaceRef)
	}
	stale := []string{
		filepath.Join(home, ".claude", "skills", "nexo-protocol"),
		filepath.Join(home, ".codex", "skills", "nexo-protocol"),
		filepath.Join(home, ".agents", "skills", "nexo-protocol"),
	}
	for _, d := range stale {
		if _, err := nexosetup.WriteSkill(filepath.Dir(d)); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(home, ".claude", "skills", "otra", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("---\nname: otra\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"claude-code", "codex"} {
		files, err := NexoInstallProtocol(slug)
		if err != nil || len(files) != 0 {
			t.Fatalf("%s: no debe escribir archivos (la skill llega con el plugin): %v %v", slug, files, err)
		}
	}
	for _, d := range stale {
		if _, err := os.Stat(d); err == nil {
			t.Fatalf("copia directa no borrada: %s", d)
		}
	}
	if readOrEmpty(other) == "" {
		t.Fatal("se borró una skill que no es de Nexo")
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
