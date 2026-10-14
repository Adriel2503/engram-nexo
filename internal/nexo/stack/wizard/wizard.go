// Package wizard define el menú inicial con acciones inyectables y entradas simulables.
package wizard

import (
	"context"
	"errors"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/ui"
)

type Actions struct {
	Demo    func(context.Context, *ui.UI) error
	Connect func(context.Context, *ui.UI) error
	Server  func(context.Context, *ui.UI) error
}

func Start(ctx context.Context, u *ui.UI, a Actions) error {
	choice, e := u.Choose("¿Qué quieres hacer?", []string{"Probar el demo en mi equipo", "Conectar mi asistente a un servidor Nexo", "Montar un servidor (dev o prod)"})
	if e != nil {
		return e
	}
	switch choice {
	case "Probar el demo en mi equipo":
		if a.Demo != nil {
			return a.Demo(ctx, u)
		}
	case "Conectar mi asistente a un servidor Nexo":
		if a.Connect != nil {
			return a.Connect(ctx, u)
		}
	case "Montar un servidor (dev o prod)":
		if a.Server != nil {
			return a.Server(ctx, u)
		}
	}
	return errors.New("acción no disponible")
}
