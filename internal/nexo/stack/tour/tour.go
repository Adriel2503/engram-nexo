// Package tour define un recorrido humano repetible, sin imprimir credenciales.
package tour

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/secrets"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/ui"
)

type Step struct {
	Title        string
	Instructions []string
}

func Script(server, mode, name string) []Step {
	suffix := " --mode " + mode
	if name != "" {
		suffix += " --name " + name
	}
	return []Step{{Title: "1. Tu propia memoria", Instructions: []string{"En tu asistente: guarda una memoria del proyecto nexo-stack-tour titulada Decisión de prueba (EJEMPLO), con el contenido Usar respuestas de error uniformes. Después búscala por ese título.", "Comprueba que el recuerdo pertenece a tu memoria local. Demo/dev por HTTP permiten conocimiento y panel; la sincronización cloud de memorias exige HTTPS."}}, {Title: "2. Buscar conocimiento y abrir la cita", Instructions: []string{"Pregunta al asistente: ¿Qué nivel de log uso para errores recuperables? Usa nexo_search y abre el enlace de la cita.", "Otra pregunta para comparar la búsqueda por significado: ¿Puedo escribir el password del usuario en el log? Sin embeddings, prueba también con las palabras logging y credenciales.", "Panel: " + server + "/dashboard/kb/"}}, {Title: "3. Proponer, aprobar y revisar la auditoría", Instructions: []string{"Copia el acceso proponente: engram nexo stack accesos" + suffix + " --copiar proponente. Entra al panel y propone Agregar jitter a la política de reintentos (EJEMPLO) en backend.", "Copia el acceso publicador con el mismo comando y --copiar publicador. Entra al panel, revisa y aprueba la propuesta.", "Copia el acceso auditor con --copiar auditor y abre la auditoría. Comprueba autor, aprobación y la ausencia de contenido de operaciones. El lector no debe poder leer operaciones.", "En dev/prod solicita esos roles al superadmin: esos modos no crean todas las personas del demo."}}}
}
func Seen(dir string) bool { _, e := os.Stat(filepath.Join(dir, "tour.done")); return e == nil }
func Run(ctx context.Context, u *ui.UI, out io.Writer, dir, server, mode, name string, example bool, connected ...bool) error {
	if !example {
		_, _ = fmt.Fprintln(out, "El ejemplo no está cargado. Puedes recorrer tus propios documentos; la propuesta requiere un espacio y los roles indicados.")
	}
	steps := Script(server, mode, name)
	if len(connected) > 0 && !connected[0] {
		suffix := " --mode " + mode
		if name != "" {
			suffix += " --name " + name
		}
		steps[2].Instructions = []string{"Propón como proponente: engram nexo tour" + suffix + " --action proponer", "Aprueba como publicador: engram nexo tour" + suffix + " --action aprobar --proposal-id <ID devuelto>", "Consulta como auditor: engram nexo tour" + suffix + " --action auditoria. Solo informa el número de eventos; abre el panel para revisar la redacción."}
		steps[0].Instructions = []string{"Guarda una memoria: engram save \"Decisión de prueba (EJEMPLO)\" \"Usar respuestas de error uniformes\" --project nexo-stack-tour", "Búscala: engram search \"Decisión de prueba\" --project nexo-stack-tour"}
		steps[1].Instructions = []string{"Conecta la CLI: copia el token publicador con stack accesos --copiar publicador y usa engram nexo connect --server " + server + " --token-stdin (sin imprimirlo).", "Pregunta: engram nexo kb search \"¿Qué nivel de log uso para errores recuperables?\"", "Abre el enlace de la cita devuelta por la CLI. Panel: " + server + "/dashboard/kb/"}
	}
	for _, s := range steps {
		if e := ctx.Err(); e != nil {
			return e
		}
		_, _ = fmt.Fprintln(out, s.Title)
		for _, line := range s.Instructions {
			_, _ = fmt.Fprintln(out, line)
		}
		ok, e := u.Confirm("¿Completaste este paso?", false)
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("recorrido pausado; repite engram nexo tour cuando estés listo")
		}
	}
	if e := secrets.Write(filepath.Join(dir, "tour.done"), []byte("Recorrido completado\n")); e != nil {
		return e
	}
	_, _ = fmt.Fprintln(out, "Recorrido completado. Puedes repetirlo con engram nexo tour.")
	return nil
}
