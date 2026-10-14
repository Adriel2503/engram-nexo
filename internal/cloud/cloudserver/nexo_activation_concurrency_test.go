package cloudserver

// NX-07 · concurrencia del canje (punto 10 de las correcciones): ganador único,
// revocación y deshabilitación simultáneas con el canje, y emisión y canje con
// el pool de conexiones en 4 y 32 operaciones a la vez (sin bloqueo: un
// DeadlineExceeded cuenta como falla; OP-13 del backlog). Postgres real.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// nexoDoCtx hace una petición con plazo; devuelve el estado y el cuerpo, o el error de contexto.
func nexoDoCtx(ctx context.Context, e *nexoSecEnv, method, path, who, body, ip string) (int, string, error) {
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	req.RemoteAddr = ip + ":40000"
	req.Header.Set("User-Agent", "nexo-concurrency/1.0")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok := e.tokens[who]; tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { e.h.ServeHTTP(rec, req); close(done) }()
	select {
	case <-done:
		return rec.Code, rec.Body.String(), nil
	case <-ctx.Done():
		return 0, "", ctx.Err()
	}
}

func nexoIssueCodes(t *testing.T, e *nexoSecEnv, n int, person string) (codes, ids []string) {
	t.Helper()
	for i := 0; i < n; i++ {
		r := e.req("POST", "/nexo/authz/v1/tokens", "carla", `{"principal":"`+person+`","name":"concurrente","scope":"read","expires_in_days":5}`, "10.90.0.1")
		if r.Code != http.StatusCreated {
			t.Fatalf("emisión %d: %d", i, r.Code)
		}
		v := nexoActivationJSON(t, r)
		codes = append(codes, v["activation_code"].(string))
		ids = append(ids, v["activation"].(map[string]any)["id"].(string))
	}
	return codes, ids
}

// 32 canjes simultáneos del mismo código: solo uno gana, el resto recibe "ya canjeado".
func TestNexoActivationRedeem32ConcurrentSingleWinnerPG(t *testing.T) {
	e := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	codes, ids := nexoIssueCodes(t, e, 1, "ana")
	body, _ := json.Marshal(map[string]string{"code": codes[0]})
	const n = 32
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	start := make(chan struct{})
	status := make([]int, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			status[i], _, errs[i] = nexoDoCtx(ctx, e, "POST", "/nexo/authz/v1/activate", "", string(body), fmt.Sprintf("10.90.1.%d", i+1))
		}(i)
	}
	close(start)
	wg.Wait()
	wins, used := 0, 0
	for i := range status {
		if errs[i] != nil {
			t.Fatalf("canje %d sin respuesta a tiempo: %v", i, errs[i])
		}
		switch status[i] {
		case http.StatusCreated:
			wins++
		case http.StatusBadRequest:
			used++
		default:
			t.Errorf("canje %d: estado inesperado %d", i, status[i])
		}
	}
	if wins != 1 || used != n-1 {
		t.Fatalf("ganadores=%d rechazados=%d, esperado 1 y %d", wins, used, n-1)
	}
	if e.count(`SELECT count(*) FROM cloud_principal_tokens WHERE name='concurrente'`) != 1 ||
		e.count(`SELECT count(*) FROM nexo_activation_codes WHERE id=$1::bigint AND used_at IS NOT NULL AND token_id IS NOT NULL`, ids[0]) != 1 {
		t.Fatal("debe existir exactamente un token y el código consumido con él")
	}
	if e.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action='nexo.activation.redeemed'`) != 1 {
		t.Fatal("un solo canje auditado como exitoso")
	}
}

// Revocación y canje simultáneos del mismo código: el resultado es uno u otro, nunca ambos.
func TestNexoActivationRevokeAndRedeemRacePG(t *testing.T) {
	e := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	const rounds = 6
	codes, ids := nexoIssueCodes(t, e, rounds, "ana")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	revokeBody, _ := json.Marshal(map[string]string{"principal": "ana"})
	for r := 0; r < rounds; r++ {
		body, _ := json.Marshal(map[string]string{"code": codes[r]})
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 16; i++ {
			wg.Add(2)
			go func(i int) {
				defer wg.Done()
				<-start
				if _, _, err := nexoDoCtx(ctx, e, "POST", "/nexo/authz/v1/activate", "", string(body), fmt.Sprintf("10.90.2.%d", i+1)); err != nil {
					t.Errorf("canje sin respuesta a tiempo: %v", err)
				}
			}(i)
			go func() {
				defer wg.Done()
				<-start
				if _, _, err := nexoDoCtx(ctx, e, "POST", "/nexo/authz/v1/activations/"+ids[r]+"/revoke", "carla", string(revokeBody), "10.90.2.200"); err != nil {
					t.Errorf("revocación sin respuesta a tiempo: %v", err)
				}
			}()
		}
		close(start)
		wg.Wait()
		var used, revoked, tokenSet bool
		if err := e.db.QueryRow(`SELECT used_at IS NOT NULL, revoked_at IS NOT NULL, token_id IS NOT NULL FROM nexo_activation_codes WHERE id=$1::bigint`, ids[r]).Scan(&used, &revoked, &tokenSet); err != nil {
			t.Fatal(err)
		}
		if used && revoked {
			t.Fatalf("ronda %d: el código quedó canjeado y revocado a la vez", r)
		}
		if !used && !revoked {
			t.Fatalf("ronda %d: el código quedó sin resolver", r)
		}
		if used != tokenSet {
			t.Fatalf("ronda %d: canjeado=%v pero token=%v", r, used, tokenSet)
		}
	}
	if got := e.count(`SELECT count(*) FROM cloud_principal_tokens WHERE name='concurrente'`); got != e.count(`SELECT count(*) FROM nexo_activation_codes WHERE used_at IS NOT NULL`) {
		t.Fatalf("hay %d tokens y los canjes registrados no coinciden", got)
	}
}

// Deshabilitar a la persona mientras se canjea: nunca un código consumido sin token ni un token de una persona ya deshabilitada.
func TestNexoActivationDisableAndRedeemRacePG(t *testing.T) {
	e := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	const rounds = 5
	for r := 0; r < rounds; r++ {
		codes, ids := nexoIssueCodes(t, e, 1, "ana")
		body, _ := json.Marshal(map[string]string{"code": codes[0]})
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				if _, _, err := nexoDoCtx(ctx, e, "POST", "/nexo/authz/v1/activate", "", string(body), fmt.Sprintf("10.90.3.%d", i+1)); err != nil {
					t.Errorf("canje sin respuesta a tiempo: %v", err)
				}
			}(i)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := e.cs.SetHumanUserEnabled(ctx, e.ids["ana"], false); err != nil && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("deshabilitar: %v", err)
			}
		}()
		close(start)
		wg.Wait()
		cancel()
		var used, tokenSet bool
		if err := e.db.QueryRow(`SELECT used_at IS NOT NULL, token_id IS NOT NULL FROM nexo_activation_codes WHERE id=$1::bigint`, ids[0]).Scan(&used, &tokenSet); err != nil {
			t.Fatal(err)
		}
		if used != tokenSet {
			t.Fatalf("ronda %d: canjeado=%v con token=%v", r, used, tokenSet)
		}
		// Si ganó la deshabilitación, el código falla por "persona deshabilitada" y no hay token.
		if !used {
			rec := e.req("POST", "/nexo/authz/v1/activate", "", string(body), "10.90.3.99")
			if rec.Code != http.StatusBadRequest || nexoActivationJSON(t, rec)["error_code"] != "activation_disabled" {
				t.Fatalf("ronda %d: código no consumido pero el canje posterior dio %d", r, rec.Code)
			}
		}
		if err := e.cs.SetHumanUserEnabled(t.Context(), e.ids["ana"], true); err != nil {
			t.Fatal(err)
		}
	}
}

// Emisión y canje con el pool en 4 conexiones y 32 operaciones a la vez: ninguna
// transacción pide una segunda conexión mientras está abierta; un plazo vencido es falla.
func TestNexoActivationPool4Concurrent32PG(t *testing.T) {
	t.Setenv(security.DBMaxOpenEnv, "4")
	t.Setenv(security.DBMaxIdleEnv, "4")
	e := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	if got := e.db.Stats().MaxOpenConnections; got != 4 {
		t.Fatalf("el pool de prueba debía tener 4 conexiones, tiene %d", got)
	}
	const n = 32
	codes, _ := nexoIssueCodes(t, e, n/2, "ana")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]int, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i < n/2 { // canjes distintos
				b, _ := json.Marshal(map[string]string{"code": codes[i]})
				results[i], _, errs[i] = nexoDoCtx(ctx, e, "POST", "/nexo/authz/v1/activate", "", string(b), fmt.Sprintf("10.90.4.%d", i+1))
				return
			}
			results[i], _, errs[i] = nexoDoCtx(ctx, e, "POST", "/nexo/authz/v1/tokens", "carla", `{"principal":"luis","name":"emision-pool","scope":"read"}`, fmt.Sprintf("10.90.5.%d", i))
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range results {
		if errors.Is(errs[i], context.DeadlineExceeded) {
			t.Fatalf("operación %d bloqueada (DeadlineExceeded): una transacción pide otra conexión del pool", i)
		}
		if errs[i] != nil || results[i] != http.StatusCreated {
			t.Errorf("operación %d: estado %d, error %v", i, results[i], errs[i])
		}
	}
	if e.count(`SELECT count(*) FROM cloud_principal_tokens WHERE name='concurrente'`) != n/2 || e.count(`SELECT count(*) FROM nexo_activation_codes WHERE name='emision-pool'`) != n/2 {
		t.Fatal("conteos finales inesperados")
	}
}

// El mismo escenario directamente sobre el almacén (sin HTTP): cada operación pide su transacción.
func TestNexoActivationStorePool4Concurrent32PG(t *testing.T) {
	t.Setenv(security.DBMaxOpenEnv, "4")
	t.Setenv(security.DBMaxIdleEnv, "4")
	e := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	const n = 32
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	hasher := e.hasher
	issueCtx := security.WithTokenIssue(cloudstoreOriginCtx(ctx, "10.90.6.1"), security.TokenIssue{Days: 7, Scope: "write"})
	codeHashes := make([]string, n/2)
	var wg sync.WaitGroup
	for i := 0; i < n/2; i++ {
		raw, _ := security.GenerateActivationCode()
		h, err := hasher.Hash("nexo-activation:v1:" + raw)
		if err != nil {
			t.Fatal(err)
		}
		codeHashes[i] = h
		if _, err := e.cs.NexoIssueActivation(issueCtx, e.ids["ana"], h, "almacen", "", cloudstore.AuthAuditEvent{ActorSource: "test", Outcome: "success", ActorPrincipalID: e.ids["carla"]}); err != nil {
			t.Fatalf("emisión previa: %v", err)
		}
	}
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i < n/2 {
				mt, _ := cloudauth.GenerateManagedToken("live")
				th, _ := hasher.Hash(mt.Raw)
				_, errs[i] = e.cs.NexoRedeemActivation(cloudstoreOriginCtx(ctx, "10.90.6.2"), codeHashes[i], cloudstore.CreatePrincipalTokenParams{TokenPrefix: mt.Prefix, TokenHash: th}, cloudstore.NexoRedeemCaller{})
				return
			}
			raw, _ := security.GenerateActivationCode()
			h, _ := hasher.Hash("nexo-activation:v1:" + raw)
			_, errs[i] = e.cs.NexoIssueActivation(issueCtx, e.ids["luis"], h, "almacen-2", "", cloudstore.AuthAuditEvent{ActorSource: "test", Outcome: "success", ActorPrincipalID: e.ids["carla"]})
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("operación %d bloqueada (DeadlineExceeded)", i)
		}
		if err != nil {
			t.Errorf("operación %d: %v", i, err)
		}
	}
	if e.count(`SELECT count(*) FROM cloud_principal_tokens WHERE name='almacen'`) != n/2 {
		t.Fatal("faltan canjes")
	}
}

func cloudstoreOriginCtx(ctx context.Context, ip string) context.Context {
	return cloudstore.WithNexoRequestOrigin(ctx, cloudstore.NexoRequestOrigin{ClientIP: ip, UserAgent: "nexo-concurrency/1.0"})
}
