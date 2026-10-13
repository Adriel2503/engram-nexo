package security

// Fase S3 (NX-06, RS-20..RS-24, design §4): caducidad y alcance de los
// tokens administrados.
//
//   - Columnas nexo_expires_at (NULL = sin caducidad) y nexo_scope (read |
//     write) en cloud_principal_tokens (MigrateTokens, aditiva).
//   - La consulta que resuelve el token (cloudstore.FindPrincipalTokenByHash)
//     trae ambas y si el token venció devuelve ErrTokenExpired; además deja
//     los datos del token en el contexto de la petición (TokenFacts) para que
//     cloudserver arme authz.Identity.TokenScope sin otra consulta.
//   - La emisión recibe los días y el alcance por el contexto (TokenIssue) y
//     los aplica en la misma transacción que crea el token.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Variables de entorno de la fase S3.
const (
	// TokenMaxDaysEnv es la caducidad máxima en días (por defecto 365).
	TokenMaxDaysEnv = "NEXO_TOKEN_MAX_DAYS"
)

const (
	// DefaultTokenDays es la caducidad por defecto (DS-2).
	DefaultTokenDays = 90
	// DefaultTokenMaxDays es la caducidad máxima por defecto.
	DefaultTokenMaxDays = 365
	// TokenScopeRead y TokenScopeWrite son los alcances (mismos valores que
	// authz.TokenScopeRead/Write).
	TokenScopeRead  = "read"
	TokenScopeWrite = "write"
)

// ErrTokenExpired es el rechazo de un token vencido (RS-22). El texto empieza
// con el código token_expired y no revela nada más del token.
var ErrTokenExpired = errors.New("token_expired: the token has expired; ask an administrator for a new one")

// TokenMaxDays lee NEXO_TOKEN_MAX_DAYS (entero >= 1; inválido = 365 con aviso).
func TokenMaxDays() int {
	raw := strings.TrimSpace(os.Getenv(TokenMaxDaysEnv))
	if raw == "" {
		return DefaultTokenMaxDays
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 36500 {
		warnOnce("token-max-days", fmt.Sprintf("[nexo] %s=%q inválido: se usa %d", TokenMaxDaysEnv, raw, DefaultTokenMaxDays))
		return DefaultTokenMaxDays
	}
	return n
}

// DefaultIssueDays es la caducidad por defecto acotada por el máximo.
func DefaultIssueDays() int {
	if m := TokenMaxDays(); m < DefaultTokenDays {
		return m
	}
	return DefaultTokenDays
}

var warned sync.Map

func warnOnce(key, msg string) {
	if _, loaded := warned.LoadOrStore(key+"|"+msg, true); !loaded {
		log.Print(msg)
	}
}

// ─── Datos del token en el contexto de la petición ───────────────────────────

// TokenFacts son los datos Nexo de un token administrado resuelto.
type TokenFacts struct {
	TokenID     string
	PrincipalID string
	Scope       string     // read | write
	ExpiresAt   *time.Time // nil = sin caducidad
	Expired     bool
	Revoked     bool
}

type tokenFactsHolder struct {
	mu     sync.Mutex
	facts  TokenFacts
	set    bool
	credFP string // huella de la credencial rechazada (A2, dedup.go)
}

type tokenFactsKey struct{}

// WithTokenFactsHolder prepara el contexto de una petición para recibir los
// datos del token que se resuelva en ella (lo hace el middleware de Nexo).
func WithTokenFactsHolder(ctx context.Context) context.Context {
	if _, ok := ctx.Value(tokenFactsKey{}).(*tokenFactsHolder); ok {
		return ctx
	}
	return context.WithValue(ctx, tokenFactsKey{}, &tokenFactsHolder{})
}

// RecordTokenFacts guarda los datos del token resuelto (sin efecto si el
// contexto no tiene dónde guardarlos).
func RecordTokenFacts(ctx context.Context, f TokenFacts) {
	if ctx == nil {
		return
	}
	h, ok := ctx.Value(tokenFactsKey{}).(*tokenFactsHolder)
	if !ok {
		return
	}
	h.mu.Lock()
	h.facts, h.set = f, true
	h.mu.Unlock()
}

// TokenFactsFrom devuelve los datos del último token resuelto en la petición.
func TokenFactsFrom(ctx context.Context) (TokenFacts, bool) {
	if ctx == nil {
		return TokenFacts{}, false
	}
	h, ok := ctx.Value(tokenFactsKey{}).(*tokenFactsHolder)
	if !ok {
		return TokenFacts{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.facts, h.set
}

// ─── Emisión ─────────────────────────────────────────────────────────────────

// TokenIssue son la caducidad y el alcance pedidos para un token nuevo.
type TokenIssue struct {
	Days  int    // días de vida; 0 = sin caducidad
	Scope string // read | write
}

type tokenIssueKey struct{}

// WithTokenIssue pasa la caducidad y el alcance a la creación del token.
func WithTokenIssue(ctx context.Context, t TokenIssue) context.Context {
	return context.WithValue(ctx, tokenIssueKey{}, t)
}

// TokenIssueFrom devuelve lo pedido o, si no se pidió nada (bootstrap por
// CLI, recuperación), los valores por defecto: 90 días y write.
func TokenIssueFrom(ctx context.Context) TokenIssue {
	if ctx != nil {
		if t, ok := ctx.Value(tokenIssueKey{}).(TokenIssue); ok {
			return t
		}
	}
	return TokenIssue{Days: DefaultIssueDays(), Scope: TokenScopeWrite}
}

// IssueRequest es lo que llega en la petición de emisión.
type IssueRequest struct {
	ExpiresInDays *int   // nil = por defecto; 0 = sin caducidad
	Scope         string // "" = write
}

// IssueError es un rechazo de validación de la emisión.
type IssueError struct {
	Status  int    // 400 o 403
	Code    string // invalid_request | no_expiry_forbidden
	Message string
}

func (e *IssueError) Error() string { return e.Code + ": " + e.Message }

// ResolveIssue valida la petición (RS-20, RS-21):
//   - expires_in_days entre 1 y NEXO_TOKEN_MAX_DAYS; omitido = 90 (o el máximo
//     si es menor);
//   - 0 = sin caducidad: solo para cuentas de servicio y solo si quien emite
//     puede (superadmin);
//   - scope read o write (omitido = write).
func ResolveIssue(req IssueRequest, targetKind string, canIssueNoExpiry bool) (TokenIssue, *IssueError) {
	scope := strings.ToLower(strings.TrimSpace(req.Scope))
	if scope == "" {
		scope = TokenScopeWrite
	}
	if scope != TokenScopeRead && scope != TokenScopeWrite {
		return TokenIssue{}, &IssueError{Status: 400, Code: "invalid_request", Message: "scope must be read or write"}
	}
	maxDays := TokenMaxDays()
	if req.ExpiresInDays == nil {
		return TokenIssue{Days: DefaultIssueDays(), Scope: scope}, nil
	}
	days := *req.ExpiresInDays
	switch {
	case days == 0:
		if targetKind != "service_account" {
			return TokenIssue{}, &IssueError{Status: 400, Code: "invalid_request", Message: "only service accounts can have tokens without expiration (expires_in_days=0)"}
		}
		if !canIssueNoExpiry {
			return TokenIssue{}, &IssueError{Status: 403, Code: "no_expiry_forbidden", Message: "only a superadmin can issue tokens without expiration"}
		}
	case days < 0 || days > maxDays:
		return TokenIssue{}, &IssueError{Status: 400, Code: "invalid_request", Message: fmt.Sprintf("expires_in_days must be between 1 and %d (0 = no expiration, service accounts only)", maxDays)}
	}
	return TokenIssue{Days: days, Scope: scope}, nil
}

// ─── Migración y base de datos ───────────────────────────────────────────────

// MigrateTokens agrega las columnas de caducidad y alcance (aditiva e
// idempotente). Los tokens existentes quedan sin caducidad y con write
// (RS-23); los nuevos nacen con la caducidad por defecto aunque los cree un
// camino que no conoce Nexo (bootstrap, recuperación). Un trigger impide que
// un token de persona quede sin caducidad (solo cuentas de servicio).
func MigrateTokens(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE cloud_principal_tokens ADD COLUMN IF NOT EXISTS nexo_expires_at TIMESTAMPTZ`,
		`ALTER TABLE cloud_principal_tokens ADD COLUMN IF NOT EXISTS nexo_scope TEXT NOT NULL DEFAULT 'write'`,
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'cloud_principal_tokens_nexo_scope_check' AND conrelid = 'cloud_principal_tokens'::regclass) THEN
				ALTER TABLE cloud_principal_tokens ADD CONSTRAINT cloud_principal_tokens_nexo_scope_check CHECK (nexo_scope IN ('read', 'write'));
			END IF;
		END $$`,
		// Después de ADD COLUMN: solo afecta a las filas nuevas.
		fmt.Sprintf(`ALTER TABLE cloud_principal_tokens ALTER COLUMN nexo_expires_at SET DEFAULT (now() + make_interval(days => %d))`, DefaultIssueDays()),
		`CREATE OR REPLACE FUNCTION nexo_token_expiry_guard() RETURNS trigger AS $$
		BEGIN
			IF NEW.nexo_expires_at IS NULL
			   AND (TG_OP = 'INSERT' OR OLD.nexo_expires_at IS NOT NULL)
			   AND (SELECT p.kind FROM cloud_principals p WHERE p.id = NEW.principal_id) IS DISTINCT FROM 'service_account' THEN
				RAISE EXCEPTION 'nexo: solo las cuentas de servicio pueden tener tokens sin caducidad'
					USING ERRCODE = 'check_violation';
			END IF;
			RETURN NEW;
		END
		$$ LANGUAGE plpgsql`,
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_token_expiry_guard' AND tgrelid = 'cloud_principal_tokens'::regclass) THEN
				CREATE TRIGGER nexo_token_expiry_guard BEFORE INSERT OR UPDATE OF nexo_expires_at ON cloud_principal_tokens
					FOR EACH ROW EXECUTE FUNCTION nexo_token_expiry_guard();
			END IF;
		END $$`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("nexo tokens: %w", err)
		}
	}
	return nil
}

// ApplyTokenIssue fija caducidad y alcance de un token recién creado, en la
// misma transacción. Devuelve los valores guardados.
func ApplyTokenIssue(ctx context.Context, tx *sql.Tx, tokenID string, t TokenIssue) (*time.Time, string, error) {
	var expires sql.NullTime
	var scope string
	err := tx.QueryRowContext(ctx, `
		UPDATE cloud_principal_tokens
		SET nexo_expires_at = CASE WHEN $2::int = 0 THEN NULL ELSE now() + make_interval(days => $2::int) END,
		    nexo_scope = $3
		WHERE id = $1::bigint
		RETURNING nexo_expires_at, nexo_scope`, strings.TrimSpace(tokenID), t.Days, t.Scope).Scan(&expires, &scope)
	if err != nil {
		return nil, "", fmt.Errorf("nexo: caducidad y alcance del token: %w", err)
	}
	if !expires.Valid {
		return nil, scope, nil
	}
	v := expires.Time
	return &v, scope, nil
}

// LoadTokenFacts lee el estado de un token por id (sesiones del panel, que
// no vuelven a presentar el token en cada petición).
func LoadTokenFacts(ctx context.Context, db *sql.DB, tokenID string) (TokenFacts, bool, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(tokenID), 10, 64)
	if err != nil {
		return TokenFacts{}, false, nil
	}
	var f TokenFacts
	var expires sql.NullTime
	err = db.QueryRowContext(ctx, `
		SELECT id::text, principal_id::text, nexo_scope, nexo_expires_at,
		       (nexo_expires_at IS NOT NULL AND nexo_expires_at <= NOW()), revoked_at IS NOT NULL
		FROM cloud_principal_tokens WHERE id = $1`, id).Scan(&f.TokenID, &f.PrincipalID, &f.Scope, &expires, &f.Expired, &f.Revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenFacts{}, false, nil
	}
	if err != nil {
		return TokenFacts{}, false, err
	}
	if expires.Valid {
		v := expires.Time
		f.ExpiresAt = &v
	}
	return f, true, nil
}

// TokenInfo son los datos de un token para /me y los listados.
type TokenInfo struct {
	ID         string     `json:"id"`
	Scope      string     `json:"scope"`
	ExpiresAt  *time.Time `json:"expires_at"`
	NoExpiry   bool       `json:"no_expiry"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// LoadTokenInfo lee caducidad, alcance y último uso de un token.
func LoadTokenInfo(ctx context.Context, db *sql.DB, tokenID string) (TokenInfo, bool, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(tokenID), 10, 64)
	if err != nil {
		return TokenInfo{}, false, nil
	}
	var info TokenInfo
	var expires, used sql.NullTime
	err = db.QueryRowContext(ctx, `SELECT id::text, nexo_scope, nexo_expires_at, last_used_at FROM cloud_principal_tokens WHERE id = $1`, id).
		Scan(&info.ID, &info.Scope, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenInfo{}, false, nil
	}
	if err != nil {
		return TokenInfo{}, false, err
	}
	if expires.Valid {
		v := expires.Time
		info.ExpiresAt = &v
	} else {
		info.NoExpiry = true
	}
	if used.Valid {
		v := used.Time
		info.LastUsedAt = &v
	}
	return info, true, nil
}
