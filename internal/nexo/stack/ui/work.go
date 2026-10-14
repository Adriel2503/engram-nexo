package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

type completed struct{ err error }
type work struct {
	label   string
	spin    spinner.Model
	action  func() error
	cancel  context.CancelFunc
	err     error
	percent float64
}

func (w work) Init() tea.Cmd {
	return tea.Batch(w.spin.Tick, func() tea.Msg { return completed{w.action()} })
}
func (w work) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case completed:
		w.err = m.err
		w.percent = 1
		return w, tea.Quit
	case tea.KeyMsg:
		if m.String() == "ctrl+c" || m.String() == "esc" {
			w.cancel()
			w.err = errors.New("operación cancelada")
			return w, nil
		}
	}
	var c tea.Cmd
	w.spin, c = w.spin.Update(msg)
	return w, c
}
func (w work) View() string {
	n := int(w.percent * 20)
	return w.spin.View() + " " + w.label + " [" + strings.Repeat("=", n) + strings.Repeat(" ", 20-n) + "]\n"
}

// Work mantiene el trabajo fuera de Update y cancela la operación antes de salir.
func (u *UI) Work(ctx context.Context, label string, percent float64, fn func(context.Context) error) error {
	if u.Plain {
		_, _ = fmt.Fprintln(u.Out, "[..] "+label)
		return fn(ctx)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := spinner.New(spinner.WithSpinner(spinner.Spinner{Frames: []string{"-", "/", "|", "\\"}, FPS: 120 * time.Millisecond}))
	m, e := tea.NewProgram(work{label: label, spin: s, action: func() error { return fn(ctx) }, cancel: cancel, percent: percent}, tea.WithInput(u.In), tea.WithOutput(u.Out)).Run()
	if e != nil {
		return e
	}
	return m.(work).err
}
