package cloudserver

// Cobertura del canario: el canario falla hasta cubrir un campo nuevo.
//
//   - TestCanaryClassifiesEveryTextColumn: toda columna de texto (text, varchar,
//     json, jsonb, arreglos) de las tablas del esquema está clasificada aquí. Una
//     columna nueva rompe la prueba hasta que alguien decida si admite texto
//     externo (y entonces la escribe un escenario del canario o las filas
//     históricas) o si no lo admite (y dice por qué).
//   - TestCanaryCoversEveryGETRoute: toda ruta GET registrada la recorre el
//     canario o está en canaryUncheckedRoutes con su motivo.

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// canaryColumns clasifica cada columna de texto: "escenario: <nombre>" la escribe un escenario del canario,
// "histórica" la escriben las filas históricas por SQL, "exento: <motivo>" no admite texto externo.
var canaryColumns = map[string]string{
	// Auditoría de seguridad y tokens.
	"cloud_auth_audit_log.action":              "exento: constante del servidor",
	"cloud_auth_audit_log.actor_source":        "exento: constante del servidor",
	"cloud_auth_audit_log.metadata":            "escenario: personas y tokens; historial de memorias y conexiones; cuentas de servicio",
	"cloud_auth_audit_log.outcome":             "exento: constante del servidor",
	"cloud_auth_audit_log.project":             "escenario: personas y tokens (grant_project)",
	"cloud_auth_audit_log.reason_code":         "exento: código del servidor",
	"cloud_human_users.email":                  "escenario: personas y tokens",
	"cloud_human_users.username":               "escenario: personas y tokens",
	"cloud_principal_tokens.name":              "escenario: personas y tokens; cuentas de servicio",
	"cloud_principal_tokens.nexo_scope":        "exento: enumeración read|write validada",
	"cloud_principal_tokens.revocation_reason": "escenario: personas y tokens; cuentas de servicio",
	"cloud_principal_tokens.token_hash":        "exento: hash calculado por el servidor",
	"cloud_principal_tokens.token_prefix":      "exento: prefijo generado por el servidor",
	"cloud_principals.display_name":            "escenario: personas y tokens; cuentas de servicio",
	"cloud_principals.kind":                    "exento: enumeración",
	"cloud_principals.role":                    "exento: enumeración",
	"cloud_project_grants.project":             "escenario: personas y tokens (grant_project)",
	"nexo_activation_codes.code_hash":          "exento: hash calculado por el servidor",
	"nexo_activation_codes.issued_ip":          "exento: IP de la conexión",
	"nexo_activation_codes.name":               "escenario: personas y tokens (activation_name)",
	"nexo_activation_codes.scope":              "exento: enumeración read|write",
	"nexo_service_accounts.description":        "escenario: cuentas de servicio",
	"nexo_service_accounts.slug":               "exento: nombre corto validado (letras, dígitos y guiones)",

	// Memorias sincronizadas (contenido de Engram, sin vista de administración salvo el historial).
	"cloud_chunks.actor":                               "exento: sincronización de Engram (sin salida de administración)",
	"cloud_chunks.actor_client_claim":                  "exento: sincronización de Engram (sin salida de administración)",
	"cloud_chunks.actor_email":                         "exento: sincronización de Engram (sin salida de administración)",
	"cloud_chunks.actor_principal_id":                  "exento: id del servidor",
	"cloud_chunks.actor_token_id":                      "exento: id del servidor",
	"cloud_chunks.chunk_id":                            "exento: hash del contenido",
	"cloud_chunks.created_by":                          "exento: sincronización de Engram (sin salida de administración)",
	"cloud_chunks.payload":                             "exento: contenido de proyecto de Engram: solo con permiso de proyecto por /sync",
	"cloud_chunks.project_name":                        "exento: sincronización de Engram (sin salida de administración)",
	"cloud_mutations.actor":                            "escenario: historial de memorias y conexiones",
	"cloud_mutations.actor_client_claim":               "escenario: historial de memorias y conexiones",
	"cloud_mutations.actor_email":                      "escenario: historial de memorias y conexiones",
	"cloud_mutations.actor_principal_id":               "exento: id del servidor",
	"cloud_mutations.actor_token_id":                   "exento: id del servidor",
	"cloud_mutations.entity":                           "exento: enumeración de entidades de Engram",
	"cloud_mutations.entity_key":                       "escenario: historial de memorias y conexiones",
	"cloud_mutations.op":                               "exento: enumeración",
	"cloud_mutations.payload":                          "escenario: historial de memorias y conexiones",
	"cloud_mutations.project":                          "escenario: historial de memorias y conexiones",
	"nexo_mutation_history.actor":                      "escenario: historial de memorias y conexiones",
	"nexo_mutation_history.actor_client_claim":         "escenario: historial de memorias y conexiones",
	"nexo_mutation_history.actor_email":                "escenario: historial de memorias y conexiones",
	"nexo_mutation_history.actor_principal_id":         "exento: id del servidor",
	"nexo_mutation_history.actor_token_id":             "exento: id del servidor",
	"nexo_mutation_history.current_email":              "escenario: personas y tokens",
	"nexo_mutation_history.entity":                     "exento: enumeración de entidades de Engram",
	"nexo_mutation_history.entity_key":                 "escenario: historial de memorias y conexiones",
	"nexo_mutation_history.op":                         "exento: enumeración",
	"nexo_mutation_history.project":                    "escenario: historial de memorias y conexiones",
	"nexo_mutation_history.title":                      "escenario: historial de memorias y conexiones",
	"nexo_mutation_history.token_name":                 "escenario: personas y tokens",
	"nexo_mutation_history.token_prefix":               "exento: prefijo generado por el servidor",
	"nexo_mutation_history.username":                   "escenario: personas y tokens",
	"cloud_project_sessions.project_name":              "exento: índice de sesiones de Engram (sin salida de administración)",
	"cloud_project_sessions.session_id":                "exento: índice de sesiones de Engram (sin salida de administración)",
	"cloud_prompt_pair_claims.claimed_by":              "exento: sincronización de Engram (sin salida de administración)",
	"cloud_prompt_pair_claims.prompt_project":          "exento: sincronización de Engram (sin salida de administración)",
	"cloud_prompt_pair_claims.session_id":              "exento: sincronización de Engram (sin salida de administración)",
	"cloud_prompt_pair_claims.source_inbox_id":         "exento: sincronización de Engram (sin salida de administración)",
	"cloud_prompt_pair_claims.sync_id":                 "exento: sincronización de Engram (sin salida de administración)",
	"cloud_prompt_source_attestations.actor_id":        "exento: sincronización de Engram (sin salida de administración)",
	"cloud_prompt_source_attestations.owner_project":   "exento: sincronización de Engram (sin salida de administración)",
	"cloud_prompt_source_attestations.prompt_project":  "exento: sincronización de Engram (sin salida de administración)",
	"cloud_prompt_source_attestations.session_id":      "exento: sincronización de Engram (sin salida de administración)",
	"cloud_prompt_source_attestations.source_inbox_id": "exento: sincronización de Engram (sin salida de administración)",
	"cloud_prompt_source_attestations.sync_id":         "exento: sincronización de Engram (sin salida de administración)",
	"cloud_session_authority.owner_project":            "exento: sincronización de Engram (sin salida de administración)",
	"cloud_session_authority.registered_by":            "exento: sincronización de Engram (sin salida de administración)",
	"cloud_session_authority.session_id":               "exento: sincronización de Engram (sin salida de administración)",
	"cloud_users.email":                                "exento: tabla heredada de Engram sin uso en Nexo",
	"cloud_users.password_hash":                        "exento: hash",
	"cloud_users.username":                             "exento: tabla heredada de Engram sin uso en Nexo",

	// Tablero de Engram (no configurado en las pruebas de servidor): la escritura redacta y se prueba en cloudstore.
	"cloud_project_controls.paused_reason": "histórica: redacción en SetProjectSyncEnabled y en la lectura (cloudstore, TestNexoEngramAdminTextRedacted)",
	"cloud_project_controls.project":       "exento: nombre de proyecto de Engram (identificador)",
	"cloud_project_controls.updated_by":    "histórica: redacción en SetProjectSyncEnabled y en la lectura (cloudstore, TestNexoEngramAdminTextRedacted)",
	"cloud_sync_audit_log.action":          "exento: constante del servidor",
	"cloud_sync_audit_log.contributor":     "histórica: redacción en InsertAuditEntry y en la lectura (cloudstore, TestNexoEngramAdminTextRedacted)",
	"cloud_sync_audit_log.metadata":        "histórica: redacción en InsertAuditEntry y en la lectura (cloudstore, TestNexoEngramAdminTextRedacted)",
	"cloud_sync_audit_log.outcome":         "exento: constante del servidor",
	"cloud_sync_audit_log.project":         "exento: nombre de proyecto de Engram (identificador)",
	"cloud_sync_audit_log.reason_code":     "exento: código del servidor",

	// Evidencia.
	"nexo_access_reviews.client_ip":           "exento: IP de la conexión",
	"nexo_access_reviews.notes":               "escenario: alertas y revisión de accesos",
	"nexo_access_reviews.report_sha256":       "exento: hash",
	"nexo_access_reviews.reviewer_label":      "histórica",
	"nexo_access_reviews.summary":             "histórica: solo contadores (NexoStreamReviewSummary)",
	"nexo_access_reviews.user_agent":          "escenario: historial de memorias y conexiones; histórica",
	"nexo_activity_log.actor":                 "escenario: historial de memorias y conexiones",
	"nexo_activity_log.actor_email":           "escenario: historial de memorias y conexiones",
	"nexo_activity_log.actor_principal_id":    "exento: id del servidor",
	"nexo_activity_log.actor_token_id":        "exento: id del servidor",
	"nexo_activity_log.client_claim":          "escenario: historial de memorias y conexiones",
	"nexo_activity_log.client_ip":             "exento: IP de la conexión",
	"nexo_activity_log.operation":             "exento: enumeración de operaciones",
	"nexo_activity_log.project":               "escenario: historial de memorias y conexiones",
	"nexo_activity_log.user_agent":            "escenario: historial de memorias y conexiones",
	"nexo_actor_presence.actor":               "escenario: historial de memorias y conexiones",
	"nexo_actor_presence.actor_email":         "escenario: historial de memorias y conexiones",
	"nexo_actor_presence.actor_principal_id":  "exento: id del servidor",
	"nexo_actor_presence.actor_token_id":      "exento: id del servidor",
	"nexo_actor_presence.last_client_claim":   "escenario: historial de memorias y conexiones",
	"nexo_actor_presence.last_ip":             "exento: IP de la conexión",
	"nexo_actor_presence.last_project":        "escenario: historial de memorias y conexiones",
	"nexo_actor_presence.last_user_agent":     "escenario: historial de memorias y conexiones",
	"nexo_actor_presence.presence_key":        "exento: clave compuesta de ids del servidor",
	"nexo_alert_acks.acknowledged_by_label":   "histórica",
	"nexo_alert_acks.client_ip":               "exento: IP de la conexión",
	"nexo_alert_acks.note":                    "escenario: alertas y revisión de accesos",
	"nexo_alert_acks.user_agent":              "escenario: historial de memorias y conexiones; histórica",
	"nexo_alerts.dedup_key":                   "exento: compuesta por el servidor",
	"nexo_alerts.details":                     "escenario: alertas y revisión de accesos; histórica",
	"nexo_alerts.layer":                       "exento: enumeración",
	"nexo_alerts.rule":                        "exento: enumeración de reglas",
	"nexo_alerts.severity":                    "exento: enumeración",
	"nexo_alerts.subject":                     "escenario: alertas y revisión de accesos; histórica",
	"nexo_alerts.summary":                     "escenario: alertas y revisión de accesos",
	"nexo_audit_chain_anchors.details":        "exento: la escribe la cadena de hashes (valores calculados)",
	"nexo_audit_chain_anchors.kind":           "exento: enumeración",
	"nexo_audit_chain_anchors.table_name":     "exento: nombre de tabla",
	"nexo_audit_chain_checkpoints.source":     "exento: enumeración",
	"nexo_audit_chain_checkpoints.table_name": "exento: nombre de tabla",
	"nexo_audit_exports.created_by":           "exento: etiqueta fija de la exportación",
	"nexo_audit_exports.file_name":            "exento: ruta calculada por el servidor",
	"nexo_audit_exports.file_sha256":          "exento: hash",
	"nexo_audit_exports.hmac_sha256":          "exento: hash",
	"nexo_audit_exports.key_id":               "exento: huella de la clave",
	"nexo_audit_exports.last_hash":            "exento: hash",
	"nexo_audit_exports.table_name":           "exento: nombre de tabla",
	"nexo_authz_events.action":                "exento: constante del servidor",
	"nexo_authz_events.actor_email":           "escenario: personas y tokens",
	"nexo_authz_events.actor_label":           "escenario: personas y tokens; histórica",
	"nexo_authz_events.client_claim":          "histórica",
	"nexo_authz_events.client_ip":             "exento: IP de la conexión",
	"nexo_authz_events.details":               "escenario: espacio; cuentas de servicio; histórica",
	"nexo_authz_events.role_after":            "exento: enumeración de roles",
	"nexo_authz_events.role_before":           "exento: enumeración de roles",
	"nexo_authz_events.scope_key":             "exento: identificador de ámbito validado",
	"nexo_authz_events.scope_type":            "exento: enumeración",
	"nexo_authz_events.user_agent":            "escenario: historial de memorias y conexiones; histórica",
	"nexo_backfill_marks.name":                "exento: marca interna",
	"nexo_role_bindings.role":                 "exento: enumeración de roles",
	"nexo_role_bindings.scope_key":            "exento: identificador de ámbito validado",
	"nexo_role_bindings.scope_type":           "exento: enumeración",
	"nexo_scopes.default_role":                "exento: enumeración de roles",
	"nexo_scopes.scope_key":                   "exento: identificador de ámbito validado",
	"nexo_scopes.scope_type":                  "exento: enumeración",
	"nexo_scopes.status":                      "exento: enumeración",
	"nexo_scopes.visibility":                  "exento: enumeración",

	// Conocimiento.
	"nexo_kb_changes.kind":                       "exento: enumeración",
	"nexo_kb_documents.category":                 "escenario: documentos",
	"nexo_kb_documents.doc_key":                  "escenario: documentos",
	"nexo_kb_documents.origin_kind":              "exento: enumeración",
	"nexo_kb_documents.origin_ref":               "escenario: documentos; histórica",
	"nexo_kb_documents.owner_label":              "escenario: documentos; histórica",
	"nexo_kb_documents.retirement_note":          "escenario: documentos; histórica",
	"nexo_kb_documents.status":                   "exento: enumeración",
	"nexo_kb_documents.tags":                     "escenario: documentos; histórica",
	"nexo_kb_documents.title":                    "escenario: documentos; histórica",
	"nexo_kb_eval_runs.actor_label":              "escenario: evaluación; histórica",
	"nexo_kb_eval_runs.by_class":                 "escenario: evaluación; histórica",
	"nexo_kb_eval_runs.commit_ref":               "escenario: evaluación; histórica",
	"nexo_kb_eval_runs.config":                   "escenario: evaluación; histórica",
	"nexo_kb_eval_runs.config_sha256":            "exento: hash",
	"nexo_kb_eval_runs.metrics":                  "escenario: evaluación; histórica",
	"nexo_kb_eval_runs.mode":                     "escenario: evaluación; histórica",
	"nexo_kb_eval_runs.note":                     "escenario: evaluación; histórica",
	"nexo_kb_eval_runs.question_set_name":        "escenario: evaluación; histórica",
	"nexo_kb_eval_runs.question_set_sha256":      "exento: hash",
	"nexo_kb_eval_runs.results":                  "escenario: evaluación; histórica",
	"nexo_kb_eval_runs.retriever":                "escenario: evaluación; histórica",
	"nexo_kb_eval_runs.spaces":                   "exento: identificadores de espacio validados",
	"nexo_kb_events.action":                      "exento: constante del servidor",
	"nexo_kb_events.actor_email":                 "escenario: personas y tokens",
	"nexo_kb_events.actor_label":                 "histórica",
	"nexo_kb_events.client_claim":                "histórica",
	"nexo_kb_events.client_ip":                   "exento: IP de la conexión",
	"nexo_kb_events.details":                     "escenario: espacio; documentos; propuestas; evaluación; histórica",
	"nexo_kb_events.space_slug":                  "exento: identificador de espacio validado",
	"nexo_kb_events.target_id":                   "histórica: solo identificadores sin lectura (NexoIDOrEmpty)",
	"nexo_kb_events.target_kind":                 "exento: constante del servidor",
	"nexo_kb_events.user_agent":                  "histórica",
	"nexo_kb_index_dead.last_error":              "histórica: error del proveedor de embeddings; se retiene sin lectura (nexoDeadLetterVisible)",
	"nexo_kb_index_dead.payload":                 "histórica",
	"nexo_kb_index_jobs.claimed_by":              "exento: identificador del proceso indexador",
	"nexo_kb_index_jobs.last_error":              "exento: error del proveedor; solo lo muestra nexoDeadLetterVisible tras pasar a la cola muerta",
	"nexo_kb_index_versions.config":              "exento: descripción del índice generada por el servidor (sin claves; ver pruebas de search-status)",
	"nexo_kb_index_versions.index_kind":          "exento: enumeración",
	"nexo_kb_index_versions.status":              "exento: enumeración",
	"nexo_kb_proposals.category":                 "escenario: propuestas",
	"nexo_kb_proposals.content_md":               "escenario: propuestas",
	"nexo_kb_proposals.doc_key":                  "escenario: propuestas",
	"nexo_kb_proposals.proposed_by_client_claim": "histórica",
	"nexo_kb_proposals.proposed_by_email":        "histórica",
	"nexo_kb_proposals.proposed_by_ip":           "exento: IP de la conexión",
	"nexo_kb_proposals.proposed_by_label":        "histórica",
	"nexo_kb_proposals.rationale":                "escenario: propuestas",
	"nexo_kb_proposals.review_note":              "escenario: propuestas; histórica",
	"nexo_kb_proposals.reviewed_by_email":        "histórica",
	"nexo_kb_proposals.reviewed_by_label":        "histórica",
	"nexo_kb_proposals.status":                   "exento: enumeración",
	"nexo_kb_proposals.tags":                     "escenario: propuestas; histórica",
	"nexo_kb_proposals.title":                    "escenario: propuestas",
	"nexo_kb_publish_runs.actor_label":           "histórica",
	"nexo_kb_publish_runs.client_ip":             "exento: IP de la conexión",
	"nexo_kb_publish_runs.kind":                  "exento: enumeración",
	"nexo_kb_publish_runs.origin":                "escenario: documentos; histórica",
	"nexo_kb_publish_runs.prune_skipped":         "histórica",
	"nexo_kb_publish_runs.rejected":              "histórica",
	"nexo_kb_publish_runs.source":                "histórica",
	"nexo_kb_publish_runs.space_slug":            "exento: identificador de espacio validado",
	"nexo_kb_publish_runs.summary":               "exento: contadores del servidor",
	"nexo_kb_publish_runs.upload":                "histórica",
	"nexo_kb_publish_runs.user_agent":            "histórica",
	"nexo_kb_section_vectors.text_sha256":        "exento: hash",
	"nexo_kb_sections.anchor":                    "exento: derivado del contenido (solo con lectura del espacio)",
	"nexo_kb_sections.body":                      "escenario: documentos (contenido: solo con lectura del espacio)",
	"nexo_kb_sections.body_sha256":               "exento: hash",
	"nexo_kb_sections.category":                  "escenario: documentos",
	"nexo_kb_sections.extraction":                "exento: enumeración",
	"nexo_kb_sections.heading_path":              "escenario: documentos (contenido: solo con lectura del espacio)",
	"nexo_kb_sections.source_location":           "exento: posiciones del archivo",
	"nexo_kb_sections.tags":                      "escenario: documentos",
	"nexo_kb_spaces.categories":                  "escenario: espacio",
	"nexo_kb_spaces.description":                 "escenario: espacio; histórica",
	"nexo_kb_spaces.name":                        "escenario: espacio; histórica",
	"nexo_kb_spaces.scope_type":                  "exento: enumeración",
	"nexo_kb_spaces.settings":                    "escenario: espacio; histórica",
	"nexo_kb_spaces.slug":                        "exento: identificador validado",
	"nexo_kb_versions.category":                  "escenario: documentos",
	"nexo_kb_versions.change_note":               "escenario: documentos",
	"nexo_kb_versions.content_md":                "escenario: documentos (contenido: solo con lectura del espacio)",
	"nexo_kb_versions.content_sha256":            "exento: hash",
	"nexo_kb_versions.meta_sha256":               "exento: hash",
	"nexo_kb_versions.origin":                    "escenario: documentos",
	"nexo_kb_versions.owner_label":               "escenario: documentos",
	"nexo_kb_versions.published_by_email":        "escenario: personas y tokens",
	"nexo_kb_versions.published_by_label":        "escenario: personas y tokens",
	"nexo_kb_versions.source_blob_ref":           "exento: referencia interna que no sale en la API",
	"nexo_kb_versions.source_format":             "exento: enumeración",
	"nexo_kb_versions.source_mime":               "exento: tipo MIME validado",
	"nexo_kb_versions.source_sha256":             "exento: hash",
	"nexo_kb_versions.tags":                      "escenario: documentos",
	"nexo_kb_versions.title":                     "escenario: documentos",
}

func TestCanaryClassifiesEveryTextColumn(t *testing.T) {
	e := newNexoSecEnv(t, nil)
	rows, err := e.db.Query(`SELECT table_name || '.' || column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND data_type IN ('text', 'character varying', 'jsonb', 'json', 'ARRAY') ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	found := map[string]bool{}
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatal(err)
		}
		found[col] = true
		if _, ok := canaryColumns[col]; !ok {
			t.Errorf("columna de texto sin clasificar: %s. Decida si admite texto externo: si lo admite, que lo escriba un escenario del canario "+
				"o las filas históricas (y que su salida pase por redact.Struct o por NexoProject); si no, anótela con el motivo en canaryColumns", col)
		}
	}
	scenarios := map[string]bool{}
	for _, s := range canaryScenarios() {
		scenarios[s.name] = true
	}
	for col, why := range canaryColumns {
		if !found[col] {
			t.Errorf("canaryColumns tiene %s, que ya no existe en el esquema", col)
		}
		if strings.HasPrefix(why, "escenario: ") {
			for _, name := range strings.Split(strings.SplitN(strings.TrimPrefix(why, "escenario: "), " (", 2)[0], ";") {
				name = strings.TrimSpace(name)
				if name == "histórica" {
					continue
				}
				if !canaryScenarioPrefix(scenarios, name) {
					t.Errorf("%s: el escenario %q no existe en canaryScenarios", col, name)
				}
			}
		}
	}
}

// canaryScenarioPrefix: el registro nombra los escenarios por su primera palabra ("documentos", "propuestas").
func canaryScenarioPrefix(scenarios map[string]bool, name string) bool {
	for s := range scenarios {
		if strings.HasPrefix(s, name) {
			return true
		}
	}
	return false
}

// canaryUncheckedRoutes: rutas GET registradas que el canario no recorre, con el motivo.
var canaryUncheckedRoutes = map[string]string{
	"GET /nexo/health/ready":            "pública: solo estado de salud",
	"GET /dashboard/activate":           "pública: formulario de canje, sin datos",
	"GET /dashboard/kb/proposals/badge": "solo un contador",
	"GET /nexo/kb/v1/sections":          "contenido del conocimiento: exige lectura del espacio (ver /search y documentos)",
	"GET /nexo/kb/v1/changes":           "contenido del conocimiento: exige lectura del espacio",
	"GET /nexo/kb/v1/sync":              "contenido del conocimiento: exige lectura del espacio",
	"GET /nexo/kb/v1/spaces/{slug}/documents/{docKey}/sections/{anchor}": "contenido del conocimiento: exige lectura del espacio",
	"GET /dashboard/kb/spaces/{slug}/doc":                                "documento: se recorre con ?doc=",
}

func TestCanaryCoversEveryGETRoute(t *testing.T) {
	c := newCanary(t)
	c.post("carla", "/nexo/authz/v1/service-accounts", map[string]any{"slug": "ci-cobertura", "owner": "ana"})
	slug := c.historic()
	dynAPI, dynPanel := c.dynamicPaths(slug)
	covered := map[string]bool{}
	for _, p := range append(append(append(c.apiPaths(slug), c.panelPaths(slug)...), dynAPI...), dynPanel...) {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		_, pattern := c.e.srv.mux.Handler(req)
		covered[pattern] = true
	}
	var missing []string
	for _, r := range c.e.srv.nexo.routes.Routes() {
		if r.Method != http.MethodGet || covered[r.Pattern()] {
			continue
		}
		if _, ok := canaryUncheckedRoutes[r.Pattern()]; ok {
			continue
		}
		missing = append(missing, r.Pattern())
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("ruta GET que el canario no recorre: %s. Agréguela a apiPaths/panelPaths o a canaryUncheckedRoutes con su motivo", m)
	}
}
