package evidence

import (
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
)

// NX-07: los detalles originales identifican el ámbito antes de redactar la salida.
func nexoRedactAlert(a Alert) Alert {
	a.NexoRememberScope(authz.NexoAlertScope(a.Details))
	redact.Struct(&a)
	return a
}
