package cloudserver

// Nexo IA · NX-07 fase S5 (S5-01, CA-S13, RS-40) — revisión estática de los
// tipos de respuesta JSON de /admin/*, /nexo/* y del panel.
//
// Cada tipo que una ruta serializa se recorre por reflexión (campos, campos
// embebidos, punteros, listas y mapas) y la prueba falla si:
//   - un campo exportado no tiene etiqueta json explícita (nombre o "-"): un
//     campo nuevo en una estructura interna no debe salir solo por existir;
//   - un nombre JSON parece un secreto (hash de token, contraseña, secreto,
//     pepper, verificador, cookie, cabecera Authorization);
//   - un campo es un tipo de database/sql (detalle interno de la base).
//
// Los mapas libres (details, metadata, settings) no se pueden revisar aquí:
// los cubre la prueba dinámica TestNexoNoSecretsInResponsesAndLogs
// (nexo_secrets_test.go), que recorre las respuestas reales.
//
// Al agregar una ruta JSON nueva, agregar aquí el tipo que devuelve.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
	kbservice "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	kbstore "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

// nexoResponseTypes: ruta (o familia) -> tipo serializado.
func nexoResponseTypes() map[string]any {
	return map[string]any{
		// Engram /admin/* (tipos de Engram; sin cambios de código: si Engram
		// agrega un campo sin etiqueta o con nombre sensible, esta prueba lo ve).
		"GET /admin/users":                        []cloudstore.HumanUser{},
		"POST /admin/users":                       cloudstore.HumanUser{},
		"GET /admin/users/{id}/tokens":            []adminTokenMetadata{},
		"POST /admin/users/{id}/tokens":           adminTokenMetadata{},
		"GET|POST /admin/users/{id}/grants":       cloudstore.ProjectGrant{},
		"GET /nexo/admin/audit":                   nexoPage[cloudstore.NexoAuditRecord]{},
		"GET /nexo/admin/activity":                nexoPage[cloudstore.NexoActivityRecord]{},
		"GET /nexo/admin/presence":                nexoPage[cloudstore.NexoPresenceRecord]{},
		"GET /nexo/admin/history/{entityKey}":     nexoPage[cloudstore.NexoHistoryRecord]{},
		"GET /nexo/admin/authz-events":            nexoPage[authz.EventRecord]{},
		"GET /nexo/admin/database":                cloudstore.NexoDBStatus{}, // SG-05
		"GET /nexo/admin/audit/verify":            evidence.Report{},
		"GET /nexo/admin/alerts":                  nexoPage[evidence.Alert]{},
		"POST /nexo/admin/alerts/{id}:ack":        evidence.AlertAck{},
		"GET /nexo/admin/access-review":           evidence.AccessReview{},
		"POST /nexo/admin/access-review:complete": evidence.ReviewRecord{},
		"GET /nexo/admin/access-review?format=json": evidence.ReviewExport{}, // SG-07
		"GET /nexo/authz/v1/me":                   nexoMeResponse{},
		"GET /nexo/authz/v1/platform/members":     nexoPage[authz.Member]{},
		"PUT|DELETE /nexo/authz/v1/*/members":     authz.BindingChange{},
		"POST|PATCH /nexo/authz/v1/scopes":        authz.ScopeInfo{},
		"GET /nexo/authz/v1/scopes/*/members":     nexoScopeMembersPage{},
		"GET|POST /nexo/authz/v1/tokens":          nexoTokenView{},
		// SG-10 (las respuestas de emisión y rotación llevan raw_token una
		// sola vez, como POST /nexo/authz/v1/tokens, y no entran aquí).
		"GET /nexo/authz/v1/service-accounts":                          nexoPage[authz.ServiceAccount]{},
		"POST|PATCH /nexo/authz/v1/service-accounts[/{sa}]":            authz.ServiceAccount{},
		"GET /nexo/authz/v1/service-accounts/{sa}":                     nexoSADetail{},
		"POST /nexo/authz/v1/service-accounts/{sa}:disable|enable":     nexoSAStateResponse{},
		"POST /nexo/authz/v1/service-accounts/{sa}/tokens/{id}:revoke": nexoSATokenRevokedResponse{},
		"/nexo/kb/v1/spaces":                                           domain.Space{},
		"GET /nexo/kb/v1/spaces/{slug}/events":                         domain.Event{},
		"POST /nexo/kb/v1/.../documents:batch":                         kbservice.BatchResult{},
		"GET /nexo/kb/v1/.../documents":                                domain.Document{},
		"GET /nexo/kb/v1/.../documents/{docKey}":                       kbservice.DocumentView{},
		"GET /nexo/kb/v1/.../versions":                                 domain.Version{},
		"GET /nexo/kb/v1/.../sections":                                 kbservice.SectionView{},
		"GET /nexo/kb/v1/search":                                       search.Result{},
		"GET /nexo/kb/v1/.../publish-runs":                             kbstore.PublishRun{},
		"/nexo/kb/v1/proposals":                                        kbstore.Proposal{},
		"POST /nexo/kb/v1/proposals/{id}:approve":                      kbservice.ApproveResult{},
		"GET /nexo/kb/v1/changes":                                      kbservice.ChangesResult{},
		"GET /nexo/kb/v1/sync":                                         kbservice.SyncResult{},
		"/nexo/kb/v1/eval-runs":                                        kbstore.EvalRun{},
		"GET /nexo/kb/v1/admin/index":                                  kbstore.IndexState{},
		"GET /nexo/kb/v1/admin/index (vector)":                         kbservice.VectorIndexState{},
		"GET /nexo/kb/v1/admin/index (rerank)":                         kbservice.RerankState{},
		"GET /nexo/kb/v1/search (rerank)":                              search.RerankInfo{},
		"POST /nexo/kb/v1/admin/index:reindex":                         kbservice.ReindexResult{},
	}
}

// nexoForbiddenJSONNames: fragmentos que no pueden aparecer en un nombre JSON
// de respuesta. Los hashes de la cadena de auditoría (prev_hash, last_hash) y
// las sumas de contenido (content_sha256) no son secretos y no coinciden.
var nexoForbiddenJSONNames = []string{
	"token_hash", "tokenhash", "password", "passwd", "secret", "pepper", "verifier",
	"cookie", "authorization", "api_key", "apikey", "private_key", "raw_token", "session_token",
}

func nexoForbiddenJSONName(name string) (string, bool) {
	lower := strings.ToLower(name)
	for _, f := range nexoForbiddenJSONNames {
		if strings.Contains(lower, f) {
			return f, true
		}
	}
	return "", false
}

func TestNexoResponseTypesHaveExplicitSafeFields(t *testing.T) {
	for route, v := range nexoResponseTypes() {
		var problems []string
		nexoCheckResponseType(reflect.TypeOf(v), reflect.TypeOf(v).String(), map[reflect.Type]bool{}, &problems)
		for _, p := range problems {
			t.Errorf("%s: %s", route, p)
		}
	}
}

func nexoCheckResponseType(t reflect.Type, path string, seen map[reflect.Type]bool, problems *[]string) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() == reflect.Map {
		nexoCheckResponseType(t.Elem(), path+"[]", seen, problems)
		return
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return
	}
	if t.PkgPath() == "time" {
		return
	}
	if t.PkgPath() == "database/sql" {
		*problems = append(*problems, path+": database/sql type "+t.String())
		return
	}
	seen[t] = true
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag, hasTag := f.Tag.Lookup("json")
		name := strings.Split(tag, ",")[0]
		if name == "-" && tag == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			nexoCheckResponseType(f.Type, path, seen, problems)
			continue
		}
		if !hasTag || name == "" {
			*problems = append(*problems, path+"."+f.Name+": exported field without explicit json name")
			name = f.Name
		}
		if bad, ok := nexoForbiddenJSONName(name); ok {
			*problems = append(*problems, path+"."+f.Name+": json name "+name+" looks like a secret ("+bad+")")
		}
		if ft := f.Type; ft.PkgPath() == "database/sql" {
			*problems = append(*problems, path+"."+f.Name+": database/sql type "+ft.String())
			continue
		}
		nexoCheckResponseType(f.Type, path+"."+name, seen, problems)
	}
}

func TestNexoResponseTypeCheckerCatchesLeaks(t *testing.T) {
	type leaky struct {
		ID        string `json:"id"`
		TokenHash string `json:"token_hash"`
		Untagged  string
	}
	var problems []string
	nexoCheckResponseType(reflect.TypeOf([]leaky{}), "leaky", map[reflect.Type]bool{}, &problems)
	if len(problems) != 2 {
		t.Fatalf("checker problems = %v, want the token_hash name and the untagged field", problems)
	}
}

// nexoAdminResponseKeys: claves permitidas en las respuestas JSON de /admin/*
// de Engram (contrato revisado). Si Engram agrega un campo, la prueba falla y
// hay que revisarlo antes de aceptarlo (RS-40).
var nexoAdminResponseKeys = map[string][]string{
	"user":  {"principal_id", "username", "email", "display_name", "role", "platform_role", "enabled", "created_at"},
	"token": {"id", "principal_id", "token_prefix", "name", "created_by_principal_id", "created_at", "last_used_at", "revoked_at", "revoked_by_principal_id", "revocation_reason", "expires_at", "scope"},
	"grant": {"principal_id", "project", "granted_by_principal_id", "created_at"},
}

func nexoCheckKeys(t *testing.T, label, kind string, obj map[string]any) {
	t.Helper()
	allowed := map[string]bool{}
	for _, k := range nexoAdminResponseKeys[kind] {
		allowed[k] = true
	}
	for k := range obj {
		if !allowed[k] {
			t.Errorf("%s: unreviewed %s field %q in the response", label, kind, k)
		}
	}
}

// S5-01 (CA-S13): todas las respuestas de la corrida, sin claves con nombre
// de secreto ni valores secretos (tokens, hashes, cabeceras Authorization),
// y las de /admin/* de Engram con el conjunto de campos revisado.
func TestNexoResponsesHaveNoSecrets(t *testing.T) {
	env := newNexoSecEnv(t, nil)
	f := runNexoSecurityFlow(t, env)
	var hashes []string
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
		hashes = append(hashes, h)
	}
	_ = rows.Close()
	if len(hashes) < 8 {
		t.Fatalf("la corrida debía emitir tokens: %d", len(hashes))
	}
	checked := 0
	for _, r := range f.responses {
		for _, leak := range f.scanner.find(string(r.body), r.allow...) {
			t.Errorf("%s (%d): %s", r.label, r.code, leak)
		}
		if strings.HasPrefix(r.ctype, "application/json") {
			checked++
			for _, k := range nexoForbiddenKeys(r.body) {
				if len(r.allow) > 0 && k == "$.raw_token (raw_token)" {
					continue // emisión: el token se entrega una sola vez
				}
				t.Errorf("%s: forbidden key %s", r.label, k)
			}
		}
	}
	if checked < 40 {
		t.Fatalf("respuestas JSON revisadas: %d", checked)
	}

	// Contrato de campos de /admin/*.
	ana := env.ids["ana"]
	var users []map[string]any
	nexoUnmarshal(t, env.req(http.MethodGet, "/admin/users", "carla", "", "10.95.0.1"), &users)
	for _, u := range users {
		nexoCheckKeys(t, "GET /admin/users", "user", u)
	}
	var tokens []map[string]any
	nexoUnmarshal(t, env.req(http.MethodGet, "/admin/users/"+ana+"/tokens", "carla", "", "10.95.0.1"), &tokens)
	if len(tokens) == 0 {
		t.Fatal("ana sin tokens")
	}
	for _, tok := range tokens {
		nexoCheckKeys(t, "GET /admin/users/{id}/tokens", "token", tok)
	}
	var grants []map[string]any
	nexoUnmarshal(t, env.req(http.MethodGet, "/admin/users/"+ana+"/grants", "carla", "", "10.95.0.1"), &grants)
	for _, g := range grants {
		nexoCheckKeys(t, "GET /admin/users/{id}/grants", "grant", g)
	}
	var created map[string]any
	nexoUnmarshal(t, env.req(http.MethodPost, "/admin/users/"+ana+"/tokens", "carla", `{"name":"contrato"}`, "10.95.0.1"), &created)
	if len(created) != 2 || created["raw_token"] == nil || created["token"] == nil {
		t.Fatalf("POST /admin/users/{id}/tokens: keys %v", created)
	}
	nexoCheckKeys(t, "POST /admin/users/{id}/tokens", "token", created["token"].(map[string]any))
}

func nexoUnmarshal(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if rec.Code >= 300 {
		t.Fatalf("%d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("JSON: %v (%s)", err, rec.Body.String())
	}
}

// Un 500 de las rutas Nexo no devuelve el detalle del error (SQL, tablas).
func TestNexoInternalErrorHidesDetail(t *testing.T) {
	rec := httptest.NewRecorder()
	nexoInternalError(rec, "nexo query", errors.New(`pq: relation "cloud_principal_tokens" column token_hash`))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "token_hash") || !strings.Contains(rec.Body.String(), "nexo query: internal error") {
		t.Fatalf("500: %d %s", rec.Code, rec.Body.String())
	}
}
