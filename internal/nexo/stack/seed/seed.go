// Package seed implementa la carga y retiro de datos de ejemplo y propios
// para `engram nexo stack` (stackapi.Seeder).
package seed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/cli"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest/docx"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
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

	// Token de administración para crear espacios
	adminUser := "superadmin"
	if _, ok := env.Tokens[adminUser]; !ok {
		adminUser = "admin"
	}
	adminAPI, err := s.clientFor(env, adminUser)
	if err != nil {
		return rep, fmt.Errorf("cliente de administración: %w", err)
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
				// El espacio ya existe (idempotente)
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
		path := fmt.Sprintf("/nexo/authz/v1/scopes/space/%s/members/%s", url.PathEscape(ra.Space), url.PathEscape(ra.Principal))
		body := map[string]string{"role": ra.Role}
		var change authz.BindingChange
		err = ownerAPI.Do(ctx, http.MethodPut, path, body, &change)
		if err != nil && !client.IsStatus(err, http.StatusOK) {
			return rep, fmt.Errorf("asignar rol %s en %s a %s: %w", ra.Role, ra.Space, ra.Principal, err)
		}
	}

	// Si no se solicitaron datos de ejemplo, terminamos aquí
	if !opts.Example {
		return rep, nil
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
		if err != nil {
			return rep, fmt.Errorf("publicar documentos en %s: %w", slug, err)
		}
		rep.Documents += createdDocs
	}

	// 4. Cargar las 6 memorias en el espacio transversal compatible con import-transversal
	transMemAPI := pubAPI
	memCreated, skippedMems, err := s.loadTransversalMemories(ctx, transMemAPI, env)
	if err != nil {
		return rep, fmt.Errorf("cargar memorias transversales: %w", err)
	}
	rep.Memories += memCreated
	rep.Skipped = append(rep.Skipped, skippedMems...)

	// 5. Crear 3 propuestas con el token de proponente
	propAPI, err := s.clientFor(env, "proponente")
	if err != nil {
		return rep, fmt.Errorf("cliente proponente: %w", err)
	}
	propsCreated, err := s.createSampleProposals(ctx, propAPI, env)
	if err != nil {
		return rep, fmt.Errorf("crear propuestas de ejemplo: %w", err)
	}
	rep.Proposals += propsCreated

	// 6. Provocar acceso denegado a propósito (el lector intenta acceder a operaciones)
	if lectorAPI, err := s.clientFor(env, "lector"); err == nil {
		var ignored any
		_ = lectorAPI.Do(ctx, http.MethodGet, "/nexo/kb/v1/spaces/operaciones", nil, &ignored)
		rep.Notes = append(rep.Notes, "acceso denegado provocado a propósito (lector intentó leer operaciones)")
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
	fileCount := 0

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
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			return 0, err
		}
		if _, err := fw.Write(data); err != nil {
			return 0, err
		}
		fileCount++
	}

	_ = mw.WriteField("source", "seed")
	_ = mw.WriteField("collection", slug)
	_ = mw.WriteField("change_note", "Carga inicial de semilla (EJEMPLO)")
	if err := mw.Close(); err != nil {
		return 0, err
	}

	var res service.BatchResult
	path := fmt.Sprintf("/nexo/kb/v1/spaces/%s/uploads", url.PathEscape(slug))
	if err := api.DoMultipart(ctx, path, &body, mw.FormDataContentType(), &res); err != nil {
		return 0, err
	}

	return res.Summary[domain.ResultCreated], nil
}

// loadTransversalMemories carga las memorias de data/memorias.json usando la misma lógica de import-transversal.
func (s *seeder) loadTransversalMemories(ctx context.Context, pubAPI *client.API, env stackapi.Env) (int, []string, error) {
	raw, err := seedFS.ReadFile("data/memorias.json")
	if err != nil {
		return 0, nil, fmt.Errorf("leer data/memorias.json: %w", err)
	}
	var data struct {
		Observations []cli.Memory `json:"observations"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return 0, nil, fmt.Errorf("decodificar memorias.json: %w", err)
	}

	items := cli.PlanImport(data.Observations)
	var docs []service.PublishDoc
	var skipped []string
	createdCount := 0

	// Listar propuestas existentes en el espacio transversal creadas por el proponente
	propAPI, propErr := s.clientFor(env, "proponente")
	existingProposals := map[string]string{}
	if propErr == nil {
		var mine struct {
			Items []struct {
				DocKey string `json:"doc_key"`
				Status string `json:"status"`
			} `json:"items"`
		}
		if err := propAPI.Do(ctx, http.MethodGet, "/nexo/kb/v1/proposals?mine=true&space=transversal&limit=200", nil, &mine); err == nil {
			for _, it := range mine.Items {
				existingProposals[it.DocKey] = it.Status
			}
		}
	}

	for _, it := range items {
		switch it.Action {
		case "document":
			docs = append(docs, service.PublishDoc{
				DocKey:    it.DocKey,
				Title:     it.Title,
				Category:  it.Category,
				Content:   memoryMarkdownFromItem(it),
				Tags:      []string{cli.TransversalProject},
				Origin:    domain.Origin{Kind: domain.OriginImport, Filename: cli.TransversalProject},
			})
		case "proposal":
			if _, ok := existingProposals[it.DocKey]; ok {
				// Ya existe la propuesta, omitir
				continue
			}
			if propAPI != nil {
				var p struct {
					ID string `json:"id"`
				}
				body := service.ProposalInput{
					DocKey:    it.DocKey,
					Title:     it.Title,
					Category:  it.Category,
					Content:   memoryMarkdownFromItem(it),
					Rationale: "Importada de memoria transversal de ejemplo.",
					Tags:      []string{cli.TransversalProject},
				}
				if err := propAPI.Do(ctx, http.MethodPost, "/nexo/kb/v1/spaces/transversal/proposals", body, &p); err == nil {
					createdCount++
				}
			}
		case "skip":
			skipped = append(skipped, fmt.Sprintf("memoria #%d (%s): %s", it.MemoryID, it.Title, it.Detail))
		}
	}

	if len(docs) > 0 {
		req := service.BatchRequest{Documents: docs}
		var res service.BatchResult
		if err := pubAPI.Do(ctx, http.MethodPost, "/nexo/kb/v1/spaces/transversal/documents:batch", req, &res); err != nil {
			return createdCount, skipped, fmt.Errorf("batch documents transversal: %w", err)
		}
		createdCount += res.Summary[domain.ResultCreated]
	}

	return createdCount, skipped, nil
}

func memoryMarkdownFromItem(it cli.ImportItem) string {
	return fmt.Sprintf("# %s\n\n> Nota: Memoria de (EJEMPLO) importada para el entorno demo.\n\nMemoria del proyecto %s.\n", it.Title, cli.TransversalProject)
}

// createSampleProposals crea las 3 propuestas del diseño con el token del proponente.
func (s *seeder) createSampleProposals(ctx context.Context, propAPI *client.API, env stackapi.Env) (int, error) {
	createdCount := 0

	for _, pdef := range s.sampleProposals {
		if err := ctx.Err(); err != nil {
			return createdCount, err
		}
		// Comprobar si ya existe una propuesta propia con este doc_key en el espacio
		path := fmt.Sprintf("/nexo/kb/v1/proposals?mine=true&space=%s&limit=200", url.QueryEscape(pdef.Space))
		var mine struct {
			Items []struct {
				ID     string `json:"id"`
				DocKey string `json:"doc_key"`
				Status string `json:"status"`
			} `json:"items"`
		}
		exists := false
		if err := propAPI.Do(ctx, http.MethodGet, path, nil, &mine); err == nil {
			for _, it := range mine.Items {
				if it.DocKey == pdef.DocKey {
					exists = true
					break
				}
			}
		}
		if exists {
			continue
		}

		body := service.ProposalInput{
			DocKey:    pdef.DocKey,
			Title:     pdef.Title,
			Category:  pdef.Category,
			Content:   pdef.Content,
			Rationale: pdef.Rationale,
			Tags:      []string{"ejemplo"},
		}
		var created struct {
			ID string `json:"id"`
		}
		createPath := fmt.Sprintf("/nexo/kb/v1/spaces/%s/proposals", url.PathEscape(pdef.Space))
		if err := propAPI.Do(ctx, http.MethodPost, createPath, body, &created); err != nil {
			return createdCount, fmt.Errorf("crear propuesta %s: %w", pdef.Title, err)
		}
		createdCount++

		if pdef.Withdraw && created.ID != "" {
			withdrawPath := fmt.Sprintf("/nexo/kb/v1/proposals/%s:withdraw", url.PathEscape(created.ID))
			withdrawBody := map[string]string{"reason": "Propuesta retirada por el autor (EJEMPLO)"}
			var wout map[string]any
			_ = propAPI.Do(ctx, http.MethodPost, withdrawPath, withdrawBody, &wout)
		}
	}

	return createdCount, nil
}

// RemoveExample retira solo lo que Load creó (marcado como ejemplo).
func (s *seeder) RemoveExample(ctx context.Context, env stackapi.Env) (stackapi.Report, error) {
	var rep stackapi.Report

	// 1. Descartar o retirar propuestas de ejemplo en backend, frontend, transversal
	if propAPI, err := s.clientFor(env, "proponente"); err == nil {
		for _, sp := range []string{"backend", "frontend", "transversal"} {
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			path := fmt.Sprintf("/nexo/kb/v1/proposals?mine=true&space=%s&limit=200", url.QueryEscape(sp))
			var mine struct {
				Items []struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"items"`
			}
			if err := propAPI.Do(ctx, http.MethodGet, path, nil, &mine); err == nil {
				for _, it := range mine.Items {
					if it.Status == domain.ProposalOpen {
						wPath := fmt.Sprintf("/nexo/kb/v1/proposals/%s:withdraw", url.PathEscape(it.ID))
						var ignored any
						if err := propAPI.Do(ctx, http.MethodPost, wPath, map[string]string{"reason": "Retiro de semilla de ejemplo"}, &ignored); err == nil {
							rep.Proposals++
						}
					}
				}
			}
		}
	}

	// 2. Retirar documentos en cada espacio
	for _, sp := range s.spaces {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		ownerUser := sp.Owner
		ownerAPI, err := s.clientFor(env, ownerUser)
		if err != nil {
			continue
		}
		// Listar documentos del espacio
		var docs struct {
			Items []struct {
				DocKey string `json:"doc_key"`
				Status string `json:"status"`
			} `json:"items"`
		}
		path := fmt.Sprintf("/nexo/kb/v1/spaces/%s/documents?limit=200", url.PathEscape(sp.Slug))
		if err := ownerAPI.Do(ctx, http.MethodGet, path, nil, &docs); err == nil {
			for _, doc := range docs.Items {
				if doc.Status == domain.DocActive {
					retirePath := fmt.Sprintf("/nexo/kb/v1/spaces/%s/documents/%s:retire", url.PathEscape(sp.Slug), url.PathEscape(doc.DocKey))
					var ignored any
					if err := ownerAPI.Do(ctx, http.MethodPost, retirePath, map[string]string{"reason": "Retiro de semilla de ejemplo"}, &ignored); err == nil {
						if sp.Slug == "transversal" {
							rep.Memories++
						} else {
							rep.Documents++
						}
					}
				}
			}
		}
	}

	// 3. Archivar los espacios de ejemplo
	adminUser := "superadmin"
	if _, ok := env.Tokens[adminUser]; !ok {
		adminUser = "admin"
	}
	adminAPI, err := s.clientFor(env, adminUser)
	if err == nil {
		for _, sp := range s.spaces {
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			path := fmt.Sprintf("/nexo/kb/v1/spaces/%s:archive", url.PathEscape(sp.Slug))
			var ignored any
			if err := adminAPI.Do(ctx, http.MethodPost, path, nil, &ignored); err == nil {
				rep.Spaces++
			}
		}
	}

	rep.Notes = append(rep.Notes, "espacios de ejemplo archivados (:archive); el servidor no cuenta con API para borrar espacios definitivamente")
	return rep, nil
}

// LoadOwn sube el contenido de una carpeta del usuario.
func (s *seeder) LoadOwn(ctx context.Context, env stackapi.Env, opts stackapi.OwnOptions) (stackapi.Report, error) {
	var rep stackapi.Report
	if opts.Dir == "" {
		return rep, errors.New("falta la carpeta de origen (Dir)")
	}
	info, err := os.Stat(opts.Dir)
	if err != nil {
		return rep, fmt.Errorf("carpeta %s no accesible: %w", opts.Dir, err)
	}
	if !info.IsDir() {
		return rep, fmt.Errorf("%s no es una carpeta", opts.Dir)
	}

	lim := service.UploadLimitsFromEnv()
	reg := ingest.DefaultRegistry()
	reg.Register(docx.Converter{})

	// Cliente para crear espacios y subir documentos
	pubAPI, err := s.clientFor(env, "publicador")
	if err != nil {
		if sAPI, sErr := s.clientFor(env, "superadmin"); sErr == nil {
			pubAPI = sAPI
		} else if aAPI, aErr := s.clientFor(env, "admin"); aErr == nil {
			pubAPI = aAPI
		} else {
			return rep, fmt.Errorf("no hay token disponible para publicar: %w", err)
		}
	}

	adminAPI, adminErr := s.clientFor(env, "superadmin")
	if adminErr != nil {
		adminAPI, _ = s.clientFor(env, "admin")
	}

	type fileItem struct {
		relPath string
		absPath string
		data    []byte
	}

	// Agrupación de archivos por slug de espacio
	spaceFiles := map[string][]fileItem{}
	absBase, err := filepath.Abs(opts.Dir)
	if err != nil {
		return rep, err
	}

	err = filepath.WalkDir(absBase, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != absBase && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "eval") {
				return filepath.SkipDir
			}
			return nil
		}
		// Ignorar archivos ocultos o no soportados
		if strings.HasPrefix(name, ".") || strings.EqualFold(name, "README.md") || name == "nexo-kb.yaml" {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !isOwnSupportedExt(ext) {
			return nil
		}

		rel, err := filepath.Rel(absBase, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.Size() > lim.MaxFileBytes {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: supera el límite de tamaño por archivo (%d bytes)", rel, lim.MaxFileBytes))
			return nil
		}

		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		// Escanear credenciales con redact
		hasSecret := redact.Has(string(data))
		if !hasSecret && ext == ".docx" {
			// Para docx, intentar convertir y escanear el texto resultante
			res, cErr := docx.Converter{}.Convert(ctx, ingest.Input{Name: name, Data: data})
			if cErr == nil && redact.Has(res.Markdown) {
				hasSecret = true
			}
		}

		if hasSecret {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: contiene formato de credenciales o cadena de conexión", rel))
			return nil
		}

		// Asignar espacio
		targetSlug := ""
		if opts.PerSubfolder {
			parts := strings.Split(rel, "/")
			if len(parts) > 1 {
				targetSlug = normalizeOwnSlug(opts.SpacePrefix, parts[0])
			} else {
				targetSlug = normalizeOwnSlug(opts.SpacePrefix, "general")
			}
		} else {
			targetSlug = normalizeOwnSlug(opts.SpacePrefix, filepath.Base(absBase))
		}

		spaceFiles[targetSlug] = append(spaceFiles[targetSlug], fileItem{relPath: rel, absPath: p, data: data})
		return nil
	})
	if err != nil {
		return rep, err
	}

	// Subir por cada espacio
	for slug, files := range spaceFiles {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		// Crear espacio si no existe
		if adminAPI != nil {
			in := service.CreateSpaceInput{
				Slug:        slug,
				Name:        fmt.Sprintf("Espacio %s", slug),
				Description: fmt.Sprintf("Espacio creado a partir de contenido propio (%s)", slug),
				Visibility:  "open",
				DefaultRole: "lector",
				Owner:       "publicador",
			}
			var spOut domain.Space
			if cErr := adminAPI.Do(ctx, http.MethodPost, "/nexo/kb/v1/spaces", in, &spOut); cErr == nil {
				rep.Spaces++
			}
		}

		// Multipart upload
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		for _, f := range files {
			fw, err := mw.CreateFormFile("file", f.relPath)
			if err != nil {
				return rep, err
			}
			if _, err := fw.Write(f.data); err != nil {
				return rep, err
			}
		}
		_ = mw.WriteField("source", "load-own")
		_ = mw.WriteField("collection", slug)
		_ = mw.WriteField("change_note", "Carga de contenido propio")
		if err := mw.Close(); err != nil {
			return rep, err
		}

		var bres service.BatchResult
		path := fmt.Sprintf("/nexo/kb/v1/spaces/%s/uploads", url.PathEscape(slug))
		if err := pubAPI.DoMultipart(ctx, path, &body, mw.FormDataContentType(), &bres); err != nil {
			return rep, fmt.Errorf("subir contenido a espacio %s: %w", slug, err)
		}
		rep.Documents += bres.Summary[domain.ResultCreated]
	}

	return rep, nil
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
