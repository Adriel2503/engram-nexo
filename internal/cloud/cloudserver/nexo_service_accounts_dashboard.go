package cloudserver

// Nexo IA · SG-10 — página del panel para las cuentas de servicio.
//
//	GET  /dashboard/admin/service-accounts[?sa=&all=1]      listado y detalle (platform.directory.read)
//	POST /dashboard/admin/service-accounts                  alta (platform.users.manage)
//	POST /dashboard/admin/service-accounts/{sa}/{op}        disable | enable (platform.users.manage),
//	                                                        token | rotate | revoke (platform.tokens.manage)
//
// Usa los mismos casos de uso que la API (/nexo/authz/v1/service-accounts):
// la identidad sale del token de la sesión, la sesión hereda su alcance
// (un token read no cambia nada) y los formularios llevan el token CSRF del
// panel de conocimiento. El token nuevo se muestra una sola vez en la
// respuesta del POST (Cache-Control: no-store) y nunca se registra.

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/dashboard"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

func (s *CloudServer) registerNexoServiceAccountDashboard(mux *http.ServeMux) {
	s.nexoRoute(mux, "GET", "/dashboard/admin/service-accounts", "platform.directory.read", s.requireDashboardSession(s.nexoSAPanel(s.saPanelHome)))
	s.nexoRoute(mux, "POST", "/dashboard/admin/service-accounts", "platform.users.manage", s.requireDashboardSession(s.nexoSAPanel(s.saPanelCreate)))
	// {op}: disable | enable (platform.users.manage), token | rotate | revoke (platform.tokens.manage)
	s.nexoRoute(mux, "POST", "/dashboard/admin/service-accounts/{sa}/{op}", "platform.users.manage|platform.tokens.manage", s.requireDashboardSession(s.nexoSAPanel(s.saPanelAction)))
}

type saPanelHandler func(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity)

// nexoSAPanel arma la identidad desde la sesión y comprueba CSRF en los POST
// (igual que las páginas de conocimiento).
func (s *CloudServer) nexoSAPanel(fn saPanelHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc, ok := s.nexoAuthz()
		if !ok {
			http.Error(w, "service accounts are not available on this server", http.StatusServiceUnavailable)
			return
		}
		principal, ok := s.dashboardPrincipalFromRequest(r)
		if !ok {
			http.Error(w, "forbidden: service accounts require a managed user session", http.StatusForbidden)
			return
		}
		r = r.WithContext(WithPrincipal(r.Context(), principal))
		r, id := nexoWithAuthzIdentity(r)
		id.TokenScope = s.nexoEffectiveTokenScope(r.Context(), principal)
		r = r.WithContext(authz.WithIdentity(r.Context(), id))
		if !id.Managed() {
			http.Error(w, "forbidden: service accounts require a managed user token", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost && !s.kbCSRFOK(r) {
			http.Error(w, "forbidden: invalid or missing CSRF token; reload the page", http.StatusForbidden)
			return
		}
		fn(w, r, svc, id)
	}
}

// saPanelState es lo que una acción deja para la página.
type saPanelState struct {
	selected string
	notice   string
	errText  string
	status   int
	newToken string
	note     string
}

// saPanelErrText traduce un error del servicio a un texto para la página.
func saPanelErrText(err error) (int, string) {
	var denied *authz.DeniedError
	var rule *authz.RuleError
	switch {
	case errors.As(err, &denied):
		return http.StatusForbidden, "Tu rol o tu token no permiten esta acción (" + string(denied.Capability) + "). Pide el permiso a un administrador o entra con un token de escritura."
	case errors.As(err, &rule):
		status := http.StatusConflict
		switch rule.Code {
		case authz.CodeInvalid:
			status = http.StatusBadRequest
		case authz.CodeNotFound:
			status = http.StatusNotFound
		}
		return status, saPanelRuleText(rule)
	}
	fmt.Printf("dashboard service accounts: error: %v\n", err)
	return http.StatusInternalServerError, "Ocurrió un error interno. Vuelve a intentarlo; si se repite, avisa al administrador."
}

func saPanelRuleText(rule *authz.RuleError) string {
	switch rule.Code {
	case authz.CodeServiceAccountExists:
		return "Ya existe un usuario o una cuenta con ese nombre corto. Elige otro."
	case authz.CodeNotFound:
		return "No encontramos la cuenta, la persona o el token. Revisa el nombre."
	case authz.CodePrincipalDisabled:
		return "La persona o la cuenta está deshabilitada."
	}
	return "No se pudo completar: " + rule.Message
}

func (s *CloudServer) saPanelHome(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	s.saPanelRender(w, r, svc, id, saPanelState{selected: r.URL.Query().Get("sa")})
}

func (s *CloudServer) saPanelCreate(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	a, err := svc.CreateServiceAccount(r.Context(), id, authz.CreateServiceAccountInput{
		Slug: strings.TrimSpace(r.FormValue("slug")), Description: r.FormValue("description"), Owner: r.FormValue("owner"),
	})
	if err != nil {
		st, msg := saPanelErrText(err)
		s.saPanelRender(w, r, svc, id, saPanelState{errText: msg, status: st})
		return
	}
	s.saPanelRender(w, r, svc, id, saPanelState{selected: a.Slug, notice: "Cuenta " + a.Slug + " creada. Pide al responsable del espacio que le dé el rol publicador y emite un token."})
}

func (s *CloudServer) saPanelAction(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity) {
	ref, op := r.PathValue("sa"), r.PathValue("op")
	st := saPanelState{selected: ref}
	fail := func(err error) {
		st.status, st.errText = saPanelErrText(err)
		s.saPanelRender(w, r, svc, id, st)
	}
	respond := func(_ http.ResponseWriter, status int, code string, message string) {
		st.status, st.errText = status, saPanelCodeText(code, message)
		s.saPanelRender(w, r, svc, id, st)
	}
	switch op {
	case "disable", "enable":
		a, changed, err := svc.SetServiceAccountEnabled(r.Context(), id, ref, op == "enable")
		if err != nil {
			fail(err)
			return
		}
		switch {
		case !changed:
			st.notice = "La cuenta " + a.Slug + " no cambió."
		case a.Enabled:
			st.notice = "Cuenta " + a.Slug + " habilitada: sus tokens vigentes vuelven a funcionar."
		default:
			st.notice = "Cuenta " + a.Slug + " deshabilitada: todos sus tokens se rechazan desde la siguiente petición."
		}
		s.saPanelRender(w, r, svc, id, st)
		return
	case "token", "rotate", "revoke":
	default:
		http.NotFound(w, r)
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	if err := s.saPanelRequireTokens(r, svc, id, ref); err != nil {
		fail(err)
		return
	}
	a, err := svc.LookupServiceAccount(r.Context(), ref)
	if err != nil {
		fail(err)
		return
	}
	var days *int
	if raw := strings.TrimSpace(r.FormValue("expires_in_days")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			respond(w, http.StatusBadRequest, authz.CodeInvalid, "los días de vida deben ser un número entero")
			return
		}
		days = &n
	}
	switch op {
	case "token":
		issueCtx, ok := s.nexoTokenIssueContext(w, r, a.PrincipalID, security.IssueRequest{ExpiresInDays: days, Scope: r.FormValue("scope")}, respond)
		if !ok {
			return
		}
		raw, token, ok := s.nexoCreateManagedToken(w, r, issueCtx, store, a.PrincipalID, nexoSATokenName(r.FormValue("name"), a.Slug), "nexo_service_account_panel", respond)
		if !ok {
			return
		}
		s.nexoRecordSATokenIssued(r, svc, id, a.PrincipalID, token)
		st.newToken, st.note = raw, "Token "+token.ID+" ("+token.Name+") de la cuenta "+a.Slug+", "+saPanelExpiry(token.NexoExpiresAt)+"."
	case "rotate":
		grace, err := strconv.Atoi(strings.TrimSpace(r.FormValue("grace_minutes")))
		if err != nil {
			grace = 0
		}
		res, ok := s.nexoSATokenOp(w, r, svc, id, store, a, "rotate", r.FormValue("token_id"), nexoSARotateBody{GraceMinutes: grace, ExpiresInDays: days}, respond, fail)
		if !ok {
			return
		}
		old := "El token anterior quedó revocado."
		switch {
		case res.Old.Error != "":
			old = "Aviso: el token anterior no se pudo cerrar; revócalo."
		case res.Old.ExpiresAt != nil:
			old = fmt.Sprintf("El token anterior vale %d minutos más.", grace)
		}
		st.newToken, st.note = res.Raw, "Token "+res.Token.ID+" ("+res.Token.Name+") reemplaza al "+strings.TrimSpace(r.FormValue("token_id"))+", "+saPanelExpiry(res.Token.NexoExpiresAt)+". "+old
	case "revoke":
		if _, ok := s.nexoSATokenOp(w, r, svc, id, store, a, "revoke", r.FormValue("token_id"), nexoSARotateBody{Reason: "revocado desde el panel"}, respond, fail); !ok {
			return
		}
		st.notice = "Token " + strings.TrimSpace(r.FormValue("token_id")) + " revocado: se rechaza desde la siguiente petición."
	}
	s.saPanelRender(w, r, svc, id, st)
}

// saPanelCodeText traduce los rechazos de validación de la emisión.
func saPanelCodeText(code, message string) string {
	switch code {
	case "no_expiry_forbidden":
		return "Solo un superadmin puede emitir tokens sin caducidad."
	case authz.CodePrincipalDisabled:
		return "La cuenta está deshabilitada: habilítela antes de emitir tokens."
	case authz.CodeNotFound:
		return "No encontramos el token en esta cuenta."
	}
	return "No se pudo completar: " + message
}

func (s *CloudServer) saPanelRequireTokens(r *http.Request, svc *authz.Service, id authz.Identity, ref string) error {
	d, err := svc.Engine().Require(r.Context(), id, authz.PlatformScope(), authz.CapPlatformTokensManage, map[string]any{"route": r.Pattern, "target": ref})
	if err != nil {
		return err
	}
	if !d.Allowed {
		return &authz.DeniedError{Scope: authz.PlatformScope(), Capability: authz.CapPlatformTokensManage, Decision: d}
	}
	return nil
}

func saPanelExpiry(t *time.Time) string {
	if t == nil {
		return "sin caducidad"
	}
	return "vence el " + t.UTC().Format("2006-01-02")
}

func saPanelWhen(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04") + " UTC"
}

func saPanelRow(a authz.ServiceAccount) dashboard.NexoSARow {
	roles := make([]string, 0, len(a.Bindings))
	for _, b := range a.Bindings {
		roles = append(roles, string(b.Role)+" en "+b.Scope.Key)
	}
	owner := a.OwnerUsername
	if owner == "" {
		owner = a.OwnerID
	}
	return dashboard.NexoSARow{Slug: a.Slug, ID: a.PrincipalID, Description: a.Description, Owner: owner, Enabled: a.Enabled, Roles: strings.Join(roles, ", "),
		Usage: saPanelUsage(a), Stale: a.Stale}
}

// saPanelUsage es el último uso de la cuenta en texto.
func saPanelUsage(a authz.ServiceAccount) string {
	switch {
	case a.LastActivityAt == nil:
		return "nunca usada (creada hace " + saPanelDays(a.IdleDays) + ")"
	case a.IdleDays == 0:
		return "en las últimas 24 horas"
	}
	return "sin uso desde hace " + saPanelDays(a.IdleDays)
}

func saPanelDays(n int) string {
	if n == 1 {
		return "1 día"
	}
	return strconv.Itoa(n) + " días"
}

// saPanelStaleNote explica la marca "Sin uso" en el detalle.
func saPanelStaleNote(a authz.ServiceAccount, owner string) string {
	return "Sin uso desde hace " + saPanelDays(a.IdleDays) + " (el aviso empieza a los " + saPanelDays(a.StaleAfterDays) +
		", NEXO_SA_STALE_DAYS). Una cuenta de servicio no se deshabilita sola: confirme con " + owner +
		" si sigue en uso; si no, revoque sus tokens o deshabilítela."
}

// saPanelRender arma la página: listado, detalle de la cuenta elegida y
// formularios según las capacidades de la sesión.
func (s *CloudServer) saPanelRender(w http.ResponseWriter, r *http.Request, svc *authz.Service, id authz.Identity, st saPanelState) {
	ctx := r.Context()
	all := r.URL.Query().Get("all") == "1"
	page := dashboard.NexoSAPage{CSRF: s.kbCSRFToken(r), Notice: st.notice, Error: st.errText, ShowAll: all, NewToken: st.newToken, NewTokenNote: st.note}
	q := authz.ServiceAccountQuery{Limit: authz.MaxLimit}
	if !all {
		on := true
		q.Enabled = &on
	}
	items, _, err := svc.ListServiceAccounts(ctx, id, q)
	if err != nil {
		status, msg := saPanelErrText(err)
		page.Error = msg
		s.saPanelWrite(w, r, status, page)
		return
	}
	for _, a := range items {
		page.Accounts = append(page.Accounts, saPanelRow(a))
	}
	if d, err := svc.Engine().Can(ctx, id, authz.PlatformScope(), authz.CapPlatformUsersManage); err == nil {
		page.CanManage = d.Allowed
	}
	if d, err := svc.Engine().Can(ctx, id, authz.PlatformScope(), authz.CapPlatformTokensManage); err == nil {
		page.CanTokens = d.Allowed
	}
	if ref := strings.TrimSpace(st.selected); ref != "" {
		if a, err := svc.LookupServiceAccount(ctx, ref); err == nil {
			d := dashboard.NexoSADetail{NexoSARow: saPanelRow(a), CreatedBy: a.CreatedByName, CreatedAt: a.CreatedAt.UTC().Format("2006-01-02")}
			if d.Stale {
				d.StaleNote = saPanelStaleNote(a, d.Owner)
			}
			if d.CreatedBy == "" {
				d.CreatedBy = "-"
			}
			tokens, err := s.nexoSATokens(ctx, a.PrincipalID)
			if err != nil {
				page.Error = "No se pudieron leer los tokens de la cuenta."
			}
			for _, t := range tokens {
				row := dashboard.NexoSATokenRow{ID: t.ID, Name: t.Name, Scope: t.Scope, Expires: saPanelExpiry(t.ExpiresAt), LastUsed: saPanelWhen(t.LastUsedAt), LastSeen: "-"}
				switch {
				case t.RevokedAt != nil:
					row.State = "Revocado"
				case t.Expired:
					row.State = "Vencido"
				default:
					row.State, row.Active = "Activo", true
				}
				if t.LastSeen != nil {
					row.LastSeen = saPanelWhen(&t.LastSeen.At) + ", " + t.LastSeen.IP + ", " + t.LastSeen.UserAgent
				}
				d.Tokens = append(d.Tokens, row)
			}
			page.Selected = &d
		} else if page.Error == "" {
			_, page.Error = saPanelErrText(err)
		}
	}
	status := st.status
	if status == 0 {
		status = http.StatusOK
	}
	s.saPanelWrite(w, r, status, page)
}

func (s *CloudServer) saPanelWrite(w http.ResponseWriter, r *http.Request, status int, page dashboard.NexoSAPage) {
	layout := dashboard.Layout("Cuentas de servicio", s.dashboardDisplayName(r), "service-accounts", s.isDashboardAdmin(r), dashboard.NexoServiceAccountsPage(page))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
	w.WriteHeader(status)
	if err := layout.Render(r.Context(), w); err != nil {
		fmt.Printf("dashboard service accounts: render: %v\n", err)
	}
}
