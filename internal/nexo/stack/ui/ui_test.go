package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestStackMenuKeys(t *testing.T) {
	m := menu{label: "Modo", items: []string{"demo", "dev"}}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(menu)
	if m.index != 1 || !strings.Contains(m.View(), "> dev") {
		t.Fatal("selección")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !next.(menu).cancel {
		t.Fatal("cancelación")
	}
}
func TestStackHiddenField(t *testing.T) {
	f := field{hidden: true}
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("valor sintético")})
	if strings.Contains(f.View(), f.Value()) {
		t.Fatal("campo visible")
	}
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if len(f.value) != len([]rune("valor sintético"))-1 {
		t.Fatal("borrado por runa")
	}
	p := prompt{label: "Clave", input: f}
	if strings.Contains(p.View(), f.Value()) {
		t.Fatal("prompt visible")
	}
}

func TestStackFieldSpace(t *testing.T) {
	f := field{}
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Mi")})
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeySpace})
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("carpeta")})
	if f.Value() != "Mi carpeta" {
		t.Fatal("espacio omitido")
	}
}

func TestStackMultiKeys(t *testing.T) {
	m := multi{menu: menu{items: []string{"codex", "pi"}}, checked: map[int]bool{}}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(multi)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(multi)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(multi)
	if !m.checked[0] || !m.checked[1] || !strings.Contains(m.View(), "[x]") {
		t.Fatal("selección múltiple")
	}
}
