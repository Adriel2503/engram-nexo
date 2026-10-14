package cloudserver

// Nexo IA · SG-05 y pool de conexiones — GET /nexo/admin/database y prueba
// de concurrencia con un pool chico (docs/nexo/manuales/servidor-produccion.md,
// secciones 2.1 y 4.1.1).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

func nexoDBStatusEnv(t *testing.T, poolEnv map[string]string) *nexoSecEnv {
	t.Helper()
	for _, k := range []string{security.DBRequireTLSEnv, security.DBMaxOpenEnv, security.DBMaxIdleEnv, security.DBMaxLifetimeEnv, security.DBMaxIdleTimeEnv} {
		t.Setenv(k, "")
	}
	for k, v := range poolEnv {
		t.Setenv(k, v)
	}
	env := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "carla", "", "10.97.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("me de carla: %d %s", rec.Code, rec.Body.String())
	}
	return env
}

func TestNexoDBStatusRoute(t *testing.T) {
	env := nexoDBStatusEnv(t, nil)

	// Solo el superadmin (platform.settings): admin y miembros, 403; sin token, 401.
	for who, want := range map[string]int{"carla": 200, "pedro": 403, "ana": 403, "ci": 403} {
		if rec := env.req(http.MethodGet, "/nexo/admin/database", who, "", "10.97.0.1"); rec.Code != want {
			t.Fatalf("estado como %s: %d %s", who, rec.Code, rec.Body.String())
		}
	}
	if rec := env.req(http.MethodGet, "/nexo/admin/database", "", "", "10.97.0.1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin token: %d", rec.Code)
	}

	rec := env.req(http.MethodGet, "/nexo/admin/database", "carla", "", "10.97.0.1")
	var st cloudstore.NexoDBStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Pool.MaxOpen != 25 || st.Pool.MaxIdle != 10 || st.Pool.Open < 1 || st.Pool.InUse < 0 {
		t.Errorf("pool: %+v", st.Pool)
	}
	if st.Server.PostgresVersion == "" || st.Server.MaxConnections < 1 {
		t.Errorf("servidor: %+v", st.Server)
	}
	if st.TLS.SSLMode == "" || st.TLS.RequireTLS {
		t.Errorf("tls: %+v", st.TLS)
	}
	// Sin secretos: ni tokens ni hashes ni la cadena de conexión.
	scan := newNexoLeakScanner()
	for name, raw := range env.tokens {
		scan.addToken(name, raw, env.hasher.Hash)
	}
	if got := scan.find(rec.Body.String()); len(got) > 0 {
		t.Errorf("la respuesta filtra secretos: %v", got)
	}
	for _, bad := range []string{"://", "password", "@"} {
		if strings.Contains(rec.Body.String(), bad) {
			t.Errorf("la respuesta no debe traer %q: %s", bad, rec.Body.String())
		}
	}
	// El acceso queda en la lista de denegaciones del motor (admin 403), no en 500.
	if n := env.count(`SELECT count(*) FROM nexo_authz_events WHERE action = 'authz.denied' AND details->>'capability' = 'platform.settings'`); n < 1 {
		t.Errorf("la denegación de pedro queda en los eventos: %d", n)
	}
}

// Con un máximo bajo, muchas peticiones concurrentes nunca superan el límite
// de conexiones abiertas y ninguna falla: esperan su turno.
func TestNexoPoolLimitUnderConcurrency(t *testing.T) {
	const limit = 4 // security.MinDBMaxOpen
	env := nexoDBStatusEnv(t, map[string]string{security.DBMaxOpenEnv: fmt.Sprint(limit), security.DBMaxIdleEnv: "2"})

	var maxOpen, maxInUse int64
	done := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		for {
			s := env.db.Stats()
			if v := int64(s.OpenConnections); v > atomic.LoadInt64(&maxOpen) {
				atomic.StoreInt64(&maxOpen, v)
			}
			if v := int64(s.InUse); v > atomic.LoadInt64(&maxInUse) {
				atomic.StoreInt64(&maxInUse, v)
			}
			select {
			case <-done:
				return
			case <-time.After(200 * time.Microsecond):
			}
		}
	}()

	// 1) Carga directa sobre el pool: consultas lentas para que se encimen.
	const direct = 60
	var wg sync.WaitGroup
	errs := make(chan error, direct)
	for i := 0; i < direct; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var one int
			if err := env.db.QueryRowContext(ctx, `SELECT 1 FROM pg_sleep(0.02)`).Scan(&one); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()

	// 2) Carga HTTP: peticiones autenticadas que consultan la base.
	const requests = 150
	codes := make(chan int, requests)
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			who := []string{"carla", "pedro", "ana", "luis"}[i%4]
			rec := env.req(http.MethodGet, "/nexo/authz/v1/me", who, "", fmt.Sprintf("10.98.%d.%d", i/200, i%200+1))
			codes <- rec.Code
		}(i)
	}
	wg.Wait()
	close(done)
	sampler.Wait()
	close(errs)
	close(codes)

	for err := range errs {
		t.Errorf("consulta directa fallida: %v", err)
	}
	ok := 0
	for c := range codes {
		if c == http.StatusOK {
			ok++
		} else {
			t.Errorf("petición HTTP respondió %d", c)
		}
	}
	if ok != requests {
		t.Errorf("respuestas 200: %d de %d", ok, requests)
	}
	if got := atomic.LoadInt64(&maxOpen); got > limit {
		t.Errorf("conexiones abiertas máximas = %d, el límite es %d", got, limit)
	}
	if got := atomic.LoadInt64(&maxInUse); got > limit {
		t.Errorf("conexiones en uso máximas = %d, el límite es %d", got, limit)
	}
	stats := env.db.Stats()
	if stats.MaxOpenConnections != limit {
		t.Errorf("MaxOpenConnections = %d, want %d", stats.MaxOpenConnections, limit)
	}
	if stats.WaitCount == 0 {
		t.Errorf("con %d conexiones y %d consultas encimadas debía haber esperas", limit, direct)
	}

	// El estado lo muestra.
	rec := env.req(http.MethodGet, "/nexo/admin/database", "carla", "", "10.97.0.1")
	var st cloudstore.NexoDBStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("estado: %d %v", rec.Code, err)
	}
	if st.Pool.MaxOpen != limit || st.Pool.MaxIdle != 2 || st.Pool.WaitCount == 0 || st.Pool.Open > limit {
		t.Errorf("estado del pool: %+v", st.Pool)
	}
	t.Logf("máximo abiertas=%d, en uso=%d, esperas=%d, espera total=%s", maxOpen, maxInUse, stats.WaitCount, stats.WaitDuration)
}

// El monitor deja una línea en el log cuando hubo esperas nuevas.
func TestNexoMonitorPoolWaitsLog(t *testing.T) {
	env := nexoDBStatusEnv(t, map[string]string{security.DBMaxOpenEnv: "4", security.DBMaxIdleEnv: "2"})
	logs := nexoCaptureLogs(t)
	m := env.srv.nexoMonitorState()
	env.srv.nexoMonitorPool(m) // línea base
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var one int
			_ = env.db.QueryRow(`SELECT 1 FROM pg_sleep(0.02)`).Scan(&one)
		}()
	}
	wg.Wait()
	env.srv.nexoMonitorPool(m)
	if !strings.Contains(logs.String(), "esperaron una conexion libre del pool") {
		t.Fatalf("falta el aviso de esperas:\n%s", logs.String())
	}
	before := strings.Count(logs.String(), "esperaron una conexion libre")
	env.srv.nexoMonitorPool(m) // sin esperas nuevas: sin línea nueva
	if after := strings.Count(logs.String(), "esperaron una conexion libre"); after != before {
		t.Fatalf("sin esperas nuevas no se avisa de nuevo")
	}
}
