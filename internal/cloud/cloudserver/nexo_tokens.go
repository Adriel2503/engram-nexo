package cloudserver

// Nexo IA · NX-07 fase S3 (NX-06) — caducidad y alcance de los tokens en el
// servidor (design §4):
//
//   - emisión con expires_in_days y scope en /admin/* (JSON), el panel
//     (formulario) y /nexo/authz/v1/tokens; "sin caducidad" (0) solo para
//     cuentas de servicio y solo con platform.admins.manage (superadmin);
//   - las sesiones del panel no vuelven a presentar el token: en cada
//     petición se revisa que el token de la sesión no esté revocado ni
//     vencido y se deja su alcance en el contexto;
//   - /nexo/authz/v1/me y los listados muestran caducidad, alcance y último
//     uso (RS-24).
//
//	GET  /nexo/authz/v1/tokens[?principal=]  self.read (propios) o platform.tokens.manage
//	POST /nexo/authz/v1/tokens  {principal,name,expires_in_days,scope}
//	                            platform.tokens.manage (+ platform.admins.manage si el
//	                            dueño es admin, superadmin o auditor, R-7)

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// nexoDB devuelve la base de Nexo si el almacén es CloudStore.
func (s *CloudServer) nexoDB() *sql.DB {
	for _, candidate := range []any{s.store, s.adminIdentity} {
		if p, ok := candidate.(nexoAuthzDB); ok && p != nil {
			if db := p.NexoDB(); db != nil {
				return db
			}
		}
	}
	return nil
}

// nexoRevalidateSessionToken es el enganche de revalidateDashboardPrincipal:
// una sesión del panel deja de valer apenas su token se revoca o vence.
func (s *CloudServer) nexoRevalidateSessionToken(ctx context.Context, principal cloudauth.Principal) error {
	db := s.nexoDB()
	if db == nil {
		return nil // sin almacén Nexo: comportamiento de Engram
	}
	if strings.TrimSpace(principal.TokenID) == "" {
		return cloudauth.ErrUnknownToken
	}
	f, found, err := security.LoadTokenFacts(ctx, db, principal.TokenID)
	if err != nil {
		return fmt.Errorf("nexo: revalidate session token: %w", err)
	}
	if !found || !nexoSamePrincipal(f.PrincipalID, principal.ID) {
		return cloudauth.ErrUnknownToken
	}
	if f.Revoked {
		return cloudauth.ErrTokenRevoked
	}
	security.RecordTokenFacts(ctx, f)
	if f.Expired {
		return security.ErrTokenExpired
	}
	return nil
}

// ─── Emisión ─────────────────────────────────────────────────────────────────

// nexoTokenIssueContext valida caducidad y alcance pedidos para un token de
// targetPrincipalID y devuelve el contexto con los que debe aplicar
// CreatePrincipalTokenWithAudit (enganche // NX-07 de /admin/* y del panel).
func (s *CloudServer) nexoTokenIssueContext(w http.ResponseWriter, r *http.Request, targetPrincipalID string, req security.IssueRequest, respond nexoRespond) (context.Context, bool) {
	ctx := r.Context()
	targetKind := ""
	canNoExpiry := false
	if svc, available := s.nexoAuthz(); available {
		if target, err := svc.ResolvePrincipal(ctx, targetPrincipalID); err == nil {
			targetKind = target.Kind
		}
		if req.ExpiresInDays != nil && *req.ExpiresInDays == 0 {
			id := nexoAuthzIdentity(ctx)
			d, err := svc.Engine().Require(ctx, id, authz.PlatformScope(), authz.CapPlatformAdminsManage,
				map[string]any{"route": r.Pattern, "action": "token.no_expiry", "target_principal_id": targetPrincipalID})
			if err != nil {
				nexoRespondAuthzError(w, respond, err)
				return nil, false
			}
			canNoExpiry = d.Allowed
		}
	}
	issue, ierr := security.ResolveIssue(req, targetKind, canNoExpiry)
	if ierr != nil {
		respond(w, ierr.Status, ierr.Code, ierr.Message)
		return nil, false
	}
	return security.WithTokenIssue(ctx, issue), true
}

// nexoTokenIssueFromForm lee expires_in_days y scope del formulario del panel
// (vacío = por defecto).
func (s *CloudServer) nexoTokenIssueFromForm(w http.ResponseWriter, r *http.Request, targetPrincipalID string) (context.Context, bool) {
	req := security.IssueRequest{Scope: strings.TrimSpace(r.FormValue("scope"))}
	if raw := strings.TrimSpace(r.FormValue("expires_in_days")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			http.Error(w, "expires_in_days must be a whole number of days", http.StatusBadRequest)
			return nil, false
		}
		req.ExpiresInDays = &n
	}
	return s.nexoTokenIssueContext(w, r, targetPrincipalID, req, nexoRespondText)
}

// ─── /nexo/authz/v1/tokens ───────────────────────────────────────────────────

// nexoTokenView es un token en la API Nexo (sin hash ni secreto).
type nexoTokenView struct {
	ID          string     `json:"id"`
	PrincipalID string     `json:"principal_id"`
	TokenPrefix string     `json:"token_prefix"`
	Name        string     `json:"name"`
	Scope       string     `json:"scope"`
	ExpiresAt   *time.Time `json:"expires_at"`
	NoExpiry    bool       `json:"no_expiry"`
	Expired     bool       `json:"expired"`
	CreatedAt   time.Time  `json:"created_at"`
	CreatedBy   string     `json:"created_by_principal_id,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

func newNexoTokenView(t cloudstore.PrincipalToken, now time.Time) nexoTokenView {
	return nexoTokenView{
		ID: t.ID, PrincipalID: t.PrincipalID, TokenPrefix: t.TokenPrefix, Name: t.Name,
		Scope: authz.NormalizeTokenScope(t.NexoScope), ExpiresAt: t.NexoExpiresAt, NoExpiry: t.NexoExpiresAt == nil,
		Expired:   t.NexoExpiresAt != nil && !t.NexoExpiresAt.After(now),
		CreatedAt: t.CreatedAt, CreatedBy: t.CreatedByPrincipalID, LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt,
	}
}

type nexoIssueTokenBody struct {
	Principal     string `json:"principal"`
	Name          string `json:"name"`
	ExpiresInDays *int   `json:"expires_in_days"`
	Scope         string `json:"scope"`
}

func (s *CloudServer) registerNexoTokenRoutes(mux *http.ServeMux) {
	s.nexoRoute(mux, "GET", nexoAuthzPrefix+"/tokens", "self.read|platform.tokens.manage", s.nexoAuthzRoute(s.handleNexoAuthzListTokens))
	s.nexoRoute(mux, "POST", nexoAuthzPrefix+"/tokens", "platform.tokens.manage", s.nexoAuthzRoute(s.handleNexoAuthzIssueToken))
}

func (s *CloudServer) handleNexoAuthzListTokens(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("principal"))
	target := id.PrincipalID
	c := authz.CapSelfRead
	if ref != "" {
		row, err := svc.ResolvePrincipal(r.Context(), ref)
		if err != nil {
			nexoAuthzWriteError(w, err)
			return
		}
		target = row.ID
		if !nexoSamePrincipal(target, id.PrincipalID) {
			c = authz.CapPlatformTokensManage
		}
	}
	d, err := svc.Engine().Require(r.Context(), id, authz.PlatformScope(), c, map[string]any{"route": r.Pattern, "target_principal_id": target})
	if err != nil {
		nexoAuthzWriteError(w, err)
		return
	}
	if !d.Allowed {
		nexoAuthzWriteError(w, &authz.DeniedError{Scope: authz.PlatformScope(), Capability: c, Decision: d})
		return
	}
	tokens, err := store.ListPrincipalTokens(r.Context(), target)
	if err != nil {
		nexoAuthzWriteError(w, err)
		return
	}
	now := time.Now()
	items := make([]nexoTokenView, 0, len(tokens))
	for _, t := range tokens {
		items = append(items, newNexoTokenView(t, now))
	}
	jsonResponse(w, http.StatusOK, map[string]any{"principal_id": target, "items": items})
}

func (s *CloudServer) handleNexoAuthzIssueToken(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	if s.managedHasher == nil {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, "managed token hasher is not configured")
		return
	}
	var body nexoIssueTokenBody
	if err := decodeJSONBody(r, &body); err != nil {
		nexoAuthzBadRequest(w, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if strings.TrimSpace(body.Principal) == "" {
		nexoAuthzBadRequest(w, "principal is required (id or username)")
		return
	}
	d, err := svc.Engine().Require(r.Context(), id, authz.PlatformScope(), authz.CapPlatformTokensManage, map[string]any{"route": r.Pattern, "target": body.Principal})
	if err != nil {
		nexoAuthzWriteError(w, err)
		return
	}
	if !d.Allowed {
		nexoAuthzWriteError(w, &authz.DeniedError{Scope: authz.PlatformScope(), Capability: authz.CapPlatformTokensManage, Decision: d})
		return
	}
	target, err := svc.ResolvePrincipal(r.Context(), body.Principal)
	if err != nil {
		nexoAuthzWriteError(w, err)
		return
	}
	if !s.nexoAllowManageTargetTokens(w, r, target.ID, nexoRespondJSON) { // R-7: tokens de admins solo por superadmin
		return
	}
	issueCtx, ok := s.nexoTokenIssueContext(w, r, target.ID, security.IssueRequest{ExpiresInDays: body.ExpiresInDays, Scope: body.Scope}, nexoRespondJSON)
	if !ok {
		return
	}
	raw, token, ok := s.nexoCreateManagedToken(w, r, issueCtx, store, target.ID, body.Name, "nexo_authz", nexoRespondJSON)
	if !ok {
		return
	}
	if target.Kind == authz.KindServiceAccount { // SG-10: emisión para una cuenta de servicio, también en nexo_authz_events
		s.nexoRecordSATokenIssued(r, svc, id, target.ID, token)
	}
	jsonResponse(w, http.StatusCreated, map[string]any{"raw_token": raw, "token": newNexoTokenView(token, time.Now())})
}

// nexoCreateManagedToken genera, guarda (hash) y audita un token nuevo para
// targetID con la caducidad y el alcance de issueCtx. Devuelve el token en
// claro solo para entregarlo una vez; nunca se registra. Si falla, ya
// respondió.
func (s *CloudServer) nexoCreateManagedToken(w http.ResponseWriter, r *http.Request, issueCtx context.Context, store AdminIdentityStore, targetID, name, via string, respond nexoRespond) (string, cloudstore.PrincipalToken, bool) {
	if s.managedHasher == nil {
		respond(w, http.StatusInternalServerError, constants.UpgradeErrorCodeInternal, "managed token hasher is not configured")
		return "", cloudstore.PrincipalToken{}, false
	}
	actor, _ := PrincipalFromContext(r.Context())
	managedToken, err := cloudauth.GenerateManagedToken("live")
	if err != nil {
		nexoInternalError(w, "generate token", err)
		return "", cloudstore.PrincipalToken{}, false
	}
	tokenHash, err := s.managedHasher.Hash(managedToken.Raw)
	if err != nil {
		nexoInternalError(w, "hash token", err)
		return "", cloudstore.PrincipalToken{}, false
	}
	name = strings.TrimSpace(name)
	token, err := store.CreatePrincipalTokenWithAudit(issueCtx,
		cloudstore.CreatePrincipalTokenParams{PrincipalID: targetID, TokenPrefix: managedToken.Prefix, TokenHash: tokenHash, Name: name, CreatedByPrincipalID: actor.ID},
		adminAuditEvent(actor, authAuditActionTokenCreate, targetID, "", map[string]any{"token_prefix": managedToken.Prefix, "name": name, "via": via}),
	)
	if err != nil {
		switch {
		case errors.Is(err, cloudstore.ErrPrincipalDisabled):
			respond(w, http.StatusConflict, authz.CodePrincipalDisabled, disabledManagedUserTokenMessage(targetID))
		case errors.Is(err, cloudstore.ErrPrincipalNotFound):
			respond(w, http.StatusNotFound, authz.CodeNotFound, "principal not found")
		default:
			// Auditoría o base: el detalle solo en el log (nunca el texto de la base al cliente).
			nexoInternalError(w, "issue token", fmt.Errorf("principal %s: %w", targetID, err))
		}
		return "", cloudstore.PrincipalToken{}, false
	}
	return managedToken.Raw, token, true
}

// ─── /me ─────────────────────────────────────────────────────────────────────

// nexoMeResponse es GET /nexo/authz/v1/me con los datos del token (RS-24).
type nexoMeResponse struct {
	authz.MeView
	Token *security.TokenInfo `json:"token,omitempty"`
}

func (s *CloudServer) nexoMeWithToken(ctx context.Context, view authz.MeView) nexoMeResponse {
	out := nexoMeResponse{MeView: view}
	if db := s.nexoDB(); db != nil && view.TokenID != "" {
		if info, found, err := security.LoadTokenInfo(ctx, db, view.TokenID); err == nil && found {
			out.Token = &info
		}
	}
	return out
}
