package store

// Nexo IA · NX-10 entrega 2b — propuestas (C-07, KP-08).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// Proposal es una propuesta (nexo_kb_proposals).
type Proposal struct {
	ID              string     `json:"id"`
	SpaceID         string     `json:"-"`
	Space           string     `json:"space"`
	TargetDocID     string     `json:"-"`
	DocKey          string     `json:"doc_key,omitempty"`
	BaseVersion     int        `json:"base_version,omitempty"`
	Title           string     `json:"title"`
	Category        string     `json:"category,omitempty"`
	Tags            []string   `json:"tags"`
	ContentMD       string     `json:"content_md,omitempty"`
	Rationale       string     `json:"rationale,omitempty"`
	ValidFrom       *time.Time `json:"valid_from,omitempty"`
	ValidUntil      *time.Time `json:"valid_until,omitempty"`
	Status          string     `json:"status"`
	ProposedByID    string     `json:"proposed_by_principal_id,omitempty"`
	ProposedBy      string     `json:"proposed_by"`
	ProposedByEmail string     `json:"proposed_by_email,omitempty"`
	ProposedClaim   string     `json:"proposed_by_client,omitempty"`
	ReviewedByID    string     `json:"reviewed_by_principal_id,omitempty"`
	ReviewedBy      string     `json:"reviewed_by,omitempty"`
	ReviewNote      string     `json:"review_note,omitempty"`
	DecidedAt       *time.Time `json:"decided_at,omitempty"`
	ResultVersionID string     `json:"result_version_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// InsertProposal crea una propuesta abierta.
func InsertProposal(ctx context.Context, q Querier, p Proposal, act Actor) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO nexo_kb_proposals (id, space_id, target_document_id, base_version, doc_key, title, category, content_md, rationale, status,
			proposed_by_principal_id, proposed_by_token_id, proposed_by_label, proposed_by_email, proposed_by_client_claim, valid_from, valid_until,
			tags, proposed_by_ip)
		VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, NULLIF($4, 0), NULLIF($5, ''), $6, NULLIF($7, ''), $8, NULLIF($9, ''), 'open',
			$10::bigint, $11::bigint, $12, (SELECT u.email FROM cloud_human_users u WHERE u.principal_id = $10::bigint), NULLIF($13, ''), $14, $15,
			$16::text[], NULLIF($17, ''))`,
		p.ID, p.SpaceID, p.TargetDocID, p.BaseVersion, p.DocKey, p.Title, p.Category, p.ContentMD, p.Rationale,
		nullBigint(act.PrincipalID), nullBigint(act.TokenID), labelOrUnknown(act.Label), security.NexoRedactExternal(act.Claim, 0), nullTime(p.ValidFrom), nullTime(p.ValidUntil),
		nonNil(p.Tags), act.ClientIP)
	return err
}

const proposalColumns = `p.id::text, p.space_id::text, sp.slug, COALESCE(p.target_document_id::text, ''), COALESCE(p.doc_key, ''),
	COALESCE(p.base_version, 0), p.title, COALESCE(p.category, ''), array_to_json(p.tags)::text, p.content_md, COALESCE(p.rationale, ''),
	p.valid_from, p.valid_until, p.status, COALESCE(p.proposed_by_principal_id::text, ''), p.proposed_by_label, COALESCE(p.proposed_by_email, ''),
	COALESCE(p.proposed_by_client_claim, ''), COALESCE(p.reviewed_by_principal_id::text, ''), COALESCE(p.reviewed_by_label, ''),
	COALESCE(p.review_note, ''), p.decided_at, COALESCE(p.result_version_id::text, ''), p.created_at, p.updated_at`

func scanProposal(row interface{ Scan(...any) error }) (Proposal, error) {
	var p Proposal
	var tags string
	var vf, vu, dec sql.NullTime
	err := row.Scan(&p.ID, &p.SpaceID, &p.Space, &p.TargetDocID, &p.DocKey, &p.BaseVersion, &p.Title, &p.Category, &tags, &p.ContentMD, &p.Rationale,
		&vf, &vu, &p.Status, &p.ProposedByID, &p.ProposedBy, &p.ProposedByEmail, &p.ProposedClaim, &p.ReviewedByID, &p.ReviewedBy, &p.ReviewNote,
		&dec, &p.ResultVersionID, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	_ = json.Unmarshal([]byte(tags), &p.Tags)
	if p.Tags == nil {
		p.Tags = []string{}
	}
	// NX-07: etiquetas, nota de revisión, autor, revisor y declaración del cliente (filas históricas) salen sin credenciales.
	for i := range p.Tags {
		p.Tags[i] = redact.Text(p.Tags[i])
	}
	p.ReviewNote, p.ProposedBy, p.ProposedByEmail, p.ProposedClaim, p.ReviewedBy = redact.Text(p.ReviewNote), redact.Text(p.ProposedBy), redact.Text(p.ProposedByEmail), redact.Text(p.ProposedClaim), redact.Text(p.ReviewedBy)
	p.DocKey, p.Category = redact.Text(p.DocKey), redact.Text(p.Category)
	p.ValidFrom, p.ValidUntil, p.DecidedAt = timePtr(vf), timePtr(vu), timePtr(dec)
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return p, nil
}

// GetProposal devuelve una propuesta (forUpdate bloquea su fila).
func GetProposal(ctx context.Context, q Querier, id string, forUpdate bool) (Proposal, bool, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE OF p"
	}
	p, err := scanProposal(q.QueryRowContext(ctx, `SELECT `+proposalColumns+` FROM nexo_kb_proposals p JOIN nexo_kb_spaces sp ON sp.id = p.space_id
		WHERE p.id = $1::uuid`+lock, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, false, nil
	}
	return p, err == nil, err
}

// ProposalFilter filtra el listado: las propuestas de ReviewSpaceIDs (todas)
// más las propias de AuthorID (en OwnSpaceIDs).
type ProposalFilter struct {
	ReviewSpaceIDs []string
	OwnSpaceIDs    []string
	AuthorID       string
	Status         string
	Before         *time.Time
	Limit          int
	WithContent    bool
}

// ListProposals lista propuestas, de la más nueva a la más antigua.
func ListProposals(ctx context.Context, q Querier, f ProposalFilter) ([]Proposal, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+proposalColumns+` FROM nexo_kb_proposals p JOIN nexo_kb_spaces sp ON sp.id = p.space_id
		WHERE (p.space_id = ANY($1::uuid[]) OR (p.space_id = ANY($2::uuid[]) AND p.proposed_by_principal_id = $3::bigint))
		  AND ($4 = '' OR p.status = $4) AND ($5::timestamptz IS NULL OR p.created_at < $5)
		ORDER BY p.created_at DESC, p.id LIMIT $6`, nonNil(f.ReviewSpaceIDs), nonNil(f.OwnSpaceIDs), nullBigint(f.AuthorID), f.Status, nullTime(f.Before), f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Proposal{}
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		if !f.WithContent {
			p.ContentMD = ""
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DecideProposal cierra una propuesta abierta (aprobada, descartada o
// retirada por su autor). Devuelve false si ya no estaba abierta.
func DecideProposal(ctx context.Context, q Querier, id, status string, act Actor, note, resultVersionID string, now time.Time) (bool, error) {
	res, err := q.ExecContext(ctx, `
		UPDATE nexo_kb_proposals SET status = $2, reviewed_by_principal_id = $3::bigint, reviewed_by_token_id = $4::bigint, reviewed_by_label = $5,
			reviewed_by_email = (SELECT u.email FROM cloud_human_users u WHERE u.principal_id = $3::bigint),
			review_note = NULLIF($6, ''), decided_at = $7, result_version_id = NULLIF($8, '')::uuid, updated_at = $7
		WHERE id = $1::uuid AND status = 'open'`,
		id, status, nullBigint(act.PrincipalID), nullBigint(act.TokenID), labelOrUnknown(act.Label), security.NexoRedactExternal(note, 0), now, resultVersionID) // NX-07: la nota se guarda sin credenciales
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// VersionID devuelve el id de una versión de un documento.
func VersionID(ctx context.Context, q Querier, docID string, version int) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT id::text FROM nexo_kb_versions WHERE document_id = $1::uuid AND version = $2`, docID, version).Scan(&id)
	return id, err
}
