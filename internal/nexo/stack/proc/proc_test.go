package proc

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStackProcessHelper(t *testing.T) {
	if os.Getenv("STACK_PROCESS_HELPER") != "1" {
		return
	}
	time.Sleep(20 * time.Second)
	os.Exit(0)
}
func TestStackProcess(t *testing.T) {
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
	dir := t.TempDir()
	bin := filepath.Join(dir, name)
	if e = os.WriteFile(bin, b, 0700); e != nil {
		t.Fatal(e)
	}
	id, e := Start(bin, []string{"-test.run=TestStackProcessHelper"}, append(os.Environ(), "STACK_PROCESS_HELPER=1"), filepath.Join(dir, "servidor.pid"))
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = Stop(id) }()
	if !Alive(id) {
		t.Fatal("proceso no está vivo")
	}
	other := id
	other.Started = "otro inicio"
	if Stop(other) == nil {
		t.Fatal("detuvo PID reutilizado")
	}
	if !Alive(id) {
		t.Fatal("el rechazo detuvo el proceso")
	}
	if e = Stop(id); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 30 && Alive(id); i++ {
		time.Sleep(30 * time.Millisecond)
	}
	if Alive(id) {
		t.Fatal("sigue vivo")
	}
}
