package preflight

import (
	"context"
	"errors"
	"testing"
)

func TestStackDocker(t *testing.T) {
	calls := 0
	r := func(context.Context, string, ...string) ([]byte, error) { calls++; return []byte("2.0"), nil }
	if Docker(context.Background(), r) != nil || calls != 2 {
		t.Fatal("preflight")
	}
	r = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("denegado") }
	if Docker(context.Background(), r) == nil {
		t.Fatal("aceptó motor detenido")
	}
	if CheckBind("0.0.0.0", false) == nil {
		t.Fatal("bind público")
	}
	if CheckBind("127.0.0.1", false) != nil {
		t.Fatal("bind local")
	}
}
