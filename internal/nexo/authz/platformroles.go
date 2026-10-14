package authz

// Roles de plataforma unificados (docs/nexo/roles/design.md §11).
//
// Una persona tiene UN solo rol de plataforma: el de Nexo (miembro, auditor,
// admin, superadmin). La columna role de cloud_principals (admin | member) es
// un valor DERIVADO que Engram sigue leyendo en sus pantallas y consultas:
//
//	superadmin, admin -> admin      miembro, auditor -> member
//
// Nadie escribe esa columna por su cuenta. La deriva la base de datos:
//   - el trigger nexo_role_bindings_sync_engram la recalcula en la misma
//     transacción que cambia la asignación de plataforma;
//   - el trigger nexo_principal_role_derive la recalcula en cualquier INSERT o
//     UPDATE de la columna, así que un formulario viejo, la API /admin, el
//     bootstrap o SQL directo no pueden dejarla distinta del rol de Nexo.
//
// Este archivo tiene la correspondencia, el contexto con el rol pedido al
// crear una persona y la asignación inicial (AssignOnCreateTx) que usan los
// enganches de cloudstore (NX-05 unificación).

import (
	"context"
	"database/sql"
	"strings"
)

// EngramRoleFor devuelve el valor de la columna role de Engram que le
// corresponde a un rol de plataforma de Nexo.
func EngramRoleFor(r Role) string {
	if r == RoleSuperadmin || r == RoleAdmin {
		return "admin"
	}
	return "member"
}

// ParseAssignableRole interpreta un rol de plataforma pedido por un
// formulario o la API. "member" (el valor histórico de Engram) vale miembro.
// Cualquier otro texto no es un rol: el servidor lo rechaza.
func ParseAssignableRole(s string) (Role, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "member", "miembro":
		return RoleMiembro, true
	case "auditor":
		return RoleAuditor, true
	case "admin":
		return RoleAdmin, true
	case "superadmin":
		return RoleSuperadmin, true
	}
	return RoleNone, false
}

// PlatformRoleLabel es el nombre del rol en español para pantallas y mensajes.
func PlatformRoleLabel(r Role) string {
	switch r {
	case RoleSuperadmin:
		return "Superadmin"
	case RoleAdmin:
		return "Administrador"
	case RoleAuditor:
		return "Auditor"
	}
	return "Miembro"
}

// ─── Rol pedido al crear una persona ─────────────────────────────────────────

// CreateRequest es el rol de plataforma que un enganche pide al crear una
// persona, con quién lo pide. Lo deja cloudserver en el contexto después de
// comprobar la capacidad; lo lee cloudstore (AssignOnCreateTx).
type CreateRequest struct {
	Role   Role
	Actor  Identity
	Source string // panel | api | cli, para el evento
}

type createRequestKey struct{}

// WithCreateRequest deja el rol pedido en el contexto.
func WithCreateRequest(ctx context.Context, req CreateRequest) context.Context {
	return context.WithValue(ctx, createRequestKey{}, req)
}

// CreateRequestFrom devuelve el rol pedido (ok = false si nadie lo pidió).
func CreateRequestFrom(ctx context.Context) (CreateRequest, bool) {
	req, ok := ctx.Value(createRequestKey{}).(CreateRequest)
	return req, ok
}

type txExecer interface {
	execer
	queryer
}

// AssignOnCreateTx deja el rol de plataforma de una persona recién creada,
// dentro de la transacción que la crea. La columna de Engram la recalcula el
// trigger al insertar la asignación.
//
//   - Con CreateRequest en el contexto, aplica ese rol (la capacidad ya la
//     comprobó el enganche de la ruta).
//   - Sin él (llamada directa al almacén de Engram: pruebas, comandos), el
//     valor histórico de Engram role=admin se interpreta como "pide ser
//     administrador"; si todavía no hay superadmin habilitado, la primera
//     persona queda como superadmin (diseño §6.3).
//   - Una cuenta de servicio nunca recibe rol de plataforma (R-10).
//
// Devuelve el rol de plataforma final. Registra binding.granted.
func AssignOnCreateTx(ctx context.Context, tx txExecer, principalID, kind, legacyEngramRole, defaultSource string) (Role, error) {
	if kind != KindHuman {
		return RoleMiembro, nil
	}
	req, explicit := CreateRequestFrom(ctx)
	role := RoleMiembro
	source := defaultSource
	if source == "" {
		source = "store"
	}
	actor := Identity{}
	if explicit {
		role, source, actor = req.Role, req.Source, req.Actor
	} else if strings.EqualFold(strings.TrimSpace(legacyEngramRole), "admin") {
		role = RoleAdmin
	}
	if role == RoleNone || role == RoleMiembro {
		return RoleMiembro, nil
	}
	if !StorableRole(ScopePlatform, role) {
		return RoleNone, ruleErr(CodeInvalid, "role %q is not a platform role", role)
	}
	if _, err := tx.ExecContext(ctx, guardLockSQL); err != nil {
		return RoleNone, err
	}
	reason := "create_user"
	if !explicit && role == RoleAdmin {
		// Compatibilidad con quien llama sin rol de Nexo: el primer admin es superadmin.
		var hasSuper bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM nexo_role_bindings b JOIN cloud_principals p ON p.id = b.principal_id
				WHERE b.scope_type = 'platform' AND b.scope_key = '*' AND b.role = 'superadmin' AND p.enabled)`).Scan(&hasSuper); err != nil {
			return RoleNone, err
		}
		if !hasSuper {
			role, reason = RoleSuperadmin, "create_first_admin"
		}
	}
	if err := upsertBinding(ctx, tx, principalID, PlatformScope(), role, actor.PrincipalID); err != nil {
		return RoleNone, err
	}
	err := insertEvent(ctx, tx, Event{
		Action: ActionBindingGranted, Scope: PlatformScope(), TargetPrincipalID: principalID,
		RoleBefore: RoleMiembro, RoleAfter: role, Actor: actor,
		Details: map[string]any{"reason": reason, "source": source, "engram_role_after": EngramRoleFor(role)},
	})
	if err != nil {
		return RoleNone, err
	}
	return role, nil
}

// PlatformRoleMap devuelve el rol de plataforma de Nexo de todas las personas
// y cuentas (sin fila: miembro). Una sola consulta; lo usan las pantallas.
func PlatformRoleMap(ctx context.Context, db *sql.DB) (map[string]Role, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT p.id::text, COALESCE(b.role, ''), p.kind
		FROM cloud_principals p
		LEFT JOIN nexo_role_bindings b ON b.principal_id = p.id AND b.scope_type = 'platform' AND b.scope_key = '*'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]Role{}
	for rows.Next() {
		var id, role, kind string
		if err := rows.Scan(&id, &role, &kind); err != nil {
			return nil, err
		}
		r := Role(role)
		if r == RoleNone || kind == KindServiceAccount || !StorableRole(ScopePlatform, r) {
			r = RoleMiembro
		}
		out[id] = r
	}
	return out, rows.Err()
}

// SyncEngramRoleTx lo usan las APIs de Engram que todavía reciben un valor de
// la columna role (admin | member) al actualizar una persona. Esa columna no
// se escribe: el valor pedido se traduce al rol de plataforma de Nexo, que
// actualiza la asignación (y, por trigger, la columna) en la misma
// transacción y deja el evento.
//
//   - admin: quien no es admin ni superadmin pasa a admin.
//   - member: un admin o un superadmin pasa a miembro (el trigger de la guarda
//     rechaza degradar al último superadmin). Un auditor sigue siendo auditor:
//     también es "member" para Engram.
//
// Una cuenta de servicio no recibe rol de plataforma. Devuelve el rol final.
func SyncEngramRoleTx(ctx context.Context, tx txExecer, principalID, kind, engramRole, source string) (Role, error) {
	if kind != KindHuman {
		return RoleMiembro, nil
	}
	if _, err := tx.ExecContext(ctx, guardLockSQL); err != nil {
		return RoleNone, err
	}
	var current sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT role FROM nexo_role_bindings WHERE principal_id = $1::bigint AND scope_type = 'platform' AND scope_key = '*'`, principalID).Scan(&current); err != nil && err != sql.ErrNoRows {
		return RoleNone, err
	}
	before := RoleMiembro
	if current.Valid {
		before = Role(current.String)
	}
	after := before
	switch strings.ToLower(strings.TrimSpace(engramRole)) {
	case "admin":
		if before != RoleAdmin && before != RoleSuperadmin {
			after = RoleAdmin
		}
	case "member":
		if before == RoleAdmin || before == RoleSuperadmin {
			after = RoleMiembro
		}
	}
	if after == before {
		return before, nil
	}
	if after == RoleMiembro {
		if _, err := tx.ExecContext(ctx, `DELETE FROM nexo_role_bindings WHERE principal_id = $1::bigint AND scope_type = 'platform' AND scope_key = '*'`, principalID); err != nil {
			return RoleNone, mapDBError(err)
		}
	} else if err := upsertBinding(ctx, tx, principalID, PlatformScope(), after, ""); err != nil {
		return RoleNone, err
	}
	err := insertEvent(ctx, tx, Event{
		Action: bindingAction(ScopePlatform, before, after), Scope: PlatformScope(), TargetPrincipalID: principalID,
		RoleBefore: before, RoleAfter: after,
		Details: map[string]any{"source": source, "engram_role_requested": engramRole, "engram_role_after": EngramRoleFor(after)},
	})
	return after, err
}
