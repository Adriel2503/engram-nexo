package cloudstore

// NX-07: el almacén conserva la identidad original y redacta la copia de salida.
import (
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
)

func nexoRedactAuditRecord(rec NexoAuditRecord) NexoAuditRecord {
	rec.NexoRememberScope(authz.NexoAuditScope(rec.Project, rec.Metadata))
	redact.Struct(&rec)
	return rec
}
func nexoRedactHistoryRecord(rec NexoHistoryRecord) NexoHistoryRecord {
	rec.NexoRememberScope(authz.ProjectScope(rec.Project))
	redact.Struct(&rec)
	return rec
}
