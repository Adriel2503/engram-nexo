package stack

import (
	"context"
	"fmt"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/tour"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/ui"
)

func (a *App) tour(ctx context.Context, s state.State, o Options) error {
	if o.Action != "" {
		return a.tourAction(ctx, s, o)
	}
	u := ui.New(a.In, a.Out, o.Plain)
	connected := a.agentsSeen(s)
	if !o.NoAgents && !connected {
		installed := false
		for _, info := range a.catalog() {
			installed = installed || info.Installed
		}
		if installed {
			if e := a.agents(ctx, s, o, u, false); e != nil {
				return e
			}
			connected = a.agentsSeen(s)
		} else {
			_, _ = fmt.Fprintln(a.Out, "Paso 0: no se detectaron asistentes instalados; continúa con la CLI.")
		}
	} else if o.NoAgents {
		connected = false
		_, _ = fmt.Fprintln(a.Out, "Paso 0 omitido con --no-agents.")
	}
	return tour.Run(ctx, u, a.Out, s.Dir, s.ServerURL, s.Mode, s.Name, s.ExampleLoaded, connected)
}
