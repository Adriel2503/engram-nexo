package cli

// engram nexo kb eval (R0, RC-01, RC-28): mide la búsqueda del servidor con
// un conjunto de preguntas por clase y, con --record, guarda la corrida en
// nexo_kb_eval_runs.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/eval"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
)

// ServerRetriever evalúa la búsqueda del servidor (pgfts) y la consulta de
// cambios (/changes).
type ServerRetriever struct {
	C *Client
	// Mode es el modo de las preguntas que no fijan uno (NX-10 R2: hybrid).
	Mode string
	// Rerank (NX-10 R3): on u off para medir con o sin reordenamiento; vacío =
	// el del servidor.
	Rerank string
}

// Name implementa eval.Retriever.
func (s ServerRetriever) Name() string {
	name := "pgfts"
	if s.Mode == "hybrid" {
		name = "hybrid"
	}
	if s.Rerank == "on" {
		name += "+rerank"
	}
	return name
}

// Search implementa eval.Retriever.
func (s ServerRetriever) Search(ctx context.Context, q eval.Question, spaces []string, at, knownAt *time.Time, limit int) (eval.Outcome, error) {
	atS, knS := "", ""
	if at != nil {
		atS = at.Format(time.RFC3339)
	}
	if knownAt != nil {
		knS = knownAt.Format(time.RFC3339)
	}
	path := SearchQuery(q.Question, spaces, "", "", atS, knS, limit)
	mode := q.Mode
	if mode == "" {
		mode = s.Mode
	}
	if mode != "" {
		path += "&mode=" + url.QueryEscape(mode)
	}
	if s.Rerank != "" {
		path += "&rerank=" + url.QueryEscape(s.Rerank)
	}
	var res SearchResponse
	if err := s.C.Do(ctx, "GET", path, nil, &res); err != nil {
		return eval.Outcome{}, err
	}
	out := eval.Outcome{NoResults: res.NoResults}
	for _, h := range res.Results {
		out.Hits = append(out.Hits, eval.Hit{Space: h.Space, DocKey: h.DocKey, Anchor: h.Anchor, Version: h.Version, PrePos: h.PreRerankPosition})
	}
	if ri := res.Rerank; ri != nil {
		out.Rerank = &eval.RerankTrace{Status: ri.Status, Reason: ri.Reason, LatencyMS: ri.LatencyMS, Candidates: ri.Candidates, Reordered: ri.Reordered,
			TopChanged: ri.TopChanged, Dropped: ri.Dropped}
	}
	return out, nil
}

// Changes implementa eval.ChangeLister.
func (s ServerRetriever) Changes(ctx context.Context, q eval.Question, spaces []string, since, until *time.Time) ([]eval.Hit, error) {
	v := url.Values{}
	if since != nil {
		v.Set("since", since.Format(time.RFC3339))
	}
	if until != nil {
		v.Set("until", until.Format(time.RFC3339))
	}
	if len(spaces) > 0 {
		v.Set("spaces", strings.Join(spaces, ","))
	}
	var res struct {
		Changes []struct {
			Space         string `json:"space"`
			DocKey        string `json:"doc_key"`
			Anchor        string `json:"anchor"`
			Change        string `json:"change"`
			VersionBefore int    `json:"version_before"`
			VersionAfter  int    `json:"version_after"`
		} `json:"changes"`
	}
	if err := s.C.Do(ctx, "GET", "/nexo/kb/v1/changes?"+v.Encode(), nil, &res); err != nil {
		return nil, err
	}
	out := make([]eval.Hit, 0, len(res.Changes))
	for _, c := range res.Changes {
		ver := c.VersionAfter
		if ver == 0 {
			ver = c.VersionBefore
		}
		out = append(out, eval.Hit{Space: c.Space, DocKey: c.DocKey, Anchor: c.Anchor, Version: ver, Change: c.Change})
	}
	return out, nil
}

func runEval(args []string, dataDir string, stdout io.Writer) error {
	fsx := flag.NewFlagSet("eval", flag.ContinueOnError)
	retriever := fsx.String("retriever", "pgfts", "motor: pgfts (servidor, texto), hybrid (servidor, texto y significado, R2) o local-bm25 (copia local, FTS5)")
	k := fsx.Int("k", 5, "Recall@k")
	server := fsx.String("server", "", "URL del servidor")
	asJSON := fsx.Bool("json", false, "salida JSON")
	validated := fsx.Bool("validated-only", false, "solo preguntas con status: validated")
	record := fsx.Bool("record", false, "guardar la corrida en el servidor (nexo_kb_eval_runs)")
	accept := fsx.Bool("accept", false, "con --record: marcar la corrida como aceptada (referencia del CI)")
	commit := fsx.String("commit", os.Getenv("GITHUB_SHA"), "commit medido (referencia)")
	note := fsx.String("note", "", "nota de la corrida")
	minHit := fsx.Float64("min-hit", 0, "salir con 1 si Hit@5 total queda por debajo (0-1)")
	waitIndex := fsx.Bool("wait-index", false, "con hybrid: esperar a que el índice vectorial esté activo y sin secciones pendientes (requiere superadmin)")
	baseTime := fsx.String("base-time", "", "momento de la marca @base de las preguntas (RFC 3339): entre la carga base y las versiones nuevas")
	rerankSel := fsx.String("rerank", "off", "R3: on (reordenar los mejores resultados con el modelo del servidor, motor hybrid+rerank), off (por defecto: sin reordenar, para comparar) o auto (lo decide el servidor)")
	if err := fsx.Parse(reorder(args, map[string]bool{"json": true, "validated-only": true, "record": true, "accept": true, "wait-index": true})); err != nil {
		return exitError{2, err.Error()}
	}
	if fsx.NArg() != 1 {
		return exitError{2, "uso: engram nexo kb eval <preguntas.yaml> [--retriever pgfts] [--k 5] [--validated-only] [--record [--accept]] [--min-hit 0.8]"}
	}
	set, err := eval.Load(fsx.Arg(0))
	if err == nil && *baseTime != "" {
		bt, perr := eval.ParseTime(*baseTime)
		if perr != nil || bt == nil {
			return exitError{2, "--base-time inválido (RFC 3339)"}
		}
		set.Bind(map[string]time.Time{eval.MarkBase: *bt})
	}
	if err != nil {
		return err
	}
	rr, okRerank := search.NormalizeRerank(*rerankSel)
	if !okRerank {
		return exitError{2, "--rerank debe ser on, off o auto"}
	}
	if *rerankSel == "off" || *rerankSel == "" {
		rr = search.RerankOff // la comparación es explícita: sin reordenar aunque el servidor lo active por defecto
	}
	if rr == search.RerankOn && (*retriever == "local-bm25" || *retriever == "local") {
		return exitError{2, "la copia local no reordena: --rerank on exige --retriever pgfts o hybrid"}
	}
	var r eval.Retriever
	var c *Client
	switch *retriever {
	case "pgfts", "server":
		if c, err = newClient(dataDir, *server); err != nil {
			return err
		}
		r = ServerRetriever{C: c, Mode: "fts", Rerank: rr} // NX-10 R2: texto aunque el servidor tenga índice vectorial
	case "local-bm25", "local":
		// Tanda 2 (C-10): la copia local con BM25 (FTS5), sin servidor.
		l, err := client.OpenLocal(client.DefaultLocalPath())
		if err != nil {
			return err
		}
		defer l.Close()
		r = LocalRetriever{L: l}
	case "hybrid":
		if c, err = newClient(dataDir, *server); err != nil {
			return err
		}
		if *waitIndex {
			if err := waitVectorIndex(c, stdout); err != nil {
				return err
			}
		}
		r = ServerRetriever{C: c, Mode: "hybrid", Rerank: rr}
	default:
		return exitError{2, "motor desconocido: " + *retriever}
	}
	rep := eval.RunWith(context.Background(), set, r, eval.Options{K: *k, Depth: 10, ValidatedOnly: *validated})
	if *record {
		if c == nil {
			if c, err = newClient(dataDir, *server); err != nil {
				return err
			}
		}
		spaces := set.Spaces
		if len(spaces) == 0 {
			seen := map[string]bool{}
			for _, q := range set.Questions {
				for _, s := range q.Spaces {
					if !seen[s] {
						seen[s] = true
						spaces = append(spaces, s)
					}
				}
			}
		}
		body := map[string]any{"report": rep, "accepted": *accept, "commit": *commit, "note": *note, "spaces": spaces, "mode": evalMode(*retriever, rr)}
		var run struct {
			ID string `json:"id"`
		}
		if err := c.Do(context.Background(), "POST", "/nexo/kb/v1/eval-runs", body, &run); err != nil {
			return fmt.Errorf("registrar la corrida: %w", err)
		}
		fmt.Fprintf(stdout, "corrida registrada: %s\n", run.ID)
	}
	if *asJSON {
		if err := json.NewEncoder(stdout).Encode(rep); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(stdout, rep.Summary())
		fmt.Fprint(stdout, rep.Table())
		if line := rep.RerankLine(); line != "" {
			_, _ = fmt.Fprintln(stdout, line)
		}
		for _, q := range rep.Results {
			mark := "acierto"
			switch {
			case q.Error != "":
				mark = "error: " + q.Error
			case q.Class == eval.ClassNoAnswer && q.Correct:
				mark = "acierto (no hay respuesta)"
			case q.Class == eval.ClassNoAnswer:
				mark = "respondió algo"
			case q.Rank == 0:
				mark = "sin acierto"
			case q.Rank > rep.K:
				mark = fmt.Sprintf("fuera de @%d", rep.K)
			}
			fmt.Fprintf(stdout, "  %-34s %-10s rank=%-2d %s\n", q.ID, q.Class, q.Rank, mark)
		}
	}
	if *minHit > 0 && rep.Total.HitAt5 < *minHit {
		return exitError{1, fmt.Sprintf("calidad por debajo del umbral: Hit@5 %.3f < %.3f", rep.Total.HitAt5, *minHit)}
	}
	return nil
}

// waitVectorIndex espera (hasta 30 minutos) a que haya una versión activa
// del índice vectorial y ninguna sección pendiente (eval --wait-index).
func waitVectorIndex(c *Client, stdout io.Writer) error {
	deadline := time.Now().Add(30 * time.Minute)
	for {
		var st struct {
			Vector struct {
				Enabled  bool   `json:"enabled"`
				Reason   string `json:"reason"`
				Versions []struct {
					Version int    `json:"version"`
					Status  string `json:"status"`
					Pending int64  `json:"pending"`
					FeedLag int64  `json:"feed_lag"`
				} `json:"versions"`
			} `json:"vector"`
		}
		if err := c.Do(context.Background(), "GET", "/nexo/kb/v1/admin/index", nil, &st); err != nil {
			return fmt.Errorf("--wait-index: %w", err)
		}
		if !st.Vector.Enabled {
			return exitError{1, "el servidor no tiene búsqueda por significado: " + st.Vector.Reason}
		}
		active, pending := false, int64(0)
		for _, v := range st.Vector.Versions {
			active = active || v.Status == "active"
			pending += v.Pending + v.FeedLag
		}
		if active && pending == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return exitError{1, fmt.Sprintf("--wait-index: el índice vectorial sigue con %d secciones pendientes", pending)}
		}
		_, _ = fmt.Fprintf(stdout, "esperando el índice vectorial: %d secciones pendientes\n", pending)
		time.Sleep(10 * time.Second)
	}
}
