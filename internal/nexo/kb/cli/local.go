package cli

// Entrega 2b, tanda 2 (D-01, D-03): copia local y lectura desde la terminal.
//
//	engram nexo kb sync [--status] [--json]
//	engram nexo kb get --space S <doc_key>[#ancla] [--anchor A] [--version N] [--local] [--json]
//	engram nexo kb search ... --local
//	engram nexo kb eval <preguntas.yaml> --retriever local-bm25

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/eval"
)

func openKB(dataDir string) (*client.KB, error) {
	kb, err := client.OpenKB(dataDir)
	if err != nil {
		return nil, err
	}
	if kb.API != nil {
		kb.API.UserAgent = client.UserAgent + " (cli)"
		kb.API.HTTP.Timeout = 60 * time.Second
	}
	return kb, nil
}

// ─── sync ────────────────────────────────────────────────────────────────────

func runSync(args []string, dataDir string, stdout io.Writer) error {
	fsx := flag.NewFlagSet("sync", flag.ContinueOnError)
	statusOnly := fsx.Bool("status", false, "solo mostrar el estado de la copia local")
	asJSON := fsx.Bool("json", false, "salida JSON")
	if err := fsx.Parse(reorder(args, map[string]bool{"status": true, "json": true})); err != nil {
		return exitError{2, err.Error()}
	}
	kb, err := openKB(dataDir)
	if err != nil {
		return err
	}
	defer kb.Close()
	ctx := context.Background()
	if *statusOnly {
		st := kb.Local.Status(ctx)
		if *asJSON {
			return json.NewEncoder(stdout).Encode(st)
		}
		printStatus(stdout, st)
		return nil
	}
	rep, err := kb.SyncNow(ctx)
	if *asJSON {
		out := map[string]any{"report": rep, "ok": err == nil}
		if err != nil {
			out["error"] = client.FriendlyError(err)
			out["offline"] = client.Unreachable(err)
		}
		_ = json.NewEncoder(stdout).Encode(out)
	}
	switch {
	case err == nil:
		if !*asJSON {
			fmt.Fprintf(stdout, "Copia local actualizada en %s: %d documentos actualizados, %d quitados", rep.Duration, rep.Upserts, rep.Removes)
			if len(rep.SpacesRemoved) > 0 {
				fmt.Fprintf(stdout, ", espacios quitados: %s", strings.Join(rep.SpacesRemoved, ", "))
			}
			if rep.Reason != "" {
				fmt.Fprintf(stdout, " (%s)", rep.Reason)
			}
			fmt.Fprintln(stdout)
			printStatus(stdout, rep.Status)
		}
		return nil
	case errors.Is(err, client.ErrNotConfigured):
		return exitError{1, err.Error()}
	case client.Unreachable(err):
		// Sin red no es un error para los hooks: se conserva la copia.
		if !*asJSON {
			fmt.Fprintln(stdout, "Sin conexión con el servidor: se conserva la copia local actual.")
			printStatus(stdout, rep.Status)
		}
		return nil
	}
	if *asJSON {
		return exitError{1, ""}
	}
	return exitError{1, "no se pudo actualizar la copia local: " + client.FriendlyError(err)}
}

func printStatus(w io.Writer, st client.Status) {
	if st.SyncedAt == nil {
		fmt.Fprintf(w, "Copia local %s: vacía (nunca se sincronizó)\n", st.Path)
		return
	}
	fmt.Fprintf(w, "Copia local %s: %d espacios, %d documentos, %d secciones; actualizada %s (%s); seq %d\n",
		st.Path, st.Spaces, st.Documents, st.Sections, st.SyncedAt.Local().Format("2006-01-02 15:04"), st.Age, st.Seq)
	if st.LastError != "" {
		fmt.Fprintf(w, "Último error: %s\n", st.LastError)
	}
}

// ─── get ─────────────────────────────────────────────────────────────────────

func runGet(args []string, dataDir string, stdout io.Writer) error {
	fsx := flag.NewFlagSet("get", flag.ContinueOnError)
	space := fsx.String("space", "", "espacio (slug)")
	anchor := fsx.String("anchor", "", "ancla de la sección (o doc_key#ancla)")
	version := fsx.Int("version", 0, "versión (por defecto la vigente)")
	local := fsx.Bool("local", false, "leer solo de la copia local")
	asJSON := fsx.Bool("json", false, "salida JSON")
	if err := fsx.Parse(reorder(args, map[string]bool{"local": true, "json": true})); err != nil {
		return exitError{2, err.Error()}
	}
	if *space == "" || fsx.NArg() != 1 {
		return exitError{2, "uso: engram nexo kb get --space S <doc_key>[#ancla] [--version N] [--local] [--json]"}
	}
	kb, err := openKB(dataDir)
	if err != nil {
		return err
	}
	defer kb.Close()
	res, err := kb.Get(context.Background(), client.GetParams{Space: *space, DocKey: fsx.Arg(0), Anchor: *anchor, Version: *version, LocalOnly: *local})
	if err != nil {
		return exitError{1, client.FriendlyError(err)}
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(res)
	}
	if res.Notice != "" {
		fmt.Fprintln(stdout, "Aviso: "+res.Notice)
	}
	fmt.Fprintf(stdout, "%s\nEstado: %s\n", res.Citation, res.Status)
	if res.DocNotice != "" {
		fmt.Fprintln(stdout, "Nota: "+res.DocNotice)
	}
	if res.Link != "" {
		fmt.Fprintln(stdout, "Enlace: "+res.Link)
	}
	if res.SourceURL != "" {
		fmt.Fprintln(stdout, "Fuente: "+res.SourceURL)
	}
	fmt.Fprintln(stdout, strings.Repeat("-", 60))
	fmt.Fprintln(stdout, strings.TrimSpace(res.Content))
	return nil
}

// ─── search --local ──────────────────────────────────────────────────────────

// runSearchLocal busca en la copia local. why es el motivo del aviso ("" =
// consulta pedida con --local).
func runSearchLocal(q string, spaces []string, category, tag, at, mode string, limit int, asJSON bool, why string, stdout io.Writer) error {
	atT, err := client.ParseWhen(at)
	if err != nil {
		return exitError{2, err.Error()}
	}
	l, err := client.OpenLocal(client.DefaultLocalPath())
	if err != nil {
		return err
	}
	defer l.Close()
	kb := client.NewKB(nil, l)
	res, err := kb.Search(context.Background(), client.SearchParams{Query: q, Spaces: spaces, Category: category, Tag: tag, At: atT, Mode: mode, Limit: limit, LocalOnly: true, LocalReason: why})
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(res)
	}
	fmt.Fprintln(stdout, "Aviso: "+res.Notice)
	if res.NoResults || len(res.Results) == 0 {
		fmt.Fprintln(stdout, res.Message)
		return nil
	}
	for i, h := range res.Results {
		fmt.Fprintf(stdout, "%d. %s\n   Estado: %s\n   %s\n   %s\n", i+1, h.Citation, h.Status, h.Quote, h.Link)
	}
	return nil
}

// ─── eval --retriever local-bm25 ─────────────────────────────────────────────

// LocalRetriever evalúa la copia local (SQLite FTS5 con BM25, design §14.5).
type LocalRetriever struct{ L *client.Local }

// Name implementa eval.Retriever.
func (r LocalRetriever) Name() string { return "local-bm25" }

// Search implementa eval.Retriever. La copia no guarda el historial de
// publicación: una pregunta con known_at da error (se cuenta como fallo).
func (r LocalRetriever) Search(ctx context.Context, q eval.Question, spaces []string, at, knownAt *time.Time, limit int) (eval.Outcome, error) {
	if knownAt != nil {
		return eval.Outcome{}, errors.New("la copia local no responde known_at")
	}
	res, err := r.L.Search(ctx, client.LocalQuery{Text: q.Question, Spaces: spaces, At: at, Limit: limit, Mode: q.Mode})
	if err != nil {
		return eval.Outcome{}, err
	}
	out := eval.Outcome{NoResults: res.NoResults}
	for _, h := range res.Hits {
		out.Hits = append(out.Hits, eval.Hit{Space: h.Space, DocKey: h.DocKey, Anchor: h.Anchor, Version: h.Version})
	}
	return out, nil
}

func evalMode(retriever string) string {
	if retriever == "local-bm25" || retriever == "local" {
		return "local-bm25"
	}
	return "fts"
}
