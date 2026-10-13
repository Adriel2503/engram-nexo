package setup

// Nexo IA · NX-10: el plugin del fork se llama `engram`, igual que el de
// Engram, y Claude Code y Codex identifican el catálogo (marketplace) por ese
// nombre. Si la PC ya tenía el catálogo `engram` apuntando al Engram oficial
// (Gentleman-Programming/engram), `marketplace add` del fork responde
// "already" y el plugin seguiría saliendo del oficial, sin la skill
// nexo-protocol. Antes de agregar el catálogo, estas funciones detectan un
// catálogo `engram` con otro origen y lo quitan para que el paso siguiente lo
// agregue desde el fork. Se llaman desde enganches de una línea marcados
// `// NX-10` en installClaudeCode e installCodex (setup.go).
//
// La detección lee los archivos de configuración de cada agente (no ejecuta
// comandos): sin catálogo, sin archivo o con un formato desconocido no hace
// nada. Si quitar el catálogo falla, avisa por stderr con los comandos para
// hacerlo a mano y sigue.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// nexoMarketplaceName es el nombre del catálogo (el `name` de
// .claude-plugin/marketplace.json y .agents/plugins/marketplace.json).
const nexoMarketplaceName = "engram"

// nexoForkSourceMarker identifica el origen del fork en un repositorio de
// GitHub, una URL de git o una carpeta local con un clon.
const nexoForkSourceMarker = "engram-nexo"

func nexoIsForkSource(source string) bool {
	return strings.Contains(strings.ToLower(source), nexoForkSourceMarker)
}

// nexoClaudeMarketplaceSource devuelve el origen del catálogo `engram`
// registrado en Claude Code (plugins/known_marketplaces.json), o "" si no hay.
func nexoClaudeMarketplaceSource() string {
	home, _ := userHomeDir()
	data, err := readFileFn(filepath.Join(claudeCodeConfigRoot(home), "plugins", "known_marketplaces.json"))
	if err != nil {
		return ""
	}
	var known map[string]struct {
		Source struct {
			Repo string `json:"repo"`
			URL  string `json:"url"`
			Path string `json:"path"`
		} `json:"source"`
	}
	if json.Unmarshal(data, &known) != nil {
		return ""
	}
	entry, ok := known[nexoMarketplaceName]
	if !ok {
		return ""
	}
	for _, s := range []string{entry.Source.Repo, entry.Source.URL, entry.Source.Path} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// nexoReplaceForeignClaudeMarketplace quita el catálogo `engram` de Claude
// Code si apunta a otro origen (por ejemplo el Engram oficial).
func nexoReplaceForeignClaudeMarketplace(claudeBin string) {
	source := nexoClaudeMarketplaceSource()
	if source == "" || nexoIsForkSource(source) {
		return
	}
	fmt.Fprintf(nexoStderr, "[nexo] el catálogo de plugins %q de Claude Code apunta a %s; se reemplaza por %s (plugin con la skill nexo-protocol)\n", nexoMarketplaceName, source, claudeCodeMarketplace)
	if out, err := runCommand(claudeBin, "plugin", "marketplace", "remove", nexoMarketplaceName); err != nil {
		fmt.Fprintf(nexoStderr, "[nexo] no se pudo quitar el catálogo %q (%s). Hágalo a mano y repita la instalación:\n  claude plugin marketplace remove %s\n  claude plugin marketplace add %s\n  engram setup claude-code\n", nexoMarketplaceName, strings.TrimSpace(string(out)), nexoMarketplaceName, claudeCodeMarketplace)
	}
}

// nexoCodexMarketplace devuelve `source` y `ref` de la tabla
// [marketplaces.engram] del config.toml de Codex ("" si no existen).
func nexoCodexMarketplace(configPath string) (source, ref string) {
	if configPath == "" {
		return "", ""
	}
	data, err := readFileFn(configPath)
	if err != nil {
		return "", ""
	}
	inTable := false
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			header := strings.TrimSpace(strings.Trim(trimmed, "[]"))
			inTable = header == "marketplaces."+nexoMarketplaceName || header == `marketplaces."`+nexoMarketplaceName+`"`
			continue
		}
		if !inTable {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "source":
			source = value
		case "ref":
			ref = value
		}
	}
	return source, ref
}

// nexoReplaceForeignCodexMarketplace quita el catálogo `engram` de Codex si
// apunta a otro origen o a otra rama que la estable del fork.
func nexoReplaceForeignCodexMarketplace(codexBin, configPath string) {
	source, ref := nexoCodexMarketplace(configPath)
	if source == "" {
		return
	}
	if nexoIsForkSource(source) && (ref == "" || ref == codexMarketplaceRef) {
		return
	}
	fmt.Fprintf(nexoStderr, "[nexo] el catálogo de plugins %q de Codex apunta a %s (rama %q); se reemplaza por %s --ref %s (plugin con la skill nexo-protocol)\n", nexoMarketplaceName, source, ref, codexMarketplace, codexMarketplaceRef)
	if out, err := runCommand(codexBin, "plugin", "marketplace", "remove", nexoMarketplaceName); err != nil {
		fmt.Fprintf(nexoStderr, "[nexo] no se pudo quitar el catálogo %q (%s). Hágalo a mano y repita la instalación:\n  codex plugin marketplace remove %s\n  codex plugin marketplace add %s --ref %s\n  engram setup codex\n", nexoMarketplaceName, strings.TrimSpace(string(out)), nexoMarketplaceName, codexMarketplace, codexMarketplaceRef)
	}
}
