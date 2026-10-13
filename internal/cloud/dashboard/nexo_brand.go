package dashboard

// Nexo IA · NX-10 (panel) — marca "Nexo IA", textos en español y tema propio
// del panel. Engram queda igual mientras nadie llame a EnableNexoBrand: el
// servidor de Nexo lo activa al registrar las páginas de conocimiento
// (cloudserver/nexo_kb_dashboard.go). Las plantillas de Engram solo cambian en
// líneas marcadas que pasan sus textos por nx() o preguntan NexoBrand().

import "sync/atomic"

var nexoBrandOn atomic.Bool

// EnableNexoBrand activa la marca y el tema de Nexo IA en todo el panel.
func EnableNexoBrand() { nexoBrandOn.Store(true) }

// NexoBrand informa si la marca de Nexo IA está activa.
func NexoBrand() bool { return nexoBrandOn.Load() }

// nexoText traduce los textos visibles de Engram que el panel de Nexo muestra
// en el marco común, el menú, el login y el inicio.
var nexoText = map[string]string{
	// Marco común
	"Engram Cloud":               "Nexo IA",
	"An elephant never forgets.": "Memoria y estándares del equipo",
	"CLOUD ACTIVE":               "Conectado",
	"Logout":                     "Cerrar sesión",
	"shared memory index / local-first replication / org policy controls": "Memoria compartida de los asistentes y estándares oficiales del equipo",
	"ENGRAM CLOUD / SHARED MEMORY INDEX / LIVE SYNC READY":                "Nexo IA para Real Plaza. Basado en Engram Cloud (código abierto).",
	// Login
	"Login":   "Iniciar sesión",
	"Sign In": "Iniciar sesión",
	"Use your cloud runtime token to open a signed dashboard session.": "Ingresa el token de acceso que te asignó el administrador. El panel usa los mismos permisos que ese token.",
	"Cloud token":       "Token de acceso",
	"token":             "Pega aquí tu token",
	"token is required": "Ingresa tu token de acceso.",
	"invalid token":     "El token no es válido, venció o fue revocado. Pide uno nuevo al administrador.",
	// Menú
	"Dashboard":    "Inicio",
	"Browser":      "Memorias",
	"Projects":     "Proyectos",
	"Contributors": "Colaboradores",
	"Admin":        "Administración",
	// Inicio
	"Welcome to Engram Cloud": "Bienvenido a Nexo IA",
	"Your shared memory workspace is ready. Local SQLite stays authoritative, while cloud turns it into an index your team and agents can actually use.": "Aquí ves la memoria que comparten los asistentes del equipo y los estándares oficiales publicados en los espacios de conocimiento.",
	// Títulos de página
	"Stats":              "Estadísticas",
	"Activity":           "Actividad",
	"Session Detail":     "Sesión",
	"Project Detail":     "Proyecto",
	"Contributor Detail": "Colaborador",
	"Admin Projects":     "Proyectos (administración)",
	"Managed Users":      "Usuarios",
	"Admin Health":       "Salud del servidor",
	"Session":            "Sesión",
	"Observation":        "Memoria",
	"Observation Detail": "Memoria",
	"Prompt":             "Prompt",
	"Prompt Detail":      "Prompt",
	"Audit Log":          "Auditoría",
}

// nx devuelve el texto de Nexo si la marca está activa; si no, el original.
func nx(s string) string {
	if !NexoBrand() {
		return s
	}
	if t, ok := nexoText[s]; ok {
		return t
	}
	return s
}

// nexoLang es el idioma del documento.
func nexoLang() string {
	if NexoBrand() {
		return "es"
	}
	return "en"
}

// nexoTheme es el tema de Pico: claro con la marca de Nexo.
func nexoTheme() string {
	if NexoBrand() {
		return "light"
	}
	return "dark"
}

// nexoBodyClass agrega la clase del tema de Nexo al cuerpo.
func nexoBodyClass() string {
	if NexoBrand() {
		return "shell-body nexo"
	}
	return "shell-body"
}
