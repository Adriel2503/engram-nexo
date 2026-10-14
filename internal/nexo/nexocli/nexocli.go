// Package nexocli despacha `engram nexo <área> ...` a las CLIs de los módulos
// Nexo: kb (NX-10, espacios de conocimiento), audit (NX-07 S4, evidencia) y,
// desde la tanda 2 de NX-10, la conexión de la PC (connect, status,
// disconnect) y la instalación del protocolo Nexo en un asistente (setup).
// Desde SG-10, sa: cuentas de servicio para CI y bots (serviceaccounts.go).
package nexocli

import (
	"fmt"
	"io"

	nexokbcli "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/cli"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/auditcli"
)

const usage = `Uso: engram nexo <comando> [opciones]

Conexión de esta PC:
  connect --server URL [--token-stdin | --token T]   Valida el token, guarda la conexión y sincroniza la copia local
  status [--json]                                    Conexión, identidad, servidor y copia local
  disconnect [--keep-copy]                           Quita la conexión (y la copia local)
  setup <agente>                                     Instala el protocolo Nexo en un asistente

Áreas:
  kb      espacios de conocimiento (spaces, search, get, sync, upload, publish, changes, eval, reindex, import-transversal)
  audit   evidencia de seguridad (verify, export, verify-export, verify-review)
  sa      cuentas de servicio para CI y bots (list, show, create, update, disable, enable, grant, ungrant, token)
  authz   roles de plataforma (reconcile-roles: detecta y corrige la columna de Engram desalineada del rol de Nexo)

Instalación para un dev: engram nexo connect --server <url> --token-stdin  y luego  engram setup <agente>.
El token también se puede pasar en NEXO_TOKEN (evita dejarlo en el historial de la terminal).
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
	case "authz": // NX-05: roles de plataforma (reconcile-roles)
		return runAuthz(args[1:], stdout, stderr)
	case "sa", "service-accounts": // SG-10
		return runServiceAccounts(args[1:], dataDir, stdout, stderr)
	case "connect", "status", "disconnect", "setup":
		return runConnection(args[0], args[1:], dataDir, stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "área desconocida: %s\n\n%s", args[0], usage)
	return 2
}
