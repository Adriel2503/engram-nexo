package cloudserver

// Escenarios de escritura del canario (nexo_canary_test.go): cada uno escribe
// texto libre por una ruta real del sistema. Los campos están nombrados con la
// etiqueta que aparece en el texto reservado, así una fuga dice de qué campo viene.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

// space crea un espacio restringido cuyo responsable es ana (nadie más tiene rol).
func (c *canary) space() string {
	slug := c.uniq("cs-")
	rec := c.post("carla", "/nexo/kb/v1/spaces", map[string]any{"slug": slug, "name": "Canario " + slug, "owner": "ana", "visibility": "restricted"})
	if rec.Code != http.StatusCreated {
		c.t.Fatalf("crear espacio %s: %d %s", slug, rec.Code, rec.Body)
	}
	return slug
}

// emailOf arma un correo válido con el valor del campo (sin espacios).
func emailOf(v string) string { return strings.NewReplacer(" ", "", "_", "-").Replace(v) + "@rp.pe" }

func canaryScenarios() []canaryScenario {
	return []canaryScenario{
		{"espacio: crear y configurar", []string{"space_name", "space_description", "space_category", "settings_value", "settings_key", "settings_nested_name"}, func(c *canary, v func(string) string) {
			slug := c.uniq("cn-")
			c.post("carla", "/nexo/kb/v1/spaces", map[string]any{"slug": slug, "name": v("space_name"), "description": v("space_description"), "owner": "ana", "visibility": "restricted", "categories": []string{"lineamiento", "glosario"}})
			c.patch("ana", "/nexo/kb/v1/spaces/"+slug, map[string]any{
				"name": v("space_name"), "description": v("space_description"),
				"settings": map[string]any{"cache": map[string]any{"name": v("settings_nested_name"), v("settings_key"): v("settings_value")}, "ttl": 5, "activo": true},
			})
			c.patch("ana", "/nexo/kb/v1/spaces/"+slug, map[string]any{"categories": []string{"lineamiento", "glosario", v("space_category")}})
		}},

		{"documentos: publicar, versionar y retirar", []string{"path", "doc_key", "title", "doc_owner", "doc_tag", "change_note", "origin_repo", "origin_branch", "origin_commit", "origin_path", "origin_author",
			"origin_approver", "origin_ci_run", "origin_filename", "origin_source", "origin_source_url", "origin_proposal_id", "origin_path_title", "fm_source_url", "fm_id", "retire_note"}, func(c *canary, v func(string) string) {
			slug := c.space()
			doc := func(body string) map[string]any {
				content := "---\ncategory: lineamiento\ntitle: " + v("title") + "\nsource_url: https://fuente.example/?q=" + strings.ReplaceAll(v("fm_source_url"), " ", "+") + "\nid: " + strings.ReplaceAll(v("fm_id"), " ", "-") + "\n---\n# Guia\n\n" + body + "\n"
				return map[string]any{
					"path": v("path") + ".md", "doc_key": v("doc_key"), "content": content, "owner": v("doc_owner"), "tags": []string{v("doc_tag")}, "change_note": v("change_note"),
					"origin": map[string]any{"repo": v("origin_repo"), "branch": v("origin_branch"), "commit": v("origin_commit"), "path": v("origin_path"), "author": v("origin_author"),
						"approver": v("origin_approver"), "ci_run": v("origin_ci_run"), "filename": v("origin_filename"), "source": v("origin_source"),
						"source_url": "https://fuente.example/?q=" + v("origin_source_url"), "proposal_id": v("origin_proposal_id"), "path_titles": []string{v("origin_path_title")}},
				}
			}
			c.post("ana", "/nexo/kb/v1/spaces/"+slug+"/documents:batch", map[string]any{"documents": []any{doc("Primera version del texto.")}})
			c.post("ana", "/nexo/kb/v1/spaces/"+slug+"/documents:batch", map[string]any{"documents": []any{doc("Segunda version del texto.")}})
			// Clave y ruta válidas fijas: el retiro y la reactivación escriben las notas.
			c.post("ana", "/nexo/kb/v1/spaces/"+slug+"/documents:batch", map[string]any{"documents": []any{map[string]any{"path": "fijo.md", "content": "---\ncategory: lineamiento\n---\n# Fijo\n\ncontenido fijo\n"}}})
			c.post("ana", "/nexo/kb/v1/spaces/"+slug+"/documents/fijo:retire", map[string]any{"note": v("retire_note")})
			c.post("ana", "/nexo/kb/v1/spaces/"+slug+"/documents/fijo:supersede", map[string]any{"by": v("doc_key"), "note": v("retire_note")})
		}},

		{"propuestas: crear, aprobar, descartar y retirar", []string{"proposal_title", "proposal_content", "proposal_rationale", "proposal_tag", "proposal_doc_key", "approve_note", "discard_reason", "withdraw_reason"}, func(c *canary, v func(string) string) {
			slug := c.space()
			create := func() string {
				rec := c.post("ana", "/nexo/kb/v1/spaces/"+slug+"/proposals", map[string]any{"doc_key": v("proposal_doc_key"), "title": v("proposal_title"), "category": "lineamiento",
					"tags": []string{v("proposal_tag")}, "content": "# Propuesta\n\n" + v("proposal_content") + "\n", "rationale": v("proposal_rationale")})
				var out struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal([]byte(rec.Body), &out)
				return out.ID
			}
			if id := create(); id != "" {
				c.post("ana", "/nexo/kb/v1/proposals/"+id+":approve", map[string]any{"note": v("approve_note"), "force": true})
			}
			if id := create(); id != "" {
				c.post("ana", "/nexo/kb/v1/proposals/"+id+":discard", map[string]any{"reason": v("discard_reason")})
			}
			if id := create(); id != "" {
				c.post("ana", "/nexo/kb/v1/proposals/"+id+":withdraw", map[string]any{"reason": v("withdraw_reason")})
			}
		}},

		{"evaluación: registrar corridas", []string{"eval_set", "eval_class", "eval_cfg_key", "eval_cfg_value", "eval_cfg_nested", "eval_note", "eval_question"}, func(c *canary, v func(string) string) {
			slug := c.space()
			body := func(retriever, mode, commit string) map[string]any {
				return map[string]any{
					"report": map[string]any{"retriever": retriever, "set": v("eval_set"), "set_sha256": strings.Repeat("a", 64), "questions": 1,
						"total":    map[string]any{"hit_at_5": 1, "mrr": 1},
						"by_class": map[string]any{v("eval_class"): map[string]any{"hit_at_5": 1}},
						"results":  []any{map[string]any{"id": "q1", "question": v("eval_question")}}},
					"config": map[string]any{v("eval_cfg_key"): v("eval_cfg_value"), "nested": map[string]any{"name": v("eval_cfg_nested")}, "ttl": 3},
					"mode":   mode, "accepted": true, "commit": commit, "note": v("eval_note"), "spaces": []string{slug},
				}
			}
			c.post("ana", "/nexo/kb/v1/eval-runs", body("pgfts", "fts", "abc1234"))
			// Enumeraciones y referencia: un valor cualquiera se rechaza al escribir.
			c.post("ana", "/nexo/kb/v1/eval-runs", body(v("eval_retriever"), "fts", "abc1234"))
			c.post("ana", "/nexo/kb/v1/eval-runs", body("pgfts", v("eval_mode"), "abc1234"))
			c.post("ana", "/nexo/kb/v1/eval-runs", body("pgfts", "fts", v("eval_commit")))
		}},

		{"personas y tokens", []string{"username", "email", "display_name", "token_name", "activation_name", "revoke_reason", "grant_project"}, func(c *canary, v func(string) string) {
			name := strings.ToLower(c.uniq("px"))
			rec := c.post("carla", "/admin/users", map[string]any{"username": strings.ReplaceAll(v("username"), " ", "_") + name, "email": emailOf(v("email") + name), "display_name": v("display_name"), "role": "member"})
			var u struct {
				PrincipalID string `json:"principal_id"`
				ID          string `json:"id"`
			}
			_ = json.Unmarshal([]byte(rec.Body), &u)
			// Persona fija: tokens con nombre, motivo de revocación, proyecto de un permiso.
			c.post("carla", "/nexo/authz/v1/tokens", map[string]any{"principal": "carla", "name": v("token_name")})
			c.post("carla", "/nexo/authz/v1/tokens", map[string]any{"principal": "ana", "name": v("activation_name")})
			own := c.post("pedro", "/nexo/authz/v1/tokens", map[string]any{"principal": "pedro", "name": v("token_name")})
			var t struct {
				Token struct {
					ID string `json:"id"`
				} `json:"token"`
			}
			_ = json.Unmarshal([]byte(own.Body), &t)
			if t.Token.ID != "" {
				c.post("pedro", "/admin/tokens/"+t.Token.ID+"/revoke", map[string]any{"reason": v("revoke_reason")})
			}
			c.post("carla", "/admin/users/"+c.e.ids["ana"]+"/grants", map[string]any{"project": strings.ReplaceAll(v("grant_project"), " ", "-")})
			if pid := firstNonEmpty(u.PrincipalID, u.ID); pid != "" {
				c.post("carla", "/admin/users/"+pid+"/disable", map[string]any{})
			}
		}},

		{"cuentas de servicio", []string{"sa_display_name", "sa_description", "sa_description_2", "sa_token_name", "sa_revoke_reason"}, func(c *canary, v func(string) string) {
			slug := c.uniq("ci-")
			c.post("carla", "/nexo/authz/v1/service-accounts", map[string]any{"slug": slug, "display_name": v("sa_display_name"), "description": v("sa_description"), "owner": "ana"})
			c.patch("carla", "/nexo/authz/v1/service-accounts/"+slug, map[string]any{"description": v("sa_description_2")})
			iss := c.post("carla", "/nexo/authz/v1/service-accounts/"+slug+"/tokens", map[string]any{"name": v("sa_token_name")})
			var t struct {
				Token struct {
					ID string `json:"id"`
				} `json:"token"`
			}
			_ = json.Unmarshal([]byte(iss.Body), &t)
			if t.Token.ID != "" {
				c.post("carla", "/nexo/authz/v1/service-accounts/"+slug+"/tokens/"+t.Token.ID+":revoke", map[string]any{"reason": v("sa_revoke_reason")})
			}
		}},

		{"alertas y revisión de accesos", []string{"alert_title", "alert_owner", "alert_summary", "ack_note", "review_notes"}, func(c *canary, v func(string) string) {
			slug := c.space()
			a, err := evidence.InsertAlert(c.t.Context(), c.e.db, evidence.NewAlert{Rule: evidence.RuleKBDocExpiring, Layer: evidence.LayerKnowledge, Severity: evidence.SeverityInfo,
				Subject: "space:" + slug + "/doc:d1", DedupKey: c.uniq("canary-alert-"), Summary: v("alert_summary"),
				Details: map[string]any{"space": slug, "doc_key": "d1", "title": v("alert_title"), "owner": v("alert_owner")}})
			if err != nil || a == nil {
				c.t.Fatalf("alerta: %v", err)
			}
			c.post("luis", fmt.Sprintf("/nexo/admin/alerts/%d:ack", a.ID), map[string]any{"note": v("ack_note")})
			c.post("luis", "/nexo/admin/access-review:complete", map[string]any{"notes": v("review_notes")})
		}},

		{"historial de memorias y conexiones", []string{"mut_title", "mut_content", "mut_nested_key", "mut_nested_value", "mut_session", "claim_name", "client_claim", "user_agent", "presence_project"}, func(c *canary, v func(string) string) {
			project := c.uniq("cproj-")
			c.exec(`INSERT INTO nexo_scopes(scope_type,scope_key) VALUES('project',$1) ON CONFLICT DO NOTHING`, project)
			c.exec(`INSERT INTO nexo_role_bindings(principal_id,scope_type,scope_key,role) VALUES($1::bigint,'project',$2,'responsable') ON CONFLICT DO NOTHING`, c.e.ids["ana"], project)
			payload, _ := json.Marshal(map[string]any{"sync_id": "obs-canary", "session_id": "s-" + project, "type": "decision", "scope": "project", "project": project,
				"title": v("mut_title"), "content": v("mut_content"), "extra": map[string]any{v("mut_nested_key"): v("mut_nested_value")}})
			ctx := cloudstore.WithMutationActor(c.t.Context(), cloudstore.MutationActor{Name: v("claim_name"), PrincipalID: c.e.ids["ana"], Verified: true, ClientClaim: v("client_claim")})
			if _, err := c.e.cs.InsertMutationBatch(ctx, []cloudstore.MutationEntry{{Project: project, Entity: "observation", EntityKey: "obs-canary", Op: "upsert", Payload: payload}}); err != nil {
				c.t.Logf("mutación rechazada (válido): %v", err)
			}
			if err := c.e.cs.RecordActivity(c.t.Context(), cloudstore.ActivityEvent{Actor: cloudstore.MutationActor{Name: v("claim_name"), PrincipalID: c.e.ids["ana"], Verified: true, ClientClaim: v("client_claim")},
				Operation: cloudstore.NexoOpPullMutations, Project: v("presence_project"), UserAgent: v("user_agent"), LogDetail: true}); err != nil {
				c.t.Logf("actividad rechazada (válido): %v", err)
			}
			// Fallos de autenticación con User-Agent y credencial inválida: eventos de seguridad.
			nexoDoFrom(c.t, c.e.h, "GET", "/nexo/authz/v1/me", c.tok, "", "10.99.9.9:40000", "", v("user_agent"))
			nexoDoFrom(c.t, c.e.h, "POST", "/nexo/authz/v1/activate", "", `{"code":"`+c.code+`"}`, "10.99.9.8:40000", "", v("user_agent"))
			nexoDoFrom(c.t, c.e.h, "GET", "/nexo/admin/audit", c.e.tokens["ana"], "", "10.99.9.7:40000", "", v("user_agent"))
			nexoDoFrom(c.t, c.e.h, "GET", "/nexo/admin/audit", c.e.tokens["ci"], "", "10.99.9.6:40000", "", v("user_agent")) // la cuenta de servicio deja su último uso con ese User-Agent
		}},
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

var _ = time.Now
