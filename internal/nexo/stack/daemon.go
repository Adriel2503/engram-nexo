package stack

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/proc"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/secrets"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
)

func (a *App) internal(ctx context.Context, cmd string, o Options) int {
	s, e := state.Load(o.Dir)
	if e != nil {
		return 1
	}
	if cmd == "_bootstrap" {
		return a.internalBootstrap(ctx, s)
	}
	if e = a.serve(s); e != nil {
		return 1
	}
	return 0
}
func (a *App) internalBootstrap(ctx context.Context, s state.State) int {
	if e := bootstrapLocal(ctx, s); e != nil {
		env, _ := render.ReadEnv(s.Dir)
		a.values = sensitive(env)
		return a.failure(e)
	}
	return 0
}

// serve es el supervisor desacoplado. Redacta cada línea antes de persistirla.
// El hijo recibe solo el entorno generado; detener pide el cierre al supervisor por su PID.
func (a *App) serve(s state.State) error {
	env, e := render.ReadEnv(s.Dir)
	if e != nil {
		return e
	}
	path := filepath.Join(s.Dir, "servidor.log")
	if _, e = os.Stat(path); errors.Is(e, os.ErrNotExist) {
		if e = secrets.Write(path, nil); e != nil {
			return e
		}
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return e
	}
	defer func() { _ = f.Close() }()
	w := secrets.NewWriter(f, sensitive(env))
	defer func() { _ = w.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	child := exec.CommandContext(ctx, a.Executable, "cloud", "serve")
	proc.HideChild(child)
	child.Env = isolated(env)
	child.Stdout = w
	child.Stderr = w
	if e = child.Start(); e != nil {
		return errors.New("no se pudo iniciar cloud serve")
	}
	id, e := proc.Inspect(child.Process.Pid)
	if e == nil {
		e = proc.Save(filepath.Join(s.Dir, "cloud.pid"), id)
	}
	if e != nil {
		cancel()
		_ = child.Wait()
		return e
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case e := <-done:
			return e
		case <-tick.C:
			b, e := os.ReadFile(filepath.Join(s.Dir, "detener"))
			if e == nil && strings.TrimSpace(string(b)) == strconv.Itoa(os.Getpid()) {
				cancel()
				<-done
				return nil
			}
		}
	}
}
