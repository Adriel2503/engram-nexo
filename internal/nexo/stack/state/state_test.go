package state

import (
	"path/filepath"
	"testing"
)

func TestStackState(t *testing.T) {
	home := t.TempDir()
	dir, e := Directory(home, "demo", "prueba", "")
	if e != nil || dir != filepath.Join(home, "demo-prueba") {
		t.Fatal("ruta")
	}
	s := State{Mode: "demo", Name: "prueba", Project: Project("demo", "prueba"), Dir: dir}
	if e = Save(s); e != nil {
		t.Fatal(e)
	}
	got, e := Load(dir)
	if e != nil || got.Project != s.Project {
		t.Fatal("estado")
	}
	unlock, e := Lock(dir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Lock(dir); e == nil {
		t.Fatal("bloqueo concurrente")
	}
	unlock()
	s.Project = "ajeno"
	if e = Save(s); e != nil {
		t.Fatal(e)
	}
	if _, e = Load(dir); e == nil {
		t.Fatal("aceptó ajeno")
	}
}
