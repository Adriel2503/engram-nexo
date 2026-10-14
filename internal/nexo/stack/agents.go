package stack

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/secrets"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/tour"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/ui"
	"github.com/Gentleman-Programming/engram/v3/internal/setup"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type AgentInfo struct {
	Name, Dir string
	Installed bool
}
type agentMark struct {
	Server string
	Agents []string
}

func detectAgents() []AgentInfo {
	result := []AgentInfo{}
	commands := map[string]string{"claude-code": "claude", "gemini-cli": "gemini", "antigravity-cli": "antigravity", "vscode-copilot": "code", "kilocode": "kilo", "commandcode": "command"}
	for _, a := range setup.SupportedAgents() {
		name := commands[a.Name]
		if name == "" {
			name = a.Name
		}
		_, e := exec.LookPath(name)
		installed := e == nil
		if !installed {
			_, e = os.Stat(a.InstallDir)
			installed = e == nil
		}
		result = append(result, AgentInfo{a.Name, a.InstallDir, installed})
	}
	return result
}
func (a *App) catalog() []AgentInfo {
	if a.AgentCatalog != nil {
		return a.AgentCatalog()
	}
	return detectAgents()
}
func (a *App) agentsSeen(s state.State) bool {
	b, e := os.ReadFile(filepath.Join(s.Dir, "agentes.json"))
	if e != nil {
		return false
	}
	var m agentMark
	return json.Unmarshal(b, &m) == nil && m.Server == s.ServerURL && len(m.Agents) > 0
}

// agents configura la conexión común que utilizan las integraciones existentes de setup.
func (a *App) agents(ctx context.Context, s state.State, o Options, u *ui.UI, connected bool) error {
	if o.NoAgents {
		return nil
	}
	catalog := a.catalog()
	names, defaults := []string{}, []string{}
	for _, info := range catalog {
		names = append(names, info.Name)
		if info.Installed {
			defaults = append(defaults, info.Name)
		}
	}
	var chosen []string
	var e error
	if o.Agents != "" {
		chosen = strings.Split(o.Agents, ",")
	} else if o.Yes {
		chosen = defaults
	} else {
		chosen, e = u.Multi("¿Para qué asistentes quieres habilitarlo?", names, defaults)
		if e != nil {
			return e
		}
	}
	if len(chosen) == 0 {
		_, _ = fmt.Fprintln(a.Out, "Sin asistentes elegidos; el recorrido usará la CLI.")
		return nil
	}
	selected := []AgentInfo{}
	for _, name := range chosen {
		found := false
		for _, info := range catalog {
			if info.Name == strings.TrimSpace(name) {
				selected = append(selected, info)
				found = true
				break
			}
		}
		if !found {
			return errors.New("asistente no soportado por engram setup")
		}
	}
	if !connected {
		person := o.User
		if person == "" {
			person = "publicador"
			if s.Mode == "prod" {
				person = "superadmin"
			}
		}
		access, e := render.ReadAccess(s.Dir)
		if e != nil {
			return e
		}
		token := ""
		for _, p := range access {
			if p.Username == person {
				token = p.Token
			}
		}
		if token == "" {
			return errors.New("no hay acceso para la persona elegida")
		}
		a.values = append(a.values, token)
		connect := a.ConnectToken
		if connect == nil {
			connect = client.Connect
		}
		if _, e = connect(ctx, a.DataDir, s.ServerURL, token); e != nil {
			return errors.New("no se pudo conectar la persona al servidor; no se configuraron asistentes")
		}
		_, _ = fmt.Fprintf(a.Out, "Conexión de la persona %s guardada para %s.\n", person, s.ServerURL)
	}
	for _, info := range selected {
		before := configSnapshot(info.Dir)
		if a.Setup != nil {
			e = a.Setup(ctx, info.Name)
		} else {
			cmd := exec.CommandContext(ctx, a.Executable, "setup", info.Name)
			cmd.Env = agentEnvironment(a.DataDir)
			raw, err := cmd.CombinedOutput()
			e = err // Se conserva solo el diagnóstico redactado en memoria; nunca la salida cruda de setup.
			if e != nil {
				_ = secrets.Clean(string(raw), a.values)
			}
		}
		if e != nil {
			return errors.New("la conexión se guardó, pero falló setup; repite stack agentes para el asistente pendiente")
		}
		after := configSnapshot(info.Dir)
		_, _ = fmt.Fprintf(a.Out, "Asistente habilitado: %s. Configuración: %s\n", info.Name, info.Dir)
		for path, hash := range after {
			if old, ok := before[path]; !ok || old != hash {
				_, _ = fmt.Fprintln(a.Out, "Archivo actualizado: "+path)
			}
		}
	}
	old := agentMark{Server: s.ServerURL}
	if b, e := os.ReadFile(filepath.Join(s.Dir, "agentes.json")); e == nil {
		_ = json.Unmarshal(b, &old)
		if old.Server != s.ServerURL {
			old = agentMark{Server: s.ServerURL}
		}
	}
	for _, info := range selected {
		exists := false
		for _, name := range old.Agents {
			exists = exists || name == info.Name
		}
		if !exists {
			old.Agents = append(old.Agents, info.Name)
		}
	}
	b, e := json.MarshalIndent(old, "", "  ")
	if e != nil {
		return e
	}
	if e = secrets.Write(filepath.Join(s.Dir, "agentes.json"), b); e != nil {
		return e
	}
	if os.Getenv("ENGRAM_DATA_DIR") != "" {
		_, _ = fmt.Fprintf(a.Out, "Inicia tus asistentes con ENGRAM_DATA_DIR=%s y NEXO_HOME=%s para usar esta carpeta aislada.\n", a.DataDir, os.Getenv("NEXO_HOME"))
	}
	_, _ = fmt.Fprintln(a.Out, "Reinicia los asistentes. Para deshacer la conexión: engram nexo disconnect --keep-copy. Para quitar la integración, elimina solo la entrada MCP engram y los bloques del protocolo Engram/Nexo de los archivos indicados, o desinstala su plugin desde el asistente.")
	return nil
}
func configSnapshot(dir string) map[string][32]byte {
	out := map[string][32]byte{}
	count := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return nil
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			if strings.Count(rel, string(filepath.Separator)) > 3 {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".json" && ext != ".toml" && ext != ".md" && ext != ".yaml" && ext != ".yml" {
			return nil
		}
		if count >= 1000 {
			return fs.SkipAll
		}
		info, e := d.Info()
		if e != nil || info.Size() > 2<<20 || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		b, e := os.ReadFile(path)
		if e == nil {
			out[path] = sha256.Sum256(b)
			count++
		}
		return nil
	})
	return out
}
func agentEnvironment(dataDir string) []string {
	env := []string{}
	for _, v := range os.Environ() {
		k, _, _ := strings.Cut(v, "=")
		if k == "ENGRAM_DATA_DIR" || k == "ENGRAM_CLOUD_SERVER" || k == "ENGRAM_CLOUD_TOKEN" || k == "NEXO_TOKEN" {
			continue
		}
		env = append(env, v)
	}
	return append(env, "ENGRAM_DATA_DIR="+dataDir)
}
func (a *App) next(ctx context.Context, s state.State, o Options) error {
	if o.Yes || o.NoTour || !a.Interactive {
		return nil
	}
	u := ui.New(a.In, a.Out, o.Plain)
	if tour.Seen(s.Dir) {
		_, _ = fmt.Fprintln(a.Out, "El recorrido ya se completó; puedes repetirlo.")
	}
	choice, e := u.Choose("Siguiente paso", []string{"Recorrido guiado (recomendado)", "Solo conectar mis asistentes", "Terminar"})
	if e != nil {
		return e
	}
	switch choice {
	case "Recorrido guiado (recomendado)":
		return a.tour(ctx, s, o)
	case "Solo conectar mis asistentes":
		return a.agents(ctx, s, o, u, false)
	}
	suffix := " --mode " + s.Mode
	if s.Name != "" {
		suffix += " --name " + s.Name
	}
	_, _ = fmt.Fprintln(a.Out, "Para después: engram nexo tour"+suffix+"; engram nexo stack agentes"+suffix)
	return nil
}
