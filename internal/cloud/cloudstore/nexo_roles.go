package cloudstore

// Nexo IA · NX-05 (unificación de roles de plataforma) — enganche de
// creación de personas. La columna role de cloud_principals (admin | member)
// es un valor derivado del rol de plataforma de Nexo: la base lo recalcula con
// triggers (internal/nexo/authz/migrate_roles.go). Quien crea una persona con
// Role "admin" no escribe esa columna: pide ser administrador, y este enganche
// deja la asignación de Nexo (con su evento) en la misma transacción.

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
)

// nexoAssignRoleOnCreate aplica el rol de plataforma de una persona recién
// creada y deja en principal y human el valor final. requested es el Role que
// recibió el almacén de Engram; source dice quién llama (bootstrap o vacío).
func nexoAssignRoleOnCreate(ctx context.Context, tx *sql.Tx, principal *Principal, human *HumanUser, requested, source string) error {
	role, err := authz.AssignOnCreateTx(ctx, tx, principal.ID, principal.Kind, requested, source)
	if err != nil {
		return fmt.Errorf("cloudstore: assign platform role: %w", err)
	}
	principal.Role = authz.EngramRoleFor(role)
	if human != nil {
		human.PlatformRole = string(role)
	}
	return nil
}

// nexoCreatePrincipal es CreatePrincipal de Engram con el rol de Nexo: inserta
// la fila (la columna la deriva la base) y deja la asignación de plataforma en
// la misma transacción. Un role "admin" pide ser administrador.
func (cs *CloudStore) nexoCreatePrincipal(ctx context.Context, kind, displayName, role string, enabled bool) (Principal, error) {
	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return Principal{}, fmt.Errorf("cloudstore: begin create principal tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	const q = `
		INSERT INTO cloud_principals (kind, display_name, role, enabled)
		VALUES ($1, $2, $3, $4)
		RETURNING id::text, kind, display_name, role, enabled, created_at, updated_at`
	principal, err := scanPrincipal(tx.QueryRowContext(ctx, q, kind, displayName, role, enabled))
	if err != nil {
		return Principal{}, err
	}
	if err := nexoAssignRoleOnCreate(ctx, tx, &principal, nil, role, ""); err != nil {
		return Principal{}, err
	}
	if err := tx.Commit(); err != nil {
		return Principal{}, fmt.Errorf("cloudstore: commit create principal: %w", err)
	}
	return principal, nil
}

// nexoSyncEngramRole traduce el role pedido a UpdatePrincipal al rol de
// plataforma de Nexo (misma transacción, con evento): la columna no se escribe.
func nexoSyncEngramRole(ctx context.Context, tx *sql.Tx, id, role string) error {
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM cloud_principals WHERE id = $1`, id).Scan(&kind); err != nil {
		return fmt.Errorf("cloudstore: read principal kind: %w", err)
	}
	if _, err := authz.SyncEngramRoleTx(ctx, tx, id, kind, role, "store"); err != nil {
		return fmt.Errorf("cloudstore: sync platform role: %w", err)
	}
	return nil
}
