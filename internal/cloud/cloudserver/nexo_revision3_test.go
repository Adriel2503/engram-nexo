package cloudserver

// NX-07 · regresiones de las dos revisiones adversariales sobre develop
// (informe en NEXO_CHANGES.md): proyección de eventos de espacio, corridas de
// evaluación, origen de publicación y errores transaccionales de la activación.
// Postgres real.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

// Punto 1: settings={"cache":{"name":"texto reservado"}} se copiaba a los detalles de space.updated y la lista
// global de nombres lo dejaba pasar a un auditor sin space.read (API, exportaciones).
func TestNexoSpaceSettingsNeverReachAuditorEventsPG(t *testing.T) {
	c := newCanary(t)
	const reserved = "texto reservado del documento"
	slug := c.space()
	if rec := c.patch("ana", "/nexo/kb/v1/spaces/"+slug, map[string]any{"name": "Nombre " + reserved, "settings": map[string]any{"cache": map[string]any{"name": reserved}, "ttl": 5, "activo": true}}); rec.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", rec.Code, rec.Body)
	}
	rec := c.e.req("GET", "/nexo/kb/v1/spaces/"+slug+"/events?limit=50", "luis", "", "10.78.1.1") // auditor sin space.read
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), reserved) || strings.Contains(rec.Body.String(), `"ttl"`) || !strings.Contains(rec.Body.String(), `"payload_withheld":true`) {
		t.Fatalf("el auditor recibió ajustes del espacio: %d %s", rec.Code, rec.Body.String())
	}
	own := c.e.req("GET", "/nexo/kb/v1/spaces/"+slug+"/events?limit=50", "ana", "", "10.78.1.2") // responsable: ve el detalle
	if !strings.Contains(own.Body.String(), reserved) {
		t.Fatalf("el responsable debía ver el detalle: %s", own.Body.String())
	}
	c.scanExportsAndWebhook() // exportación diaria y de la CLI: el texto reservado no sale (scanBody lo comprueba por etiqueta)
}

// Punto 2: corridas de evaluación.
func TestNexoEvalRunsProjectionAndValidationPG(t *testing.T) {
	c := newCanary(t)
	slug := c.space()
	post := func(retriever, mode, commit, note string) *httpRecorder {
		return c.post("ana", "/nexo/kb/v1/eval-runs", map[string]any{
			"report": map[string]any{"retriever": retriever, "set": "conjunto-reservado", "set_sha256": strings.Repeat("b", 64), "questions": 1, "total": map[string]any{"hit_at_5": 1, "mrr": 1},
				"by_class": map[string]any{"clase-reservada": map[string]any{"hit_at_5": 1}}},
			"config": map[string]any{"clave-reservada": "valor-reservado"}, "mode": mode, "accepted": true, "commit": commit, "note": note, "spaces": []string{slug},
		})
	}
	if rec := post("pgfts", "fts", "abc1234", "nota-reservada"); rec.Code != http.StatusCreated {
		t.Fatalf("corrida válida: %d %s", rec.Code, rec.Body)
	}
	for name, rec := range map[string]*httpRecorder{
		"retriever libre":          post("texto reservado "+c.tok, "fts", "abc1234", "x"),
		"retriever con credencial": post("pgfts+"+c.code, "fts", "abc1234", "x"),
		"modo libre":               post("pgfts", "texto reservado", "abc1234", "x"),
		"commit libre":             post("pgfts", "fts", "texto reservado "+c.code, "x"),
		"commit con DSN":           post("pgfts", "fts", "postgres://u:p@db/x", "x"),
		"nota con DSN":             post("pgfts", "fts", "abc1234", "conectar con postgres://u:clave@db/x"),
	} {
		if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Errorf("%s: debía rechazarse (%d) %s", name, rec.Code, rec.Body)
		}
	}
	// Fila histórica con DSN en la nota y en el commit, y retriever/modo fuera de forma.
	c.exec(`INSERT INTO nexo_kb_eval_runs (id, retriever, mode, config, config_sha256, question_set_sha256, question_set_name, spaces, metrics, commit_ref, note, actor_label)
		VALUES (gen_random_uuid(), 'texto reservado libre', 'otro modo', '{"dsn":"postgres://u:clave@db/x"}'::jsonb, 'c', 'z', 'conjunto', ARRAY[$1::text], '{"hit_at_5":1}'::jsonb, 'postgres://u:clave@db/x', 'nota postgres://u:clave@db/x', 'autor')`, slug)
	// Un espacio abierto: luis tiene "algún espacio visible", requisito para listar corridas, pero no lee el espacio medido.
	c.post("carla", "/nexo/kb/v1/spaces", map[string]any{"slug": "abierto-eval", "owner": "ana"})
	out := c.e.req("GET", "/nexo/kb/v1/eval-runs", "luis", "", "10.78.2.1")
	body := out.Body.String()
	for _, hidden := range []string{"nota-reservada", "conjunto-reservado", "abc1234", "clase-reservada", "clave-reservada", "texto reservado libre", "otro modo", "clave@db", "autor"} {
		if strings.Contains(body, hidden) {
			t.Fatalf("la corrida ajena entregó %q sin lectura del espacio: %s", hidden, body)
		}
	}
	if !strings.Contains(body, `"retriever":"pgfts"`) || !strings.Contains(body, `"mode":"fts"`) {
		t.Fatalf("retriever y modo validados deben entregarse: %s", body)
	}
	own := c.e.req("GET", "/nexo/kb/v1/eval-runs", "ana", "", "10.78.2.2").Body.String() // con lectura: todo, sin credenciales ni DSN
	for _, want := range []string{"nota-reservada", "abc1234", "conjunto-reservado", "clave-reservada"} {
		if !strings.Contains(own, want) {
			t.Errorf("quien lee el espacio debía ver %q", want)
		}
	}
	if strings.Contains(own, "clave@db") {
		t.Fatalf("una cadena postgres:// llegó a quien lee el espacio: %s", own)
	}
	panel := c.e.mustBody("carla", "/dashboard/kb/search-status", http.StatusOK)
	if strings.Contains(panel, "texto reservado libre") || strings.Contains(panel, "otro modo") || strings.Contains(panel, "clave@db") {
		t.Fatalf("estado de la búsqueda entrega campos libres de corridas ajenas")
	}
}

// Punto 5: el origen definitivo se valida después de incorporar el source_url del front matter.
func TestNexoFrontMatterSourceURLWithCodeIsRejectedPG(t *testing.T) {
	c := newCanary(t)
	slug := c.space()
	rec := c.post("ana", "/nexo/kb/v1/spaces/"+slug+"/documents:batch", map[string]any{"documents": []any{map[string]any{
		"path": "guia.md", "content": "---\ncategory: lineamiento\nsource_url: https://ejemplo.test/?code=" + c.code + "\n---\n# Guía\n\ncontenido\n",
	}}})
	var out struct {
		Results []struct {
			Status string `json:"status"`
			Code   string `json:"code"`
		} `json:"results"`
	}
	_ = json.Unmarshal([]byte(rec.Body), &out)
	if len(out.Results) != 1 || out.Results[0].Status != "rejected" {
		t.Fatalf("un source_url con código debía rechazarse: %d %s", rec.Code, rec.Body)
	}
	for _, q := range []string{`SELECT count(*) FROM nexo_kb_versions WHERE position($1 in origin::text) > 0`, `SELECT count(*) FROM nexo_kb_events WHERE position($1 in details::text) > 0`,
		`SELECT count(*) FROM nexo_kb_publish_runs WHERE position($1 in origin::text || coalesce(rejected::text, '')) > 0`, `SELECT count(*) FROM nexo_kb_documents WHERE position($1 in origin_ref) > 0`} {
		if n := c.e.count(q, c.code); n != 0 {
			t.Fatalf("el código quedó guardado (%d): %s", n, q)
		}
	}
	// Con un enlace limpio sí se publica, y el origen tipado queda normalizado en el evento.
	ok := c.post("ana", "/nexo/kb/v1/spaces/"+slug+"/documents:batch", map[string]any{"documents": []any{map[string]any{
		"path": "guia.md", "content": "---\ncategory: lineamiento\nsource_url: https://ejemplo.test/guia\n---\n# Guía\n\ncontenido\n",
	}}})
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body, `"created"`) {
		t.Fatalf("publicación limpia: %d %s", ok.Code, ok.Body)
	}
}

// failingActivations devuelve un error de base de datos en cada operación de activación.
type failingActivations struct {
	ChunkStore
	err error
}

func (f *failingActivations) NexoIssueActivation(context.Context, string, string, string, string, cloudstore.AuthAuditEvent) (cloudstore.NexoActivation, error) {
	return cloudstore.NexoActivation{}, f.err
}
func (f *failingActivations) NexoPendingActivations(context.Context, string) ([]cloudstore.NexoActivation, error) {
	return nil, f.err
}
func (f *failingActivations) NexoRevokeActivation(context.Context, string, string, cloudstore.AuthAuditEvent) (bool, error) {
	return false, f.err
}
func (f *failingActivations) NexoRedeemActivation(context.Context, string, cloudstore.CreatePrincipalTokenParams, cloudstore.NexoRedeemCaller) (cloudstore.PrincipalToken, error) {
	return cloudstore.PrincipalToken{}, f.err
}

// Punto 9: el error real (SQLSTATE incluido) llega al registro redactado; la respuesta pública no cambia.
func TestNexoActivationInternalErrorsKeepSQLStateInLogPG(t *testing.T) {
	c := newCanary(t)
	logs := nexoCaptureLogs(t)
	original := c.e.srv.store
	defer func() { c.e.srv.store = original }()
	for _, tc := range []struct {
		name, state, msg string
		do               func() *httpRecorder
	}{
		{"emisión", "40P01", "deadlock detected", func() *httpRecorder {
			return c.post("carla", "/nexo/authz/v1/tokens", map[string]any{"principal": "ana", "name": "x"})
		}},
		{"pendientes", "42501", "permission denied for table nexo_activation_codes", func() *httpRecorder {
			return c.req("carla", "GET", "/nexo/authz/v1/activations?principal=ana", "")
		}},
		{"revocación", "40P01", "deadlock detected", func() *httpRecorder {
			return c.post("carla", "/nexo/authz/v1/activations/7/revoke", map[string]any{"principal": "ana"})
		}},
		{"canje", "40P01", "deadlock detected", func() *httpRecorder {
			return c.post("", "/nexo/authz/v1/activate", map[string]any{"code": c.code})
		}},
	} {
		c.e.srv.store = &failingActivations{ChunkStore: original, err: &pgconn.PgError{Code: tc.state, Message: tc.msg}}
		rec := tc.do()
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body, "internal error") || strings.Contains(rec.Body, tc.msg) || strings.Contains(rec.Body, tc.state) {
			t.Errorf("%s: la respuesta pública debía ser el 500 genérico: %d %s", tc.name, rec.Code, rec.Body)
		}
		if got := logs.String(); !strings.Contains(got, "sqlstate="+tc.state) || !strings.Contains(got, tc.msg) {
			t.Errorf("%s: el registro debía conservar la causa y el SQLSTATE %s", tc.name, tc.state)
		}
	}
	all := logs.String()
	if !strings.Contains(all, "emisión destinatario=") || !strings.Contains(all, "revocación código=7") || !strings.Contains(all, "consulta de pendientes destinatario=") {
		t.Errorf("el registro debía llevar la operación y los identificadores seguros: %s", all)
	}
	for _, secret := range []string{c.tok, c.code} {
		if strings.Contains(all, secret) {
			t.Fatal("el registro de errores contiene una credencial")
		}
	}
}
