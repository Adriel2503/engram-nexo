package authz

// Casos de uso de administración de roles (API /nexo/authz/v1). Cada método
// autoriza al actor con el motor, aplica las reglas R-1..R-11 dentro de una
// transacción y registra los eventos (R-14). Las denegaciones generan
// authz.denied.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Códigos de error de reglas (la API los devuelve en error_code).
const (
	CodeInvalid            = "invalid_request"
	CodeNotFound           = "not_found"
	CodeLastSuperadmin     = "last_superadmin"      // R-8
	CodeLastOwner          = "last_owner"           // R-9
	CodeServiceAccountRole = "service_account_role" // R-10
	CodeSelfPromotion      = "self_promotion"       // R-11
	CodeScopeExists        = "scope_exists"
	CodeScopeHasOwner      = "scope_has_owner"
	CodePrincipalDisabled  = "principal_disabled"
)

// RuleError es un rechazo por validación o por una regla de negocio.
type RuleError struct {
	Code    string
	Message string
}

func (e *RuleError) Error() string { return e.Code + ": " + e.Message }

func ruleErr(code, format string, args ...any) *RuleError {
	return &RuleError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// DeniedError es una denegación del motor (403, o 404 si Decision.Hidden).
type DeniedError struct {
	Scope      Scope
	Capability Capability
	Decision   Decision
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf("forbidden: %s requires %s (%s)", e.Scope, e.Capability, e.Decision.Reason)
}

// Service agrupa los casos de uso.
type Service struct {
	db    *sql.DB
	eng   *Engine
	store *PGStore
	// saStaleDays: días sin uso para marcar una cuenta de servicio
	// (NEXO_SA_STALE_DAYS; 0 = sin marca). Ver WithServiceAccountStaleDays.
	saStaleDays int
}

// NewService crea el servicio sobre la base de Engram Cloud.
func NewService(db *sql.DB, eng *Engine) *Service {
	return &Service{db: db, eng: eng, store: NewPGStore(db)}
}

// Engine devuelve el motor del servicio.
func (s *Service) Engine() *Engine { return s.eng }

func (s *Service) require(ctx context.Context, actor Identity, sc Scope, c Capability, details map[string]any) error {
	d, err := s.eng.Require(ctx, actor, sc, c, details)
	if err != nil {
		return err
	}
	if !d.Allowed {
		return &DeniedError{Scope: sc, Capability: c, Decision: d}
	}
	return nil
}

// reject registra la regla incumplida como authz.denied y devuelve el error.
func (s *Service) reject(ctx context.Context, actor Identity, sc Scope, c Capability, target string, e *RuleError) error {
	s.eng.RecordDenied(ctx, actor, sc, c, Decision{Reason: e.Code}, map[string]any{"rule": e.Code, "target_principal_id": target, "message": e.Message})
	return e
}

func (s *Service) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	InvalidateCache(ctx)
	return nil
}

// principalRow es un principal leído (y bloqueado) para una escritura.
type principalRow struct {
	ID         string
	Kind       string
	Enabled    bool
	EngramRole string
	Label      string
}

// ResolvePrincipal acepta el id numérico, el nombre de usuario o, desde
// SG-10, el nombre corto (slug) de una cuenta de servicio.
func (s *Service) ResolvePrincipal(ctx context.Context, ref string) (principalRow, error) {
	return resolvePrincipal(ctx, s.db, ref, false)
}

func resolvePrincipal(ctx context.Context, q queryer, ref string, forUpdate bool) (principalRow, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return principalRow{}, ruleErr(CodeInvalid, "principal is required")
	}
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE OF p"
	}
	var row principalRow
	var err error
	if n, perr := strconv.ParseInt(ref, 10, 64); perr == nil {
		err = q.QueryRowContext(ctx, `SELECT p.id::text, p.kind, p.enabled, p.role, p.display_name FROM cloud_principals p WHERE p.id = $1`+lock, n).
			Scan(&row.ID, &row.Kind, &row.Enabled, &row.EngramRole, &row.Label)
	} else {
		err = q.QueryRowContext(ctx, `SELECT p.id::text, p.kind, p.enabled, p.role, p.display_name FROM cloud_principals p JOIN cloud_human_users h ON h.principal_id = p.id WHERE h.username = $1`+lock, ref).
			Scan(&row.ID, &row.Kind, &row.Enabled, &row.EngramRole, &row.Label)
		if errors.Is(err, sql.ErrNoRows) {
			row, err = resolveServiceAccountSlug(ctx, q, ref, lock) // SG-10: nombre corto de una cuenta de servicio
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return principalRow{}, ruleErr(CodeNotFound, "principal %q not found", ref)
	}
	if err != nil {
		return principalRow{}, err
	}
	return row, nil
}

// ─── Mis roles (RF-05) ───────────────────────────────────────────────────────

// MeView es la respuesta de GET /me.
type MeView struct {
	PrincipalID string      `json:"principal_id"`
	Kind        string      `json:"kind"`
	Label       string      `json:"label"`
	TokenID     string      `json:"token_id,omitempty"`
	TokenScope  string      `json:"token_scope"`
	Platform    ScopeRole   `json:"platform"`
	Projects    []ScopeRole `json:"projects"`
	Spaces      []ScopeRole `json:"spaces"`
}

// Me devuelve los roles de la identidad en todos los niveles (self.read).
func (s *Service) Me(ctx context.Context, actor Identity) (MeView, error) {
	if err := s.require(ctx, actor, PlatformScope(), CapSelfRead, nil); err != nil {
		return MeView{}, err
	}
	plat, err := s.eng.VisibleScopes(ctx, actor, ScopePlatform)
	if err != nil {
		return MeView{}, err
	}
	projects, err := s.eng.VisibleScopes(ctx, actor, ScopeProject)
	if err != nil {
		return MeView{}, err
	}
	spaces, err := s.eng.VisibleScopes(ctx, actor, ScopeSpace)
	if err != nil {
		return MeView{}, err
	}
	view := MeView{PrincipalID: actor.PrincipalID, Kind: actor.Kind, Label: actor.Label, TokenID: actor.TokenID,
		TokenScope: NormalizeTokenScope(actor.TokenScope), Projects: projects, Spaces: spaces}
	if len(plat) == 1 {
		view.Platform = plat[0]
	}
	return view, nil
}

// ─── Plataforma (RF-02, R-7, R-8) ────────────────────────────────────────────

// ListPlatformMembers requiere platform.directory.read.
func (s *Service) ListPlatformMembers(ctx context.Context, actor Identity, q MemberQuery) ([]Member, string, error) {
	if err := s.require(ctx, actor, PlatformScope(), CapPlatformDirectoryRead, nil); err != nil {
		return nil, "", err
	}
	if q.Role != RoleNone && !ValidRole(ScopePlatform, q.Role) {
		return nil, "", ruleErr(CodeInvalid, "role must be one of superadmin, admin, auditor, miembro")
	}
	return s.store.ListPlatformMembers(ctx, q)
}

// BindingChange describe el resultado de una asignación.
type BindingChange struct {
	Scope       Scope  `json:"scope"`
	PrincipalID string `json:"principal_id"`
	RoleBefore  Role   `json:"role_before"`
	RoleAfter   Role   `json:"role_after"`
	Changed     bool   `json:"changed"`
}

// SetPlatformRole asigna, cambia o quita (RoleMiembro / RoleNone) el rol de
// plataforma. Requiere platform.admins.manage (solo superadmin, R-7). Mantiene
// el rol de Engram en sintonía (superadmin y admin = admin; auditor y miembro
// = member) para que la compatibilidad de requisitos §9 no lo revierta.
func (s *Service) SetPlatformRole(ctx context.Context, actor Identity, targetRef string, role Role) (BindingChange, error) {
	plat := PlatformScope()
	if err := s.require(ctx, actor, plat, CapPlatformAdminsManage, map[string]any{"target": targetRef, "role": string(role)}); err != nil {
		return BindingChange{}, err
	}
	if role == RoleNone {
		role = RoleMiembro
	}
	if !ValidRole(ScopePlatform, role) {
		return BindingChange{}, ruleErr(CodeInvalid, "role must be one of superadmin, admin, auditor, miembro")
	}
	target, err := s.ResolvePrincipal(ctx, targetRef)
	if err != nil {
		return BindingChange{}, err
	}
	if target.Kind == KindServiceAccount && role != RoleMiembro {
		return BindingChange{}, s.reject(ctx, actor, plat, CapPlatformAdminsManage, target.ID, ruleErr(CodeServiceAccountRole, "a service account can only be miembro at platform level (R-10)"))
	}
	actorRole, err := s.eng.EffectiveRole(ctx, actor, plat)
	if err != nil {
		return BindingChange{}, err
	}
	if target.ID == actor.PrincipalID && Rank(ScopePlatform, role) > Rank(ScopePlatform, actorRole) {
		return BindingChange{}, s.reject(ctx, actor, plat, CapPlatformAdminsManage, target.ID, ruleErr(CodeSelfPromotion, "nobody can assign themselves a higher role (R-11)"))
	}
	change := BindingChange{Scope: plat, PrincipalID: target.ID, RoleAfter: role}
	var ruleViolation *RuleError
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		// Orden de bloqueos: fila del principal, luego la guarda de superadmin
		// (el mismo orden que los triggers), para no cruzarse con Engram.
		row, err := resolvePrincipal(ctx, tx, target.ID, true)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, guardLockSQL); err != nil {
			return err
		}
		var binding sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT role FROM nexo_role_bindings WHERE principal_id = $1 AND scope_type = 'platform' AND scope_key = '*' FOR UPDATE`, row.ID).Scan(&binding); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		before, _ := resolvePlatform(PlatformFacts{Found: true, Enabled: true, Kind: row.Kind, EngramRole: row.EngramRole, Binding: Role(binding.String)})
		change.RoleBefore = before
		if before == role {
			return nil
		}
		if before == RoleSuperadmin && row.Enabled {
			var others int
			if err := tx.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM nexo_role_bindings b JOIN cloud_principals p ON p.id = b.principal_id
				WHERE b.scope_type = 'platform' AND b.scope_key = '*' AND b.role = 'superadmin' AND p.enabled AND b.principal_id <> $1`, row.ID).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				ruleViolation = ruleErr(CodeLastSuperadmin, "the last enabled superadmin cannot be demoted (R-8)")
				return ruleViolation
			}
		}
		if role == RoleMiembro {
			if _, err := tx.ExecContext(ctx, `DELETE FROM nexo_role_bindings WHERE principal_id = $1 AND scope_type = 'platform' AND scope_key = '*'`, row.ID); err != nil {
				return mapDBError(err)
			}
		} else if err := upsertBinding(ctx, tx, row.ID, plat, role, actor.PrincipalID); err != nil {
			return err
		}
		// La columna role de Engram la deriva el trigger
		// nexo_role_bindings_sync_engram en esta misma transacción
		// (migrate_roles.go); aquí solo se lee para dejarla en el evento.
		engramRole := EngramRoleFor(role)
		var engramAfter string
		if err := tx.QueryRowContext(ctx, `SELECT role FROM cloud_principals WHERE id = $1`, row.ID).Scan(&engramAfter); err != nil {
			return err
		}
		if engramAfter != engramRole {
			return fmt.Errorf("authz: la columna role de Engram quedó en %q y debía ser %q", engramAfter, engramRole)
		}
		change.Changed = true
		return insertEvent(ctx, tx, Event{
			Action: bindingAction(ScopePlatform, before, role), Scope: plat, TargetPrincipalID: row.ID,
			RoleBefore: before, RoleAfter: role, Actor: actor,
			Details: map[string]any{"engram_role_before": row.EngramRole, "engram_role_after": engramRole},
		})
	})
	if ruleViolation != nil {
		return BindingChange{}, s.reject(ctx, actor, plat, CapPlatformAdminsManage, target.ID, ruleViolation)
	}
	if err != nil {
		var re *RuleError
		if errors.As(err, &re) && re.Code == CodeLastSuperadmin {
			return BindingChange{}, s.reject(ctx, actor, plat, CapPlatformAdminsManage, target.ID, re)
		}
		return BindingChange{}, err
	}
	return change, nil
}

func bindingAction(t ScopeType, before, after Role) string {
	none := func(r Role) bool { return r == RoleNone || (t == ScopePlatform && r == RoleMiembro) }
	switch {
	case none(before) && !none(after):
		return ActionBindingGranted
	case !none(before) && none(after):
		return ActionBindingRevoked
	default:
		return ActionBindingChanged
	}
}

func upsertBinding(ctx context.Context, tx execer, principalID string, sc Scope, role Role, grantedBy string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO nexo_role_bindings (principal_id, scope_type, scope_key, role, granted_by_principal_id)
		VALUES ($1::bigint, $2, $3, $4, $5::bigint)
		ON CONFLICT (principal_id, scope_type, scope_key)
		DO UPDATE SET role = EXCLUDED.role, granted_by_principal_id = EXCLUDED.granted_by_principal_id, updated_at = NOW()`,
		principalID, string(sc.Type), sc.Key, string(role), parseBigint(grantedBy))
	return mapDBError(err)
}

// mapDBError traduce los rechazos de los triggers a errores de reglas.
func mapDBError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "nexo_last_superadmin"):
		return ruleErr(CodeLastSuperadmin, "the last enabled superadmin cannot be removed, demoted or disabled (R-8)")
	case strings.Contains(msg, "nexo_service_account_role"):
		return ruleErr(CodeServiceAccountRole, "a service account cannot have that role (R-10)")
	case strings.Contains(msg, "nexo_invalid_role"):
		return ruleErr(CodeInvalid, "invalid role for this scope level")
	}
	return err
}

// ─── Ámbitos (RF-03, RF-04, R-3, R-5, R-9) ───────────────────────────────────

// CreateScopeInput son los datos para crear un proyecto o espacio.
type CreateScopeInput struct {
	Type        ScopeType
	Key         string
	Visibility  string // "" = por defecto del nivel (R-5)
	DefaultRole Role   // "" = lector
	Owner       string // responsable inicial (id o usuario), obligatorio (R-9)
}

func validVisibility(v string) bool { return v == VisibilityOpen || v == VisibilityRestricted }

func validDefaultRole(r Role) bool { return r == RoleLector || r == RoleProponente }

// CreateScope crea el ámbito con su responsable inicial. Requiere
// platform.scope.create y platform.scope.assign_owner.
func (s *Service) CreateScope(ctx context.Context, actor Identity, in CreateScopeInput) (ScopeInfo, error) {
	plat := PlatformScope()
	if err := s.require(ctx, actor, plat, CapPlatformScopeCreate, map[string]any{"scope_type": string(in.Type), "scope_key": in.Key}); err != nil {
		return ScopeInfo{}, err
	}
	if err := s.require(ctx, actor, plat, CapPlatformScopeAssignOwner, map[string]any{"scope_type": string(in.Type), "scope_key": in.Key}); err != nil {
		return ScopeInfo{}, err
	}
	if in.Type != ScopeProject && in.Type != ScopeSpace {
		return ScopeInfo{}, ruleErr(CodeInvalid, "type must be project or space")
	}
	sc, err := s.eng.NormalizeScope(Scope{Type: in.Type, Key: in.Key})
	if err != nil {
		return ScopeInfo{}, ruleErr(CodeInvalid, "%v", err)
	}
	vis := strings.TrimSpace(in.Visibility)
	if vis == "" {
		vis = VisibilityRestricted // R-5: proyectos restringidos
		if sc.Type == ScopeSpace {
			vis = VisibilityOpen // R-5: espacios abiertos
		}
	}
	if !validVisibility(vis) {
		return ScopeInfo{}, ruleErr(CodeInvalid, "visibility must be open or restricted")
	}
	def := in.DefaultRole
	if def == RoleNone {
		def = RoleLector
	}
	if !validDefaultRole(def) {
		return ScopeInfo{}, ruleErr(CodeInvalid, "default_role must be lector or proponente (R-3)")
	}
	if strings.TrimSpace(in.Owner) == "" {
		return ScopeInfo{}, ruleErr(CodeInvalid, "owner is required: every active scope has at least one responsable (R-9)")
	}
	owner, err := s.ResolvePrincipal(ctx, in.Owner)
	if err != nil {
		return ScopeInfo{}, err
	}
	if err := s.checkOwnerCandidate(ctx, actor, sc, CapPlatformScopeAssignOwner, owner); err != nil {
		return ScopeInfo{}, err
	}
	var info ScopeInfo
	var exists bool
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `
			INSERT INTO nexo_scopes (scope_type, scope_key, visibility, default_role, status, created_by_principal_id)
			VALUES ($1, $2, $3, $4, 'active', $5::bigint)
			ON CONFLICT DO NOTHING
			RETURNING `+scopeColumns, string(sc.Type), sc.Key, vis, string(def), parseBigint(actor.PrincipalID))
		var found bool
		var err error
		info, found, err = scanScope(row)
		if err != nil {
			return err
		}
		if !found {
			exists = true
			return ruleErr(CodeScopeExists, "scope %s already exists", sc)
		}
		if err := upsertBinding(ctx, tx, owner.ID, sc, RoleResponsable, actor.PrincipalID); err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, Event{Action: ActionScopeCreated, Scope: sc, Actor: actor,
			Details: map[string]any{"visibility": vis, "default_role": string(def), "owner_principal_id": owner.ID}}); err != nil {
			return err
		}
		return insertEvent(ctx, tx, Event{Action: ActionBindingGranted, Scope: sc, TargetPrincipalID: owner.ID, RoleAfter: RoleResponsable, Actor: actor,
			Details: map[string]any{"reason": "initial_owner"}})
	})
	if exists {
		return ScopeInfo{}, ruleErr(CodeScopeExists, "scope %s already exists", sc)
	}
	if err != nil {
		return ScopeInfo{}, err
	}
	return info, nil
}

// checkOwnerCandidate aplica las reglas para nombrar responsable: persona
// existente y habilitada, no cuenta de servicio (R-10) y no uno mismo (R-11).
func (s *Service) checkOwnerCandidate(ctx context.Context, actor Identity, sc Scope, c Capability, owner principalRow) error {
	if owner.Kind == KindServiceAccount {
		return s.reject(ctx, actor, sc, c, owner.ID, ruleErr(CodeServiceAccountRole, "a service account cannot be responsable (R-10)"))
	}
	if !owner.Enabled {
		return ruleErr(CodePrincipalDisabled, "principal %s is disabled", owner.ID)
	}
	if owner.ID == actor.PrincipalID {
		return s.reject(ctx, actor, sc, c, owner.ID, ruleErr(CodeSelfPromotion, "nobody can name themselves responsable of a scope where they have no role (R-11)"))
	}
	return nil
}

// UpdateScopeInput son los cambios de configuración (nil = sin cambio).
type UpdateScopeInput struct {
	Visibility  *string
	DefaultRole *Role
}

func configureCap(t ScopeType) Capability {
	if t == ScopeSpace {
		return CapSpaceConfigure
	}
	return CapProjectConfigure
}

func membersCap(t ScopeType) Capability {
	if t == ScopeSpace {
		return CapSpaceMembersManage
	}
	return CapProjectMembersManage
}

func readCap(t ScopeType) Capability {
	if t == ScopeSpace {
		return CapSpaceRead
	}
	return CapProjectRead
}

func (s *Service) contentScope(sc Scope) (Scope, error) {
	if sc.Type != ScopeProject && sc.Type != ScopeSpace {
		return Scope{}, ruleErr(CodeInvalid, "scope type must be project or space")
	}
	ns, err := s.eng.NormalizeScope(sc)
	if err != nil {
		return Scope{}, ruleErr(CodeInvalid, "%v", err)
	}
	return ns, nil
}

// UpdateScope cambia visibilidad y rol implícito. Requiere project.configure
// o space.configure en el ámbito.
func (s *Service) UpdateScope(ctx context.Context, actor Identity, sc Scope, in UpdateScopeInput) (ScopeInfo, error) {
	sc, err := s.contentScope(sc)
	if err != nil {
		return ScopeInfo{}, err
	}
	if err := s.require(ctx, actor, sc, configureCap(sc.Type), nil); err != nil {
		return ScopeInfo{}, err
	}
	if in.Visibility == nil && in.DefaultRole == nil {
		return ScopeInfo{}, ruleErr(CodeInvalid, "nothing to update: send visibility and/or default_role")
	}
	if in.Visibility != nil && !validVisibility(*in.Visibility) {
		return ScopeInfo{}, ruleErr(CodeInvalid, "visibility must be open or restricted")
	}
	if in.DefaultRole != nil && !validDefaultRole(*in.DefaultRole) {
		return ScopeInfo{}, ruleErr(CodeInvalid, "default_role must be lector or proponente (R-3)")
	}
	var info ScopeInfo
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		before, found, err := scanScope(tx.QueryRowContext(ctx, `SELECT `+scopeColumns+` FROM nexo_scopes WHERE scope_type = $1 AND scope_key = $2 FOR UPDATE`, string(sc.Type), sc.Key))
		if err != nil {
			return err
		}
		if !found {
			return ruleErr(CodeNotFound, "scope %s is not configured yet", sc)
		}
		vis, def := before.Visibility, before.DefaultRole
		if in.Visibility != nil {
			vis = *in.Visibility
		}
		if in.DefaultRole != nil {
			def = *in.DefaultRole
		}
		info, _, err = scanScope(tx.QueryRowContext(ctx, `
			UPDATE nexo_scopes SET visibility = $3, default_role = $4, updated_at = NOW()
			WHERE scope_type = $1 AND scope_key = $2 RETURNING `+scopeColumns, string(sc.Type), sc.Key, vis, string(def)))
		if err != nil {
			return err
		}
		if vis == before.Visibility && def == before.DefaultRole {
			return nil
		}
		return insertEvent(ctx, tx, Event{Action: ActionScopeUpdated, Scope: sc, Actor: actor, Details: map[string]any{
			"before": map[string]any{"visibility": before.Visibility, "default_role": string(before.DefaultRole)},
			"after":  map[string]any{"visibility": vis, "default_role": string(def)},
		}})
	})
	return info, err
}

// SetScopeArchived archiva o restaura un ámbito (platform.scope.archive).
func (s *Service) SetScopeArchived(ctx context.Context, actor Identity, sc Scope, archived bool) (ScopeInfo, error) {
	sc, err := s.contentScope(sc)
	if err != nil {
		return ScopeInfo{}, err
	}
	if err := s.require(ctx, actor, PlatformScope(), CapPlatformScopeArchive, map[string]any{"scope_type": string(sc.Type), "scope_key": sc.Key}); err != nil {
		return ScopeInfo{}, err
	}
	status, action := StatusActive, ActionScopeRestored
	if archived {
		status, action = StatusArchived, ActionScopeArchived
	}
	var info ScopeInfo
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		before, found, err := scanScope(tx.QueryRowContext(ctx, `SELECT `+scopeColumns+` FROM nexo_scopes WHERE scope_type = $1 AND scope_key = $2 FOR UPDATE`, string(sc.Type), sc.Key))
		if err != nil {
			return err
		}
		if !found {
			return ruleErr(CodeNotFound, "scope %s not found", sc)
		}
		info = before
		if before.Status == status {
			return nil
		}
		info, _, err = scanScope(tx.QueryRowContext(ctx, `UPDATE nexo_scopes SET status = $3, updated_at = NOW() WHERE scope_type = $1 AND scope_key = $2 RETURNING `+scopeColumns, string(sc.Type), sc.Key, status))
		if err != nil {
			return err
		}
		return insertEvent(ctx, tx, Event{Action: action, Scope: sc, Actor: actor, Details: map[string]any{"status_before": before.Status, "status_after": status}})
	})
	return info, err
}

// RecoverScope nombra responsable en un ámbito que no tiene ninguno
// habilitado (R-9). Requiere platform.scope.recover (superadmin).
func (s *Service) RecoverScope(ctx context.Context, actor Identity, sc Scope, ownerRef string) (BindingChange, error) {
	sc, err := s.contentScope(sc)
	if err != nil {
		return BindingChange{}, err
	}
	if err := s.require(ctx, actor, PlatformScope(), CapPlatformScopeRecover, map[string]any{"scope_type": string(sc.Type), "scope_key": sc.Key}); err != nil {
		return BindingChange{}, err
	}
	owner, err := s.ResolvePrincipal(ctx, ownerRef)
	if err != nil {
		return BindingChange{}, err
	}
	if err := s.checkOwnerCandidate(ctx, actor, sc, CapPlatformScopeRecover, owner); err != nil {
		return BindingChange{}, err
	}
	change := BindingChange{Scope: sc, PrincipalID: owner.ID, RoleAfter: RoleResponsable}
	var hasOwner bool
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		_, found, err := scanScope(tx.QueryRowContext(ctx, `SELECT `+scopeColumns+` FROM nexo_scopes WHERE scope_type = $1 AND scope_key = $2 FOR UPDATE`, string(sc.Type), sc.Key))
		if err != nil {
			return err
		}
		if !found {
			return ruleErr(CodeNotFound, "scope %s not found", sc)
		}
		n, err := countEnabledOwners(ctx, tx, sc, "")
		if err != nil {
			return err
		}
		if n > 0 {
			hasOwner = true
			return ruleErr(CodeScopeHasOwner, "scope %s already has an enabled responsable; ask them to assign roles", sc)
		}
		before, err := bindingRoleTx(ctx, tx, owner.ID, sc)
		if err != nil {
			return err
		}
		change.RoleBefore, change.Changed = before, true
		if err := upsertBinding(ctx, tx, owner.ID, sc, RoleResponsable, actor.PrincipalID); err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, Event{Action: ActionScopeRecovered, Scope: sc, TargetPrincipalID: owner.ID, RoleBefore: before, RoleAfter: RoleResponsable, Actor: actor}); err != nil {
			return err
		}
		return insertEvent(ctx, tx, Event{Action: bindingAction(sc.Type, before, RoleResponsable), Scope: sc, TargetPrincipalID: owner.ID,
			RoleBefore: before, RoleAfter: RoleResponsable, Actor: actor, Details: map[string]any{"reason": "recover"}})
	})
	if hasOwner {
		return BindingChange{}, s.reject(ctx, actor, sc, CapPlatformScopeRecover, owner.ID, ruleErr(CodeScopeHasOwner, "scope %s already has an enabled responsable", sc))
	}
	if err != nil {
		return BindingChange{}, err
	}
	return change, nil
}

func countEnabledOwners(ctx context.Context, tx *sql.Tx, sc Scope, except string) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM nexo_role_bindings b JOIN cloud_principals p ON p.id = b.principal_id
		WHERE b.scope_type = $1 AND b.scope_key = $2 AND b.role = 'responsable' AND p.enabled
		  AND ($3 = '' OR b.principal_id::text <> $3)`, string(sc.Type), sc.Key, except).Scan(&n)
	return n, err
}

func bindingRoleTx(ctx context.Context, tx *sql.Tx, principalID string, sc Scope) (Role, error) {
	var role sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT role FROM nexo_role_bindings WHERE principal_id = $1::bigint AND scope_type = $2 AND scope_key = $3 FOR UPDATE`, principalID, string(sc.Type), sc.Key).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return RoleNone, nil
	}
	return Role(role.String), err
}

// ScopeMembersView es la respuesta del listado de miembros de un ámbito.
type ScopeMembersView struct {
	Scope   ScopeInfo `json:"scope"`
	Members []Member  `json:"items"`
	Next    string    `json:"-"`
}

// ListScopeMembers requiere leer el ámbito (*.read) o platform.directory.read
// (auditor y admin, RF-06). Un ámbito restringido sin rol responde 404.
func (s *Service) ListScopeMembers(ctx context.Context, actor Identity, sc Scope, q MemberQuery) (ScopeMembersView, error) {
	sc, err := s.contentScope(sc)
	if err != nil {
		return ScopeMembersView{}, err
	}
	d, err := s.eng.Can(ctx, actor, sc, readCap(sc.Type))
	if err != nil {
		return ScopeMembersView{}, err
	}
	if !d.Allowed {
		dir, err := s.eng.Can(ctx, actor, PlatformScope(), CapPlatformDirectoryRead)
		if err != nil {
			return ScopeMembersView{}, err
		}
		if !dir.Allowed {
			s.eng.RecordDenied(ctx, actor, sc, readCap(sc.Type), d, nil)
			return ScopeMembersView{}, &DeniedError{Scope: sc, Capability: readCap(sc.Type), Decision: d}
		}
	}
	if q.Role != RoleNone && !ValidRole(sc.Type, q.Role) {
		return ScopeMembersView{}, ruleErr(CodeInvalid, "invalid role filter")
	}
	info, found, err := s.store.GetScope(ctx, sc)
	if err != nil {
		return ScopeMembersView{}, err
	}
	if !found {
		if sc.Type == ScopeSpace {
			return ScopeMembersView{}, ruleErr(CodeNotFound, "scope %s not found", sc)
		}
		// Proyecto de Engram sin configurar: restringido y activo (compatibilidad).
		info = ScopeInfo{Scope: sc, Visibility: VisibilityRestricted, DefaultRole: RoleLector, Status: StatusActive}
	}
	members, next, err := s.store.ListScopeMembers(ctx, sc, q)
	if err != nil {
		return ScopeMembersView{}, err
	}
	return ScopeMembersView{Scope: info, Members: members, Next: next}, nil
}

// SetScopeMember asigna, cambia (R-1) o quita (role = RoleNone) el rol de una
// persona en un ámbito. Requiere project.members.manage o
// space.members.manage. Aplica R-9, R-10 y R-11.
func (s *Service) SetScopeMember(ctx context.Context, actor Identity, sc Scope, targetRef string, role Role) (BindingChange, error) {
	sc, err := s.contentScope(sc)
	if err != nil {
		return BindingChange{}, err
	}
	c := membersCap(sc.Type)
	if err := s.require(ctx, actor, sc, c, map[string]any{"target": targetRef, "role": string(role)}); err != nil {
		return BindingChange{}, err
	}
	if role != RoleNone && !StorableRole(sc.Type, role) {
		return BindingChange{}, ruleErr(CodeInvalid, "role must be one of lector, proponente, publicador, responsable")
	}
	target, err := s.ResolvePrincipal(ctx, targetRef)
	if err != nil {
		return BindingChange{}, err
	}
	if target.Kind == KindServiceAccount && role == RoleResponsable {
		return BindingChange{}, s.reject(ctx, actor, sc, c, target.ID, ruleErr(CodeServiceAccountRole, "a service account cannot be responsable (R-10)"))
	}
	if target.ID == actor.PrincipalID {
		actorRole, err := s.eng.EffectiveRole(ctx, actor, sc)
		if err != nil {
			return BindingChange{}, err
		}
		if Rank(sc.Type, role) > Rank(sc.Type, actorRole) {
			return BindingChange{}, s.reject(ctx, actor, sc, c, target.ID, ruleErr(CodeSelfPromotion, "nobody can assign themselves a higher role (R-11)"))
		}
	}
	change := BindingChange{Scope: sc, PrincipalID: target.ID, RoleAfter: role}
	var violation *RuleError
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		// La fila del ámbito serializa los cambios de miembros (R-9).
		_, found, err := scanScope(tx.QueryRowContext(ctx, `SELECT `+scopeColumns+` FROM nexo_scopes WHERE scope_type = $1 AND scope_key = $2 FOR UPDATE`, string(sc.Type), sc.Key))
		if err != nil {
			return err
		}
		if !found {
			return ruleErr(CodeNotFound, "scope %s is not configured yet: create it with POST /nexo/authz/v1/scopes", sc)
		}
		before, err := bindingRoleTx(ctx, tx, target.ID, sc)
		if err != nil {
			return err
		}
		change.RoleBefore = before
		if before == role {
			return nil
		}
		if before == RoleResponsable && role != RoleResponsable && target.Enabled {
			n, err := countEnabledOwners(ctx, tx, sc, target.ID)
			if err != nil {
				return err
			}
			if n == 0 {
				violation = ruleErr(CodeLastOwner, "the last responsable of %s cannot be removed or demoted (R-9)", sc)
				return violation
			}
		}
		if role == RoleNone {
			if _, err := tx.ExecContext(ctx, `DELETE FROM nexo_role_bindings WHERE principal_id = $1::bigint AND scope_type = $2 AND scope_key = $3`, target.ID, string(sc.Type), sc.Key); err != nil {
				return mapDBError(err)
			}
		} else if err := upsertBinding(ctx, tx, target.ID, sc, role, actor.PrincipalID); err != nil {
			return err
		}
		change.Changed = true
		return insertEvent(ctx, tx, Event{Action: bindingAction(sc.Type, before, role), Scope: sc, TargetPrincipalID: target.ID,
			RoleBefore: before, RoleAfter: role, Actor: actor})
	})
	if violation != nil {
		return BindingChange{}, s.reject(ctx, actor, sc, c, target.ID, violation)
	}
	if err != nil {
		return BindingChange{}, err
	}
	return change, nil
}

// ─── Eventos (RF-06, R-14) ───────────────────────────────────────────────────

// ListEvents requiere platform.audit.read.
func (s *Service) ListEvents(ctx context.Context, actor Identity, q EventQuery) ([]EventRecord, string, error) {
	if err := s.require(ctx, actor, PlatformScope(), CapPlatformAuditRead, nil); err != nil {
		return nil, "", err
	}
	return s.store.ListEvents(ctx, q)
}

// PlatformRoleOf devuelve el rol de plataforma de cualquier principal (para
// los enganches de Engram que actúan sobre otra persona).
func (s *Service) PlatformRoleOf(ctx context.Context, principalID string) (Role, bool, error) {
	pid, ok := (Identity{PrincipalID: principalID}).principalNumber()
	if !ok {
		return RoleNone, false, nil
	}
	f, err := s.store.PlatformFacts(ctx, pid)
	if err != nil {
		return RoleNone, false, err
	}
	if !f.Found {
		return RoleNone, false, nil
	}
	f.Enabled = true // el rol que tiene asignado, aunque esté deshabilitada
	role, _ := resolvePlatform(f)
	return role, true, nil
}

// OtherEnabledSuperadmins cuenta superadmins habilitados distintos de principalID.
func (s *Service) OtherEnabledSuperadmins(ctx context.Context, principalID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM nexo_role_bindings b JOIN cloud_principals p ON p.id = b.principal_id
		WHERE b.scope_type = 'platform' AND b.scope_key = '*' AND b.role = 'superadmin' AND p.enabled AND b.principal_id::text <> $1`, strings.TrimSpace(principalID)).Scan(&n)
	return n, err
}

// TokenOwner devuelve el principal dueño de un token (para el enganche de
// revocación).
func (s *Service) TokenOwner(ctx context.Context, tokenID string) (string, bool, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(tokenID), 10, 64)
	if err != nil {
		return "", false, nil
	}
	var owner string
	err = s.db.QueryRowContext(ctx, `SELECT principal_id::text FROM cloud_principal_tokens WHERE id = $1`, n).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return owner, true, nil
}
