// Package seed implementa la carga y retiro de datos de ejemplo y propios
// para `engram nexo stack` (stackapi.Seeder).
package seed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/cli"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/source"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/stackapi"
)

// SpaceDef define un espacio de ejemplo a crear.
type SpaceDef struct {
	Slug        string
	Name        string
	Description string
	Visibility  string
	DefaultRole string
	Owner       string
}

// RoleAssignment fija la asignación de un rol en un ámbito de espacio.
type RoleAssignment struct {
	Space     string
	Principal string
	Role      string
}

// seeder implementa stackapi.Seeder.
type seeder struct {
	spaces          []SpaceDef
	roles           []RoleAssignment
	personas        []stackapi.Persona
	sampleProposals []sampleProposalDef
}

type sampleProposalDef struct {
	Space     string
	DocKey    string
	Title     string
	Category  string
	Content   string
	Rationale string
	Withdraw  bool
}

// New crea una instancia de stackapi.Seeder lista para usar.
func New() stackapi.Seeder {
	return &seeder{
		personas: defaultPersonas(),
		spaces:   defaultSpaces(),
		roles:    defaultRoleMatrix(),
		sampleProposals: []sampleProposalDef{
			{
				Space:     "backend",
				DocKey:    "backend/reintentos-jitter",
				Title:     "Agregar jitter a la política de reintentos (EJEMPLO)",
				Category:  "lineamiento",
				Content:   "# Agregar jitter a la política de reintentos (EJEMPLO)\n\n> Nota: Propuesta de (EJEMPLO) para el recorrido interactivo.\n\nSe propone añadir jitter a la política de reintentos de llamadas HTTP entre microservicios para mitigar el efecto manada.",
				Rationale: "Evitar tormentas de reintentos coordinados tras una caída.",
				Withdraw:  false,
			},
			{
				Space:     "frontend",
				DocKey:    "frontend/prohibir-estilos-en-linea",
				Title:     "Prohibir estilos en línea en formularios (EJEMPLO)",
				Category:  "lineamiento",
				Content:   "# Prohibir estilos en línea en formularios (EJEMPLO)\n\n> Nota: Propuesta de (EJEMPLO) para revisión y rechazo en el recorrido.\n\nSe propone prohibir terminantemente atributos style en línea en componentes de formulario.",
				Rationale: "Garantizar coherencia visual con el sistema de diseño.",
				Withdraw:  false,
			},
			{
				Space:     "backend",
				DocKey:    "backend/estandarizar-fechas-log",
				Title:     "Estandarizar formato de fechas en logs (EJEMPLO)",
				Category:  "estandar",
				Content:   "# Estandarizar formato de fechas en logs (EJEMPLO)\n\n> Nota: Propuesta de (EJEMPLO) para ensayar el retiro por parte del autor.\n\nSe propone usar formato ISO-8601 UTC en todos los timestamps de logs.",
				Rationale: "Unificar el formato temporal para facilitar la ingestión en sistemas de observabilidad.",
				Withdraw:  true,
			},
		},
	}
}

func defaultPersonas() []stackapi.Persona {
	return []stackapi.Persona{
		{Username: "superadmin", Email: "superadmin@demo.local", Plataforma: "superadmin"},
		{Username: "admin", Email: "admin@demo.local", Plataforma: "admin"},
		{Username: "publicador", Email: "publicador@demo.local", Plataforma: ""},
		{Username: "proponente", Email: "proponente@demo.local", Plataforma: ""},
		{Username: "lector", Email: "lector@demo.local", Plataforma: ""},
		{Username: "auditor", Email: "auditor@demo.local", Plataforma: "auditor"},
	}
}

func defaultSpaces() []SpaceDef {
	return []SpaceDef{
		{
			Slug:        "backend",
			Name:        "Backend (EJEMPLO)",
			Description: "Lineamientos de desarrollo backend y servicios (EJEMPLO)",
			Visibility:  "open",
			DefaultRole: "lector",
			Owner:       "publicador",
		},
		{
			Slug:        "datos",
			Name:        "Datos (EJEMPLO)",
			Description: "Estándares y nomenclatura de bases de datos (EJEMPLO)",
			Visibility:  "open",
			DefaultRole: "lector",
			Owner:       "admin",
		},
		{
			Slug:        "frontend",
			Name:        "Frontend (EJEMPLO)",
			Description: "Lineamientos y catálogo de frontend (EJEMPLO)",
			Visibility:  "open",
			DefaultRole: "lector",
			Owner:       "publicador",
		},
		{
			Slug:        "general",
			Name:        "General (EJEMPLO)",
			Description: "Lineamientos generales y glosario (EJEMPLO)",
			Visibility:  "open",
			DefaultRole: "lector",
			Owner:       "admin",
		},
		{
			Slug:        "operaciones",
			Name:        "Operaciones (EJEMPLO)",
			Description: "Procedimientos operativos restringidos (EJEMPLO)",
			Visibility:  "restricted",
			DefaultRole: "",
			Owner:       "publicador",
		},
		{
			Slug:        "transversal",
			Name:        "Transversal (EJEMPLO)",
			Description: "Memorias transversales importadas (EJEMPLO)",
			Visibility:  "open",
			DefaultRole: "lector",
			Owner:       "admin",
		},
	}
}

func defaultRoleMatrix() []RoleAssignment {
	return []RoleAssignment{
		// backend (abierto, owner: publicador)
		{"backend", "publicador", "publicador"},
		{"backend", "proponente", "proponente"},
		{"backend", "lector", "lector"},

		// datos (abierto, owner: admin)
		{"datos", "publicador", "lector"},
		{"datos", "proponente", "lector"},
		{"datos", "lector", "lector"},

		// frontend (abierto, owner: publicador)
		{"frontend", "publicador", "publicador"},
		{"frontend", "proponente", "proponente"},
		{"frontend", "lector", "lector"},

		// general (abierto, owner: admin)
		{"general", "publicador", "lector"},
		{"general", "proponente", "lector"},
		{"general", "lector", "lector"},

		// operaciones (restringido, owner: publicador)
		{"operaciones", "publicador", "publicador"},
		{"operaciones", "superadmin", "publicador"},

		// transversal (abierto, owner: admin; lector para todos, publicador publicador, proponente proponente)
		{"transversal", "publicador", "publicador"},
		{"transversal", "proponente", "proponente"},
		{"transversal", "lector", "lector"},
	}
}

// Personas devuelve las 6 identidades de prueba del entorno demo.
func (s *seeder) Personas() []stackapi.Persona {
	out := make([]stackapi.Persona, len(s.personas))
	copy(out, s.personas)
	return out
}

func (s *seeder) clientFor(env stackapi.Env, username string) (*client.API, error) {
	tok, ok := env.Tokens[username]
	if !ok || strings.TrimSpace(tok) == "" {
		return nil, fmt.Errorf("no hay token para el usuario %q en el entorno", username)
	}
	st := client.Settings{Server: strings.TrimRight(strings.TrimSpace(env.ServerURL), "/"), Token: strings.TrimSpace(tok)}
	return client.NewAPI(st, 30*time.Second), nil
}

// Load crea espacios, asignaciones de rol, documentos, memorias y propuestas de ejemplo.
func (s *seeder) Load(ctx context.Context, env stackapi.Env, opts stackapi.LoadOptions) (stackapi.Report, error) {
	var rep stackapi.Report
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	if !opts.Example {
		rep.Notes = append(rep.Notes, "sin cambios: ejemplo no solicitado")
		return rep, nil
	}

	// Token de administración para crear espacios
	adminUser := "superadmin"
	if _, ok := env.Tokens[adminUser]; !ok {
		adminUser = "admin"
	}
	adminAPI, err := s.clientFor(env, adminUser)
	if err != nil {
		return rep, fmt.Errorf("cliente de administración: %w", err)
	}

	// Validar las identidades que necesita el ejemplo completo antes de escribir.
	for _, user := range []string{"admin", "publicador", "proponente", "lector"} {
		if _, err := s.clientFor(env, user); err != nil {
			return rep, err
		}
	}

	// 1. Crear los 6 espacios
	for _, sp := range s.spaces {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		in := service.CreateSpaceInput{
			Slug:        sp.Slug,
			Name:        sp.Name,
			Description: sp.Description,
			Visibility:  sp.Visibility,
			DefaultRole: sp.DefaultRole,
			Owner:       sp.Owner,
		}
		var out domain.Space
		err := adminAPI.Do(ctx, http.MethodPost, "/nexo/kb/v1/spaces", in, &out)
		if err != nil {
			if client.IsStatus(err, http.StatusConflict) {
				// No adoptar un espacio ajeno solo porque comparte el slug.
				var existing domain.Space
				ownerAPI, err := s.clientFor(env, sp.Owner)
				if err != nil {
					return rep, err
				}
				if err := ownerAPI.Do(ctx, http.MethodGet, spacePath(sp.Slug), nil, &existing); err != nil {
					return rep, err
				}
				if existing.Name != sp.Name || existing.Description != sp.Description {
					return rep, fmt.Errorf("el espacio %s ya existe y no pertenece a la semilla", sp.Slug)
				}
				if env.Logf != nil {
					env.Logf("espacio %s ya existe (sin cambios)", sp.Slug)
				}
			} else {
				return rep, fmt.Errorf("crear espacio %s: %w", sp.Slug, err)
			}
		} else {
			rep.Spaces++
		}
	}

	// 2. Asignar matriz de roles de espacio
	for _, ra := range s.roles {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		ownerUser := "publicador"
		for _, sp := range s.spaces {
			if sp.Slug == ra.Space {
				ownerUser = sp.Owner
				break
			}
		}
		ownerAPI, err := s.clientFor(env, ownerUser)
		if err != nil {
			return rep, fmt.Errorf("cliente del responsable de %s: %w", ra.Space, err)
		}
		// El responsable incluye las capacidades de publicador; no se degrada (R-9).
		if ra.Principal == ownerUser {
			continue
		}
		path := fmt.Sprintf("/nexo/authz/v1/scopes/space/%s/members/%s", url.PathEscape(ra.Space), url.PathEscape(ra.Principal))
		body := map[string]string{"role": ra.Role}
		var change authz.BindingChange
		err = ownerAPI.Do(ctx, http.MethodPut, path, body, &change)
		if err != nil {
			return rep, fmt.Errorf("asignar rol %s en %s a %s: %w", ra.Role, ra.Space, ra.Principal, err)
		}
	}

	// 3. Cargar los 12 documentos del inventario en los espacios correspondientes
	pubAPI, err := s.clientFor(env, "publicador")
	if err != nil {
		return rep, fmt.Errorf("cliente publicador: %w", err)
	}

	docSpaces := []string{"backend", "datos", "frontend", "general", "operaciones"}
	for _, slug := range docSpaces {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		// Determinamos el usuario con permisos de publicación en este espacio
		uploaderAPI := pubAPI
		if slug == "datos" || slug == "general" {
			// En datos y general el owner es admin (que tiene rol responsable)
			if aAPI, aErr := s.clientFor(env, "admin"); aErr == nil {
				uploaderAPI = aAPI
			}
		}
		createdDocs, err := s.uploadSpaceDir(ctx, uploaderAPI, slug)
		rep.Documents += createdDocs
		if err != nil {
			return rep, fmt.Errorf("publicar documentos en %s: %w", slug, err)
		}
	}

	// 4. Cargar las 6 memorias en el espacio transversal compatible con import-transversal
	transMemAPI := pubAPI
	memCreated, skippedMems, err := s.loadTransversalMemories(ctx, transMemAPI, env)
	rep.Memories += memCreated
	rep.Skipped = append(rep.Skipped, skippedMems...)
	if err != nil {
		return rep, fmt.Errorf("cargar memorias transversales: %w", err)
	}

	// 5. Crear 3 propuestas con el token de proponente
	propAPI, err := s.clientFor(env, "proponente")
	if err != nil {
		return rep, fmt.Errorf("cliente proponente: %w", err)
	}
	propsCreated, err := s.createSampleProposals(ctx, propAPI)
	rep.Proposals += propsCreated
	if err != nil {
		return rep, fmt.Errorf("crear propuestas de ejemplo: %w", err)
	}

	// 6. Provocar acceso denegado a propósito (el lector intenta acceder a operaciones)
	if lectorAPI, err := s.clientFor(env, "lector"); err == nil {
		var ignored any
		err := lectorAPI.Do(ctx, http.MethodGet, "/nexo/kb/v1/spaces/operaciones", nil, &ignored)
		if !client.IsStatus(err, http.StatusNotFound) && !client.IsStatus(err, http.StatusForbidden) {
			if err == nil {
				return rep, errors.New("el lector tiene acceso inesperado a operaciones")
			}
			return rep, err
		}
		rep.Notes = append(rep.Notes, "acceso denegado provocado a propósito (lector intentó leer operaciones)")
	}

	if rep.Spaces == 0 && rep.Documents == 0 && rep.Memories == 0 && rep.Proposals == 0 {
		rep.Notes = append(rep.Notes, "sin cambios")
	}
	return rep, nil
}

// uploadSpaceDir lee los archivos de data/<slug> de seedFS y los sube vía multipart a /spaces/<slug>/uploads
func (s *seeder) uploadSpaceDir(ctx context.Context, api *client.API, slug string) (int, error) {
	dirPath := "data/" + slug
	entries, err := seedFS.ReadDir(dirPath)
	if err != nil {
		return 0, fmt.Errorf("leer directorio embebido %s: %w", dirPath, err)
	}
	if len(entries) == 0 {
		return 0, nil
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		filePath := dirPath + "/" + name
		data, err := seedFS.ReadFile(filePath)
		if err != nil {
			return 0, fmt.Errorf("leer archivo embebido %s: %w", filePath, err)
		}
		uploadName := name
		if slug == "general" && name == "glosario.txt" {
			uploadName = "glosario-texto.txt"
		}
		fw, err := mw.CreateFormFile("file", uploadName)
		if err != nil {
			return 0, err
		}
		if _, err := fw.Write(data); err != nil {
			return 0, err
		}
	}

	_ = mw.WriteField("source", source.KindDir)
	_ = mw.WriteField("collection", exampleSource)
	_ = mw.WriteField("tags", exampleSource)
	_ = mw.WriteField("change_note", "Carga inicial de semilla (EJEMPLO)")
	if err := mw.Close(); err != nil {
		return 0, err
	}

	var res service.BatchResult
	path := fmt.Sprintf("/nexo/kb/v1/spaces/%s/uploads", url.PathEscape(slug))
	if err := api.DoMultipart(ctx, path, &body, mw.FormDataContentType(), &res); err != nil {
		return 0, err
	}

	return changedDocuments(res)
}

// loadTransversalMemories usa el plan del importador y conserva el contenido original.
func (s *seeder) loadTransversalMemories(ctx context.Context, pubAPI *client.API, env stackapi.Env) (int, []string, error) {
	raw, err := seedFS.ReadFile("data/memorias.json")
	if err != nil {
		return 0, nil, err
	}
	var data struct{ Observations []cli.Memory }
	if err := json.Unmarshal(raw, &data); err != nil {
		return 0, nil, err
	}
	propAPI, err := s.clientFor(env, "proponente")
	if err != nil {
		return 0, nil, err
	}
	existing, err := listProposals(ctx, propAPI, "transversal", true)
	if err != nil {
		return 0, nil, err
	}
	var docs []service.PublishDoc
	var skipped []string
	n := 0
	for i, it := range cli.PlanImport(data.Observations) {
		if err := ctx.Err(); err != nil {
			return n, skipped, err
		}
		content := "# " + it.Title + "\n\n" + data.Observations[i].Content + "\n"
		if redact.Has(content) {
			return n, skipped, errors.New("la memoria de ejemplo contiene formato de credencial")
		}
		switch it.Action {
		case "document":
			docs = append(docs, service.PublishDoc{DocKey: it.DocKey, Path: it.DocKey + ".md", Title: it.Title, Category: it.Category, Content: content, Tags: []string{cli.TransversalProject, exampleSource}, Origin: domain.Origin{Kind: domain.OriginImport, Source: exampleSource, Path: fmt.Sprintf("engram:%s#%d", cli.TransversalProject, it.MemoryID)}, ChangeNote: "import-transversal (EJEMPLO)"})
		case "proposal":
			if slices.ContainsFunc(existing, func(p store.Proposal) bool { return p.DocKey == it.DocKey }) {
				continue
			}
			var p store.Proposal
			if err := propAPI.Do(ctx, http.MethodPost, spacePath("transversal")+"/proposals", service.ProposalInput{DocKey: it.DocKey, Title: it.Title, Category: it.Category, Content: content, Rationale: "Importada de memoria transversal de ejemplo.", Tags: []string{cli.TransversalProject, exampleSource}}, &p); err != nil {
				return n, skipped, err
			}
			n++
		case "skip":
			skipped = append(skipped, fmt.Sprintf("memoria #%d (%s): %s", it.MemoryID, it.Title, it.Detail))
		}
	}
	if len(docs) > 0 {
		var res service.BatchResult
		if err := pubAPI.Do(ctx, http.MethodPost, spacePath("transversal")+"/documents:batch", service.BatchRequest{Documents: docs}, &res); err != nil {
			return n, skipped, err
		}
		count, err := changedDocuments(res)
		n += count
		if err != nil {
			return n, skipped, err
		}
	}
	return n, skipped, nil
}

// createSampleProposals deja dos propuestas abiertas para el recorrido y una retirada.
func (s *seeder) createSampleProposals(ctx context.Context, propAPI *client.API) (int, error) {
	n := 0
	for _, def := range s.sampleProposals {
		existing, err := listProposals(ctx, propAPI, def.Space, true)
		if err != nil {
			return n, err
		}
		var p store.Proposal
		for _, it := range existing {
			if it.DocKey == def.DocKey {
				p = it
				break
			}
		}
		if p.ID == "" {
			in := service.ProposalInput{DocKey: def.DocKey, Title: def.Title, Category: def.Category, Content: def.Content, Rationale: def.Rationale, Tags: []string{"ejemplo", exampleSource}}
			if err := propAPI.Do(ctx, http.MethodPost, spacePath(def.Space)+"/proposals", in, &p); err != nil {
				return n, err
			}
			n++
		}
		if def.Withdraw && p.Status == domain.ProposalOpen && slices.Contains(p.Tags, exampleSource) {
			if err := propAPI.Do(ctx, http.MethodPost, "/nexo/kb/v1/proposals/"+url.PathEscape(p.ID)+":withdraw", map[string]string{"note": "Propuesta retirada por el autor (EJEMPLO)"}, nil); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

// RemoveExample retira solo elementos con procedencia de la semilla. Un espacio
// con documentos o propuestas propios se conserva activo para no ocultarlos.
func (s *seeder) RemoveExample(ctx context.Context, env stackapi.Env) (stackapi.Report, error) {
	var rep stackapi.Report
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	adminAPI, err := s.clientFor(env, "superadmin")
	if err != nil {
		adminAPI, err = s.clientFor(env, "admin")
	}
	if err != nil {
		return rep, err
	}
	for _, sp := range s.spaces {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		api, err := s.clientFor(env, sp.Owner)
		if err != nil {
			return rep, err
		}
		var current domain.Space
		if err := api.Do(ctx, http.MethodGet, spacePath(sp.Slug), nil, &current); err != nil {
			var ae *client.APIError
			if client.IsStatus(err, http.StatusNotFound) || (errors.As(err, &ae) && ae.Code == domain.CodeArchived) {
				continue
			}
			return rep, err
		}
		if current.Status == authz.StatusArchived {
			continue
		}
		if current.Name != sp.Name || current.Description != sp.Description {
			rep.Skipped = append(rep.Skipped, sp.Slug+": espacio ajeno a la semilla")
			continue
		}
		docs, err := listDocuments(ctx, api, sp.Slug)
		if err != nil {
			return rep, err
		}
		props, err := listProposals(ctx, api, sp.Slug, false)
		if err != nil {
			return rep, err
		}
		keep := false
		for _, p := range props {
			if !s.exampleProposal(p) {
				keep = true
				continue
			}
			if p.Status != domain.ProposalOpen {
				continue
			}
			authorAPI, err := s.clientFor(env, "proponente")
			if err != nil {
				return rep, err
			}
			if err := authorAPI.Do(ctx, http.MethodPost, "/nexo/kb/v1/proposals/"+url.PathEscape(p.ID)+":withdraw", map[string]string{"note": "Retiro de semilla de ejemplo"}, nil); err != nil {
				return rep, err
			}
			rep.Proposals++
		}
		for _, d := range docs {
			var view service.DocumentView
			docPath := spacePath(sp.Slug) + "/documents/" + url.PathEscape(d.DocKey)
			if err := api.Do(ctx, http.MethodGet, docPath, nil, &view); err != nil {
				return rep, err
			}
			if view.Version.Origin.Source != exampleSource {
				keep = true
				continue
			}
			if d.Status != domain.DocActive {
				continue
			}
			if err := api.Do(ctx, http.MethodPost, docPath+":retire", map[string]string{"note": "Retiro de semilla de ejemplo"}, nil); err != nil {
				return rep, err
			}
			if sp.Slug == "transversal" {
				rep.Memories++
			} else {
				rep.Documents++
			}
		}
		if keep {
			rep.Notes = append(rep.Notes, sp.Slug+": se conserva activo porque contiene documentos o propuestas propios")
			continue
		}
		if err := adminAPI.Do(ctx, http.MethodPost, spacePath(sp.Slug)+":archive", nil, nil); err != nil {
			return rep, err
		}
		rep.Spaces++
	}
	rep.Notes = append(rep.Notes, "retiro y archivo conservan el historial: la API no ofrece borrado permanente de documentos, propuestas ni espacios")
	return rep, nil
}

func (s *seeder) exampleProposal(p store.Proposal) bool {
	if !slices.Contains(p.Tags, exampleSource) {
		return false
	}
	for _, def := range s.sampleProposals {
		if p.Space == def.Space && p.DocKey == def.DocKey && p.Title == def.Title {
			return true
		}
	}
	if p.Space == "transversal" {
		raw, err := seedFS.ReadFile("data/memorias.json")
		if err != nil {
			return false
		}
		var data struct{ Observations []cli.Memory }
		if json.Unmarshal(raw, &data) != nil {
			return false
		}
		for _, it := range cli.PlanImport(data.Observations) {
			if it.Action == "proposal" && p.DocKey == it.DocKey && p.Title == it.Title {
				return true
			}
		}
	}
	return false
}

// LoadOwn recorre sin seguir enlaces y sube un archivo por petición. Así los
// límites de cuerpo y de cantidad se respetan sin acumular la carpeta en memoria.
func (s *seeder) LoadOwn(ctx context.Context, env stackapi.Env, opts stackapi.OwnOptions) (stackapi.Report, error) {
	var rep stackapi.Report
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	if opts.Dir == "" {
		return rep, errors.New("falta la carpeta de origen (Dir)")
	}
	base, err := filepath.Abs(opts.Dir)
	if err != nil {
		return rep, err
	}
	info, err := os.Stat(base)
	if err != nil {
		return rep, errors.New("carpeta de origen no accesible")
	}
	if !info.IsDir() {
		return rep, errors.New("el origen no es una carpeta")
	}
	lim := service.UploadLimitsFromEnv()
	reg := service.DefaultRegistry()
	type fileItem struct{ rel, abs string }
	groups := map[string][]fileItem{}
	names := map[string]string{}
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return errors.New("no se pudo recorrer la carpeta de origen")
		}
		if d.IsDir() {
			if p != base && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".") || !isOwnSupportedExt(strings.ToLower(filepath.Ext(d.Name()))) {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		group := filepath.Base(base)
		if opts.PerSubfolder {
			group = "general"
			if parts := strings.Split(rel, "/"); len(parts) > 1 {
				group = parts[0]
			}
		}
		slug := normalizeOwnSlug(opts.SpacePrefix, group)
		if prev, ok := names[slug]; ok && prev != group {
			return fmt.Errorf("dos subcarpetas producen el mismo espacio %s; cambie sus nombres", slug)
		}
		names[slug] = group
		groups[slug] = append(groups[slug], fileItem{rel, p})
		return nil
	})
	if err != nil {
		return rep, err
	}
	username := "publicador"
	api, err := s.clientFor(env, username)
	if err != nil {
		username = "superadmin"
		api, err = s.clientFor(env, username)
		if err != nil {
			username = "admin"
			api, err = s.clientFor(env, username)
		}
	}
	if err != nil {
		return rep, err
	}
	adminAPI, _ := s.clientFor(env, "superadmin")
	if adminAPI == nil {
		adminAPI, _ = s.clientFor(env, "admin")
	}
	slugs := make([]string, 0, len(groups))
	for slug := range groups {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		ready := false
		keys := map[string]bool{}
		for _, item := range groups[slug] {
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			skip := func(reason string) { rep.Skipped = append(rep.Skipped, redact.Text(item.rel)+": "+reason) }
			f, err := os.Open(item.abs)
			if err != nil {
				return rep, fmt.Errorf("no se pudo abrir %s", redact.Text(item.rel))
			}
			fi, err := f.Stat()
			if err != nil {
				_ = f.Close()
				return rep, errors.New("no se pudo leer el archivo")
			}
			if !fi.Mode().IsRegular() || fi.Size() > lim.MaxFileBytes {
				_ = f.Close()
				skip("supera el límite por archivo o no es un archivo regular")
				continue
			}
			data, err := io.ReadAll(io.LimitReader(f, lim.MaxFileBytes+1))
			_ = f.Close()
			if err != nil {
				return rep, errors.New("no se pudo leer el archivo")
			}
			if int64(len(data)) > lim.MaxFileBytes {
				skip("supera NEXO_KB_UPLOAD_MAX_FILE_BYTES")
				continue
			}
			if redact.Has(item.rel) || (!strings.EqualFold(filepath.Ext(item.rel), ".docx") && (redact.Has(string(data)) || len(ingest.DetectSecrets(string(data))) > 0)) {
				skip("contiene formato de credenciales o cadena de conexión")
				continue
			}
			if source.Sniff(item.rel, data) != "" {
				skip("contenido binario o formato incompatible con la extensión")
				continue
			}
			converted, err := reg.Convert(ctx, "", ingest.Input{Name: item.rel, Data: data}, ingest.Limits{})
			if ctx.Err() != nil {
				return rep, ctx.Err()
			}
			if err != nil {
				skip("no se pudo convertir con los límites de ingesta")
				continue
			}
			if redact.Has(converted.Markdown) || redact.HasAny(converted.Meta) || len(ingest.DetectSecrets(converted.Markdown)) > 0 {
				skip("contiene formato de credenciales o cadena de conexión")
				continue
			}
			key := source.DocKeyFromPath(item.rel)
			if converted.Meta.ID != "" {
				key = converted.Meta.ID
			}
			if keys[key] {
				skip("clave de documento duplicada en la carpeta")
				continue
			}
			keys[key] = true
			body, contentType, err := ownMultipart(item.rel, data, slug)
			if err != nil {
				return rep, err
			}
			if int64(body.Len()) > lim.MaxBytes {
				skip("supera NEXO_KB_UPLOAD_MAX_BYTES con el envoltorio multipart")
				continue
			}
			if !ready {
				var sp domain.Space
				err := api.Do(ctx, http.MethodGet, spacePath(slug), nil, &sp)
				if err != nil {
					if !client.IsStatus(err, http.StatusNotFound) {
						return rep, err
					}
					if adminAPI == nil {
						return rep, errors.New("se necesita un token de administración para crear el espacio propio")
					}
					if adminAPI.Token == api.Token {
						return rep, errors.New("crear el espacio requiere un responsable distinto del administrador (R-11); configure también publicador")
					}
					in := service.CreateSpaceInput{Slug: slug, Name: "Espacio " + slug, Description: "Espacio creado a partir de contenido propio", Visibility: "open", DefaultRole: "lector", Owner: username}
					if err := adminAPI.Do(ctx, http.MethodPost, "/nexo/kb/v1/spaces", in, &sp); err != nil && !client.IsStatus(err, http.StatusConflict) {
						return rep, err
					} else if err == nil {
						rep.Spaces++
					}
				}
				ready = true
			}
			var result service.BatchResult
			if err := api.DoMultipart(ctx, spacePath(slug)+"/uploads", body, contentType, &result); err != nil {
				return rep, err
			}
			n, err := changedDocuments(result)
			rep.Documents += n
			if err != nil {
				return rep, err
			}
		}
	}
	if rep.Spaces == 0 && rep.Documents == 0 {
		rep.Notes = append(rep.Notes, "sin cambios")
	}
	return rep, nil
}

func ownMultipart(name string, data []byte, slug string) (*bytes.Buffer, string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(data); err != nil {
		return nil, "", err
	}
	for _, field := range [][2]string{{"source", source.KindDir}, {"collection", "contenido-propio-" + slug}, {"change_note", "Carga de contenido propio"}} {
		if err := mw.WriteField(field[0], field[1]); err != nil {
			return nil, "", err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return &body, mw.FormDataContentType(), nil
}

func isOwnSupportedExt(ext string) bool {
	switch ext {
	case ".md", ".markdown", ".mdx", ".txt", ".text", ".docx":
		return true
	}
	return false
}

// normalizeOwnSlug normaliza el slug a ^[a-z0-9][a-z0-9-]{1,62}$
func normalizeOwnSlug(prefix, name string) string {
	s := ingest.Slugify(name)
	if prefix != "" {
		p := ingest.Slugify(prefix)
		s = p + "-" + s
	}
	s = strings.Trim(s, "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	if len(s) < 2 {
		s = s + "-espacio"
	}
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-")
	}
	if !domain.ValidSlug(s) {
		return "espacio-propio"
	}
	return s
}

const exampleSource = "nexo-stack-seed-v1"

func spacePath(slug string) string { return "/nexo/kb/v1/spaces/" + url.PathEscape(slug) }

// changedDocuments comprueba también rechazos parciales de una respuesta HTTP 200.
func changedDocuments(res service.BatchResult) (int, error) {
	n := res.Summary[domain.ResultCreated] + res.Summary[domain.ResultUpdated]
	for _, r := range res.Results {
		if r.Status == domain.ResultRejected {
			return n, fmt.Errorf("documento rechazado (%s): %s", redact.Text(r.Code), redact.Text(r.Reason))
		}
	}
	if res.Summary[domain.ResultRejected] > 0 || len(res.Skipped) > 0 {
		return n, errors.New("la subida contiene elementos rechazados u omitidos")
	}
	return n, nil
}

func listDocuments(ctx context.Context, api *client.API, slug string) ([]domain.Document, error) {
	var all []domain.Document
	cursor := ""
	for {
		var page struct {
			Items      []domain.Document
			NextCursor *string `json:"next_cursor"`
		}
		p := spacePath(slug) + "/documents?limit=200&cursor=" + url.QueryEscape(cursor)
		if err := api.Do(ctx, http.MethodGet, p, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return all, nil
		}
		if *page.NextCursor == cursor {
			return nil, errors.New("cursor de documentos repetido")
		}
		cursor = *page.NextCursor
	}
}

func listProposals(ctx context.Context, api *client.API, slug string, mine bool) ([]store.Proposal, error) {
	var all []store.Proposal
	seen := map[string]bool{}
	before := ""
	for {
		var page struct{ Items []store.Proposal }
		p := fmt.Sprintf("/nexo/kb/v1/proposals?space=%s&mine=%t&limit=200&before=%s", url.QueryEscape(slug), mine, url.QueryEscape(before))
		if err := api.Do(ctx, http.MethodGet, p, nil, &page); err != nil {
			return nil, err
		}
		for _, p := range page.Items {
			if !seen[p.ID] {
				all = append(all, p)
				seen[p.ID] = true
			}
		}
		if len(page.Items) < 200 {
			return all, nil
		}
		next := page.Items[len(page.Items)-1].CreatedAt.Add(time.Nanosecond).Format(time.RFC3339Nano)
		if next == before {
			return nil, errors.New("la API no puede paginar más de 200 propuestas con la misma fecha; no se modificaron propuestas")
		}
		before = next
	}
}
