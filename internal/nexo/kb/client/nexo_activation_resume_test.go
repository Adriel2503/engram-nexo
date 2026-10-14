package client

// NX-07 · la CLI no pierde el token tras consumir el código (revisión
// adversarial, punto 8). Sin Postgres: servidor HTTP de prueba.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

type activationServer struct {
	*httptest.Server
	activations atomic.Int32
	meStatus    atomic.Int32 // 0 = 200
	token       string
	beforeToken func() // se ejecuta dentro del canje, antes de responder
}

func newActivationServer(t *testing.T) *activationServer {
	t.Helper()
	s := &activationServer{token: "egc_live_0123abcd_" + strings.Repeat("Zy9-", 11)}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/nexo/authz/v1/activate":
			if s.activations.Add(1) > 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error_code":"activation_used","error":"usado"}`))
				return
			}
			if s.beforeToken != nil {
				s.beforeToken()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"raw_token": s.token})
		case "/nexo/authz/v1/me":
			if code := int(s.meStatus.Load()); code != 0 {
				w.WriteHeader(code)
				_, _ = w.Write([]byte(`{"error_code":"x","error":"x"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"principal_id": "7", "label": "persona", "token_scope": "read", "spaces": []any{}})
		default:
			_, _ = w.Write([]byte(`{"spaces":[],"documents":[],"next_cursor":null}`))
		}
	}))
	t.Cleanup(s.Close)
	t.Setenv(EnvLocalDB, filepath.Join(t.TempDir(), "kb.db"))
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	return s
}

func dirHasSecret(t *testing.T, dir, secret string) []string {
	t.Helper()
	var hits []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if raw, rerr := os.ReadFile(p); rerr == nil && strings.Contains(string(raw), secret) {
				hits = append(hits, filepath.Base(p))
			}
		}
		return nil
	})
	return hits
}

// /me responde 503 después del canje: el token queda guardado y --resume lo conecta sin volver a canjear.
func TestConnectActivatedKeepsTokenWhenMeFails(t *testing.T) {
	s := newActivationServer(t)
	dataDir := t.TempDir()
	s.meStatus.Store(http.StatusServiceUnavailable)

	_, err := ConnectActivated(t.Context(), dataDir, s.URL, "nxa_codigo-de-prueba-0123456789")
	if err == nil {
		t.Fatal("debía fallar con /me 503")
	}
	if strings.Contains(err.Error(), s.token) || strings.Contains(err.Error(), "nxa_codigo") {
		t.Fatal("el error repite un secreto")
	}
	if !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("el error debe indicar cómo continuar: %v", err)
	}
	if !hasPendingToken(dataDir, s.URL) {
		t.Fatal("el token ya canjeado debe quedar guardado")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Join(dataDir, PendingTokenFile)); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("permisos del token guardado: %v %v", info, err)
		}
	}
	if cfg, _ := os.ReadFile(filepath.Join(dataDir, "cloud.json")); strings.Contains(string(cfg), s.token) {
		t.Fatal("cloud.json no debe guardarse hasta validar el token")
	}
	if hits := dirHasSecret(t, dataDir, "nxa_codigo"); len(hits) != 0 {
		t.Fatalf("el código de activación nunca se guarda: %v", hits)
	}

	// El servidor se recupera: --resume conecta con el token guardado, sin pedir código.
	s.meStatus.Store(0)
	res, err := ResumeActivation(t.Context(), dataDir, s.URL)
	if err != nil || res.Label != "persona" {
		t.Fatalf("reanudar: %+v %v", res, err)
	}
	if n := s.activations.Load(); n != 1 {
		t.Fatalf("el código se canjeó %d veces; debe ser una", n)
	}
	if hasPendingToken(dataDir, s.URL) {
		t.Fatal("el token pendiente debe borrarse al conectar")
	}
	if cfg, _ := os.ReadFile(filepath.Join(dataDir, "cloud.json")); !strings.Contains(string(cfg), s.token) {
		t.Fatal("cloud.json debe quedar con el token")
	}
}

// Repetir --activate con el mismo código tras un fallo reanuda y no canjea otra vez (el código ya está usado).
func TestConnectActivatedRetryReusesPendingToken(t *testing.T) {
	s := newActivationServer(t)
	dataDir := t.TempDir()
	s.meStatus.Store(http.StatusServiceUnavailable)
	if _, err := ConnectActivated(t.Context(), dataDir, s.URL, "nxa_codigo-de-prueba-0123456789"); err == nil {
		t.Fatal("debía fallar")
	}
	s.meStatus.Store(0)
	if _, err := ConnectActivated(t.Context(), dataDir, s.URL, "nxa_codigo-de-prueba-0123456789"); err != nil {
		t.Fatalf("el reintento con el mismo código debía reanudar: %v", err)
	}
	if n := s.activations.Load(); n != 1 {
		t.Fatalf("canjes: %d", n)
	}
}

// El token vence antes de poder conectarlo (401): ya no sirve, no se conserva y no se pide --resume.
func TestConnectActivatedDropsRejectedToken(t *testing.T) {
	s := newActivationServer(t)
	dataDir := t.TempDir()
	s.meStatus.Store(http.StatusUnauthorized)
	_, err := ConnectActivated(t.Context(), dataDir, s.URL, "nxa_codigo-de-prueba-0123456789")
	if err == nil || strings.Contains(err.Error(), "--resume") {
		t.Fatalf("un 401 es definitivo: %v", err)
	}
	if hasPendingToken(dataDir, s.URL) {
		t.Fatal("un token rechazado no debe conservarse")
	}
}

// Antes de canjear se comprueba que se puede escribir: sin escritura, el código no se consume.
func TestConnectActivatedChecksWritableBeforeRedeeming(t *testing.T) {
	s := newActivationServer(t)
	base := t.TempDir()
	blocked := filepath.Join(base, "datos")
	if err := os.WriteFile(blocked, []byte("no soy una carpeta"), 0o600); err != nil { // un archivo ocupa el lugar de la carpeta
		t.Fatal(err)
	}
	_, err := ConnectActivated(t.Context(), blocked, s.URL, "nxa_codigo-de-prueba-0123456789")
	if err == nil {
		t.Fatal("debía fallar")
	}
	if n := s.activations.Load(); n != 0 {
		t.Fatalf("el código se canjeó (%d) aunque no se podía escribir", n)
	}
	if strings.Contains(err.Error(), "nxa_codigo") {
		t.Fatal("el error repite el código")
	}
}

// cloud.json existente y de solo lectura: tampoco se canjea.
func TestConnectActivatedChecksCloudJSONWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignora los permisos")
	}
	s := newActivationServer(t)
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "cloud.json")
	if err := os.WriteFile(path, []byte(`{"server_url":"","token":""}`), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := ConnectActivated(t.Context(), dataDir, s.URL, "nxa_codigo-de-prueba-0123456789"); err == nil {
		t.Fatal("debía fallar")
	}
	if n := s.activations.Load(); n != 0 {
		t.Fatalf("el código se canjeó (%d) con cloud.json de solo lectura", n)
	}
}

// El disco falla justo después del canje (la carpeta desaparece): el error dice qué hacer y no revela el token.
func TestConnectActivatedPersistenceFailureAfterRedeem(t *testing.T) {
	s := newActivationServer(t)
	base := t.TempDir()
	dataDir := filepath.Join(base, "datos")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	s.beforeToken = func() {
		_ = os.RemoveAll(dataDir)
		_ = os.WriteFile(dataDir, []byte("ocupado"), 0o600)
	}
	_, err := ConnectActivated(t.Context(), dataDir, s.URL, "nxa_codigo-de-prueba-0123456789")
	if err == nil {
		t.Fatal("debía fallar")
	}
	msg := err.Error()
	if strings.Contains(msg, s.token) || strings.Contains(msg, "nxa_codigo") {
		t.Fatal("el error repite un secreto")
	}
	if !strings.Contains(msg, "revoque") {
		t.Fatalf("el error debe indicar el siguiente paso: %v", err)
	}
}

// Desconectar también borra un token pendiente.
func TestDisconnectClearsPendingToken(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(EnvLocalDB, filepath.Join(t.TempDir(), "kb.db"))
	if err := savePendingToken(dataDir, pendingToken{Server: "https://nexo.example.test", Token: "egc_x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Disconnect(dataDir, true); err != nil {
		t.Fatal(err)
	}
	if hasPendingToken(dataDir, "https://nexo.example.test") {
		t.Fatal("el token pendiente debe borrarse al desconectar")
	}
}

// hasPendingToken informa si hay un token canjeado sin conectar para el servidor.
func hasPendingToken(dataDir, server string) bool {
	p, err := loadPendingToken(dataDir)
	return err == nil && p != nil && sameServer(p.Server, server)
}
