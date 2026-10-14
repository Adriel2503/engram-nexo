package cloudserver

// Filas históricas del canario: texto que se guardó ANTES de que existiera la
// validación (o saltándola, por SQL). Cada columna de texto de las tablas con
// salida lleva el texto reservado del campo más las dos credenciales. Las
// salidas deben entregar las filas sin credenciales y, a quien no lee el
// ámbito, sin el texto reservado.

import (
	"encoding/json"
	"strings"
)

// both: texto reservado del campo seguido de las dos credenciales.
func (c *canary) both(tag string) string {
	return c.reserved(tag) + " " + c.tok + " " + c.code
}

func (c *canary) tryExec(label, q string, args ...any) {
	c.t.Helper()
	if _, err := c.e.db.Exec(q, args...); err != nil {
		c.t.Logf("fila histórica no escrita (%s): %.120v", label, err)
	}
}

func (c *canary) historic() string {
	c.t.Helper()
	slug := c.space()
	b := c.both
	c.post("ana", "/nexo/kb/v1/spaces/"+slug+"/documents:batch", map[string]any{"documents": []any{map[string]any{"path": "hist.md", "content": "---\ncategory: lineamiento\n---\n# Hist\n\ncontenido histórico\n"}}})
	rec := c.post("ana", "/nexo/kb/v1/spaces/"+slug+"/proposals", map[string]any{"doc_key": "hist-prop", "title": "Propuesta historica", "category": "lineamiento", "content": "# P\n\ncuerpo\n"})
	var prop struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(rec.Body), &prop)
	js := func(s string) string { raw, _ := json.Marshal(s); return string(raw) }

	// Espacio, documentos, versiones, propuestas.
	c.tryExec("espacio", `UPDATE nexo_kb_spaces SET name=$2, description=$3, settings=jsonb_build_object('cache', jsonb_build_object('name',$4::text, $5::text, $6::text)) WHERE slug=$1`,
		slug, b("h_space_name"), b("h_space_description"), b("h_settings_name"), b("h_settings_key"), b("h_settings_value"))
	c.tryExec("documento", `UPDATE nexo_kb_documents SET title=$2, owner_label=$3, tags=ARRAY[$4::text], origin_ref=$5, retirement_note=$6 WHERE space_id=(SELECT id FROM nexo_kb_spaces WHERE slug=$1)`,
		slug, b("h_doc_title"), b("h_doc_owner"), b("h_doc_tag"), b("h_doc_origin_ref"), b("h_doc_retire_note"))
	c.tryExec("versión", `UPDATE nexo_kb_versions SET title=$2, owner_label=$3, tags=ARRAY[$4::text], change_note=$5, published_by_label=$6, origin=jsonb_build_object('repo',$7::text,'source_url',$8::text) WHERE space_id=(SELECT id FROM nexo_kb_spaces WHERE slug=$1)`,
		slug, b("h_ver_title"), b("h_ver_owner"), b("h_ver_tag"), b("h_ver_change_note"), b("h_ver_published_by"), b("h_ver_repo"), b("h_ver_source_url"))
	if prop.ID != "" {
		c.tryExec("propuesta", `UPDATE nexo_kb_proposals SET review_note=$2, proposed_by_label=$3, proposed_by_client_claim=$4, reviewed_by_label=$5, tags=ARRAY[$6::text], proposed_by_email=$7 WHERE id=$1::uuid`,
			prop.ID, b("h_prop_review_note"), b("h_prop_proposed_by"), b("h_prop_claim"), b("h_prop_reviewed_by"), b("h_prop_tag"), b("h_prop_email"))
	}

	// Registros de solo inserción.
	c.tryExec("corrida de evaluación", `INSERT INTO nexo_kb_eval_runs (id, retriever, mode, index_version, config, config_sha256, question_set_sha256, question_set_name, spaces, accepted, metrics, by_class, results, commit_ref, note, actor_label)
		VALUES (gen_random_uuid(), $1, $2, 1, $3::jsonb, 'c', 's', $4, ARRAY[$5::text], true, $6::jsonb, $7::jsonb, $8::jsonb, $9, $10, $11)`,
		b("h_eval_retriever"), b("h_eval_mode"), `{`+js(b("h_eval_cfg_key"))+`:`+js(b("h_eval_cfg_value"))+`,"nested":{"name":`+js(b("h_eval_cfg_nested"))+`}}`, b("h_eval_set"), slug,
		`{"hit_at_5":1,`+js(b("h_eval_metric_key"))+`:2}`, `{`+js(b("h_eval_class"))+`:{"hit_at_5":1}}`, `[{"question":`+js(b("h_eval_question"))+`}]`, b("h_eval_commit"), b("h_eval_note"), b("h_eval_actor"))
	c.tryExec("ejecución de publicación", `INSERT INTO nexo_kb_publish_runs (id, space_id, space_slug, kind, started_at, source, origin, upload, summary, rejected, prune_skipped, actor_label, client_ip, user_agent)
		VALUES (gen_random_uuid(), (SELECT id FROM nexo_kb_spaces WHERE slug=$1), $1, 'batch', now(), $2, $3::jsonb, $4::jsonb, '{}'::jsonb, $5::jsonb, $6, $7, '10.1.1.1', $8)`,
		slug, b("h_run_source"), `{"repo":`+js(b("h_run_origin_repo"))+`,"source_url":`+js(b("h_run_origin_url"))+`}`, `{"name":`+js(b("h_run_upload_name"))+`}`, `[{"reason":`+js(b("h_run_rejected"))+`}]`,
		b("h_run_prune_skipped"), b("h_run_actor"), b("h_run_ua"))
	c.tryExec("evento de conocimiento", `INSERT INTO nexo_kb_events (space_id, space_slug, action, target_kind, target_id, actor_label, user_agent, client_claim, details)
		VALUES ((SELECT id FROM nexo_kb_spaces WHERE slug=$1), $1, 'space.updated', 'space', $2, $3, $4, $5, $6::jsonb)`,
		slug, b("h_kbevent_target"), b("h_kbevent_actor"), b("h_kbevent_ua"), b("h_kbevent_claim"),
		`{"before":{"name":`+js(b("h_kbevent_before_name"))+`,"settings":{"x":`+js(b("h_kbevent_settings"))+`}},"after":{"description":`+js(b("h_kbevent_after_desc"))+`},"extra":{"k":`+js(b("h_kbevent_extra"))+`},"note":`+js(b("h_kbevent_note"))+`}`)
	c.tryExec("evento de roles", `INSERT INTO nexo_authz_events (action, scope_type, scope_key, actor_label, user_agent, client_claim, details)
		VALUES ('scope.updated','space',$1,$2,$3,$4,$5::jsonb)`, slug, b("h_authz_actor"), b("h_authz_ua"), b("h_authz_claim"),
		`{"reason":`+js(b("h_authz_reason"))+`,"description":`+js(b("h_authz_description"))+`,"settings":{"name":`+js(b("h_authz_settings"))+`},"slug":`+js(b("h_authz_slug"))+`}`)
	c.tryExec("auditoría de seguridad", `INSERT INTO cloud_auth_audit_log (actor_source, action, outcome, project, reason_code, metadata) VALUES ($1,'hist.cred','success',$2,$3,$4::jsonb)`,
		"request", b("h_audit_project"), "x",
		`{"user_agent":`+js(b("h_audit_ua"))+`,"reason":`+js(b("h_audit_reason"))+`,"name":`+js(b("h_audit_name"))+`,"settings":{"name":`+js(b("h_audit_settings"))+`},"filters":{"actor":`+js(b("h_audit_filter_actor"))+`},"space":`+js(slug)+`}`)
	c.tryExec("actividad", `INSERT INTO nexo_activity_log (actor, actor_email, actor_principal_id, operation, project, user_agent, client_claim) VALUES ($1,$2,'1',$3,$4,$5,$6)`,
		b("h_act_actor"), b("h_act_email"), "pull_mutations", b("h_act_project"), b("h_act_ua"), b("h_act_claim"))
	c.tryExec("presencia", `INSERT INTO nexo_actor_presence (presence_key, actor, actor_email, last_user_agent, last_client_claim, last_project) VALUES ($1,$2,$3,$4,$5,$6)`,
		"hist|"+c.rnd, b("h_pres_actor"), b("h_pres_email"), b("h_pres_ua"), b("h_pres_claim"), b("h_pres_project"))
	c.tryExec("alerta", `INSERT INTO nexo_alerts (rule, severity, layer, subject, dedup_key, summary, details) VALUES ('kb_doc_expiring','warning','conocimiento',$1,$2,$3,$4::jsonb)`,
		"space:"+slug+"/doc:"+b("h_alert_doc"), "hist|"+c.rnd, b("h_alert_summary"), `{"space":`+js(slug)+`,"doc_key":"d1","title":`+js(b("h_alert_title"))+`,"owner":`+js(b("h_alert_owner"))+`}`)
	c.tryExec("alerta de plataforma", `INSERT INTO nexo_alerts (rule, severity, layer, subject, dedup_key, summary, details) VALUES ('platform_role_changed','high','plataforma',$1,$2,$3,$4::jsonb)`,
		"principal:1", "hist-plat|"+c.rnd, b("h_alert_plat_summary"), `{"username":`+js(b("h_alert_plat_username"))+`,"actor":`+js(b("h_alert_plat_actor"))+`,"sample":[{"x":`+js(b("h_alert_plat_sample"))+`}],"unknown":{"name":`+js(b("h_alert_plat_unknown"))+`}}`)
	c.tryExec("reconocimiento", `INSERT INTO nexo_alert_acks (alert_id, acknowledged_by_label, note, user_agent) SELECT id,$1,$2,$3 FROM nexo_alerts WHERE dedup_key=$4`,
		b("h_ack_label"), b("h_ack_note"), b("h_ack_ua"), "hist|"+c.rnd)
	c.tryExec("revisión de accesos", `INSERT INTO nexo_access_reviews (reviewer_label, notes, summary, report_sha256, user_agent) VALUES ($1,$2,$3::jsonb,'abc',$4)`,
		b("h_review_label"), b("h_review_notes"), `{"people":1,`+js(b("h_review_summary_key"))+`:`+js(b("h_review_summary_value"))+`}`, b("h_review_ua"))
	c.tryExec("cola de indexación", `INSERT INTO nexo_kb_index_dead (index_version, section_id, space_id, payload, last_error, fail_count) VALUES (1, gen_random_uuid(), (SELECT id FROM nexo_kb_spaces WHERE slug=$1), $2::jsonb, $3, 3)`,
		slug, `{"x":`+js(b("h_dead_payload"))+`}`, b("h_dead_error"))

	// Personas, tokens, cuentas de servicio y controles de proyecto de Engram.
	c.tryExec("persona", `WITH p AS (INSERT INTO cloud_principals (kind, display_name, role) VALUES ('human',$1,'member') RETURNING id)
		INSERT INTO cloud_human_users (principal_id, username, email) SELECT id, $2, $3 FROM p`, b("h_person_display_name"), "h-"+strings.ReplaceAll(b("h_person_username"), " ", "_"), strings.ReplaceAll(b("h_person_email"), " ", "")+"@rp.pe")
	c.tryExec("token", `INSERT INTO cloud_principal_tokens (principal_id, token_prefix, token_hash, name, revoked_at, revocation_reason) VALUES ($1::bigint,'egc_hist','hash-hist-`+c.rnd+`',$2,now(),$3)`,
		c.e.ids["ana"], b("h_token_name"), b("h_token_revoke_reason"))
	c.tryExec("cuenta de servicio", `UPDATE nexo_service_accounts SET description=$1 WHERE slug=(SELECT slug FROM nexo_service_accounts LIMIT 1)`, b("h_sa_description"))
	c.tryExec("proyecto pausado", `INSERT INTO cloud_project_controls (project, sync_enabled, paused_reason, updated_by) VALUES ($1,false,$2,$3) ON CONFLICT (project) DO UPDATE SET paused_reason=EXCLUDED.paused_reason, updated_by=EXCLUDED.updated_by`,
		"hist-"+c.rnd, b("h_ctrl_reason"), b("h_ctrl_updated_by"))
	c.tryExec("auditoría de sincronización", `INSERT INTO cloud_sync_audit_log (contributor, project, action, outcome, entry_count, reason_code, metadata) VALUES ($1,$2,'push','success',1,$3,$4::jsonb)`,
		b("h_syncaudit_contributor"), "hist-"+c.rnd, b("h_syncaudit_reason"), `{"note":`+js(b("h_syncaudit_note"))+`}`)
	return slug
}
