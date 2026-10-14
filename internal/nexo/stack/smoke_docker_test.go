//go:build stackdocker

package stack

// Esta prueba es optativa: solo el coordinador la ejecuta con acceso al motor.
import (
	"bytes"
	"context"
	"errors"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/preflight"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/proc"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/secrets"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStackDockerSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	if e := preflight.Docker(ctx, preflight.Exec); e != nil {
		t.Fatal(e)
	}
	project := state.Project("demo", "smoke")
	resources := func() []string {
		var found []string
		for _, args := range [][]string{{"ps", "-a", "--filter", "label=com.docker.compose.project=" + project, "--format", "{{.Names}}"}, {"volume", "ls", "--filter", "name=" + project, "--format", "{{.Name}}"}, {"network", "ls", "--filter", "name=" + project, "--format", "{{.Name}}"}} {
			b, e := preflight.Exec(ctx, "docker", args...)
			if e != nil {
				t.Error("no se pudo comprobar recursos propios")
				continue
			}
			for _, line := range strings.Fields(string(b)) {
				if strings.HasPrefix(line, project) {
					found = append(found, line)
				}
			}
		}
		return found
	}
	if len(resources()) != 0 {
		t.Fatal("ya hay recursos smoke: no se tocaron; usa un motor sin ese proyecto")
	}
	home := t.TempDir()
	data := filepath.Join(home, "cliente")
	t.Setenv("NEXO_STACK_HOME", home)
	t.Setenv("ENGRAM_DATA_DIR", data)
	t.Setenv("NEXO_HOME", filepath.Join(home, "copia"))
	t.Setenv("NEXO_STACK_DATABASE_URL", "")
	t.Setenv("NEXO_STACK_MIGRATION_DATABASE_URL", "")
	key, e := secrets.Hex(32)
	if e != nil {
		t.Fatal("generación de clave sintética")
	}
	t.Setenv("NEXO_KB_EMBED_API_KEY", key)
	cwd, e := os.Getwd()
	if e != nil {
		t.Fatal(e)
	}
	root := filepath.Clean(filepath.Join(cwd, "../../.."))
	name := "engram"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(home, name)
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/engram")
	build.Dir = root
	build.Env = append(os.Environ(), "GOTOOLCHAIN=go1.27.2")
	if build.Run() != nil {
		t.Fatal("no se pudo construir el binario de humo")
	}
	var output bytes.Buffer
	a := New(strings.NewReader(""), &output, &output, data, "smoke")
	a.Executable = bin
	a.Home = home
	a.Seeder = unavailableSeeder{}
	dir, _ := state.Directory(home, "demo", "smoke", "")
	s := state.State{Mode: "demo", Name: "smoke", Project: project, Dir: dir}
	ids := []proc.Identity{}
	private := []string{key}
	collect := func() {
		if env, e := render.ReadEnv(dir); e == nil {
			private = append(private, sensitive(env)...)
		}
		if acc, e := render.ReadAccess(dir); e == nil {
			for _, v := range acc {
				private = append(private, v.Token)
			}
		}
	}
	tail := func(b []byte) string {
		lines := strings.Split(string(b), "\n")
		if len(lines) > 80 {
			lines = lines[len(lines)-80:]
		}
		return secrets.Clean(strings.Join(lines, "\n"), private)
	}
	defer func() {
		for _, file := range []string{"servidor.pid", "cloud.pid"} {
			if id, e := proc.Read(filepath.Join(dir, file)); e == nil {
				ids = append(ids, id)
			}
		}
		collect()
		if t.Failed() {
			if b, e := os.ReadFile(filepath.Join(dir, "servidor.log")); e == nil {
				t.Log("Últimas líneas del servidor (redactadas):\n" + tail(b))
			}
			if _, e := os.Stat(filepath.Join(dir, ".env")); e == nil {
				b, _ := a.compose(ctx, s, "logs", "--no-color", "--tail", "80")
				t.Log("Últimas líneas de compose (redactadas):\n" + tail(b))
			}
		}
		if current, e := state.Load(dir); e == nil {
			cleanupCtx, done := context.WithTimeout(context.Background(), 2*time.Minute)
			defer done()
			if e = a.down(cleanupCtx, current, true); e != nil {
				t.Error("falló la limpieza propia; diagnóstico sin secretos")
			}
			if e = a.reset(cleanupCtx, current, Options{Yes: true, Plain: true}); e != nil {
				t.Error("no se pudo retirar el estado propio")
			}
		}
		for _, id := range ids {
			if proc.Alive(id) {
				if e = proc.Stop(id); e != nil {
					t.Error("no se pudo detener un PID propio verificado")
				}
				t.Error("el cierre dejó un proceso registrado vivo")
			}
		}
		if len(resources()) != 0 {
			t.Error("quedan recursos del proyecto smoke")
		}
		for _, secret := range private {
			if secret != "" && strings.Contains(output.String(), secret) {
				t.Error("una salida contiene un secreto; contenido omitido")
			}
		}
	}()
	port := smokePort(t, "STACK_SMOKE_PORT", 18480)
	pgport := smokePort(t, "STACK_SMOKE_DBPORT", 55480)
	base := []string{"--mode", "demo", "--name", "smoke", "--yes", "--plain", "--no-tour", "--no-agents", "--port", strconv.Itoa(port), "--pg-port", strconv.Itoa(pgport)}
	run := func(cmd string) {
		t.Helper()
		if a.Run(append([]string{"stack", cmd}, base...)) != 0 {
			collect()
			t.Fatalf("stack %s falló: %s", cmd, secrets.Clean(output.String(), private))
		}
	}
	run("up")
	s, e = state.Load(dir)
	if e != nil {
		t.Fatal("no hay estado válido")
	}
	if s.Port != port || s.PGPort != pgport {
		t.Fatal("los puertos pedidos están ocupados; la prueba no acepta puertos vecinos")
	}
	for _, path := range []string{"/dashboard/", "/nexo/health/ready"} {
		r, e := http.Get(s.ServerURL + path)
		if e != nil {
			t.Fatal("el panel o la preparación no responden")
		}
		_ = r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatal("el panel o la preparación no están listos")
		}
	}
	acc, e := render.ReadAccess(dir)
	if e != nil || len(acc) != 6 {
		t.Fatal("no hay seis accesos")
	}
	collect()
	smokePrivateFile(t, filepath.Join(dir, "accesos.md"))
	checkPeople := func() map[string]string {
		t.Helper()
		c := api(s, acc[0].Token)
		for _, v := range acc {
			if v.Username == "superadmin" {
				c.Token = v.Token
			}
		}
		var people []struct {
			Username string `json:"username"`
			ID       string `json:"principal_id"`
		}
		if e := c.Do(ctx, "GET", "/admin/users", nil, &people); e != nil || len(people) != 6 {
			t.Fatal("la API no devuelve seis personas")
		}
		result := map[string]string{}
		for _, p := range people {
			result[p.Username] = p.ID
		}
		for _, v := range acc {
			var me map[string]any
			if e := api(s, v.Token).Do(ctx, "GET", "/nexo/authz/v1/me", nil, &me); e != nil {
				t.Fatal("un token de persona no funciona; token omitido")
			}
		}
		return result
	}
	first := checkPeople()
	id, e := proc.Read(filepath.Join(dir, "servidor.pid"))
	if e != nil {
		t.Fatal("falta identidad del servidor")
	}
	ids = append(ids, id)
	if child, e := proc.Read(filepath.Join(dir, "cloud.pid")); e == nil {
		ids = append(ids, child)
	}
	run("status")
	run("logs")
	run("up")
	next, e := proc.Read(filepath.Join(dir, "servidor.pid"))
	if e != nil || next != id {
		t.Fatal("repetir up reinició el servidor")
	}
	if !sameSmokePeople(first, checkPeople()) {
		t.Fatal("repetir up duplicó o reemplazó personas")
	}
	if b, e := os.ReadFile(filepath.Join(dir, "servidor.log")); e == nil {
		for _, secret := range private {
			if secret != "" && strings.Contains(string(b), secret) {
				t.Fatal("un registro contiene un secreto; contenido omitido")
			}
		}
	}
	run("down")
	if proc.Alive(id) {
		t.Fatal("down no detuvo el servidor")
	}
	if _, e = os.Stat(filepath.Join(dir, "accesos.md")); e != nil {
		t.Fatal("down borró los accesos")
	}
	run("up")
	if !sameSmokePeople(first, checkPeople()) {
		t.Fatal("down/up perdió o duplicó datos")
	}
	if id, e := proc.Read(filepath.Join(dir, "servidor.pid")); e == nil {
		ids = append(ids, id)
	}
	if child, e := proc.Read(filepath.Join(dir, "cloud.pid")); e == nil {
		ids = append(ids, child)
	}
	run("reset")
	for _, path := range []string{"estado.json", "accesos.md", ".env"} {
		if _, e = os.Stat(filepath.Join(dir, path)); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("reset no borró archivos propios")
		}
	}
}
func smokePort(t *testing.T, key string, def int) int {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, e := strconv.Atoi(v)
	if e != nil || n < 1 || n > 65535 {
		t.Fatal("puerto de humo inválido")
	}
	return n
}
func sameSmokePeople(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
func smokePrivateFile(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		i, e := os.Stat(path)
		if e != nil || i.Mode().Perm()&0077 != 0 {
			t.Fatal("permisos del acceso no restringidos")
		}
		return
	}
	who, e := exec.Command("whoami").Output()
	if e != nil {
		t.Fatal("no se pudo comprobar usuario de ACL")
	}
	acl, e := exec.Command("icacls", path).Output()
	if e != nil {
		t.Fatal("no se pudo comprobar ACL")
	}
	text := strings.ToLower(string(acl))
	user := strings.ToLower(strings.TrimSpace(string(who)))
	if !strings.Contains(text, user+":") || strings.Contains(text, "(i)") {
		t.Fatal("ACL del acceso no está restringida")
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, ":(") && !strings.Contains(line, user+":") {
			t.Fatal("ACL concede acceso a otra identidad")
		}
	}
}
