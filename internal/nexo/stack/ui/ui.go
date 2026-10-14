// Package ui ofrece controles Bubble Tea y una alternativa de texto plano.
package ui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
)

type UI struct {
	In     io.Reader
	Out    io.Writer
	Plain  bool
	reader *bufio.Reader
}

func Interactive(in io.Reader) bool { f, ok := in.(*os.File); return ok && term.IsTerminal(f.Fd()) }
func New(in io.Reader, out io.Writer, plain bool) *UI {
	outputTTY := false
	if f, ok := out.(*os.File); ok {
		outputTTY = term.IsTerminal(f.Fd())
	}
	return &UI{In: in, Out: out, Plain: !outputTTY || plain || os.Getenv("NO_COLOR") != "" || !Interactive(in), reader: bufio.NewReader(in)}
}
func (u *UI) Ask(label, def string, hidden bool) (string, error) {
	if u.Plain {
		_, _ = fmt.Fprint(u.Out, label)
		if def != "" && !hidden {
			_, _ = fmt.Fprint(u.Out, " ["+def+"]")
		}
		_, _ = fmt.Fprint(u.Out, ": ")
		var s string
		var e error
		if hidden && Interactive(u.In) {
			f := u.In.(*os.File)
			b, err := term.ReadPassword(f.Fd())
			e = err
			s = string(b)
			_, _ = fmt.Fprintln(u.Out)
		} else {
			s, e = u.reader.ReadString('\n')
		}
		if e != nil && len(s) == 0 {
			return "", e
		}
		s = strings.TrimSpace(s)
		if s == "" {
			s = def
		}
		return s, nil
	}
	input := field{placeholder: def, hidden: hidden}
	m, e := tea.NewProgram(prompt{label: label, input: input}, tea.WithInput(u.In), tea.WithOutput(u.Out)).Run()
	if e != nil {
		return "", e
	}
	p := m.(prompt)
	if p.cancel {
		return "", errors.New("operación cancelada")
	}
	s := strings.TrimSpace(p.input.Value())
	if s == "" {
		s = def
	}
	return s, nil
}
func (u *UI) Choose(label string, items []string) (string, error) {
	if len(items) == 0 {
		return "", errors.New("no hay opciones")
	}
	if u.Plain {
		_, _ = fmt.Fprintln(u.Out, label)
		for i, v := range items {
			_, _ = fmt.Fprintf(u.Out, "%d. %s\n", i+1, v)
		}
		s, e := u.Ask("Opción", "1", false)
		if e != nil {
			return "", e
		}
		for i, v := range items {
			if s == fmt.Sprint(i+1) || s == v {
				return v, nil
			}
		}
		return "", errors.New("opción inválida")
	}
	m, e := tea.NewProgram(menu{label: label, items: items}, tea.WithInput(u.In), tea.WithOutput(u.Out)).Run()
	if e != nil {
		return "", e
	}
	p := m.(menu)
	if p.cancel {
		return "", errors.New("operación cancelada")
	}
	return p.items[p.index], nil
}
func (u *UI) Confirm(label string, def bool) (bool, error) {
	d := "no"
	if def {
		d = "sí"
	}
	s, e := u.Ask(label+" (sí/no)", d, false)
	if e != nil {
		return false, e
	}
	switch strings.ToLower(s) {
	case "sí", "si", "s", "yes":
		return true, nil
	case "no", "n":
		return false, nil
	}
	return false, errors.New("responde sí o no")
}

type prompt struct {
	label  string
	input  field
	cancel bool
}

func (p prompt) Init() tea.Cmd { return nil }
func (p prompt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "enter":
			return p, tea.Quit
		case "esc", "ctrl+c":
			p.cancel = true
			return p, tea.Quit
		}
	}
	var c tea.Cmd
	p.input, c = p.input.Update(msg)
	return p, c
}
func (p prompt) View() string {
	return lipgloss.NewStyle().Bold(true).Render(p.label) + "\n" + p.input.View() + "\nEnter: continuar. Esc: cancelar.\n"
}

type menu struct {
	label  string
	items  []string
	index  int
	cancel bool
}

func (m menu) Init() tea.Cmd { return nil }
func (m menu) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "up", "k":
			if m.index > 0 {
				m.index--
			}
		case "down", "j":
			if m.index < len(m.items)-1 {
				m.index++
			}
		case "enter":
			return m, tea.Quit
		case "esc", "ctrl+c":
			m.cancel = true
			return m, tea.Quit
		}
	}
	return m, nil
}
func (m menu) View() string {
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(m.label) + "\n")
	for i, v := range m.items {
		mark := "  "
		if i == m.index {
			mark = "> "
		}
		b.WriteString(mark + v + "\n")
	}
	b.WriteString("Enter: elegir. Esc: cancelar.\n")
	return b.String()
}

// Copy usa una tubería al portapapeles nativo: OSC52 codificaría el token en la salida del terminal.
// clipboard.go tiene funciones privadas; esta copia evita modificar Engram y evita esa filtración.
func Copy(s string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		c = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "[Console]::In.ReadToEnd() | Set-Clipboard")
	case "darwin":
		c = exec.Command("pbcopy")
	default:
		if _, e := exec.LookPath("wl-copy"); e == nil {
			c = exec.Command("wl-copy")
		} else {
			c = exec.Command("xclip", "-selection", "clipboard")
		}
	}
	c.Stdin = strings.NewReader(s)
	if c.Run() != nil {
		return errors.New("no se pudo copiar: abre el archivo de accesos")
	}
	return nil
}
func Open(path string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	case "darwin":
		c = exec.Command("open", path)
	default:
		c = exec.Command("xdg-open", path)
	}
	if c.Run() != nil {
		return errors.New("no se pudo abrir el archivo o navegador")
	}
	return nil
}

// field edita por runa y nunca dibuja el contenido oculto.
type field struct {
	value       []rune
	placeholder string
	hidden      bool
}

func (f field) Value() string { return string(f.value) }
func (f field) View() string {
	if f.hidden {
		return strings.Repeat("*", len(f.value))
	}
	if len(f.value) == 0 {
		return f.placeholder
	}
	return string(f.value)
}
func (f field) Update(msg tea.Msg) (field, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.Type {
		case tea.KeyBackspace, tea.KeyDelete:
			if len(f.value) > 0 {
				f.value = f.value[:len(f.value)-1]
			}
		case tea.KeySpace:
			if len(f.value) < 4096 {
				f.value = append(f.value, ' ')
			}
		case tea.KeyRunes:
			if len(f.value)+len(k.Runes) <= 4096 {
				f.value = append(f.value, k.Runes...)
			}
		}
	}
	return f, nil
}
