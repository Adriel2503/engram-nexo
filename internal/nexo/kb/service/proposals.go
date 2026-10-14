package service

// Nexo IA · NX-10 entrega 2b — propuestas y revisión (C-07, RF-30..RF-33,
// HU-35, HU-36) con fecha de entrada en vigencia (KP-08). Una propuesta nunca
// aparece en búsquedas ni en el feed; la ven su autor y quienes tienen
// space.review en el espacio (un proponente no ve propuestas ajenas). Aprobar
// crea la versión con origen "proposal" en la misma transacción que cierra la
// propuesta; si el documento cambió desde la versión que vio el proponente,
// responde 409 salvo force.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// ProposalInput es el cuerpo de POST /spaces/{slug}/proposals.
type ProposalInput struct {
	DocKey      string     `json:"doc_key,omitempty"` // cambio a un documento existente (o clave del nuevo)
	BaseVersion int        `json:"base_version,omitempty"`
	Title       string     `json:"title"`
	Category    string     `json:"category,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	Content     string     `json:"content"`
	Rationale   string     `json:"rationale,omitempty"`
	ValidFrom   *time.Time `json:"valid_from,omitempty"` // KP-08
	ValidUntil  *time.Time `json:"valid_until,omitempty"`
}

// CreateProposal registra una propuesta (space.propose).
func (s *Service) CreateProposal(ctx context.Context, id authz.Identity, slug string, in ProposalInput) (store.Proposal, error) {
	sp, err := s.authorize(ctx, id, slug, authz.CapSpacePropose, map[string]any{"action": "proposal.create"})
	if err != nil {
		return store.Proposal{}, err
	}
	in.Title, in.Content, in.Rationale = strings.TrimSpace(in.Title), strings.TrimSpace(in.Content), strings.TrimSpace(in.Rationale)
	if in.Title == "" || in.Content == "" {
		return store.Proposal{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "title and content are required")
	}
	if int64(len(in.Content)) > s.limits.MaxDocBytes || len(in.Rationale) > 20000 || len(in.Title) > 300 {
		return store.Proposal{}, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "proposal is too large (content up to %d bytes)", s.limits.MaxDocBytes)
	}
	if in.ValidFrom != nil && in.ValidUntil != nil && !in.ValidUntil.After(*in.ValidFrom) {
		return store.Proposal{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "valid_until must be after valid_from")
	}
	if found := ingest.DetectSecrets(in.Content + "\n" + in.Title + "\n" + in.Rationale); len(found) > 0 {
		return store.Proposal{}, domain.Errorf(domain.KindInvalid, domain.CodeSecret, "the proposal contains evident secrets (RN-K4): %s (line %d)", found[0].Kind, found[0].Line)
	}
	// NX-07: clave, categoría y etiquetas son texto externo que llega a eventos y alertas.
	if field := nexoCredentialIn(map[string]string{"doc_key": in.DocKey, "category": in.Category, "tags": strings.Join(in.Tags, " ")}); field != "" {
		return store.Proposal{}, domain.Errorf(domain.KindInvalid, domain.CodeSecret, "the proposal field %s contains a credential format (RN-K4); remove it", field)
	}
	p := store.Proposal{ID: store.NewID(), SpaceID: sp.ID, Space: sp.Slug, Title: in.Title, ContentMD: in.Content, Rationale: in.Rationale,
		ValidFrom: in.ValidFrom, ValidUntil: in.ValidUntil, Tags: normTags(in.Tags), Status: domain.ProposalOpen}
	p.Category = strings.ToLower(strings.TrimSpace(in.Category))
	if key := strings.TrimSpace(in.DocKey); key != "" {
		if !domain.ValidDocKey(key) {
			return store.Proposal{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "doc_key %q is invalid", key)
		}
		p.DocKey = key
		cur, found, err := store.GetDocument(ctx, s.db, sp.ID, key, false)
		if err != nil {
			return store.Proposal{}, err
		}
		if found {
			p.TargetDocID = cur.ID
			p.BaseVersion = cur.CurrentVersion
			if in.BaseVersion > 0 {
				p.BaseVersion = in.BaseVersion
			}
			if p.Category == "" {
				p.Category = cur.Category
			}
		} else if in.BaseVersion > 0 {
			return store.Proposal{}, domain.Errorf(domain.KindNotFound, domain.CodeNotFound, "document %q not found in space %s", key, sp.Slug)
		}
	}
	if p.Category == "" {
		p.Category = s.defaultCategory(sp, "")
	}
	if p.Category != "" && !containsStr(sp.Categories, p.Category) {
		return store.Proposal{}, domain.Errorf(domain.KindInvalid, domain.CodeCategory, "category %q is not allowed in space %s", p.Category, sp.Slug)
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if err := store.InsertProposal(ctx, tx, p, actorOf(id)); err != nil {
			return err
		}
		return store.InsertEvent(ctx, tx, store.EventWrite{SpaceID: sp.ID, SpaceSlug: sp.Slug, Action: domain.ActionProposalCreated, TargetKind: "proposal",
			TargetID: p.ID, Actor: actorOf(id), Details: map[string]any{"doc_key": p.DocKey, "title": p.Title, "base_version": p.BaseVersion,
				"content_sha256": ingest.SHA256Hex(p.ContentMD), "valid_from": p.ValidFrom, "valid_until": p.ValidUntil}})
	})
	if err != nil {
		return store.Proposal{}, err
	}
	out, _, err := store.GetProposal(ctx, s.db, p.ID, false)
	return out, err
}

// ProposalQuery filtra el listado de propuestas.
type ProposalQuery struct {
	Space  string
	Status string
	Mine   bool
	Before *time.Time
	Limit  int
}

// ListProposals: las propias (en espacios visibles) y, para quien tiene
// space.review, todas las de esos espacios (salvo mine=true).
func (s *Service) ListProposals(ctx context.Context, id authz.Identity, q ProposalQuery) ([]store.Proposal, error) {
	visible, err := s.eng.VisibleScopes(ctx, id, authz.ScopeSpace)
	if err != nil {
		return nil, err
	}
	want := strings.ToLower(strings.TrimSpace(q.Space))
	var reviewSlugs, ownSlugs []string
	for _, v := range visible {
		if want != "" && v.Scope.Key != want {
			continue
		}
		if containsCap(v.Capabilities, authz.CapSpaceRead) {
			ownSlugs = append(ownSlugs, v.Scope.Key)
		}
		if !q.Mine && containsCap(v.Capabilities, authz.CapSpaceReview) {
			reviewSlugs = append(reviewSlugs, v.Scope.Key)
		}
	}
	if want != "" && len(ownSlugs) == 0 {
		return nil, domain.Errorf(domain.KindNotFound, domain.CodeNotFound, "space %q not found", want)
	}
	rows, err := s.spacesFor(ctx, append(append([]string{}, ownSlugs...), reviewSlugs...), id.PrincipalID)
	if err != nil {
		return nil, err
	}
	ids := func(slugs []string) []string {
		out := []string{}
		for _, sl := range slugs {
			if sp, ok := rows[sl]; ok {
				out = append(out, sp.ID)
			}
		}
		return out
	}
	status := strings.TrimSpace(q.Status)
	switch status {
	case "", domain.ProposalOpen, domain.ProposalApproved, domain.ProposalDiscarded, domain.ProposalWithdrawn:
	default:
		return nil, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "status must be open, approved, discarded or withdrawn")
	}
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return store.ListProposals(ctx, s.db, store.ProposalFilter{ReviewSpaceIDs: ids(reviewSlugs), OwnSpaceIDs: ids(ownSlugs), AuthorID: id.PrincipalID,
		Status: status, Before: q.Before, Limit: limit})
}

// proposalFor carga la propuesta si la identidad puede verla (su autor o
// space.review en su espacio); si no, 404 sin revelar que existe.
func (s *Service) proposalFor(ctx context.Context, id authz.Identity, propID string) (store.Proposal, bool, error) {
	propID = strings.TrimSpace(propID)
	notFound := domain.Errorf(domain.KindNotFound, domain.CodeNotFound, "proposal %q not found", propID)
	if len(propID) != 36 {
		return store.Proposal{}, false, notFound
	}
	p, found, err := store.GetProposal(ctx, s.db, propID, false)
	if err != nil || !found {
		if err == nil {
			err = notFound
		}
		return store.Proposal{}, false, err
	}
	d, err := s.eng.Can(ctx, id, authz.SpaceScope(p.Space), authz.CapSpaceReview)
	if err != nil {
		return store.Proposal{}, false, err
	}
	reviewer := d.Allowed
	if !reviewer && (id.PrincipalID == "" || p.ProposedByID != id.PrincipalID) {
		return store.Proposal{}, false, notFound
	}
	if !reviewer {
		// El autor solo la ve mientras el espacio le siga siendo visible.
		if dr, err := s.eng.Can(ctx, id, authz.SpaceScope(p.Space), authz.CapSpaceRead); err != nil || !dr.Allowed {
			return store.Proposal{}, false, notFound
		}
	}
	return p, reviewer, nil
}

// GetProposal lee una propuesta (autor o revisor).
func (s *Service) GetProposal(ctx context.Context, id authz.Identity, propID string) (store.Proposal, error) {
	p, _, err := s.proposalFor(ctx, id, propID)
	return p, err
}

// ApproveInput es el cuerpo de :approve (todo opcional).
type ApproveInput struct {
	Content    *string    `json:"content,omitempty"` // contenido editado por quien revisa
	Title      *string    `json:"title,omitempty"`
	DocKey     string     `json:"doc_key,omitempty"` // clave del documento nuevo (si la propuesta no la trae)
	ValidFrom  *time.Time `json:"valid_from,omitempty"`
	ValidUntil *time.Time `json:"valid_until,omitempty"`
	Force      bool       `json:"force,omitempty"` // aprobar aunque el documento haya cambiado
	Note       string     `json:"note,omitempty"`
}

// ApproveResult es la respuesta de :approve.
type ApproveResult struct {
	Proposal store.Proposal `json:"proposal"`
	Result   DocResult      `json:"result"`
}

// ApproveProposal aprueba (space.review): publica la versión con origen
// proposal (autor de la propuesta y de la aprobación) y cierra la propuesta
// en la misma transacción.
func (s *Service) ApproveProposal(ctx context.Context, id authz.Identity, propID string, in ApproveInput) (ApproveResult, error) {
	started := time.Now().UTC()
	p, reviewer, err := s.proposalFor(ctx, id, propID)
	if err != nil {
		return ApproveResult{}, err
	}
	sp, err := s.authorize(ctx, id, p.Space, authz.CapSpaceReview, map[string]any{"action": "proposal.approve", "proposal_id": p.ID})
	if err != nil || !reviewer {
		if err == nil {
			err = domain.Errorf(domain.KindForbidden, domain.CodeForbidden, "space.review is required")
		}
		return ApproveResult{}, err
	}
	if p.Status != domain.ProposalOpen {
		return ApproveResult{}, domain.Errorf(domain.KindConflict, domain.CodeProposalState, "proposal is %s, not open", p.Status)
	}
	key := firstNonEmpty(p.DocKey, in.DocKey)
	if key == "" {
		key = "propuestas/" + ingest.Slugify(p.Title)
	}
	content, title := p.ContentMD, p.Title
	if in.Content != nil && strings.TrimSpace(*in.Content) != "" {
		content = *in.Content
	}
	if in.Title != nil && strings.TrimSpace(*in.Title) != "" {
		title = strings.TrimSpace(*in.Title)
	}
	validFrom, validUntil := p.ValidFrom, p.ValidUntil
	if in.ValidFrom != nil {
		validFrom = in.ValidFrom
	}
	if in.ValidUntil != nil {
		validUntil = in.ValidUntil
	}
	cur, found, err := store.GetDocument(ctx, s.db, sp.ID, key, false)
	if err != nil {
		return ApproveResult{}, err
	}
	if !in.Force {
		switch {
		case p.TargetDocID == "" && found:
			return ApproveResult{}, domain.Errorf(domain.KindConflict, domain.CodeVersionConflict,
				"document %q was created after the proposal (current version %d); review it and approve with force=true", key, cur.CurrentVersion)
		case p.TargetDocID != "" && found && cur.CurrentVersion != p.BaseVersion:
			return ApproveResult{}, domain.Errorf(domain.KindConflict, domain.CodeVersionConflict,
				"document %q changed since the proposal (proposal saw v%d, current is v%d); review it and approve with force=true", key, p.BaseVersion, cur.CurrentVersion)
		}
	}
	owner, tags, category := "", p.Tags, p.Category
	if found {
		owner = cur.OwnerLabel
		if len(tags) == 0 {
			tags = cur.Tags
		}
		if category == "" {
			category = cur.Category
		}
	}
	doc := PublishDoc{DocKey: key, Path: key + ".md", Format: domain.FormatMarkdown, Content: content, Title: title, Category: category, Tags: tags,
		Owner: owner, ValidFrom: validFrom, ValidUntil: validUntil, ChangeNote: firstNonEmpty(in.Note, p.Rationale),
		Origin: domain.Origin{Kind: domain.OriginProposal, ProposalID: p.ID, Author: p.ProposedBy, Approver: id.Label}, proposalID: p.ID}
	prep, rej := s.prepare(ctx, sp, doc)
	if rej != nil {
		return ApproveResult{}, domain.Errorf(domain.KindInvalid, firstNonEmpty(rej.Code, domain.CodeInvalid), "%s", rej.Reason)
	}
	var out ApproveResult
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		locked, ok, err := store.GetProposal(ctx, tx, p.ID, true)
		if err != nil {
			return err
		}
		if !ok || locked.Status != domain.ProposalOpen {
			return domain.Errorf(domain.KindConflict, domain.CodeProposalState, "proposal is no longer open")
		}
		if !in.Force && p.TargetDocID != "" {
			now, ok2, err := store.GetDocument(ctx, tx, sp.ID, key, true)
			if err != nil {
				return err
			}
			if ok2 && now.CurrentVersion != p.BaseVersion {
				return domain.Errorf(domain.KindConflict, domain.CodeVersionConflict, "document %q changed while approving (v%d)", key, now.CurrentVersion)
			}
		}
		res, err := s.writeVersion(ctx, tx, id, sp, prep)
		if err != nil {
			return err
		}
		docRow, _, err := store.GetDocument(ctx, tx, sp.ID, key, false)
		if err != nil {
			return err
		}
		versionID, err := store.VersionID(ctx, tx, docRow.ID, res.Version)
		if err != nil {
			return err
		}
		now, err := store.Now(ctx, tx)
		if err != nil {
			return err
		}
		if ok, err := store.DecideProposal(ctx, tx, p.ID, domain.ProposalApproved, actorOf(id), strings.TrimSpace(in.Note), versionID, now); err != nil {
			return err
		} else if !ok {
			return domain.Errorf(domain.KindConflict, domain.CodeProposalState, "proposal is no longer open")
		}
		details := map[string]any{"doc_key": key, "version": res.Version, "result": res.Status, "proposed_by": p.ProposedBy,
			"proposed_by_principal_id": p.ProposedByID, "self_approval": p.ProposedByID != "" && p.ProposedByID == id.PrincipalID,
			"edited": in.Content != nil, "forced": in.Force, "valid_from": validFrom, "valid_until": validUntil}
		if err := store.InsertEvent(ctx, tx, store.EventWrite{SpaceID: sp.ID, SpaceSlug: sp.Slug, Action: domain.ActionProposalApproved, TargetKind: "proposal",
			TargetID: p.ID, Actor: actorOf(id), Details: details, At: &now}); err != nil {
			return err
		}
		out.Result = res
		return nil
	})
	if err != nil {
		return ApproveResult{}, err
	}
	run := BatchResult{Space: sp.Slug, Results: []DocResult{out.Result}, Summary: map[string]int{out.Result.Status: 1}}
	s.recordRun(ctx, id, sp, runInfo{kind: "proposal", started: started, origin: domain.Origin{Kind: domain.OriginProposal, ProposalID: p.ID}}, &run)
	out.Proposal, _, err = store.GetProposal(ctx, s.db, p.ID, false)
	return out, err
}

// DiscardProposal descarta con motivo (space.review); se conserva (D-K8).
func (s *Service) DiscardProposal(ctx context.Context, id authz.Identity, propID, reason string) (store.Proposal, error) {
	p, reviewer, err := s.proposalFor(ctx, id, propID)
	if err != nil {
		return store.Proposal{}, err
	}
	sp, err := s.authorize(ctx, id, p.Space, authz.CapSpaceReview, map[string]any{"action": "proposal.discard", "proposal_id": p.ID})
	if err != nil || !reviewer {
		if err == nil {
			err = domain.Errorf(domain.KindForbidden, domain.CodeForbidden, "space.review is required")
		}
		return store.Proposal{}, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return store.Proposal{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "a reason is required to discard a proposal")
	}
	return s.closeProposal(ctx, id, sp, p, domain.ProposalDiscarded, domain.ActionProposalDiscarded, truncate(reason, 2000))
}

// WithdrawProposal: el autor retira su propuesta abierta.
func (s *Service) WithdrawProposal(ctx context.Context, id authz.Identity, propID, note string) (store.Proposal, error) {
	p, _, err := s.proposalFor(ctx, id, propID)
	if err != nil {
		return store.Proposal{}, err
	}
	if p.ProposedByID == "" || p.ProposedByID != id.PrincipalID {
		return store.Proposal{}, domain.Errorf(domain.KindForbidden, domain.CodeForbidden, "only the author can withdraw a proposal")
	}
	if !authz.TokenScopeAllows(id.TokenScope, authz.CapSpacePropose) {
		return store.Proposal{}, domain.Errorf(domain.KindForbidden, domain.CodeForbidden, "a write token is required")
	}
	sp, found, err := store.GetSpace(ctx, s.db, p.Space, false)
	if err != nil || !found {
		return store.Proposal{}, fmt.Errorf("kb: space of proposal: %v", err)
	}
	return s.closeProposal(ctx, id, sp, p, domain.ProposalWithdrawn, domain.ActionProposalWithdrawn, truncate(strings.TrimSpace(note), 2000))
}

func (s *Service) closeProposal(ctx context.Context, id authz.Identity, sp domain.Space, p store.Proposal, status, action, note string) (store.Proposal, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		now, err := store.Now(ctx, tx)
		if err != nil {
			return err
		}
		ok, err := store.DecideProposal(ctx, tx, p.ID, status, actorOf(id), note, "", now)
		if err != nil {
			return err
		}
		if !ok {
			return domain.Errorf(domain.KindConflict, domain.CodeProposalState, "proposal is %s, not open", p.Status)
		}
		return store.InsertEvent(ctx, tx, store.EventWrite{SpaceID: sp.ID, SpaceSlug: sp.Slug, Action: action, TargetKind: "proposal", TargetID: p.ID,
			Actor: actorOf(id), Details: map[string]any{"doc_key": p.DocKey, "title": p.Title, "note": note, "proposed_by": p.ProposedBy}, At: &now})
	})
	if err != nil {
		return store.Proposal{}, err
	}
	out, _, err := store.GetProposal(ctx, s.db, p.ID, false)
	return out, err
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
