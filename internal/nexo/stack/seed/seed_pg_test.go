package seed

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudserver"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/stackapi"
)

type testAuth struct {
	r *cloudauth.PrincipalResolver
}

func (a testAuth) Authorize(*http.Request) error {
	return errors.New("legacy Authorize no se usa con resolución de principales")
}

func (a testAuth) ResolveBearerToken(ctx context.Context, token string) (cloudauth.Principal, error) {
	return a.r.ResolveBearerToken(ctx, token)
}

type testTokenLookup struct {
	cs *cloudstore.CloudStore
}

func (l testTokenLookup) FindManagedTokenByHash(ctx context.Context, hash string) (cloudauth.ManagedTokenRecord, cloudauth.Principal, error) {
	token, p, err := l.cs.FindPrincipalTokenByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, cloudstore.ErrPrincipalTokenNotFound) {
			return cloudauth.ManagedTokenRecord{}, cloudauth.Principal{}, cloudauth.ErrUnknownToken
		}
		return cloudauth.ManagedTokenRecord{}, cloudauth.Principal{}, err
	}
	kind := cloudauth.PrincipalKindHuman
	if p.Kind == cloudstore.PrincipalKindServiceAccount {
		kind = cloudauth.PrincipalKindServiceAccount
	}
	role := cloudauth.RoleMember
	if p.Role == cloudstore.PrincipalRoleAdmin {
		role = cloudauth.RoleAdmin
	}
	return cloudauth.ManagedTokenRecord{ID: token.ID, PrincipalID: token.PrincipalID, RevokedAt: token.RevokedAt},
		cloudauth.Principal{ID: p.ID, Kind: kind, DisplayName: p.DisplayName, Role: role, Enabled: p.Enabled, Source: cloudauth.PrincipalSourceManagedToken}, nil
}

func (l testTokenLookup) MarkManagedTokenUsed(ctx context.Context, tokenID string) error {
	err := l.cs.MarkPrincipalTokenUsed(ctx, tokenID)
	switch {
	case errors.Is(err, cloudstore.ErrPrincipalTokenRevoked):
		return cloudauth.ErrTokenRevoked
	case errors.Is(err, cloudstore.ErrPrincipalDisabled):
		return cloudauth.ErrPrincipalDisabled
	}
	return err
}

func newSeedPGEnv(t *testing.T) (stackapi.Seeder, stackapi.Env) {
	t.Helper()
	t.Setenv("NEXO_KB_EMBEDDER", "off")
	t.Setenv("NEXO_KB_RERANKER", "off")
	t.Setenv("NEXO_RATE_LIMITS", `{"nexo":{"ip":10000,"token":10000}}`)
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("se salta la prueba de integración con Postgres: falta CLOUDSTORE_TEST_DSN")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("se salta la prueba: CLOUDSTORE_TEST_DSN debe ser una URL de conexión postgres://")
	}

	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Hostname() != "127.0.0.1" || parsed.Port() != "55437" || parsed.Path != "/nexoseed" {
		t.Fatal("esta prueba requiere la base local nexoseed en 127.0.0.1:55437")
	}
	ctx := context.Background()
	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("conectar a postgres: %s", redact.Text(err.Error()))
	}
	t.Cleanup(func() { _ = adminDB.Close() })

	schema := fmt.Sprintf("seed_int_test_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("crear schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		if _, err := adminDB.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Errorf("no se pudo limpiar el esquema de prueba: %s", redact.Text(err.Error()))
		}
	})

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	cs, err := cloudstore.New(cloud.Config{DSN: dsn + sep + "search_path=" + schema})
	if err != nil {
		t.Fatalf("iniciar cloudstore en schema %s: %s", schema, redact.Text(err.Error()))
	}
	t.Cleanup(func() { _ = cs.Close() })

	pepper := make([]byte, 32)
	if _, err := rand.Read(pepper); err != nil {
		t.Fatal(err)
	}
	hasher, err := cloudauth.NewManagedTokenHasher(pepper)
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}

	resolver := cloudauth.NewPrincipalResolver(cloudauth.ResolverConfig{
		Hasher:        hasher,
		ManagedTokens: testTokenLookup{cs: cs},
	})

	srv := cloudserver.New(cs, testAuth{r: resolver}, 0,
		cloudserver.WithAdminIdentityStore(cs),
		cloudserver.WithManagedTokenHasher(hasher),
	)

	ts := seedHTTPServer(t, srv.Handler())

	seederInstance := New()
	personas := seederInstance.Personas()
	tokens := map[string]string{}

	// Solo el fixture crea identidades; toda carga de contenido usa HTTP.
	for _, p := range personas {
		role := authz.RoleMiembro
		if p.Plataforma != "" {
			role = authz.Role(p.Plataforma)
		}
		createCtx := authz.WithCreateRequest(ctx, authz.CreateRequest{Role: role, Source: "seed-test"})
		user, err := cs.CreateHumanUser(createCtx, cloudstore.CreateHumanUserParams{Username: p.Username, Email: p.Email, DisplayName: p.Username, Role: "member"})
		if err != nil {
			t.Fatalf("crear persona %s: %s", p.Username, redact.Text(err.Error()))
		}
		id := user.PrincipalID

		// Emitir token administrado
		mt, err := cloudauth.GenerateManagedToken("live")
		if err != nil {
			t.Fatalf("generar token: %v", err)
		}
		h, err := hasher.Hash(mt.Raw)
		if err != nil {
			t.Fatalf("hasher.Hash: %v", err)
		}
		_, err = cs.CreatePrincipalTokenWithAudit(security.WithTokenIssue(ctx, security.TokenIssue{Days: 90, Scope: "write"}),
			cloudstore.CreatePrincipalTokenParams{PrincipalID: id, TokenPrefix: mt.Prefix, TokenHash: h, Name: p.Username},
			cloudstore.AuthAuditEvent{ActorSource: "test", Action: "token.create", Outcome: "success"})
		if err != nil {
			t.Fatalf("guardar token de %s: %v", p.Username, err)
		}
		tokens[p.Username] = mt.Raw
	}

	env := stackapi.Env{
		ServerURL: ts.URL,
		Tokens:    tokens,
	}

	return seederInstance, env
}

func TestSeedPostgresIntegration(t *testing.T) {
	seederInstance, env := newSeedPGEnv(t)
	ctx := context.Background()
	tokens := env.Tokens
	tsURL := env.ServerURL
	// 1. Carga inicial
	rep, err := seederInstance.Load(ctx, env, stackapi.LoadOptions{Example: true})
	if err != nil {
		t.Fatalf("Load falló: %v", err)
	}

	if rep.Spaces != 6 {
		t.Errorf("rep.Spaces = %d, want 6", rep.Spaces)
	}
	if rep.Documents != 12 {
		t.Errorf("rep.Documents = %d, want 12", rep.Documents)
	}
	if rep.Memories != 5 { // 3 documentos + 2 propuestas; 1 omitida (obsoleta)
		t.Errorf("rep.Memories = %d, want 5", rep.Memories)
	}
	if rep.Proposals != 3 {
		t.Errorf("rep.Proposals = %d, want 3", rep.Proposals)
	}

	// 2. Repetir Load debe ser idempotente y no duplicar
	rep2, err := seederInstance.Load(ctx, env, stackapi.LoadOptions{Example: true})
	if err != nil {
		t.Fatalf("Load (segunda llamada) falló: %v", err)
	}
	if rep2.Spaces != 0 {
		t.Errorf("rep2.Spaces = %d, want 0 (sin cambios)", rep2.Spaces)
	}
	if rep2.Documents != 0 {
		t.Errorf("rep2.Documents = %d, want 0 (sin cambios)", rep2.Documents)
	}
	if rep2.Memories != 0 {
		t.Errorf("rep2.Memories = %d, want 0 (sin cambios)", rep2.Memories)
	}
	if rep2.Proposals != 0 {
		t.Errorf("rep2.Proposals = %d, want 0 (sin cambios)", rep2.Proposals)
	}

	// 3. El lector no ve operaciones (restringido sin acceso -> 404 hidden)
	lectorReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, tsURL+"/nexo/kb/v1/spaces/operaciones", nil)
	lectorReq.Header.Set("Authorization", "Bearer "+tokens["lector"])
	lectorResp, err := http.DefaultClient.Do(lectorReq)
	if err != nil {
		t.Fatalf("petición lector: %v", err)
	}
	_ = lectorResp.Body.Close()
	if lectorResp.StatusCode != http.StatusNotFound && lectorResp.StatusCode != http.StatusForbidden {
		t.Errorf("lector no debe ver operaciones: código HTTP recibido %d", lectorResp.StatusCode)
	}

	// 4. El auditor ve eventos sin contenido (payload_withheld: true)
	auditorReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, tsURL+"/nexo/kb/v1/spaces/operaciones/events?limit=50", nil)
	auditorReq.Header.Set("Authorization", "Bearer "+tokens["auditor"])
	auditorResp, err := http.DefaultClient.Do(auditorReq)
	if err != nil {
		t.Fatalf("petición auditor: %v", err)
	}
	var evOut struct {
		Items []domain.Event `json:"items"`
	}
	if err := json.NewDecoder(auditorResp.Body).Decode(&evOut); err != nil {
		t.Fatal(err)
	}
	_ = auditorResp.Body.Close()
	if auditorResp.StatusCode != http.StatusOK {
		t.Errorf("auditor debe poder leer eventos de auditoría: HTTP %d", auditorResp.StatusCode)
	}
	if len(evOut.Items) == 0 {
		t.Fatal("el auditor debe recibir eventos reales")
	}
	if len(evOut.Items) > 0 {
		withheld := false
		denied := false
		for _, it := range evOut.Items {
			if it.Details["payload_withheld"] == true {
				withheld = true
			} else if it.Action == domain.ActionDocumentPublished {
				t.Error("publicación sin retención de contenido")
			}
			if it.Action == domain.ActionAuthzDenied {
				denied = true
			}
			for _, field := range []string{"content", "content_md", "title", "body"} {
				if _, ok := it.Details[field]; ok {
					t.Errorf("campo de contenido visible al auditor: %s", field)
				}
			}
		}
		if !denied {
			t.Error("falta el evento de acceso denegado")
		}
		if !withheld {
			t.Errorf("los eventos de operaciones para el auditor deben tener payload_withheld: true")
		}
	}

	// 5. El proponente no ve las propuestas de otros
	propReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, tsURL+"/nexo/kb/v1/proposals", nil)
	propReq.Header.Set("Authorization", "Bearer "+tokens["proponente"])
	propResp, err := http.DefaultClient.Do(propReq)
	if err != nil {
		t.Fatalf("petición proponente: %v", err)
	}
	var propOut struct {
		Items []struct {
			AuthorID string `json:"author_principal_id"`
		} `json:"items"`
	}
	_ = json.NewDecoder(propResp.Body).Decode(&propOut)
	_ = propResp.Body.Close()
	if propResp.StatusCode != http.StatusOK {
		t.Errorf("proponente listar propuestas: HTTP %d", propResp.StatusCode)
	}

	auditorAPI, _ := seederInstance.(*seeder).clientFor(env, "auditor")
	var auditorSpace domain.Space
	if err := auditorAPI.Do(ctx, "GET", spacePath("backend"), nil, &auditorSpace); err != nil {
		t.Fatal(err)
	}
	if auditorSpace.Role != "lector" {
		t.Error("cambió la herencia de lectura del auditor; revisar la limitación documentada")
	}
	t.Log("limitación del servidor: el auditor hereda lector en espacios abiertos")

	// Comprobar la matriz observable y los responsables (incluyen publicador).
	for _, sp := range seederInstance.(*seeder).spaces {
		for _, ra := range seederInstance.(*seeder).roles {
			if ra.Space != sp.Slug {
				continue
			}
			api, _ := seederInstance.(*seeder).clientFor(env, ra.Principal)
			var got domain.Space
			if err := api.Do(ctx, "GET", spacePath(sp.Slug), nil, &got); err != nil {
				t.Fatal(err)
			}
			want := ra.Role
			if ra.Principal == sp.Owner {
				want = "responsable"
			}
			if got.Role != want {
				t.Errorf("rol %s en %s: %s, esperado %s", ra.Principal, sp.Slug, got.Role, want)
			}
		}
		owner, _ := seederInstance.(*seeder).clientFor(env, sp.Owner)
		var got domain.Space
		if err := owner.Do(ctx, "GET", spacePath(sp.Slug), nil, &got); err != nil {
			t.Fatal(err)
		}
		if got.Role != "responsable" {
			t.Errorf("falta responsable en %s", sp.Slug)
		}
		docs, err := listDocuments(ctx, owner, sp.Slug)
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{"backend": 2, "datos": 1, "frontend": 4, "general": 2, "operaciones": 3, "transversal": 3}
		if len(docs) != counts[sp.Slug] {
			t.Errorf("inventario de %s: %d", sp.Slug, len(docs))
		}
		for _, d := range docs {
			if d.CurrentVersion != 1 {
				t.Errorf("duplicación de versión en %s", sp.Slug)
			}
			var view service.DocumentView
			if err := owner.Do(ctx, "GET", spacePath(sp.Slug)+"/documents/"+url.PathEscape(d.DocKey), nil, &view); err != nil {
				t.Fatal(err)
			}
			if view.Version.Origin.Source != exampleSource {
				t.Errorf("falta origen de semilla en %s", sp.Slug)
			}
			if sp.Slug == "transversal" && !strings.Contains(view.Version.ContentMD, "**Qué**:") {
				t.Error("la memoria perdió el contenido original")
			}
		}
	}
	pub, _ := seederInstance.(*seeder).clientFor(env, "publicador")
	prop, _ := seederInstance.(*seeder).clientFor(env, "proponente")
	props, err := listProposals(ctx, prop, "backend", true)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, p := range props {
		statuses[p.DocKey] = p.Status
	}
	if statuses["backend/reintentos-jitter"] != "open" || statuses["backend/estandarizar-fechas-log"] != "withdrawn" {
		t.Errorf("estados de propuestas inesperados")
	}
	var alien store.Proposal
	if err := pub.Do(ctx, "POST", spacePath("backend")+"/proposals", service.ProposalInput{DocKey: "propia-del-publicador", Title: "Propuesta propia", Content: "Contenido propio"}, &alien); err != nil {
		t.Fatal(err)
	}
	own, err := listProposals(ctx, prop, "backend", false)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(own, func(p store.Proposal) bool { return p.ID == alien.ID }) {
		t.Error("el proponente ve una propuesta ajena")
	}
	if err := prop.Do(ctx, "GET", "/nexo/kb/v1/proposals/"+alien.ID, nil, nil); !client.IsStatus(err, http.StatusNotFound) {
		t.Error("el proponente puede abrir propuesta ajena")
	}
	// La propuesta propia se conserva: backend permanece activo incluso si ya se retiró.
	if err := pub.Do(ctx, "POST", "/nexo/kb/v1/proposals/"+alien.ID+":withdraw", map[string]string{"note": "Retiro de prueba propia"}, nil); err != nil {
		t.Fatal(err)
	}

	// 6. Probar RemoveExample
	remRep, err := seederInstance.RemoveExample(ctx, env)
	if err != nil {
		t.Fatalf("RemoveExample falló: %v", err)
	}
	if remRep.Spaces != 5 {
		t.Errorf("remRep.Spaces = %d, want 5 (backend conserva una propuesta propia)", remRep.Spaces)
	}
	if remRep.Documents != 12 || remRep.Memories != 3 || remRep.Proposals != 4 {
		t.Errorf("retiro incompleto: documentos %d, memorias %d, propuestas %d", remRep.Documents, remRep.Memories, remRep.Proposals)
	}
	if len(remRep.Notes) == 0 {
		t.Errorf("remRep.Notes debe reportar la falta de API para borrado permanente de espacios")
	}
}

func TestSeedOwnPostgres(t *testing.T) {
	loader, env := newSeedPGEnv(t)
	ctx := context.Background()
	dir := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", []byte("# Guía propia\n\nTexto de bienvenida."))
	write("Backend/guia.md", []byte("# Guía backend\n\nTexto limpio."))
	write("Backend/anidada/guia.md", []byte("# Guía anidada\n\nOtro texto."))
	write("Frontend/glosario.txt", []byte("Glosario propio."))
	word, err := seedFS.ReadFile("data/frontend/lineamiento-ejemplo.docx")
	if err != nil {
		t.Fatal(err)
	}
	write("Frontend/guia.docx", word)
	write(".git/config.md", []byte("# Ignorar"))
	write("imagen.png", []byte("imagen"))
	write("binario.md", []byte{0, 1, 2})
	write("invalido.docx", []byte("PK-malformado"))
	fake := "Authorization: Bearer " + strings.Repeat("x", 40)
	write("credencial.md", []byte(fake))
	var docx bytes.Buffer
	zw := zip.NewWriter(&docx)
	part, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = part.Write([]byte("<w:document xmlns:w=\"urn:w\"><w:body><w:p><w:r><w:t>Authorization: </w:t></w:r><w:r><w:t>Bearer " + strings.Repeat("x", 40) + "</w:t></w:r></w:p></w:body></w:document>"))
	if err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	write("credencial.docx", docx.Bytes())
	t.Setenv(service.EnvUploadMaxFiles, "1")
	opts := stackapi.OwnOptions{Dir: dir, PerSubfolder: true, SpacePrefix: "mis"}
	rep, err := loader.LoadOwn(ctx, env, opts)
	if err != nil {
		t.Fatalf("LoadOwn: %s", redact.Text(err.Error()))
	}
	if rep.Spaces != 3 || rep.Documents != 5 || len(rep.Skipped) != 4 {
		t.Fatalf("carga propia: espacios %d, documentos %d, omitidos %d", rep.Spaces, rep.Documents, len(rep.Skipped))
	}
	if strings.Contains(fmt.Sprint(rep), strings.Repeat("x", 40)) {
		t.Fatal("se filtró contenido omitido")
	}
	rep2, err := loader.LoadOwn(ctx, env, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Spaces != 0 || rep2.Documents != 0 {
		t.Error("LoadOwn no es idempotente")
	}
	api, _ := loader.(*seeder).clientFor(env, "publicador")
	docs, err := listDocuments(ctx, api, "mis-backend")
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, d := range docs {
		keys = append(keys, d.DocKey)
	}
	if !slices.Contains(keys, "backend/guia") || !slices.Contains(keys, "backend/anidada/guia") {
		t.Error("se perdieron rutas relativas")
	}
	one, err := loader.LoadOwn(ctx, env, stackapi.OwnOptions{Dir: dir, SpacePrefix: "unico"})
	if err != nil {
		t.Fatal(err)
	}
	if one.Spaces != 1 || one.Documents != 5 {
		t.Error("carga en espacio único incorrecta")
	}
	rem, err := loader.RemoveExample(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if rem.Spaces != 0 || rem.Documents != 0 {
		t.Error("el retiro tocó espacios propios")
	}
}

func TestSeedRemovePreservesOwnPostgres(t *testing.T) {
	loader, env := newSeedPGEnv(t)
	ctx := context.Background()
	if _, err := loader.Load(ctx, env, stackapi.LoadOptions{Example: true}); err != nil {
		t.Fatal(err)
	}
	api, _ := loader.(*seeder).clientFor(env, "publicador")
	docs, err := listDocuments(ctx, api, "backend")
	if err != nil {
		t.Fatal(err)
	}
	// Editar una clave de ejemplo con contenido propio también debe preservarse.
	key := docs[0].DocKey
	var out service.BatchResult
	if err := api.Do(ctx, "POST", spacePath("backend")+"/documents:batch", service.BatchRequest{Documents: []service.PublishDoc{{DocKey: key, Path: key + ".md", Title: "Documento propio", Category: "lineamiento", Content: "# Documento propio\n\nTexto de la persona.", Origin: domain.Origin{Kind: domain.OriginManual}}}}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Summary[domain.ResultUpdated] != 1 {
		_, why := changedDocuments(out)
		t.Fatalf("no se creó la edición propia: %v", why)
	}
	rem, err := loader.RemoveExample(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if rem.Spaces != 5 || rem.Documents != 11 || rem.Memories != 3 || rem.Proposals != 4 {
		t.Errorf("retiro selectivo incorrecto: espacios %d documentos %d memorias %d propuestas %d", rem.Spaces, rem.Documents, rem.Memories, rem.Proposals)
	}
	var view service.DocumentView
	if err := api.Do(ctx, "GET", spacePath("backend")+"/documents/"+url.PathEscape(key), nil, &view); err != nil {
		t.Fatal(err)
	}
	if view.Document.Status != domain.DocActive || view.Version.Title != "Documento propio" {
		t.Error("el retiro modificó contenido propio")
	}
	again, err := loader.RemoveExample(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if again.Spaces != 0 || again.Documents != 0 || again.Memories != 0 || again.Proposals != 0 {
		t.Error("retiro no idempotente")
	}
}

func TestSeedRemoveCleanPostgres(t *testing.T) {
	loader, env := newSeedPGEnv(t)
	ctx := context.Background()
	if _, err := loader.Load(ctx, env, stackapi.LoadOptions{Example: true}); err != nil {
		t.Fatal(err)
	}
	rep, err := loader.RemoveExample(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Spaces != 6 || rep.Documents != 12 || rep.Memories != 3 || rep.Proposals != 4 {
		t.Errorf("retiro completo incorrecto: espacios %d documentos %d memorias %d propuestas %d", rep.Spaces, rep.Documents, rep.Memories, rep.Proposals)
	}
	again, err := loader.RemoveExample(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if again.Spaces != 0 || again.Documents != 0 || again.Memories != 0 || again.Proposals != 0 {
		t.Error("retiro completo no idempotente")
	}
}

func TestSeedOwnSingleAdminRequiresOwnerPostgres(t *testing.T) {
	loader, env := newSeedPGEnv(t)
	env.Tokens = map[string]string{"superadmin": env.Tokens["superadmin"]}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "guia.md"), []byte("# Guía\n\nContenido propio."), 0600); err != nil {
		t.Fatal(err)
	}

	rep, err := loader.LoadOwn(context.Background(), env, stackapi.OwnOptions{Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "responsable distinto") || rep.Spaces != 0 || rep.Documents != 0 {
		t.Error("crear con una sola identidad debe explicar el responsable distinto sin efectos parciales")
	}
}
