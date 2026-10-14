package cloudserver

// NX-07: una etiqueta de salida nunca identifica el ámbito autorizado.
import (
	"encoding/json"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"net/http"
	"strings"
	"testing"
)

func TestNexoScopeRedactionCollisionPG(t *testing.T) {
	c := newCanary(t)
	original := "nxa_" + strings.Repeat("a", 16)
	c.exec("INSERT INTO nexo_scopes(scope_type,scope_key) VALUES('project','credencial-oculta') ON CONFLICT DO NOTHING")
	c.exec("INSERT INTO nexo_role_bindings(principal_id,scope_type,scope_key,role) VALUES($1::bigint,'project','credencial-oculta','lector')", c.e.ids["luis"])
	const reserved = "contenido-del-proyecto-original"
	payload, _ := json.Marshal(map[string]any{"sync_id": "collision", "session_id": "s1", "type": "decision", "scope": "project", "project": original, "title": reserved, "content": reserved})
	if _, err := c.e.cs.InsertMutationBatch(t.Context(), []cloudstore.MutationEntry{{Project: original, Entity: "observation", EntityKey: "collision", Op: "upsert", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	c.exec("INSERT INTO cloud_auth_audit_log(actor_source,project,action,outcome,metadata) VALUES('test',$1,'collision','ok',jsonb_build_object('payload',$2::text))", original, reserved)
	c.exec("INSERT INTO nexo_authz_events(scope_type,scope_key,action,actor_label,details) VALUES('project',$1,'binding.granted','test',jsonb_build_object('payload',$2::text))", original, reserved)
	c.exec("INSERT INTO nexo_alerts(rule,severity,layer,subject,dedup_key,summary,details) VALUES('collision','info','proyectos','test','collision',$2,jsonb_build_object('project',$1::text,'title',$2::text))", original, reserved)
	// Filas que la ruta de escritura ya guardó con la etiqueta (sin el original): tampoco las autoriza quien lee el proyecto homónimo.
	const label = "[credencial oculta]"
	c.exec("INSERT INTO cloud_auth_audit_log(actor_source,project,action,outcome,metadata) VALUES('test',$1,'collision','ok',jsonb_build_object('payload',$2::text))", label, reserved)
	c.exec("INSERT INTO nexo_authz_events(scope_type,scope_key,action,actor_label,details) VALUES('project',$1,'binding.granted','test',jsonb_build_object('payload',$2::text))", label, reserved)
	c.exec("INSERT INTO nexo_alerts(rule,severity,layer,subject,dedup_key,summary,details) VALUES('collision','info','proyectos','test','collision-label',$2,jsonb_build_object('project',$1::text,'title',$2::text))", label, reserved)
	for _, path := range []string{
		"/nexo/admin/history/collision?include_payload=true", "/dashboard/admin/audit/history?key=collision",
		"/nexo/admin/audit?action=collision", "/dashboard/admin/audit?action=collision",
		"/nexo/admin/authz-events?action=binding.granted", "/nexo/authz/v1/events?action=binding.granted",
		"/nexo/admin/alerts?state=todas", "/dashboard/admin/audit/alerts?state=todas",
	} {
		t.Run(path, func(t *testing.T) {
			var body string
			if strings.HasPrefix(path, "/dashboard/") {
				body = c.e.mustBody("luis", path, http.StatusOK)
			} else {
				rec := c.e.req("GET", path, "luis", "", "10.77.7.3")
				if rec.Code != http.StatusOK {
					t.Fatalf("estado inesperado: %d", rec.Code)
				}
				body = rec.Body.String()
			}
			if strings.Contains(body, reserved) {
				t.Error("la lectura de la etiqueta redactada autorizó contenido del proyecto original")
			}
			if strings.Contains(body, original) {
				t.Error("sale el identificador con formato de credencial")
			}
		})
	}
	// El lector del ámbito real sí recibe contenido; el identificador sigue redactado.
	c.exec("INSERT INTO nexo_scopes(scope_type,scope_key) VALUES('project',$1) ON CONFLICT DO NOTHING", cloudstore.NormalizeProjectGrant(original))
	c.exec("INSERT INTO nexo_role_bindings(principal_id,scope_type,scope_key,role) VALUES($1::bigint,'project',$2,'lector')", c.e.ids["luis"], cloudstore.NormalizeProjectGrant(original))
	rec := c.e.req("GET", "/nexo/admin/history/collision?include_payload=true", "luis", "", "10.77.7.4")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), reserved) || strings.Contains(rec.Body.String(), original) {
		t.Error("el lector del proyecto original debe recibir contenido redactado")
	}
}
