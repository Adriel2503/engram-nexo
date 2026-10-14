package nexocli

// engram nexo sa ... (SG-10): cuentas de servicio para CI y bots contra la
// API /nexo/authz/v1/service-accounts, con el servidor y el token de
// `engram cloud config` (o ENGRAM_CLOUD_SERVER y ENGRAM_CLOUD_TOKEN), como
// `engram nexo kb`. El token nuevo de una cuenta se imprime una sola vez y
// solo él en la salida estándar (o en el archivo de --out, con permisos
// 0600); los mensajes van a la salida de errores. Nunca se registra.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
)

const saUsage = `Uso: engram nexo sa <comando> [opciones]

Cuentas de servicio (CI y bots; admin o superadmin):
  list [--all] [--json]                              Cuentas (por defecto, solo las habilitadas)
  show <cuenta> [--json]                             Detalle: responsable, roles y tokens con su último uso
  create --slug S --owner USUARIO [--description D] [--display-name N] [--json]
                                                     Alta; el responsable es la persona a cargo
  update <cuenta> [--owner USUARIO] [--description D]
  disable <cuenta> | enable <cuenta>                 Deshabilitada, todos sus tokens se rechazan
  grant <cuenta> (--space S | --project P) --role lector|proponente|publicador
                                                     Rol en un espacio o proyecto (lo da su responsable)
  ungrant <cuenta> (--space S | --project P)         Quita el rol
  token issue <cuenta> [--name N] [--days N] [--scope read|write] [--out ARCHIVO] [--json]
                                                     Emite un token (--days 0: sin caducidad, solo superadmin)
  token rotate <cuenta> <token-id> [--grace-minutes M] [--days N] [--scope S] [--out ARCHIVO] [--json]
                                                     Token nuevo; el anterior se revoca o vence en M minutos
  token revoke <cuenta> <token-id> [--reason R]

<cuenta> es el nombre corto (slug) o el id. El token nuevo se muestra una sola
vez: solo él va a la salida estándar (o a --out); guárdelo como secreto del CI.
Servidor y token: engram cloud config / ENGRAM_CLOUD_SERVER y ENGRAM_CLOUD_TOKEN.
`

// saAPI crea el cliente (las pruebas lo reemplazan).
var saAPI = func(dataDir, server string) (*client.API, error) {
	return client.New(dataDir, server, 60*time.Second)
}

func runServiceAccounts(args []string, dataDir string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, _ = fmt.Fprint(stdout, saUsage)
		return 0
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "list":
		err = saList(rest, dataDir, stdout)
	case "show":
		err = saShow(rest, dataDir, stdout)
	case "create":
		err = saCreate(rest, dataDir, stdout, stderr)
	case "update":
		err = saUpdate(rest, dataDir, stderr)
	case "disable", "enable":
		err = saSetEnabled(cmd, rest, dataDir, stderr)
	case "grant", "ungrant":
		err = saGrant(cmd, rest, dataDir, stderr)
	case "token":
		err = saTokenCmd(rest, dataDir, stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "comando desconocido: %s\n\n%s", cmd, saUsage)
		return 2
	}
	if err != nil {
		var ue saUsageError
		if errors.As(err, &ue) {
			_, _ = fmt.Fprintln(stderr, ue.msg)
			return 2
		}
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "service_accounts_disabled" { // NX-07: interruptor apagado; el mensaje del servidor explica cómo activarlas
			_, _ = fmt.Fprintln(stderr, "error:", apiErr.Msg)
			return 1
		}
		_, _ = fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

type saUsageError struct{ msg string }

func (e saUsageError) Error() string { return e.msg }

// saFlags crea un FlagSet que acepta opciones antes o después de los
// argumentos posicionales.
type saFlags struct {
	*flag.FlagSet
	server *string
	asJSON *bool
}

func newSAFlags(name string) saFlags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return saFlags{FlagSet: fs, server: fs.String("server", "", "URL del servidor"), asJSON: fs.Bool("json", false, "salida JSON")}
}

// parse separa posicionales y opciones en cualquier orden.
func (f saFlags) parse(args []string) ([]string, error) {
	var pos []string
	for len(args) > 0 {
		if err := f.Parse(args); err != nil {
			return nil, saUsageError{err.Error()}
		}
		args = f.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return pos, nil
}

func saPath(ref string, rest ...string) string {
	p := "/nexo/authz/v1/service-accounts/" + url.PathEscape(strings.TrimSpace(ref))
	for _, r := range rest {
		p += r
	}
	return p
}

func saPrintJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

type saAccount struct {
	PrincipalID   string `json:"principal_id"`
	Slug          string `json:"slug"`
	DisplayName   string `json:"display_name"`
	Description   string `json:"description"`
	Enabled       bool   `json:"enabled"`
	OwnerID       string `json:"owner_principal_id"`
	OwnerUsername string `json:"owner_username"`
	CreatedBy     string `json:"created_by_username"`
	// Último uso y marca de cuenta sin uso (NEXO_SA_STALE_DAYS).
	LastActivityAt *time.Time `json:"last_activity_at"`
	IdleDays       int        `json:"idle_days"`
	Stale          bool       `json:"stale"`
	StaleAfterDays int        `json:"stale_after_days"`
	Bindings       []struct {
		Scope struct {
			Type string `json:"type"`
			Key  string `json:"key"`
		} `json:"scope"`
		Role string `json:"role"`
	} `json:"bindings"`
	Tokens []saTokenView `json:"tokens"`
}

type saTokenView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"token_prefix"`
	Scope      string     `json:"scope"`
	ExpiresAt  *time.Time `json:"expires_at"`
	NoExpiry   bool       `json:"no_expiry"`
	Expired    bool       `json:"expired"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	LastSeen   *struct {
		At        time.Time `json:"at"`
		IP        string    `json:"ip"`
		UserAgent string    `json:"user_agent"`
		Source    string    `json:"source"`
	} `json:"last_seen"`
}

func saOwner(a saAccount) string {
	if a.OwnerUsername != "" {
		return a.OwnerUsername
	}
	return a.OwnerID
}

func saDays(n int) string {
	if n == 1 {
		return "1 día"
	}
	return fmt.Sprintf("%d días", n)
}

// saLastUse es el último uso en texto.
func saLastUse(a saAccount) string {
	switch {
	case a.LastActivityAt == nil:
		return "nunca usada (creada hace " + saDays(a.IdleDays) + ")"
	case a.IdleDays == 0:
		return "en las últimas 24 horas"
	}
	return "sin uso desde hace " + saDays(a.IdleDays)
}

// saListState es el estado con la marca de cuenta sin uso.
func saListState(a saAccount) string {
	if a.Stale {
		return saState(a.Enabled) + ", SIN USO"
	}
	return saState(a.Enabled)
}

func saState(enabled bool) string {
	if enabled {
		return "habilitada"
	}
	return "deshabilitada"
}

func saWhen(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func saList(args []string, dataDir string, stdout io.Writer) error {
	f := newSAFlags("list")
	all := f.Bool("all", false, "incluir las deshabilitadas")
	if _, err := f.parse(args); err != nil {
		return err
	}
	c, err := saAPI(dataDir, *f.server)
	if err != nil {
		return err
	}
	path := "/nexo/authz/v1/service-accounts?limit=200"
	if !*all {
		path += "&enabled=true"
	}
	var page struct {
		Items []saAccount `json:"items"`
	}
	if err := c.Do(context.Background(), "GET", path, nil, &page); err != nil {
		return err
	}
	if *f.asJSON {
		return saPrintJSON(stdout, page)
	}
	if len(page.Items) == 0 {
		_, _ = fmt.Fprintln(stdout, "No hay cuentas de servicio.")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "CUENTA\tID\tESTADO\tULTIMO USO\tRESPONSABLE\tROLES\tDESCRIPCION")
	stale := 0
	for _, a := range page.Items {
		roles := []string{}
		for _, b := range a.Bindings {
			roles = append(roles, b.Role+"@"+b.Scope.Type+":"+b.Scope.Key)
		}
		if a.Stale {
			stale++
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", a.Slug, a.PrincipalID, saListState(a), saLastUse(a), saOwner(a), strings.Join(roles, ","), a.Description)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if stale > 0 {
		_, _ = fmt.Fprintf(stdout, "\n%d cuenta(s) SIN USO en %s o más (NEXO_SA_STALE_DAYS): no se deshabilitan solas; confirme con el responsable si siguen en uso.\n",
			stale, saDays(page.Items[0].StaleAfterDays))
	}
	return nil
}

func saShow(args []string, dataDir string, stdout io.Writer) error {
	f := newSAFlags("show")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return saUsageError{"uso: engram nexo sa show <cuenta> [--json]"}
	}
	c, err := saAPI(dataDir, *f.server)
	if err != nil {
		return err
	}
	var a saAccount
	if err := c.Do(context.Background(), "GET", saPath(pos[0]), nil, &a); err != nil {
		return err
	}
	if *f.asJSON {
		return saPrintJSON(stdout, a)
	}
	_, _ = fmt.Fprintf(stdout, "Cuenta %s (id %s), %s. Responsable: %s.\n", a.Slug, a.PrincipalID, saState(a.Enabled), saOwner(a))
	_, _ = fmt.Fprintf(stdout, "Último uso: %s.\n", saLastUse(a))
	if a.Stale {
		_, _ = fmt.Fprintf(stdout, "SIN USO: %s sin uso (aviso a partir de %s, NEXO_SA_STALE_DAYS). No se deshabilita sola: confirme con %s si sigue en uso; si no, revoque sus tokens o deshabilítela.\n",
			saDays(a.IdleDays), saDays(a.StaleAfterDays), saOwner(a))
	}
	if a.Description != "" {
		_, _ = fmt.Fprintf(stdout, "Descripción: %s\n", a.Description)
	}
	if len(a.Bindings) == 0 {
		_, _ = fmt.Fprintln(stdout, "Roles: ninguno (asígnelo con engram nexo sa grant).")
	} else {
		for _, b := range a.Bindings {
			_, _ = fmt.Fprintf(stdout, "Rol: %s en %s %s\n", b.Role, b.Scope.Type, b.Scope.Key)
		}
	}
	if len(a.Tokens) == 0 {
		_, _ = fmt.Fprintln(stdout, "Tokens: ninguno.")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TOKEN\tNOMBRE\tALCANCE\tVENCE\tESTADO\tULTIMO USO\tULTIMA ACCION (IP, CLIENTE)")
	for _, t := range a.Tokens {
		state := "activo"
		switch {
		case t.RevokedAt != nil:
			state = "revocado"
		case t.Expired:
			state = "vencido"
		}
		vence := saWhen(t.ExpiresAt)
		if t.NoExpiry {
			vence = "sin caducidad"
		}
		last := "-"
		if t.LastSeen != nil {
			last = fmt.Sprintf("%s %s %s (%s)", t.LastSeen.At.Local().Format("2006-01-02 15:04"), t.LastSeen.IP, t.LastSeen.UserAgent, t.LastSeen.Source)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Name, t.Scope, vence, state, saWhen(t.LastUsedAt), last)
	}
	return tw.Flush()
}

func saCreate(args []string, dataDir string, stdout, stderr io.Writer) error {
	f := newSAFlags("create")
	slug := f.String("slug", "", "nombre corto (minúsculas, números y guiones)")
	owner := f.String("owner", "", "persona responsable (usuario o id)")
	desc := f.String("description", "", "para qué se usa")
	name := f.String("display-name", "", "nombre mostrado (por defecto, el slug)")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if *slug == "" && len(pos) == 1 {
		*slug = pos[0]
	}
	if *slug == "" || *owner == "" {
		return saUsageError{"uso: engram nexo sa create --slug S --owner USUARIO [--description D] [--display-name N]"}
	}
	c, err := saAPI(dataDir, *f.server)
	if err != nil {
		return err
	}
	var a saAccount
	body := map[string]string{"slug": *slug, "owner": *owner, "description": *desc, "display_name": *name}
	if err := c.Do(context.Background(), "POST", "/nexo/authz/v1/service-accounts", body, &a); err != nil {
		return err
	}
	if *f.asJSON {
		return saPrintJSON(stdout, a)
	}
	_, _ = fmt.Fprintf(stdout, "Cuenta de servicio %s creada (id %s), responsable %s.\n", a.Slug, a.PrincipalID, saOwner(a))
	_, _ = fmt.Fprintf(stderr, "Siguiente paso: engram nexo sa grant %s --space <espacio> --role publicador (lo hace el responsable del espacio) y engram nexo sa token issue %s.\n", a.Slug, a.Slug)
	return nil
}

func saUpdate(args []string, dataDir string, stderr io.Writer) error {
	f := newSAFlags("update")
	owner := f.String("owner", "", "nueva persona responsable")
	desc := f.String("description", "", "nueva descripción")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	body := map[string]string{}
	f.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "owner":
			body["owner"] = *owner
		case "description":
			body["description"] = *desc
		}
	})
	if len(pos) != 1 || len(body) == 0 {
		return saUsageError{"uso: engram nexo sa update <cuenta> [--owner USUARIO] [--description D]"}
	}
	c, err := saAPI(dataDir, *f.server)
	if err != nil {
		return err
	}
	var a saAccount
	if err := c.Do(context.Background(), "PATCH", saPath(pos[0]), body, &a); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "Cuenta %s actualizada: responsable %s.\n", a.Slug, saOwner(a))
	return nil
}

func saSetEnabled(cmd string, args []string, dataDir string, stderr io.Writer) error {
	f := newSAFlags(cmd)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return saUsageError{"uso: engram nexo sa " + cmd + " <cuenta>"}
	}
	c, err := saAPI(dataDir, *f.server)
	if err != nil {
		return err
	}
	var res struct {
		ServiceAccount saAccount `json:"service_account"`
		Changed        bool      `json:"changed"`
	}
	if err := c.Do(context.Background(), "POST", saPath(pos[0], ":"+cmd), map[string]any{}, &res); err != nil {
		return err
	}
	switch {
	case !res.Changed:
		_, _ = fmt.Fprintf(stderr, "La cuenta %s ya estaba %s.\n", res.ServiceAccount.Slug, saState(res.ServiceAccount.Enabled))
	case res.ServiceAccount.Enabled:
		_, _ = fmt.Fprintf(stderr, "Cuenta %s habilitada: sus tokens vigentes vuelven a funcionar.\n", res.ServiceAccount.Slug)
	default:
		_, _ = fmt.Fprintf(stderr, "Cuenta %s deshabilitada: todos sus tokens se rechazan desde la siguiente petición.\n", res.ServiceAccount.Slug)
	}
	return nil
}

func saGrant(cmd string, args []string, dataDir string, stderr io.Writer) error {
	f := newSAFlags(cmd)
	space := f.String("space", "", "espacio de conocimiento (slug)")
	project := f.String("project", "", "proyecto de memoria")
	role := f.String("role", "", "lector, proponente o publicador")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || (*space == "") == (*project == "") || (cmd == "grant" && *role == "") {
		return saUsageError{"uso: engram nexo sa " + cmd + " <cuenta> (--space S | --project P)" + map[bool]string{true: " --role lector|proponente|publicador", false: ""}[cmd == "grant"]}
	}
	scopeType, key := "space", *space
	if *project != "" {
		scopeType, key = "project", *project
	}
	c, err := saAPI(dataDir, *f.server)
	if err != nil {
		return err
	}
	path := "/nexo/authz/v1/scopes/" + scopeType + "/" + url.PathEscape(key) + "/members/" + url.PathEscape(pos[0])
	if cmd == "ungrant" {
		if err := c.Do(context.Background(), "DELETE", path, nil, nil); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "Rol de %s en %s %s quitado.\n", pos[0], scopeType, key)
		return nil
	}
	if err := c.Do(context.Background(), "PUT", path, map[string]string{"role": *role}, nil); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "%s es %s en %s %s.\n", pos[0], *role, scopeType, key)
	return nil
}

func saTokenCmd(args []string, dataDir string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return saUsageError{"uso: engram nexo sa token issue|rotate|revoke ..."}
	}
	sub, rest := args[0], args[1:]
	f := newSAFlags("token " + sub)
	name := f.String("name", "", "nombre del token (máquina o uso, por ejemplo ci-github)")
	days := f.Int("days", -1, "días de vida (por defecto 90; 0 = sin caducidad, solo superadmin)")
	scope := f.String("scope", "", "read o write")
	out := f.String("out", "", "guardar el token en este archivo (permisos 0600) en lugar de la salida estándar")
	grace := f.Int("grace-minutes", 0, "minutos que el token anterior sigue valiendo (0 = se revoca ya)")
	reason := f.String("reason", "", "motivo de la revocación")
	pos, err := f.parse(rest)
	if err != nil {
		return err
	}
	body := map[string]any{}
	if *name != "" {
		body["name"] = *name
	}
	if *days >= 0 {
		body["expires_in_days"] = *days
	}
	if *scope != "" {
		body["scope"] = *scope
	}
	var path string
	switch {
	case sub == "issue" && len(pos) == 1:
		path = saPath(pos[0], "/tokens")
	case sub == "rotate" && len(pos) == 2:
		path = saPath(pos[0], "/tokens/"+url.PathEscape(pos[1])+":rotate")
		body["grace_minutes"] = *grace
	case sub == "revoke" && len(pos) == 2:
		path = saPath(pos[0], "/tokens/"+url.PathEscape(pos[1])+":revoke")
		body = map[string]any{"reason": *reason}
	default:
		return saUsageError{"uso: engram nexo sa token issue <cuenta> | rotate <cuenta> <token-id> | revoke <cuenta> <token-id> (engram nexo sa help)"}
	}
	c, err := saAPI(dataDir, *f.server)
	if err != nil {
		return err
	}
	if sub == "revoke" {
		var res struct {
			Changed bool `json:"changed"`
		}
		if err := c.Do(context.Background(), "POST", path, body, &res); err != nil {
			return err
		}
		if res.Changed {
			_, _ = fmt.Fprintf(stderr, "Token %s revocado.\n", pos[1])
		} else {
			_, _ = fmt.Fprintf(stderr, "El token %s ya estaba revocado.\n", pos[1])
		}
		return nil
	}
	var res struct {
		RawToken string         `json:"raw_token"`
		Token    saTokenView    `json:"token"`
		OldToken map[string]any `json:"old_token,omitempty"`
	}
	if err := c.Do(context.Background(), "POST", path, body, &res); err != nil {
		return err
	}
	if *out != "" {
		if err := os.WriteFile(*out, []byte(res.RawToken+"\n"), 0o600); err != nil {
			return fmt.Errorf("guardar el token en %s: %w (el token ya se emitió: revóquelo y emita otro)", *out, err)
		}
	}
	if *f.asJSON {
		v := map[string]any{"token": res.Token, "old_token": res.OldToken}
		if *out == "" {
			v["raw_token"] = res.RawToken
		} else {
			v["raw_token_file"] = *out
		}
		return saPrintJSON(stdout, v)
	}
	vence := "sin caducidad"
	if res.Token.ExpiresAt != nil {
		vence = "vence " + res.Token.ExpiresAt.Local().Format("2006-01-02")
	}
	_, _ = fmt.Fprintf(stderr, "Token %s (%s, alcance %s, %s).\n", res.Token.ID, res.Token.Name, res.Token.Scope, vence)
	if res.OldToken != nil {
		switch {
		case res.OldToken["error"] != nil:
			_, _ = fmt.Fprintf(stderr, "Aviso: el token anterior %v no se pudo cerrar: revóquelo con engram nexo sa token revoke.\n", res.OldToken["id"])
		case res.OldToken["revoked"] == true:
			_, _ = fmt.Fprintf(stderr, "El token anterior %v quedó revocado.\n", res.OldToken["id"])
		default:
			_, _ = fmt.Fprintf(stderr, "El token anterior %v vale hasta %v.\n", res.OldToken["id"], res.OldToken["expires_at"])
		}
	}
	if *out != "" {
		_, _ = fmt.Fprintf(stderr, "Token guardado en %s. Se muestra una sola vez: cárguelo como secreto del CI y borre el archivo.\n", *out)
		return nil
	}
	_, _ = fmt.Fprintln(stderr, "Se muestra una sola vez: cárguelo como secreto del CI (por ejemplo NEXO_TOKEN).")
	_, _ = fmt.Fprintln(stdout, res.RawToken)
	return nil
}
