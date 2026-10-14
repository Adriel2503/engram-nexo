package stack

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
)

// bootstrapLocal usa la misma guarda atómica y la emisión auditada del bootstrap de Engram.
// Corre en un proceso aislado con el entorno propio, para no heredar la base del usuario.
func bootstrapLocal(ctx context.Context, s state.State) error {
	a, e := render.ReadAccess(s.Dir)
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if len(a) > 0 {
		return nil
	}
	cs, e := cloudstore.New(cloud.ConfigFromEnv())
	if e != nil {
		return errors.New("no se pudo abrir la base para crear el superadmin; revisa el registro")
	}
	defer func() { _ = cs.Close() }()
	h, e := cloudauth.NewManagedTokenHasher([]byte(os.Getenv("ENGRAM_CLOUD_TOKEN_PEPPER")))
	if e != nil {
		return e
	}
	u, e := cs.CreateFirstAdminHumanUser(ctx, cloudstore.CreateHumanUserParams{Username: "superadmin", Email: "superadmin@demo.local", DisplayName: "superadmin"})
	if e != nil {
		return errors.New("no se pudo crear el primer superadmin; si el bootstrap quedó interrumpido usa cloud bootstrap recover-token y guarda el token en accesos.md")
	}
	if e = cs.InsertAuthAuditEvent(ctx, cloudstore.AuthAuditEvent{ActorSource: "bootstrap_cli", TargetPrincipalID: u.PrincipalID, Action: "bootstrap.cli", Outcome: "success", Metadata: map[string]any{"created_admin": true}}); e != nil {
		return e
	}
	mt, e := cloudauth.GenerateManagedToken("live")
	if e != nil {
		return e
	}
	hash, e := h.Hash(mt.Raw)
	if e != nil {
		return e
	}
	_, e = cs.CreatePrincipalTokenWithAudit(ctx, cloudstore.CreatePrincipalTokenParams{PrincipalID: u.PrincipalID, TokenPrefix: mt.Prefix, TokenHash: hash, Name: "nexo-stack", CreatedByPrincipalID: u.PrincipalID}, cloudstore.AuthAuditEvent{ActorSource: "bootstrap_cli", TargetPrincipalID: u.PrincipalID, Action: "bootstrap.cli", Outcome: "success", ReasonCode: "bootstrap_completed"})
	if e != nil {
		return e
	}
	return render.AccessFile(render.Data{State: s, Access: []render.Access{{Username: "superadmin", Role: "superadmin", Token: mt.Raw}}})
}

// ensurePeople guarda cada token inmediatamente después del canje. Un reintento no duplica personas.
func ensurePeople(ctx context.Context, s state.State, people []Person, activate func(context.Context, string, string) (string, error), httpClient *client.API) error {
	a, e := render.ReadAccess(s.Dir)
	if e != nil {
		return e
	}
	tokens := map[string]string{}
	for _, p := range a {
		tokens[p.Username] = p.Token
	}
	if tokens["superadmin"] == "" {
		return errors.New("falta el acceso privado del superadmin")
	}
	httpClient.Server = s.ServerURL
	httpClient.Token = tokens["superadmin"]
	httpClient.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, p := range people {
		if tokens[p.Username] != "" {
			continue
		}
		role := p.Role
		if role == "" {
			role = "member"
		}
		var u struct {
			PrincipalID string `json:"principal_id"`
		}
		e = httpClient.Do(ctx, "POST", "/admin/users", map[string]string{"username": p.Username, "email": p.Email, "display_name": p.Username, "role": role}, &u)
		if e != nil && !client.IsStatus(e, 409) && !client.IsStatus(e, 400) {
			return errors.New("no se pudo crear una persona de prueba")
		}
		if e != nil {
			var users []struct {
				Username string
				Email    string
			}
			if err := httpClient.Do(ctx, "GET", "/admin/users", nil, &users); err != nil {
				return errors.New("no se pudo comprobar una persona existente")
			}
			found := false
			for _, u := range users {
				if u.Username == p.Username && u.Email == p.Email {
					found = true
					break
				}
			}
			if !found {
				return errors.New("la creación falló y no existe una persona propia coincidente")
			}
		}
		var issued struct {
			Code string `json:"activation_code"`
		}
		if e = httpClient.Do(ctx, "POST", "/nexo/authz/v1/tokens", map[string]any{"principal": p.Username, "name": "nexo-stack", "scope": "write", "expires_in_days": 90}, &issued); e != nil {
			return errors.New("no se pudo emitir el código de activación")
		}
		tok, e := activate(ctx, s.ServerURL, issued.Code)
		if e != nil {
			return e
		}
		label := p.Role
		if label == "" {
			label = p.Username
		}
		a = append(a, render.Access{Username: p.Username, Role: label, Token: tok})
		tokens[p.Username] = tok
		if e = render.AccessFile(render.Data{State: s, Access: a}); e != nil {
			return e
		}
	}
	return nil
}

type Person struct {
	Username string
	Email    string
	Role     string
}

func api(s state.State, token string) *client.API {
	a := client.NewAPI(client.Settings{Server: s.ServerURL, Token: token}, 20*time.Second)
	a.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return a
}
