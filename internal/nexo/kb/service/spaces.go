package service

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

const denyRecordTimeout = 3 * time.Second

// CreateSpaceInput son los datos para crear un espacio (RF-01).
type CreateSpaceInput struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Visibility  string   `json:"visibility"`   // "" = open (D-K4)
	DefaultRole string   `json:"default_role"` // "" = lector
	Owner       string   `json:"owner"`        // responsable inicial (id o usuario)
	Categories  []string `json:"categories"`   // vacío = por defecto
}

// CreateSpace crea el ámbito en nexo_scopes con su responsable (motor de
// roles: platform.scope.create + platform.scope.assign_owner, R-9, R-10,
// R-11) y la fila de nexo_kb_spaces con su evento. Son dos transacciones:
// si la segunda falla, la fila del espacio se completa sola en el primer uso
// (loadSpace) y el ámbito queda bien formado.
func (s *Service) CreateSpace(ctx context.Context, id authz.Identity, in CreateSpaceInput) (domain.Space, error) {
	slug := strings.ToLower(strings.TrimSpace(in.Slug))
	if !domain.ValidSlug(slug) {
		return domain.Space{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "slug must match ^[a-z0-9][a-z0-9-]{1,62}$")
	}
	cats := domain.DefaultCategories
	if len(in.Categories) > 0 {
		var err error
		if cats, err = validateCategories(in.Categories); err != nil {
			return domain.Space{}, err
		}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = slug
	}
	vis := strings.TrimSpace(in.Visibility)
	if vis == "" {
		vis = authz.VisibilityOpen
	}
	if _, err := s.roles.CreateScope(ctx, id, authz.CreateScopeInput{Type: authz.ScopeSpace, Key: slug, Visibility: vis,
		DefaultRole: authz.Role(strings.TrimSpace(in.DefaultRole)), Owner: in.Owner}); err != nil {
		return domain.Space{}, mapAuthzError(err)
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := store.EnsureSpace(ctx, tx, slug, name, strings.TrimSpace(in.Description), cats, id.PrincipalID); err != nil {
			return err
		}
		sp, _, err := store.GetSpace(ctx, tx, slug, false)
		if err != nil {
			return err
		}
		return store.InsertEvent(ctx, tx, store.EventWrite{SpaceID: sp.ID, SpaceSlug: slug, Action: domain.ActionSpaceCreated, TargetKind: "space",
			TargetID: slug, Actor: actorOf(id), Details: map[string]any{"name": name, "visibility": sp.Visibility, "default_role": sp.DefaultRole,
				"owner": in.Owner, "categories": cats}})
	})
	if err != nil {
		return domain.Space{}, wrapf(err, "kb: create space %s", slug)
	}
	authz.InvalidateCache(ctx)
	// Quien crea (admin de plataforma) puede no tener rol en un espacio
	// restringido: se devuelve el espacio con su rol efectivo (posiblemente
	// ninguno), sin exigir space.read.
	sp, err := s.loadSpace(ctx, slug, id.PrincipalID)
	if err != nil {
		return domain.Space{}, err
	}
	if role, err := s.eng.EffectiveRole(ctx, id, authz.SpaceScope(slug)); err == nil && role != authz.RoleNone {
		sp.Role = string(role)
		sp.Capabilities = capsOf(authz.Capabilities(authz.ScopeSpace, role, id.TokenScope))
	}
	return sp, nil
}

// ListSpaces devuelve los espacios visibles con el rol efectivo (RF-05). Un
// espacio restringido sin rol no aparece (CA-K02).
func (s *Service) ListSpaces(ctx context.Context, id authz.Identity) ([]domain.Space, error) {
	visible, err := s.eng.VisibleScopes(ctx, id, authz.ScopeSpace)
	if err != nil {
		return nil, err
	}
	slugs := make([]string, 0, len(visible))
	for _, v := range visible {
		slugs = append(slugs, v.Scope.Key)
	}
	rows, err := s.spacesFor(ctx, slugs, id.PrincipalID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Space, 0, len(visible))
	for _, v := range visible {
		sp, ok := rows[v.Scope.Key]
		if !ok {
			continue
		}
		sp.Role = string(v.Role)
		sp.Capabilities = capsOf(v.Capabilities)
		out = append(out, sp)
	}
	return out, nil
}

// spacesFor devuelve las filas de nexo_kb_spaces de los slugs, creando las
// que falten (ámbitos creados por la API de roles).
func (s *Service) spacesFor(ctx context.Context, slugs []string, actorID string) (map[string]domain.Space, error) {
	rows, err := store.SpacesBySlugs(ctx, s.db, slugs)
	if err != nil {
		return nil, err
	}
	missing := false
	for _, slug := range slugs {
		if _, ok := rows[slug]; !ok {
			missing = true
			if _, err := store.EnsureSpace(ctx, s.db, slug, slug, "", nil, actorID); err != nil {
				return nil, err
			}
		}
	}
	if missing {
		return store.SpacesBySlugs(ctx, s.db, slugs)
	}
	return rows, nil
}

// visibleSpaceIDs: ids de los espacios visibles con space.read, cortados a
// los pedidos (un slug pedido que no es visible se ignora sin revelarlo).
func (s *Service) visibleSpaceIDs(ctx context.Context, id authz.Identity, requested []string) ([]string, map[string]string, error) {
	visible, err := s.eng.VisibleScopes(ctx, id, authz.ScopeSpace)
	if err != nil {
		return nil, nil, err
	}
	want := map[string]bool{}
	for _, r := range requested {
		if r = strings.ToLower(strings.TrimSpace(r)); r != "" {
			want[r] = true
		}
	}
	var slugs []string
	for _, v := range visible {
		if !slices.Contains(v.Capabilities, authz.CapSpaceRead) {
			continue
		}
		if len(want) > 0 && !want[v.Scope.Key] {
			continue
		}
		slugs = append(slugs, v.Scope.Key)
	}
	rows, err := s.spacesFor(ctx, slugs, id.PrincipalID)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, 0, len(rows))
	bySlug := map[string]string{}
	for _, slug := range slugs {
		if sp, ok := rows[slug]; ok {
			ids = append(ids, sp.ID)
			bySlug[slug] = sp.ID
		}
	}
	return ids, bySlug, nil
}

// GetSpace requiere space.read (404 si no es visible).
func (s *Service) GetSpace(ctx context.Context, id authz.Identity, slug string) (domain.Space, error) {
	return s.authorize(ctx, id, slug, authz.CapSpaceRead, nil)
}

// UpdateSpaceInput son los cambios de configuración (nil = sin cambio).
type UpdateSpaceInput struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Categories  *[]string       `json:"categories"`
	Settings    *map[string]any `json:"settings"`
	Visibility  *string         `json:"visibility"`
	DefaultRole *string         `json:"default_role"`
}

// UpdateSpace (RF-02) requiere space.configure. El slug no cambia.
// Visibilidad y rol implícito se guardan en nexo_scopes por el motor de roles.
func (s *Service) UpdateSpace(ctx context.Context, id authz.Identity, slug string, in UpdateSpaceInput) (domain.Space, error) {
	sp, err := s.authorize(ctx, id, slug, authz.CapSpaceConfigure, map[string]any{"action": "space.update"})
	if err != nil {
		return domain.Space{}, err
	}
	if in.Name == nil && in.Description == nil && in.Categories == nil && in.Settings == nil && in.Visibility == nil && in.DefaultRole == nil {
		return domain.Space{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "nothing to update")
	}
	var cats []string
	if in.Categories != nil {
		if cats, err = validateCategories(*in.Categories); err != nil {
			return domain.Space{}, err
		}
	}
	if in.Visibility != nil || in.DefaultRole != nil {
		upd := authz.UpdateScopeInput{Visibility: in.Visibility}
		if in.DefaultRole != nil {
			r := authz.Role(strings.TrimSpace(*in.DefaultRole))
			upd.DefaultRole = &r
		}
		if _, err := s.roles.UpdateScope(ctx, id, authz.SpaceScope(sp.Slug), upd); err != nil {
			return domain.Space{}, mapAuthzError(err)
		}
	}
	if in.Name != nil || in.Description != nil || in.Categories != nil || in.Settings != nil {
		err = s.inTx(ctx, func(tx *sql.Tx) error {
			before, _, err := store.GetSpace(ctx, tx, sp.Slug, true)
			if err != nil {
				return err
			}
			after := before
			if in.Name != nil {
				after.Name = strings.TrimSpace(*in.Name)
			}
			if in.Description != nil {
				after.Description = strings.TrimSpace(*in.Description)
			}
			if in.Categories != nil {
				after.Categories = cats
			}
			if in.Settings != nil {
				after.Settings = *in.Settings
				if after.Settings == nil {
					after.Settings = map[string]any{}
				}
			}
			if err := store.UpdateSpace(ctx, tx, before.ID, after.Name, after.Description, after.Categories, after.Settings); err != nil {
				return err
			}
			return store.InsertEvent(ctx, tx, store.EventWrite{SpaceID: before.ID, SpaceSlug: sp.Slug, Action: domain.ActionSpaceUpdated, TargetKind: "space",
				TargetID: sp.Slug, Actor: actorOf(id), Details: map[string]any{
					"before": map[string]any{"name": before.Name, "description": before.Description, "categories": before.Categories, "settings": before.Settings},
					"after":  map[string]any{"name": after.Name, "description": after.Description, "categories": after.Categories, "settings": after.Settings},
				}})
		})
		if err != nil {
			return domain.Space{}, err
		}
	} else {
		// Solo visibilidad o rol implícito: el motor de roles ya registró
		// scope.updated; el espacio deja constancia en su propio registro.
		_ = store.InsertEvent(ctx, s.db, store.EventWrite{SpaceID: sp.ID, SpaceSlug: sp.Slug, Action: domain.ActionSpaceUpdated, TargetKind: "space",
			TargetID: sp.Slug, Actor: actorOf(id), Details: map[string]any{"visibility": deref(in.Visibility), "default_role": deref(in.DefaultRole)}})
	}
	authz.InvalidateCache(ctx)
	return s.GetSpace(ctx, id, sp.Slug)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// SetArchived archiva o restaura un espacio (RF-03, platform.scope.archive):
// deja de aparecer en listados y búsquedas (VisibleScopes solo devuelve
// ámbitos activos) y conserva todo su contenido.
func (s *Service) SetArchived(ctx context.Context, id authz.Identity, slug string, archived bool) (domain.Space, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !domain.ValidSlug(slug) {
		return domain.Space{}, domain.Errorf(domain.KindNotFound, domain.CodeNotFound, "space %q not found", slug)
	}
	before, found, err := store.GetSpace(ctx, s.db, slug, false)
	if err != nil {
		return domain.Space{}, err
	}
	info, err := s.roles.SetScopeArchived(ctx, id, authz.SpaceScope(slug), archived)
	if err != nil {
		return domain.Space{}, mapAuthzError(err)
	}
	sp, err := s.loadSpace(ctx, slug, id.PrincipalID)
	if err != nil {
		return domain.Space{}, err
	}
	if !found || before.Status != info.Status {
		action, change := domain.ActionSpaceRestored, domain.ChangeSpaceRestored
		if archived {
			action, change = domain.ActionSpaceArchived, domain.ChangeSpaceArchived
		}
		err = s.inTx(ctx, func(tx *sql.Tx) error {
			now, err := store.Now(ctx, tx)
			if err != nil {
				return err
			}
			if err := store.InsertChange(ctx, tx, sp.ID, "", change, now); err != nil {
				return err
			}
			return store.InsertEvent(ctx, tx, store.EventWrite{SpaceID: sp.ID, SpaceSlug: slug, Action: action, TargetKind: "space", TargetID: slug,
				Actor: actorOf(id), Details: map[string]any{"status_before": before.Status, "status_after": info.Status}})
		})
		if err != nil {
			return domain.Space{}, err
		}
	}
	authz.InvalidateCache(ctx)
	return sp, nil
}

// ListEvents requiere space.audit.read (publicador y responsable; también en
// un espacio archivado) o platform.audit.read (superadmin, admin y auditor
// leen los eventos de todos los espacios, RF-51). Cursor = id del último
// evento recibido.
func (s *Service) ListEvents(ctx context.Context, id authz.Identity, slug string, beforeID int64, action string, limit int) ([]domain.Event, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if d, err := s.eng.Can(ctx, id, authz.PlatformScope(), authz.CapPlatformAuditRead); err == nil && d.Allowed && domain.ValidSlug(slug) {
		if _, found, err := store.GetSpace(ctx, s.db, slug, false); err != nil {
			return nil, err
		} else if found {
			return store.ListEvents(ctx, s.db, slug, beforeID, strings.TrimSpace(action), limit)
		}
	}
	sp, err := s.authorize(ctx, id, slug, authz.CapSpaceAuditRead, map[string]any{"action": "events.read"})
	if err != nil {
		return nil, err
	}
	return store.ListEvents(ctx, s.db, sp.Slug, beforeID, strings.TrimSpace(action), limit)
}
