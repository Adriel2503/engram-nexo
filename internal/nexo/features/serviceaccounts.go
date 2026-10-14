// Package features reúne los interruptores de funciones de Nexo que se leen
// del entorno. Es un paquete hoja (sin dependencias de Nexo) para que el motor
// de roles, el almacén y el servidor lo usen sin ciclos de importación.
package features

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ServiceAccountsEnv activa las cuentas de servicio. Por defecto están
// apagadas durante la beta: no hay automatizaciones previstas y apagarlas
// elimina el riesgo de que un administrador emita tokens de una cuenta con
// roles de contenido (R2, SG-17).
const ServiceAccountsEnv = "NEXO_SERVICE_ACCOUNTS_ENABLED"

// ErrServiceAccountsEnvInvalid es el error de un valor no reconocido. Nombra la
// variable y nunca incluye el valor recibido.
var ErrServiceAccountsEnvInvalid = fmt.Errorf("%s no es válido: usa true, false, 1 o 0 (vacío equivale a false); corrígela o quítala", ServiceAccountsEnv)

// ErrServiceAccountsDisabled lo devuelven el motor de roles y el almacén cuando
// se intenta crear, habilitar o emitir tokens de una cuenta de servicio con el
// interruptor apagado. El texto empieza por el código público.
var ErrServiceAccountsDisabled = errors.New(ServiceAccountsDisabledCode + ": " + ServiceAccountsDisabledMessageEN)

// Código y mensajes públicos del rechazo.
const (
	ServiceAccountsDisabledCode      = "service_accounts_disabled"
	ServiceAccountsDisabledMessageEN = "service accounts are disabled on this server (NEXO_SERVICE_ACCOUNTS_ENABLED=false)"
	// ServiceAccountsDisabledMessage explica en español cómo activarlas.
	ServiceAccountsDisabledMessage = "Las cuentas de servicio están desactivadas en este servidor (beta). Para activarlas, quien administra el servidor debe definir NEXO_SERVICE_ACCOUNTS_ENABLED=true y reiniciarlo. Mientras tanto se pueden consultar, revocar sus tokens y deshabilitarlas."
)

// ParseServiceAccountsEnabled interpreta el valor: true/1 activa; false/0 o
// vacío apaga; cualquier otro es un error (nunca se asume un valor).
func ParseServiceAccountsEnabled(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "false", "0":
		return false, nil
	case "true", "1":
		return true, nil
	}
	return false, ErrServiceAccountsEnvInvalid
}

// ServiceAccountsFromEnv lee NEXO_SERVICE_ACCOUNTS_ENABLED. Se valida al arrancar
// el servidor: un valor inválido impide arrancar.
func ServiceAccountsFromEnv() (bool, error) {
	return ParseServiceAccountsEnabled(os.Getenv(ServiceAccountsEnv))
}

// ServiceAccountsEnabled indica si las cuentas de servicio están activas.
// Falla cerrado: un valor inválido cuenta como apagado.
func ServiceAccountsEnabled() bool {
	on, err := ServiceAccountsFromEnv()
	return err == nil && on
}
