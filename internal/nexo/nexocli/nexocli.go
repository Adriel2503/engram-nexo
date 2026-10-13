// Package nexocli despacha `engram nexo <área> ...` a las CLIs de los módulos
// Nexo: kb (NX-10, espacios de conocimiento) y audit (NX-07 S4, evidencia).
package nexocli

import (
	"fmt"
	"io"

	nexokbcli "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/cli"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/auditcli"
)

const usage = `Uso: engram nexo <área> <comando> [opciones]

Áreas:
  kb      espacios de conocimiento (spaces, publish, search, eval)
  audit   evidencia de seguridad (verify, export, verify-export)
`

// Run ejecuta `engram nexo <args>` y devuelve el código de salida.
func Run(args []string, dataDir string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "kb":
		return nexokbcli.Run(args, dataDir, stdout, stderr)
	case "audit":
		return auditcli.Run(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "área desconocida: %s\n\n%s", args[0], usage)
	return 2
}
