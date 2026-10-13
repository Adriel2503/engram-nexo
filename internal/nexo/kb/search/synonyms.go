package search

// Nexo IA · NX-10 entrega 2b (RC-06) — sinónimos y siglas. Dos fuentes:
//
//   - un diccionario base de términos técnicos español/inglés (BaseSynonyms);
//   - el glosario de cada espacio (documentos de categoría "glosario"): las
//     líneas "TÉRMINO (SIGLA): definición", "SIGLA: Nombre completo",
//     "A = B" o "A / B" producen equivalencias (ParseGlossary).
//
// La expansión solo se usa en la segunda pasada (cualquier término): nunca
// estrecha una búsqueda que ya encontraba todas las palabras.

import (
	"regexp"
	"sort"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
)

// BaseSynonyms son grupos de términos equivalentes (minúsculas, sin tildes).
var BaseSynonyms = [][]string{
	{"formulario", "form"},
	{"prueba", "test", "testing"},
	{"pruebas unitarias", "unit test", "unit tests", "test unitario", "tests unitarios"},
	{"prueba de carga", "load test", "pruebas de carga"},
	{"registro", "log", "logging", "logs", "bitacora"},
	{"base de datos", "database", "bd", "bbdd", "db"},
	{"tabla", "table"},
	{"consulta", "query", "queries"},
	{"procedimiento almacenado", "stored procedure", "sp", "procedure"},
	{"funcion", "function"},
	{"llave primaria", "clave primaria", "primary key", "pk"},
	{"llave foranea", "clave foranea", "foreign key", "fk"},
	{"indice", "index"},
	{"esquema", "schema"},
	{"migracion", "migration"},
	{"cache", "caching", "redis"},
	{"servicio", "service"},
	{"microservicio", "microservice", "microservicios", "microservices"},
	{"microfrontend", "micro frontend", "mfe"},
	{"punto de acceso", "endpoint", "endpoints"},
	{"ruta", "route", "routing", "ruteo"},
	{"despliegue", "deploy", "deployment", "pipeline"},
	{"cobertura", "coverage"},
	{"contrasena", "password"},
	{"secreto", "secret", "secrets"},
	{"variable de entorno", "environment variable", "env var", "variables de entorno"},
	{"contenedor", "container", "docker"},
	{"repositorio", "repository", "repo"},
	{"validacion", "validation", "validator", "validators", "validador"},
	{"excepcion", "exception", "error"},
	{"autenticacion", "authentication", "auth", "login"},
	{"autorizacion", "authorization", "permisos", "permissions"},
	{"certificado", "certificate", "ssl", "tls"},
	{"monitoreo", "monitoring", "observabilidad", "observability"},
	{"alerta", "alert", "alertas", "alerts"},
	{"evento", "event", "eventos", "events"},
	{"cola", "queue", "pubsub", "pub/sub"},
	{"manejador", "handler", "handlers"},
	{"memoizacion", "memoization", "memo", "usememo", "usecallback"},
	{"gancho", "hook", "hooks"},
	{"componente", "component", "components"},
	{"buenas practicas", "best practices", "lineamientos"},
	{"nomenclatura", "naming", "convencion de nombres", "naming convention"},
	{"reintento", "retry", "reintentos", "retries", "polly"},
	{"almacenamiento", "storage", "bucket"},
	{"api", "rest", "web api"},
	{"ssr", "server side rendering", "renderizado en el servidor"},
	{"ci", "integracion continua", "continuous integration"},
	{"cd", "entrega continua", "continuous delivery"},
}

// Synonyms es un diccionario término -> equivalentes.
type Synonyms map[string][]string

// NewSynonyms arma el diccionario a partir de grupos.
func NewSynonyms(groups ...[][]string) Synonyms {
	s := Synonyms{}
	for _, gs := range groups {
		for _, g := range gs {
			s.AddGroup(g)
		}
	}
	return s
}

// AddGroup agrega un grupo de equivalentes.
func (s Synonyms) AddGroup(g []string) {
	norm := make([]string, 0, len(g))
	for _, t := range g {
		if t = normTerm(t); t != "" && len(t) <= 60 {
			norm = append(norm, t)
		}
	}
	for _, a := range norm {
		for _, b := range norm {
			if a != b && !contains(s[a], b) {
				s[a] = append(s[a], b)
			}
		}
	}
}

func contains(l []string, x string) bool {
	for _, v := range l {
		if v == x {
			return true
		}
	}
	return false
}

var termCleanRe = regexp.MustCompile(`[^a-z0-9/ ]+`)

func normTerm(t string) string {
	t = strings.ToLower(ingest.Fold(strings.TrimSpace(t)))
	t = termCleanRe.ReplaceAllString(t, " ")
	return strings.Join(strings.Fields(t), " ")
}

// ExpandSynonyms devuelve la expansión de la consulta como texto websearch
// ("a or \"b c\" or d"), o "" si ningún término tiene sinónimos.
func ExpandSynonyms(query string, syn map[string][]string) string {
	if len(syn) == 0 {
		return ""
	}
	q := " " + normTerm(query) + " "
	keys := make([]string, 0, len(syn))
	for k := range syn {
		keys = append(keys, k)
	}
	// Frases largas primero (base de datos antes que base).
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	var alts []string
	seen := map[string]bool{}
	for _, k := range keys {
		if !strings.Contains(q, " "+k+" ") {
			continue
		}
		for _, a := range syn[k] {
			if seen[a] || strings.Contains(q, " "+a+" ") {
				continue
			}
			seen[a] = true
			if strings.Contains(a, " ") {
				alts = append(alts, `"`+a+`"`)
			} else {
				alts = append(alts, a)
			}
			if len(alts) >= 24 {
				break
			}
		}
	}
	return strings.Join(alts, " or ")
}

var (
	glossAcronymRe = regexp.MustCompile(`^\s*[-*]?\s*\**([^:=(|]{2,60}?)\**\s*\(([A-Za-z0-9/.\- ]{2,20})\)\s*[:\-–]`)
	glossDefRe     = regexp.MustCompile(`^\s*[-*]?\s*\**([A-Z0-9][A-Z0-9/.\-]{1,15})\**\s*[:\-–]\s*([^.;:]{3,60})`)
	glossEqRe      = regexp.MustCompile(`^\s*[-*]?\s*\**([^:=/|]{2,40}?)\**\s*(?:=|/)\s*\**([^:=/|]{2,40}?)\**\s*$`)
)

// ParseGlossary extrae equivalencias de un documento de glosario.
func ParseGlossary(text string) [][]string {
	var out [][]string
	for _, line := range strings.Split(text, "\n") {
		if m := glossAcronymRe.FindStringSubmatch(line); m != nil {
			out = append(out, []string{m[1], m[2]})
			continue
		}
		if m := glossDefRe.FindStringSubmatch(line); m != nil {
			// "SSR: Server Side Rendering, renderizado..." -> la primera frase.
			def := strings.Split(m[2], ",")[0]
			if words := len(strings.Fields(def)); words >= 1 && words <= 6 {
				out = append(out, []string{m[1], def})
			}
			continue
		}
		if m := glossEqRe.FindStringSubmatch(line); m != nil {
			out = append(out, []string{m[1], m[2]})
		}
	}
	return out
}
