package cloudstore

// NX-07: emisión, canje y revocación atómicos; sin secretos en la auditoría.
//
// Todas las transacciones de este archivo usan únicamente su propia
// transacción (nunca piden otra conexión del pool mientras están abiertas,
// OP-13). Orden de bloqueos en canje, revocación y emisión: persona, código,
// cadena de auditoría.
import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

// NexoActivation contiene únicamente metadatos públicos del código.
type NexoActivation struct {
	ID          string    `json:"id"`
	PrincipalID string    `json:"principal_id"`
	Name        string    `json:"name"`
	Scope       string    `json:"scope"`
	TokenDays   int       `json:"token_days"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedBy   string    `json:"created_by_principal_id"`
}

// Códigos de rechazo del canje.
const (
	NexoActivationInvalid       = "activation_invalid"
	NexoActivationUsed          = "activation_used"
	NexoActivationExpired       = "activation_expired"
	NexoActivationRevoked       = "activation_revoked"
	NexoActivationDisabled      = "activation_disabled"
	NexoActivationPolicyChanged = "activation_policy_changed" // la política vigente ya no permite el alcance o la vida del código
	NexoActivationSelfRedeem    = "activation_self_redeem"    // lo intenta canjear quien lo emitió
)

type NexoActivationError struct{ Code string }

func (e *NexoActivationError) Error() string { return e.Code }

// CountsAsFailure indica si el rechazo suma al bloqueo por intentos fallidos.
// Un código válido cuya política cambió, o que intenta canjear su emisor, no
// es un intento de adivinar códigos.
func (e *NexoActivationError) CountsAsFailure() bool {
	return e.Code != NexoActivationPolicyChanged && e.Code != NexoActivationSelfRedeem
}

// NexoRedeemCaller es quien presenta el código si la petición llegó
// autenticada (token o sesión del panel). Vacío si es anónima.
type NexoRedeemCaller struct{ PrincipalID, TokenID string }

// nexoSameIPWindow: un canje desde la IP del emisor dentro de este plazo
// levanta una alerta (indicio, no prueba).
const nexoSameIPWindow = time.Minute

func nexoMigrateActivations(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
 CREATE TABLE IF NOT EXISTS nexo_activation_codes (
  id BIGSERIAL PRIMARY KEY,
  principal_id BIGINT NOT NULL REFERENCES cloud_principals(id),
  code_hash TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL DEFAULT '',
  scope TEXT NOT NULL CHECK(scope IN ('read','write')),
  token_days INTEGER NOT NULL CHECK(token_days BETWEEN 1 AND 36500),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  created_by_principal_id BIGINT REFERENCES cloud_principals(id),
  used_at TIMESTAMPTZ,
  token_id BIGINT REFERENCES cloud_principal_tokens(id),
  revoked_at TIMESTAMPTZ,
  failed_attempts INTEGER NOT NULL DEFAULT 0,
  issued_by_token_id BIGINT,
  issued_ip TEXT
 );
 ALTER TABLE nexo_activation_codes ADD COLUMN IF NOT EXISTS issued_by_token_id BIGINT;
 ALTER TABLE nexo_activation_codes ADD COLUMN IF NOT EXISTS issued_ip TEXT;
 CREATE INDEX IF NOT EXISTS nexo_activation_pending_idx ON nexo_activation_codes(principal_id,id)
 WHERE used_at IS NULL AND revoked_at IS NULL;
 `)
	return err
}

// NexoIssueActivation guarda el hash de un código nuevo para una persona.
// issuerTokenID es el token (o la sesión) con que el administrador lo emite.
func (cs *CloudStore) NexoIssueActivation(ctx context.Context, target, hash, name string, issuerTokenID string, audit AuthAuditEvent) (NexoActivation, error) {
	if err := cs.nexoReady(); err != nil {
		return NexoActivation{}, err
	}
	name, err := security.NexoCheckTokenName(name)
	if err != nil {
		return NexoActivation{}, err
	}
	ttl, err := security.ActivationTTLFromEnv()
	if err != nil {
		return NexoActivation{}, err
	}
	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return NexoActivation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockPrincipalTokenTargetTx(ctx, tx, target); err != nil {
		return NexoActivation{}, err
	}
	var kind string
	if err = tx.QueryRowContext(ctx, "SELECT kind FROM cloud_principals WHERE id=$1", target).Scan(&kind); err != nil {
		return NexoActivation{}, err
	}
	if kind != "human" {
		return NexoActivation{}, errors.New("la activación exige una persona")
	}
	issue := security.TokenIssueFrom(ctx)
	issuedIP := ""
	if origin, ok := NexoRequestOriginFrom(ctx); ok {
		issuedIP = nexoTruncateRunes(strings.TrimSpace(origin.ClientIP), 64)
	}
	var a NexoActivation
	err = tx.QueryRowContext(ctx, `INSERT INTO nexo_activation_codes(principal_id,code_hash,name,scope,token_days,expires_at,created_by_principal_id,issued_by_token_id,issued_ip)
 VALUES($1,$2,$3,$4,$5,now()+make_interval(secs=>$6::double precision),$7::bigint,NULLIF($8,'')::bigint,NULLIF($9,''))
 RETURNING id::text,principal_id::text,name,scope,token_days,created_at,expires_at,created_by_principal_id::text`,
		target, hash, name, issue.Scope, issue.Days, ttl.Seconds(), audit.ActorPrincipalID, strings.TrimSpace(issuerTokenID), issuedIP).
		Scan(&a.ID, &a.PrincipalID, &a.Name, &a.Scope, &a.TokenDays, &a.CreatedAt, &a.ExpiresAt, &a.CreatedBy)
	if err != nil {
		return a, err
	}
	audit.Action = "nexo.activation.issued"
	audit.Metadata = map[string]any{"activation_id": a.ID, "scope": a.Scope, "expires_in_days": a.TokenDays, "expires_at": a.ExpiresAt}
	if err = insertAuthAuditEvent(ctx, tx, audit); err != nil {
		return a, err
	}
	return a, tx.Commit()
}

func (cs *CloudStore) NexoPendingActivations(ctx context.Context, target string) ([]NexoActivation, error) {
	if err := cs.nexoReady(); err != nil {
		return nil, err
	}
	rows, err := cs.db.QueryContext(ctx, `SELECT id::text,principal_id::text,name,scope,token_days,created_at,expires_at,created_by_principal_id::text
 FROM nexo_activation_codes WHERE principal_id=$1::bigint AND used_at IS NULL AND revoked_at IS NULL AND expires_at>now() ORDER BY id DESC`, target)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []NexoActivation{}
	for rows.Next() {
		var a NexoActivation
		if err = rows.Scan(&a.ID, &a.PrincipalID, &a.Name, &a.Scope, &a.TokenDays, &a.CreatedAt, &a.ExpiresAt, &a.CreatedBy); err != nil {
			return nil, err
		}
		a.Name = security.NexoRedactExternal(a.Name, security.NexoMaxTokenNameRunes) // filas históricas
		out = append(out, a)
	}
	return out, rows.Err()
}

func (cs *CloudStore) NexoRevokeActivation(ctx context.Context, id, target string, audit AuthAuditEvent) (bool, error) {
	if err := cs.nexoReady(); err != nil {
		return false, err
	}
	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	// Mismo orden que el canje: persona, código, cadena de auditoría.
	var pid string
	if err = tx.QueryRowContext(ctx, "SELECT id::text FROM cloud_principals WHERE id=$1::bigint FOR UPDATE", target).Scan(&pid); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE nexo_activation_codes SET revoked_at=now() WHERE id=$1::bigint AND principal_id=$2::bigint AND used_at IS NULL AND revoked_at IS NULL`, id, target)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	audit.Action = "nexo.activation.revoked"
	audit.Metadata = map[string]any{"activation_id": id}
	if err = insertAuthAuditEvent(ctx, tx, audit); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// NexoRedeemActivation consume el código y crea el token en la misma transacción.
// El bloqueo de la persona serializa deshabilitación y canje. El del código
// serializa canjes y revocaciones; los fallos se confirman como evidencia.
//
// Antes de consumir se revalida contra la política vigente: el alcance y los
// días pedidos al emitir deben seguir permitidos (NEXO_TOKEN_MAX_DAYS puede
// haber bajado) y quien presenta el código no puede ser quien lo emitió. En
// ambos casos el código no se consume.
func (cs *CloudStore) NexoRedeemActivation(ctx context.Context, hash string, params CreatePrincipalTokenParams, caller NexoRedeemCaller) (PrincipalToken, error) {
	if err := cs.nexoReady(); err != nil {
		return PrincipalToken{}, err
	}
	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return PrincipalToken{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var target string
	err = tx.QueryRowContext(ctx, "SELECT principal_id::text FROM nexo_activation_codes WHERE code_hash=$1", hash).Scan(&target)
	audit := AuthAuditEvent{ActorSource: "activation", Action: "nexo.activation.failed", Outcome: "denied", Metadata: map[string]any{}}
	if errors.Is(err, sql.ErrNoRows) {
		audit.ReasonCode = NexoActivationInvalid
		if err = insertAuthAuditEvent(ctx, tx, audit); err != nil {
			return PrincipalToken{}, err
		}
		if err = tx.Commit(); err != nil {
			return PrincipalToken{}, err
		}
		return PrincipalToken{}, &NexoActivationError{Code: audit.ReasonCode}
	}
	if err != nil {
		return PrincipalToken{}, err
	}
	var enabled bool
	var kind string
	if err = tx.QueryRowContext(ctx, "SELECT enabled,kind FROM cloud_principals WHERE id=$1::bigint FOR UPDATE", target).Scan(&enabled, &kind); err != nil {
		return PrincipalToken{}, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('engram_cloud_active_admin_guard'))`); err != nil {
		return PrincipalToken{}, err
	}
	var a NexoActivation
	var stored, issuerToken, issuedIP string
	var used, revoked sql.NullTime
	var expired bool
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT id::text,name,scope,token_days,code_hash,used_at,revoked_at,expires_at<=clock_timestamp(),failed_attempts,
 COALESCE(created_by_principal_id::text,''),COALESCE(issued_by_token_id::text,''),COALESCE(issued_ip,'')
 FROM nexo_activation_codes WHERE code_hash=$1 FOR UPDATE`, hash).
		Scan(&a.ID, &a.Name, &a.Scope, &a.TokenDays, &stored, &used, &revoked, &expired, &attempts, &a.CreatedBy, &issuerToken, &issuedIP)
	if err != nil {
		return PrincipalToken{}, err
	}
	audit.TargetPrincipalID = target
	audit.Metadata["activation_id"] = a.ID
	_, policyErr := security.ResolveIssue(security.IssueRequest{ExpiresInDays: &a.TokenDays, Scope: a.Scope}, "human", false)
	switch {
	case subtle.ConstantTimeCompare([]byte(stored), []byte(hash)) != 1:
		audit.ReasonCode = NexoActivationInvalid
	case used.Valid:
		audit.ReasonCode = NexoActivationUsed
	case revoked.Valid:
		audit.ReasonCode = NexoActivationRevoked
	case expired:
		audit.ReasonCode = NexoActivationExpired
	case !enabled || kind != "human":
		audit.ReasonCode = NexoActivationDisabled
	case nexoIssuerRedeems(caller, a.CreatedBy, issuerToken):
		audit.ReasonCode = NexoActivationSelfRedeem
		audit.ActorPrincipalID = caller.PrincipalID
	case policyErr != nil:
		audit.ReasonCode = NexoActivationPolicyChanged
	}
	sameIP := false
	if audit.ReasonCode == "" {
		// La ventana de "misma IP" se mide con el reloj de la base, en la misma sentencia que consume.
		err = tx.QueryRowContext(ctx, `UPDATE nexo_activation_codes SET used_at=clock_timestamp() WHERE id=$1::bigint AND used_at IS NULL AND revoked_at IS NULL AND expires_at>clock_timestamp()
 RETURNING created_at>clock_timestamp()-make_interval(secs=>$2::double precision)`, a.ID, nexoSameIPWindow.Seconds()).Scan(&sameIP)
		if errors.Is(err, sql.ErrNoRows) {
			audit.ReasonCode = NexoActivationExpired
		} else if err != nil {
			return PrincipalToken{}, err
		}
	}
	if audit.ReasonCode != "" {
		denial := &NexoActivationError{Code: audit.ReasonCode}
		if denial.CountsAsFailure() {
			if _, err = tx.ExecContext(ctx, "UPDATE nexo_activation_codes SET failed_attempts=failed_attempts+1 WHERE id=$1::bigint", a.ID); err != nil {
				return PrincipalToken{}, err
			}
			rules, _ := evidence.RulesFromEnv()
			cfg := rules[evidence.RuleActivationRepeated]
			if cfg.Enabled && (used.Valid || attempts+1 >= cfg.Threshold) {
				_, err = evidence.InsertAlert(ctx, tx, evidence.NewAlert{Rule: evidence.RuleActivationRepeated, Severity: cfg.Severity, Layer: evidence.LayerPlatform,
					Subject: "activation:" + a.ID, DedupKey: "activation:" + a.ID, Summary: "Intentos repetidos de canje de un código de activación",
					Details: map[string]any{"activation_id": a.ID, "principal_id": target, "reason": audit.ReasonCode, "attempts": attempts + 1}})
				if err != nil {
					return PrincipalToken{}, err
				}
			}
		}
		if err = insertAuthAuditEvent(ctx, tx, audit); err != nil {
			return PrincipalToken{}, err
		}
		if err = tx.Commit(); err != nil {
			return PrincipalToken{}, err
		}
		return PrincipalToken{}, denial
	}
	name, nameErr := security.NexoCheckTokenName(a.Name)
	if nameErr != nil {
		name = "" // código histórico con un nombre que hoy no se aceptaría
	}
	token, err := scanPrincipalToken(tx.QueryRowContext(ctx, `INSERT INTO cloud_principal_tokens(principal_id,token_prefix,token_hash,name,created_by_principal_id)
 VALUES($1::bigint,$2,$3,$4,$1::bigint)
 RETURNING id::text,principal_id::text,token_prefix,'' AS token_hash,name,created_by_principal_id::text,created_at,last_used_at,revoked_at,COALESCE(revoked_by_principal_id::text,''),COALESCE(revocation_reason,'')`, target, params.TokenPrefix, params.TokenHash, name))
	if err != nil {
		return PrincipalToken{}, err
	}
	ctx = security.WithTokenIssue(ctx, security.TokenIssue{Days: a.TokenDays, Scope: a.Scope})
	audit.Action = "nexo.activation.redeemed"
	audit.Outcome = "success"
	audit.ActorPrincipalID = target
	audit.Metadata["credential_id"] = token.ID
	if err = nexoApplyTokenIssue(ctx, tx, &token, &audit); err != nil {
		return PrincipalToken{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE nexo_activation_codes SET token_id=$2::bigint WHERE id=$1::bigint", a.ID, token.ID); err != nil {
		return PrincipalToken{}, err
	}
	if origin, ok := NexoRequestOriginFrom(ctx); ok && sameIP && issuedIP != "" && strings.TrimSpace(origin.ClientIP) == issuedIP {
		// Heurística: indicio de que quien emitió el código lo canjeó. No es prueba (NAT, proxy, mismo equipo).
		audit.Metadata["issuer_ip_match"] = true
		rules, _ := evidence.RulesFromEnv()
		if cfg := rules[evidence.RuleActivationSameIP]; cfg.Enabled {
			if _, err = evidence.InsertAlert(ctx, tx, evidence.NewAlert{Rule: evidence.RuleActivationSameIP, Severity: cfg.Severity, Layer: evidence.LayerPlatform,
				Subject: "activation:" + a.ID, DedupKey: "activation_same_ip:" + a.ID,
				Summary: "Un código de activación se canjeó desde la misma IP que lo emitió en menos de un minuto (indicio, no prueba)",
				Details: map[string]any{"activation_id": a.ID, "principal_id": target, "issuer_principal_id": a.CreatedBy, "heuristic": true}}); err != nil {
				return PrincipalToken{}, err
			}
		}
	}
	if err = insertAuthAuditEvent(ctx, tx, audit); err != nil {
		return PrincipalToken{}, err
	}
	if err = tx.Commit(); err != nil {
		return PrincipalToken{}, err
	}
	return token, nil
}

// nexoIssuerRedeems es verdadero si quien presenta el código llegó autenticado
// como el administrador que lo emitió (misma persona o mismo token).
func nexoIssuerRedeems(caller NexoRedeemCaller, issuerPrincipal, issuerToken string) bool {
	if p := strings.TrimSpace(caller.PrincipalID); p != "" && p == strings.TrimSpace(issuerPrincipal) {
		return true
	}
	t := strings.TrimSpace(caller.TokenID)
	return t != "" && t == strings.TrimSpace(issuerToken)
}
