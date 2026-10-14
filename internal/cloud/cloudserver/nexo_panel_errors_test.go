package cloudserver

// Nexo IA · NX-10 (panel, OP-06) — los errores de texto plano del panel salen
// en español y sin el detalle de la base; todo lo demás pasa igual.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/dashboard"
)

func panelErrorsRun(t *testing.T, path string, h http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	dashboard.EnableNexoBrand()
	rec := httptest.NewRecorder()
	nexoPanelErrorText(h).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	return rec
}

func TestNexoPanelErrorTextTranslatesPlainTextErrors(t *testing.T) {
	rec := panelErrorsRun(t, "/dashboard/admin/users", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "No tienes permiso") {
		t.Fatalf("forbidden: %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("cabeceras de http.Error perdidas: %q %q", ct, rec.Header().Get("X-Content-Type-Options"))
	}

	rec = panelErrorsRun(t, "/dashboard/admin/users", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "list users: pq: password authentication failed for user \"app\"", http.StatusInternalServerError)
	})
	body := rec.Body.String()
	if rec.Code != 500 || !strings.Contains(body, "error interno") || strings.Contains(body, "pq:") || strings.Contains(body, "password") {
		t.Errorf("un 500 no debe mostrar el detalle: %d %q", rec.Code, body)
	}

	rec = panelErrorsRun(t, "/dashboard/admin/users", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "create user: ERROR: duplicate key value violates unique constraint (SQLSTATE 23505)", http.StatusBadRequest)
	})
	if body := rec.Body.String(); rec.Code != 400 || !strings.Contains(body, "Ya existe") || strings.Contains(body, "SQLSTATE") {
		t.Errorf("duplicado: %d %q", rec.Code, body)
	}
}

func TestNexoPanelErrorTextLeavesEverythingElse(t *testing.T) {
	for name, tc := range map[string]struct {
		path string
		h    http.HandlerFunc
		want string
	}{
		"mensaje que no conoce": {"/dashboard/x", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "mensaje nuevo en español", http.StatusBadRequest)
		}, "mensaje nuevo en español\n"},
		"fuera del panel": {"/sync/push", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "forbidden", http.StatusForbidden) }, "forbidden\n"},
		"estáticos":       {"/dashboard/static/x.css", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "forbidden", http.StatusForbidden) }, "forbidden\n"},
		"éxito":           {"/dashboard/x", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("forbidden")) }, "forbidden"},
		"html de error": {"/dashboard/x", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(404)
			_, _ = w.Write([]byte("forbidden"))
		}, "forbidden"},
	} {
		rec := panelErrorsRun(t, tc.path, tc.h)
		if rec.Body.String() != tc.want {
			t.Errorf("%s: %q, quería %q", name, rec.Body.String(), tc.want)
		}
	}
	// Un cuerpo grande no se retiene ni se toca.
	big := strings.Repeat("x", 20<<10)
	rec := panelErrorsRun(t, "/dashboard/x", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(big))
	})
	if rec.Code != 400 || rec.Body.String() != big {
		t.Errorf("cuerpo grande alterado: %d %d", rec.Code, rec.Body.Len())
	}
}

// El panel real: un POST sin sesión pasa por el middleware sin romperse.
func TestNexoPanelErrorTextInRealHandler(t *testing.T) {
	srv := New(&fakeStore{}, strictBearerAuth{token: "test-token"}, 0)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader(strings.Repeat("a", 1<<20)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "demasiado grande") {
		t.Fatalf("login con cuerpo enorme: %d %q", rec.Code, rec.Body.String())
	}
}
