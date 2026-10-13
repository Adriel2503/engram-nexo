package dashboard

// Nexo IA · NX-10 (panel, OP-06) — textos en inglés que salen de código Go de
// Engram (no de plantillas): páginas de error y de "no encontrado", títulos de
// contexto, el "Never" de las fechas. Los usa nx() igual que nexoTextPanel.
// TestNexoGoTextsAreTranslated recorre esos archivos y falla si una cadena
// fija no tiene traducción.

import "strings"

var nexoTextGo = map[string]string{
	// Páginas de "no encontrado" y de error (dashboard.go, classifyStoreError)
	"Session Not Found":      "Sesión no encontrada",
	"Project Not Found":      "Proyecto no encontrado",
	"Contributor Not Found":  "Colaborador no encontrado",
	"Observation Not Found":  "Memoria no encontrada",
	"Managed User Not Found": "Persona no encontrada",
	"Project not found":      "Proyecto no encontrado",
	"Contributor not found":  "Colaborador no encontrado",
	"Session not found":      "Sesión no encontrada",
	"Observation not found":  "Memoria no encontrada",
	"Prompt not found":       "Prompt no encontrado",
	"Project access denied":  "Sin acceso al proyecto",
	"Service Unavailable":    "Servicio no disponible",
	"Cannot Create Token":    "No se puede crear el token",
	"Token Created":          "Token creado",
	"Managed User":           "Persona administrada",
	"No dashboard data exists for that session identifier.":                "No hay datos del panel para esa sesión.",
	"No replicated dashboard data exists for that project.":                "No hay datos replicados en el panel para ese proyecto.",
	"No dashboard data exists for that contributor.":                       "No hay datos del panel para ese colaborador.",
	"No managed user exists with that principal id.":                       "No existe una persona con ese identificador.",
	"No contributor with that name has been seen in this cloud workspace.": "Este servidor no ha visto a ningún colaborador con ese nombre.",
	"No session with that identifier exists in the specified project.":     "No hay una sesión con ese identificador en el proyecto indicado.",
	"No observation with that identifier exists in the specified session.": "No hay una memoria con ese identificador en la sesión indicada.",
	"No prompt with that identifier exists in the specified session.":      "No hay un prompt con ese identificador en la sesión indicada.",
	"You are not allowed to access that project scope.":                    "No tienes acceso a ese proyecto.",
	"Invalid session identifier.":                                          "El identificador de la sesión no es válido.",
	"Invalid observation identifier.":                                      "El identificador de la memoria no es válido.",
	"Invalid prompt identifier.":                                           "El identificador del prompt no es válido.",
	"Dashboard data is temporarily unavailable.":                           "Los datos del panel no están disponibles por ahora. Vuelve a intentarlo en unos minutos.",
	"Cloud dashboard data is temporarily unavailable.":                     "Los datos del panel no están disponibles por ahora. Vuelve a intentarlo en unos minutos.",
	"Managed user data is temporarily unavailable.":                        "Los datos de las personas no están disponibles por ahora. Vuelve a intentarlo en unos minutos.",
	"Audit log data is temporarily unavailable.":                           "El registro de auditoría no está disponible por ahora. Vuelve a intentarlo en unos minutos.",
	"Enable this managed user before creating a managed token.":            "Habilita a esta persona antes de crearle un token.",
	"DEGRADED":             "Con problemas",
	"No content captured.": "Sin contenido registrado.",

	// Contexto de las páginas de error (renderStoreError): títulos y "<x> unavailable"
	"Dashboard data":       "Datos del panel",
	"Stats":                "Estadísticas",
	"Activity":             "Actividad",
	"Observations":         "Memorias",
	"Sessions":             "Sesiones",
	"Prompts":              "Prompts",
	"Project detail":       "Detalle del proyecto",
	"Contributor detail":   "Detalle del colaborador",
	"Admin":                "Administración",
	"Project controls":     "Controles del proyecto",
	"Project observations": "Memorias del proyecto",
	"Project sessions":     "Sesiones del proyecto",
	"Project prompts":      "Prompts del proyecto",
	"Session detail":       "Detalle de la sesión",
	"Observation detail":   "Detalle de la memoria",
	"Prompt detail":        "Detalle del prompt",
}

// nexoTextPatterns traduce textos de Go que llevan datos pegados.
func nexoTextPattern(s string) (string, bool) {
	// "<contexto> unavailable" (classifyStoreError)
	if base, ok := strings.CutSuffix(s, " unavailable"); ok {
		if t, found := nexoLookup(base); found {
			return t + " no disponible", true
		}
	}
	// "<aviso>. User <nombre> is currently disabled." (token de una persona deshabilitada)
	if i := strings.Index(s, " User "); i > 0 && strings.HasSuffix(s, " is currently disabled.") {
		if head, found := nexoLookup(s[:i]); found {
			user := strings.TrimSuffix(s[i+len(" User "):], " is currently disabled.")
			return head + " La persona " + user + " está deshabilitada.", true
		}
	}
	return "", false
}
