package cloudserver

// NX-07 · interruptor de cuentas de servicio (NEXO_SERVICE_ACCOUNTS_ENABLED).
//
// Apagado (el defecto en la beta) se cierran todos los caminos que crean o
// dan acceso a una cuenta de servicio:
//
//   - crear la cuenta (API, panel, CLI `nexo sa create`);
//   - habilitarla de nuevo (`:enable`, panel, CLI);
//   - emitir su token: POST /nexo/authz/v1/service-accounts/{sa}/tokens,
//     POST /nexo/authz/v1/tokens con una cuenta de servicio como principal, las
//     rutas de Engram /admin/users/{id}/tokens y el panel de usuarios;
//   - rotar su token (`:rotate`, panel, CLI `nexo sa token rotate`);
//   - autenticar con un token que ya existía (cloudstore.FindPrincipalTokenByHash
//     la trata como deshabilitada: falla cerrado).
//
// Consultar (listar y ver), revocar tokens y deshabilitar siguen permitidos
// para poder revisar y limpiar. Cada rechazo responde 403 con el código
// service_accounts_disabled, queda en cloud_auth_audit_log y no crea nada.
// Los servicios (authz y cloudstore) repiten la comprobación como respaldo.

import (
	"errors"
	"net/http"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/features"
)

const nexoAuditActionSADenied = "nexo.service_accounts.denied"

// nexoSADenied responde 403 service_accounts_disabled y audita el intento si el
// interruptor está apagado. Devuelve true si ya respondió.
func (s *CloudServer) nexoSADenied(w http.ResponseWriter, r *http.Request, respond nexoRespond, operation string) bool {
	if features.ServiceAccountsEnabled() {
		return false
	}
	actor, _ := PrincipalFromContext(r.Context())
	ev := adminAuditEvent(actor, nexoAuditActionSADenied, "", "", map[string]any{"operation": operation, "route": nexoRouteLabel(r)})
	ev.Outcome, ev.ReasonCode = authAuditOutcomeDenied, features.ServiceAccountsDisabledCode
	s.recordAuthAuditBestEffort(r.Context(), ev, "cuentas de servicio apagadas")
	respond(w, http.StatusForbidden, features.ServiceAccountsDisabledCode, features.ServiceAccountsDisabledMessage)
	return true
}

// nexoSAStoreDenied traduce el respaldo del almacén (cloudstore) al mismo 403.
func nexoSAStoreDenied(w http.ResponseWriter, respond nexoRespond, err error) bool {
	if !isServiceAccountsDisabled(err) {
		return false
	}
	respond(w, http.StatusForbidden, features.ServiceAccountsDisabledCode, features.ServiceAccountsDisabledMessage)
	return true
}

func isServiceAccountsDisabled(err error) bool {
	return errors.Is(err, cloudstore.ErrServiceAccountsDisabled)
}
