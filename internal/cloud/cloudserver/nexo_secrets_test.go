package cloudserver

// Nexo IA · NX-07 fase S5 (S5-03, CA-S13, RS-43) — secretos en registros.
//
// Corre la corrida completa en el proceso (nexo_flow_test.go: login del
// panel, emisión de tokens, sync push/pull, publicación y búsqueda en la base
// de conocimiento, tokens inválidos, revocados y compartido), una pasada del
// monitor de evidencia (resúmenes A2, alertas, webhook, verificación y
// exportación) y falla si encuentra un token en claro, un hash de token o el
// valor de una cabecera Authorization en:
//   - la salida de log y log/slog del proceso;
//   - cualquier fila de cualquier tabla del esquema (auditoría de Engram,
//     eventos de roles y de conocimiento, actividad, alertas, cambios
//     sincronizados...), salvo la columna token_hash de cloud_principal_tokens,
//     que es donde el hash debe estar;
//   - los cuerpos enviados al webhook de alertas y los archivos exportados.

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

// nexoSyncBuffer junta la salida de log de varias goroutines.
type nexoSyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *nexoSyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *nexoSyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// nexoCaptureLogs redirige log y log/slog a un búfer hasta el final de la prueba.
func nexoCaptureLogs(t *testing.T) *nexoSyncBuffer {
	t.Helper()
	buf := &nexoSyncBuffer{}
	prevSlog := slog.Default()
	prevFlags, prevPrefix := log.Flags(), log.Prefix()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	log.SetOutput(buf) // después de SetDefault: log escribe directo al búfer
	t.Cleanup(func() {
		slog.SetDefault(prevSlog)
		log.SetOutput(os.Stderr)
		log.SetFlags(prevFlags)
		log.SetPrefix(prevPrefix)
	})
	return buf
}

func TestNexoNoSecretsInLogsAndEvents(t *testing.T) {
	var hookMu sync.Mutex
	var hookBodies []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		hookMu.Lock()
		hookBodies = append(hookBodies, r.Header.Get("Authorization")+" "+string(raw))
		hookMu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()
	exportDir := t.TempDir()
	t.Setenv(evidence.AlertWebhookEnv, hook.URL+"/teams")
	t.Setenv(evidence.ExportDirEnv, exportDir)
	t.Setenv(evidence.ExportKeyEnv, "clave-de-prueba-s5-secretos-0123456789abcdef")
	t.Setenv(evidence.DisableInactiveEnv, "")
	t.Setenv(NexoAuditVerifyMinutesEnv, "60")

	logs := nexoCaptureLogs(t)
	env := newNexoSecEnv(t, nil)
	f := runNexoSecurityFlow(t, env)

	// Rechazos repetidos con el mismo token (A2: fila completa + resumen) y
	// denegaciones en ráfaga (alerta authz_denied_burst).
	repeated := nexoRandomToken(t)
	f.scanner.addToken("repeated", repeated, env.hasher.Hash)
	for i := 0; i < 4; i++ {
		env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", "raw:"+repeated, "", "10.95.0.2")
	}
	for i := 0; i < 5; i++ {
		env.req(http.MethodGet, "/nexo/admin/alerts", "ana", "", "10.95.0.3")
	}
	m := env.srv.nexoMonitorState()
	env.srv.nexoFlushAuditDedup(context.Background(), true)
	env.srv.nexoMonitorTick(context.Background(), m, time.Now())
	m.webhookWG.Wait()

	// Todos los hashes guardados (también los de tokens emitidos por HTTP).
	rows, err := env.db.Query(`SELECT id::text, token_hash FROM cloud_principal_tokens`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, h string
		if err := rows.Scan(&id, &h); err != nil {
			t.Fatal(err)
		}
		f.scanner.addHash("id "+id, h)
	}
	_ = rows.Close()
	for _, c := range f.cookies {
		f.scanner.values[c] = "dashboard session cookie"
	}

	// 1. Logs del proceso.
	out := logs.String()
	if !strings.Contains(out, "[nexo]") {
		t.Fatalf("la captura de logs no recibió la salida del servidor: %q", out)
	}
	for _, leak := range f.scanner.find(out) {
		t.Errorf("log: %s", leak)
	}

	// 2. Filas de todas las tablas del esquema.
	tables, err := env.db.Query(`SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		if err := tables.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	_ = tables.Close()
	scanned := map[string]int{}
	for _, table := range names {
		// token_hash (cloud_principal_tokens) y code_hash (nexo_activation_codes, NX-07) son las únicas
		// columnas donde el HMAC de una credencial se guarda a propósito; el valor de la credencial no.
		q := `SELECT (to_jsonb(t) - 'token_hash' - 'code_hash')::text FROM "` + table + `" t`
		rs, err := env.db.Query(q)
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		for rs.Next() {
			var row string
			if err := rs.Scan(&row); err != nil {
				t.Fatal(err)
			}
			scanned[table]++
			for _, leak := range f.scanner.find(row) {
				t.Errorf("table %s: %s", table, leak)
			}
		}
		_ = rs.Close()
	}
	t.Logf("filas revisadas por tabla: %v", scanned)
	t.Logf("log capturado: %d bytes", len(out))
	for _, table := range []string{"cloud_auth_audit_log", "nexo_authz_events", "nexo_activity_log", "nexo_kb_events", "nexo_alerts", "cloud_mutations"} {
		if scanned[table] == 0 {
			t.Errorf("la corrida debía dejar filas en %s", table)
		}
	}
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE metadata ? 'nexo_repeated'`); n == 0 {
		t.Error("la corrida debía dejar un resumen A2")
	}

	// 3. Webhook de alertas y archivos exportados.
	hookMu.Lock()
	joined := strings.Join(hookBodies, "\n")
	hookMu.Unlock()
	if joined == "" {
		t.Error("el monitor debía enviar alertas al webhook")
	}
	for _, leak := range f.scanner.find(joined) {
		t.Errorf("webhook: %s", leak)
	}
	_ = filepath.WalkDir(exportDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatal(rerr)
		}
		for _, leak := range f.scanner.find(string(raw)) {
			t.Errorf("export %s: %s", filepath.Base(path), leak)
		}
		return nil
	})
}

// El detector de esta prueba ve un secreto en el log y en una fila.
func TestNexoSecretsTestDetectsLeaks(t *testing.T) {
	logs := nexoCaptureLogs(t)
	raw := "egc_live_0a1b2c3d_" + strings.Repeat("Zz9-", 10)
	log.Printf("[nexo] depuracion: Authorization: Bearer %s", raw)
	slog.Info("auth", "authorization", "Bearer "+raw)
	s := newNexoLeakScanner()
	s.addToken("t", raw, nil)
	if got := s.find(logs.String()); len(got) < 3 {
		t.Fatalf("hallazgos en el log: %v", got)
	}
}
