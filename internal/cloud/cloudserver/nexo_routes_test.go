package cloudserver

// Nexo IA · NX-07 fase S5 (S5-02, RS-41, CA-S14) — cobertura del registro de
// rutas: recorre el mux real del servidor y compara con el registro Nexo y
// con la lista revisada de rutas de Engram (testdata/nexo_engram_routes.txt).

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/routes"
)

const nexoEngramRoutesFile = "testdata/nexo_engram_routes.txt"

// nexoMuxPatterns devuelve los patrones registrados en un http.ServeMux.
// ServeMux no los expone: se leen por reflexión del índice interno
// (net/http routingIndex: segments y multis, que juntos contienen todos los
// patrones). Si una versión de Go cambia esos campos, la prueba falla con un
// mensaje claro en vez de pasar en falso.
func nexoMuxPatterns(t *testing.T, mux *http.ServeMux) []string {
	t.Helper()
	idx := reflect.ValueOf(mux).Elem().FieldByName("index")
	if !idx.IsValid() {
		t.Fatal("net/http.ServeMux has no index field: update nexoMuxPatterns for this Go version")
	}
	seen := map[string]bool{}
	add := func(p reflect.Value) {
		if p.Kind() == reflect.Pointer {
			p = p.Elem()
		}
		str := p.FieldByName("str")
		if !str.IsValid() || str.Kind() != reflect.String {
			t.Fatal("net/http pattern has no str field: update nexoMuxPatterns for this Go version")
		}
		seen[str.String()] = true
	}
	segments, multis := idx.FieldByName("segments"), idx.FieldByName("multis")
	if !segments.IsValid() || !multis.IsValid() {
		t.Fatal("net/http routingIndex changed: update nexoMuxPatterns for this Go version")
	}
	for it := segments.MapRange(); it.Next(); {
		for i := 0; i < it.Value().Len(); i++ {
			add(it.Value().Index(i))
		}
	}
	for i := 0; i < multis.Len(); i++ {
		add(multis.Index(i))
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// nexoIsNexoPath: rutas que solo puede registrar Nexo (nunca en la lista de Engram).
func nexoIsNexoPath(pattern string) bool {
	path := pattern
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		path = pattern[i+1:]
	}
	return strings.HasPrefix(path, "/nexo/") || path == "/dashboard/kb" || strings.HasPrefix(path, "/dashboard/kb/")
}

func nexoReadEngramRoutes(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open(nexoEngramRoutesFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if out[line] {
			t.Errorf("%s: duplicated line %q", nexoEngramRoutesFile, line)
		}
		out[line] = true
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// CA-S14: toda ruta Nexo del mux está en el registro con capacidad válida, y
// toda otra ruta está en la lista revisada de Engram (y la lista no tiene
// rutas que ya no existen).
func TestNexoRouteRegistryCoversMux(t *testing.T) {
	srv := New(&fakeStore{}, strictBearerAuth{token: "test-token"}, 0)
	_ = srv.Handler()
	patterns := nexoMuxPatterns(t, srv.mux)
	if len(patterns) < 60 {
		t.Fatalf("mux patterns read by reflection: %d (expected all server routes)", len(patterns))
	}
	registry := map[string]routes.Route{}
	for _, r := range srv.nexo.routes.Routes() {
		registry[r.Pattern()] = r
		if err := routes.Validate(r.Capability); err != nil {
			t.Errorf("%s: %v", r.Pattern(), err)
		}
	}
	engram := nexoReadEngramRoutes(t)
	inMux := map[string]bool{}
	for _, p := range patterns {
		inMux[p] = true
		_, registered := registry[p]
		switch {
		case registered:
		case nexoIsNexoPath(p):
			t.Errorf("Nexo route %q is not in the route registry: register it with s.nexoRoute(mux, method, path, capability, handler)", p)
		case !engram[p]:
			t.Errorf("unreviewed Engram route %q: review its authorization and responses and add it to %s", p, nexoEngramRoutesFile)
		}
	}
	for p := range engram {
		if nexoIsNexoPath(p) {
			t.Errorf("%s lists a Nexo route %q: Nexo routes go in the registry", nexoEngramRoutesFile, p)
		}
		if !inMux[p] {
			t.Errorf("%s lists %q, which is no longer registered: remove it", nexoEngramRoutesFile, p)
		}
	}
	for p := range registry {
		if !inMux[p] {
			t.Errorf("registry route %q is not in the mux", p)
		}
	}
	// Cada ruta fija del registro resuelve a su patrón en el mux (API pública).
	for p, r := range registry {
		if strings.Contains(r.Path, "{") {
			continue
		}
		if _, got := srv.mux.Handler(httptest.NewRequest(r.Method, r.Path, nil)); got != p {
			t.Errorf("%s resolves to %q", p, got)
		}
	}
}

// CA-S14: una ruta Nexo sin capacidad declarada (o con una desconocida) no
// se puede registrar.
func TestNexoRouteRequiresCapability(t *testing.T) {
	for _, capability := range []string{"", "  ", "platform.nope", "space.read|platform.nope", "space.read|space.publish+token.write", "public|space.read", "admin"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("capability %q: registration must panic", capability)
				}
			}()
			var reg routes.Registry
			reg.Handle(http.NewServeMux(), "GET", "/nexo/x", capability, func(http.ResponseWriter, *http.Request) {})
		}()
	}
	var reg routes.Registry
	mux := http.NewServeMux()
	for _, capability := range []string{"public", "identity", "platform.audit.read", "self.read|platform.tokens.manage", "platform.audit.read+token.write", "project.read|space.read|platform.directory.read"} {
		reg.Handle(mux, "GET", "/nexo/ok/"+strings.NewReplacer("|", "-", "+", "-", ".", "-").Replace(capability), capability, func(http.ResponseWriter, *http.Request) {})
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("duplicated route must panic")
			}
		}()
		reg.Handle(mux, "GET", "/nexo/ok/public", "public", func(http.ResponseWriter, *http.Request) {})
	}()
	// Una ruta /nexo/* registrada directo en el mux es detectada por el recorrido.
	mux.HandleFunc("GET /nexo/sin-registro", func(http.ResponseWriter, *http.Request) {})
	found := false
	for _, p := range nexoMuxPatterns(t, mux) {
		found = found || p == "GET /nexo/sin-registro"
	}
	if !found {
		t.Fatal("the mux walk must see routes registered outside the registry")
	}
}
