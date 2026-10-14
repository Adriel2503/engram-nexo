// Package stack coordina entornos Nexo aislados; las reglas de acceso siguen en el servidor.
package stack

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/preflight"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/proc"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/secrets"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/stackapi"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/ui"
)

const Usage = "Uso: engram nexo start | tour | stack <up|down|status|reset|logs|accesos|datos|config|agentes> [--mode demo|dev|prod] [--name N] [--plain]\nup: --port P --pg-port P --dir D --database-url URL --no-data --yes --bind IP --i-understand-this-is-a-demo\nbúsqueda: --embedder P --embed-model M --embed-dims N --embed-url URL --embed-key-file F --rerank P --rerank-model M --rerank-key-file F --r1 all|off|lista\nprod: --image I --build-from D --rebuild --up\nasistentes: --no-tour --no-agents --agents codex,pi --user publicador\naccesos: --abrir | --copiar usuario (la terminal nunca muestra tokens)\n"

type Options struct {
	Mode, Name, Dir, Bind, DatabaseURL, Image, BuildFrom, OwnDir                    string
	Port, PGPort                                                                    int
	Yes, Plain, NoData, Up, Rebuild, Acknowledge, Open, RemoveExample, PerSubfolder bool
	Show, Copy                                                                      string
	Search                                                                          SearchOptions
	NoTour, NoAgents                                                                bool
	Agents, User                                                                    string
	Action, ProposalID                                                              string
}
type App struct {
	In                                 io.Reader
	Out, Err                           io.Writer
	Home, Version, Executable, DataDir string
	Runner                             preflight.Runner
	HTTP                               *http.Client
	Seeder                             stackapi.Seeder
	Interactive                        bool
	Bootstrap                          func(context.Context, state.State, map[string]string) error
	Launch                             func(string, []string, []string, string) (proc.Identity, error)
	Alive                              func(proc.Identity) bool
	Stop                               func(proc.Identity) error
	Activate                           func(context.Context, string, string) (string, error)
	Connect                            func(context.Context, string, string, string) (client.ConnectResult, error)
	Setup                              func(context.Context, string) error
	values                             []string
	caDir                              string
	AgentCatalog                       func() []AgentInfo
	ConnectToken                       func(context.Context, string, string, string) (client.ConnectResult, error)
}

func New(in io.Reader, out, err io.Writer, dataDir, version string) *App {
	bin, _ := os.Executable()
	return &App{In: in, Out: out, Err: err, Home: os.Getenv("NEXO_STACK_HOME"), Version: version, Executable: bin, DataDir: dataDir, Runner: preflight.Exec, HTTP: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Seeder: NewSeeder(), Interactive: ui.Interactive(in), Launch: proc.Start, Alive: proc.Alive, Stop: proc.Stop, Activate: client.Activate, Connect: client.ConnectActivated}
}

// Run es el único punto de entrada del despachador Nexo.
func Run(args []string, dataDir, version string, in io.Reader, out, err io.Writer) int {
	if version == "auto" {
		version = "dev"
		if bin, e := os.Executable(); e == nil && !strings.Contains(filepath.Base(bin), ".test") {
			if b, e := exec.Command(bin, "version").Output(); e == nil && strings.HasPrefix(string(b), "engram ") {
				version = strings.TrimSpace(strings.TrimPrefix(string(b), "engram "))
			}
		}
	}
	return New(in, out, err, dataDir, version).Run(args)
}
func (a *App) Run(args []string) int {
	if len(args) == 0 {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if args[0] == "start" {
		return a.start(ctx, args[1:])
	}
	cmd := args[0]
	rest := args[1:]
	if cmd == "stack" {
		if len(rest) == 0 {
			_, _ = fmt.Fprint(a.Out, Usage)
			return 2
		}
		cmd, rest = rest[0], rest[1:]
	}
	if cmd == "help" || cmd == "--help" || cmd == "-h" {
		_, _ = fmt.Fprint(a.Out, Usage)
		return 0
	}
	o, e := parse(rest)
	if e != nil {
		_, _ = fmt.Fprintln(a.Err, e)
		return 2
	}
	if cmd == "_serve" || cmd == "_bootstrap" {
		return a.internal(ctx, cmd, o)
	}
	if o.Mode == "" {
		if !a.Interactive {
			_, _ = fmt.Fprintln(a.Err, "sin terminal debes indicar --mode demo, dev o prod")
			return 2
		}
		o.Mode, e = ui.New(a.In, a.Out, o.Plain).Choose("Elige el modo", []string{"demo", "dev", "prod"})
		if e != nil {
			return a.failure(e)
		}
	}
	dir, e := state.Directory(a.Home, o.Mode, o.Name, o.Dir)
	if e != nil {
		return a.failure(e)
	}
	o.Dir = dir
	if cmd == "up" {
		e = a.up(ctx, o)
	} else {
		s, le := state.Load(dir)
		if le != nil {
			return a.failure(errors.New("no hay un entorno válido aquí; ejecuta stack up con el mismo modo y nombre"))
		}
		switch cmd {
		case "status":
			e = a.status(ctx, s)
		case "logs":
			e = a.logs(ctx, s)
		case "accesos":
			e = a.access(s, o)
		case "agentes":
			e = a.agents(ctx, s, o, ui.New(a.In, a.Out, o.Plain), false)
		case "tour":
			e = a.tour(ctx, s, o)
		case "down", "reset", "datos", "config":
			unlock, le := state.Lock(dir)
			if le != nil {
				return a.failure(le)
			}
			defer unlock()
			switch cmd {
			case "down":
				e = a.down(ctx, s, false)
			case "reset":
				e = a.reset(ctx, s, o)
			case "datos":
				e = a.data(ctx, s, o)
			case "config":
				e = a.config(ctx, s, o)
			}
		default:
			e = errors.New("comando desconocido; usa engram nexo stack help")
		}
	}
	if e != nil {
		return a.failure(e)
	}
	return 0
}
func (a *App) failure(e error) int {
	_, _ = fmt.Fprintln(a.Err, secrets.Clean(e.Error(), a.values))
	return 1
}
func parse(args []string) (Options, error) {
	var o Options
	f := flag.NewFlagSet("stack", flag.ContinueOnError)
	f.StringVar(&o.Action, "action", "", "acción CLI del recorrido")
	f.StringVar(&o.ProposalID, "proposal-id", "", "propuesta a aprobar")
	f.BoolVar(&o.NoTour, "no-tour", false, "omitir siguiente paso")
	f.BoolVar(&o.NoAgents, "no-agents", false, "omitir asistentes")
	f.StringVar(&o.Agents, "agents", "", "asistentes separados por comas")
	f.StringVar(&o.User, "user", "", "persona para asistentes")
	f.SetOutput(io.Discard)
	f.BoolVar(&o.Search.Example, "example", false, "ejemplo explícito")
	f.StringVar(&o.Mode, "mode", "", "modo")
	f.StringVar(&o.Name, "name", "", "nombre")
	f.StringVar(&o.Dir, "dir", "", "carpeta de estado")
	f.StringVar(&o.Bind, "bind", "127.0.0.1", "bind")
	f.IntVar(&o.Port, "port", 0, "puerto")
	f.IntVar(&o.PGPort, "pg-port", 0, "puerto Postgres")
	f.StringVar(&o.DatabaseURL, "database-url", "", "Postgres existente")
	f.BoolVar(&o.Yes, "yes", false, "sin preguntas")
	f.BoolVar(&o.Plain, "plain", false, "texto plano")
	f.BoolVar(&o.NoData, "no-data", false, "sin ejemplos")
	f.BoolVar(&o.Up, "up", false, "arrancar prod")
	f.BoolVar(&o.Rebuild, "rebuild", false, "reconstruir imagen")
	f.BoolVar(&o.Acknowledge, "i-understand-this-is-a-demo", false, "exponer demo")
	f.StringVar(&o.Image, "image", "", "imagen")
	f.StringVar(&o.BuildFrom, "build-from", "", "fuente")
	f.BoolVar(&o.Open, "abrir", false, "abrir accesos")
	f.StringVar(&o.Show, "show", "", "persona")
	f.StringVar(&o.Copy, "copiar", "", "copiar token")
	f.StringVar(&o.OwnDir, "own-dir", "", "contenido propio")
	f.BoolVar(&o.PerSubfolder, "per-subfolder", false, "espacio por subcarpeta")
	f.BoolVar(&o.RemoveExample, "quitar-ejemplo", false, "retirar ejemplo")
	searchFlags(f, &o.Search)
	if f.Parse(args) != nil || f.NArg() != 0 {
		return o, errors.New("opciones inválidas; consulta engram nexo stack help (las claves se leen de archivo o entorno)")
	}
	return o, nil
}
func (a *App) compose(ctx context.Context, s state.State, args ...string) ([]byte, error) {
	if s.Project != state.Project(s.Mode, s.Name) || !strings.HasPrefix(s.Project, "nexo-stack-") {
		return nil, errors.New("proyecto Docker ajeno rechazado")
	}
	prefix := []string{"compose", "--project-name", s.Project, "--env-file", filepath.Join(s.Dir, ".env"), "-f", filepath.Join(s.Dir, "compose.yaml")}
	return a.Runner(ctx, "docker", append(prefix, args...)...)
}
func (a *App) healthy(ctx context.Context, s state.State) bool {
	if s.Mode != "prod" {
		id, e := proc.Read(filepath.Join(s.Dir, "servidor.pid"))
		if e != nil || !a.Alive(id) {
			return false
		}
	} else {
		if e := a.trustCA(s); e != nil {
			return false
		}
	}
	for _, path := range []string{"/health", "/nexo/health/ready"} {
		req, e := http.NewRequestWithContext(ctx, "GET", s.ServerURL+path, nil)
		if e != nil {
			return false
		}
		r, e := a.HTTP.Do(req)
		if e != nil {
			return false
		}
		var status struct {
			Service string
			Status  string
		}
		e = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&status)
		_ = r.Body.Close()
		if e != nil || r.StatusCode != 200 {
			return false
		}
		if path == "/health" && (status.Service != "engram-cloud" || status.Status != "ok") || path != "/health" && status.Status != "ready" {
			return false
		}
	}
	return true
}

func (a *App) wait(ctx context.Context, check func() bool, label string) error {
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		if check() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%s no está listo: revisa stack logs y vuelve a ejecutar up", label)
		case <-tick.C:
		}
	}
}
func (a *App) up(ctx context.Context, o Options) error {
	if e := preflight.Disk(o.Dir); e != nil {
		return e
	}
	unlock, e := state.Lock(o.Dir)
	if e != nil {
		return e
	}
	defer unlock()
	if e = a.upUnlocked(ctx, o); e != nil {
		return e
	}
	s, e := state.Load(o.Dir)
	if e != nil {
		return e
	}
	return a.next(ctx, s, o)
}
func (a *App) upUnlocked(ctx context.Context, o Options) error {
	if e := preflight.CheckBind(o.Bind, o.Acknowledge); e != nil {
		return e
	}
	if e := preflight.Disk(o.Dir); e != nil {
		return e
	}
	s, e := state.Load(o.Dir)
	fresh := errors.Is(e, os.ErrNotExist)
	if e != nil && !fresh {
		return e
	}
	if !fresh && a.healthy(ctx, s) && s.UsersReady && !o.Rebuild && o.Image == "" && o.BuildFrom == "" && o.OwnDir == "" && !o.Search.Example && (s.Mode == "prod" || o.NoData || s.ExampleLoaded) {
		_, _ = fmt.Fprintln(a.Out, "[ok] Ya está en marcha.")
		return a.summary(s)
	}
	if !fresh && (s.Mode != o.Mode || s.Name != o.Name) {
		return errors.New("la carpeta pertenece a otro modo o nombre")
	}
	if fresh {
		entries, err := os.ReadDir(o.Dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() != ".operacion" {
				return errors.New("la carpeta no está vacía y no tiene estado propio; usa otro --dir")
			}
		}
		s = state.State{Mode: o.Mode, Name: o.Name, Project: state.Project(o.Mode, o.Name), Dir: o.Dir, Bind: o.Bind, Version: a.Version, ExternalDB: o.DatabaseURL != "" || os.Getenv("NEXO_STACK_DATABASE_URL") != ""}
		p, pg := 18480, 55480
		if o.Mode == "dev" {
			p, pg = 18481, 55481
		}
		if o.Mode == "prod" {
			p, pg = 18443, 0
		}
		if o.Port != 0 {
			p = o.Port
		}
		if o.PGPort != 0 {
			pg = o.PGPort
		}
		s.Port, e = preflight.Port(o.Bind, p)
		if e != nil {
			return e
		}
		if !s.ExternalDB && o.Mode != "prod" {
			s.PGPort, e = preflight.Port("127.0.0.1", pg)
			if e != nil {
				return e
			}
		}
		s.ServerURL = "http://" + net.JoinHostPort(s.Bind, fmt.Sprint(s.Port))
		if o.Mode == "prod" {
			s.ServerURL = fmt.Sprintf("https://localhost:%d", s.Port)
		}
	}
	if !s.ExternalDB || s.Mode == "prod" {
		if e = preflight.Docker(ctx, a.Runner); e != nil {
			return e
		}
	}
	_, _ = fmt.Fprintf(a.Out, "Nexo IA — entorno %s\n", s.Mode)
	if s.Mode != "prod" {
		_, _ = fmt.Fprintln(a.Out, "Solo pruebas. No uses datos reales.")
	}
	env, e := render.ReadEnv(s.Dir)
	if fresh {
		if a.Interactive && !o.Yes {
			if e = a.askSearch(ui.New(a.In, a.Out, o.Plain), &o.Search); e != nil {
				return e
			}
		}
		env, e = a.environment(ctx, &s, o)
	}
	if e != nil {
		return e
	}
	a.values = append(a.values, sensitive(env)...)
	if e = render.WriteEnv(s.Dir, env); e != nil {
		return e
	}
	if e = state.Save(s); e != nil {
		return e
	}
	if e = render.Files(render.Data{State: s, Env: env, TwoAccounts: s.Mode != "demo"}); e != nil {
		return e
	}
	if s.Mode == "prod" {
		if o.OwnDir != "" || !o.NoData && o.Search.Example {
			return errors.New("prod rechaza datos de ejemplo")
		}
		if e = a.production(ctx, &s, o, env); e != nil {
			return e
		}
	}
	if e = state.Save(s); e != nil {
		return e
	}
	if !s.ExternalDB || s.Mode == "prod" {
		if _, e = a.compose(ctx, s, "config", "--quiet"); e != nil {
			return errors.New("compose inválido; revisa los archivos generados sin imprimir .env")
		}
	}
	if s.Mode == "prod" && !o.Up {
		s.Prepared = true
		if e = state.Save(s); e != nil {
			return e
		}
		_, _ = fmt.Fprintln(a.Out, "[ok] Archivos prod generados y validados. Usa --up para arrancar.")
		return a.summary(s)
	}
	if !s.ExternalDB {
		if e = ui.New(a.In, a.Out, o.Plain).Work(ctx, "Levantando Postgres 18", 0.3, func(c context.Context) error {
			_, err := a.compose(c, s, "up", "-d", "--wait", "--wait-timeout", "90", "postgres")
			return err
		}); e != nil {
			return errors.New("no se pudo arrancar Postgres; revisa Docker y los puertos publicados")
		}
	}
	if s.Mode == "prod" {
		if _, e = a.compose(ctx, s, "up", "-d"); e != nil {
			return errors.New("no se pudo arrancar prod")
		}
		if e = a.productionHealth(ctx, s); e != nil {
			return e
		}
	} else {
		if id, pe := proc.Read(filepath.Join(s.Dir, "servidor.pid")); pe == nil && a.Alive(id) {
			if !a.healthy(ctx, s) {
				return errors.New("el servidor está vivo pero no responde; revisa logs o ejecuta down")
			}
		} else {
			if child, ce := proc.Read(filepath.Join(s.Dir, "cloud.pid")); ce == nil && a.Alive(child) {
				return errors.New("hay un proceso cloud registrado todavía vivo; ejecuta down antes de up")
			}
			_ = os.Remove(filepath.Join(s.Dir, "detener"))
			_, e = a.Launch(a.Executable, []string{"nexo", "stack", "_serve", "--dir", s.Dir}, isolated(env), filepath.Join(s.Dir, "servidor.pid"))
			if e != nil {
				return e
			}
		}
	}
	if e = ui.New(a.In, a.Out, o.Plain).Work(ctx, "Esperando salud del servidor", 0.6, func(c context.Context) error { return a.wait(c, func() bool { return a.healthy(c, s) }, "Servidor") }); e != nil {
		return e
	}
	_, _ = fmt.Fprintln(a.Out, "[ok] Servidor listo.")
	if a.Bootstrap != nil {
		e = a.Bootstrap(ctx, s, env)
	} else if s.Mode == "prod" {
		e = a.bootstrapProd(ctx, s)
	} else {
		cmd := exec.CommandContext(ctx, a.Executable, "nexo", "stack", "_bootstrap", "--dir", s.Dir)
		cmd.Env = isolated(env)
		logFile, err := os.OpenFile(filepath.Join(s.Dir, "servidor.log"), os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		w := secrets.NewWriter(logFile, sensitive(env))
		cmd.Stdout = w
		cmd.Stderr = w
		runErr := cmd.Run()
		_ = w.Close()
		_ = logFile.Close()
		if runErr != nil {
			e = errors.New("no se pudo completar el bootstrap; revisa los accesos y la base")
		}
	}
	if e != nil {
		return e
	}
	a.HTTP.Timeout = 20 * time.Second
	peopleAPI := api(s, "")
	peopleAPI.HTTP = a.HTTP
	if e = ensurePeople(ctx, s, a.people(s.Mode), a.Activate, peopleAPI); e != nil {
		return e
	}
	s.UsersReady = true
	if e = state.Save(s); e != nil {
		return e
	}
	_, _ = fmt.Fprintf(a.Out, "[ok] %d personas disponibles.\n", len(a.people(s.Mode)))
	if e = a.checkVector(ctx, &s, env); e != nil {
		return e
	}
	if !o.NoData && s.Mode != "prod" {
		if e = a.loadExample(ctx, &s); e != nil {
			return e
		}
	}
	if o.OwnDir != "" {
		if e = a.data(ctx, s, o); e != nil {
			return e
		}
	}
	return a.summary(s)
}
func (a *App) people(mode string) []Person {
	var out []Person
	for _, p := range a.Seeder.Personas() {
		if mode == "prod" && p.Username != "superadmin" || mode == "dev" && p.Username != "superadmin" && p.Username != "publicador" && p.Username != "lector" {
			continue
		}
		out = append(out, Person{p.Username, p.Email, p.Plataforma})
	}
	return out
}
func (a *App) summary(s state.State) error {
	suffix := " --mode " + s.Mode
	if s.Name != "" {
		suffix += " --name " + s.Name
	}
	_, _ = fmt.Fprintf(a.Out, "Panel: %s/dashboard/\nAccesos privados en %s\nSiguiente paso: engram nexo tour%s\n", s.ServerURL, filepath.Join(s.Dir, "accesos.md"), suffix)
	return nil
}
func (a *App) status(ctx context.Context, s state.State) error {
	health := "detenido o no saludable"
	if a.healthy(ctx, s) {
		health = "en marcha"
	} else if s.Prepared {
		health = "archivos preparados"
	}
	_, _ = fmt.Fprintf(a.Out, "%s: %s\nVersión: %s\nServidor: %s; Postgres: %d\nBúsqueda: %s; reordenador: %s; R1: %s\n", s.Project, health, s.Version, s.ServerURL, s.PGPort, s.Embedder, s.Reranker, s.R1)
	return nil
}
func (a *App) down(ctx context.Context, s state.State, volumes bool) error {
	if s.Mode != "prod" {
		id, e := proc.Read(filepath.Join(s.Dir, "servidor.pid"))
		if e == nil && a.Alive(id) {
			if e = secrets.Write(filepath.Join(s.Dir, "detener"), []byte(fmt.Sprint(id.PID))); e != nil {
				return e
			}
			if e = a.wait(ctx, func() bool { return !a.Alive(id) }, "Detención"); e != nil {
				return e
			}
		}
		if child, e := proc.Read(filepath.Join(s.Dir, "cloud.pid")); e == nil && a.Alive(child) {
			if e = a.Stop(child); e != nil {
				return e
			}
		}
		_ = os.Remove(filepath.Join(s.Dir, "servidor.pid"))
		_ = os.Remove(filepath.Join(s.Dir, "cloud.pid"))
	}
	if !s.ExternalDB || s.Mode == "prod" {
		args := []string{"down", "--remove-orphans"}
		if volumes {
			args = append(args, "-v")
		}
		if _, e := a.compose(ctx, s, args...); e != nil {
			return errors.New("no se pudieron retirar los recursos propios de Docker; conserva el estado y vuelve a intentar")
		}
	}
	_, _ = fmt.Fprintln(a.Out, "[ok] Entorno detenido.")
	return nil
}
func (a *App) reset(ctx context.Context, s state.State, o Options) error {
	if !a.Interactive && !o.Yes {
		return errors.New("reset sin terminal requiere --yes para confirmar el borrado de este entorno")
	}
	if !o.Yes {
		ok, e := ui.New(a.In, a.Out, o.Plain).Confirm("Borrar los datos y volúmenes de "+s.Project+"?", false)
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("borrado cancelado")
		}
	}
	if e := a.down(ctx, s, true); e != nil {
		return e
	}
	if s.ExternalDB {
		_, _ = fmt.Fprintln(a.Out, "La base externa conserva sus datos; reset solo retira el estado local.")
	} // Solo archivos conocidos: jamás RemoveAll sobre --dir.
	for _, n := range []string{".env", "estado.json", "accesos.md", "compose.yaml", "Caddyfile", "servidor.log", "tour.done", "agentes.json", "detener", "ca.crt", "postgres-init/01-cuentas.sh"} {
		if e := os.Remove(filepath.Join(s.Dir, n)); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	_, _ = fmt.Fprintln(a.Out, "[ok] Recursos propios borrados.")
	return nil
}
func (a *App) logs(ctx context.Context, s state.State) error {
	env, e := render.ReadEnv(s.Dir)
	if e != nil {
		return e
	}
	values := sensitive(env)
	access, _ := render.ReadAccess(s.Dir)
	for _, p := range access {
		values = append(values, p.Token)
	}
	w := secrets.NewWriter(a.Out, values)
	defer func() { _ = w.Close() }()
	if s.Mode == "prod" {
		b, e := a.compose(ctx, s, "logs", "--no-color", "--tail", "200")
		if e != nil {
			return errors.New("no se pudo leer el registro Docker")
		}
		_, e = w.Write(b)
		return e
	}
	f, e := os.Open(filepath.Join(s.Dir, "servidor.log"))
	if errors.Is(e, os.ErrNotExist) {
		_, _ = fmt.Fprintln(a.Out, "El servidor aún no tiene registro.")
		return nil
	}
	if e != nil {
		return e
	}
	defer func() { _ = f.Close() }()
	_, e = io.Copy(w, f)
	return e
}
func (a *App) access(s state.State, o Options) error {
	path := filepath.Join(s.Dir, "accesos.md")
	if o.Open {
		return ui.Open(path)
	}
	if o.Show != "" {
		return errors.New("los tokens no se imprimen: usa --copiar usuario o --abrir")
	}
	if o.Copy != "" {
		p, e := render.ReadAccess(s.Dir)
		if e != nil {
			return e
		}
		for _, v := range p {
			if v.Username == o.Copy {
				if e = ui.Copy(v.Token); e != nil {
					return e
				}
				_, _ = fmt.Fprintln(a.Out, "Token copiado al portapapeles.")
				return nil
			}
		}
		return errors.New("persona desconocida")
	}
	_, _ = fmt.Fprintln(a.Out, "Accesos privados en "+path)
	return nil
}
func (a *App) seedEnv(s state.State) (stackapi.Env, error) {
	p, e := render.ReadAccess(s.Dir)
	if e != nil {
		return stackapi.Env{}, e
	}
	m := map[string]string{}
	values := []string{}
	for _, v := range p {
		m[v.Username] = v.Token
		values = append(values, v.Token)
	}
	env, _ := render.ReadEnv(s.Dir)
	values = append(values, sensitive(env)...)
	a.values = append(a.values, values...)
	return stackapi.Env{ServerURL: s.ServerURL, Tokens: m, Logf: func(f string, args ...any) {
		_, _ = fmt.Fprintln(a.Out, secrets.Clean(fmt.Sprintf(f, args...), values))
	}}, nil
}
func (a *App) loadExample(ctx context.Context, s *state.State) error {
	env, e := a.seedEnv(*s)
	if e != nil {
		return e
	}
	r, e := a.Seeder.Load(ctx, env, stackapi.LoadOptions{Example: true})
	if errors.Is(e, ErrSeedUnavailable) {
		_, _ = fmt.Fprintln(a.Out, "Aviso: semilla no disponible; las personas y el panel están listos.")
		return nil
	}
	if e != nil {
		return errors.New("no se pudo cargar el ejemplo")
	}
	s.ExampleLoaded = true
	if e = state.Save(*s); e != nil {
		return e
	}
	_, _ = fmt.Fprintf(a.Out, "[ok] %d documentos, %d memorias y %d propuestas cargados.\n", r.Documents, r.Memories, r.Proposals)
	if s.Embedder != "off" {
		return api(*s, env.Tokens["superadmin"]).Do(ctx, "POST", "/nexo/kb/v1/reindex", map[string]any{}, nil)
	}
	return nil
}
func (a *App) data(ctx context.Context, s state.State, o Options) error {
	if s.Mode == "prod" {
		return errors.New("usa kb upload para publicar contenido en prod")
	}
	env, e := a.seedEnv(s)
	if e != nil {
		return e
	}
	var r stackapi.Report
	if o.RemoveExample {
		r, e = a.Seeder.RemoveExample(ctx, env)
	} else if o.OwnDir != "" {
		_, _ = fmt.Fprintln(a.Out, "El contenido se guarda en Postgres; con búsqueda externa sus fragmentos se envían al proveedor. La redacción puede rechazar documentos.")
		r, e = a.Seeder.LoadOwn(ctx, env, stackapi.OwnOptions{Dir: o.OwnDir, PerSubfolder: o.PerSubfolder})
	} else {
		return a.loadExample(ctx, &s)
	}
	if e != nil {
		return e
	}
	if o.RemoveExample {
		s.ExampleLoaded = false
		if e = state.Save(s); e != nil {
			return e
		}
	}
	_, _ = fmt.Fprintf(a.Out, "[ok] %d documentos procesados.\n", r.Documents)
	return nil
}
func sensitive(env map[string]string) []string {
	var s []string
	for k, v := range env {
		if strings.Contains(k, "PASSWORD") || strings.Contains(k, "SECRET") || strings.Contains(k, "TOKEN") || strings.Contains(k, "KEY") || strings.Contains(k, "DATABASE_URL") {
			s = append(s, v)
		}
	}
	return s
}
func isolated(env map[string]string) []string {
	out := []string{}
	for _, s := range os.Environ() {
		k, _, _ := strings.Cut(s, "=")
		if strings.HasPrefix(k, "NEXO_") || strings.HasPrefix(k, "ENGRAM_") || strings.HasSuffix(k, "API_KEY") {
			continue
		}
		out = append(out, s)
	}
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
