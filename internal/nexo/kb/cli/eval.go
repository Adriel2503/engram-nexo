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

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/eval"
)

// ServerRetriever evalúa la búsqueda del servidor (pgfts) y la consulta de
// cambios (/changes).
type ServerRetriever struct{ C *Client }

// Name implementa eval.Retriever.
func (s ServerRetriever) Name() string { return "pgfts" }

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
	if q.Mode != "" {
		path += "&mode=" + url.QueryEscape(q.Mode)
	}
	var res SearchResponse
	if err := s.C.Do(ctx, "GET", path, nil, &res); err != nil {
		return eval.Outcome{}, err
	}
	out := eval.Outcome{NoResults: res.NoResults}
	for _, h := range res.Results {
		out.Hits = append(out.Hits, eval.Hit{Space: h.Space, DocKey: h.DocKey, Anchor: h.Anchor, Version: h.Version})
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
	retriever := fsx.String("retriever", "pgfts", "motor: pgfts (servidor); local-bm25 e hybrid llegan después")
	k := fsx.Int("k", 5, "Recall@k")
	server := fsx.String("server", "", "URL del servidor")
	asJSON := fsx.Bool("json", false, "salida JSON")
	validated := fsx.Bool("validated-only", false, "solo preguntas con status: validated")
	record := fsx.Bool("record", false, "guardar la corrida en el servidor (nexo_kb_eval_runs)")
	accept := fsx.Bool("accept", false, "con --record: marcar la corrida como aceptada (referencia del CI)")
	commit := fsx.String("commit", os.Getenv("GITHUB_SHA"), "commit medido (referencia)")
	note := fsx.String("note", "", "nota de la corrida")
	minHit := fsx.Float64("min-hit", 0, "salir con 1 si Hit@5 total queda por debajo (0-1)")
	baseTime := fsx.String("base-time", "", "momento de la marca @base de las preguntas (RFC 3339): entre la carga base y las versiones nuevas")
	if err := fsx.Parse(reorder(args, map[string]bool{"json": true, "validated-only": true, "record": true, "accept": true})); err != nil {
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
	var r ServerRetriever
	switch *retriever {
	case "pgfts", "server":
		c, err := newClient(dataDir, *server)
		if err != nil {
			return err
		}
		r = ServerRetriever{C: c}
	case "local-bm25", "hybrid":
		return exitError{2, "el motor " + *retriever + " llega con la copia local (tanda 2); hoy: --retriever pgfts"}
	default:
		return exitError{2, "motor desconocido: " + *retriever}
	}
	rep := eval.RunWith(context.Background(), set, r, eval.Options{K: *k, Depth: 10, ValidatedOnly: *validated})
	if *record {
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
		body := map[string]any{"report": rep, "accepted": *accept, "commit": *commit, "note": *note, "spaces": spaces, "mode": "fts"}
		var run struct {
			ID string `json:"id"`
		}
		if err := r.C.Do(context.Background(), "POST", "/nexo/kb/v1/eval-runs", body, &run); err != nil {
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
