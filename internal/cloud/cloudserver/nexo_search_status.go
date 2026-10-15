package cloudserver

// Nexo IA · NX-10 (panel) — pantalla "Estado de la búsqueda" (docs/nexo/panel/
// design.md §11).
//
//	GET /dashboard/kb/search-status      estado del buscador y del índice por significado (platform.settings)
//
// Es solo lectura. La capacidad es platform.settings (superadmin), la misma
// que exige GET /nexo/kb/v1/admin/index, de donde sale buena parte de los
// datos (kbservice.SearchStatus junta ese estado con las versiones retiradas,
// el avance sobre el total, las cartas muertas y las últimas evaluaciones):
// la pantalla no abre una vía distinta a la de la API y la CLI. Además,
// platform.settings es una de las capacidades que el superadmin puede limitar
// a ciertas redes (NEXO_SUPERADMIN_ALLOWED_CIDRS). Un administrador sin ese
// rol recibe 403 con el motivo. La clave del proveedor nunca se muestra: solo
// "configurada" y la variable de la que sale.
//
// Para agregar un bloque (por ejemplo el reordenamiento de R3): un campo en
// dashboard.NexoSearchStatusPage, una función como searchStatusRerank que lo
// llene y una llamada en nexo_search_status.templ.

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/dashboard"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/embed"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/rerank"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
	kbservice "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	kbstore "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

func (s *CloudServer) registerNexoSearchStatusDashboard(mux *http.ServeMux) {
	s.nexoRoute(mux, "GET", dashboard.NexoSearchStatusPath, "platform.settings", s.requireDashboardSession(s.nexoKBPage(s.kbSearchStatus)))
}

const searchStatusNoRole = "Esta pantalla es para el superadmin de la plataforma. Si la necesitas, pide el rol a un superadmin."

func (s *CloudServer) kbSearchStatus(w http.ResponseWriter, r *http.Request, svc *kbservice.Service, id authz.Identity) {
	st, err := svc.SearchStatus(r.Context(), id)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) && de.Kind == domain.KindForbidden {
			s.kbFail(w, r, http.StatusForbidden, searchStatusNoRole)
			return
		}
		s.kbError(w, r, err)
		return
	}
	page := buildSearchStatusPage(st)
	s.kbRenderComponent(w, r, http.StatusOK, "Estado de la búsqueda", dashboard.NexoSearchStatusTab, dashboard.NexoSearchStatusPageView(page))
}

// buildSearchStatusPage arma el texto de la pantalla a partir del estado. Es
// una función pura (sin base de datos ni reloj propio) para poder probarla.
func buildSearchStatusPage(st kbservice.SearchStatus) dashboard.NexoSearchStatusPage {
	p := dashboard.NexoSearchStatusPage{
		Config:   searchStatusConfig(st),
		Versions: searchStatusVersions(st),
		Progress: searchStatusProgress(st),
		Dead:     searchStatusDead(st),
		Evals:    searchStatusEvals(st),
		Params:   searchStatusParams(st),
		Rerank:   searchStatusRerank(st),
	}
	p.Verdict = searchStatusVerdict(st, p.Progress)
	return p
}

// searchStatusRerank es el bloque "Reordenamiento (R3)": proveedor, modelo y
// parámetros en uso (design §21). Nunca muestra la clave ni la URL del
// proveedor.
func searchStatusRerank(st kbservice.SearchStatus) dashboard.NexoSearchRerank {
	rr := st.Rerank
	if !rr.Enabled {
		return dashboard.NexoSearchRerank{Note: "Sin " + rerank.EnvReranker + " la búsqueda no se reordena. Ver el manual de producción §4.6.2."}
	}
	o := rr.Options
	str := func(k string) string {
		if v, ok := rr.Reranker[k].(string); ok && v != "" {
			return v
		}
		return "—"
	}
	def, defChip := "Solo si la consulta lo pide (rerank=on)", "nx-chip-ret"
	if o.Default {
		def, defChip = "En todas las consultas", "nx-chip-ok"
	}
	minScore := "Apagado"
	if o.MinScore > 0 {
		minScore = searchFloat(o.MinScore, 2)
	}
	rows := []dashboard.NexoSearchRow{
		{Label: "Proveedor", Value: str("provider"), Env: rerank.EnvReranker},
		{Label: "Modelo", Value: str("model"), Env: rerank.EnvModel},
		{Label: "Cuándo reordena", Value: def, Chip: defChip, Env: kbservice.EnvRerankDefault},
		{Label: "Candidatos que reordena", Value: strconv.Itoa(o.TopN), Env: kbservice.EnvRerankTopN},
		{Label: "Peso del reordenador", Value: searchFloat(o.Blend, 2), Env: kbservice.EnvRerankBlend, Note: "1 = solo el orden del reordenador"},
		{Label: "Puntaje mínimo", Value: minScore, Env: kbservice.EnvRerankMinScore},
		{Label: "Tiempo máximo por consulta", Value: o.Budget.String(), Env: kbservice.EnvRerankBudget, Note: "Pasado ese tiempo se responde con el orden de la búsqueda"},
	}
	if s := rr.Stats; s != nil {
		rows = append(rows, dashboard.NexoSearchRow{Label: "Uso desde el arranque",
			Value: fmt.Sprintf("%s llamadas, %s reintentos, %s lotes fallidos", searchInt(s.Calls), searchInt(s.Retries), searchInt(s.FailedBatch))})
	}
	return dashboard.NexoSearchRerank{Configured: true, Rows: rows}
}

// ─── Formato ─────────────────────────────────────────────────────────────────

// searchInt escribe un entero con un espacio fino entre miles (1 284).
func searchInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(" ")
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func searchWhen(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04")
}

func searchAgo(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d < time.Minute:
		return "menos de 1 min"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d días", int(d.Hours()/24))
}

func searchFloat(v float64, decimals int) string { return strconv.FormatFloat(v, 'f', decimals, 64) }

func searchPlural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return searchInt(n) + " " + many
}

// ─── Bloques ─────────────────────────────────────────────────────────────────

func searchStatusConfig(st kbservice.SearchStatus) []dashboard.NexoSearchRow {
	rows := []dashboard.NexoSearchRow{}
	text := dashboard.NexoSearchRow{Label: "Índice de texto", Value: "Al día", Chip: "nx-chip-ok", Note: "Versión " + strconv.Itoa(st.Text.ActiveVersion) + "."}
	if st.Text.NeedsReindex {
		text = dashboard.NexoSearchRow{Label: "Índice de texto", Value: "Necesita reindexar", Chip: "nx-chip-exp",
			Note: "Cambió la configuración de NEXO_KB_R1 desde el último indexado. Un superadmin lo rehace con: engram nexo kb reindex."}
	}
	mode := dashboard.NexoSearchRow{Label: "Modo por defecto", Value: "Solo texto", Chip: "nx-chip-ret", Note: st.Reason}
	if st.DefaultMode == search.ModeHybrid {
		mode = dashboard.NexoSearchRow{Label: "Modo por defecto", Value: "Texto y significado", Chip: "nx-chip-ok", Note: "Una consulta que no pide un modo usa este."}
	}
	rows = append(rows, mode)
	rows = append(rows, text)

	e := st.Embedder
	if !st.Enabled && e.Provider == embed.ProviderOff {
		rows = append(rows, dashboard.NexoSearchRow{Label: "Proveedor de embeddings", Value: "Sin proveedor", Env: embed.EnvEmbedder,
			Note: "Con un proveedor (gemini, openai, azure-openai u openai-compatible) la búsqueda suma el significado de las preguntas."})
	} else {
		rows = append(rows,
			dashboard.NexoSearchRow{Label: "Proveedor de embeddings", Value: e.Provider, Env: embed.EnvEmbedder},
			dashboard.NexoSearchRow{Label: "Modelo", Value: e.Model, Env: embed.EnvModel},
			dashboard.NexoSearchRow{Label: "Dimensiones", Value: strconv.Itoa(e.Dims), Env: embed.EnvDims, Note: "Almacenadas como " + e.Storage + " de pgvector."})
	}
	rows = append(rows, searchKeyRow(e))

	pg := dashboard.NexoSearchRow{Label: "pgvector", Value: "No disponible", Chip: "nx-chip-exp",
		Note: "La base no tiene la extensión. Con una cuenta con permiso: CREATE EXTENSION vector."}
	if st.PGVector.Available {
		iter := "no"
		if st.PGVector.Iterative {
			iter = "sí"
		}
		pg = dashboard.NexoSearchRow{Label: "pgvector", Value: "Disponible", Chip: "nx-chip-ok",
			Note: "Versión " + st.PGVector.Version + ", esquema " + st.PGVector.Schema + "; búsqueda iterativa: " + iter + "."}
	}
	return append(rows, pg)
}

// searchKeyRow dice si la clave está configurada y de dónde sale. Nunca
// recibe ni muestra la clave: embed.KeyInfo no la contiene.
func searchKeyRow(e kbservice.SearchEmbedder) dashboard.NexoSearchRow {
	row := dashboard.NexoSearchRow{Label: "Clave del proveedor"}
	k := e.Key
	switch {
	case e.Provider == embed.ProviderOff || e.Provider == "":
		row.Value = "No aplica"
	case !k.Required:
		row.Value = "No requiere clave"
		row.Note = "Este proveedor se usa sin autenticación."
	case k.Configured:
		row.Value, row.Chip = "Configurada", "nx-chip-ok"
		switch k.Source {
		case embed.KeyFromFile:
			row.Note = "Se lee del archivo que indica " + k.Variable + "."
		case embed.KeyFromEnv:
			row.Note = "Se lee de la variable de entorno " + k.Variable + "."
		}
	default:
		row.Value, row.Chip = "No configurada", "nx-chip-err"
		row.Note = "Define " + embed.EnvAPIKey + " o " + embed.EnvAPIKeyFile + " y reinicia el servidor."
	}
	return row
}

func searchStatusVersions(st kbservice.SearchStatus) []dashboard.NexoSearchVersion {
	out := make([]dashboard.NexoSearchVersion, 0, len(st.Versions))
	for _, v := range st.Versions {
		label, chip := v.Status, "nx-chip-ret"
		switch v.Status {
		case "active":
			label, chip = "Activa", "nx-chip-ok"
		case "building":
			label, chip = "En construcción", "nx-chip-sched"
		case "retired":
			label = "Retirada"
		}
		model := strings.TrimSpace(v.Model)
		dims := "-"
		if v.Dims > 0 {
			dims = strconv.Itoa(v.Dims)
		}
		out = append(out, dashboard.NexoSearchVersion{Version: strconv.Itoa(v.Version), Status: label, StatusChip: chip, Model: model, Provider: v.Provider, Dims: dims,
			Created: searchWhen(&v.CreatedAt), Activated: searchWhen(v.ActivatedAt), Retired: searchWhen(v.RetiredAt)})
	}
	return out
}

func searchStatusProgress(st kbservice.SearchStatus) []dashboard.NexoSearchProgress {
	out := []dashboard.NexoSearchProgress{}
	for _, q := range st.Queue {
		state := "activa"
		if q.Status == "building" {
			state = "en construcción"
		}
		done, total := q.Vectors, st.Sections
		pct := 0
		if total > 0 {
			pct = int(min(100, done*100/total))
		}
		text := fmt.Sprintf("%s de %s secciones con vector (%d %%)", searchInt(done), searchInt(total), pct)
		facts := []dashboard.NexoSearchRow{
			{Label: "Pendientes", Value: searchInt(q.Pending)},
			{Label: "En proceso", Value: searchInt(q.Claimed)},
			{Label: "Con fallos", Value: searchInt(q.Failing)},
			{Label: "Cambios sin leer", Value: searchInt(q.FeedLag)},
			{Label: "Cartas muertas", Value: searchInt(q.Dead)},
		}
		if q.Pending > 0 {
			facts = append(facts[:1], append([]dashboard.NexoSearchRow{{Label: "Espera más antigua", Value: searchAgo(q.OldestSeconds)}}, facts[1:]...)...)
		}
		out = append(out, dashboard.NexoSearchProgress{Label: fmt.Sprintf("Versión %d (%s), %s, %d dimensiones", q.Version, state, q.Model, q.Dims), Short: fmt.Sprintf("Versión %d (%s)", q.Version, state),
			Done: done, Total: total, Percent: pct, DoneText: text, Facts: facts, ProgressText: fmt.Sprintf("Versión %d: %s", q.Version, text)})
	}
	return out
}

func searchStatusDead(st kbservice.SearchStatus) dashboard.NexoSearchDead {
	d := dashboard.NexoSearchDead{Total: st.DeadTotal}
	if st.DeadTotal == 0 {
		d.Summary = "Ninguna. Todas las secciones que entraron al indexador tienen su vector o están en la cola."
		return d
	}
	d.Summary = searchPlural(st.DeadTotal, "sección", "secciones") + " fallaron cinco veces seguidas y quedaron fuera de la cola. Últimos errores del proveedor:"
	if st.DeadTotal == 1 {
		d.Summary = "1 sección falló cinco veces seguidas y quedó fuera de la cola. Último error del proveedor:"
	}
	for _, e := range st.Dead {
		msg := e.Error
		if msg == "" {
			msg = "(sin mensaje)"
		}
		d.Errors = append(d.Errors, dashboard.NexoSearchDeadError{When: e.FailedAt.UTC().Format("2006-01-02 15:04"), Version: strconv.Itoa(e.Version),
			Attempts: searchPlural(int64(e.FailCount), "intento", "intentos"), Message: msg})
	}
	return d
}

func searchStatusEvals(st kbservice.SearchStatus) []dashboard.NexoSearchEval {
	out := []dashboard.NexoSearchEval{}
	metric := func(m map[string]any, key string) string {
		if v, ok := m[key].(float64); ok {
			return searchFloat(v, 3)
		}
		return "-"
	}
	for _, r := range st.EvalRuns {
		questions := "-"
		if v, ok := r.Metrics["questions"].(float64); ok {
			questions = searchInt(int64(v))
		}
		set := r.QuestionSetName
		if set == "" {
			set = "-"
		}
		out = append(out, dashboard.NexoSearchEval{When: r.RanAt.UTC().Format("2006-01-02 15:04"), Retriever: r.Retriever, Mode: r.Mode, Set: set,
			Questions: questions, Hit5: metric(r.Metrics, "hit_at_5"), MRR: metric(r.Metrics, "mrr"), NoAnswer: metric(r.Metrics, "no_answer_accuracy"), Accepted: r.Accepted})
	}
	return out
}

func searchStatusParams(st kbservice.SearchStatus) []dashboard.NexoSearchParamGroup {
	r1, r2 := st.R1, st.R2
	h := r2.Hybrid
	off := func(on bool, v string) (string, string) {
		if on {
			return v, ""
		}
		return "Desactivado", "Se activa en NEXO_KB_R1."
	}
	covVal, covNote := off(r1.Threshold, searchFloat(r1.MinCoverage, 2))
	simVal, simNote := off(r1.Threshold, searchFloat(r2.MinSimilarity, 2))
	txtVal, txtNote := off(r1.Threshold, searchFloat(r2.MinTextSimilarity, 2))
	perDoc := "Sin límite"
	if r1.Diversity {
		perDoc = strconv.Itoa(r1.MaxPerDoc)
	}
	worker := "Activo"
	if !r2.Worker {
		worker = "Apagado"
	}
	return []dashboard.NexoSearchParamGroup{
		{Title: "Fusión de resultados (RRF)", Rows: []dashboard.NexoSearchRow{
			{Label: "Constante k", Value: strconv.Itoa(h.Weights.K), Env: kbservice.EnvRRFK},
			{Label: "Peso del significado", Value: searchFloat(h.Weights.Vector, 2), Env: kbservice.EnvRRFVectorWeight},
			{Label: "Peso del texto", Value: searchFloat(h.Weights.Text, 2), Env: kbservice.EnvRRFTextWeight},
		}},
		{Title: "Umbrales", Rows: []dashboard.NexoSearchRow{
			{Label: "Similitud mínima por significado", Value: simVal, Env: kbservice.EnvMinSimilarity, Note: simNote},
			{Label: "Similitud mínima por texto", Value: txtVal, Env: kbservice.EnvMinTextSimilarity, Note: txtNote},
			{Label: "Cobertura mínima de la pregunta", Value: covVal, Env: kbservice.EnvMinCoverage, Note: covNote},
			{Label: "Secciones por documento", Value: perDoc, Env: kbservice.EnvMaxPerDoc},
		}},
		{Title: "Consulta por significado", Rows: []dashboard.NexoSearchRow{
			{Label: "Candidatos del índice", Value: strconv.Itoa(h.Vector.Candidates), Env: kbservice.EnvVectorCandidates},
			{Label: "Recorrido exacto hasta", Value: searchPlural(int64(h.Vector.ExactMax), "sección", "secciones"), Env: kbservice.EnvVectorExactMax},
			{Label: "Búsqueda iterativa", Value: h.Vector.Iterative, Env: kbservice.EnvVectorIterative},
			{Label: "Tiempo máximo del vector de la pregunta", Value: strconv.Itoa(int(h.QueryTimeout/time.Second)) + " s"},
		}},
		{Title: "Indexador", Rows: []dashboard.NexoSearchRow{
			{Label: "Trabajador en segundo plano", Value: worker, Env: kbservice.EnvVectorWorker},
			{Label: "Secciones por lote", Value: strconv.Itoa(r2.Batch), Env: kbservice.EnvVectorBatch},
			{Label: "Revisa la cola cada", Value: strconv.Itoa(int(r2.Poll/time.Second)) + " s", Env: kbservice.EnvVectorPoll},
		}},
	}
}

// searchStatusVerdict es la frase principal: qué buscador responde hoy y si
// el índice por significado está al día.
func searchStatusVerdict(st kbservice.SearchStatus, progress []dashboard.NexoSearchProgress) dashboard.NexoSearchVerdict {
	var active, building *kbstore.VectorQueueStats
	for i := range st.Queue {
		switch st.Queue[i].Status {
		case "active":
			active = &st.Queue[i]
		case "building":
			building = &st.Queue[i]
		}
	}
	deadNote := ""
	if st.DeadTotal > 0 {
		deadNote = " " + searchPlural(st.DeadTotal, "sección no se pudo vectorizar", "secciones no se pudieron vectorizar") + " (ver más abajo)."
	}
	switch {
	case st.ConfigError != "":
		return dashboard.NexoSearchVerdict{Class: "warn", Title: "La búsqueda usa solo texto: la configuración de embeddings no es válida.", Detail: st.Reason}
	case !st.Enabled:
		return dashboard.NexoSearchVerdict{Class: "info", Title: "La búsqueda usa solo texto.",
			Detail: "Encuentra documentos por sus palabras. Puedes activar la búsqueda por significado desde la configuración del stack."}
	case !st.PGVector.Available:
		return dashboard.NexoSearchVerdict{Class: "warn", Title: "La búsqueda usa solo texto: falta la extensión pgvector en la base de datos.", Detail: st.Reason}
	case active == nil && building != nil:
		pct := 0
		if len(progress) > 0 {
			pct = progress[len(progress)-1].Percent
		}
		return dashboard.NexoSearchVerdict{Class: "warn", Title: fmt.Sprintf("El índice por significado se está construyendo (%d %%).", pct),
			Detail: "Mientras no haya una versión activa, la búsqueda usa solo texto. Cuando termine, pasa sola a texto y significado." + deadNote}
	case active == nil:
		return dashboard.NexoSearchVerdict{Class: "warn", Title: "La búsqueda usa solo texto: todavía no hay un índice por significado.", Detail: st.Reason}
	case st.DefaultMode != search.ModeHybrid:
		return dashboard.NexoSearchVerdict{Class: "warn", Title: "Hay un índice por significado, pero la búsqueda usa solo texto.", Detail: st.Reason}
	}
	waiting := active.Pending + active.FeedLag
	if waiting == 0 && st.DeadTotal == 0 {
		return dashboard.NexoSearchVerdict{Class: "ok", Title: "La búsqueda usa texto y significado, y el índice está al día.",
			Detail: "Las " + searchInt(st.Sections) + " secciones tienen su vector."}
	}
	detail := ""
	if waiting > 0 {
		detail = "Faltan " + searchPlural(waiting, "sección o cambio", "secciones o cambios") + " por vectorizar; mientras tanto esas secciones se encuentran solo por texto."
	}
	return dashboard.NexoSearchVerdict{Class: "info", Title: "La búsqueda usa texto y significado.", Detail: strings.TrimSpace(detail + deadNote)}
}
