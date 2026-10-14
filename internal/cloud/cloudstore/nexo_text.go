package cloudstore

// NX-07: texto externo de personas y tokens. Usuario, correo, nombre y motivo
// de revocación los escribe quien administra o quien se registra; llegan a la
// auditoría, la actividad, la presencia, el historial, los eventos y la
// revisión de accesos. Se validan al escribir (un formato de credencial se
// rechaza) y se limpian al leer (filas históricas), con redact.Struct.

import (
	"errors"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
)

// ErrCredentialInText rechaza un texto con formato de credencial. No repite el valor.
var ErrCredentialInText = errors.New("cloudstore: el texto contiene un formato de credencial; quítalo")

// nexoCheckText devuelve ErrCredentialInText si algún texto trae un formato de credencial o una cadena de conexión.
func nexoCheckText(values ...string) error {
	for _, v := range values {
		if redact.Has(v) {
			return ErrCredentialInText
		}
	}
	return nil
}
