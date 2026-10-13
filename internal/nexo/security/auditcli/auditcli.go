// Package auditcli implementa `engram nexo audit` (NX-07 fase S4): verificar
// la cadena de hashes, exportar la auditoría firmada y verificar un archivo
// exportado. Se conecta directo a Postgres con ENGRAM_DATABASE_URL (la cuenta
// de aplicación alcanza: solo lee e inserta el registro de exportación).
package auditcli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// DatabaseURLEnv es la conexión que usa la CLI.
const DatabaseURLEnv = "ENGRAM_DATABASE_URL"

const usage = `Uso: engram nexo audit <comando> [opciones]

Comandos:
  verify [--table T]                         verifica la cadena de hashes de
                                             cloud_auth_audit_log, nexo_authz_events y
                                             nexo_kb_events (o solo T). Sale con 1 si falla.
  export --since AAAA-MM-DD [--until AAAA-MM-DD] [--include-today]
                                             exporta los días (UTC) a archivos JSONL firmados
                                             en NEXO_AUDIT_EXPORT_DIR (clave NEXO_AUDIT_EXPORT_KEY);
                                             los días ya exportados se saltan.
  verify-export ARCHIVO.jsonl [--db]         verifica la firma, el contenido y la continuidad
                                             de un archivo exportado; con --db, también contra la base.
  verify-review ARCHIVO.csv|ARCHIVO.json
                                             comprueba la huella SHA-256 de un archivo descargado de la
                                             revisión de accesos (no usa la base).

Conexión: ENGRAM_DATABASE_URL. La salida es JSON.
`

// Run ejecuta `engram nexo audit <args>` y devuelve el código de salida.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	cmd, rest := args[0], args[1:]
	ctx := context.Background()
	var (
		ok  bool
		out any
		err error
	)
	switch cmd {
	case "verify":
		out, ok, err = runVerify(ctx, rest)
	case "export":
		out, ok, err = runExport(ctx, rest)
	case "verify-review":
		out, ok, err = runVerifyReview(ctx, rest)
	case "verify-export":
		out, ok, err = runVerifyExport(ctx, rest)
	default:
		fmt.Fprintf(stderr, "comando desconocido: %s\n\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(stderr, "%s\n\n%s", ue.msg, usage)
			return 2
		}
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
	if !ok {
		return 1
	}
	return 0
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func openDB() (*sql.DB, error) {
	dsn := strings.TrimSpace(os.Getenv(DatabaseURLEnv))
	if dsn == "" {
		return nil, fmt.Errorf("%s no está definida", DatabaseURLEnv)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("conectar con la base: %w", err)
	}
	return db, nil
}

func runVerify(ctx context.Context, args []string) (any, bool, error) {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	table := fs.String("table", "", "tabla")
	if err := fs.Parse(args); err != nil {
		return nil, false, usageError{err.Error()}
	}
	var tables []string
	if *table != "" {
		if _, ok := evidence.ChainSpecFor(*table); !ok {
			return nil, false, usageError{fmt.Sprintf("tabla sin cadena de hashes: %s", *table)}
		}
		tables = []string{*table}
	}
	db, err := openDB()
	if err != nil {
		return nil, false, err
	}
	defer db.Close()
	rep, err := evidence.VerifyAll(ctx, db, tables)
	if err != nil {
		return nil, false, err
	}
	return rep, rep.OK, nil
}

func parseDay(s string) (time.Time, error) {
	if strings.EqualFold(s, "today") || strings.EqualFold(s, "hoy") {
		n := time.Now().UTC()
		return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	return time.Parse(time.DateOnly, s)
}

func runExport(ctx context.Context, args []string) (any, bool, error) {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	since := fs.String("since", "", "AAAA-MM-DD")
	until := fs.String("until", "", "AAAA-MM-DD")
	today := fs.Bool("include-today", false, "exportar también el día en curso (parcial)")
	if err := fs.Parse(args); err != nil {
		return nil, false, usageError{err.Error()}
	}
	if *since == "" {
		return nil, false, usageError{"falta --since AAAA-MM-DD"}
	}
	from, err := parseDay(*since)
	if err != nil {
		return nil, false, usageError{"--since debe ser AAAA-MM-DD"}
	}
	var to time.Time
	if *until != "" {
		if to, err = parseDay(*until); err != nil {
			return nil, false, usageError{"--until debe ser AAAA-MM-DD"}
		}
	}
	cfg, ok, why := evidence.ExportConfigFromEnv()
	if !ok {
		return nil, false, errors.New(why)
	}
	db, err := openDB()
	if err != nil {
		return nil, false, err
	}
	defer db.Close()
	res, err := evidence.ExportRange(ctx, db, cfg, from, to, time.Now(), "cli", *today)
	if err != nil {
		return nil, false, err
	}
	if res == nil {
		res = []evidence.ExportResult{}
	}
	return map[string]any{"dir": cfg.Dir, "key_id": cfg.KeyID(), "results": res}, true, nil
}

func runVerifyExport(ctx context.Context, args []string) (any, bool, error) {
	var file string
	var withDB bool
	for _, a := range args {
		switch {
		case a == "--db" || a == "-db":
			withDB = true
		case strings.HasPrefix(a, "-"):
			return nil, false, usageError{"opción desconocida: " + a}
		case file == "":
			file = a
		default:
			return nil, false, usageError{"un solo archivo"}
		}
	}
	if file == "" {
		return nil, false, usageError{"falta el archivo .jsonl"}
	}
	key := []byte(strings.TrimSpace(os.Getenv(evidence.ExportKeyEnv)))
	var db *sql.DB
	if withDB {
		var err error
		if db, err = openDB(); err != nil {
			return nil, false, err
		}
		defer db.Close()
	}
	chk, err := evidence.VerifyExport(ctx, file, key, db)
	if err != nil {
		return nil, false, err
	}
	return chk, chk.OK, nil
}
