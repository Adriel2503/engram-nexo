package cloudserver

// NX-07 (revisión adversarial, punto 3): el historial con permiso de lectura del
// proyecto entregaba el título y el payload originales. Una memoria sincronizada
// con un egc_ o un nxa_ en el título, el contenido o campos anidados los daba al
// auditor. Ahora el título y el JSON del payload (claves y valores anidados) se
// limpian siempre, con o sin permiso, en la API y en el panel.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

func TestNexoHistoryRedactsTitleAndPayloadEvenWithProjectReadPG(t *testing.T) {
	c := newCanary(t)
	project := "hist-lectura"
	c.exec(`INSERT INTO nexo_scopes(scope_type,scope_key) VALUES('project',$1) ON CONFLICT DO NOTHING`, project)
	// luis es auditor y además lee el proyecto: ve título y payload, pero nunca una credencial.
	c.exec(`INSERT INTO nexo_role_bindings(principal_id,scope_type,scope_key,role) VALUES($1::bigint,'project',$2,'lector')`, c.e.ids["luis"], project)
	payload, _ := json.Marshal(map[string]any{
		"sync_id": "obs-lectura", "session_id": "s1", "type": "decision", "scope": "project", "project": project,
		"title":     "decisión con " + c.tok,
		"content":   "el código " + c.code + " va en el contenido",
		"anidado":   map[string]any{"clave " + c.tok: []any{"valor " + c.code, map[string]any{"profundo": "Bearer " + c.tok}}},
		"numero":    9007199254740993,
		"autorizar": "Authorization: Basic Zm9vOmJhcg==",
	})
	ctx := cloudstore.WithMutationActor(t.Context(), cloudstore.MutationActor{Name: "ana", PrincipalID: c.e.ids["ana"], Verified: true})
	if _, err := c.e.cs.InsertMutationBatch(ctx, []cloudstore.MutationEntry{{Project: project, Entity: "observation", EntityKey: "obs-lectura", Op: "upsert", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	rec := c.e.req("GET", "/nexo/admin/history/obs-lectura?include_payload=true", "luis", "", "10.77.7.1")
	if rec.Code != http.StatusOK {
		t.Fatalf("historial: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, secret := range []string{c.tok, c.code, "Zm9vOmJhcg=="} {
		if strings.Contains(body, secret) {
			t.Fatalf("el historial con project.read entregó una credencial: %s", c.snip(body, secret))
		}
	}
	for _, want := range []string{"decisión con", "va en el contenido", "9007199254740993", "[credencial oculta]", `"payload_withheld":false`} {
		if !strings.Contains(body, want) {
			t.Fatalf("falta %q en la respuesta: %s", want, body)
		}
	}
	panel := c.e.mustBody("luis", "/dashboard/admin/audit/history?key=obs-lectura", http.StatusOK)
	for _, secret := range []string{c.tok, c.code} {
		if strings.Contains(panel, secret) {
			t.Fatalf("el panel de historial entregó una credencial: %s", c.snip(panel, secret))
		}
	}
	// Sin permiso de lectura del proyecto: sin título ni payload.
	rec = c.e.req("GET", "/nexo/admin/history/obs-lectura?include_payload=true", "carla", "", "10.77.7.2")
	if strings.Contains(rec.Body.String(), "decisión con") || !strings.Contains(rec.Body.String(), `"payload_withheld":true`) {
		t.Fatalf("sin project.read no debe salir contenido: %s", rec.Body.String())
	}
}
