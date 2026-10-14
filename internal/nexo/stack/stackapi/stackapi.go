// Package stackapi fija el contrato entre el núcleo de `engram nexo stack`
// (internal/nexo/stack) y la carga de datos (internal/nexo/stack/seed).
// Es solo tipos e interfaz: ninguna de las dos piezas importa a la otra.
package stackapi

import "context"

// Persona es una persona de prueba del entorno demo. El núcleo la crea y
// canjea su código de activación; la carga de datos le asigna roles de espacio.
type Persona struct {
	Username   string // p. ej. "publicador"
	Email      string // <usuario>@demo.local
	Plataforma string // "superadmin", "admin", "auditor" o "" (miembro)
}

// Env es lo que la carga de datos necesita para hablar con el servidor.
type Env struct {
	ServerURL string            // p. ej. http://127.0.0.1:18480
	Tokens    map[string]string // usuario -> token; solo en memoria, nunca se registra
	Logf      func(format string, args ...any)
}

// LoadOptions controla la carga del ejemplo.
type LoadOptions struct {
	Example bool // cargar la semilla de ejemplo
}

// OwnOptions controla la carga de contenido propio.
type OwnOptions struct {
	Dir          string // carpeta con los archivos
	PerSubfolder bool   // un espacio por subcarpeta (si no, un solo espacio)
	SpacePrefix  string // prefijo opcional del slug
}

// Report resume lo que hizo una operación. Idempotente: repetirla informa
// "sin cambios" en lugar de duplicar.
type Report struct {
	Spaces    int
	Documents int
	Memories  int
	Proposals int
	Skipped   []string // elementos omitidos, con el motivo (sin contenido ni credenciales)
	Notes     []string
}

// Seeder lo implementa internal/nexo/stack/seed y lo consume el núcleo.
type Seeder interface {
	// Personas devuelve las personas de la demo (el núcleo las crea).
	Personas() []Persona
	// Load crea espacios, asignaciones de rol, documentos, memorias y propuestas de ejemplo.
	Load(ctx context.Context, env Env, opts LoadOptions) (Report, error)
	// RemoveExample retira solo lo que Load creó (marcado como ejemplo).
	RemoveExample(ctx context.Context, env Env) (Report, error)
	// LoadOwn sube el contenido de una carpeta del usuario.
	LoadOwn(ctx context.Context, env Env, opts OwnOptions) (Report, error)
}
