package dashboard

// Nexo IA · NX-04 — el login del panel valida el token con la petición a mano,
// para que un rechazo quede auditado con la IP y el cliente de origen (CA-24).
// Si el servidor no configura la variante con petición, se usa
// ValidateLoginToken de Engram tal cual.

import "net/http"

func nexoValidateLoginToken(cfg MountConfig, r *http.Request, token string) error {
	if cfg.NexoValidateLoginTokenRequest != nil {
		return cfg.NexoValidateLoginTokenRequest(r, token)
	}
	return cfg.ValidateLoginToken(token)
}
