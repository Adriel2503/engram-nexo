package service

// NX-07 / R-6: evidencia de conocimiento filtrada por lectura del espacio.
import (
	"context"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *Service) nexoAuditRead(ctx context.Context, id authz.Identity, slug string) (bool, error) {
	d, err := s.eng.Can(ctx, id, authz.SpaceScope(slug), authz.CapSpaceRead)
	return d.Allowed && err == nil, err
}
func (s *Service) nexoListEvents(ctx context.Context, id authz.Identity, slug string, before int64, action string, limit int) ([]domain.Event, error) {
	allowed, err := s.nexoAuditRead(ctx, id, slug)
	if err != nil {
		return nil, err
	}
	items, err := store.ListEvents(ctx, s.db, slug, before, strings.TrimSpace(action), limit)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Details = security.NexoAuditMetadata(items[i].Details, allowed)
	}
	return items, nil
}
func (s *Service) nexoListPublishRuns(ctx context.Context, id authz.Identity, sp domain.Space, before *time.Time, limit int) ([]store.PublishRun, error) {
	allowed, err := s.nexoAuditRead(ctx, id, sp.Slug)
	if err != nil {
		return nil, err
	}
	items, err := store.ListPublishRuns(ctx, s.db, sp.ID, before, limit)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Origin = security.NexoAuditMetadata(items[i].Origin, allowed)
		items[i].Upload = security.NexoAuditMetadata(items[i].Upload, allowed)
		if !allowed {
			items[i].Rejected = nil
			items[i].PruneSkipped = ""
			items[i].Source = ""
		}
	}
	return items, nil
}
func (s *Service) nexoListEvalRuns(ctx context.Context, id authz.Identity, set string, accepted bool, limit int) ([]store.EvalRun, error) {
	items, err := store.ListEvalRuns(ctx, s.db, set, accepted, limit)
	if err != nil {
		return nil, err
	}
	for i := range items {
		allowed := len(items[i].Spaces) > 0
		for _, slug := range items[i].Spaces {
			ok, err := s.nexoAuditRead(ctx, id, slug)
			if err != nil {
				return nil, err
			}
			allowed = allowed && ok
		}
		items[i].Config = security.NexoAuditMetadata(items[i].Config, allowed)
		items[i].Metrics = security.NexoAuditMetadata(items[i].Metrics, allowed)
		items[i].ByClass = security.NexoAuditMetadata(items[i].ByClass, allowed)
		if !allowed {
			items[i].Config = map[string]any{"payload_withheld": true}
			items[i].ByClass = nil
			items[i].Results = nil
			items[i].Note = ""
			items[i].QuestionSetName = ""
		}
	}
	return items, nil
}

func (s *Service) nexoDeadLetterVisible(ctx context.Context, id authz.Identity, d store.DeadLetter) (store.DeadLetter, error) {
	allowed, err := s.nexoAuditRead(ctx, id, d.Space)
	if err != nil {
		return d, err
	}
	if !allowed {
		d.Error = "oculto: requiere rol de lectura en el espacio"
	} else {
		d.Error = security.NexoRedactAuditText(d.Error)
	}
	return d, nil
}

// nexoCredentialIn devuelve el nombre del primer campo de texto externo que
// trae un formato de credencial (código de activación, token, cabecera de
// autorización, hash). Cubre lo que no es contenido del documento: clave,
// ruta, origen, nota de cambio, responsable y etiquetas, que terminan en
// eventos, alertas y webhooks.
func nexoCredentialIn(fields map[string]string) string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names) // resultado estable
	for _, name := range names {
		v := fields[name]
		if v == "" {
			continue
		}
		if security.NexoAuditHasCredential(v) || len(ingest.DetectSecrets(v)) > 0 {
			return name
		}
	}
	return ""
}

// nexoOriginFields lista los textos libres del origen declarado.
func nexoOriginFields(o domain.Origin) map[string]string {
	m := map[string]string{
		"origin.repo": o.Repo, "origin.branch": o.Branch, "origin.commit": o.Commit, "origin.path": o.Path, "origin.author": o.Author,
		"origin.approver": o.Approver, "origin.ci_run": o.CIRun, "origin.filename": o.Filename, "origin.source": o.Source,
		"origin.source_url": o.SourceURL, "origin.proposal_id": o.ProposalID,
	}
	for i, t := range o.PathTitles {
		m["origin.path_titles["+strconv.Itoa(i)+"]"] = t
	}
	return m
}

// nexoRejectCredential es el rechazo de un campo externo con formato de
// credencial. La respuesta nombra el campo y no repite su valor: la clave y la
// ruta salen redactadas, porque el resultado se guarda en la ejecución.
func nexoRejectCredential(key, path, field string) *DocResult {
	key, path = security.NexoRedactAuditText(key), security.NexoRedactAuditText(path)
	if len(ingest.DetectSecrets(key)) > 0 {
		key = "[credencial oculta]"
	}
	if len(ingest.DetectSecrets(path)) > 0 {
		path = "[credencial oculta]"
	}
	return &DocResult{DocKey: key, Path: path, Status: domain.ResultRejected, Code: domain.CodeSecret,
		Reason: "el campo " + field + " contiene un formato de credencial (RN-K4); quítalo antes de publicar"}
}
