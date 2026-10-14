package service

// NX-07 / R-6: evidencia de conocimiento filtrada por lectura del espacio.
import (
	"context"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
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
		redact.Struct(&items[i]) // todo texto de la fila, con o sin permiso
		items[i].Details = security.NexoProject(security.NexoStreamKB, items[i].Details, allowed)
		if !allowed { // identificadores definidos por el productor; el resto de la fila no es contenido
			items[i].TargetID = security.NexoIDOrEmpty(items[i].TargetID)
		}
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
		redact.Struct(&items[i])
		items[i].Origin = security.NexoProject(security.NexoStreamPublishRun, items[i].Origin, allowed)
		items[i].Upload = security.NexoProject(security.NexoStreamPublishRun, items[i].Upload, allowed)
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
		nexoProjectEvalRun(&items[i], allowed)
	}
	return items, nil
}

// nexoProjectEvalRun deja en una corrida lo que corresponde entregar. Todo texto
// se limpia siempre (filas históricas y con permiso incluidos, claves y valores de
// Config, Metrics y ByClass). Sin lectura de TODOS los espacios medidos se retienen los
// campos libres (commit, nota, nombre del conjunto, autor, configuración y detalle por
// clase); retriever y modo salen solo si tienen la forma de una enumeración, y las
// métricas solo con los nombres numéricos del esquema.
func nexoProjectEvalRun(r *store.EvalRun, allowed bool) {
	redact.Struct(r)
	if !allowed {
		if !store.ValidEvalRetriever(r.Retriever) {
			r.Retriever = ""
		}
		if !store.ValidEvalMode(r.Mode) {
			r.Mode = ""
		}
		r.Metrics = security.NexoProject(security.NexoStreamEvalMetrics, r.Metrics, false)
		r.Config = map[string]any{"payload_withheld": true}
		r.ByClass = nil
		r.Results = nil
		r.Note, r.QuestionSetName, r.CommitRef, r.Actor = "", "", "", ""
	}
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

// nexoCheckCredentialFree rechaza la escritura de un campo de texto libre
// (nombre, descripción, etiquetas, ajustes, notas) que traiga un formato de
// credencial o una cadena de conexión, en cualquier clave o valor anidado. La
// respuesta nombra el campo y nunca repite el valor.
func nexoCheckCredentialFree(fields map[string]any) error {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if redact.HasAny(fields[name]) {
			return domain.Errorf(domain.KindInvalid, domain.CodeSecret, "el campo %s contiene un formato de credencial (RN-K4); quítalo antes de guardar", name)
		}
	}
	return nil
}
