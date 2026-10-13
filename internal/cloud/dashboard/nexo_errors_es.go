package dashboard

// Nexo IA · NX-10 (panel, OP-06) — mensajes de error de texto plano del panel
// en español. Los handlers de Engram responden con http.Error y un mensaje en
// inglés (a veces con el error de la base de datos pegado); cloudserver
// (nexo_panel_errors.go) pasa por aquí el cuerpo de esas respuestas, solo con
// la marca de Nexo activa y solo en /dashboard/, y reemplaza lo que conoce.
// Lo que no conoce queda igual (un mensaje que ya está en español, por
// ejemplo). Un error interno (5xx) nunca muestra el detalle: el servidor lo
// deja en su log y la persona ve un texto genérico que dice qué hacer.
//
// TestNexoErrorTextCoversEngramMessages recorre los archivos de Engram y
// falla si un mensaje fijo de http.Error no tiene traducción.

import (
	"regexp"
	"strings"
)

// NexoInternalErrorText es el texto de cualquier error interno del panel.
const NexoInternalErrorText = "Ocurrió un error interno. Vuelve a intentarlo; si se repite, avisa al administrador."

const nexoErrFormText = "El formulario no es válido. Vuelve a abrir la página e inténtalo de nuevo."

// nexoErrorExact: mensajes completos (sin %v) de http.Error.
var nexoErrorExact = map[string]string{
	"forbidden":                          "No tienes permiso para esta acción. Si la necesitas, pide el rol a un administrador.",
	"invalid form":                       nexoErrFormText,
	"invalid form payload":               nexoErrFormText,
	"unable to create dashboard session": NexoInternalErrorText,
	"store error":                        NexoInternalErrorText,
	"invalid value for enabled: must be 'true' or 'false'": "El valor de «habilitado» debe ser true o false.",
	"username is required":                                 "Escribe el nombre de usuario.",
	"principal id is required":                             "Falta el identificador de la persona.",
	"principal id and project are required":                "Faltan la persona y el proyecto.",
	"token id is required":                                 "Falta el identificador del token.",
	"project is required":                                  "Falta el proyecto.",
	"managed user not found":                               "No encontramos a esa persona.",
	"managed token hasher is not configured":               NexoInternalErrorText,
	"principal not found":                                  "No encontramos a esa persona.",
	"authorization unavailable: internal error":            NexoInternalErrorText,
	// Nexo: reglas de plataforma y emisión de tokens (compartidas con la API).
	"the last enabled superadmin cannot be disabled (R-8)":                                          "No se puede deshabilitar al último superadmin habilitado (R-8). Nombra antes a otro superadmin.",
	"forbidden: nobody can grant themselves access to a project; ask another administrator (RS-12)": "Nadie puede darse acceso a sí mismo a un proyecto. Pide a otro administrador que lo haga (RS-12).",
	"scope must be read or write":                                                                   "El alcance del token debe ser lectura (read) o escritura (write).",
	"only service accounts can have tokens without expiration (expires_in_days=0)":                  "Solo las cuentas de servicio pueden tener tokens sin caducidad (días de vida = 0).",
	"only a superadmin can issue tokens without expiration":                                         "Solo un superadmin puede emitir tokens sin caducidad.",
}

type nexoErrorRule struct {
	re *regexp.Regexp
	es string // $1... del patrón
}

// nexoErrorPatterns: mensajes con datos (tamaños, nombres, capacidades).
var nexoErrorPatterns = []nexoErrorRule{
	{regexp.MustCompile(`^(?:login |bootstrap )?payload too large \(max (\d+) bytes\)$`), "El formulario es demasiado grande (máximo $1 bytes)."},
	{regexp.MustCompile(`^invalid_time_format: .*`), "La fecha no es válida. Usa el formato AAAA-MM-DD o RFC 3339."},
	{regexp.MustCompile(`^forbidden: (\S+) is required \(.*\)$`), "No tienes permiso para esta acción: hace falta la capacidad $1. Pide el rol o el permiso a un administrador, o entra con un token de escritura."},
	{regexp.MustCompile(`^managed user "(.*)" is disabled; enable the user before creating a managed token$`), "La persona $1 está deshabilitada. Habilítala antes de emitirle un token."},
	{regexp.MustCompile(`^expires_in_days must be between 1 and (\d+) \(0 = no expiration, service accounts only\)$`), "Los días de vida deben estar entre 1 y $1 (0 = sin caducidad, solo cuentas de servicio)."},
}

// nexoErrorPrefixes: "<operación>: <error de la base>". Con 400 se resume en
// una frase sin el detalle; los de 5xx son internos.
var nexoErrorPrefixes = []struct{ prefix, es string }{
	{"create user: ", "No se pudo crear el usuario. Revisa los datos e inténtalo de nuevo."},
	{"set user enabled: ", "No se pudo cambiar el estado de la persona."},
	{"create token: ", "No se pudo emitir el token. Revisa los datos e inténtalo de nuevo."},
	{"revoke token: ", "No se pudo revocar el token."},
	{"create grant: ", "No se pudo dar el permiso de proyecto. Revisa el proyecto e inténtalo de nuevo."},
	{"revoke grant: ", "No se pudo quitar el permiso de proyecto."},
}

// nexoErrorHints precisan un "<operación>: <error>" de 400 cuando el error
// de la base lo permite (en orden).
var nexoErrorHints = []struct{ contains, es string }{
	{"duplicate key", "Ya existe un registro con esos datos (por ejemplo, el mismo nombre de usuario o correo)."},
	{"unique constraint", "Ya existe un registro con esos datos (por ejemplo, el mismo nombre de usuario o correo)."},
	{"last active admin", "No se puede dejar la plataforma sin un administrador activo."},
	{"principal not found", "No encontramos a esa persona."},
	{"principal is disabled", "La persona está deshabilitada."},
	{"token not found", "No encontramos el token."},
}

// nexoErrorInternalPrefixes: operaciones cuyo fallo es del servidor.
var nexoErrorInternalPrefixes = []string{
	"record audit: ", "list users: ", "list tokens: ", "list grants: ", "generate token: ", "hash token: ", "render dashboard component: ",
}

// NexoErrorText devuelve el texto en español de un error de texto plano del
// panel. false si no lo conoce (el mensaje se deja como está). Con status 5xx
// y un mensaje de operación conocida devuelve el texto interno genérico.
func NexoErrorText(status int, msg string) (string, bool) {
	msg = strings.TrimSpace(msg)
	if es, ok := nexoErrorExact[msg]; ok {
		return es, true
	}
	for _, r := range nexoErrorPatterns {
		if m := r.re.FindStringSubmatchIndex(msg); m != nil {
			return string(r.re.ExpandString(nil, r.es, msg, m)), true
		}
	}
	for _, p := range nexoErrorInternalPrefixes {
		if strings.HasPrefix(msg, p) {
			return NexoInternalErrorText, true
		}
	}
	for _, p := range nexoErrorPrefixes {
		if !strings.HasPrefix(msg, p.prefix) {
			continue
		}
		if status >= 500 {
			return NexoInternalErrorText, true
		}
		low := strings.ToLower(msg)
		for _, h := range nexoErrorHints {
			if strings.Contains(low, h.contains) {
				return h.es, true
			}
		}
		return p.es, true
	}
	return "", false
}
