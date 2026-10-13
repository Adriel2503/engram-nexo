package cloudserver

// Nexo IA · NX-07 fase S5 (S5-02, RS-41, CA-S14) — registro de rutas Nexo
// con su capacidad declarada (internal/nexo/routes).
//
// Cómo agregar una ruta Nexo:
//
//  1. Registrarla con s.nexoRoute(mux, método, ruta, capacidad, handler)
//     desde una función register* de un archivo nexo_*.go (nunca con
//     mux.HandleFunc directo). La capacidad es una de authz (por ejemplo
//     "platform.users.manage"), una combinación ("a|b" cualquiera, "a+b"
//     todas) o un marcador: "public", "identity", "token.write"
//     (internal/nexo/routes). Una capacidad vacía o desconocida entra en
//     pánico al armar el servidor.
//  2. El handler sigue comprobando la capacidad real con authz (la
//     declaración no reemplaza la comprobación).
//  3. Si la respuesta es JSON, agregar su tipo a nexoResponseTypes
//     (nexo_response_fields_test.go).
//
// TestNexoRouteRegistryCoversMux (nexo_routes_test.go) recorre el mux y falla
// si una ruta /nexo/* o /dashboard/kb* no está en el registro, o si aparece
// una ruta de Engram que no está en testdata/nexo_engram_routes.txt (lista
// revisada: al integrar una versión nueva de Engram, revisar la ruta nueva
// y agregarla a la lista).

import "net/http"

// nexoRoute registra una ruta Nexo con su capacidad declarada.
func (s *CloudServer) nexoRoute(mux *http.ServeMux, method, path, capability string, h http.HandlerFunc) {
	s.nexo.routes.Handle(mux, method, path, capability, h)
}
