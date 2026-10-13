// Package nexosetup instala el protocolo Nexo (skill nexo-protocol v0.3) en
// los asistentes que configura `engram setup` (NX-10, tanda 2, F-04 dentro
// del fork). La fuente única del texto es SKILL.md de este paquete
// (embebido); las copias del plugin (plugin/claude-code/skills/nexo-protocol,
// plugin/codex/skills/nexo-protocol) se generan con `go generate` y una
// prueba verifica que no diverjan.
//
// Decisión: el protocolo se instala solo si la PC está conectada a Nexo
// (`engram nexo connect`, marcador nexo.json en la carpeta de datos de
// Engram). Sin conexión, `engram setup` queda exactamente igual que en
// Engram. `engram nexo setup <agente>` lo instala aparte (por ejemplo, si
// `engram setup` se corrió antes de conectar).
package nexosetup

//go:generate sh -c "mkdir -p ../../../plugin/claude-code/skills/nexo-protocol ../../../plugin/codex/skills/nexo-protocol && cp SKILL.md ../../../plugin/claude-code/skills/nexo-protocol/SKILL.md && cp SKILL.md ../../../plugin/codex/skills/nexo-protocol/SKILL.md"

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed SKILL.md
var skillMD string

// SkillName es la carpeta y el nombre de la skill.
const SkillName = "nexo-protocol"

// Marcadores del bloque administrado en archivos de reglas compartidos.
const (
	BlockBegin = "<!-- BEGIN NEXO PROTOCOL (NX-10) — managed by engram setup -->"
	BlockEnd   = "<!-- END NEXO PROTOCOL -->"
)

// Skill devuelve SKILL.md completo (con front matter).
func Skill() string { return skillMD }

var frontMatterRe = regexp.MustCompile(`(?s)\A---\r?\n.*?\r?\n---\r?\n`)

// Body devuelve el protocolo sin front matter, para archivos de reglas.
func Body() string {
	return strings.TrimSpace(frontMatterRe.ReplaceAllString(skillMD, ""))
}

// Block devuelve el bloque delimitado para archivos de reglas compartidos.
func Block() string {
	return BlockBegin + "\n" + Body() + "\n" + BlockEnd
}

// WriteSkill escribe <dir>/nexo-protocol/SKILL.md y devuelve la ruta.
func WriteSkill(skillsDir string) (string, error) {
	dest := filepath.Join(skillsDir, SkillName, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	return dest, os.WriteFile(dest, []byte(skillMD), 0o644)
}

// WriteFile escribe un archivo propio de Nexo (prefijo opcional, por ejemplo
// el front matter de VS Code) con el protocolo.
func WriteFile(path, prefix string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(prefix+Body()+"\n"), 0o644)
}

// UpsertBlock agrega o reemplaza el bloque de Nexo en un archivo de reglas
// compartido, sin tocar el resto (ni el bloque de Engram).
func UpsertBlock(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(raw)
	block := Block()
	if i := strings.Index(content, BlockBegin); i >= 0 {
		j := strings.Index(content[i:], BlockEnd)
		if j < 0 {
			return fmt.Errorf("%s: bloque Nexo sin cierre (%s); corríjalo a mano", path, BlockEnd)
		}
		content = content[:i] + block + content[i+j+len(BlockEnd):]
	} else {
		if strings.TrimSpace(content) != "" {
			content = strings.TrimRight(content, "\r\n") + "\n\n"
		}
		content += block + "\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
