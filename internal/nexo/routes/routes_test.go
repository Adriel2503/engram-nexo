package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidate(t *testing.T) {
	for _, ok := range []string{"public", "identity", "self.read", "platform.audit.read+token.write", "space.read|platform.audit.read", "project.read|space.read|platform.directory.read"} {
		if err := Validate(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " ", "admin", "space.nope", "space.read|", "space.read|space.publish+token.write", "public+token.write", "identity|public"} {
		if err := Validate(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestRegistryHandleAndRoutes(t *testing.T) {
	var reg Registry
	mux := http.NewServeMux()
	reg.Handle(mux, "GET", "/nexo/b", Identity, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	reg.Registrar(mux)("POST", "/nexo/a", "platform.users.manage", func(http.ResponseWriter, *http.Request) {})
	got := reg.Routes()
	if len(got) != 2 || got[0].Pattern() != "GET /nexo/b" || got[1].Pattern() != "POST /nexo/a" || got[1].Capability != "platform.users.manage" {
		t.Fatalf("routes: %+v", got)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/nexo/b", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("handler not mounted: %d", rec.Code)
	}
	for name, fn := range map[string]func(){
		"no method":     func() { reg.Handle(mux, "", "/nexo/c", Public, nil) },
		"bad path":      func() { reg.Handle(mux, "GET", "nexo/c", Public, nil) },
		"no capability": func() { reg.Handle(mux, "GET", "/nexo/c", "", nil) },
		"duplicate":     func() { reg.Handle(mux, "GET", "/nexo/b", Public, nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: must panic", name)
				}
			}()
			fn()
		}()
	}
}
