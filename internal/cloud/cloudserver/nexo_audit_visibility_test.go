package cloudserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	kbstore "github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

func TestNexoAuditContentPerProjectPG(t *testing.T) {
	e := newAuditPanelEnv(t)
	ctx := t.Context()
	for _, project := range []string{"auditoria-a", "auditoria-b"} {
		if _, err := e.db.Exec("INSERT INTO nexo_scopes(scope_type,scope_key) VALUES('project',$1)", project); err != nil {
			t.Fatal("no se pudo crear proyecto")
		}
		payload, _ := json.Marshal(map[string]any{"sync_id": "misma-clave", "session_id": "sesion-prueba", "type": "decision", "scope": "project", "title": "Título reservado " + project, "content": "Contenido reservado " + project, "project": project})
		if _, err := e.cs.InsertMutationBatch(ctx, []cloudstore.MutationEntry{{Project: project, Entity: "observation", EntityKey: "misma-clave", Op: "upsert", Payload: payload}}); err != nil {
			t.Fatal("no se pudo crear mutación")
		}
	}
	path := "/nexo/admin/history/misma-clave?include_payload=true"
	for _, who := range []string{"luis", "pedro", "carla"} {
		r := e.req("GET", path, who, "", "10.78.1.1")
		if r.Code != 200 || strings.Contains(r.Body.String(), "reservado") {
			t.Fatalf("R-6: %s pudo leer contenido", who)
		}
		var page struct {
			Items []cloudstore.NexoHistoryRecord `json:"items"`
		}
		if json.Unmarshal(r.Body.Bytes(), &page) != nil || len(page.Items) != 2 {
			t.Fatal("historial inválido")
		}
		for _, row := range page.Items {
			if !row.PayloadWithheld || row.Title != "" || len(row.Payload) > 0 {
				t.Fatal("fila sin marca de retención")
			}
		}
	}
	if _, err := e.db.Exec("INSERT INTO nexo_role_bindings(principal_id,scope_type,scope_key,role) VALUES($1,'project','auditoria-a','lector')", e.ids["luis"]); err != nil {
		t.Fatal("no se pudo dar lectura")
	}
	r := e.req("GET", path, "luis-read", "", "10.78.1.1")
	var page struct {
		Items []cloudstore.NexoHistoryRecord `json:"items"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &page) != nil {
		t.Fatal("consulta fallida")
	}
	for _, row := range page.Items {
		if row.Project == "auditoria-a" {
			if row.Title == "" || len(row.Payload) == 0 || row.PayloadWithheld {
				t.Fatal("lector no ve su proyecto")
			}
		} else if row.Title != "" || len(row.Payload) > 0 || !row.PayloadWithheld {
			t.Fatal("se mezcló permiso entre proyectos")
		}
	}
	for _, who := range []string{"pedro", "carla"} {
		body := e.mustBody(who, "/dashboard/admin/audit/history?key=misma-clave", 200)
		if strings.Contains(body, "reservado") {
			t.Fatal("panel filtra título sin rol")
		}
	}
	body := e.mustBody("luis-read", "/dashboard/admin/audit/history?key=misma-clave", 200)
	if strings.Contains(body, "Contenido reservado") || strings.Contains(body, "Título reservado auditoria-b") || !strings.Contains(body, "Título reservado auditoria-a") {
		t.Fatal("panel viola acceso por fila")
	}
	e.assertNoLeaks()
}

func TestNexoAuditMetadataAndAlertsPG(t *testing.T) {
	e := newAuditPanelEnv(t)
	if _, err := e.db.Exec("INSERT INTO nexo_scopes(scope_type,scope_key) VALUES('space','evidencia-privada')"); err != nil {
		t.Fatal("no se pudo crear ámbito")
	}
	secret := map[string]any{"space": "evidencia-privada", "doc_key": "doc-1", "title": "Título reservado", "nested": map[string]any{"payload": "Contenido reservado"}}
	if err := e.cs.InsertAuthAuditEvent(t.Context(), cloudstore.AuthAuditEvent{ActorSource: "prueba", Action: "prueba.audit.content", Outcome: "success", Metadata: secret}); err != nil {
		t.Fatal("no se pudo registrar evento")
	}
	if _, err := evidence.InsertAlert(t.Context(), e.db, evidence.NewAlert{Rule: evidence.RuleKBDocExpiring, Layer: evidence.LayerKnowledge, Severity: evidence.SeverityInfo, Subject: "doc-1", DedupKey: "doc-1", Summary: "Título reservado", Details: secret}); err != nil {
		t.Fatal("no se pudo registrar alerta")
	}
	for _, who := range []string{"luis", "pedro", "carla"} {
		for _, url := range []string{"/nexo/admin/audit?action=prueba.audit.content", "/nexo/admin/alerts"} {
			r := e.req("GET", url, who, "", "10.78.1.1")
			if r.Code != 200 || strings.Contains(r.Body.String(), "reservado") {
				t.Fatal("auditoría o alerta filtra documento")
			}
		}
		for _, url := range []string{"/dashboard/admin/audit?action=prueba.audit.content", "/dashboard/admin/audit/alerts"} {
			body := e.mustBody(who, url, 200)
			if strings.Contains(body, "reservado") {
				t.Fatal("detalle de evento o alerta filtra documento")
			}
		}
	}
	if _, err := e.db.Exec("INSERT INTO nexo_role_bindings(principal_id,scope_type,scope_key,role) VALUES($1,'space','evidencia-privada','lector')", e.ids["luis"]); err != nil {
		t.Fatal("no se pudo dar lectura")
	}
	r := e.req("GET", "/nexo/admin/alerts", "luis-read", "", "10.78.1.1")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "Título reservado") {
		t.Fatal("lector del espacio no ve título")
	}
	e.assertNoLeaks()
}

func TestNexoAuditSearchDiagnosticsPG(t *testing.T) {
	e := newAuditPanelEnv(t)
	r := e.req("POST", "/nexo/kb/v1/spaces", "carla", `{"slug":"diagnostico-privado","name":"Diagnóstico","owner":"ana","visibility":"restricted"}`, "10.78.2.1")
	if r.Code != 201 {
		t.Fatalf("no se pudo crear ámbito: %d", r.Code)
	}
	var spaceID string
	if err := e.db.QueryRow("SELECT id::text FROM nexo_kb_spaces WHERE slug='diagnostico-privado'").Scan(&spaceID); err != nil {
		t.Fatal("ámbito no disponible")
	}
	if _, err := e.db.Exec(`INSERT INTO nexo_kb_index_versions(index_kind,version,status,config) VALUES('vector',99,'building','{}')`); err != nil {
		t.Fatal("índice no disponible")
	}
	if _, err := e.db.Exec(`INSERT INTO nexo_kb_index_dead(index_version,section_id,space_id,last_error,fail_count) VALUES(99,$1::uuid,$2::uuid,'Título reservado del documento',5)`, kbstore.NewID(), spaceID); err != nil {
		t.Fatal("diagnóstico no disponible")
	}
	if err := kbstore.InsertEvalRun(t.Context(), e.db, kbstore.EvalRun{ID: kbstore.NewID(), Retriever: "fts", Mode: "fts", IndexVersion: 1,
		ConfigSHA256: "prueba", QuestionSetSHA256: "prueba", QuestionSetName: "Título reservado de evaluación", Spaces: []string{"diagnostico-privado"}, Metrics: map[string]any{"questions": 1}, Results: map[string]any{"title": "Título reservado"}},
		kbstore.Actor{PrincipalID: e.ids["carla"], Label: "carla"}); err != nil {
		t.Fatal("evaluación no disponible")
	}
	path := "/dashboard/kb/search-status"
	body := e.mustBody("carla", path, 200)
	if strings.Contains(body, "Título reservado") {
		t.Fatal("diagnóstico filtra contenido por rol de plataforma")
	}
	if _, err := e.db.Exec("INSERT INTO nexo_role_bindings(principal_id,scope_type,scope_key,role) VALUES($1,'space','diagnostico-privado','lector')", e.ids["carla"]); err != nil {
		t.Fatal("no se pudo dar lectura")
	}
	body = e.mustBody("carla", path, 200)
	if !strings.Contains(body, "Título reservado") {
		t.Fatal("diagnóstico no respeta lectura del espacio")
	}
	e.assertNoLeaks()
}
