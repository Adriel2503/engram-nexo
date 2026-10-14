package security

// NX-07 / R-6: proyección por esquema de los metadatos que se entregan o
// exportan a quien no lee el contenido (auditores, administradores,
// exportaciones automáticas, webhook).
//
// Una sola función (NexoProject) y una lista permitida POR PRODUCTOR (flujo) y
// POR RUTA de campo. Antes había una lista global de nombres: un mapa
// desconocido se recorría y volvía a aplicar esos nombres, así que
// settings={"cache":{"name":"texto reservado"}} salía bajo "name". Ahora:
//
//   - sin lectura del ámbito, solo salen los campos que el esquema del flujo
//     declara, con el tipo declarado (número, identificador, fecha, enumeración,
//     etiqueta administrativa, objeto o lista declarados). Todo lo demás, objetos
//     desconocidos completos incluidos, se omite y la salida lleva
//     payload_withheld: true;
//   - con lectura del ámbito se entrega una copia con todo, pero siempre
//     normalizada a JSON y sin formatos de credencial en claves ni valores;
//   - las claves de secretos (hash, password, ...) nunca salen, con o sin permiso.
//
// Un productor nuevo que escriba un campo nuevo no sale a quien no lee el
// contenido hasta declararlo en el esquema de su flujo. La prueba canario de
// extremo a extremo (cloudserver/nexo_canary_test.go) falla si un texto libre
// llega a un lector sin rol.

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
)

// NexoStream identifica al productor de un mapa de metadatos.
type NexoStream string

// Flujos con esquema.
const (
	NexoStreamAudit         NexoStream = "audit"          // cloud_auth_audit_log.metadata
	NexoStreamRoles         NexoStream = "roles"          // nexo_authz_events.details
	NexoStreamKB            NexoStream = "kb"             // nexo_kb_events.details
	NexoStreamAlert         NexoStream = "alert"          // nexo_alerts.details
	NexoStreamAlertKB       NexoStream = "alert_kb"       // nexo_alerts.details de la capa de conocimiento: título y responsable son texto del documento
	NexoStreamPublishRun    NexoStream = "publish_run"    // nexo_kb_publish_runs.origin y upload
	NexoStreamEvalMetrics   NexoStream = "eval_metrics"   // nexo_kb_eval_runs.metrics
	NexoStreamReviewSummary NexoStream = "review_summary" // nexo_access_reviews.summary: solo contadores
)

// NexoFieldKind es el tipo declarado de un campo.
type NexoFieldKind uint8

// Tipos de campo.
const (
	// NexoFieldNumber: número, booleano o nulo.
	NexoFieldNumber NexoFieldKind = iota + 1
	// NexoFieldID: identificador corto (letras, dígitos y . _ : @ / + -), sin formato de credencial.
	NexoFieldID
	// NexoFieldTime: fecha RFC 3339 o AAAA-MM-DD.
	NexoFieldTime
	// NexoFieldLabel: etiqueta administrativa corta (nombre de una persona, de un token o motivo escrito por
	// quien administra): se entrega sin permiso de lectura, redactada y recortada. No es contenido de documentos.
	NexoFieldLabel
	// NexoFieldText: texto libre. Solo con permiso de lectura del ámbito.
	NexoFieldText
	// NexoFieldRoute: método y ruta del servidor ("GET /nexo/admin/audit").
	NexoFieldRoute
	// NexoFieldObject: objeto con esquema propio (Sub).
	NexoFieldObject
	// NexoFieldList: lista de elementos del tipo Elem.
	NexoFieldList
	// NexoFieldCounters: objeto cuyas claves son identificadores y cuyos valores son números.
	NexoFieldCounters
)

// NexoField describe un campo permitido.
type NexoField struct {
	Kind NexoFieldKind
	Sub  NexoSchema // NexoFieldObject
	Elem *NexoField // NexoFieldList
}

// NexoSchema es la lista permitida de un objeto: nombre de campo -> tipo.
type NexoSchema map[string]NexoField

var (
	nfNum   = NexoField{Kind: NexoFieldNumber}
	nfID    = NexoField{Kind: NexoFieldID}
	nfTime  = NexoField{Kind: NexoFieldTime}
	nfLabel = NexoField{Kind: NexoFieldLabel}
	nfText  = NexoField{Kind: NexoFieldText}
	nfRoute = NexoField{Kind: NexoFieldRoute}
	nfCount = NexoField{Kind: NexoFieldCounters}
)

func nfList(e NexoField) NexoField { return NexoField{Kind: NexoFieldList, Elem: &e} }
func nfObj(s NexoSchema) NexoField { return NexoField{Kind: NexoFieldObject, Sub: s} }
func nfFields(kind NexoField, names string) NexoSchema {
	s := NexoSchema{}
	for _, n := range strings.Fields(names) {
		s[n] = kind
	}
	return s
}
func nfMerge(parts ...NexoSchema) NexoSchema {
	out := NexoSchema{}
	for _, p := range parts {
		for k, v := range p {
			out[k] = v
		}
	}
	return out
}

// nexoFilterSchema: filtros de las lecturas de auditoría (consulta del propio auditor).
var nexoFilterSchema = nfMerge(
	nfFields(nfID, "action outcome entity op source since until limit rule layer state severity status format only days kind scope_type entity_key project space doc_key"),
	nfFields(nfLabel, "actor principal username token"),
	nfFields(nfText, "q query target"),
)

// nexoSchemas son los esquemas de cada flujo. Los campos de texto libre
// (NexoFieldText) solo salen con permiso de lectura del ámbito.
var nexoSchemas = map[NexoStream]NexoSchema{
	NexoStreamAudit: nfMerge(
		nfFields(nfNum, "enabled result_count rejected failures window_seconds lockout_seconds nexo_repeated nexo_window_seconds nexo_user_agents_truncated issuer_ip_match emergency recovery recovery_check_failed created_admin recovered inactive_days people ok"),
		nfFields(nfID, "source credential_id client_ip kind role platform_role token_prefix id via project scope activation_id group key_type last_reason counts alert_id review_id report_sha256 nexo_principal_id space format table day"),
		nfFields(nfTime, "expires_at minute locked_until nexo_first_at nexo_last_at nexo_window_start"),
		nfFields(nfLabel, "username name reason"),
		nfFields(nfText, "user_agent credential_fp"),
		NexoSchema{
			"expires_in_days":  nfNum,
			"nexo_user_agents": nfList(nfText),
			"tables":           nfList(nfID),
			"grant_projects":   nfList(nfID),
			"filters":          nfObj(nexoFilterSchema),
			"operation":        nfID,
			"route":            nfRoute,
		},
	),
	NexoStreamRoles: nfMerge(
		nfFields(nfNum, "owner_principal_id temporary count self_approval heuristic"),
		nfFields(nfID, "action capability target scope scope_type scope_key rule hidden role role_before role_after engram_role_before engram_role_after engram_role_requested visibility default_role status_before status_after slug token_id new_token_id token_prefix principal_id actor source via reason_code kind missing"),
		nfFields(nfTime, "expires_at"),
		nfFields(nfLabel, "reason description name token_name username"),
		NexoSchema{"grace_minutes": nfNum, "scope": nfID, "route": nfRoute},
	),
	NexoStreamKB: nfMerge(
		nfFields(nfNum, "version base_version previous_version sections pruned dry_run accepted hit_at_5 mrr self_approval edited forced reactivated hidden"),
		nfFields(nfID, "doc_key superseded_by content_sha256 source_sha256 source_format result category proposal_id reason set_sha256 visibility default_role status_before status_after run_id kind capability role proposed_by_principal_id action"),
		nfFields(nfTime, "valid_from valid_until"),
		nfFields(nfLabel, "owner proposed_by"),
		nfFields(nfText, "title note name description prune_skipped source set target"),
		NexoSchema{
			"categories": nfList(nfID),
			"summary":    nfCount,
			"route":      nfRoute,
			// space.updated: antes y después. Los nombres y descripciones son texto; "settings" y cualquier
			// objeto que no esté aquí se omiten completos.
			"before": nfObj(nexoSpaceStateSchema),
			"after":  nfObj(nexoSpaceStateSchema),
			// Origen declarado de la publicación: solo la clase y el orden son metadatos neutros.
			"origin": nfObj(nfMerge(nfFields(nfID, "kind"), NexoSchema{"order": nfNum})),
		},
	),
	NexoStreamAlert: nfMerge(
		nfFields(nfNum, "count threshold window_seconds altered_rows missing_rows checkpoint_seq last_seq days_left notice_days idle_days threshold_days ttl_hours heuristic superadmins_enabled version review_every_days event_id"),
		nfFields(nfID, "action role_before role_after rule kind nexo_role engram_role expected_engram_role platform_role scope token_id token_prefix principal_id owner_principal_id slug table space doc_key client_ip actor activation_id issuer_principal_id reason day project"),
		nfFields(nfTime, "first_at last_at at expires_at valid_until published_at created_at last_activity_at"),
		nfFields(nfLabel, "username owner token_name"),
		nfFields(nfText, "title"),
	),
	NexoStreamAlertKB: nfMerge(
		nfFields(nfNum, "version review_every_days"),
		nfFields(nfID, "space doc_key"),
		nfFields(nfTime, "valid_until published_at"),
		nfFields(nfText, "title owner"),
	),
	NexoStreamPublishRun: nfMerge(
		nfFields(nfID, "kind"),
		NexoSchema{"order": nfNum},
	),
	NexoStreamReviewSummary: nfFields(nfNum, "people enabled inactive active_tokens tokens_no_expiry tokens_never_used tokens_expiring_30d platform_staff service_accounts_stale role_drift superadmins_enabled privileged_tokens_expiring"),
	NexoStreamEvalMetrics: nfMerge(
		nfFields(nfNum, "questions hit_at_1 hit_at_3 hit_at_5 hit_at_10 mrr recall_at_5 ndcg no_result_rate latency_ms_p50 latency_ms_p95 hits misses no_results expected total"),
	),
}

var nexoSpaceStateSchema = nfMerge(
	nfFields(nfText, "name description"),
	NexoSchema{"categories": nfList(nfID)},
)

var (
	nexoIDRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/+-]{0,199}$`)
	nexoRouteRe = regexp.MustCompile(`^[A-Z]{3,7} /[A-Za-z0-9/_.{}:+-]{0,200}$`)
)

const nexoLabelMaxRunes = 200

// NexoProject devuelve la copia de meta que corresponde entregar para el flujo.
// contentAllowed es verdadero si quien consulta lee el ámbito del registro
// (espacio o proyecto). Nunca modifica meta.
func NexoProject(stream NexoStream, meta map[string]any, contentAllowed bool) map[string]any {
	if meta == nil {
		return nil
	}
	clean := redact.Metadata(meta) // normaliza a JSON y quita credenciales de claves y valores
	if contentAllowed {
		out, _ := nexoDropSecrets(clean, 0)
		return out
	}
	schema := nexoSchemas[stream] // flujo desconocido: esquema vacío, todo se omite
	out, withheld := nexoProjectObject(schema, clean, 0)
	if withheld {
		out["payload_withheld"] = true
	}
	return out
}

func nexoSecretKey(k string) bool {
	k = strings.ToLower(k)
	for _, s := range []string{"hash", "password", "secret", "pepper", "authorization", "cookie", "raw_token", "activation_code", "code_hash", "api_key"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// nexoDropSecrets quita las claves de secretos de una copia ya normalizada.
func nexoDropSecrets(m map[string]any, depth int) (map[string]any, bool) {
	out := make(map[string]any, len(m))
	dropped := false
	for k, v := range m {
		if nexoSecretKey(k) {
			dropped = true
			continue
		}
		switch x := v.(type) {
		case map[string]any:
			if depth >= 12 {
				dropped = true
				continue
			}
			y, d := nexoDropSecrets(x, depth+1)
			out[k], dropped = y, dropped || d
		case []any:
			list := make([]any, 0, len(x))
			for _, e := range x {
				if em, ok := e.(map[string]any); ok && depth < 12 {
					y, d := nexoDropSecrets(em, depth+1)
					list, dropped = append(list, y), dropped || d
					continue
				}
				list = append(list, e)
			}
			out[k] = list
		default:
			out[k] = v
		}
	}
	return out, dropped
}

func nexoProjectObject(schema NexoSchema, in map[string]any, depth int) (map[string]any, bool) {
	out := map[string]any{}
	withheld := false
	for k, v := range in {
		spec, ok := schema[k]
		if !ok || nexoSecretKey(k) || depth > 8 {
			withheld = true
			continue
		}
		val, keep, w := nexoProjectValue(spec, v, depth+1)
		withheld = withheld || w
		if !keep {
			withheld = true
			continue
		}
		out[k] = val
	}
	return out, withheld
}

// nexoProjectValue devuelve el valor proyectado, si se conserva y si algo se omitió dentro.
func nexoProjectValue(spec NexoField, v any, depth int) (any, bool, bool) {
	switch spec.Kind {
	case NexoFieldNumber:
		switch v.(type) {
		case nil, bool, json.Number, float64, int, int64:
			return v, true, false
		}
	case NexoFieldID:
		if s, ok := v.(string); ok && nexoIDRe.MatchString(s) && !redact.Has(s) {
			return s, true, false
		}
	case NexoFieldRoute:
		if s, ok := v.(string); ok && nexoRouteRe.MatchString(s) {
			return s, true, false
		}
	case NexoFieldTime:
		if s, ok := v.(string); ok && nexoIsTime(s) {
			return s, true, false
		}
		if v == nil {
			return nil, true, false
		}
	case NexoFieldLabel:
		if s, ok := v.(string); ok {
			return redact.External(s, nexoLabelMaxRunes), true, false
		}
	case NexoFieldObject:
		if m, ok := v.(map[string]any); ok {
			out, w := nexoProjectObject(spec.Sub, m, depth)
			return out, true, w
		}
	case NexoFieldCounters:
		if m, ok := v.(map[string]any); ok {
			out, w := map[string]any{}, false
			for k, x := range m {
				switch x.(type) {
				case json.Number, float64, int, int64:
					if nexoIDRe.MatchString(k) {
						out[k] = x
						continue
					}
				}
				w = true
			}
			return out, true, w
		}
	case NexoFieldList:
		if l, ok := v.([]any); ok && spec.Elem != nil {
			out, w := make([]any, 0, len(l)), false
			for _, e := range l {
				if y, keep, iw := nexoProjectValue(*spec.Elem, e, depth+1); keep {
					out, w = append(out, y), w || iw
				} else {
					w = true
				}
			}
			return out, true, w
		}
	case NexoFieldText:
		// Texto libre: solo con permiso de lectura (se omite).
	}
	return nil, false, false
}

func nexoIsTime(s string) bool {
	if _, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return true
	}
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

// NexoAuditHasCredential identifica formatos de credencial o cadenas de conexión
// que nunca deben aceptarse como etiquetas ni devolverse en evidencia.
func NexoAuditHasCredential(text string) bool { return redact.Has(text) }

// NexoIDOrEmpty devuelve s si tiene la forma de un identificador y no trae un
// formato de credencial; si no, una cadena vacía. Para las columnas de texto
// de una fila que se entregan sin lectura del ámbito.
func NexoIDOrEmpty(s string) string {
	if nexoIDRe.MatchString(s) && !redact.Has(s) {
		return s
	}
	return ""
}
