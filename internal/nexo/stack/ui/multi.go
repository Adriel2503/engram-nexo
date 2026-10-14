package ui

import (
	"errors"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"strconv"
	"strings"
)

func (u *UI) Multi(label string, items, defaults []string) ([]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	checked := map[int]bool{}
	for i, s := range items {
		for _, d := range defaults {
			if s == d {
				checked[i] = true
			}
		}
	}
	if u.Plain {
		_, _ = fmt.Fprintln(u.Out, label)
		def := []string{}
		for i, s := range items {
			mark := " "
			if checked[i] {
				mark = "x"
				def = append(def, strconv.Itoa(i+1))
			}
			_, _ = fmt.Fprintf(u.Out, "%d. [%s] %s\n", i+1, mark, s)
		}
		input, e := u.Ask("Números o nombres separados por comas; ninguno para omitir", strings.Join(def, ","), false)
		if e != nil {
			return nil, e
		}
		if input == "ninguno" || input == "" {
			return nil, nil
		}
		result := []string{}
		seen := map[int]bool{}
		for _, part := range strings.Split(input, ",") {
			part = strings.TrimSpace(part)
			found := -1
			for i, s := range items {
				if s == part || strconv.Itoa(i+1) == part {
					found = i
					break
				}
			}
			if found < 0 {
				return nil, errors.New("asistente desconocido")
			}
			if !seen[found] {
				seen[found] = true
				result = append(result, items[found])
			}
		}
		return result, nil
	}
	m, e := tea.NewProgram(multi{menu: menu{label: label, items: items}, checked: checked}, tea.WithInput(u.In), tea.WithOutput(u.Out)).Run()
	if e != nil {
		return nil, e
	}
	result := m.(multi)
	if result.cancel {
		return nil, errors.New("operación cancelada")
	}
	out := []string{}
	for i, s := range items {
		if result.checked[i] {
			out = append(out, s)
		}
	}
	return out, nil
}

type multi struct {
	menu
	checked map[int]bool
}

func (m multi) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		if k.Type == tea.KeySpace {
			m.checked[m.index] = !m.checked[m.index]
			return m, nil
		}
	}
	next, c := m.menu.Update(msg)
	m.menu = next.(menu)
	return m, c
}
func (m multi) View() string {
	var b strings.Builder
	b.WriteString(m.label + "\n")
	for i, s := range m.items {
		cursor := "  "
		if i == m.index {
			cursor = "> "
		}
		check := " "
		if m.checked[i] {
			check = "x"
		}
		b.WriteString(cursor + "[" + check + "] " + s + "\n")
	}
	b.WriteString("Espacio: marcar. Enter: continuar. Esc: cancelar.\n")
	return b.String()
}
