package service

// Nexo IA · NX-10 entrega 2b — "qué cambió desde X" (RC-27, HU-38) y feed
// /sync para la copia local (C-09, KP-07).

import (
	"context"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// ChangesRequest pide los cambios entre dos fechas.
type ChangesRequest struct {
	Since  time.Time
	Until  *time.Time // nil = ahora
	Spaces []string
	Clock  string // valid (por defecto) o recorded
	Limit  int    // máximo 1000
}

// ChangesResult es la respuesta de /changes.
type ChangesResult struct {
	Since     time.Time             `json:"since"`
	Until     time.Time             `json:"until"`
	Clock     string                `json:"clock"`
	Changes   []store.SectionChange `json:"changes"`
	Summary   map[string]int        `json:"summary"`
	Truncated bool                  `json:"truncated"`
	NoChanges bool                  `json:"no_changes"`
}

// Changes lista las secciones agregadas, modificadas y retiradas entre dos
// fechas en los espacios visibles (space.read). Cada fila lleva su enlace.
func (s *Service) Changes(ctx context.Context, id authz.Identity, req ChangesRequest) (ChangesResult, error) {
	until := time.Now().UTC()
	if req.Until != nil {
		until = req.Until.UTC()
	}
	since := req.Since.UTC()
	if since.IsZero() {
		return ChangesResult{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "since is required")
	}
	if !since.Before(until) {
		return ChangesResult{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "since must be before until")
	}
	clock := strings.ToLower(strings.TrimSpace(req.Clock))
	switch clock {
	case "", store.ClockValid:
		clock = store.ClockValid
	case store.ClockRecorded:
	default:
		return ChangesResult{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "clock must be valid or recorded")
	}
	limit := req.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	ids, _, err := s.visibleSpaceIDs(ctx, id, req.Spaces)
	if err != nil {
		return ChangesResult{}, err
	}
	res := ChangesResult{Since: since, Until: until, Clock: clock, Changes: []store.SectionChange{}, Summary: map[string]int{}}
	if len(ids) > 0 {
		res.Changes, res.Truncated, err = store.ListSectionChanges(ctx, s.db, ids, since, until, clock, limit)
		if err != nil {
			return ChangesResult{}, err
		}
	}
	for _, c := range res.Changes {
		res.Summary[c.Change]++
	}
	res.NoChanges = len(res.Changes) == 0
	return res, nil
}

// ─── Feed /sync (C-09) ───────────────────────────────────────────────────────

// SyncSpace es un espacio visible con el rol efectivo.
type SyncSpace struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// SyncDocument es el estado final de un documento cambiado: su versión
// actual y las secciones vigentes y programadas con su vigencia (KP-07).
type SyncDocument struct {
	ID             string              `json:"id"`
	Space          string              `json:"space"`
	DocKey         string              `json:"doc_key"`
	Title          string              `json:"title"`
	Category       string              `json:"category"`
	Tags           []string            `json:"tags"`
	Owner          string              `json:"owner"`
	Status         string              `json:"status"`
	CurrentVersion int                 `json:"current_version"`
	ValidFrom      time.Time           `json:"valid_from"`
	ValidUntil     *time.Time          `json:"valid_until,omitempty"`
	SupersededBy   string              `json:"superseded_by,omitempty"`
	Sections       []store.FeedSection `json:"sections"`
}

// SyncChange es un cambio del feed.
type SyncChange struct {
	Seq      int64         `json:"seq"`
	Kind     string        `json:"kind"` // upsert | remove | space_archived | space_restored
	Space    string        `json:"space"`
	Document *SyncDocument `json:"document,omitempty"`
	DocID    string        `json:"document_id,omitempty"`
	DocKey   string        `json:"doc_key,omitempty"`
}

// SyncResult es la respuesta de /sync.
type SyncResult struct {
	Seq            int64        `json:"seq"`
	HasMore        bool         `json:"has_more"`
	ResyncRequired bool         `json:"resync_required"`
	ServerTime     time.Time    `json:"server_time"`
	VisibleSpaces  []SyncSpace  `json:"visible_spaces"`
	Changes        []SyncChange `json:"changes"`
	// PublicURL (tanda 2): URL pública de Nexo para que la copia local arme
	// los enlaces permanentes de las citas sin conexión (RC-08). La completa
	// la API.
	PublicURL string `json:"public_url,omitempty"`
}

// Sync devuelve los espacios visibles y los cambios desde since (design §6.4
// y KP-07). La copia local borra los espacios que no estén en visible_spaces
// y, para cada documento con upsert, reemplaza sus secciones por las
// devueltas (vigentes y programadas, con su vigencia); para remove, lo borra.
// Un documento con varios cambios en la página se entrega una sola vez, con
// su estado final.
func (s *Service) Sync(ctx context.Context, id authz.Identity, since int64, limit int) (SyncResult, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	if since < 0 {
		return SyncResult{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "since must be >= 0")
	}
	visible, err := s.eng.VisibleScopes(ctx, id, authz.ScopeSpace)
	if err != nil {
		return SyncResult{}, err
	}
	var slugs []string
	roles := map[string]string{}
	for _, v := range visible {
		if !containsCap(v.Capabilities, authz.CapSpaceRead) {
			continue
		}
		slugs = append(slugs, v.Scope.Key)
		roles[v.Scope.Key] = string(v.Role)
	}
	rows, err := s.spacesFor(ctx, slugs, id.PrincipalID)
	if err != nil {
		return SyncResult{}, err
	}
	res := SyncResult{ServerTime: time.Now().UTC(), VisibleSpaces: []SyncSpace{}, Changes: []SyncChange{}}
	ids := []string{}
	slugByID := map[string]string{}
	for _, slug := range slugs {
		if sp, ok := rows[slug]; ok {
			res.VisibleSpaces = append(res.VisibleSpaces, SyncSpace{ID: sp.ID, Slug: sp.Slug, Name: sp.Name, Role: roles[slug]})
			ids = append(ids, sp.ID)
			slugByID[sp.ID] = sp.Slug
		}
	}
	maxSeq, err := store.MaxSeq(ctx, s.db)
	if err != nil {
		return SyncResult{}, err
	}
	if since > maxSeq {
		// El cliente viene de otra base o de un futuro imposible: reconstruir.
		res.ResyncRequired, res.Seq = true, maxSeq
		return res, nil
	}
	res.Seq = since
	if len(ids) == 0 {
		res.Seq = maxSeq
		return res, nil
	}
	changes, err := store.ListFeedChanges(ctx, s.db, ids, since, limit+1)
	if err != nil {
		return SyncResult{}, err
	}
	if len(changes) > limit {
		changes, res.HasMore = changes[:limit], true
	}
	// Último cambio de cada documento en la página.
	last := map[string]int{}
	for i, c := range changes {
		if c.DocumentID != "" {
			last[c.DocumentID] = i
		}
	}
	for i, c := range changes {
		res.Seq = c.Seq
		if c.DocumentID != "" && last[c.DocumentID] != i {
			continue
		}
		sc := SyncChange{Seq: c.Seq, Kind: c.Kind, Space: slugByID[c.SpaceID], DocID: c.DocumentID}
		if c.DocumentID != "" {
			d, found, err := store.GetDocumentByID(ctx, s.db, c.DocumentID)
			if err != nil {
				return SyncResult{}, err
			}
			if !found {
				continue
			}
			sc.DocKey = d.DocKey
			secs, err := store.OpenSections(ctx, s.db, d.ID)
			if err != nil {
				return SyncResult{}, err
			}
			if d.Status == domain.DocRetired || len(secs) == 0 {
				sc.Kind = domain.ChangeRemove
			} else {
				sc.Kind = domain.ChangeUpsert
				sc.Document = &SyncDocument{ID: d.ID, Space: d.Space, DocKey: d.DocKey, Title: d.Title, Category: d.Category, Tags: d.Tags,
					Owner: d.OwnerLabel, Status: d.Status, CurrentVersion: d.CurrentVersion, ValidFrom: d.ValidFrom, ValidUntil: d.ValidUntil,
					SupersededBy: d.SupersededBy, Sections: secs}
			}
		}
		res.Changes = append(res.Changes, sc)
	}
	if !res.HasMore && maxSeq > res.Seq {
		// Sin más cambios visibles: el cursor avanza al último del feed (los
		// cambios de espacios no visibles no se entregan).
		res.Seq = maxSeq
	}
	return res, nil
}

func containsCap(cs []authz.Capability, c authz.Capability) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}
