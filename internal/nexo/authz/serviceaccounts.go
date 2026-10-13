package authz

// SG-10 · Cuentas de servicio para CI y bots
// (docs/nexo/seguridad/cuentas-servicio.md). Una cuenta de servicio es un cloud_principals con kind
// 'service_account' (tabla de Engram, sin cambios) más una fila en la tabla
// lateral nexo_service_accounts con lo que Engram no guarda: nombre corto
// (slug), descripción, la persona responsable (owner) y quién la creó.
//
//   - Alta, cambio de descripción o responsable, deshabilitar y habilitar:
//     platform.users.manage (admin o superadmin). Listado y detalle:
//     platform.directory.read (también el auditor).
//   - Cada acción deja un evento en nexo_authz_events (service_account.*).
//   - R-10 sigue en el trigger nexo_role_bindings_validate: la cuenta solo
//     puede ser lector, proponente o publicador en ámbitos.
//   - Los tokens se emiten con la ruta de tokens de siempre (la API acepta el
//     slug); la rotación y la revocación con evento viven en cloudserver
//     (nexo_service_accounts.go), que usa RecordServiceAccountTokenEvent.

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Acciones de nexo_authz_events de las cuentas de servicio (SG-10).
const (
	ActionServiceAccountCreated      = "service_account.created"
	ActionServiceAccountUpdated      = "service_account.updated"
	ActionServiceAccountDisabled     = "service_account.disabled"
	ActionServiceAccountEnabled      = "service_account.enabled"
	ActionServiceAccountTokenIssued  = "service_account.token_issued"
	ActionServiceAccountTokenRotated = "service_account.token_rotated"
	ActionServiceAccountTokenRevoked = "service_account.token_revoked"
)

// CodeServiceAccountExists es el rechazo de un slug repetido (409).
const CodeServiceAccountExists = "service_account_exists"

// serviceAccountMigrations crean la tabla lateral (aditiva e idempotente; la
// ejecuta Migrate dentro de la transacción de migraciones de Nexo).
var serviceAccountMigrations = []string{
	`CREATE TABLE IF NOT EXISTS nexo_service_accounts (
		principal_id            BIGINT PRIMARY KEY REFERENCES cloud_principals(id) ON DELETE CASCADE,
		slug                    TEXT NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
		description             TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 500),
		owner_principal_id      BIGINT NOT NULL REFERENCES cloud_principals(id),
		created_by_principal_id BIGINT REFERENCES cloud_principals(id) ON DELETE SET NULL,
		created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_service_accounts_owner ON nexo_service_accounts (owner_principal_id)`,
	// La fila describe a una cuenta de servicio y su responsable es una
	// persona (también por SQL directo).
	`CREATE OR REPLACE FUNCTION nexo_service_accounts_validate() RETURNS trigger AS $$
	BEGIN
		IF (SELECT kind FROM cloud_principals WHERE id = NEW.principal_id) IS DISTINCT FROM 'service_account' THEN
			RAISE EXCEPTION 'nexo_service_account_kind: el principal % no es una cuenta de servicio', NEW.principal_id USING ERRCODE = 'check_violation';
		END IF;
		IF (SELECT kind FROM cloud_principals WHERE id = NEW.owner_principal_id) IS DISTINCT FROM 'human' THEN
			RAISE EXCEPTION 'nexo_service_account_owner: el responsable de una cuenta de servicio debe ser una persona' USING ERRCODE = 'check_violation';
		END IF;
		RETURN NEW;
	END
	$$ LANGUAGE plpgsql`,
	`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_service_accounts_validate' AND tgrelid = 'nexo_service_accounts'::regclass) THEN
			CREATE TRIGGER nexo_service_accounts_validate BEFORE INSERT OR UPDATE ON nexo_service_accounts
				FOR EACH ROW EXECUTE FUNCTION nexo_service_accounts_validate();
		END IF;
	END $$`,
}

var serviceAccountSlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// ValidServiceAccountSlug informa si s es un nombre corto válido: minúsculas,
// números y guiones, de 2 a 63 caracteres, sin guion inicial.
func ValidServiceAccountSlug(s string) bool { return serviceAccountSlugRE.MatchString(s) }

// ServiceAccount es una cuenta de servicio con su responsable.
type ServiceAccount struct {
	PrincipalID   string    `json:"principal_id"`
	Slug          string    `json:"slug"`
	DisplayName   string    `json:"display_name"`
	Description   string    `json:"description"`
	Enabled       bool      `json:"enabled"`
	OwnerID       string    `json:"owner_principal_id"`
	OwnerUsername string    `json:"owner_username,omitempty"`
	OwnerName     string    `json:"owner_display_name,omitempty"`
	OwnerEnabled  bool      `json:"owner_enabled"`
	CreatedBy     string    `json:"created_by_principal_id,omitempty"`
	CreatedByName string    `json:"created_by_username,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	// Bindings son los roles en proyectos y espacios.
	Bindings []ServiceAccountBinding `json:"bindings"`
}

// ServiceAccountBinding es un rol de la cuenta en un ámbito.
type ServiceAccountBinding struct {
	Scope Scope `json:"scope"`
	Role  Role  `json:"role"`
}

// CreateServiceAccountInput son los datos del alta.
type CreateServiceAccountInput struct {
	Slug        string
	DisplayName string // vacío = el slug
	Description string
	Owner       string // id o usuario de la persona responsable (obligatorio)
}

const saColumns = `p.id::text, sa.slug, p.display_name, sa.description, p.enabled,
	sa.owner_principal_id::text, COALESCE(ho.username, ''), COALESCE(po.display_name, ''), COALESCE(po.enabled, FALSE),
	COALESCE(sa.created_by_principal_id::text, ''), COALESCE(hc.username, ''), sa.created_at, sa.updated_at`

const saFrom = ` FROM nexo_service_accounts sa
	JOIN cloud_principals p ON p.id = sa.principal_id
	LEFT JOIN cloud_principals po ON po.id = sa.owner_principal_id
	LEFT JOIN cloud_human_users ho ON ho.principal_id = sa.owner_principal_id
	LEFT JOIN cloud_human_users hc ON hc.principal_id = sa.created_by_principal_id`

func scanServiceAccount(row interface{ Scan(...any) error }) (ServiceAccount, error) {
	var a ServiceAccount
	err := row.Scan(&a.PrincipalID, &a.Slug, &a.DisplayName, &a.Description, &a.Enabled,
		&a.OwnerID, &a.OwnerUsername, &a.OwnerName, &a.OwnerEnabled, &a.CreatedBy, &a.CreatedByName, &a.CreatedAt, &a.UpdatedAt)
	a.Bindings = []ServiceAccountBinding{}
	return a, err
}

func (s *Service) requirePlatform(ctx context.Context, actor Identity, c Capability, details map[string]any) error {
	return s.require(ctx, actor, PlatformScope(), c, details)
}

// CreateServiceAccount da de alta una cuenta de servicio (platform.users.manage).
func (s *Service) CreateServiceAccount(ctx context.Context, actor Identity, in CreateServiceAccountInput) (ServiceAccount, error) {
	slug := strings.TrimSpace(in.Slug)
	if err := s.requirePlatform(ctx, actor, CapPlatformUsersManage, map[string]any{"action": ActionServiceAccountCreated, "slug": slug}); err != nil {
		return ServiceAccount{}, err
	}
	if !ValidServiceAccountSlug(slug) {
		return ServiceAccount{}, ruleErr(CodeInvalid, "slug must be 2 to 63 lowercase letters, digits or hyphens (for example ci-lineamientos)")
	}
	desc := strings.TrimSpace(in.Description)
	if len([]rune(desc)) > 500 {
		return ServiceAccount{}, ruleErr(CodeInvalid, "description must be at most 500 characters")
	}
	name := strings.TrimSpace(in.DisplayName)
	if name == "" {
		name = slug
	}
	if len([]rune(name)) > 120 {
		return ServiceAccount{}, ruleErr(CodeInvalid, "display_name must be at most 120 characters")
	}
	if strings.TrimSpace(in.Owner) == "" {
		return ServiceAccount{}, ruleErr(CodeInvalid, "owner is required: the person responsible for the service account (id or username)")
	}
	owner, err := s.ResolvePrincipal(ctx, in.Owner)
	if err != nil {
		return ServiceAccount{}, err
	}
	if err := checkServiceAccountOwner(owner); err != nil {
		return ServiceAccount{}, err
	}
	var created ServiceAccount
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		// El slug no puede confundirse con un usuario (la API acepta ambos).
		var taken bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM cloud_human_users WHERE username = $1) OR EXISTS (SELECT 1 FROM nexo_service_accounts WHERE slug = $1)`, slug).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return ruleErr(CodeServiceAccountExists, "a user or service account named %q already exists", slug)
		}
		var id string
		if err := tx.QueryRowContext(ctx, `INSERT INTO cloud_principals (kind, display_name, role, enabled) VALUES ('service_account', $1, 'member', TRUE) RETURNING id::text`, name).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO nexo_service_accounts (principal_id, slug, description, owner_principal_id, created_by_principal_id) VALUES ($1::bigint, $2, $3, $4::bigint, $5::bigint)`,
			id, slug, desc, owner.ID, parseBigint(actor.PrincipalID)); err != nil {
			if strings.Contains(err.Error(), "nexo_service_accounts_slug_key") {
				return ruleErr(CodeServiceAccountExists, "a service account named %q already exists", slug)
			}
			return err
		}
		a, err := scanServiceAccount(tx.QueryRowContext(ctx, `SELECT `+saColumns+saFrom+` WHERE sa.principal_id = $1::bigint`, id))
		if err != nil {
			return err
		}
		created = a
		return insertEvent(ctx, tx, Event{Action: ActionServiceAccountCreated, Scope: PlatformScope(), TargetPrincipalID: id, Actor: actor,
			Details: map[string]any{"slug": slug, "owner_principal_id": owner.ID, "description": desc}})
	})
	if err != nil {
		return ServiceAccount{}, err
	}
	return created, nil
}

func checkServiceAccountOwner(owner principalRow) error {
	if owner.Kind != KindHuman {
		return ruleErr(CodeInvalid, "owner must be a person, not a service account")
	}
	if !owner.Enabled {
		return ruleErr(CodePrincipalDisabled, "owner %s is disabled", owner.ID)
	}
	return nil
}

// ServiceAccountQuery filtra el listado.
type ServiceAccountQuery struct {
	Enabled *bool
	Cursor  string // id de principal
	Limit   int
}

// ListServiceAccounts lista las cuentas (platform.directory.read).
func (s *Service) ListServiceAccounts(ctx context.Context, actor Identity, q ServiceAccountQuery) ([]ServiceAccount, string, error) {
	if err := s.requirePlatform(ctx, actor, CapPlatformDirectoryRead, map[string]any{"action": "service_account.list"}); err != nil {
		return nil, "", err
	}
	cursor, err := memberCursor(q.Cursor)
	if err != nil {
		return nil, "", err
	}
	limit := NormalizeLimit(q.Limit)
	var enabled any
	if q.Enabled != nil {
		enabled = *q.Enabled
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+saColumns+saFrom+`
		WHERE sa.principal_id > $1 AND ($2::boolean IS NULL OR p.enabled = $2::boolean)
		ORDER BY sa.principal_id LIMIT $3`, cursor, enabled, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	out := []ServiceAccount{}
	for rows.Next() {
		a, err := scanServiceAccount(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[len(out)-1].PrincipalID
	}
	if err := s.fillServiceAccountBindings(ctx, out); err != nil {
		return nil, "", err
	}
	return out, next, nil
}

func (s *Service) fillServiceAccountBindings(ctx context.Context, items []ServiceAccount) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(items))
	pos := make(map[string]int, len(items))
	for i, a := range items {
		if n, err := strconv.ParseInt(a.PrincipalID, 10, 64); err == nil {
			ids = append(ids, n)
			pos[a.PrincipalID] = i
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT principal_id::text, scope_type, scope_key, role FROM nexo_role_bindings WHERE principal_id = ANY($1) ORDER BY scope_type, scope_key`, ids)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var pid, st, key, role string
		if err := rows.Scan(&pid, &st, &key, &role); err != nil {
			return err
		}
		if i, ok := pos[pid]; ok {
			items[i].Bindings = append(items[i].Bindings, ServiceAccountBinding{Scope: Scope{Type: ScopeType(st), Key: key}, Role: Role(role)})
		}
	}
	return rows.Err()
}

// GetServiceAccount devuelve una cuenta por id o slug (platform.directory.read).
func (s *Service) GetServiceAccount(ctx context.Context, actor Identity, ref string) (ServiceAccount, error) {
	if err := s.requirePlatform(ctx, actor, CapPlatformDirectoryRead, map[string]any{"action": "service_account.get", "target": ref}); err != nil {
		return ServiceAccount{}, err
	}
	return s.LookupServiceAccount(ctx, ref)
}

// LookupServiceAccount lee una cuenta sin autorizar (para los adaptadores
// que ya autorizaron con otra capacidad, como la emisión de tokens).
func (s *Service) LookupServiceAccount(ctx context.Context, ref string) (ServiceAccount, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ServiceAccount{}, ruleErr(CodeInvalid, "service account is required (id or slug)")
	}
	where := `sa.slug = $1`
	var arg any = ref
	if n, err := strconv.ParseInt(ref, 10, 64); err == nil {
		where, arg = `sa.principal_id = $1`, n
	}
	a, err := scanServiceAccount(s.db.QueryRowContext(ctx, `SELECT `+saColumns+saFrom+` WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return ServiceAccount{}, ruleErr(CodeNotFound, "service account %q not found", ref)
	}
	if err != nil {
		return ServiceAccount{}, err
	}
	items := []ServiceAccount{a}
	if err := s.fillServiceAccountBindings(ctx, items); err != nil {
		return ServiceAccount{}, err
	}
	return items[0], nil
}

// UpdateServiceAccountInput cambia la descripción o el responsable (nil = no
// cambia).
type UpdateServiceAccountInput struct {
	Description *string
	Owner       *string
}

// UpdateServiceAccount cambia descripción o responsable (platform.users.manage).
func (s *Service) UpdateServiceAccount(ctx context.Context, actor Identity, ref string, in UpdateServiceAccountInput) (ServiceAccount, error) {
	if err := s.requirePlatform(ctx, actor, CapPlatformUsersManage, map[string]any{"action": ActionServiceAccountUpdated, "target": ref}); err != nil {
		return ServiceAccount{}, err
	}
	if in.Description == nil && in.Owner == nil {
		return ServiceAccount{}, ruleErr(CodeInvalid, "nothing to update: send description and/or owner")
	}
	current, err := s.LookupServiceAccount(ctx, ref)
	if err != nil {
		return ServiceAccount{}, err
	}
	desc := current.Description
	if in.Description != nil {
		desc = strings.TrimSpace(*in.Description)
		if len([]rune(desc)) > 500 {
			return ServiceAccount{}, ruleErr(CodeInvalid, "description must be at most 500 characters")
		}
	}
	ownerID := current.OwnerID
	if in.Owner != nil {
		owner, err := s.ResolvePrincipal(ctx, *in.Owner)
		if err != nil {
			return ServiceAccount{}, err
		}
		if err := checkServiceAccountOwner(owner); err != nil {
			return ServiceAccount{}, err
		}
		ownerID = owner.ID
	}
	if desc == current.Description && ownerID == current.OwnerID {
		return current, nil
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE nexo_service_accounts SET description = $2, owner_principal_id = $3::bigint, updated_at = NOW() WHERE principal_id = $1::bigint`,
			current.PrincipalID, desc, ownerID); err != nil {
			return err
		}
		details := map[string]any{"slug": current.Slug}
		if desc != current.Description {
			details["description"] = desc
		}
		if ownerID != current.OwnerID {
			details["owner_before"], details["owner_after"] = current.OwnerID, ownerID
		}
		return insertEvent(ctx, tx, Event{Action: ActionServiceAccountUpdated, Scope: PlatformScope(), TargetPrincipalID: current.PrincipalID, Actor: actor, Details: details})
	})
	if err != nil {
		return ServiceAccount{}, err
	}
	return s.LookupServiceAccount(ctx, current.PrincipalID)
}

// SetServiceAccountEnabled deshabilita o habilita la cuenta
// (platform.users.manage). Deshabilitada, todos sus tokens se rechazan en la
// siguiente petición (lo hace la resolución de tokens de Engram).
func (s *Service) SetServiceAccountEnabled(ctx context.Context, actor Identity, ref string, enabled bool) (ServiceAccount, bool, error) {
	action := ActionServiceAccountDisabled
	if enabled {
		action = ActionServiceAccountEnabled
	}
	if err := s.requirePlatform(ctx, actor, CapPlatformUsersManage, map[string]any{"action": action, "target": ref}); err != nil {
		return ServiceAccount{}, false, err
	}
	current, err := s.LookupServiceAccount(ctx, ref)
	if err != nil {
		return ServiceAccount{}, false, err
	}
	changed := false
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE cloud_principals SET enabled = $2, updated_at = NOW() WHERE id = $1::bigint AND kind = 'service_account' AND enabled <> $2`, current.PrincipalID, enabled)
		if err != nil {
			return mapDBError(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // ya estaba así: sin evento
		}
		changed = true
		return insertEvent(ctx, tx, Event{Action: action, Scope: PlatformScope(), TargetPrincipalID: current.PrincipalID, Actor: actor,
			Details: map[string]any{"slug": current.Slug}})
	})
	if err != nil {
		return ServiceAccount{}, false, err
	}
	a, err := s.LookupServiceAccount(ctx, current.PrincipalID)
	return a, changed, err
}

// RecordServiceAccountTokenEvent registra emisión, rotación o revocación de un
// token de la cuenta. Details nunca lleva el token (solo id y prefijo).
func (s *Service) RecordServiceAccountTokenEvent(ctx context.Context, actor Identity, action, principalID string, details map[string]any) error {
	return insertEvent(ctx, s.db, Event{Action: action, Scope: PlatformScope(), TargetPrincipalID: principalID, Actor: actor, Details: details})
}

// ExpireTokenWithin acorta la vida de un token de la cuenta a `grace` desde
// ahora (si vencía antes, no cambia) y registra la rotación en la misma
// transacción. grace <= 0 revoca el token. Devuelve la caducidad final (nil si
// se revocó).
func (s *Service) ExpireTokenWithin(ctx context.Context, actor Identity, principalID, tokenID string, grace time.Duration, details map[string]any) (*time.Time, error) {
	var out *time.Time
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var revoked sql.NullTime
		if err := tx.QueryRowContext(ctx, `SELECT revoked_at FROM cloud_principal_tokens WHERE id = $1::bigint AND principal_id = $2::bigint FOR UPDATE`, tokenID, principalID).Scan(&revoked); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ruleErr(CodeNotFound, "token %s not found for this service account", tokenID)
			}
			return err
		}
		switch {
		case revoked.Valid:
			// Ya estaba revocado: no hay nada que acortar.
		case grace <= 0:
			if _, err := tx.ExecContext(ctx, `UPDATE cloud_principal_tokens SET revoked_at = COALESCE(revoked_at, NOW()), revoked_by_principal_id = $2::bigint, revocation_reason = COALESCE(revocation_reason, 'rotated') WHERE id = $1::bigint`,
				tokenID, parseBigint(actor.PrincipalID)); err != nil {
				return err
			}
		default:
			var exp time.Time
			if err := tx.QueryRowContext(ctx, `UPDATE cloud_principal_tokens SET nexo_expires_at = LEAST(COALESCE(nexo_expires_at, 'infinity'::timestamptz), NOW() + make_interval(secs => $2::double precision)) WHERE id = $1::bigint RETURNING nexo_expires_at`,
				tokenID, grace.Seconds()).Scan(&exp); err != nil {
				return err
			}
			out = &exp
		}
		d := map[string]any{}
		for k, v := range details {
			d[k] = v
		}
		d["old_token_id"] = tokenID
		if out != nil {
			d["old_token_expires_at"] = out.UTC().Format(time.RFC3339)
		} else {
			d["old_token_revoked"] = true
		}
		return insertEvent(ctx, tx, Event{Action: ActionServiceAccountTokenRotated, Scope: PlatformScope(), TargetPrincipalID: principalID, Actor: actor, Details: d})
	})
	return out, err
}

// RevokeServiceAccountToken revoca un token de la cuenta y registra el evento.
func (s *Service) RevokeServiceAccountToken(ctx context.Context, actor Identity, principalID, tokenID, reason string) (bool, error) {
	changed := false
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE cloud_principal_tokens SET revoked_at = NOW(), revoked_by_principal_id = $3::bigint, revocation_reason = NULLIF($4, '')
			WHERE id = $1::bigint AND principal_id = $2::bigint AND revoked_at IS NULL`, tokenID, principalID, parseBigint(actor.PrincipalID), reason)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var exists bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM cloud_principal_tokens WHERE id = $1::bigint AND principal_id = $2::bigint)`, tokenID, principalID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ruleErr(CodeNotFound, "token %s not found for this service account", tokenID)
			}
			return nil // ya estaba revocado
		}
		changed = true
		return insertEvent(ctx, tx, Event{Action: ActionServiceAccountTokenRevoked, Scope: PlatformScope(), TargetPrincipalID: principalID, Actor: actor,
			Details: map[string]any{"token_id": tokenID, "reason": reason}})
	})
	return changed, err
}

// resolveServiceAccountSlug es el último recurso de resolvePrincipal: el
// nombre corto de una cuenta de servicio.
func resolveServiceAccountSlug(ctx context.Context, q queryer, ref, lock string) (principalRow, error) {
	var row principalRow
	err := q.QueryRowContext(ctx, `SELECT p.id::text, p.kind, p.enabled, p.role, p.display_name FROM cloud_principals p JOIN nexo_service_accounts sa ON sa.principal_id = p.id WHERE sa.slug = $1`+lock, ref).
		Scan(&row.ID, &row.Kind, &row.Enabled, &row.EngramRole, &row.Label)
	return row, err
}
