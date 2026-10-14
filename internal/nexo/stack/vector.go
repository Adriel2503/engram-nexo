package stack

import (
	"context"
	"errors"
	"fmt"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
)

// checkVector vuelve a texto si la base propia no ofrece pgvector, como hace el servidor.
func (a *App) checkVector(ctx context.Context, s *state.State, m map[string]string) error {
	if s.Embedder == "" || s.Embedder == "off" {
		return nil
	}
	p, e := render.ReadAccess(s.Dir)
	if e != nil {
		return e
	}
	var token string
	for _, v := range p {
		if v.Username == "superadmin" {
			token = v.Token
		}
	}
	c := api(*s, token)
	c.HTTP = a.HTTP
	var db struct {
		Server struct {
			VectorVersion string `json:"vector_version"`
		} `json:"server"`
	}
	if e = c.Do(ctx, "GET", "/nexo/admin/database", nil, &db); e != nil {
		return errors.New("no se pudo comprobar pgvector; revisa la salud de la base antes de cargar datos")
	}
	if db.Server.VectorVersion != "" {
		return nil
	}
	_, _ = fmt.Fprintln(a.Out, "Aviso: pgvector no está disponible; se vuelve a búsqueda solo por texto.")
	s.Embedder = "off"
	m["NEXO_KB_EMBEDDER"] = "off"
	if e = render.WriteEnv(s.Dir, m); e != nil {
		return e
	}
	if e = state.Save(*s); e != nil {
		return e
	}
	if e = a.down(ctx, *s, false); e != nil {
		return e
	}
	return a.upUnlocked(ctx, Options{Mode: s.Mode, Name: s.Name, Dir: s.Dir, Bind: s.Bind, Yes: true, NoData: true, Up: s.Mode == "prod", Acknowledge: true})
}
