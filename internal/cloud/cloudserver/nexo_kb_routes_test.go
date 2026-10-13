package cloudserver

// Nexo IA · NX-10 — tests HTTP de /nexo/kb/v1 a través del servidor real
// (withAuth, identidad de NX-05, puente nexo_kb_bridge.go) contra Postgres
// (CLOUDSTORE_TEST_DSN): 401/403/404/409/422/400, paginación por cursor,
// doc_key con "/" escapado, CA-K06 y CA-K13 por HTTP.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const kbSpace = "/nexo/kb/v1/spaces/lineamientos-frontend"

func kbJSON(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("json %q: %v", body, err)
	}
}

func TestNexoKBRoutesEndToEnd(t *testing.T) {
	e := newNexoAuthzEnv(t)

	// 401 sin token; 403 con token compartido (sin identidad asignada).
	e.do("GET", "/nexo/kb/v1/spaces", "", "", http.StatusUnauthorized)
	e.do("GET", "/nexo/kb/v1/spaces", "shared", "", http.StatusForbidden)

	// carla (superadmin) crea el espacio abierto con ana como responsable.
	rec := e.do("POST", "/nexo/kb/v1/spaces", "carla", `{"slug":"lineamientos-frontend","name":"Lineamientos de frontend","owner":"ana"}`, http.StatusCreated)
	var sp map[string]any
	kbJSON(t, rec.Body.String(), &sp)
	if sp["visibility"] != "open" || sp["default_role"] != "lector" {
		t.Fatalf("espacio: %v", sp)
	}
	e.do("POST", "/nexo/kb/v1/spaces", "carla", `{"slug":"lineamientos-frontend","owner":"ana"}`, http.StatusConflict)
	e.do("POST", "/nexo/kb/v1/spaces", "carla", `{"slug":"Mal Slug","owner":"ana"}`, http.StatusUnprocessableEntity)
	e.do("POST", "/nexo/kb/v1/spaces", "luis", `{"slug":"de-luis","owner":"ana"}`, http.StatusForbidden)
	e.do("POST", "/nexo/kb/v1/spaces", "carla", `{"slug":`, http.StatusBadRequest)

	// ana nombra publicadora a la cuenta de servicio (API de roles NX-05).
	e.do("PUT", "/nexo/authz/v1/scopes/space/lineamientos-frontend/members/"+e.ids["ci"], "ana", `{"role":"publicador"}`, http.StatusOK)

	// luis ve el espacio como lector.
	rec = e.do("GET", "/nexo/kb/v1/spaces", "luis", "", http.StatusOK)
	var list struct {
		Items []map[string]any `json:"items"`
	}
	kbJSON(t, rec.Body.String(), &list)
	if len(list.Items) != 1 || list.Items[0]["role"] != "lector" {
		t.Fatalf("listado de luis: %s", rec.Body.String())
	}

	// El CI publica un lote.
	batch := map[string]any{"documents": []map[string]any{
		{"path": "frontend/formularios.md", "content": "---\ncategory: lineamiento\ntitle: Formularios\n---\n# Formularios\n\n## Validación\n\nValidar en cliente y servidor con mensajes claros.\n",
			"origin": map[string]any{"repo": "realplaza/lineamientos", "commit": "abc123", "author": "ana@rp.pe"}},
		{"path": "glosario.md", "category": "glosario", "content": "# Glosario\n\nSSR: renderizado en el servidor.\n",
			"origin": map[string]any{"repo": "realplaza/lineamientos", "commit": "abc123"}},
	}}
	raw, _ := json.Marshal(batch)
	rec = e.do("POST", kbSpace+"/documents:batch", "ci", string(raw), http.StatusOK)
	var br struct {
		Results []map[string]any `json:"results"`
		Summary map[string]int   `json:"summary"`
	}
	kbJSON(t, rec.Body.String(), &br)
	if br.Summary["created"] != 2 {
		t.Fatalf("lote: %s", rec.Body.String())
	}
	// dry-run por parámetro y reintento idempotente.
	rec = e.do("POST", kbSpace+"/documents:batch?dry_run=true", "ci", string(raw), http.StatusOK)
	kbJSON(t, rec.Body.String(), &br)
	if br.Summary["unchanged"] != 2 {
		t.Fatalf("dry-run: %s", rec.Body.String())
	}

	// CA-K06: un lector no publica (403) y queda auditado con origen (CA-K13).
	e.do("POST", kbSpace+"/documents:batch", "luis", string(raw), http.StatusForbidden)
	var ip, ua string
	if err := e.db.QueryRow(`SELECT client_ip, user_agent FROM nexo_kb_events WHERE action = 'authz.denied' AND actor_principal_id = $1::bigint`, e.ids["luis"]).Scan(&ip, &ua); err != nil {
		t.Fatalf("denegación auditada: %v", err)
	}
	if ip != "10.0.0.15" || ua != "Go-http-client/1.1" {
		t.Fatalf("origen de la denegación: %s %s", ip, ua)
	}
	var token string
	if err := e.db.QueryRow(`SELECT actor_token_id::text FROM nexo_kb_events WHERE action = 'document.published' LIMIT 1`).Scan(&token); err != nil || token == "" {
		t.Fatalf("token en el evento de publicación: %q %v", token, err)
	}

	// Lectura con doc_key escapado, sección por ancla y por consulta.
	key := url.PathEscape("frontend/formularios")
	rec = e.do("GET", kbSpace+"/documents/"+key, "luis", "", http.StatusOK)
	var doc struct {
		Document map[string]any   `json:"document"`
		Version  map[string]any   `json:"version"`
		Sections []map[string]any `json:"sections"`
	}
	kbJSON(t, rec.Body.String(), &doc)
	if doc.Document["doc_key"] != "frontend/formularios" || len(doc.Sections) == 0 || !strings.Contains(doc.Version["content_md"].(string), "Validar") {
		t.Fatalf("documento: %s", rec.Body.String())
	}
	anchor := doc.Sections[0]["anchor"].(string)
	e.do("GET", kbSpace+"/documents/"+key+"/sections/"+url.PathEscape(anchor), "luis", "", http.StatusOK)
	e.do("GET", "/nexo/kb/v1/sections?space=lineamientos-frontend&doc="+url.QueryEscape("frontend/formularios")+"&anchor="+url.QueryEscape(anchor), "luis", "", http.StatusOK)
	e.do("GET", kbSpace+"/documents/"+key+"?version=9", "luis", "", http.StatusNotFound)
	e.do("GET", kbSpace+"/documents/"+key+"?version=abc", "luis", "", http.StatusBadRequest)
	e.do("GET", kbSpace+"/documents/"+key+"/versions", "luis", "", http.StatusOK)

	// Búsqueda.
	rec = e.do("GET", "/nexo/kb/v1/search?q="+url.QueryEscape("validacion cliente"), "luis", "", http.StatusOK)
	var sr struct {
		Results   []map[string]any `json:"results"`
		NoResults bool             `json:"no_results"`
	}
	kbJSON(t, rec.Body.String(), &sr)
	if sr.NoResults || sr.Results[0]["doc_key"] != "frontend/formularios" || sr.Results[0]["citation"] == "" || sr.Results[0]["source_location"] == nil {
		t.Fatalf("búsqueda: %s", rec.Body.String())
	}
	rec = e.do("GET", "/nexo/kb/v1/search?q=kubernetes", "luis", "", http.StatusOK)
	kbJSON(t, rec.Body.String(), &sr)
	if !sr.NoResults {
		t.Fatalf("no_results explícito: %s", rec.Body.String())
	}
	e.do("GET", "/nexo/kb/v1/search?q=x&at=ayer", "luis", "", http.StatusBadRequest)
	e.do("GET", "/nexo/kb/v1/search?q=", "luis", "", http.StatusUnprocessableEntity)

	// Paginación por cursor.
	rec = e.do("GET", kbSpace+"/documents?limit=1", "luis", "", http.StatusOK)
	var page struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"next_cursor"`
	}
	kbJSON(t, rec.Body.String(), &page)
	if len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatalf("página 1: %s", rec.Body.String())
	}
	first := page.Items[0]["doc_key"]
	rec = e.do("GET", kbSpace+"/documents?limit=1&cursor="+*page.NextCursor, "luis", "", http.StatusOK)
	kbJSON(t, rec.Body.String(), &page)
	if len(page.Items) != 1 || page.Items[0]["doc_key"] == first || page.NextCursor != nil {
		t.Fatalf("página 2: %s", rec.Body.String())
	}
	e.do("GET", kbSpace+"/documents?cursor=@@", "luis", "", http.StatusBadRequest)

	// Retiro y reemplazo.
	e.do("POST", kbSpace+"/documents/glosario:supersede", "ana", `{"by":"no-existe"}`, http.StatusNotFound)
	e.do("POST", kbSpace+"/documents/glosario:retire", "luis", `{}`, http.StatusForbidden)
	e.do("POST", kbSpace+"/documents/glosario:retire", "ana", `{"note":"ya no aplica"}`, http.StatusOK)
	e.do("POST", kbSpace+"/documents/glosario:retire", "ana", ``, http.StatusOK) // idempotente, sin evento nuevo
	if n := e.count(`SELECT COUNT(*) FROM nexo_kb_events WHERE action = 'document.retired'`); n != 1 {
		t.Fatalf("retiros registrados = %d", n)
	}
	e.do("POST", kbSpace+"/documents/glosario:supersede", "ana", `{"by":"frontend/formularios"}`, http.StatusOK)
	e.do("POST", kbSpace+"/documents/glosario:retire", "ana", ``, http.StatusConflict)
	e.do("POST", kbSpace+"/documents/glosario:borrar", "ana", ``, http.StatusNotFound)

	// Eventos: solo publicadores, responsables y plataforma.
	e.do("GET", kbSpace+"/events", "luis", "", http.StatusForbidden)
	rec = e.do("GET", kbSpace+"/events?limit=2", "ana", "", http.StatusOK)
	kbJSON(t, rec.Body.String(), &page)
	if len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("eventos paginados: %s", rec.Body.String())
	}

	// Espacio restringido: 404 para quien no es miembro.
	e.do("POST", "/nexo/kb/v1/spaces", "carla", `{"slug":"privado","visibility":"restricted","owner":"ana"}`, http.StatusCreated)
	e.do("GET", "/nexo/kb/v1/spaces/privado", "luis", "", http.StatusNotFound)
	e.do("GET", "/nexo/kb/v1/spaces/privado", "ana", "", http.StatusOK)
	e.do("PATCH", "/nexo/kb/v1/spaces/privado", "luis", `{"name":"x"}`, http.StatusNotFound)
	e.do("PATCH", "/nexo/kb/v1/spaces/privado", "ana", `{"name":"Privado","visibility":"open"}`, http.StatusOK)
	e.do("GET", "/nexo/kb/v1/spaces/privado", "luis", "", http.StatusOK)

	// Archivar y restaurar: solo plataforma.
	e.do("POST", "/nexo/kb/v1/spaces/privado:archive", "ana", "", http.StatusForbidden)
	e.do("POST", "/nexo/kb/v1/spaces/privado:archive", "carla", "", http.StatusOK)
	e.do("GET", "/nexo/kb/v1/spaces/privado", "ana", "", http.StatusForbidden)
	e.do("POST", "/nexo/kb/v1/spaces/privado:restore", "carla", "", http.StatusOK)
	e.do("POST", "/nexo/kb/v1/spaces/privado:borrar", "carla", "", http.StatusNotFound)
}
