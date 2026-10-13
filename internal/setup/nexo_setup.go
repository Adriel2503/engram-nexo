package setup

// Nexo IA · NX-10 (tanda 2, F-04 dentro del fork): instala el protocolo Nexo
// (skill nexo-protocol) junto con lo que `engram setup` ya instala. Se llama
// desde un único enganche marcado `// NX-10` en Install (setup.go). El texto
// sale de una sola fuente (internal/nexo/setup/SKILL.md).
//
// Claude Code y Codex reciben la skill dentro del plugin, que se instala desde
// el marketplace del fork (claudeCodeMarketplace y codexMarketplace en
// setup.go, opción A de D-K2b-35); los demás agentes la reciben aquí.
//
// Solo actúa si la PC está conectada a Nexo (`engram nexo connect`): sin
// conexión, `engram setup` hace exactamente lo de Engram. Un error al
// instalar el protocolo nunca hace fallar la instalación de Engram: se avisa
// por stderr.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	nexoclient "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	nexosetup "github.com/Gentleman-Programming/engram/v3/internal/nexo/setup"
)

// nexoStderr permite a las pruebas capturar los avisos.
var nexoStderr = os.Stderr

// nexoDataDir resuelve la carpeta de datos de Engram como main.go:
// ENGRAM_DATA_DIR o ~/.engram.
func nexoDataDir() string {
	if dir := strings.TrimSpace(os.Getenv("ENGRAM_DATA_DIR")); dir != "" {
		return dir
	}
	home, err := userHome()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".engram")
}

// nexoAfterInstall instala el protocolo Nexo si la PC está conectada.
func nexoAfterInstall(slug string) {
	dir := nexoDataDir()
	if dir == "" || !nexoclient.Connected(dir) {
		return
	}
	files, err := NexoInstallProtocol(slug)
	if err != nil {
		fmt.Fprintf(nexoStderr, "[nexo] protocolo Nexo no instalado para %s: %v\n", slug, err)
		return
	}
	for _, f := range files {
		fmt.Fprintf(nexoStderr, "[nexo] protocolo Nexo: %s\n", f)
	}
}

// NexoProtocolTargets describe dónde va el protocolo de un agente: carpetas
// de skills (skill propia, como `memory`) o archivos de reglas (bloque
// administrado junto al de Engram, o un archivo propio cuando Engram escribe
// un archivo completo).
type NexoProtocolTargets struct {
	SkillDirs  []string
	BlockFiles []string
	OwnFiles   map[string]string // ruta -> prefijo (front matter)
	// PluginSkillDirs: agentes cuyo plugin (marketplace del fork) ya trae la
	// skill nexo-protocol. Aquí solo se borran copias directas de versiones
	// anteriores para no tener la skill dos veces.
	PluginSkillDirs []string
}

// nexoTargets resuelve los destinos de un agente (las mismas rutas que usa
// Engram para ese agente).
func nexoTargets(slug string) (NexoProtocolTargets, error) {
	home, err := userHome()
	if err != nil {
		return NexoProtocolTargets{}, err
	}
	t := NexoProtocolTargets{OwnFiles: map[string]string{}}
	switch slug {
	case "claude-code": // la skill llega con el plugin del fork (claudeCodeMarketplace)
		t.PluginSkillDirs = []string{filepath.Join(claudeCodeConfigRoot(home), "skills")}
	case "codex": // la skill llega con el plugin del fork (codexMarketplace)
		codexDir := filepath.Join(home, ".codex")
		if p := codexConfigPath(); p != "" {
			codexDir = filepath.Dir(p)
		}
		t.PluginSkillDirs = []string{filepath.Join(codexDir, "skills"), filepath.Join(home, ".agents", "skills")}
	case "opencode":
		t.SkillDirs = []string{filepath.Join(openCodeConfigDir(), "skills")}
	case "pi":
		t.BlockFiles = []string{filepath.Join(piAgentDir(), "AGENTS.md")}
	case "gemini-cli":
		t.BlockFiles = []string{antigravityContextPath()} // ~/.gemini/GEMINI.md
	default:
		found := false
		for _, a := range agentAdapters() {
			if a.slug != slug {
				continue
			}
			found = true
			for _, ins := range a.instructions {
				p := ins.path()
				switch {
				case ins.style == markerBlock:
					t.BlockFiles = append(t.BlockFiles, p)
				case slug == "vscode-copilot":
					t.OwnFiles[filepath.Join(filepath.Dir(p), "nexo.instructions.md")] = "---\napplyTo: \"**\"\n---\n\n"
				default: // cursor: archivo informativo para pegar como User Rule
					t.OwnFiles[filepath.Join(filepath.Dir(p), "nexo-protocol.md")] = ""
				}
			}
		}
		if !found {
			return t, fmt.Errorf("agente desconocido: %q", slug)
		}
	}
	return t, nil
}

// NexoInstallProtocol instala el protocolo Nexo para un agente (lo usan
// `engram setup` con la PC conectada y `engram nexo setup <agente>`).
// Devuelve los archivos escritos.
func NexoInstallProtocol(slug string) ([]string, error) {
	t, err := nexoTargets(slug)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, d := range t.PluginSkillDirs {
		removeDirectNexoSkill(d)
	}
	for _, d := range t.SkillDirs {
		p, err := nexosetup.WriteSkill(d)
		if err != nil {
			return files, err
		}
		files = append(files, p)
	}
	for _, p := range t.BlockFiles {
		if err := nexosetup.UpsertBlock(p); err != nil {
			return files, err
		}
		files = append(files, p)
	}
	for p, prefix := range t.OwnFiles {
		if err := nexosetup.WriteFile(p, prefix); err != nil {
			return files, err
		}
		files = append(files, p)
	}
	return files, nil
}

// removeDirectNexoSkill borra <dir>/nexo-protocol si es una copia directa de
// la skill de Nexo (instalada por versiones anteriores). No toca nada más.
func removeDirectNexoSkill(dir string) {
	skillDir := filepath.Join(dir, "nexo-protocol")
	b, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil || !strings.HasPrefix(strings.ReplaceAll(string(b), "\r\n", "\n"), "---\nname: nexo-protocol") {
		return
	}
	if err := os.RemoveAll(skillDir); err == nil {
		fmt.Fprintf(nexoStderr, "[nexo] copia directa reemplazada por el plugin: %s\n", skillDir)
	}
}
