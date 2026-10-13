package dashboard

import "strconv"

// Nexo IA · NX-10 (panel) — datos de la pantalla "Estado de la búsqueda"
// (nexo_search_status.templ). Los arma cloudserver (nexo_search_status.go) con
// el servicio de conocimiento; aquí solo hay texto ya formateado, que templ
// escapa, y ningún tipo de Nexo. Diseño: docs/nexo/panel/design.md §11.

// Ruta de la pantalla y pestaña activa del menú.
const (
	NexoSearchStatusPath = "/dashboard/kb/search-status"
	NexoSearchStatusTab  = "search-status"
)

// Comando que sugiere la pantalla cuando hay cartas muertas.
const NexoRequeueDeadCommand = "engram nexo kb reindex --vectors --requeue-dead"

// NexoSearchRow es una fila de etiqueta y valor. Chip (opcional) marca el
// estado con una de las clases nx-chip-*; Note es una línea de contexto.
type NexoSearchRow struct {
	Label, Value string
	Chip         string // clase nx-chip-ok, nx-chip-exp, nx-chip-err, nx-chip-ret
	Note         string
	Env          string // variable de entorno que lo fija, si la hay
}

// NexoSearchVerdict es la frase principal de la pantalla. Class: ok, warn o
// info.
type NexoSearchVerdict struct {
	Class, Title, Detail string
}

// NexoSearchVersion es una versión del índice vectorial.
type NexoSearchVersion struct {
	Version, Status, StatusChip string
	Model, Provider             string
	Dims                        string
	Created, Activated, Retired string
}

// NexoSearchProgress es el avance de una versión activa o en construcción.
type NexoSearchProgress struct {
	Label        string
	Short        string // "Versión 2 (activa)", para la barra
	Done, Total  int64
	Percent      int
	DoneText     string // "1 240 de 3 100 secciones con vector (40 %)"
	Facts        []NexoSearchRow
	ProgressText string // texto alternativo de la barra
}

// NexoSearchDeadError es una carta muerta reciente (solo el mensaje).
type NexoSearchDeadError struct {
	When, Version, Attempts, Message string
}

// NexoSearchDead agrupa las cartas muertas.
type NexoSearchDead struct {
	Total   int64
	Summary string
	Errors  []NexoSearchDeadError
}

// NexoSearchEval es una corrida de evaluación.
type NexoSearchEval struct {
	When, Retriever, Mode, Set string
	Hit5, MRR, NoAnswer        string
	Questions                  string
	Accepted                   bool
}

// NexoSearchParamGroup agrupa parámetros en uso.
type NexoSearchParamGroup struct {
	Title string
	Rows  []NexoSearchRow
}

// NexoSearchRerank es el bloque "Reordenamiento (R3)". Mientras R3 no exista
// o no esté configurado, Configured es false y la pantalla dice "no
// configurado". Para completarlo basta llenar este bloque en cloudserver
// (searchStatusRerank) con filas de etiqueta y valor; la plantilla
// (nexoSearchRerank) ya las muestra.
type NexoSearchRerank struct {
	Configured bool
	Note       string
	Rows       []NexoSearchRow
}

// NexoSearchStatusPage es /dashboard/kb/search-status.
type NexoSearchStatusPage struct {
	Verdict  NexoSearchVerdict
	Config   []NexoSearchRow
	Versions []NexoSearchVersion
	Progress []NexoSearchProgress
	Dead     NexoSearchDead
	Evals    []NexoSearchEval
	Params   []NexoSearchParamGroup
	Rerank   NexoSearchRerank
}

// Value y Max son los atributos de la barra de avance.
func (p NexoSearchProgress) Value() string { return strconv.FormatInt(p.Done, 10) }

// Max es el total de la barra de avance.
func (p NexoSearchProgress) Max() string { return strconv.FormatInt(p.Total, 10) }
