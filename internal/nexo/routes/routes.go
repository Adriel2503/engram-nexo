// Package routes es el registro de rutas HTTP de Nexo con su capacidad
// declarada (NX-07 fase S5, S5-02; RS-41, CA-S14; OWASP API5).
//
// Toda ruta Nexo se registra con Registry.Handle (en cloudserver,
// CloudServer.nexoRoute), nunca con mux.HandleFunc directo: así cada ruta
// declara qué capacidad la protege. La prueba
// cloudserver.TestNexoRouteRegistryCoversMux recorre el mux del servidor y
// falla si una ruta Nexo no está en el registro o si aparece una ruta de
// Engram que no está en la lista revisada
// (internal/cloud/cloudserver/testdata/nexo_engram_routes.txt).
//
// La capacidad declarada documenta y fija el contrato; la comprobación real
// sigue dentro de cada handler (authz.Can con el ámbito de la petición). La
// declaración es una expresión:
//
//	platform.audit.read                         una capacidad de authz
//	self.read|platform.tokens.manage            cualquiera de ellas (según el caso)
//	platform.scope.create+platform.scope.assign_owner   todas
//	platform.audit.read+token.write             capacidad y además token write
//
// con estos marcadores además de las capacidades de authz:
//
//	public      sin autenticación (solo datos no sensibles, p. ej. salud)
//	identity    cualquier identidad administrada; el resultado se limita a lo
//	            que esa identidad puede ver (propios o visibles por su rol)
//	token.write exige un token de alcance write
package routes

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
)

// Marcadores de acceso que no son capacidades de authz.
const (
	Public     = "public"
	Identity   = "identity"
	TokenWrite = "token.write"
)

// Route es una ruta registrada.
type Route struct {
	Method     string
	Path       string
	Capability string
}

// Pattern devuelve el patrón del mux ("GET /nexo/...").
func (r Route) Pattern() string { return r.Method + " " + r.Path }

// Validate comprueba la expresión de capacidad: no vacía, átomos separados
// por "|" o "+" (sin mezclar) y cada átomo es una capacidad conocida de
// authz o un marcador.
func Validate(capability string) error {
	capability = strings.TrimSpace(capability)
	if capability == "" {
		return fmt.Errorf("capability is required")
	}
	if strings.Contains(capability, "|") && strings.Contains(capability, "+") {
		return fmt.Errorf("capability %q mixes | and +", capability)
	}
	for _, atom := range strings.Split(strings.ReplaceAll(capability, "+", "|"), "|") {
		atom = strings.TrimSpace(atom)
		switch atom {
		case "":
			return fmt.Errorf("empty capability in %q", capability)
		case Public, Identity, TokenWrite:
			continue
		}
		if _, ok := authz.LevelOf(authz.Capability(atom)); !ok {
			return fmt.Errorf("unknown capability %q in %q", atom, capability)
		}
	}
	if capability != Public && strings.Contains(capability, Public) {
		return fmt.Errorf("%q: public cannot be combined", capability)
	}
	return nil
}

// Registry guarda las rutas registradas. El valor cero sirve.
type Registry struct {
	mu     sync.Mutex
	routes map[string]Route
}

// Handle valida y registra la ruta en el registro y en el mux. Una
// capacidad vacía o desconocida, un método vacío o una ruta repetida son
// errores de programación: entra en pánico al registrar (falla cualquier
// prueba que arme el servidor).
func (g *Registry) Handle(mux *http.ServeMux, method, path, capability string, h http.HandlerFunc) {
	method, path = strings.TrimSpace(method), strings.TrimSpace(path)
	if method == "" || !strings.HasPrefix(path, "/") {
		panic(fmt.Sprintf("nexo routes: invalid route %q %q", method, path))
	}
	if err := Validate(capability); err != nil {
		panic(fmt.Sprintf("nexo routes: %s %s: %v", method, path, err))
	}
	r := Route{Method: method, Path: path, Capability: strings.TrimSpace(capability)}
	g.mu.Lock()
	if g.routes == nil {
		g.routes = map[string]Route{}
	}
	if _, dup := g.routes[r.Pattern()]; dup {
		g.mu.Unlock()
		panic("nexo routes: duplicate route " + r.Pattern())
	}
	g.routes[r.Pattern()] = r
	g.mu.Unlock()
	mux.HandleFunc(r.Pattern(), h)
}

// Routes devuelve las rutas registradas, ordenadas por patrón.
func (g *Registry) Routes() []Route {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]Route, 0, len(g.routes))
	for _, r := range g.routes {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pattern() < out[j].Pattern() })
	return out
}

// Func registra una ruta (forma que reciben los módulos que no conocen
// cloudserver, como internal/nexo/kb/httpapi).
type Func func(method, path, capability string, h http.HandlerFunc)

// Registrar devuelve el registrador de este registro sobre mux.
func (g *Registry) Registrar(mux *http.ServeMux) Func {
	return func(method, path, capability string, h http.HandlerFunc) {
		g.Handle(mux, method, path, capability, h)
	}
}
