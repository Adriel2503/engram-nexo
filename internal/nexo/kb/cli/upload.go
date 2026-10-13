package cli

// engram nexo kb upload (NX-10 entrega 2b, HU-29): sube archivos sueltos, una
// carpeta (se comprime en memoria con sus rutas relativas, sin los archivos
// ocultos) o un zip a POST /nexo/kb/v1/spaces/{slug}/uploads, la misma API
// que usa el panel. También: changes (qué cambió desde una fecha) y reindex.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

// DoMultipart envía un cuerpo multipart y decodifica la respuesta JSON.
func (c *Client) DoMultipart(ctx context.Context, path string, body *bytes.Buffer, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "POST", c.Server+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "engram-nexo-kb/1")
	req.Header.Set("Content-Type", contentType)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		ae := &APIError{Status: resp.StatusCode}
		_ = json.Unmarshal(raw, ae)
		if ae.Msg == "" {
			ae.Msg = strings.TrimSpace(string(raw))
		}
		return ae
	}
	return json.Unmarshal(raw, out)
}

// uploadable: extensiones que viajan en una carpeta (lo demás no se indexa).
func uploadable(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown", ".mdx", ".txt", ".text", ".docx":
		return true
	}
	return name == ".order"
}

// ZipFolder comprime una carpeta en memoria con rutas relativas (sin
// carpetas ocultas como .git o .attachments; .order sí viaja).
func ZipFolder(dir string) ([]byte, int, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, 0, err
	}
	root := strings.ReplaceAll(filepath.Base(abs), "/", "_")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	n := 0
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !uploadable(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		// Dentro de una carpeta raíz con el nombre de la carpeta: el servidor la
		// quita (carpeta contenedora) y las rutas quedan relativas a ella.
		w, err := zw.Create(root + "/" + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	if err := zw.Close(); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), n, nil
}

func runUpload(args []string, dataDir string, stdout io.Writer) error {
	fsx := flag.NewFlagSet("upload", flag.ContinueOnError)
	space := fsx.String("space", "", "espacio (slug)")
	dryRun := fsx.Bool("dry-run", false, "mostrar el resultado sin publicar")
	prune := fsx.Bool("prune", false, "retirar los documentos de la misma colección que ya no vienen")
	confirm := fsx.Bool("confirm-prune", false, "permitir una poda mayor que el tope de seguridad (KP-01)")
	source := fsx.String("source", "auto", "fuente: auto, dir o azure-devops-wiki")
	collection := fsx.String("collection", "", "nombre de la colección (referencia de la poda); por defecto, la carpeta o el zip")
	wikiURL := fsx.String("wiki-url", "", "URL de la wiki de Azure DevOps (enlaces a la fuente)")
	category := fsx.String("category", "", "categoría por defecto")
	owner := fsx.String("owner", "", "responsable por defecto")
	note := fsx.String("note", "", "nota del cambio")
	server := fsx.String("server", "", "URL del servidor")
	asJSON := fsx.Bool("json", false, "salida JSON")
	if err := fsx.Parse(reorder(args, map[string]bool{"dry-run": true, "prune": true, "confirm-prune": true, "json": true})); err != nil {
		return exitError{2, err.Error()}
	}
	paths := fsx.Args()
	if *space == "" || len(paths) == 0 {
		return exitError{2, "uso: engram nexo kb upload --space S [--dry-run] [--prune] <archivos | carpeta | archivo.zip>"}
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	addFile := func(field, name string, data []byte) error {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, strings.ReplaceAll(name, `"`, "_")))
		h.Set("Content-Type", "application/octet-stream")
		w, err := mw.CreatePart(h)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	coll := *collection
	files := 0
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			data, n, err := ZipFolder(p)
			if err != nil {
				return err
			}
			if n == 0 {
				return exitError{2, "la carpeta " + p + " no tiene archivos .md, .txt ni .docx"}
			}
			abs, _ := filepath.Abs(p)
			if coll == "" {
				coll = filepath.Base(abs)
			}
			if err := addFile("zip", filepath.Base(abs)+".zip", data); err != nil {
				return err
			}
			files += n
		case strings.EqualFold(filepath.Ext(p), ".zip"):
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if err := addFile("zip", filepath.Base(p), data); err != nil {
				return err
			}
			files++
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if err := addFile("file", filepath.Base(p), data); err != nil {
				return err
			}
			files++
		}
	}
	fields := map[string]string{"source": *source, "collection": coll, "wiki_url": *wikiURL, "category": *category, "owner": *owner, "change_note": *note}
	if *dryRun {
		fields["dry_run"] = "true"
	}
	if *prune {
		fields["prune"] = "true"
	}
	if *confirm {
		fields["confirm_prune"] = "true"
	}
	for k, v := range fields {
		if v != "" {
			if err := mw.WriteField(k, v); err != nil {
				return err
			}
		}
	}
	if err := mw.Close(); err != nil {
		return err
	}
	c, err := newClient(dataDir, *server)
	if err != nil {
		return err
	}
	var res service.BatchResult
	if err := c.DoMultipart(context.Background(), spacePath(*space)+"/uploads", &body, mw.FormDataContentType(), &res); err != nil {
		return err
	}
	if *asJSON {
		if err := json.NewEncoder(stdout).Encode(res); err != nil {
			return err
		}
	} else {
		printBatch(stdout, res)
		if res.Source != "" {
			fmt.Fprintf(stdout, "Fuente: %s; ejecución %s\n", res.Source, res.RunID)
		}
		for _, sk := range res.Skipped {
			fmt.Fprintf(stdout, "  aviso: %s: %s\n", sk.Path, sk.Reason)
		}
	}
	if res.Summary[domain.ResultRejected] > 0 {
		return exitError{1, fmt.Sprintf("%d documentos rechazados", res.Summary[domain.ResultRejected])}
	}
	if *prune && res.PruneSkipped != "" {
		return exitError{1, "poda omitida: " + res.PruneSkipped}
	}
	return nil
}

func runChanges(args []string, dataDir string, stdout io.Writer) error {
	fsx := flag.NewFlagSet("changes", flag.ContinueOnError)
	var spaces multiFlag
	fsx.Var(&spaces, "space", "espacio (repetible)")
	since := fsx.String("since", "", "desde (YYYY-MM-DD o RFC 3339)")
	until := fsx.String("until", "", "hasta (por defecto, ahora)")
	clock := fsx.String("clock", "valid", "reloj: valid (lo que regía) o recorded (lo publicado)")
	server := fsx.String("server", "", "URL del servidor")
	asJSON := fsx.Bool("json", false, "salida JSON")
	if err := fsx.Parse(reorder(args, map[string]bool{"json": true})); err != nil {
		return exitError{2, err.Error()}
	}
	if *since == "" {
		return exitError{2, "falta --since"}
	}
	v := url.Values{}
	v.Set("since", *since)
	if *until != "" {
		v.Set("until", *until)
	}
	v.Set("clock", *clock)
	if len(spaces) > 0 {
		v.Set("spaces", strings.Join(spaces, ","))
	}
	c, err := newClient(dataDir, *server)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.Do(context.Background(), "GET", "/nexo/kb/v1/changes?"+v.Encode(), nil, &raw); err != nil {
		return err
	}
	if *asJSON {
		_, err := stdout.Write(append(raw, '\n'))
		return err
	}
	var res struct {
		Changes []struct {
			Space, DocKey, Anchor, Change, Link string
			At                                  string `json:"at"`
		} `json:"changes"`
		NoChanges bool `json:"no_changes"`
	}
	_ = json.Unmarshal(raw, &res)
	if res.NoChanges {
		fmt.Fprintln(stdout, "Sin cambios en ese período.")
	}
	for _, ch := range res.Changes {
		fmt.Fprintf(stdout, "%-9s %s/%s#%s  %s\n", ch.Change, ch.Space, ch.DocKey, ch.Anchor, ch.At)
	}
	return nil
}

func runReindex(args []string, dataDir string, stdout io.Writer) error {
	fsx := flag.NewFlagSet("reindex", flag.ContinueOnError)
	server := fsx.String("server", "", "URL del servidor")
	status := fsx.Bool("status", false, "solo mostrar el estado del índice")
	if err := fsx.Parse(reorder(args, map[string]bool{"status": true})); err != nil {
		return exitError{2, err.Error()}
	}
	c, err := newClient(dataDir, *server)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if *status {
		err = c.Do(context.Background(), "GET", "/nexo/kb/v1/admin/index", nil, &raw)
	} else {
		err = c.Do(context.Background(), "POST", "/nexo/kb/v1/admin/index:reindex", nil, &raw)
	}
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(raw, '\n'))
	return err
}
