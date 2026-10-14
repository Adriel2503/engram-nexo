package seed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/stackapi"
)

func seedHTTPServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	var listener net.Listener
	var err error
	for port := 18490; port <= 18499; port++ {
		listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal("puertos de prueba ocupados")
	}
	ts := &httptest.Server{Listener: listener, Config: &http.Server{Handler: h}}
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

func TestLoadDisabledAndCanceled(t *testing.T) {
	loader := New()
	rep, err := loader.Load(context.Background(), stackapi.Env{}, stackapi.LoadOptions{})
	if err != nil || rep.Spaces != 0 {
		t.Fatal("sin ejemplo debe ser una operación vacía")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loader.Load(ctx, stackapi.Env{}, stackapi.LoadOptions{Example: true}); !errors.Is(err, context.Canceled) {
		t.Fatal("Load debe respetar cancelación")
	}
	if _, err := loader.RemoveExample(ctx, stackapi.Env{}); !errors.Is(err, context.Canceled) {
		t.Fatal("RemoveExample debe respetar cancelación antes de pedir tokens")
	}
}

func TestOwnUploadLimits(t *testing.T) {
	for _, tc := range []struct{ name, env, value string }{
		{"archivo", service.EnvUploadMaxFileBytes, "8"}, {"cuerpo", service.EnvUploadMaxBytes, "32"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "guia.md"), []byte("# Guía limpia\n\nTexto."), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(tc.env, tc.value)
			rep, err := New().LoadOwn(context.Background(), stackapi.Env{Tokens: map[string]string{"publicador": "sin-uso"}}, stackapi.OwnOptions{Dir: dir})
			if err != nil || rep.Documents != 0 || rep.Spaces != 0 || len(rep.Skipped) != 1 {
				t.Fatal("un archivo fuera de límites debe omitirse antes de llamar a la API")
			}
		})
	}
}

func TestOwnSlugCollision(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"Mi Carpeta", "Mi_Carpeta"} {
		p := filepath.Join(dir, sub)
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "guia.md"), []byte("Guía."), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := New().LoadOwn(context.Background(), stackapi.Env{}, stackapi.OwnOptions{Dir: dir, PerSubfolder: true})
	if err == nil || !strings.Contains(err.Error(), "mismo espacio") {
		t.Fatal("las subcarpetas no deben fusionarse silenciosamente")
	}
}

func TestOwnCancellationDuringHTTP(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "guia.md"), []byte("# Guía\n\nTexto."), 0600); err != nil {
		t.Fatal(err)
	}
	arrived := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ts := seedHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(arrived); <-r.Context().Done() }))
	result := make(chan error, 1)
	go func() {
		_, err := New().LoadOwn(ctx, stackapi.Env{ServerURL: ts.URL, Tokens: map[string]string{"publicador": "sin-uso"}}, stackapi.OwnOptions{Dir: dir})
		result <- err
	}()
	select {
	case <-arrived:
	case <-time.After(3 * time.Second):
		t.Fatal("no llegó la solicitud")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancelación HTTP no propagada")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelación bloqueada")
	}
}

func TestOwnSurfacesPartialRejection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "guia.md"), []byte("# Guía\n\nTexto."), 0600); err != nil {
		t.Fatal(err)
	}
	var uploads atomic.Int32
	ts := seedHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			_, _ = io.WriteString(w, "{}")
			return
		}
		uploads.Add(1)
		_, _ = io.WriteString(w, "{\"results\":[{\"status\":\"rejected\",\"error_code\":\"secret_detected\",\"reason\":\"contenido rechazado\"}],\"summary\":{\"rejected\":1}}")
	}))
	rep, err := New().LoadOwn(context.Background(), stackapi.Env{ServerURL: ts.URL, Tokens: map[string]string{"publicador": "sin-uso"}}, stackapi.OwnOptions{Dir: dir})
	if err == nil || rep.Documents != 0 || uploads.Load() != 1 {
		t.Fatal("un rechazo parcial HTTP 200 no puede anunciar éxito")
	}
}
