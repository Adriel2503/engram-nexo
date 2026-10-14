package stack

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/proc"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/secrets"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
)

func TestMain(m *testing.M) {
	if os.Getenv("STACK_AGENT_HELPER") == "1" {
		if len(os.Args) < 3 || os.Args[1] != "setup" {
			os.Exit(2)
		}
		dir := filepath.Join(os.Getenv("STACK_AGENT_TEST_DIR"), os.Args[2])
		_ = os.MkdirAll(dir, 0700)
		_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte("configuración sintética: "+os.Getenv("ENGRAM_DATA_DIR")), 0600)
		_, _ = fmt.Println("Destino: " + dir)
		os.Exit(0)
	}
	if os.Getenv("STACK_DAEMON_HELPER") == "1" {
		key := os.Getenv("NEXO_KB_EMBED_API_KEY")
		_, _ = fmt.Print("clave=" + key[:10])
		time.Sleep(10 * time.Millisecond)
		_, _ = fmt.Println(key[10:])
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestStackDaemonRedacts(t *testing.T) {
	t.Setenv("STACK_DAEMON_HELPER", "1")
	key, _ := secrets.Hex(32)
	dir := t.TempDir()
	src, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(src)
	if e != nil {
		t.Fatal(e)
	}
	name := "engram"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(dir, name)
	if e = os.WriteFile(bin, b, 0700); e != nil {
		t.Fatal(e)
	}
	s := state.State{Dir: dir}
	if e = render.WriteEnv(dir, map[string]string{"NEXO_KB_EMBED_API_KEY": key}); e != nil {
		t.Fatal(e)
	}
	a := New(strings.NewReader(""), io.Discard, io.Discard, t.TempDir(), "test")
	a.Executable = bin
	done := make(chan error, 1)
	go func() { done <- a.serve(s) }()
	defer func() { _ = secrets.Write(filepath.Join(dir, "detener"), []byte(fmt.Sprint(os.Getpid()))) }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if e = a.wait(ctx, func() bool {
		log, e := os.ReadFile(filepath.Join(dir, "servidor.log"))
		return e == nil && strings.Contains(string(log), "oculto")
	}, "supervisor falso"); e != nil {
		t.Fatal("supervisor no registró la línea redactada")
	}
	if e = secrets.Write(filepath.Join(dir, "detener"), []byte(fmt.Sprint(os.Getpid()))); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal("falló cierre del supervisor")
		}
	case <-ctx.Done():
		t.Fatal("no se detuvo supervisor")
	}
	log, e := os.ReadFile(filepath.Join(dir, "servidor.log"))
	if e != nil || strings.Contains(string(log), key) {
		t.Fatal("filtración")
	}
	id, e := proc.Read(filepath.Join(dir, "cloud.pid"))
	if e != nil || proc.Alive(id) {
		t.Fatal("hijo huérfano")
	}
}
