package cloudserver

// Nexo IA · NX-07 fase S5 (S5-01, RS-40) — respuestas sin detalles internos.
//
// Un error interno (de la base, del motor de roles o del generador de
// tokens) puede traer SQL, nombres de tablas o columnas y valores de la
// consulta. Las rutas Nexo responden 500 con un mensaje genérico y el detalle
// queda solo en el log del servidor (CWE-209). Los tipos de respuesta se
// revisan en nexo_response_fields_test.go y las respuestas reales en
// TestNexoResponsesHaveNoSecrets.

import (
	"fmt"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"log"
	"net/http"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/constants"
)

// nexoInternalError registra el error y responde 500 sin su detalle.
func nexoInternalError(w http.ResponseWriter, what string, err error) {
	nexoLogInternal(what, err)
	writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, what+": internal error")
}

// nexoLogInternal deja el detalle de un error interno solo en el log del
// servidor. Lo usan nexoInternalError (API JSON) y las páginas del panel, que
// responden 500 con un texto genérico en español y nunca muestran el error.
func nexoLogInternal(what string, err error) {
	log.Printf("[nexo] %s: %s", what, security.NexoRedactAuditText(fmt.Sprint(err)))
}
