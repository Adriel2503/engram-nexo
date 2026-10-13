package cloudserver

// Nexo IA · SG-07 — exportación de la revisión de accesos (ISO/IEC 27001:2022
// A.5.18). La API JSON (GET /nexo/admin/access-review?format=csv|json) y el
// panel (GET /dashboard/admin/audit/access-review/export) usan el mismo
// armado. Es una lectura: la hacen también las sesiones y tokens de solo
// lectura, y solo con platform.audit.read. Cada descarga queda en
// cloud_auth_audit_log como nexo.access_review.export con la huella; si no se
// puede registrar, no se entrega el archivo. Formato y verificación:
// internal/nexo/security/evidence/review_export.go.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

// nexoAuditActionAccessReviewExport es la acción de auditoría de la descarga.
const nexoAuditActionAccessReviewExport = "nexo.access_review.export"

// Formatos de la exportación.
const (
	nexoReviewFormatCSV  = "csv"
	nexoReviewFormatJSON = "json"
)

// nexoReviewExportFormat lee ?format=. ok=false si el valor no se conoce.
func nexoReviewExportFormat(r *http.Request) (format string, ok bool) {
	switch f := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))); f {
	case "", nexoReviewFormatCSV, nexoReviewFormatJSON:
		return f, true
	}
	return "", false
}

// nexoReviewExportFile es el archivo ya armado.
type nexoReviewExportFile struct {
	format      string
	body        []byte
	sha         string
	name        string
	contentType string
	people      int
	days        int
	onlyFlagged bool
}

// nexoBuildReviewExport arma el reporte con los filtros dados y lo convierte
// al formato pedido.
func (s *CloudServer) nexoBuildReviewExport(ctx context.Context, id authz.Identity, format string, days int, onlyFlagged bool) (nexoReviewExportFile, error) {
	rep, err := evidence.BuildAccessReview(ctx, s.nexoDB(), days, time.Now())
	if err != nil {
		return nexoReviewExportFile{}, err
	}
	exp := evidence.NewReviewExport(rep, evidence.ReviewExportActor{PrincipalID: id.PrincipalID, Name: strings.TrimSpace(id.Label)}, onlyFlagged)
	f := nexoReviewExportFile{format: format, people: len(exp.People), days: rep.InactiveDays, onlyFlagged: onlyFlagged}
	switch format {
	case nexoReviewFormatCSV:
		f.body, f.sha, err = exp.CSV()
		f.contentType = "text/csv; charset=utf-8"
	default:
		f.body, f.sha, err = exp.JSON()
		f.contentType = "application/json; charset=utf-8"
	}
	if err != nil {
		return nexoReviewExportFile{}, err
	}
	f.name = evidence.ReviewExportFilename(exp.GeneratedAt, f.sha, format)
	return f, nil
}

// nexoServeReviewExport registra la descarga y entrega el archivo. record
// deja la acción en la auditoría (API o panel) y responde el error si falla.
func (s *CloudServer) nexoServeReviewExport(w http.ResponseWriter, f nexoReviewExportFile, record func(action string, metadata map[string]any) bool) {
	if !record(nexoAuditActionAccessReviewExport, map[string]any{
		"format": f.format, "inactive_days": f.days, "only_flagged": f.onlyFlagged, "people": f.people,
		"bytes": len(f.body), "content_sha256": f.sha, "file": f.name,
	}) {
		return
	}
	h := w.Header()
	h.Set("Content-Type", f.contentType)
	h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", f.name))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Nexo-Content-SHA256", f.sha)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(f.body)
}

// auditPanelReviewExport es GET /dashboard/admin/audit/access-review/export:
// la misma exportación que la API, para el botón "Descargar" de la pantalla.
func (s *CloudServer) auditPanelReviewExport(w http.ResponseWriter, r *http.Request, _ *authz.Service, id authz.Identity) {
	only := r.URL.Query().Get("only") == "flags"
	days, err := nexoReviewInactiveDays(r)
	if err != nil {
		s.auditPanelReviewRender(w, r, id, evidence.ReviewInactiveDays, only, "", "Los días sin uso deben ser un número entre 1 y 3650.", http.StatusBadRequest)
		return
	}
	format, ok := nexoReviewExportFormat(r)
	if !ok || format == "" {
		s.auditPanelReviewRender(w, r, id, days, only, "", "Elige el formato de la descarga: CSV o JSON.", http.StatusBadRequest)
		return
	}
	file, err := s.nexoBuildReviewExport(r.Context(), id, format, days, only)
	if err != nil {
		nexoInternalError(w, "nexo audit panel", err)
		return
	}
	s.nexoServeReviewExport(w, file, func(action string, metadata map[string]any) bool {
		return s.auditPanelRecord(w, r, action, "", metadata)
	})
}
