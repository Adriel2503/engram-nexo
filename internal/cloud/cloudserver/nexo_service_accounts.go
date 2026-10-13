package cloudserver

// Nexo IA · SG-10 — cuentas de servicio para CI y bots (API).
//
//	GET    /nexo/authz/v1/service-accounts[?enabled=&cursor=&limit=]         platform.directory.read
//	POST   /nexo/authz/v1/service-accounts  {slug,display_name,description,owner}
//	                                                                         platform.users.manage
//	GET    /nexo/authz/v1/service-accounts/{sa}                              platform.directory.read
//	PATCH  /nexo/authz/v1/service-accounts/{sa}  {description,owner}         platform.users.manage
//	POST   /nexo/authz/v1/service-accounts/{sa}:disable | {sa}:enable        platform.users.manage
//	POST   /nexo/authz/v1/service-accounts/{sa}/tokens  {name,expires_in_days,scope}
//	                                                                         platform.tokens.manage
//	POST   /nexo/authz/v1/service-accounts/{sa}/tokens/{id}:rotate  {grace_minutes,name,expires_in_days,scope}
//	POST   /nexo/authz/v1/service-accounts/{sa}/tokens/{id}:revoke  {reason}
//	                                                                         platform.tokens.manage
//
// {sa} es el id numérico o el nombre corto (slug). Los roles en espacios y
// proyectos se asignan con la API de roles de siempre
// (PUT /nexo/authz/v1/scopes/{type}/{key}/members/{sa}); R-10 lo hace cumplir
// el servicio y el trigger. El token en claro solo aparece en la respuesta de
// la emisión o la rotación (raw_token), una vez; nunca en logs ni eventos.
// "Sin caducidad" (expires_in_days: 0) solo lo emite un superadmin, igual que
// en POST /nexo/authz/v1/tokens.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// nexoSAMaxGraceMinutes es la gracia máxima del token anterior al rotar (7 días).
const nexoSAMaxGraceMinutes = 7 * 24 * 60

func (s *CloudServer) registerNexoServiceAccountRoutes(mux *http.ServeMux) {
	// Todas las rutas /nexo/* de SG-10. Al integrar S5 pasan a s.nexoRoute con
	// la capacidad de cada comentario.
	p := nexoAuthzPrefix + "/service-accounts"
	mux.HandleFunc("GET "+p, s.nexoAuthzRoute(s.handleNexoSAList))                                      // cap: platform.directory.read
	mux.HandleFunc("POST "+p, s.nexoAuthzRoute(s.handleNexoSACreate))                                   // cap: platform.users.manage
	mux.HandleFunc("GET "+p+"/{sa}", s.nexoAuthzRoute(s.handleNexoSAGet))                               // cap: platform.directory.read
	mux.HandleFunc("PATCH "+p+"/{sa}", s.nexoAuthzRoute(s.handleNexoSAUpdate))                          // cap: platform.users.manage
	mux.HandleFunc("POST "+p+"/{saAction}", s.nexoAuthzRoute(s.handleNexoSAAction))                     // cap: platform.users.manage ({sa}:disable | {sa}:enable)
	mux.HandleFunc("POST "+p+"/{sa}/tokens", s.nexoAuthzRoute(s.handleNexoSAIssueToken))                // cap: platform.tokens.manage (sin caducidad: platform.tokens.manage+platform.admins.manage)
	mux.HandleFunc("POST "+p+"/{sa}/tokens/{tokenAction}", s.nexoAuthzRoute(s.handleNexoSATokenAction)) // cap: platform.tokens.manage ({id}:rotate | {id}:revoke)
	// Panel (no son /nexo/*): sesión del panel + CSRF, ver nexo_service_accounts_dashboard.go.
	s.registerNexoServiceAccountDashboard(mux)
}

// ─── Vistas ──────────────────────────────────────────────────────────────────

// nexoSATokenView es un token de la cuenta con su último uso (sin secreto).
type nexoSATokenView struct {
	ID          string                  `json:"id"`
	PrincipalID string                  `json:"principal_id"`
	TokenPrefix string                  `json:"token_prefix"`
	Name        string                  `json:"name"`
	Scope       string                  `json:"scope"`
	ExpiresAt   *time.Time              `json:"expires_at"`
	NoExpiry    bool                    `json:"no_expiry"`
	Expired     bool                    `json:"expired"`
	CreatedAt   time.Time               `json:"created_at"`
	CreatedBy   string                  `json:"created_by_principal_id,omitempty"`
	LastUsedAt  *time.Time              `json:"last_used_at"`
	RevokedAt   *time.Time              `json:"revoked_at,omitempty"`
	LastSeen    *security.TokenLastSeen `json:"last_seen,omitempty"`
}

func newNexoSATokenView(t nexoTokenView) nexoSATokenView {
	return nexoSATokenView{ID: t.ID, PrincipalID: t.PrincipalID, TokenPrefix: t.TokenPrefix, Name: t.Name, Scope: t.Scope,
		ExpiresAt: t.ExpiresAt, NoExpiry: t.NoExpiry, Expired: t.Expired, CreatedAt: t.CreatedAt, CreatedBy: t.CreatedBy,
		LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt}
}

// nexoSADetail es GET .../service-accounts/{sa}: la cuenta con sus tokens.
type nexoSADetail struct {
	PrincipalID   string                        `json:"principal_id"`
	Slug          string                        `json:"slug"`
	DisplayName   string                        `json:"display_name"`
	Description   string                        `json:"description"`
	Enabled       bool                          `json:"enabled"`
	OwnerID       string                        `json:"owner_principal_id"`
	OwnerUsername string                        `json:"owner_username,omitempty"`
	OwnerName     string                        `json:"owner_display_name,omitempty"`
	OwnerEnabled  bool                          `json:"owner_enabled"`
	CreatedBy     string                        `json:"created_by_principal_id,omitempty"`
	CreatedByName string                        `json:"created_by_username,omitempty"`
	CreatedAt     time.Time                     `json:"created_at"`
	UpdatedAt     time.Time                     `json:"updated_at"`
	Bindings      []authz.ServiceAccountBinding `json:"bindings"`
	Tokens        []nexoSATokenView             `json:"tokens"`
}

func newNexoSADetail(a authz.ServiceAccount, tokens []nexoSATokenView) nexoSADetail {
	return nexoSADetail{PrincipalID: a.PrincipalID, Slug: a.Slug, DisplayName: a.DisplayName, Description: a.Description, Enabled: a.Enabled,
		OwnerID: a.OwnerID, OwnerUsername: a.OwnerUsername, OwnerName: a.OwnerName, OwnerEnabled: a.OwnerEnabled,
		CreatedBy: a.CreatedBy, CreatedByName: a.CreatedByName, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
		Bindings: a.Bindings, Tokens: tokens}
}

// nexoSAStateResponse es la respuesta de {sa}:disable y {sa}:enable.
type nexoSAStateResponse struct {
	ServiceAccount authz.ServiceAccount `json:"service_account"`
	Changed        bool                 `json:"changed"`
}

// nexoSATokenIssuedResponse es la respuesta de la emisión; raw_token igual
// que POST /nexo/authz/v1/tokens (solo esta vez).
type nexoSATokenIssuedResponse struct {
	RawToken string        `json:"raw_token"`
	Token    nexoTokenView `json:"token"`
}

// nexoSAOldToken es el estado del token anterior tras una rotación.
type nexoSAOldToken struct {
	ID        string     `json:"id"`
	Revoked   bool       `json:"revoked,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// nexoSATokenRotatedResponse es la respuesta de {id}:rotate.
type nexoSATokenRotatedResponse struct {
	RawToken string         `json:"raw_token"`
	Token    nexoTokenView  `json:"token"`
	OldToken nexoSAOldToken `json:"old_token"`
}

// nexoSATokenRevokedResponse es la respuesta de {id}:revoke.
type nexoSATokenRevokedResponse struct {
	TokenID string `json:"token_id"`
	Revoked bool   `json:"revoked"`
	Changed bool   `json:"changed"`
}

// nexoSATokens lista los tokens de la cuenta con caducidad, alcance, último
// uso y la IP y el cliente de la última acción registrada.
func (s *CloudServer) nexoSATokens(ctx context.Context, principalID string) ([]nexoSATokenView, error) {
	out := []nexoSATokenView{}
	if s.adminIdentity == nil {
		return out, nil
	}
	tokens, err := s.adminIdentity.ListPrincipalTokens(ctx, principalID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(tokens))
	for _, t := range tokens {
		ids = append(ids, t.ID)
	}
	seen := map[string]security.TokenLastSeen{}
	if db := s.nexoDB(); db != nil {
		if seen, err = security.LoadTokensLastSeen(ctx, db, principalID, ids); err != nil {
			return nil, err
		}
	}
	now := time.Now()
	for i := len(tokens) - 1; i >= 0; i-- { // el más nuevo primero
		v := newNexoSATokenView(newNexoTokenView(tokens[i], now))
		if ls, ok := seen[tokens[i].ID]; ok {
			ls := ls
			v.LastSeen = &ls
		}
		out = append(out, v)
	}
	return out, nil
}

// ─── Cuentas ─────────────────────────────────────────────────────────────────

func (s *CloudServer) handleNexoSAList(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	p := nexoParams{values: r.URL.Query()}
	q := authz.ServiceAccountQuery{Cursor: p.str("cursor"), Limit: p.limit()}
	if p.raw("enabled") != "" {
		b := p.boolean("enabled")
		q.Enabled = &b
	}
	if p.fail(w) {
		return
	}
	items, next, err := svc.ListServiceAccounts(r.Context(), id, q)
	if err != nil {
		nexoSAWriteError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, newNexoPage(items, next, authz.NormalizeLimit(q.Limit)))
}

type nexoSACreateBody struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Owner       string `json:"owner"`
}

func (s *CloudServer) handleNexoSACreate(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	var body nexoSACreateBody
	if err := decodeJSONBody(r, &body); err != nil {
		nexoAuthzBadRequest(w, fmt.Sprintf("invalid body: %v", err))
		return
	}
	a, err := svc.CreateServiceAccount(r.Context(), id, authz.CreateServiceAccountInput{Slug: body.Slug, DisplayName: body.DisplayName, Description: body.Description, Owner: body.Owner})
	if err != nil {
		nexoSAWriteError(w, err)
		return
	}
	jsonResponse(w, http.StatusCreated, a)
}

func (s *CloudServer) handleNexoSAGet(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	a, err := svc.GetServiceAccount(r.Context(), id, r.PathValue("sa"))
	if err != nil {
		nexoSAWriteError(w, err)
		return
	}
	tokens, err := s.nexoSATokens(r.Context(), a.PrincipalID)
	if err != nil {
		nexoSAWriteError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, newNexoSADetail(a, tokens))
}

type nexoSAUpdateBody struct {
	Description *string `json:"description"`
	Owner       *string `json:"owner"`
}

func (s *CloudServer) handleNexoSAUpdate(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	var body nexoSAUpdateBody
	if err := decodeJSONBody(r, &body); err != nil {
		nexoAuthzBadRequest(w, fmt.Sprintf("invalid body: %v", err))
		return
	}
	a, err := svc.UpdateServiceAccount(r.Context(), id, r.PathValue("sa"), authz.UpdateServiceAccountInput{Description: body.Description, Owner: body.Owner})
	if err != nil {
		nexoSAWriteError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, a)
}

// handleNexoSAAction atiende {sa}:disable y {sa}:enable.
func (s *CloudServer) handleNexoSAAction(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	ref, action, ok := strings.Cut(r.PathValue("saAction"), ":")
	if !ok || (action != "disable" && action != "enable") {
		writeNexoNotFound(w)
		return
	}
	a, changed, err := svc.SetServiceAccountEnabled(r.Context(), id, ref, action == "enable")
	if err != nil {
		nexoSAWriteError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, nexoSAStateResponse{ServiceAccount: a, Changed: changed})
}

// ─── Tokens ──────────────────────────────────────────────────────────────────

type nexoSAIssueBody struct {
	Name          string `json:"name"`
	ExpiresInDays *int   `json:"expires_in_days"`
	Scope         string `json:"scope"`
}

// nexoSARequireTokens autoriza platform.tokens.manage y lee la cuenta.
func (s *CloudServer) nexoSARequireTokens(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity, ref string) (authz.ServiceAccount, bool) {
	d, err := svc.Engine().Require(r.Context(), id, authz.PlatformScope(), authz.CapPlatformTokensManage, map[string]any{"route": r.Pattern, "target": ref})
	if err != nil {
		nexoSAWriteError(w, err)
		return authz.ServiceAccount{}, false
	}
	if !d.Allowed {
		nexoAuthzWriteError(w, &authz.DeniedError{Scope: authz.PlatformScope(), Capability: authz.CapPlatformTokensManage, Decision: d})
		return authz.ServiceAccount{}, false
	}
	a, err := svc.LookupServiceAccount(r.Context(), ref)
	if err != nil {
		nexoSAWriteError(w, err)
		return authz.ServiceAccount{}, false
	}
	return a, true
}

func (s *CloudServer) handleNexoSAIssueToken(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	var body nexoSAIssueBody
	if err := decodeJSONBody(r, &body); err != nil {
		nexoAuthzBadRequest(w, fmt.Sprintf("invalid body: %v", err))
		return
	}
	a, ok := s.nexoSARequireTokens(w, r, svc, id, r.PathValue("sa"))
	if !ok {
		return
	}
	issueCtx, ok := s.nexoTokenIssueContext(w, r, a.PrincipalID, security.IssueRequest{ExpiresInDays: body.ExpiresInDays, Scope: body.Scope}, nexoRespondJSON)
	if !ok {
		return
	}
	raw, token, ok := s.nexoCreateManagedToken(w, r, issueCtx, store, a.PrincipalID, nexoSATokenName(body.Name, a.Slug), "nexo_service_account", nexoRespondJSON)
	if !ok {
		return
	}
	s.nexoRecordSATokenIssued(r, svc, id, a.PrincipalID, token)
	jsonResponse(w, http.StatusCreated, nexoSATokenIssuedResponse{RawToken: raw, Token: newNexoTokenView(token, time.Now())})
}

// nexoSATokenName: sin nombre, el token se llama como la cuenta.
func nexoSATokenName(name, slug string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	return slug
}

// nexoRecordSATokenIssued deja service_account.token_issued (sin el token).
func (s *CloudServer) nexoRecordSATokenIssued(r *http.Request, svc *authz.Service, id authz.Identity, principalID string, token cloudstore.PrincipalToken) {
	if err := svc.RecordServiceAccountTokenEvent(r.Context(), id, authz.ActionServiceAccountTokenIssued, principalID, nexoSATokenDetails(token)); err != nil {
		log.Printf("[nexo] cuentas de servicio: evento de emision no registrado (token_id=%s): %v", token.ID, err)
	}
}

func nexoSATokenDetails(t cloudstore.PrincipalToken) map[string]any {
	d := map[string]any{"token_id": t.ID, "token_prefix": t.TokenPrefix, "name": t.Name, "scope": authz.NormalizeTokenScope(t.NexoScope)}
	if t.NexoExpiresAt != nil {
		d["expires_at"] = t.NexoExpiresAt.UTC().Format(time.RFC3339)
	} else {
		d["expires_at"] = "never"
	}
	return d
}

type nexoSARotateBody struct {
	GraceMinutes  int    `json:"grace_minutes"`
	Name          string `json:"name"`
	ExpiresInDays *int   `json:"expires_in_days"`
	Scope         string `json:"scope"`
	Reason        string `json:"reason"`
}

// handleNexoSATokenAction atiende {id}:rotate y {id}:revoke.
func (s *CloudServer) handleNexoSATokenAction(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	tokenID, action, ok := strings.Cut(r.PathValue("tokenAction"), ":")
	if !ok || (action != "rotate" && action != "revoke") || strings.TrimSpace(tokenID) == "" {
		writeNexoNotFound(w)
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	var body nexoSARotateBody
	if err := decodeJSONBody(r, &body); err != nil {
		nexoAuthzBadRequest(w, fmt.Sprintf("invalid body: %v", err))
		return
	}
	a, ok := s.nexoSARequireTokens(w, r, svc, id, r.PathValue("sa"))
	if !ok {
		return
	}
	res, ok := s.nexoSATokenOp(w, r, svc, id, store, a, action, tokenID, body, nexoRespondJSON, func(err error) { nexoSAWriteError(w, err) })
	if !ok {
		return
	}
	if action == "revoke" {
		jsonResponse(w, http.StatusOK, nexoSATokenRevokedResponse{TokenID: strings.TrimSpace(tokenID), Revoked: true, Changed: res.Changed})
		return
	}
	jsonResponse(w, http.StatusCreated, nexoSATokenRotatedResponse{RawToken: res.Raw, Token: newNexoTokenView(res.Token, time.Now()), OldToken: res.Old})
}

// nexoSATokenResult es el resultado de rotar o revocar.
type nexoSATokenResult struct {
	Raw     string // token nuevo en claro (rotación), solo para entregarlo una vez
	Token   cloudstore.PrincipalToken
	Old     nexoSAOldToken
	Changed bool // revocación: false si ya estaba revocado
}

// nexoSATokenOp rota o revoca un token de la cuenta (ya autorizada con
// platform.tokens.manage). Lo usan la API y el panel: respond escribe los
// rechazos de validación y fail los errores del servicio.
func (s *CloudServer) nexoSATokenOp(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity, store AdminIdentityStore, a authz.ServiceAccount,
	action, tokenID string, body nexoSARotateBody, respond nexoRespond, fail func(error)) (nexoSATokenResult, bool) {
	var res nexoSATokenResult
	tokens, err := store.ListPrincipalTokens(r.Context(), a.PrincipalID)
	if err != nil {
		fail(err)
		return res, false
	}
	var old *cloudstore.PrincipalToken
	for i := range tokens {
		if tokens[i].ID == strings.TrimSpace(tokenID) {
			old = &tokens[i]
		}
	}
	if old == nil {
		respond(w, http.StatusNotFound, authz.CodeNotFound, "token not found for this service account")
		return res, false
	}
	if action == "revoke" {
		reason := strings.TrimSpace(body.Reason)
		if len(reason) > 200 {
			respond(w, http.StatusBadRequest, authz.CodeInvalid, "reason must be at most 200 characters")
			return res, false
		}
		changed, err := svc.RevokeServiceAccountToken(r.Context(), id, a.PrincipalID, old.ID, reason)
		if err != nil {
			fail(err)
			return res, false
		}
		res.Changed = changed
		return res, true
	}

	// Rotación: primero el token nuevo (si algo falla después, el CI no se
	// queda sin token), luego el anterior vence en la gracia o se revoca.
	if old.RevokedAt != nil {
		respond(w, http.StatusConflict, authz.CodeInvalid, "the token is already revoked: issue a new one")
		return res, false
	}
	if body.GraceMinutes < 0 || body.GraceMinutes > nexoSAMaxGraceMinutes {
		respond(w, http.StatusBadRequest, authz.CodeInvalid, fmt.Sprintf("grace_minutes must be between 0 and %d", nexoSAMaxGraceMinutes))
		return res, false
	}
	scope := body.Scope
	if strings.TrimSpace(scope) == "" {
		scope = authz.NormalizeTokenScope(old.NexoScope)
	}
	issueCtx, ok := s.nexoTokenIssueContext(w, r, a.PrincipalID, security.IssueRequest{ExpiresInDays: body.ExpiresInDays, Scope: scope}, respond)
	if !ok {
		return res, false
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = old.Name
	}
	raw, token, ok := s.nexoCreateManagedToken(w, r, issueCtx, store, a.PrincipalID, nexoSATokenName(name, a.Slug), "nexo_service_account_rotate", respond)
	if !ok {
		return res, false
	}
	details := nexoSATokenDetails(token)
	details["new_token_id"] = details["token_id"]
	delete(details, "token_id")
	details["grace_minutes"] = body.GraceMinutes
	res.Raw, res.Token, res.Old = raw, token, nexoSAOldToken{ID: old.ID}
	expires, err := svc.ExpireTokenWithin(r.Context(), id, a.PrincipalID, old.ID, time.Duration(body.GraceMinutes)*time.Minute, details)
	switch {
	case err != nil:
		// El token nuevo ya existe y solo se muestra ahora: se entrega igual y
		// se avisa que el anterior sigue como estaba.
		log.Printf("[nexo] cuentas de servicio: rotacion sin cerrar el token anterior (token_id=%s): %v", old.ID, err)
		res.Old.Error = "the previous token could not be expired or revoked; revoke it with :revoke"
	case expires == nil:
		res.Old.Revoked = true
	default:
		v := expires.UTC()
		res.Old.ExpiresAt = &v
	}
	return res, true
}

// ─── Errores ─────────────────────────────────────────────────────────────────

func writeNexoNotFound(w http.ResponseWriter) {
	writeNexoSAError(w, http.StatusNotFound, authz.CodeNotFound, "not found")
}

func writeNexoSAError(w http.ResponseWriter, status int, code, message string) {
	nexoRespondJSON(w, status, code, message)
}

// nexoSAWriteError: los rechazos del servicio (403, 404, 409, 422, 400) como
// en la API de roles; cualquier otro error, 500 genérico con el detalle solo
// en el log (nunca texto de la base al cliente).
func nexoSAWriteError(w http.ResponseWriter, err error) {
	var denied *authz.DeniedError
	var rule *authz.RuleError
	if errors.As(err, &denied) || errors.As(err, &rule) || errors.Is(err, authz.ErrInvalidCursor) {
		nexoAuthzWriteError(w, err)
		return
	}
	log.Printf("[nexo] cuentas de servicio: %v", err)
	writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, "service accounts: internal error")
}
