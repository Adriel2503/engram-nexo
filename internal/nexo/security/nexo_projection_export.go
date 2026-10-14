package security

// NX-07 / R-6: proyección de las filas de las exportaciones de evidencia
// (diaria y CLI v2). Las exportaciones automáticas no tienen un lector de
// proyecto: se exporta lo que el esquema de cada tabla declara; una columna
// nueva no sale hasta declararla aquí (la prueba de columnas del paquete
// evidence falla mientras exista una columna sin clasificar).

import "github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"

// nexoExportChainColumns son columnas de la cadena de hashes que la consulta
// de exportación ya quita de la fila (el enlace va en prev_hash y hash).
var nexoExportChainColumns = []string{"nexo_prev_hash", "nexo_hash", "nexo_chain_seq"}

var nexoExportActor = nfMerge(
	nfFields(nfID, "client_ip"),
	nfFields(nfInt, "actor_principal_id actor_token_id"),
	nfFields(nfNum, "actor_verified"),
	nfFields(nfLabel, "actor_label actor_email user_agent client_claim"),
)

// nexoExportSchemas: columnas permitidas por tabla exportada. Las columnas de
// texto libre (resumen, notas) no se declaran: nunca se exportan.
var nexoExportSchemas = map[string]NexoSchema{
	"cloud_auth_audit_log": nfMerge(
		nfFields(nfID, "actor_source project action outcome reason_code"),
		nfFields(nfInt, "id actor_principal_id target_principal_id"),
		nfFields(nfTime, "occurred_at"),
		NexoSchema{"metadata": nfObj(nexoSchemas[NexoStreamAudit])},
	),
	"nexo_authz_events": nfMerge(
		nexoExportActor,
		nfFields(nfID, "action scope_type scope_key role_before role_after"),
		nfFields(nfInt, "id target_principal_id"),
		nfFields(nfTime, "occurred_at"),
		NexoSchema{"details": nfObj(nexoSchemas[NexoStreamRoles])},
	),
	"nexo_kb_events": nfMerge(
		nexoExportActor,
		nfFields(nfID, "space_id space_slug action target_kind target_id"),
		nfFields(nfInt, "id"),
		nfFields(nfTime, "occurred_at"),
		NexoSchema{"details": nfObj(nexoSchemas[NexoStreamKB])},
	),
	"nexo_alerts": nfMerge(
		nfFields(nfID, "rule severity layer subject"),
		nfFields(nfInt, "id"),
		nfFields(nfLabel, "dedup_key"),
		nfFields(nfTime, "occurred_at"),
		NexoSchema{"details": nfObj(nexoSchemas[NexoStreamAlert])},
	),
	"nexo_alert_acks": nfMerge(
		nfFields(nfID, "client_ip"),
		nfFields(nfInt, "id alert_id acknowledged_by_principal_id acknowledged_by_token_id"),
		nfFields(nfLabel, "acknowledged_by_label user_agent"),
		nfFields(nfTime, "occurred_at"),
	),
	"nexo_access_reviews": nfMerge(
		nfFields(nfID, "report_sha256 client_ip"),
		nfFields(nfInt, "id reviewer_principal_id reviewer_token_id"),
		nfFields(nfLabel, "reviewer_label user_agent"),
		nfFields(nfTime, "occurred_at"),
		NexoSchema{"summary": nfObj(nexoSchemas[NexoStreamReviewSummary])},
	),
}

// NexoExportRow proyecta una fila exportada: normaliza a JSON, quita
// credenciales de claves y valores y conserva solo las columnas del esquema de
// la tabla (metadata y details con el esquema de su flujo). La salida siempre
// lleva payload_withheld: true.
func NexoExportRow(table string, row map[string]any) map[string]any {
	clean := redact.Metadata(row)
	schema := nexoExportSchemas[table]
	if table == "nexo_alerts" && row["layer"] == "conocimiento" { // los detalles de la capa de conocimiento llevan texto del documento
		schema = nfMerge(schema, NexoSchema{"details": nfObj(nexoSchemas[NexoStreamAlertKB])})
	}
	out, _ := nexoProjectObject(schema, clean, 0)
	out["payload_withheld"] = true
	return out
}
