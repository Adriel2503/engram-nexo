package cloudserver

// Nexo IA · NX-07 fases S2 y S3 — pruebas HTTP contra Postgres real
// (CLOUDSTORE_TEST_DSN) con la resolución real de tokens administrados
// (hash con pepper + FindPrincipalTokenByHash), como en el servidor.
// Criterios CA-S04..CA-S08 de docs/nexo/seguridad/requirements.md.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// ─── Resolución real de tokens (igual que cmd/engram/cloud_runtime_auth.go) ──

type nexoTestTokenLookup struct{ cs *cloudstore.CloudStore }

func (l nexoTestTokenLookup) FindManagedTokenByHash(ctx context.Context, hash string) (cloudauth.ManagedTokenRecord, cloudauth.Principal, error) {
	token, p, err := l.cs.FindPrincipalTokenByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, cloudstore.ErrPrincipalTokenNotFound) {
			return cloudauth.ManagedTokenRecord{}, cloudauth.Principal{}, cloudauth.ErrUnknownToken
		}
		return cloudauth.ManagedTokenRecord{}, cloudauth.Principal{}, err
	}
	kind := cloudauth.PrincipalKindHuman
	if p.Kind == cloudstore.PrincipalKindServiceAccount {
		kind = cloudauth.PrincipalKindServiceAccount
	}
	role := cloudauth.RoleMember
	if p.Role == cloudstore.PrincipalRoleAdmin {
		role = cloudauth.RoleAdmin
	}
	return cloudauth.ManagedTokenRecord{ID: token.ID, PrincipalID: token.PrincipalID, RevokedAt: token.RevokedAt},
		cloudauth.Principal{ID: p.ID, Kind: kind, DisplayName: p.DisplayName, Role: role, Enabled: p.Enabled, Source: cloudauth.PrincipalSourceManagedToken}, nil
}

func (l nexoTestTokenLookup) MarkManagedTokenUsed(ctx context.Context, tokenID string) error {
	err := l.cs.MarkPrincipalTokenUsed(ctx, tokenID)
	switch {
	case errors.Is(err, cloudstore.ErrPrincipalTokenRevoked):
		return cloudauth.ErrTokenRevoked
	case errors.Is(err, cloudstore.ErrPrincipalDisabled):
		return cloudauth.ErrPrincipalDisabled
	}
	return err
}

type nexoRealAuth struct{ r *cloudauth.PrincipalResolver }

func (a nexoRealAuth) Authorize(*http.Request) error {
	return errors.New("legacy Authorize is not used with principal resolution")
}

func (a nexoRealAuth) ResolveBearerToken(ctx context.Context, token string) (cloudauth.Principal, error) {
	return a.r.ResolveBearerToken(ctx, token)
}

type nexoSecEnv struct {
	t      *testing.T
	cs     *cloudstore.CloudStore
	db     *sql.DB
	srv    *CloudServer
	h      http.Handler
	hasher *cloudauth.ManagedTokenHasher
	ids    map[string]string // persona -> principal id
	tokens map[string]string // nombre -> token en claro
}

// newNexoSecEnv: carla (primer admin = superadmin), pedro (admin), ana y luis
// (miembros) y la cuenta de servicio ci, cada uno con un token "<nombre>"
// emitido como en producción. envs se fijan antes de crear el servidor.
func newNexoSecEnv(t *testing.T, envs map[string]string) *nexoSecEnv {
	t.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set (requires Postgres)")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN")
	}
	for _, k := range []string{security.RateLimitsEnv, security.AuthLockoutEnv, security.SuperadminAllowedCIDRsEnv, security.TokenMaxDaysEnv, security.AuditDedupEnv} {
		t.Setenv(k, envs[k])
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("cloudserver_sec_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	cs, err := cloudstore.New(cloud.Config{DSN: dsn + sep + "search_path=" + schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	hasher, err := cloudauth.NewManagedTokenHasher([]byte("test-token-pepper-at-least-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	env := &nexoSecEnv{t: t, cs: cs, db: cs.NexoDB(), hasher: hasher, ids: map[string]string{}, tokens: map[string]string{}}
	seed := []struct{ name, kind, role string }{
		{"carla", "human", "admin"}, {"pedro", "human", "admin"}, {"ana", "human", "member"}, {"luis", "human", "member"}, {"ci", "service_account", "member"},
	}
	for _, p := range seed {
		var id string
		if err := env.db.QueryRow(`INSERT INTO cloud_principals (kind, display_name, role) VALUES ($1, $2, $3) RETURNING id::text`, p.kind, p.name, p.role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if p.kind == "human" {
			if _, err := env.db.Exec(`INSERT INTO cloud_human_users (principal_id, username, email) VALUES ($1::bigint, $2, $3)`, id, p.name, p.name+"@rp.pe"); err != nil {
				t.Fatal(err)
			}
		}
		env.ids[p.name] = id
		env.issue(p.name, p.name, security.TokenIssue{Days: 90, Scope: "write"})
	}
	resolver := cloudauth.NewPrincipalResolver(cloudauth.ResolverConfig{Hasher: hasher, ManagedTokens: nexoTestTokenLookup{cs: cs}, Legacy: cloudauth.LegacyCredentials{SyncToken: "shared-sync-token-e2e"}})
	env.srv = New(cs, nexoRealAuth{r: resolver}, 0, WithAdminIdentityStore(cs), WithManagedTokenHasher(hasher), WithPrincipalStateStore(cs))
	env.h = env.srv.Handler()
	return env
}

// issue emite un token real (hash con pepper) para la persona.
func (e *nexoSecEnv) issue(person, name string, issue security.TokenIssue) string {
	e.t.Helper()
	mt, err := cloudauth.GenerateManagedToken("live")
	if err != nil {
		e.t.Fatal(err)
	}
	hash, err := e.hasher.Hash(mt.Raw)
	if err != nil {
		e.t.Fatal(err)
	}
	tok, err := e.cs.CreatePrincipalTokenWithAudit(security.WithTokenIssue(context.Background(), issue),
		cloudstore.CreatePrincipalTokenParams{PrincipalID: e.ids[person], TokenPrefix: mt.Prefix, TokenHash: hash, Name: name},
		cloudstore.AuthAuditEvent{ActorSource: "test", Action: "token.create", Outcome: "success"})
	if err != nil {
		e.t.Fatalf("emitir %s: %v", name, err)
	}
	e.tokens[name] = mt.Raw
	return tok.ID
}

func (e *nexoSecEnv) req(method, path, who, body, ip string) *httptest.ResponseRecorder {
	e.t.Helper()
	token := e.tokens[who]
	if strings.HasPrefix(who, "raw:") {
		token = strings.TrimPrefix(who, "raw:")
	}
	return nexoDoFrom(e.t, e.h, method, path, token, body, ip+":40000", "", "nexo-sec-test/1.0")
}

func (e *nexoSecEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (e *nexoSecEnv) waitCount(want int, q string, args ...any) int {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := e.count(q, args...)
		if n >= want || time.Now().After(deadline) {
			return n
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func nexoJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("JSON: %v (%s)", err, rec.Body.String())
	}
	return v
}

// ─── CA-S04: límite de peticiones ────────────────────────────────────────────

func TestNexoRateLimitPerIPAndToken(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	const path = "/nexo/authz/v1/me"
	// 1 000 peticiones del grupo admin (120/min por IP y por token) desde
	// una IP, alternando dos tokens: el límite por IP corta primero.
	allowed, limited := 0, 0
	var first429 *httptest.ResponseRecorder
	start := time.Now()
	for i := 0; i < 1000; i++ {
		who := "ana"
		if i%2 == 1 {
			who = "luis"
		}
		rec := env.req(http.MethodGet, path, who, "", "10.10.0.1")
		switch rec.Code {
		case http.StatusOK:
			if limited > 0 {
				t.Fatalf("petición %d permitida después del primer 429 (sin pausa)", i)
			}
			allowed++
		case http.StatusTooManyRequests:
			limited++
			if first429 == nil {
				first429 = rec
			}
		default:
			t.Fatalf("petición %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	elapsed := time.Since(start)
	if allowed < 120 || allowed > 120+int(elapsed.Seconds()*2)+2 || limited != 1000-allowed {
		t.Fatalf("CA-S04: permitidas=%d limitadas=%d en %s", allowed, limited, elapsed)
	}
	if first429.Header().Get("Retry-After") == "" || nexoJSON(t, first429)["error_code"] != "rate_limited" {
		t.Fatalf("429: cabeceras %v cuerpo %s", first429.Header(), first429.Body.String())
	}
	// Otra IP no se ve afectada.
	if rec := env.req(http.MethodGet, path, "carla", "", "10.10.0.2"); rec.Code != http.StatusOK {
		t.Fatalf("otra IP: %d", rec.Code)
	}
	// Evento agregado por minuto y clave (no uno por 429).
	if n := env.srv.nexoFlushRateLimitEvents(true); n != 1 {
		t.Fatalf("resúmenes = %d, want 1", n)
	}
	var meta string
	if err := env.db.QueryRow(`SELECT metadata::text FROM cloud_auth_audit_log WHERE action = 'nexo.rate_limited'`).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(meta, `"client_ip": "10.10.0.1"`) || !strings.Contains(meta, fmt.Sprintf(`"rejected": %d`, limited)) || !strings.Contains(meta, `"group": "admin"`) {
		t.Fatalf("evento: %s", meta)
	}
}

func TestNexoRateLimitPerTokenAndConfig(t *testing.T) {
	env := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: `{"admin":{"ip":0,"token":30}}`})
	limitedAt := -1
	for i := 0; i < 60; i++ {
		ip := fmt.Sprintf("10.20.0.%d", i%5+1) // el mismo token desde varias IP
		if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "ana", "", ip); rec.Code == http.StatusTooManyRequests {
			limitedAt = i
			break
		}
	}
	if limitedAt != 30 {
		t.Fatalf("límite por token: primer 429 en %d, want 30", limitedAt)
	}
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "luis", "", "10.20.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("otro token: %d", rec.Code)
	}
	env.srv.nexoFlushRateLimitEvents(true)
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = 'nexo.rate_limited' AND actor_principal_id = $1::bigint AND metadata->>'key_type' = 'token'`, env.ids["ana"]); n != 1 {
		t.Fatalf("evento por token: %d", n)
	}
	// NEXO_RATE_LIMITS=off desactiva el límite.
	off := newNexoSecEnv(t, map[string]string{security.RateLimitsEnv: "off"})
	for i := 0; i < 300; i++ {
		if rec := off.req(http.MethodGet, "/health", "", "", "10.30.0.1"); rec.Code == http.StatusTooManyRequests {
			t.Fatal("off: no debe limitar")
		}
	}
}

// ─── CA-S05: bloqueo por fallos de autenticación ─────────────────────────────

func TestNexoAuthLockoutByIP(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	const ip = "10.40.0.9"
	for i := 0; i < 20; i++ {
		rec := env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", fmt.Sprintf("raw:egc_live_falso%02d_xxxxxxxxxxxxxxxxxxxx", i), "", ip)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("intento %d: %d", i, rec.Code)
		}
	}
	// Bloqueada: incluso un token válido recibe 429 genérico (sin resolverlo).
	rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "ana", "", ip)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || nexoJSON(t, rec)["error_code"] != "auth_locked" {
		t.Fatalf("CA-S05 bloqueo: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "ana") {
		t.Fatal("la respuesta no revela datos del usuario")
	}
	// Otra IP sigue funcionando.
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "ana", "", "10.40.0.10"); rec.Code != http.StatusOK {
		t.Fatalf("otra IP: %d", rec.Code)
	}
	// El login del panel desde la IP bloqueada tampoco valida.
	form := url.Values{"token": {env.tokens["carla"]}}.Encode()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = ip + ":1"
	login := httptest.NewRecorder()
	env.h.ServeHTTP(login, req)
	if login.Code == http.StatusSeeOther {
		t.Fatal("login desde una IP bloqueada")
	}
	// Un solo evento auth.lockout con la IP.
	if n := env.waitCount(1, `SELECT count(*) FROM cloud_auth_audit_log WHERE action = 'auth.lockout' AND metadata->>'client_ip' = $1 AND metadata->>'key_type' = 'ip'`, ip); n != 1 {
		t.Fatalf("eventos auth.lockout = %d", n)
	}
}

func TestNexoAuthLockoutByUserAndExpiredTokens(t *testing.T) {
	env := newNexoSecEnv(t, map[string]string{security.AuthLockoutEnv: `{"max_failures":20,"user_max_failures":5,"window_seconds":300,"lockout_seconds":900}`})
	revoked := env.issue("luis", "luis-viejo", security.TokenIssue{Days: 90, Scope: "write"})
	if _, err := env.db.Exec(`UPDATE cloud_principal_tokens SET revoked_at = now() WHERE id = $1::bigint`, revoked); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ { // desde IP distintas: cuenta por persona
		if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "luis-viejo", "", fmt.Sprintf("10.50.0.%d", i+1)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("revocado %d: %d", i, rec.Code)
		}
	}
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "luis", "", "10.50.1.1"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("persona bloqueada: %d %s", rec.Code, rec.Body.String())
	}
	if n := env.waitCount(1, `SELECT count(*) FROM cloud_auth_audit_log WHERE action = 'auth.lockout' AND target_principal_id = $1::bigint`, env.ids["luis"]); n != 1 {
		t.Fatalf("evento auth.lockout de la persona = %d", n)
	}
	// Un token vencido no suma al bloqueo (autosync de una PC con el token
	// vencido): queda auditado como token_expired.
	expired := env.issue("ana", "ana-vencido", security.TokenIssue{Days: 90, Scope: "write"})
	if _, err := env.db.Exec(`UPDATE cloud_principal_tokens SET nexo_expires_at = now() - interval '1 second' WHERE id = $1::bigint`, expired); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		if rec := env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", "ana-vencido", "", "10.60.0.1"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("vencido %d: %d", i, rec.Code)
		}
	}
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/me", "ana", "", "10.60.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("token vencido no bloquea a la persona ni a la IP: %d", rec.Code)
	}
}

// D-S2-8: por IP cuentan tokens distintos, no intentos. Una PC con un token
// mal pegado (autosync cada ~30 s) no bloquea a la oficina detrás del NAT;
// 20 tokens distintos sí bloquean. El login del panel sigue el mismo criterio.
func TestNexoAuthLockoutCountsDistinctTokensPerIP(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	const ip = "10.41.0.1"
	const fixed = "raw:egc_live_malpegado_xxxxxxxxxxxxxxxxxxxxxxxx"
	login := func(token string) int {
		req := httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader(url.Values{"token": {token}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = ip + ":1"
		rec := httptest.NewRecorder()
		env.h.ServeHTTP(rec, req)
		return rec.Code
	}
	// 1) El mismo token inválido 100 veces (más que 20 en la ventana), por
	// sync y por el login del panel, y la misma cabecera mal formada 50 veces.
	for i := 0; i < 100; i++ {
		if rec := env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", fixed, "", ip); rec.Code != http.StatusUnauthorized {
			t.Fatalf("token fijo, intento %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	for i := 0; i < 10; i++ { // límite del grupo login: 20/min por IP
		if code := login(strings.TrimPrefix(fixed, "raw:")); code != http.StatusUnauthorized && code != http.StatusOK {
			t.Fatalf("login con token inválido: %d", code)
		}
	}
	for i := 0; i < 50; i++ {
		if rec := env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", "raw:mal formado", "", ip); rec.Code != http.StatusUnauthorized {
			t.Fatalf("cabecera mal formada %d: %d", i, rec.Code)
		}
	}
	// Las demás PCs de la misma IP siguen sincronizando.
	if rec := env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", "ana", "", ip); rec.Code != http.StatusOK {
		t.Fatalf("D-S2-8: un token inválido fijo bloqueó la IP: %d %s", rec.Code, rec.Body.String())
	}
	// Cada rechazo sigue auditado: desde el arreglo A2 (S4) el primero de cada
	// huella e IP es una fila completa y los repetidos se cuentan en un
	// resumen por ventana; el total no se pierde.
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = 'sync.auth' AND reason_code = 'unknown_token'`); n != 1 {
		t.Fatalf("filas completas del token fijo = %d, want 1 (A2)", n)
	}
	env.srv.nexoFlushAuditDedup(context.Background(), true)
	if n := env.count(`SELECT COALESCE(sum(COALESCE((metadata->>'nexo_repeated')::int, 0) + CASE WHEN metadata ? 'nexo_repeated' THEN 0 ELSE 1 END), 0)
		FROM cloud_auth_audit_log WHERE action = 'sync.auth' AND reason_code = 'unknown_token'`); n != 100 {
		t.Fatalf("rechazos auditados (filas + conteos) = %d, want 100", n)
	}
	// 2) Van 2 tokens distintos (el fijo y la cabecera mal formada); 17 más
	// suman 19: sin bloqueo. El 20.º distinto bloquea.
	for i := 0; i < 17; i++ {
		env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", fmt.Sprintf("raw:egc_live_distinto%02d_xxxxxxxxxxxxxxxxxxxx", i), "", ip)
		env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", fmt.Sprintf("raw:egc_live_distinto%02d_xxxxxxxxxxxxxxxxxxxx", i), "", ip) // repetido: no suma
	}
	if rec := env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", "ana", "", ip); rec.Code != http.StatusOK {
		t.Fatalf("19 tokens distintos no bloquean: %d", rec.Code)
	}
	if code := login("egc_live_vigesimo_distinto_xxxxxxxxxxxxxxxxxx"); code == http.StatusSeeOther {
		t.Fatal("login con token inválido")
	}
	rec := env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", "ana", "", ip)
	if rec.Code != http.StatusTooManyRequests || nexoJSON(t, rec)["error_code"] != "auth_locked" {
		t.Fatalf("CA-S05: 20 tokens distintos deben bloquear la IP: %d %s", rec.Code, rec.Body.String())
	}
	var meta string
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := env.db.QueryRow(`SELECT metadata::text FROM cloud_auth_audit_log WHERE action = 'auth.lockout' AND metadata->>'client_ip' = $1`, ip).Scan(&meta)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(meta, `"counts": "distinct_tokens"`) || !strings.Contains(meta, `"failures": 20`) {
		t.Fatalf("evento auth.lockout: %q", meta)
	}
	if strings.Contains(meta, "malpegado") || strings.Contains(meta, "distinto") {
		t.Fatal("el evento no guarda tokens")
	}
}

// ─── CA-S06: sin auto-asignación ─────────────────────────────────────────────

func TestNexoNoSelfGrant(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	ids := env.ids
	// /admin/*: la superadmin no se da acceso a sí misma; a otra persona sí.
	rec := env.req(http.MethodPost, "/admin/users/"+ids["carla"]+"/grants", "carla", `{"project":"nexo-privado"}`, "10.70.0.1")
	if rec.Code != http.StatusForbidden || nexoJSON(t, rec)["error_code"] != "self_grant_forbidden" {
		t.Fatalf("CA-S06 /admin: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.req(http.MethodPost, "/admin/users/"+ids["ana"]+"/grants", "carla", `{"project":"nexo-privado"}`, "10.70.0.1"); rec.Code != http.StatusCreated {
		t.Fatalf("permiso a otra persona: %d %s", rec.Code, rec.Body.String())
	}
	// Un admin tampoco, y un miembro no tiene platform.users.manage.
	if rec := env.req(http.MethodPost, "/admin/users/"+ids["pedro"]+"/grants", "pedro", `{"project":"x"}`, "10.70.0.1"); rec.Code != http.StatusForbidden {
		t.Fatalf("admin a sí mismo: %d", rec.Code)
	}
	if rec := env.req(http.MethodPost, "/admin/users/"+ids["ana"]+"/grants", "ana", `{"project":"x"}`, "10.70.0.1"); rec.Code != http.StatusForbidden {
		t.Fatalf("miembro: %d", rec.Code)
	}
	// Panel.
	cookie := managedDashboardLogin(t, env.srv, env.tokens["pedro"], false)
	if rec := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/users/"+ids["pedro"]+"/grants", "project=nexo-privado", cookie, false); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "themselves") {
		t.Fatalf("CA-S06 panel: %d %s", rec.Code, rec.Body.String())
	}
	if n := env.count(`SELECT count(*) FROM cloud_project_grants WHERE principal_id IN ($1::bigint, $2::bigint)`, ids["carla"], ids["pedro"]); n != 0 {
		t.Fatalf("permisos propios creados: %d", n)
	}
	if n := env.count(`SELECT count(*) FROM nexo_authz_events WHERE action = 'authz.denied' AND details->>'reason' = 'self_grant_forbidden'`); n != 3 {
		t.Fatalf("eventos self_grant_forbidden = %d, want 3", n)
	}
	// API Nexo (R-11): la superadmin crea el proyecto con Ana como responsable
	// y no puede nombrarse miembro.
	if rec := env.req(http.MethodPost, "/nexo/authz/v1/scopes", "carla", `{"type":"project","key":"nexo-privado","owner":"`+ids["carla"]+`"}`, "10.70.0.1"); rec.Code != http.StatusForbidden {
		t.Fatalf("API Nexo, responsable ella misma: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.req(http.MethodPost, "/nexo/authz/v1/scopes", "carla", `{"type":"project","key":"nexo-privado","owner":"ana"}`, "10.70.0.1"); rec.Code != http.StatusCreated {
		t.Fatalf("crear proyecto: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.req(http.MethodPut, "/nexo/authz/v1/scopes/project/nexo-privado/members/"+ids["ana"], "ana", `{"role":"responsable"}`, "10.70.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("responsable sin cambio: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.req(http.MethodPut, "/nexo/authz/v1/scopes/project/nexo-privado/members/"+ids["carla"], "carla", `{"role":"lector"}`, "10.70.0.1")
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Fatalf("API Nexo, darse acceso: %d %s", rec.Code, rec.Body.String())
	}
}

// ─── CA-S07: redes permitidas ────────────────────────────────────────────────

func TestNexoSuperadminAllowedCIDRs(t *testing.T) {
	env := newNexoSecEnv(t, map[string]string{security.SuperadminAllowedCIDRsEnv: "10.80.0.0/16"})
	rec := env.req(http.MethodPost, "/admin/users", "carla", `{"username":"jefe","role":"admin"}`, "192.168.50.7")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "network") {
		t.Fatalf("CA-S07 fuera de la red: %d %s", rec.Code, rec.Body.String())
	}
	if n := env.count(`SELECT count(*) FROM nexo_authz_events WHERE action = 'authz.denied' AND details->>'reason' = 'network' AND client_ip = '192.168.50.7'`); n != 1 {
		t.Fatalf("CA-S07 auditado: %d", n)
	}
	if rec := env.req(http.MethodPut, "/nexo/authz/v1/platform/members/"+env.ids["ana"], "carla", `{"role":"auditor"}`, "192.168.50.7"); rec.Code != http.StatusForbidden {
		t.Fatalf("API Nexo fuera de la red: %d", rec.Code)
	}
	// Desde la red permitida, sí; y lo que no es de superadmin no depende de la red.
	if rec := env.req(http.MethodPost, "/admin/users", "carla", `{"username":"jefe","role":"admin"}`, "10.80.3.4"); rec.Code != http.StatusCreated {
		t.Fatalf("desde la red: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.req(http.MethodPost, "/admin/users", "pedro", `{"username":"miembro2","role":"member"}`, "192.168.50.7"); rec.Code != http.StatusCreated {
		t.Fatalf("users.manage fuera de la red: %d %s", rec.Code, rec.Body.String())
	}
}

// ─── CA-S08: caducidad y alcance ─────────────────────────────────────────────

func TestNexoTokenExpiryAndScopeHTTP(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	ids := env.ids
	const ip = "10.90.0.1"
	// Emisión por /admin/*: 90 días por defecto, write.
	rec := env.req(http.MethodPost, "/admin/users/"+ids["ana"]+"/tokens", "carla", `{"name":"laptop"}`, ip)
	if rec.Code != http.StatusCreated {
		t.Fatalf("emitir: %d %s", rec.Code, rec.Body.String())
	}
	tok := nexoJSON(t, rec)["token"].(map[string]any)
	exp, err := time.Parse(time.RFC3339Nano, tok["expires_at"].(string))
	if err != nil || time.Until(exp) < 89*24*time.Hour || time.Until(exp) > 90*24*time.Hour+time.Minute || tok["scope"] != "write" {
		t.Fatalf("CA-S08 90 días: %v %v", tok, err)
	}
	for body, want := range map[string]int{
		`{"expires_in_days":366}`: http.StatusBadRequest,
		`{"scope":"admin"}`:       http.StatusBadRequest,
		`{"expires_in_days":0}`:   http.StatusBadRequest, // persona sin caducidad
	} {
		if rec := env.req(http.MethodPost, "/admin/users/"+ids["ana"]+"/tokens", "carla", body, ip); rec.Code != want {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	// API Nexo: sin caducidad solo cuenta de servicio y solo superadmin.
	if rec := env.req(http.MethodPost, "/nexo/authz/v1/tokens", "pedro", `{"principal":"`+ids["ci"]+`","expires_in_days":0}`, ip); rec.Code != http.StatusForbidden {
		t.Fatalf("admin sin caducidad: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.req(http.MethodPost, "/nexo/authz/v1/tokens", "carla", `{"principal":"`+ids["ci"]+`","name":"pipeline","expires_in_days":0}`, ip)
	if rec.Code != http.StatusCreated || nexoJSON(t, rec)["token"].(map[string]any)["no_expiry"] != true {
		t.Fatalf("superadmin sin caducidad para cuenta de servicio: %d %s", rec.Code, rec.Body.String())
	}
	// Token read emitido por la API Nexo para Ana.
	rec = env.req(http.MethodPost, "/nexo/authz/v1/tokens", "carla", `{"principal":"ana","name":"lectura","expires_in_days":30,"scope":"read"}`, ip)
	if rec.Code != http.StatusCreated {
		t.Fatalf("token read: %d %s", rec.Code, rec.Body.String())
	}
	env.tokens["ana-read"] = nexoJSON(t, rec)["raw_token"].(string)
	// Un token read no sube cambios de proyecto (rutas de Engram).
	for _, path := range []string{"/sync/mutations/push", "/sync/push"} {
		rec := env.req(http.MethodPost, path, "ana-read", nexoMutationBody, ip)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "read-only") {
			t.Fatalf("CA-S08 %s con token read: %d %s", path, rec.Code, rec.Body.String())
		}
		if rec := env.req(http.MethodPost, path, "ana", nexoMutationBody, ip); strings.Contains(rec.Body.String(), "read-only") {
			t.Fatalf("token write en %s: %s", path, rec.Body.String())
		}
	}
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE reason_code = 'token_scope_read'`); n != 2 {
		t.Fatalf("denegaciones por alcance auditadas: %d", n)
	}
	// /me muestra alcance, caducidad y último uso.
	me := nexoJSON(t, env.req(http.MethodGet, "/nexo/authz/v1/me", "ana-read", "", ip))
	info, _ := me["token"].(map[string]any)
	if me["token_scope"] != "read" || info == nil || info["scope"] != "read" || info["expires_at"] == nil || info["last_used_at"] == nil {
		t.Fatalf("RS-24 /me: %v", me)
	}
	// Un admin con token read no administra (pero lee).
	env.issue("carla", "carla-read", security.TokenIssue{Days: 10, Scope: "read"})
	if rec := env.req(http.MethodGet, "/admin/users", "carla-read", "", ip); rec.Code != http.StatusOK {
		t.Fatalf("lectura con token read: %d", rec.Code)
	}
	if rec := env.req(http.MethodPost, "/admin/users", "carla-read", `{"username":"x1"}`, ip); rec.Code != http.StatusForbidden {
		t.Fatalf("CA-S08 administrar con token read: %d %s", rec.Code, rec.Body.String())
	}
	// Listados con caducidad, alcance y último uso.
	list := env.req(http.MethodGet, "/admin/users/"+ids["ana"]+"/tokens", "carla", "", ip)
	if !strings.Contains(list.Body.String(), `"scope":"read"`) || !strings.Contains(list.Body.String(), `"expires_at":"`) {
		t.Fatalf("listado /admin: %s", list.Body.String())
	}
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/tokens", "ana", "", ip); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"last_used_at"`) {
		t.Fatalf("mis tokens: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.req(http.MethodGet, "/nexo/authz/v1/tokens?principal=carla", "ana", "", ip); rec.Code != http.StatusForbidden {
		t.Fatalf("tokens ajenos sin capacidad: %d", rec.Code)
	}
	// Token vencido: 401 token_expired, auditado.
	if _, err := env.db.Exec(`UPDATE cloud_principal_tokens SET nexo_expires_at = now() - interval '1 second' WHERE principal_id = $1::bigint AND name = 'lectura'`, ids["ana"]); err != nil {
		t.Fatal(err)
	}
	rec = env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", "ana-read", "", ip)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "token_expired") {
		t.Fatalf("CA-S08 vencido: %d %s", rec.Code, rec.Body.String())
	}
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = 'sync.auth' AND reason_code = 'token_expired'`); n != 1 {
		t.Fatalf("vencido auditado: %d", n)
	}
}

// Las sesiones del panel caen apenas el token se revoca o vence, y el panel
// muestra caducidad y alcance.
func TestNexoDashboardSessionFollowsToken(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	cookie := managedDashboardLogin(t, env.srv, env.tokens["carla"], false)
	page := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/admin/users/"+env.ids["ana"], cookie)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), " / write") || !strings.Contains(page.Body.String(), "expires_in_days") {
		t.Fatalf("panel: %d", page.Code)
	}
	// Emisión desde el panel con alcance read y 15 días.
	created := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/users/"+env.ids["ana"]+"/tokens", "name=panel&scope=read&expires_in_days=15", cookie, false)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), "scope read") {
		t.Fatalf("emisión en el panel: %d %s", created.Code, created.Body.String())
	}
	if n := env.count(`SELECT count(*) FROM cloud_principal_tokens WHERE name = 'panel' AND nexo_scope = 'read' AND nexo_expires_at BETWEEN now() + interval '14 days' AND now() + interval '15 days 1 minute'`); n != 1 {
		t.Fatalf("token del panel: %d", n)
	}
	if bad := performDashboardForm(env.srv, http.MethodPost, "/dashboard/admin/users/"+env.ids["ana"]+"/tokens", "name=x&expires_in_days=0", cookie, false); bad.Code != http.StatusBadRequest {
		t.Fatalf("panel sin caducidad para persona: %d", bad.Code)
	}
	if _, err := env.db.Exec(`UPDATE cloud_principal_tokens SET nexo_expires_at = now() - interval '1 second' WHERE principal_id = $1::bigint AND name = 'carla'`, env.ids["carla"]); err != nil {
		t.Fatal(err)
	}
	if rec := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/admin/users/"+env.ids["ana"], cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("sesión con token vencido: %d", rec.Code)
	}
}

// D-S3-8: alcance read con "negar por defecto" en las rutas de Engram; las
// POST de /admin/* también niegan a un token read (authz.Can).
func TestNexoReadScopeDenyByDefault(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	ids := env.ids
	const ip = "10.91.0.1"
	env.issue("ana", "ana-read", security.TokenIssue{Days: 30, Scope: "read"})
	env.issue("carla", "carla-read", security.TokenIssue{Days: 30, Scope: "read"})
	// Rutas de escritura de Engram que antes no estaban en la lista negra.
	writes := map[string]string{
		"/sync/session-authorities":        `{"session_id":"s-1","owner_project":"nexo-transversal"}`,
		"/sync/prompt-pair-claims":         `{"session_id":"s-1","source_inbox_id":"i-1","sync_id":"p-1","prompt_project":"nexo-transversal"}`,
		"/sync/prompt-source-attestations": `{"session_id":"s-1","source_inbox_id":"i-1","sync_id":"p-1","owner_project":"nexo-transversal","prompt_project":"nexo-transversal"}`,
		"/sync/push":                       nexoMutationBody,
		"/sync/mutations/push":             nexoMutationBody,
	}
	for path, body := range writes {
		rec := env.req(http.MethodPost, path, "ana-read", body, ip)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "read-only") || nexoJSON(t, rec)["error_code"] != "policy_forbidden" {
			t.Fatalf("POST %s con token read: %d %s", path, rec.Code, rec.Body.String())
		}
		// Con token write la ruta no se niega por alcance (puede fallar por
		// otros motivos de Engram, como permisos de proyecto).
		if rec := env.req(http.MethodPost, path, "ana", body, ip); strings.Contains(rec.Body.String(), "read-only") {
			t.Fatalf("POST %s con token write: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	// Una ruta de escritura nueva (sin registrar en ninguna lista) también se niega.
	probe := httptest.NewRequest(http.MethodPost, "/sync/futura-escritura", nil)
	probe.Pattern = "POST /sync/futura-escritura"
	if !nexoReadScopeDenies(probe) {
		t.Fatal("una ruta POST nueva de Engram debe negarse a un token read sin tocar listas")
	}
	// Lectura: GET y la POST de solo lectura de la lista blanca.
	if rec := env.req(http.MethodGet, "/sync/mutations/pull?since_seq=0", "ana-read", "", ip); rec.Code != http.StatusOK {
		t.Fatalf("GET con token read: %d %s", rec.Code, rec.Body.String())
	}
	rec := env.req(http.MethodPost, "/sync/prompt-source-attestations/verify", "ana-read", `{"audit_id":1,"session_id":"s-1","source_inbox_id":"i-1","sync_id":"p-1","owner_project":"nexo-transversal","prompt_project":"nexo-transversal"}`, ip)
	if strings.Contains(rec.Body.String(), "read-only") {
		t.Fatalf("verify (solo lectura) con token read: %d %s", rec.Code, rec.Body.String())
	}
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE reason_code = 'token_scope_read' AND metadata->>'route' = 'POST /sync/session-authorities'`); n != 1 {
		t.Fatalf("denegación auditada con la ruta: %d", n)
	}
	// /admin/*: todas las POST niegan a un admin con token read.
	adminPosts := map[string]string{
		"/admin/users":                                   `{"username":"x9"}`,
		"/admin/users/" + ids["ana"] + "/enable":         ``,
		"/admin/users/" + ids["ana"] + "/disable":        ``,
		"/admin/users/" + ids["ana"] + "/tokens":         `{"name":"x"}`,
		"/admin/tokens/1/revoke":                         ``,
		"/admin/users/" + ids["ana"] + "/grants":         `{"project":"x"}`,
		"/admin/users/" + ids["ana"] + "/grants/x/revoke": ``,
	}
	for path, body := range adminPosts {
		if rec := env.req(http.MethodPost, path, "carla-read", body, ip); rec.Code != http.StatusForbidden {
			t.Fatalf("POST %s con token read de superadmin: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if n := env.count(`SELECT count(*) FROM cloud_principals WHERE enabled = false`); n != 0 {
		t.Fatalf("un token read deshabilitó personas: %d", n)
	}
}

// D-S3-9: una sesión del panel hereda el alcance del token con que se abrió.
// Con write administra; con read solo mira.
func TestNexoDashboardReadSessionCannotAdminister(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	ids := env.ids
	env.issue("carla", "carla-read", security.TokenIssue{Days: 30, Scope: "read"})
	anaTok := env.issue("ana", "ana-extra", security.TokenIssue{Days: 30, Scope: "write"})
	if _, err := env.db.Exec(`INSERT INTO cloud_project_grants (principal_id, project) VALUES ($1::bigint, 'proyecto-a')`, ids["luis"]); err != nil {
		t.Fatal(err)
	}
	readCookie := managedDashboardLogin(t, env.srv, env.tokens["carla-read"], false)
	writeCookie := managedDashboardLogin(t, env.srv, env.tokens["carla"], false)
	actions := []struct{ path, form string }{
		{"/dashboard/admin/users", "username=nuevo1&display_name=Nuevo&role=member"},
		{"/dashboard/admin/users/" + ids["ana"] + "/tokens", "name=panel"},
		{"/dashboard/admin/tokens/" + anaTok + "/revoke", ""},
		{"/dashboard/admin/users/" + ids["ana"] + "/grants", "project=proyecto-b"},
		{"/dashboard/admin/users/" + ids["luis"] + "/grants/proyecto-a/revoke", ""},
		{"/dashboard/admin/users/" + ids["pedro"] + "/disable", ""},
		{"/dashboard/admin/projects/proyecto-a/sync", "enabled=false&reason=prueba"},
	}
	// Lectura con la sesión read: sí.
	if rec := performDashboardRequest(env.srv, http.MethodGet, "/dashboard/admin/users/"+ids["ana"], readCookie); rec.Code != http.StatusOK {
		t.Fatalf("lectura del panel con sesión read: %d", rec.Code)
	}
	for _, a := range actions {
		for _, htmx := range []bool{false, true} {
			rec := performDashboardForm(env.srv, http.MethodPost, a.path, a.form, readCookie, htmx)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("panel read, POST %s (htmx=%v): %d %s", a.path, htmx, rec.Code, rec.Body.String())
			}
		}
	}
	if n := env.count(`SELECT count(*) FROM cloud_auth_audit_log WHERE action = 'dashboard.authorize' AND reason_code = 'token_scope_read'`); n != 2*len(actions) {
		t.Fatalf("denegaciones del panel auditadas: %d, want %d", n, 2*len(actions))
	}
	if n := env.count(`SELECT count(*) FROM cloud_human_users WHERE username = 'nuevo1'`) +
		env.count(`SELECT count(*) FROM cloud_principal_tokens WHERE name = 'panel' OR (id = $1::bigint AND revoked_at IS NOT NULL)`, anaTok) +
		env.count(`SELECT count(*) FROM cloud_project_grants WHERE project = 'proyecto-b'`) +
		env.count(`SELECT count(*) FROM cloud_principals WHERE enabled = false`) +
		env.count(`SELECT count(*) FROM cloud_project_controls WHERE sync_enabled = false`); n != 0 {
		t.Fatalf("la sesión read cambió datos: %d", n)
	}
	if n := env.count(`SELECT count(*) FROM cloud_project_grants WHERE project = 'proyecto-a'`); n != 1 {
		t.Fatalf("la sesión read revocó un permiso: %d", n)
	}
	// La misma persona con una sesión write sí administra.
	for _, a := range actions {
		rec := performDashboardForm(env.srv, http.MethodPost, a.path, a.form, writeCookie, false)
		if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
			t.Fatalf("panel write, POST %s: %d %s", a.path, rec.Code, rec.Body.String())
		}
	}
	if n := env.count(`SELECT count(*) FROM cloud_human_users WHERE username = 'nuevo1'`); n != 1 {
		t.Fatalf("sesión write no creó el usuario: %d", n)
	}
	if n := env.count(`SELECT count(*) FROM cloud_principals WHERE id = $1::bigint AND enabled = false`, ids["pedro"]); n != 1 {
		t.Fatalf("sesión write no deshabilitó: %d", n)
	}
	if n := env.count(`SELECT count(*) FROM cloud_project_controls WHERE project = 'proyecto-a' AND sync_enabled = false`); n != 1 {
		t.Fatalf("sesión write no pausó la sincronización: %d", n)
	}
	// Cerrar sesión con la sesión read sigue permitido.
	if rec := performDashboardForm(env.srv, http.MethodPost, "/dashboard/logout", "", readCookie, false); rec.Code == http.StatusForbidden {
		t.Fatalf("logout con sesión read: %d", rec.Code)
	}
}

// ─── RNF-S1: sobrecosto del middleware ───────────────────────────────────────

func TestNexoRateLimitMiddlewareOverhead(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	t.Setenv(security.RateLimitsEnv, `{"other":{"ip":100000000}}`)
	srv := &CloudServer{}
	with := srv.nexoHandler(ok)
	without := nexoClientContextMiddleware(nexoParseTrustedProxies(os.Getenv(NexoTrustedProxiesEnv)), ok)
	measure := func(h http.Handler) time.Duration {
		const n = 20000
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.RemoteAddr = "10.0.0.1:1"
		start := time.Now()
		for i := 0; i < n; i++ {
			h.ServeHTTP(httptest.NewRecorder(), req)
		}
		return time.Since(start) / n
	}
	measure(with)
	base, full := measure(without), measure(with)
	t.Logf("por petición: NX-04 %s, NX-04 + NX-07 %s, sobrecosto %s", base, full, full-base)
	if full-base > 200*time.Microsecond {
		t.Fatalf("RNF-S1: sobrecosto %s supera 0,2 ms", full-base)
	}
}

func BenchmarkNexoSecurityMiddleware(b *testing.B) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	b.Setenv(security.RateLimitsEnv, `{"sync":{"ip":100000000}}`)
	srv := &CloudServer{}
	h := srv.nexoHandler(ok)
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull", nil)
	req.RemoteAddr = "10.0.0.1:1"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}
