package stack

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/ui"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/wizard"
)

func (a *App) start(ctx context.Context, args []string) int {
	o, e := parse(args)
	if e != nil {
		return a.failure(e)
	}
	u := ui.New(a.In, a.Out, o.Plain)
	deploy := func(ctx context.Context, u *ui.UI, mode string) error {
		o.Mode = mode
		if mode != "prod" {
			choice, e := u.Choose("¿Con qué quieres empezar?", []string{"Ejemplo", "Mis archivos", "Vacío", "Ejemplo y mis archivos"})
			if e != nil {
				return e
			}
			o.NoData = choice == "Vacío" || choice == "Mis archivos"
			if choice == "Mis archivos" || choice == "Ejemplo y mis archivos" {
				o.OwnDir, e = u.Ask("Carpeta de documentos", "", false)
				if e != nil {
					return e
				}
				o.PerSubfolder, e = u.Confirm("¿Crear un espacio por subcarpeta?", true)
				if e != nil {
					return e
				}
			}
		} else {
			o.NoData = true
		}
		if mode != "demo" {
			port, e := u.Ask("Puerto del servidor", map[string]string{"dev": "18481", "prod": "18443"}[mode], false)
			if e != nil {
				return e
			}
			o.Port, e = strconv.Atoi(port)
			if e != nil {
				return errors.New("puerto inválido")
			}
		}
		if e = a.askSearch(u, &o.Search); e != nil {
			return e
		}
		originalYes := o.Yes
		o.Yes = true
		o.Dir, e = state.Directory(a.Home, o.Mode, o.Name, o.Dir)
		if e != nil {
			return e
		}
		if e = a.up(ctx, o); e != nil {
			return e
		}

		current, err := state.Load(o.Dir)
		if err != nil {
			return err
		}
		o.Yes = originalYes
		return a.next(ctx, current, o)
	}
	e = wizard.Start(ctx, u, wizard.Actions{Demo: func(c context.Context, u *ui.UI) error { return deploy(c, u, "demo") }, Connect: func(c context.Context, u *ui.UI) error { return a.connectWizard(c, u, "", o) }, Server: func(c context.Context, u *ui.UI) error {
		mode, e := u.Choose("Modo del servidor", []string{"dev", "prod"})
		if e != nil {
			return e
		}
		return deploy(c, u, mode)
	}})
	if e != nil {
		return a.failure(e)
	}
	return 0
}
func (a *App) connectWizard(ctx context.Context, u *ui.UI, server string, opts ...Options) error {
	srv, e := u.Ask("URL del servidor", server, false)
	if e != nil {
		return e
	}
	code, e := u.Ask("Código de activación (entrada oculta; un canje pendiente se reanuda)", "", true)
	if e != nil {
		return e
	}
	result, e := a.Connect(ctx, a.DataDir, srv, code)
	if e != nil {
		return errors.New("no se completó la conexión; un token ya canjeado queda guardado para reanudar con nexo connect --resume")
	}
	_, _ = fmt.Fprintf(a.Out, "Conectado como %s.\n", result.Label)

	options := Options{}
	if len(opts) > 0 {
		options = opts[0]
	}
	return a.agents(ctx, state.State{ServerURL: srv, Dir: a.DataDir}, options, u, true)
}
