package client

// Herramientas MCP nexo_* (D-02, design §6.5). Se registran desde el MCP de
// Engram con un único enganche marcado `// NX-10` en registerTools
// (internal/mcp/mcp.go); toda la lógica vive aquí.
//
// Reglas de registro:
//   - NEXO_KB_MCP=off las desactiva.
//   - Con --tools que las nombre o incluya "nexo" (--tools=agent,nexo), esas.
//   - Con la PC conectada (`engram nexo connect`, nexo.json) y el MCP con
//     todas las herramientas o el perfil agent (el de `engram setup`), todas.
//   - Solo si hay servidor y token configurados (cloud.json o
//     ENGRAM_CLOUD_SERVER y ENGRAM_CLOUD_TOKEN): el conocimiento exige un
//     usuario administrado. Sin red se siguen registrando: responden desde la
//     copia local.

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// EnvMCP desactiva las herramientas con "off".
const EnvMCP = "NEXO_KB_MCP"

// ToolNames son las herramientas nexo_* en el orden en que se registran.
var ToolNames = []string{"nexo_search", "nexo_get", "nexo_spaces", "nexo_changes", "nexo_propose", "nexo_proposals", "nexo_review", "nexo_whoami"}

// ProfileToken es el nombre que habilita todas las herramientas nexo_* en
// --tools.
const ProfileToken = "nexo"

func allowed(name string, allowlist map[string]bool) bool {
	return allowlist == nil || allowlist[name] || allowlist[ProfileToken]
}

// RegisterTools es el enganche del MCP de Engram: abre la KB con la
// configuración de dataDir, arranca la sincronización en segundo plano y
// registra las herramientas permitidas. Nunca hace fallar al MCP: ante un
// problema lo anota en el log (stderr) y no registra nada. store es el
// almacén de Engram (solo se usa su carpeta de datos, donde está cloud.json).
func RegisterTools(srv *server.MCPServer, store interface{ DataDir() string }, allowlist map[string]bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[nexo] kb: herramientas nexo_* no disponibles: %v", r)
		}
	}()
	if srv == nil || store == nil || strings.EqualFold(strings.TrimSpace(os.Getenv(EnvMCP)), "off") {
		return
	}
	dataDir := store.DataDir()
	effective, ok := effectiveAllowlist(allowlist, Connected(dataDir))
	if !ok {
		return
	}
	st, err := LoadSettings(dataDir, "")
	if err != nil || st.Server == "" || st.Token == "" {
		return // sin servidor o token no hay conocimiento (design §6.5)
	}
	kb, err := OpenKB(dataDir)
	if err != nil {
		log.Printf("[nexo] kb: herramientas nexo_* no disponibles: %v", err)
		return
	}
	kb.Start(context.Background())
	RegisterToolsWith(srv, kb, effective)
}

// effectiveAllowlist decide qué herramientas nexo_* se registran:
//   - si --tools las nombra (o incluye "nexo"), esas;
//   - si la PC está conectada (`engram nexo connect`, nexo.json) y el MCP
//     corre con todas las herramientas o con el perfil de agente (el que
//     escribe `engram setup`: incluye mem_search), todas;
//   - si no, ninguna (un Engram con nube que no es Nexo no las ve).
func effectiveAllowlist(allowlist map[string]bool, connected bool) (map[string]bool, bool) {
	if allowlist != nil {
		for _, n := range ToolNames {
			if allowlist[n] || allowlist[ProfileToken] {
				return allowlist, true
			}
		}
	}
	if connected && (allowlist == nil || allowlist["mem_search"]) {
		return nil, true
	}
	return nil, false
}

// RegisterToolsWith registra las herramientas sobre una KB ya abierta (lo
// usan RegisterTools y las pruebas de contrato).
func RegisterToolsWith(srv *server.MCPServer, kb *KB, allowlist map[string]bool) {
	readOnly := func(title string) []mcp.ToolOption {
		return []mcp.ToolOption{mcp.WithTitleAnnotation(title), mcp.WithReadOnlyHintAnnotation(true), mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false)}
	}
	add := func(name string, opts []mcp.ToolOption, h server.ToolHandlerFunc) {
		if allowed(name, allowlist) {
			srv.AddTool(mcp.NewTool(name, opts...), h)
		}
	}

	add("nexo_search", append(readOnly("Buscar estándares de Nexo"),
		mcp.WithDescription("Busca estándares, convenciones y lineamientos OFICIALES de la empresa en los espacios de conocimiento de Nexo (no la memoria del proyecto). "+
			"Úsala antes de decidir algo transversal (arquitectura, logging, seguridad, APIs, nombres, CI/CD). Cada resultado trae status, citation, link y source_url: cítalos siempre. "+
			"Si no_results es true, di \"No existe un estándar oficial publicado sobre este tema.\" sin inventar. Con at=AAAA-MM-DD responde la regla vigente en esa fecha; "+
			"con known_at, lo que estaba publicado en esa fecha. Sin conexión responde desde la copia local (source=local_copy) e informa su fecha."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Qué buscar, en lenguaje natural o palabras clave. Con mode=exact: texto literal o /expresión/.")),
		mcp.WithString("spaces", mcp.Description("Espacios donde buscar (slugs separados por coma). Vacío: todos los visibles.")),
		mcp.WithString("category", mcp.Description("Categoría: estandar, convencion, lineamiento, glosario, regla_negocio, catalogo.")),
		mcp.WithString("tag", mcp.Description("Etiqueta.")),
		mcp.WithString("at", mcp.Description("Fecha (AAAA-MM-DD o RFC 3339): la regla que estaba vigente en esa fecha (HU-37).")),
		mcp.WithString("known_at", mcp.Description("Fecha: lo que Nexo tenía publicado en esa fecha (necesita el servidor).")),
		mcp.WithString("mode", mcp.Description("Vacío (recomendado): el servidor elige hybrid (palabras y significado) si tiene índice vectorial y fts si no. fts: solo palabras. exact: nombres de componentes, códigos, siglas. hybrid: palabras y significado (encuentra paráfrasis).")),
		mcp.WithString("rerank", mcp.Description("Vacío (recomendado): el servidor decide. on: pide reordenar los mejores resultados con un modelo (mejor primer resultado; tarda más). off: no reordenar. Solo reordena lo que ya pasó el filtro y el umbral; si falla, devuelve el orden normal con un aviso.")),
		mcp.WithNumber("limit", mcp.Description("Resultados (por defecto 10, máximo 20).")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		at, err := ParseWhen(req.GetString("at", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		known, err := ParseWhen(req.GetString("known_at", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := kb.Search(ctx, SearchParams{Query: req.GetString("query", ""), Spaces: listArg(req, "spaces"), Category: req.GetString("category", ""),
			Tag: req.GetString("tag", ""), At: at, KnownAt: known, Mode: req.GetString("mode", ""), Rerank: req.GetString("rerank", ""), Limit: min(max(req.GetInt("limit", search.DefaultLimit), 1), 20)})
		if err != nil {
			return mcp.NewToolResultError("No se pudo buscar en Nexo: " + FriendlyError(err)), nil
		}
		return mcp.NewToolResultText(MarshalIndent(res)), nil
	})

	add("nexo_get", append(readOnly("Leer documento de Nexo"),
		mcp.WithDescription("Lee el texto completo de un documento oficial o de una sección (por su ancla) de un espacio de Nexo, con su cita, enlace y estado. "+
			"Úsala después de nexo_search cuando el extracto no alcanza. Sin conexión lee la copia local (solo la versión vigente)."),
		mcp.WithString("space", mcp.Required(), mcp.Description("Slug del espacio (campo space de nexo_search).")),
		mcp.WithString("doc_key", mcp.Required(), mcp.Description("Clave del documento (campo doc_key). Acepta doc_key#ancla.")),
		mcp.WithString("anchor", mcp.Description("Ancla de la sección (campo anchor). Vacío: documento completo.")),
		mcp.WithNumber("version", mcp.Description("Versión (por defecto la vigente; una versión histórica necesita el servidor).")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := kb.Get(ctx, GetParams{Space: req.GetString("space", ""), DocKey: req.GetString("doc_key", ""), Anchor: req.GetString("anchor", ""),
			Version: req.GetInt("version", 0)})
		if err != nil {
			return mcp.NewToolResultError("No se pudo leer el documento: " + FriendlyError(err)), nil
		}
		return mcp.NewToolResultText(MarshalIndent(res)), nil
	})

	add("nexo_spaces", append(readOnly("Espacios de Nexo"),
		mcp.WithDescription("Lista los espacios de conocimiento de Nexo que puedes ver y tu rol en cada uno (lector, proponente, publicador, responsable)."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := kb.Spaces(ctx)
		if err != nil {
			return mcp.NewToolResultError("No se pudo listar los espacios: " + FriendlyError(err)), nil
		}
		return mcp.NewToolResultText(MarshalIndent(res)), nil
	})

	add("nexo_changes", append(readOnly("Qué cambió en Nexo"),
		mcp.WithDescription("Qué cambió en los espacios de conocimiento desde una fecha (HU-38): secciones agregadas, modificadas y retiradas, con fecha y enlace. Necesita el servidor."),
		mcp.WithString("since", mcp.Required(), mcp.Description("Desde (AAAA-MM-DD o RFC 3339).")),
		mcp.WithString("until", mcp.Description("Hasta (por defecto, ahora).")),
		mcp.WithString("spaces", mcp.Description("Espacios (slugs separados por coma). Vacío: todos los visibles.")),
		mcp.WithString("clock", mcp.Description("valid (por defecto: lo que empezó o dejó de regir) o recorded (lo que se publicó).")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		since, err := ParseWhen(req.GetString("since", ""))
		if err != nil || since == nil {
			return mcp.NewToolResultError("since es obligatorio (AAAA-MM-DD o RFC 3339)"), nil
		}
		until, err := ParseWhen(req.GetString("until", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := kb.Changes(ctx, ChangesParams{Since: *since, Until: until, Spaces: listArg(req, "spaces"), Clock: req.GetString("clock", "")})
		if err != nil {
			return mcp.NewToolResultError("No se pudo consultar qué cambió: " + FriendlyError(err)), nil
		}
		return mcp.NewToolResultText(MarshalIndent(res)), nil
	})

	add("nexo_propose", []mcp.ToolOption{
		mcp.WithTitleAnnotation("Proponer a un espacio de Nexo"), mcp.WithReadOnlyHintAnnotation(false), mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false), mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithDescription("Propone un lineamiento nuevo o un cambio a un documento de un espacio (HU-35). La propuesta NO es oficial hasta que un publicador la aprueba. " +
			"Úsala solo con la confirmación explícita del usuario. Nunca incluyas secretos. Sin conexión no se guarda nada."),
		mcp.WithString("space", mcp.Required(), mcp.Description("Slug del espacio.")),
		mcp.WithString("title", mcp.Required(), mcp.Description("Título corto.")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Contenido en Markdown, autocontenido.")),
		mcp.WithString("rationale", mcp.Required(), mcp.Description("Justificación: por qué debería ser estándar.")),
		mcp.WithString("doc_key", mcp.Description("Documento que cambia (si es un cambio) o clave sugerida del nuevo.")),
		mcp.WithNumber("base_version", mcp.Description("Versión del documento que se vio (para detectar conflictos).")),
		mcp.WithString("category", mcp.Description("Categoría.")),
		mcp.WithString("tags", mcp.Description("Etiquetas separadas por coma.")),
		mcp.WithString("valid_from", mcp.Description("Fecha de entrada en vigencia propuesta (AAAA-MM-DD).")),
		mcp.WithString("valid_until", mcp.Description("Fin de vigencia propuesto (AAAA-MM-DD).")),
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		vf, err := ParseWhen(req.GetString("valid_from", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		vu, err := ParseWhen(req.GetString("valid_until", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := kb.Propose(ctx, ProposeParams{Space: req.GetString("space", ""), Title: req.GetString("title", ""), Content: req.GetString("content", ""),
			Rationale: req.GetString("rationale", ""), DocKey: req.GetString("doc_key", ""), BaseVersion: req.GetInt("base_version", 0),
			Category: req.GetString("category", ""), Tags: listArg(req, "tags"), ValidFrom: vf, ValidUntil: vu})
		if err != nil {
			return mcp.NewToolResultError("La propuesta no se registró: " + FriendlyError(err)), nil
		}
		res["guidance"] = "Informa al usuario que la propuesta quedó abierta (no es oficial) y que la verán él y los revisores del espacio."
		return mcp.NewToolResultText(MarshalIndent(res)), nil
	})

	add("nexo_proposals", append(readOnly("Propuestas de Nexo"),
		mcp.WithDescription("Lista propuestas: las tuyas (estado y motivo de descarte) y, si eres publicador o responsable, las del espacio pendientes de revisar."),
		mcp.WithString("space", mcp.Description("Slug del espacio.")),
		mcp.WithString("status", mcp.Description("open, approved, discarded o withdrawn.")),
		mcp.WithBoolean("mine", mcp.Description("Solo las propias.")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := kb.Proposals(ctx, req.GetString("space", ""), req.GetString("status", ""), req.GetBool("mine", false), 50)
		if err != nil {
			return mcp.NewToolResultError("No se pudo listar las propuestas: " + FriendlyError(err)), nil
		}
		return mcp.NewToolResultText(MarshalIndent(res)), nil
	})

	add("nexo_review", []mcp.ToolOption{
		mcp.WithTitleAnnotation("Revisar propuesta de Nexo"), mcp.WithReadOnlyHintAnnotation(false), mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false), mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithDescription("Aprueba (publica como oficial) o descarta con motivo una propuesta (HU-36). Solo publicadores y responsables del espacio, y solo si el usuario lo pide. " +
			"Si responde que el documento cambió (conflicto), pregunta al usuario y reintenta con force=true."),
		mcp.WithString("proposal_id", mcp.Required(), mcp.Description("Id de la propuesta.")),
		mcp.WithString("decision", mcp.Required(), mcp.Description("approve o discard.")),
		mcp.WithString("note", mcp.Description("Comentario; obligatorio para descartar (motivo).")),
		mcp.WithBoolean("force", mcp.Description("Aprobar aunque el documento haya cambiado desde la propuesta.")),
		mcp.WithString("valid_from", mcp.Description("Ajustar la entrada en vigencia (AAAA-MM-DD).")),
		mcp.WithString("valid_until", mcp.Description("Ajustar el fin de vigencia (AAAA-MM-DD).")),
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		vf, err := ParseWhen(req.GetString("valid_from", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		vu, err := ParseWhen(req.GetString("valid_until", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := kb.Review(ctx, ReviewParams{ProposalID: req.GetString("proposal_id", ""), Decision: req.GetString("decision", ""),
			Note: req.GetString("note", ""), Force: req.GetBool("force", false), ValidFrom: vf, ValidUntil: vu})
		if err != nil {
			return mcp.NewToolResultError("No se pudo revisar la propuesta: " + FriendlyError(err)), nil
		}
		return mcp.NewToolResultText(MarshalIndent(res)), nil
	})

	add("nexo_whoami", append(readOnly("Estado de Nexo"),
		mcp.WithDescription("Quién soy para Nexo (usuario, roles), si el servidor responde y el estado de la copia local de los espacios (fecha, documentos)."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(MarshalIndent(kb.Whoami(ctx))), nil
	})
}

// listArg acepta una lista como texto separado por comas o como arreglo.
func listArg(req mcp.CallToolRequest, name string) []string {
	var raw []string
	switch v := req.GetArguments()[name].(type) {
	case string:
		raw = strings.Split(v, ",")
	case []any:
		for _, x := range v {
			raw = append(raw, fmt.Sprint(x))
		}
	}
	var out []string
	for _, s := range raw {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
