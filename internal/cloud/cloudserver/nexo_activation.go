package cloudserver

// NX-07: una persona recibe su token al canjear el código; el administrador
// solo recibe la credencial de entrega (no funciona como Bearer).
import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/dashboard"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

type nexoActivationStore interface {
	NexoIssueActivation(context.Context, string, string, string, string, cloudstore.AuthAuditEvent) (cloudstore.NexoActivation, error)
	NexoPendingActivations(context.Context, string) ([]cloudstore.NexoActivation, error)
	NexoRevokeActivation(context.Context, string, string, cloudstore.AuthAuditEvent) (bool, error)
	NexoRedeemActivation(context.Context, string, cloudstore.CreatePrincipalTokenParams, cloudstore.NexoRedeemCaller) (cloudstore.PrincipalToken, error)
}
type nexoActivationIssued struct {
	CredentialType string                    `json:"credential_type"`
	ActivationCode string                    `json:"activation_code"`
	Activation     cloudstore.NexoActivation `json:"activation"`
}
type nexoActivationRevoked struct {
	Changed bool `json:"changed"`
}
type nexoActivationRedeemed struct {
	RawToken string        `json:"raw_token"`
	Token    nexoTokenView `json:"token"`
}

func (s *CloudServer) nexoActivations() nexoActivationStore {
	for _, v := range []any{s.store, s.adminIdentity} {
		if a, ok := v.(nexoActivationStore); ok {
			return a
		}
	}
	return nil
}
func nexoPrivateCredentialResponse(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}
func (s *CloudServer) registerNexoActivationRoutes(mux *http.ServeMux) {
	s.nexoRoute(mux, "POST", "/nexo/authz/v1/activate", "public", s.handleNexoActivate)
	s.nexoRoute(mux, "GET", "/nexo/authz/v1/activations", "platform.tokens.manage", s.nexoAuthzRoute(s.handleNexoPendingActivations))
	s.nexoRoute(mux, "POST", "/nexo/authz/v1/activations/{id}/revoke", "platform.tokens.manage", s.nexoAuthzRoute(s.handleNexoRevokeActivation))
	s.nexoRoute(mux, "GET", "/dashboard/activate", "public", s.handleNexoActivationPage)
	s.nexoRoute(mux, "POST", "/dashboard/activate", "public", s.handleNexoActivationPage)
	s.nexoRoute(mux, "POST", "/dashboard/admin/users/{principalID}/activations/{id}/revoke", "platform.tokens.manage",
		s.requireDashboardSession(s.nexoUsersPanel(s.nexoPanelRevokeActivation)))
}

// nexoMaybeActivation es el enganche mínimo de las dos rutas de Engram.
// true: respondió (también ante errores); false: autoemisión o cuenta de servicio.
func (s *CloudServer) nexoMaybeActivation(w http.ResponseWriter, r *http.Request, ctx context.Context, target, name string, panel bool) bool {
	// El nombre se valida antes de cualquier excepción (autoemisión, cuenta de servicio).
	if _, err := security.NexoCheckTokenName(name); err != nil {
		if panel {
			http.Error(w, "El nombre no es válido: usa hasta 256 caracteres y no incluyas credenciales.", http.StatusBadRequest)
		} else {
			nexoAuthzBadRequest(w, "El nombre no es válido: usa hasta 256 caracteres y no incluyas credenciales.")
		}
		return true
	}
	svc, available := s.nexoAuthz()
	if !available {
		// Falla cerrado: sin el motor de roles de Nexo no se puede emitir el código de activación, así
		// que a otra persona no se entrega ningún token (NX-07). Las rutas de Engram solo emiten a
		// personas; la autoemisión (quien actúa es el destino) no depende de Nexo y se mantiene.
		if actor, _ := PrincipalFromContext(r.Context()); nexoSamePrincipal(actor.ID, target) {
			nexoPrivateCredentialResponse(w)
			return false
		}
		nexoLogInternal("activación", errors.New("motor de roles de Nexo no disponible"))
		if panel {
			http.Error(w, "Nexo no está disponible: no se emitió ninguna credencial.", http.StatusServiceUnavailable)
		} else {
			writeActionableError(w, http.StatusServiceUnavailable, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, "nexo authz store is not available: no credential was issued")
		}
		return true
	}
	actor, _ := PrincipalFromContext(r.Context())
	row, err := svc.ResolvePrincipal(ctx, target)
	if err != nil {
		nexoAuthzWriteError(w, err)
		return true
	}
	if row.Kind == authz.KindServiceAccount { // NX-07: con las cuentas de servicio apagadas no se emite ningún token para ellas
		respond := nexoRespond(nexoRespondJSON)
		if panel {
			respond = nexoRespondText
		}
		if s.nexoSADenied(w, r, respond, "token") {
			return true
		}
	}
	if row.Kind == authz.KindServiceAccount || nexoSamePrincipal(actor.ID, row.ID) {
		nexoPrivateCredentialResponse(w)
		return false
	}
	a := s.nexoActivations()
	if a == nil || s.managedHasher == nil {
		nexoInternalError(w, "activación", errors.New("almacén no disponible"))
		return true
	}
	raw, err := security.GenerateActivationCode()
	if err != nil {
		nexoInternalError(w, "activación", fmt.Errorf("emisión destinatario=%s: generar código: %w", row.ID, err))
		return true
	}
	hash, err := s.managedHasher.Hash("nexo-activation:v1:" + raw)
	if err != nil {
		nexoInternalError(w, "activación", fmt.Errorf("emisión destinatario=%s: hash: %w", row.ID, err))
		return true
	}
	code, err := a.NexoIssueActivation(ctx, row.ID, hash, name, actor.TokenID, adminAuditEvent(actor, "nexo.activation.issued", row.ID, "", nil))
	if err != nil {
		if errors.Is(err, cloudstore.ErrPrincipalDisabled) {
			nexoRespondJSON(w, 409, "principal_disabled", "La persona está deshabilitada.")
			return true
		}
		// La causa real (SQLSTATE incluido) va al registro redactado; la respuesta sigue siendo genérica.
		nexoInternalError(w, "activación", fmt.Errorf("emisión destinatario=%s: %w", row.ID, err))
		return true
	}
	nexoPrivateCredentialResponse(w)
	if panel {
		renderDashboardAdminComponent(w, r, s.dashboardAdminLayout(r, "Código de activación", dashboard.NexoActivationIssuedPage(raw, code)))
	} else {
		jsonResponse(w, http.StatusCreated, nexoActivationIssued{CredentialType: "activation_code", ActivationCode: raw, Activation: code})
	}
	return true
}

func (s *CloudServer) nexoActivationTarget(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity, ref string) (string, bool) {
	d, err := svc.Engine().Require(r.Context(), id, authz.PlatformScope(), authz.CapPlatformTokensManage, map[string]any{"route": r.Pattern})
	if err != nil {
		nexoAuthzWriteError(w, err)
		return "", false
	}
	if !d.Allowed {
		nexoRespondDenied(w, nexoRespondJSON, authz.CapPlatformTokensManage, d)
		return "", false
	}
	target, err := svc.ResolvePrincipal(r.Context(), ref)
	if err != nil {
		nexoAuthzWriteError(w, err)
		return "", false
	}
	if !s.nexoAllowManageTargetTokens(w, r, target.ID, nexoRespondJSON) {
		return "", false
	}
	return target.ID, true
}
func (s *CloudServer) handleNexoPendingActivations(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	target, ok := s.nexoActivationTarget(w, r, svc, id, r.URL.Query().Get("principal"))
	if !ok {
		return
	}
	a := s.nexoActivations()
	if a == nil {
		nexoInternalError(w, "activación", errors.New("almacén no disponible"))
		return
	}
	items, err := a.NexoPendingActivations(r.Context(), target)
	if err != nil {
		nexoInternalError(w, "activación", fmt.Errorf("consulta de pendientes destinatario=%s: %w", target, err))
		return
	}
	jsonResponse(w, 200, newNexoPage(items, "", len(items)))
}
func (s *CloudServer) handleNexoRevokeActivation(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	var body struct {
		Principal string `json:"principal"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		nexoAuthzBadRequest(w, "Solicitud no válida.")
		return
	}
	target, ok := s.nexoActivationTarget(w, r, svc, id, body.Principal)
	if !ok {
		return
	}
	s.nexoRevokeActivation(w, r, target, false)
}
func (s *CloudServer) nexoPanelRevokeActivation(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	target, ok := s.nexoActivationTarget(w, r, svc, id, r.PathValue("principalID"))
	if !ok {
		return
	}
	s.nexoRevokeActivation(w, r, target, true)
}
func (s *CloudServer) nexoRevokeActivation(w http.ResponseWriter, r *http.Request, target string, panel bool) {
	codeID := r.PathValue("id")
	if n, err := strconv.ParseInt(codeID, 10, 64); err != nil || n <= 0 {
		nexoAuthzBadRequest(w, "Identificador no válido.")
		return
	}
	a := s.nexoActivations()
	if a == nil {
		nexoInternalError(w, "activación", errors.New("almacén no disponible"))
		return
	}
	actor, _ := PrincipalFromContext(r.Context())
	changed, err := a.NexoRevokeActivation(r.Context(), codeID, target, adminAuditEvent(actor, "nexo.activation.revoked", target, "", nil))
	if err != nil {
		nexoInternalError(w, "activación", fmt.Errorf("revocación código=%s destinatario=%s: %w", codeID, target, err))
		return
	}
	if !changed {
		nexoRespondJSON(w, 409, "activation_not_pending", "El código no está pendiente o no pertenece a esta persona.")
		return
	}
	if panel {
		redirectDashboardAdmin(w, r, "/dashboard/admin/users/"+target)
		return
	}
	jsonResponse(w, 200, nexoActivationRevoked{Changed: changed})
}

var nexoActivationMessages = map[string]string{
	"activation_invalid":                   "El código no es válido. Solicita uno nuevo al administrador.",
	"activation_used":                      "El código ya fue canjeado. No se puede usar otra vez.",
	"activation_expired":                   "El código venció. Solicita uno nuevo al administrador.",
	"activation_revoked":                   "El código fue revocado. Solicita uno nuevo al administrador.",
	"activation_disabled":                  "La persona está deshabilitada. Contacta al administrador.",
	cloudstore.NexoActivationPolicyChanged: "La política de tokens cambió desde que se emitió el código y ya no permite su alcance o su vida. Solicita uno nuevo al administrador.",
	cloudstore.NexoActivationSelfRedeem:    "Quien emitió el código no puede canjearlo. Debe canjearlo la persona a la que se entregó.",
}

func nexoActivationStatus(code string) int {
	switch code {
	case cloudstore.NexoActivationPolicyChanged:
		return http.StatusConflict
	case cloudstore.NexoActivationSelfRedeem:
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}

// nexoRedeemCaller devuelve quién presenta el código si la petición llegó
// autenticada (token en Authorization o sesión del panel). El canje es público:
// una credencial inválida se ignora, no cuenta como fallo de autenticación.
func (s *CloudServer) nexoRedeemCaller(r *http.Request) cloudstore.NexoRedeemCaller {
	if p, ok := PrincipalFromContext(r.Context()); ok && strings.TrimSpace(p.ID) != "" {
		return cloudstore.NexoRedeemCaller{PrincipalID: p.ID, TokenID: p.TokenID}
	}
	if s.principalAuth != nil {
		if token, err := bearerTokenFromRequest(r); err == nil {
			if p, err := s.principalAuth.ResolveBearerToken(r.Context(), token); err == nil && strings.TrimSpace(p.ID) != "" {
				return cloudstore.NexoRedeemCaller{PrincipalID: p.ID, TokenID: p.TokenID}
			}
		}
	}
	if p, ok := s.dashboardPrincipalFromRequest(r); ok && strings.TrimSpace(p.ID) != "" {
		return cloudstore.NexoRedeemCaller{PrincipalID: p.ID, TokenID: p.TokenID}
	}
	return cloudstore.NexoRedeemCaller{}
}

func (s *CloudServer) nexoRedeemCode(w http.ResponseWriter, r *http.Request, raw string, respond nexoRespond) (string, cloudstore.PrincipalToken, bool) {
	if !s.nexoAuthPrecheck(w, r) {
		return "", cloudstore.PrincipalToken{}, false
	}
	a := s.nexoActivations()
	if a == nil || s.managedHasher == nil {
		nexoInternalError(w, "activación", errors.New("almacén no disponible"))
		return "", cloudstore.PrincipalToken{}, false
	}
	raw = strings.TrimSpace(raw)
	// Se limita el cuerpo antes de llegar aquí. Incluso un código mal formado
	// se audita con la misma respuesta genérica para códigos desconocidos.
	hash, err := s.managedHasher.Hash("nexo-activation:v1:" + raw)
	if err != nil {
		nexoInternalError(w, "activación", fmt.Errorf("canje: hash del código: %w", err))
		return "", cloudstore.PrincipalToken{}, false
	}
	mt, err := cloudauth.GenerateManagedToken("live")
	if err != nil {
		nexoInternalError(w, "activación", fmt.Errorf("canje: generar token: %w", err))
		return "", cloudstore.PrincipalToken{}, false
	}
	th, err := s.managedHasher.Hash(mt.Raw)
	if err != nil {
		nexoInternalError(w, "activación", fmt.Errorf("canje: hash del token: %w", err))
		return "", cloudstore.PrincipalToken{}, false
	}
	token, err := a.NexoRedeemActivation(r.Context(), hash, cloudstore.CreatePrincipalTokenParams{TokenPrefix: mt.Prefix, TokenHash: th}, s.nexoRedeemCaller(r))
	if err != nil {
		var denial *cloudstore.NexoActivationError
		if errors.As(err, &denial) {
			// Reutiliza el bloqueo por intentos, incluyendo repeticiones del mismo código.
			// Un código válido rechazado por política o por ser de su emisor no es un intento de adivinar.
			if denial.CountsAsFailure() {
				key := security.LockoutIPKey(nexoRequestIP(r))
				locked, attempts, until := s.nexoSecurity().lockout.Fail(key)
				if locked {
					s.recordAuthAuditBestEffort(r.Context(), cloudstore.AuthAuditEvent{ActorSource: "activation", Action: nexoAuditActionLockout, Outcome: "denied", ReasonCode: "too_many_failures", Metadata: map[string]any{"counts": "attempts", "failures": attempts, "locked_until": until.UTC().Format(time.RFC3339)}}, "bloqueo de activación")
				}
			}
			respond(w, nexoActivationStatus(denial.Code), denial.Code, nexoActivationMessages[denial.Code])
		} else {
			nexoInternalError(w, "activación", fmt.Errorf("canje no confirmado: %w", err))
		}
		return "", cloudstore.PrincipalToken{}, false
	}
	nexoPrivateCredentialResponse(w)
	return mt.Raw, token, true
}
func (s *CloudServer) handleNexoActivate(w http.ResponseWriter, r *http.Request) {
	nexoPrivateCredentialResponse(w)
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		nexoAuthzBadRequest(w, "Solicitud no válida; envía el código en el cuerpo JSON.")
		return
	}
	raw, token, ok := s.nexoRedeemCode(w, r, body.Code, nexoRespondJSON)
	if !ok {
		return
	}
	jsonResponse(w, 201, nexoActivationRedeemed{RawToken: raw, Token: newNexoTokenView(token, time.Now())})
}
func (s *CloudServer) handleNexoActivationPage(w http.ResponseWriter, r *http.Request) {
	nexoPrivateCredentialResponse(w)
	raw := ""
	errorMsg := ""
	status := http.StatusOK
	if r.Method == http.MethodPost {
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Formulario no válido.", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Formulario no válido.", 400)
			return
		}
		var ok bool
		raw, _, ok = s.nexoRedeemCode(w, r, r.PostForm.Get("code"), func(_ http.ResponseWriter, code int, _ string, message string) { status, errorMsg = code, message })
		if !ok && errorMsg == "" {
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; frame-ancestors 'none'; form-action 'self'")
	w.WriteHeader(status)
	if err := dashboard.NexoActivatePage(raw, errorMsg).Render(r.Context(), w); err != nil {
		nexoLogInternal("dibujar activación", fmt.Errorf("render: %w", err))
	}
}
