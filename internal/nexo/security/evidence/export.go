package evidence

// S4-02 (RS-31, design §5.2 y §8.4): exportación diaria firmada de la
// evidencia fuera de la base.
//
//   - Un archivo JSONL por tabla y día (UTC) en
//     NEXO_AUDIT_EXPORT_DIR/<tabla>/<AAAA-MM-DD>.jsonl, más
//     <AAAA-MM-DD>.jsonl.sig con los datos del archivo (filas, posiciones de
//     la cadena, último hash, sha256 del archivo) y su HMAC-SHA256 con
//     NEXO_AUDIT_EXPORT_KEY. Registro en nexo_audit_exports.
//   - En las tablas encadenadas el archivo de un día toma las posiciones de la
//     cadena siguientes al último archivo exportado: dos días seguidos
//     enlazan (último hash de uno = prev_hash de la primera fila del otro),
//     aunque una fila tenga una hora anterior (eventos con hora explícita).
//   - Un día sin eventos también deja su archivo (vacío y firmado): así no se
//     puede alegar que faltó la exportación.
//   - Sin NEXO_AUDIT_EXPORT_DIR la exportación está desactivada (aviso al
//     iniciar). Sin clave (o con menos de 32 bytes), también.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Variables de la exportación.
const (
	ExportDirEnv = "NEXO_AUDIT_EXPORT_DIR"
	ExportKeyEnv = "NEXO_AUDIT_EXPORT_KEY"
	// ExportFormat identifica el formato del archivo de firma.
	ExportFormat = "nexo-audit-export/v2"
	// MinExportKeyBytes es el largo mínimo de la clave HMAC.
	MinExportKeyBytes = 32
)

// ExportTables: tablas que se exportan (las encadenadas primero).
var ExportTables = []string{"cloud_auth_audit_log", "nexo_authz_events", "nexo_kb_events", "nexo_alerts", "nexo_alert_acks", "nexo_access_reviews"}

// ExportConfig es la configuración de la exportación.
type ExportConfig struct {
	Dir string
	Key []byte
}

// KeyID identifica la clave sin revelarla (rotación).
func (c ExportConfig) KeyID() string {
	sum := sha256.Sum256(append([]byte("nexo-export-key-id|"), c.Key...))
	return hex.EncodeToString(sum[:4])
}

// ExportConfigFromEnv lee la configuración. ok = false con el motivo si la
// exportación está desactivada.
func ExportConfigFromEnv() (ExportConfig, bool, string) {
	dir := strings.TrimSpace(os.Getenv(ExportDirEnv))
	if dir == "" {
		return ExportConfig{}, false, ExportDirEnv + " vacío: la exportación firmada de la auditoría está desactivada (RS-31)"
	}
	key := strings.TrimSpace(os.Getenv(ExportKeyEnv))
	if len(key) < MinExportKeyBytes {
		return ExportConfig{}, false, fmt.Sprintf("%s ausente o con menos de %d caracteres: la exportación está desactivada (genere una con openssl rand -hex 32)", ExportKeyEnv, MinExportKeyBytes)
	}
	return ExportConfig{Dir: dir, Key: []byte(key)}, true, ""
}

// Signature es el contenido del archivo .sig.
type Signature struct {
	Format        string `json:"format"`
	Table         string `json:"table"`
	Date          string `json:"date"`
	File          string `json:"file"`
	Rows          int64  `json:"rows"`
	ChainedRows   int64  `json:"chained_rows"`
	FirstSeq      int64  `json:"first_seq"`
	LastSeq       int64  `json:"last_seq"`
	PrevLastSeq   int64  `json:"prev_last_seq"`
	FirstPrevHash string `json:"first_prev_hash"`
	LastHash      string `json:"last_hash"`
	FileSHA256    string `json:"file_sha256"`
	CreatedAt     string `json:"created_at"`
	CreatedBy     string `json:"created_by"`
	KeyID         string `json:"key_id"`
	HMAC          string `json:"hmac_sha256"`
}

// message es el texto firmado (todos los campos menos la firma, en orden).
func (s Signature) message() []byte {
	return []byte(strings.Join([]string{
		s.Format, s.Table, s.Date, s.File, strconv.FormatInt(s.Rows, 10), strconv.FormatInt(s.ChainedRows, 10),
		strconv.FormatInt(s.FirstSeq, 10), strconv.FormatInt(s.LastSeq, 10), strconv.FormatInt(s.PrevLastSeq, 10),
		s.FirstPrevHash, s.LastHash, s.FileSHA256, s.CreatedAt, s.CreatedBy, s.KeyID,
	}, "\n"))
}

func sign(key []byte, s Signature) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(s.message())
	return hex.EncodeToString(mac.Sum(nil))
}

// ExportResult es el resultado de exportar una tabla y un día.
type ExportResult struct {
	Table    string `json:"table"`
	Date     string `json:"date"`
	File     string `json:"file"`
	Rows     int64  `json:"rows"`
	FirstSeq int64  `json:"first_seq,omitempty"`
	LastSeq  int64  `json:"last_seq,omitempty"`
	Skipped  bool   `json:"skipped,omitempty"` // ya exportado
}

// ExportDay exporta una tabla y un día (UTC). Si ya está registrado, no hace
// nada (Skipped).
func ExportDay(ctx context.Context, db *sql.DB, cfg ExportConfig, table string, day time.Time, createdBy string) (ExportResult, error) {
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	date := day.Format(time.DateOnly)
	res := ExportResult{Table: table, Date: date}
	if !validExportTable(table) {
		return res, fmt.Errorf("evidence: exportar: tabla %q no exportable", table)
	}
	var exists, tableExists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&tableExists); err != nil {
		return res, err
	}
	if !tableExists {
		res.Skipped = true
		return res, nil
	}
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM nexo_audit_exports WHERE table_name = $1 AND day = $2::date)`, table, date).Scan(&exists); err != nil {
		return res, fmt.Errorf("evidence: exportar: %w", err)
	}
	if exists {
		res.Skipped = true
		return res, nil
	}
	dayEnd := day.Add(24 * time.Hour)
	_, chained := ChainSpecFor(table)

	sig := Signature{Format: ExportFormat, Table: table, Date: date, File: date + ".jsonl",
		CreatedAt: time.Now().UTC().Format(time.RFC3339), CreatedBy: createdBy, KeyID: cfg.KeyID()}
	var buf bytes.Buffer

	if chained {
		// Un día anterior a otro ya exportado no se exporta: sus filas ya
		// están en ese archivo (la cadena se exporta en orden).
		var later bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM nexo_audit_exports WHERE table_name = $1 AND day > $2::date)`, table, date).Scan(&later); err != nil {
			return res, fmt.Errorf("evidence: exportar: %w", err)
		}
		if later {
			res.Skipped = true
			return res, nil
		}
		// Posición de inicio: después del último archivo exportado de la tabla.
		var prevLast sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT max(last_seq) FROM nexo_audit_exports WHERE table_name = $1 AND day < $2::date`, table, date).Scan(&prevLast); err != nil {
			return res, fmt.Errorf("evidence: exportar: %w", err)
		}
		sig.PrevLastSeq = prevLast.Int64
		// En la primera exportación de la tabla, las filas anteriores a la
		// cadena de cualquier fecha; después, solo las del día.
		legacyFrom := day
		if !prevLast.Valid {
			legacyFrom = time.Unix(0, 0).UTC()
		}
		// Fin: la última posición con hora anterior al fin del día.
		var lastSeq sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT max(nexo_chain_seq) FROM `+table+` WHERE nexo_chain_seq > $1 AND occurred_at < $2`, sig.PrevLastSeq, dayEnd).Scan(&lastSeq); err != nil {
			return res, fmt.Errorf("evidence: exportar: %w", err)
		}
		// Filas anteriores a la cadena (migración) del día.
		if err := writeLines(ctx, db, &buf, `
			SELECT jsonb_build_object('table', $1::text, 'seq', NULL, 'id', x.id, 'occurred_at', nexo_chain_ts(x.occurred_at),
				'row', to_jsonb(x) - 'nexo_prev_hash' - 'nexo_hash' - 'nexo_chain_seq')::text
			FROM `+table+` x WHERE x.nexo_chain_seq IS NULL AND x.occurred_at >= $2 AND x.occurred_at < $3 ORDER BY x.id`,
			&sig.Rows, table, legacyFrom, dayEnd); err != nil {
			return res, err
		}
		if lastSeq.Valid {
			rows, err := db.QueryContext(ctx, `
				SELECT x.nexo_chain_seq, encode(x.nexo_prev_hash, 'hex'), encode(x.nexo_hash, 'hex'),
					jsonb_build_object('table', $1::text, 'seq', x.nexo_chain_seq, 'id', x.id, 'occurred_at', nexo_chain_ts(x.occurred_at),
						'prev_hash', encode(x.nexo_prev_hash, 'hex'), 'hash', encode(x.nexo_hash, 'hex'),
						'digest', encode(sha256(convert_to(`+canonFunc(table)+`(x), 'UTF8')), 'hex'),
						'row', to_jsonb(x) - 'nexo_prev_hash' - 'nexo_hash' - 'nexo_chain_seq')::text
				FROM `+table+` x WHERE x.nexo_chain_seq > $2 AND x.nexo_chain_seq <= $3 ORDER BY x.nexo_chain_seq`,
				table, sig.PrevLastSeq, lastSeq.Int64)
			if err != nil {
				return res, fmt.Errorf("evidence: exportar: %w", err)
			}
			for rows.Next() {
				var seq int64
				var prev, h, line string
				if err := rows.Scan(&seq, &prev, &h, &line); err != nil {
					_ = rows.Close()
					return res, fmt.Errorf("evidence: exportar: %w", err)
				}
				if sig.ChainedRows == 0 {
					sig.FirstSeq, sig.FirstPrevHash = seq, prev
				}
				sig.LastSeq, sig.LastHash = seq, h
				sig.ChainedRows++
				sig.Rows++
				line, err = nexoExportMetadataLine(line)
				if err != nil {
					_ = rows.Close()
					return res, err
				}
				buf.WriteString(line)
				buf.WriteByte('\n')
			}
			if err := rows.Close(); err != nil {
				return res, err
			}
		}
		if sig.ChainedRows == 0 {
			sig.LastSeq = sig.PrevLastSeq
		}
	} else {
		if err := writeLines(ctx, db, &buf, `
			SELECT jsonb_build_object('table', $1::text, 'id', x.id, 'occurred_at', nexo_chain_ts(x.occurred_at), 'row', to_jsonb(x))::text
			FROM `+table+` x WHERE x.occurred_at >= $2 AND x.occurred_at < $3 ORDER BY x.id`,
			&sig.Rows, table, day, dayEnd); err != nil {
			return res, err
		}
	}

	fileSum := sha256.Sum256(buf.Bytes())
	sig.FileSHA256 = hex.EncodeToString(fileSum[:])
	sig.HMAC = sign(cfg.Key, sig)

	dir := filepath.Join(cfg.Dir, table)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return res, fmt.Errorf("evidence: exportar: %w", err)
	}
	path := filepath.Join(dir, sig.File)
	if err := writeFileAtomic(path, buf.Bytes()); err != nil {
		return res, err
	}
	sigRaw, _ := json.MarshalIndent(sig, "", "  ")
	if err := writeFileAtomic(path+".sig", append(sigRaw, '\n')); err != nil {
		return res, err
	}
	var firstSeq, lastSeqV, lastHash any
	if chained {
		firstSeq, lastSeqV, lastHash = nullInt(sig.FirstSeq, sig.ChainedRows > 0), sig.LastSeq, nullStr(sig.LastHash)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO nexo_audit_exports (table_name, day, file_name, rows, first_seq, last_seq, last_hash, file_sha256, hmac_sha256, key_id, created_by)
		VALUES ($1, $2::date, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (table_name, day) DO NOTHING`,
		table, date, filepath.ToSlash(filepath.Join(table, sig.File)), sig.Rows, firstSeq, lastSeqV, lastHash, sig.FileSHA256, sig.HMAC, sig.KeyID, createdBy); err != nil {
		return res, fmt.Errorf("evidence: exportar: registro: %w", err)
	}
	res.File, res.Rows, res.FirstSeq, res.LastSeq = path, sig.Rows, sig.FirstSeq, sig.LastSeq
	return res, nil
}

func nullInt(v int64, ok bool) any {
	if !ok {
		return nil
	}
	return v
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func validExportTable(t string) bool {
	for _, x := range ExportTables {
		if x == t {
			return true
		}
	}
	return false
}

func writeLines(ctx context.Context, db *sql.DB, buf *bytes.Buffer, q string, count *int64, args ...any) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("evidence: exportar: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("evidence: exportar: %w", err)
		}
		line, err = nexoExportMetadataLine(line)
		if err != nil {
			return err
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
		*count++
	}
	return rows.Err()
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return fmt.Errorf("evidence: exportar: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("evidence: exportar: %w", err)
	}
	return nil
}

// ExportRange exporta los días [from, to] (UTC) de todas las tablas,
// saltando los ya exportados. Sin includeToday, to nunca es posterior a ayer:
// el día en curso no está cerrado. Con includeToday (solo a pedido, por la
// CLI) el día en curso se exporta parcial: las filas posteriores de ese día
// irán al archivo del día siguiente, que sigue la cadena desde ahí.
func ExportRange(ctx context.Context, db *sql.DB, cfg ExportConfig, from, to, now time.Time, createdBy string, includeToday bool) ([]ExportResult, error) {
	last := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	if !includeToday {
		last = last.AddDate(0, 0, -1)
	}
	if to.IsZero() || to.After(last) {
		to = last
	}
	from = time.Date(from.UTC().Year(), from.UTC().Month(), from.UTC().Day(), 0, 0, 0, 0, time.UTC)
	var out []ExportResult
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		for _, t := range ExportTables {
			r, err := ExportDay(ctx, db, cfg, t, d, createdBy)
			if err != nil {
				return out, err
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// ─── Verificación de un archivo exportado ────────────────────────────────────

// ExportCheck es el resultado de verificar un archivo exportado.
type ExportCheck struct {
	File        string   `json:"file"`
	Table       string   `json:"table"`
	Date        string   `json:"date"`
	OK          bool     `json:"ok"`
	Rows        int64    `json:"rows"`
	SignatureOK bool     `json:"signature_ok"`
	FileHashOK  bool     `json:"file_hash_ok"`
	ChainOK     bool     `json:"chain_ok"`
	PreviousOK  *bool    `json:"previous_day_ok,omitempty"` // continuidad con el archivo del día anterior (si existe)
	DatabaseOK  *bool    `json:"database_ok,omitempty"`     // coincidencia con la base (--db)
	Problems    []string `json:"problems,omitempty"`
}

type exportLine struct {
	Table    string `json:"table"`
	Seq      *int64 `json:"seq"`
	ID       int64  `json:"id"`
	PrevHash string `json:"prev_hash"`
	Hash     string `json:"hash"`
	Digest   string `json:"digest"`
}

// VerifyExport verifica la firma (HMAC), el sha256 del archivo, el
// encadenamiento interno de las filas, la continuidad con el archivo del día
// anterior en la misma carpeta y, con db != nil, que cada fila encadenada
// coincida con la base (detecta alteraciones posteriores a la exportación).
func VerifyExport(ctx context.Context, path string, key []byte, db *sql.DB) (ExportCheck, error) {
	chk := ExportCheck{File: path}
	problem := func(f string, a ...any) { chk.Problems = append(chk.Problems, fmt.Sprintf(f, a...)) }
	sigRaw, err := os.ReadFile(path + ".sig")
	if err != nil {
		return chk, fmt.Errorf("evidence: no se encontró la firma %s.sig: %w", path, err)
	}
	var sig Signature
	if err := json.Unmarshal(sigRaw, &sig); err != nil {
		return chk, fmt.Errorf("evidence: firma ilegible: %w", err)
	}
	chk.Table, chk.Date = sig.Table, sig.Date
	if sig.Format != ExportFormat && sig.Format != "nexo-audit-export/v1" {
		problem("formato de firma desconocido %q", sig.Format)
	}
	if len(key) == 0 {
		problem("sin clave (%s): no se puede verificar la firma", ExportKeyEnv)
	} else if hmac.Equal([]byte(sign(key, sig)), []byte(sig.HMAC)) {
		chk.SignatureOK = true
	} else {
		problem("la firma HMAC no coincide (archivo .sig alterado o clave distinta, key_id %s)", sig.KeyID)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return chk, fmt.Errorf("evidence: leer %s: %w", path, err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) == sig.FileSHA256 {
		chk.FileHashOK = true
	} else {
		problem("el sha256 del archivo no coincide con la firma: el archivo fue modificado")
	}

	chk.ChainOK = true
	var lines []exportLine
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	var prevHash string
	var prevSeq int64
	first := true
	for n := 1; sc.Scan(); n++ {
		var l exportLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			problem("línea %d ilegible", n)
			chk.ChainOK = false
			continue
		}
		chk.Rows++
		if l.Seq == nil {
			continue
		}
		lines = append(lines, l)
		prev, _ := hex.DecodeString(l.PrevHash)
		dig, _ := hex.DecodeString(l.Digest)
		if hex.EncodeToString(LinkHash(prev, dig)) != l.Hash {
			problem("línea %d (posición %d, id %d): el hash no corresponde a su contenido", n, *l.Seq, l.ID)
			chk.ChainOK = false
		}
		if first {
			first = false
			if l.PrevHash != sig.FirstPrevHash || *l.Seq != sig.FirstSeq {
				problem("la primera fila encadenada no coincide con la firma")
				chk.ChainOK = false
			}
		} else if *l.Seq != prevSeq+1 || l.PrevHash != prevHash {
			problem("línea %d (posición %d): no enlaza con la anterior (posición %d)", n, *l.Seq, prevSeq)
			chk.ChainOK = false
		}
		prevHash, prevSeq = l.Hash, *l.Seq
	}
	if err := sc.Err(); err != nil {
		return chk, err
	}
	if chk.Rows != sig.Rows || int64(len(lines)) != sig.ChainedRows {
		problem("cantidad de filas distinta de la firma (%d/%d)", chk.Rows, sig.Rows)
		chk.ChainOK = false
	}
	if len(lines) > 0 && (prevHash != sig.LastHash || prevSeq != sig.LastSeq) {
		problem("la última fila no coincide con la firma")
		chk.ChainOK = false
	}

	// Continuidad con el día anterior (mismo directorio).
	if _, chained := ChainSpecFor(sig.Table); chained {
		if d, err := time.Parse(time.DateOnly, sig.Date); err == nil {
			prevPath := filepath.Join(filepath.Dir(path), d.AddDate(0, 0, -1).Format(time.DateOnly)+".jsonl.sig")
			if raw, err := os.ReadFile(prevPath); err == nil {
				var ps Signature
				ok := json.Unmarshal(raw, &ps) == nil && ps.LastSeq == sig.PrevLastSeq
				if ok && len(lines) > 0 && ps.ChainedRows > 0 && ps.LastHash != sig.FirstPrevHash {
					ok = false
				}
				if !ok {
					problem("no enlaza con el archivo del día anterior (%s)", filepath.Base(prevPath))
				}
				chk.PreviousOK = &ok
			}
		}
	}

	if db != nil && len(lines) > 0 {
		ok, err := compareWithDB(ctx, db, sig.Table, lines, problem)
		if err != nil {
			return chk, err
		}
		chk.DatabaseOK = &ok
	}
	chk.OK = chk.SignatureOK && chk.FileHashOK && chk.ChainOK && (chk.PreviousOK == nil || *chk.PreviousOK) &&
		(chk.DatabaseOK == nil || *chk.DatabaseOK) && len(chk.Problems) == 0
	return chk, nil
}

func compareWithDB(ctx context.Context, db *sql.DB, table string, lines []exportLine, problem func(string, ...any)) (bool, error) {
	if _, ok := ChainSpecFor(table); !ok {
		return true, nil
	}
	first, last := *lines[0].Seq, *lines[len(lines)-1].Seq
	rows, err := db.QueryContext(ctx, `
		SELECT x.nexo_chain_seq, encode(x.nexo_hash, 'hex'), encode(sha256(convert_to(`+canonFunc(table)+`(x), 'UTF8')), 'hex')
		FROM `+table+` x WHERE x.nexo_chain_seq BETWEEN $1 AND $2`, first, last)
	if err != nil {
		return false, fmt.Errorf("evidence: comparar con la base: %w", err)
	}
	defer rows.Close()
	type dbRow struct{ hash, digest string }
	inDB := map[int64]dbRow{}
	for rows.Next() {
		var seq int64
		var r dbRow
		if err := rows.Scan(&seq, &r.hash, &r.digest); err != nil {
			return false, err
		}
		inDB[seq] = r
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	var purgedBefore int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(max(seq), 0) FROM `+AnchorsTable+` WHERE table_name = $1 AND kind = 'purge'`, table).Scan(&purgedBefore); err != nil {
		return false, err
	}
	ok := true
	reported := 0
	for _, l := range lines {
		r, found := inDB[*l.Seq]
		switch {
		case !found && *l.Seq < purgedBefore:
			// depurada por retención (ancla registrada): no es una alteración
		case !found:
			ok = false
			if reported < 20 {
				problem("posición %d (id %d): está en el archivo y falta en la base", *l.Seq, l.ID)
				reported++
			}
		case r.hash != l.Hash || r.digest != l.Digest:
			ok = false
			if reported < 20 {
				problem("posición %d (id %d): la fila de la base difiere del archivo exportado", *l.Seq, l.ID)
				reported++
			}
		}
	}
	return ok, nil
}

// ErrExportDisabled indica que la exportación no está configurada.
var ErrExportDisabled = errors.New("export disabled")
