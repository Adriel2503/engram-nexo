package service

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// ─── Retirar y reemplazar (RF-14, HU-30, HU-08) ──────────────────────────────

// RetireDocument retira un documento: deja de estar vigente desde ahora, se
// conserva con todo su historial y no vuelve a aparecer como vigente.
func (s *Service) RetireDocument(ctx context.Context, id authz.Identity, slug, docKey, note string) (domain.Document, error) {
	sp, err := s.authorize(ctx, id, slug, authz.CapSpacePublish, map[string]any{"action": "document.retire", "doc_key": docKey})
	if err != nil {
		return domain.Document{}, err
	}
	return s.retire(ctx, id, sp, docKey, "", note, nil)
}

// SupersedeDocument retira un documento indicando cuál lo reemplaza (otro
// documento activo del mismo espacio).
func (s *Service) SupersedeDocument(ctx context.Context, id authz.Identity, slug, docKey, byKey, note string) (domain.Document, error) {
	sp, err := s.authorize(ctx, id, slug, authz.CapSpacePublish, map[string]any{"action": "document.supersede", "doc_key": docKey, "by": byKey})
	if err != nil {
		return domain.Document{}, err
	}
	byKey = strings.TrimSpace(byKey)
	if byKey == "" {
		return domain.Document{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "by (doc_key of the replacement) is required")
	}
	if byKey == strings.TrimSpace(docKey) {
		return domain.Document{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "a document cannot replace itself")
	}
	return s.retire(ctx, id, sp, docKey, byKey, note, nil)
}

func (s *Service) retire(ctx context.Context, id authz.Identity, sp domain.Space, docKey, byKey, note string, extra map[string]any) (domain.Document, error) {
	docKey = strings.TrimSpace(docKey)
	var out domain.Document
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		now, err := store.Now(ctx, tx)
		if err != nil {
			return err
		}
		cur, found, err := store.GetDocument(ctx, tx, sp.ID, docKey, true)
		if err != nil {
			return err
		}
		if !found {
			return domain.Errorf(domain.KindNotFound, domain.CodeNotFound, "document %q not found in space %s", docKey, sp.Slug)
		}
		replacementID := ""
		if byKey != "" {
			repl, ok, err := store.GetDocument(ctx, tx, sp.ID, byKey, false)
			if err != nil {
				return err
			}
			if !ok {
				return domain.Errorf(domain.KindNotFound, domain.CodeNotFound, "replacement document %q not found in space %s", byKey, sp.Slug)
			}
			if repl.Status != domain.DocActive {
				return domain.Errorf(domain.KindConflict, domain.CodeConflict, "replacement document %q is retired", byKey)
			}
			replacementID = repl.ID
		}
		if cur.Status == domain.DocRetired {
			if cur.SupersededBy == byKey {
				out = cur.Document // idempotente: sin evento
				return nil
			}
			if byKey == "" || cur.SupersededBy != "" {
				return domain.Errorf(domain.KindConflict, domain.CodeConflict, "document %q is already retired", docKey)
			}
			// Retirado sin reemplazo: se puede indicar el reemplazo después.
		} else if err := store.CloseCurrentSections(ctx, tx, cur.ID, now, now); err != nil {
			return err
		}
		if err := store.RetireDocument(ctx, tx, cur.ID, replacementID, note, now); err != nil {
			return err
		}
		if err := store.InsertChange(ctx, tx, sp.ID, cur.ID, domain.ChangeRemove, now); err != nil {
			return err
		}
		action := domain.ActionDocumentRetired
		details := map[string]any{"doc_key": docKey, "version": cur.CurrentVersion, "note": note}
		if byKey != "" {
			action = domain.ActionDocumentSuperseded
			details["superseded_by"] = byKey
		}
		for k, v := range extra {
			details[k] = v
		}
		if err := store.InsertEvent(ctx, tx, store.EventWrite{SpaceID: sp.ID, SpaceSlug: sp.Slug, Action: action, TargetKind: "document",
			TargetID: docKey, Actor: actorOf(id), Details: details, At: &now}); err != nil {
			return err
		}
		updated, _, err := store.GetDocument(ctx, tx, sp.ID, docKey, false)
		out = updated.Document
		return err
	})
	return out, err
}

// ─── Lectura (RF-22, RF-24) ──────────────────────────────────────────────────

// SectionRef es el índice de secciones de un documento.
type SectionRef struct {
	Anchor         string                `json:"anchor"`
	Ordinal        int                   `json:"ordinal"`
	HeadingPath    []string              `json:"heading_path"`
	TokenCount     int                   `json:"token_count"`
	SourceLocation domain.SourceLocation `json:"source_location"`
	Extraction     string                `json:"extraction"`
}

// DocumentView es la lectura de un documento en una versión.
type DocumentView struct {
	Document domain.Document `json:"document"`
	Version  domain.Version  `json:"version"`
	Sections []SectionRef    `json:"sections"`
	// Notice indica si lo leído no es la versión vigente o el documento está
	// retirado y por cuál se reemplazó (HU-08).
	Notice string `json:"notice,omitempty"`
}

// ListDocuments requiere space.read. Cursor = último doc_key recibido.
func (s *Service) ListDocuments(ctx context.Context, id authz.Identity, slug string, q store.DocQuery) ([]domain.Document, error) {
	sp, err := s.authorize(ctx, id, slug, authz.CapSpaceRead, nil)
	if err != nil {
		return nil, err
	}
	if q.Status != "" && q.Status != domain.DocActive && q.Status != domain.DocRetired {
		return nil, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "status must be active or retired")
	}
	return store.ListDocuments(ctx, s.db, sp.ID, q)
}

func (s *Service) docFor(ctx context.Context, id authz.Identity, slug, docKey string) (domain.Space, store.DocRow, error) {
	sp, err := s.authorize(ctx, id, slug, authz.CapSpaceRead, nil)
	if err != nil {
		return domain.Space{}, store.DocRow{}, err
	}
	d, found, err := store.GetDocument(ctx, s.db, sp.ID, strings.TrimSpace(docKey), false)
	if err != nil {
		return domain.Space{}, store.DocRow{}, err
	}
	if !found {
		return domain.Space{}, store.DocRow{}, domain.Errorf(domain.KindNotFound, domain.CodeNotFound, "document %q not found in space %s", docKey, sp.Slug)
	}
	return sp, d, nil
}

// GetDocument lee un documento completo en su versión actual (version = 0) o
// en una versión dada.
func (s *Service) GetDocument(ctx context.Context, id authz.Identity, slug, docKey string, version int) (DocumentView, error) {
	_, d, err := s.docFor(ctx, id, slug, docKey)
	if err != nil {
		return DocumentView{}, err
	}
	v, found, err := store.GetVersion(ctx, s.db, d.ID, version)
	if err != nil {
		return DocumentView{}, err
	}
	if !found {
		return DocumentView{}, domain.Errorf(domain.KindNotFound, domain.CodeVersion, "version %d of %q not found", version, docKey)
	}
	secs, err := store.ListSections(ctx, s.db, v.ID)
	if err != nil {
		return DocumentView{}, err
	}
	refs := make([]SectionRef, 0, len(secs))
	for _, sec := range secs {
		refs = append(refs, SectionRef{Anchor: sec.Anchor, Ordinal: sec.Ordinal, HeadingPath: sec.HeadingPath, TokenCount: sec.TokenCount,
			SourceLocation: sec.SourceLocation, Extraction: sec.Extraction})
	}
	return DocumentView{Document: d.Document, Version: v, Sections: refs, Notice: notice(d.Document, v.Version, time.Now())}, nil
}

// notice explica el estado para HU-08 y HU-20 c2.
func notice(d domain.Document, version int, now time.Time) string {
	var parts []string
	if d.Status == domain.DocRetired {
		msg := "documento retirado"
		if d.RetiredAt != nil {
			msg += " el " + d.RetiredAt.Format("2006-01-02")
		}
		if d.SupersededBy != "" {
			msg += "; reemplazado por " + d.SupersededBy
		}
		parts = append(parts, msg+": no está vigente")
	} else {
		if d.ValidFrom.After(now) {
			parts = append(parts, "vigente desde "+d.ValidFrom.Format("2006-01-02")+" (todavía no rige)")
		}
		if d.ValidUntil != nil && !d.ValidUntil.After(now) {
			parts = append(parts, "vigencia vencida el "+d.ValidUntil.Format("2006-01-02"))
		}
	}
	if version != d.CurrentVersion {
		parts = append(parts, "versión histórica (la vigente es la "+strconv.Itoa(d.CurrentVersion)+")")
	}
	return strings.Join(parts, "; ")
}

// ListVersions devuelve el historial con autor y origen (HU-20 c2).
func (s *Service) ListVersions(ctx context.Context, id authz.Identity, slug, docKey string, before, limit int) ([]domain.Version, error) {
	_, d, err := s.docFor(ctx, id, slug, docKey)
	if err != nil {
		return nil, err
	}
	return store.ListVersions(ctx, s.db, d.ID, before, limit)
}

// SectionView es la lectura de una sección por ancla.
type SectionView struct {
	Space    string         `json:"space"`
	DocKey   string         `json:"doc_key"`
	Title    string         `json:"title"`
	Category string         `json:"category"`
	Owner    string         `json:"owner"`
	Version  int            `json:"version"`
	Section  domain.Section `json:"section"`
	Citation string         `json:"citation"`
	Status   string         `json:"document_status"`
	Notice   string         `json:"notice,omitempty"`
}

// GetSection lee una sección por su ancla, en la versión vigente (0) o dada.
func (s *Service) GetSection(ctx context.Context, id authz.Identity, slug, docKey, anchor string, version int) (SectionView, error) {
	sp, d, err := s.docFor(ctx, id, slug, docKey)
	if err != nil {
		return SectionView{}, err
	}
	v, found, err := store.GetVersion(ctx, s.db, d.ID, version)
	if err != nil {
		return SectionView{}, err
	}
	if !found {
		return SectionView{}, domain.Errorf(domain.KindNotFound, domain.CodeVersion, "version %d of %q not found", version, docKey)
	}
	sec, found, err := store.GetSection(ctx, s.db, v.ID, strings.TrimSpace(anchor))
	if err != nil {
		return SectionView{}, err
	}
	if !found {
		return SectionView{}, domain.Errorf(domain.KindNotFound, domain.CodeAnchor, "section %q not found in %s v%d", anchor, docKey, v.Version)
	}
	h := search.Hit{Space: sp.Slug, Title: v.Title, DocKey: d.DocKey, HeadingPath: sec.HeadingPath, Anchor: sec.Anchor, Version: v.Version,
		ValidFrom: sec.ValidFrom, ValidUntil: sec.ValidUntil, SourceLocation: sec.SourceLocation}
	return SectionView{Space: sp.Slug, DocKey: d.DocKey, Title: v.Title, Category: v.Category, Owner: v.OwnerLabel, Version: v.Version,
		Section: sec, Citation: search.Citation(h), Status: d.Status, Notice: notice(d.Document, v.Version, time.Now())}, nil
}

// ─── Búsqueda (RF-20..RF-24) ─────────────────────────────────────────────────

// SearchRequest es una búsqueda.
type SearchRequest struct {
	Q        string
	Spaces   []string
	Category string
	Tag      string
	At       *time.Time
	KnownAt  *time.Time
	Limit    int
}

// Search busca en los espacios visibles (o en los pedidos que sean visibles).
// Los filtros de permisos y de tiempo los aplica search.Searcher, sea cual
// sea el motor.
func (s *Service) Search(ctx context.Context, id authz.Identity, req SearchRequest) (search.Result, error) {
	ids, _, err := s.visibleSpaceIDs(ctx, id, req.Spaces)
	if err != nil {
		return search.Result{}, err
	}
	return s.searcher.Search(ctx, search.Query{Text: req.Q, Category: strings.ToLower(strings.TrimSpace(req.Category)),
		Tag: strings.ToLower(strings.TrimSpace(req.Tag)), Limit: req.Limit}, ids, req.At, req.KnownAt)
}
