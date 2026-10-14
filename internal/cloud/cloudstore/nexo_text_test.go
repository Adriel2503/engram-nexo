package cloudstore

// NX-07: texto externo de personas, tokens y del tablero de Engram. Postgres real.

import (
	"errors"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

func nexoTextProbes(t *testing.T) (token, code string) {
	t.Helper()
	code, err := security.GenerateActivationCode()
	if err != nil {
		t.Fatal(err)
	}
	return "egc_live_0123abcd_" + strings.Repeat("Zy9-", 11), code
}

func TestNexoHumanUserRejectsCredentialsAndReadsRedacted(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	token, code := nexoTextProbes(t)
	for _, p := range []CreateHumanUserParams{
		{Username: "ana-" + token, Email: "ana@rp.pe", Role: "member"},
		{Username: "ana", Email: code + "@rp.pe", Role: "member"},
		{Username: "ana", Email: "ana@rp.pe", DisplayName: "Ana " + code, Role: "member"},
	} {
		if _, err := cs.CreateHumanUser(t.Context(), p); !errors.Is(err, ErrCredentialInText) {
			t.Fatalf("debía rechazar %+v: %v", p, err)
		}
		if _, err := cs.CreateFirstAdminHumanUser(t.Context(), p); !errors.Is(err, ErrCredentialInText) {
			t.Fatalf("el primer administrador también valida: %v", err)
		}
	}
	if n := nexoCountRows(t, cs, "cloud_human_users"); n != 0 {
		t.Fatalf("no debía crearse ninguna persona: %d", n)
	}
	// Filas históricas con credenciales: la lectura las limpia.
	if _, err := cs.db.ExecContext(t.Context(), `WITH p AS (INSERT INTO cloud_principals (kind, display_name, role) VALUES ('human',$1,'member') RETURNING id)
		INSERT INTO cloud_human_users (principal_id, username, email) SELECT id, $2, $3 FROM p`, "Ana "+code, "ana-"+token, "ana@"+code); err != nil {
		t.Fatal(err)
	}
	users, err := cs.ListHumanUsers(t.Context())
	if err != nil || len(users) != 1 {
		t.Fatalf("listar: %v %d", err, len(users))
	}
	for _, v := range []string{users[0].Username, users[0].Email, users[0].DisplayName} {
		if strings.Contains(v, token) || strings.Contains(v, code) {
			t.Fatalf("la lectura devolvió una credencial: %q", v)
		}
	}
}

func nexoCountRows(t *testing.T, cs *CloudStore, table string) int {
	t.Helper()
	var n int
	if err := cs.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNexoRevokeReasonValidatedAndHistoricReasonRedacted(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	token, code := nexoTextProbes(t)
	ana := nexoSeedUser(t, cs, "ana", "ana@rp.pe", "laptop")
	if err := cs.RevokePrincipalToken(t.Context(), ana.TokenIDs["laptop"], ana.PrincipalID, "ya no lo uso "+token); !errors.Is(err, ErrCredentialInText) {
		t.Fatalf("un motivo con credencial debía rechazarse: %v", err)
	}
	if err := cs.RevokePrincipalToken(t.Context(), ana.TokenIDs["laptop"], ana.PrincipalID, "rotado"); err != nil {
		t.Fatal(err)
	}
	// Fila histórica: un motivo escrito antes de la validación.
	if _, err := cs.db.ExecContext(t.Context(), `UPDATE cloud_principal_tokens SET revocation_reason = $1, name = $2 WHERE id = $3::bigint`, "motivo "+code, "nombre "+token, ana.TokenIDs["laptop"]); err != nil {
		t.Fatal(err)
	}
	tokens, err := cs.ListPrincipalTokens(t.Context(), ana.PrincipalID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("listar: %v %d", err, len(tokens))
	}
	if strings.Contains(tokens[0].RevocationReason, code) || strings.Contains(tokens[0].Name, token) || !strings.Contains(tokens[0].RevocationReason, "motivo") {
		t.Fatalf("el motivo y el nombre históricos deben salir sin credenciales y conservar el resto: %+v", tokens[0])
	}
}

// Tablero de Engram: motivo de pausa, quién la cambió y auditoría de sincronización.
func TestNexoEngramAdminTextRedacted(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	token, code := nexoTextProbes(t)
	if err := cs.SetProjectSyncEnabled("proy-a", false, "ana "+token, "pausa "+code); err != nil {
		t.Fatal(err)
	}
	var reason, by string
	if err := cs.db.QueryRowContext(t.Context(), `SELECT paused_reason, updated_by FROM cloud_project_controls WHERE project = 'proy-a'`).Scan(&reason, &by); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reason+by, token) || strings.Contains(reason+by, code) || !strings.HasPrefix(reason, "pausa ") {
		t.Fatalf("el motivo se guardó con credenciales: %q %q", reason, by)
	}
	// Fila histórica escrita antes de la redacción.
	if _, err := cs.db.ExecContext(t.Context(), `UPDATE cloud_project_controls SET paused_reason = $1, updated_by = $2`, "hist "+code, "quien "+token); err != nil {
		t.Fatal(err)
	}
	ctrl, err := cs.GetProjectSyncControl("proy-a")
	if err != nil || ctrl == nil || ctrl.PausedReason == nil || strings.Contains(*ctrl.PausedReason, code) {
		t.Fatalf("GetProjectSyncControl: %+v %v", ctrl, err)
	}
	list, err := cs.ListProjectSyncControls()
	if err != nil || len(list) != 1 || (list[0].PausedReason != nil && strings.Contains(*list[0].PausedReason, code)) || (list[0].UpdatedBy != nil && strings.Contains(*list[0].UpdatedBy, token)) {
		t.Fatalf("ListProjectSyncControls: %+v %v", list, err)
	}
	if err := cs.InsertAuditEntry(t.Context(), AuditEntry{Contributor: "ana " + token, Project: "proy-a", Action: AuditActionMutationPush, Outcome: AuditOutcomeRejectedProjectPaused, Metadata: map[string]any{"nota": code}}); err != nil {
		t.Fatal(err)
	}
	rows, _, err := cs.ListAuditEntriesPaginated(t.Context(), AuditFilter{}, 10, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("auditoría: %v %d", err, len(rows))
	}
	if strings.Contains(rows[0].Contributor, token) || strings.Contains(strings.Join([]string{rows[0].Contributor}, ""), code) {
		t.Fatalf("contribuidor con credencial: %q", rows[0].Contributor)
	}
	if v, _ := rows[0].Metadata["nota"].(string); strings.Contains(v, code) {
		t.Fatalf("metadatos con credencial: %v", rows[0].Metadata)
	}
}
