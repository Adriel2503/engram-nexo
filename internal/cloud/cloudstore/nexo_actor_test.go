package cloudstore

// Nexo IA · NX-01 — tests de integración (Postgres, CLOUDSTORE_TEST_DSN).

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/chunkcodec"
)

type nexoSeededUser struct {
	PrincipalID string
	TokenIDs    map[string]string // nombre del token → id
}

// nexoSeedUser crea un usuario administrado con email y tokens nombrados.
func nexoSeedUser(t *testing.T, cs *CloudStore, username, email string, tokenNames ...string) nexoSeededUser {
	t.Helper()
	ctx := context.Background()
	var principalID string
	if err := cs.db.QueryRowContext(ctx, `INSERT INTO cloud_principals (kind, display_name, role) VALUES ('human', $1, 'member') RETURNING id::text`, username).Scan(&principalID); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	if _, err := cs.db.ExecContext(ctx, `INSERT INTO cloud_human_users (principal_id, username, email) VALUES ($1::bigint, $2, $3)`, principalID, username, email); err != nil {
		t.Fatalf("seed human user: %v", err)
	}
	seeded := nexoSeededUser{PrincipalID: principalID, TokenIDs: map[string]string{}}
	for _, name := range tokenNames {
		var tokenID string
		if err := cs.db.QueryRowContext(ctx, `INSERT INTO cloud_principal_tokens (principal_id, token_prefix, token_hash, name) VALUES ($1::bigint, $2, $3, $4) RETURNING id::text`,
			principalID, "egc_test_"+name, "hash-"+username+"-"+name, name).Scan(&tokenID); err != nil {
			t.Fatalf("seed token: %v", err)
		}
		seeded.TokenIDs[name] = tokenID
	}
	return seeded
}

func nexoVerifiedActor(name string, user nexoSeededUser, tokenName, claim string) MutationActor {
	return MutationActor{Name: name, PrincipalID: user.PrincipalID, TokenID: user.TokenIDs[tokenName], Verified: true, ClientClaim: claim}
}

func nexoObservationEntry(project, syncID, title string) MutationEntry {
	payload, _ := json.Marshal(map[string]any{
		"sync_id": syncID, "session_id": "s-nexo", "type": "decision", "title": title,
		"content": "contenido " + title, "scope": "project", "project": project,
	})
	return MutationEntry{Project: project, Entity: "observation", EntityKey: syncID, Op: "upsert", Payload: payload}
}

func nexoSessionEntry(project string) MutationEntry {
	return MutationEntry{Project: project, Entity: "session", EntityKey: "s-nexo", Op: "upsert", Payload: json.RawMessage(`{"id":"s-nexo","directory":"/tmp/s-nexo"}`)}
}

type nexoHistoryRow struct {
	Seq             int64
	Op              string
	Actor           sql.NullString
	ActorEmail      sql.NullString
	Verified        bool
	PrincipalID     sql.NullString
	CurrentEmail    sql.NullString
	TokenName       sql.NullString
	ClientClaim     sql.NullString
	Title           sql.NullString
	TokenID         sql.NullString
	CurrentUsername sql.NullString
}

func nexoHistory(t *testing.T, cs *CloudStore, entityKey string) []nexoHistoryRow {
	t.Helper()
	rows, err := cs.db.QueryContext(context.Background(), `
		SELECT seq, op, actor, actor_email, actor_verified, actor_principal_id, current_email, token_name,
		       actor_client_claim, title, actor_token_id, username
		FROM nexo_mutation_history WHERE entity_key = $1 ORDER BY seq`, entityKey)
	if err != nil {
		t.Fatalf("query nexo_mutation_history: %v", err)
	}
	defer rows.Close()
	var out []nexoHistoryRow
	for rows.Next() {
		var r nexoHistoryRow
		if err := rows.Scan(&r.Seq, &r.Op, &r.Actor, &r.ActorEmail, &r.Verified, &r.PrincipalID, &r.CurrentEmail, &r.TokenName, &r.ClientClaim, &r.Title, &r.TokenID, &r.CurrentUsername); err != nil {
			t.Fatalf("scan history: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate history: %v", err)
	}
	return out
}

func TestNexoMigrationsAreIdempotentAndAdditive(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := cs.migrate(ctx); err != nil {
			t.Fatalf("re-run migrate #%d: %v", i+1, err)
		}
	}
	for _, table := range []string{"cloud_mutations", "cloud_chunks"} {
		for _, column := range []string{"actor", "actor_email", "actor_principal_id", "actor_token_id", "actor_verified", "actor_client_claim"} {
			var n int
			if err := cs.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, table, column).Scan(&n); err != nil {
				t.Fatalf("columns query: %v", err)
			}
			if n != 1 {
				t.Fatalf("%s.%s missing after migrate", table, column)
			}
		}
	}
	for _, rel := range []string{"nexo_mutation_history", "nexo_activity_log", "nexo_actor_presence"} {
		var exists bool
		if err := cs.db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, rel).Scan(&exists); err != nil || !exists {
			t.Fatalf("relation %s missing (err=%v)", rel, err)
		}
	}
	var triggers int
	if err := cs.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_trigger WHERE tgname LIKE 'nexo_%' AND tgrelid IN ('cloud_mutations'::regclass, 'cloud_chunks'::regclass, 'nexo_activity_log'::regclass)`).Scan(&triggers); err != nil {
		t.Fatalf("trigger count: %v", err)
	}
	if triggers != 4 {
		t.Fatalf("nexo triggers = %d, want 4 (no duplicates after re-migrate)", triggers)
	}
}

// CA-01, CA-02, CA-06, CA-08 (camino de paquetes /sync/push).
func TestNexoWriteChunkStampsVerifiedActorOnChunkAndMutations(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ana := nexoSeedUser(t, cs, "ana", "ana@rp.pe", "laptop-ana", "ci")
	project := "nexo-transversal"
	payload, err := chunkcodec.CanonicalizeForProject([]byte(`{
		"sessions":[{"id":"s-1","directory":"/tmp/s-1"}],
		"observations":[{"sync_id":"obs-ana-1","session_id":"s-1","type":"decision","title":"Decisión de Ana","content":"x","scope":"project"}]
	}`), project)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	ctx := WithMutationActor(context.Background(), nexoVerifiedActor("ana", ana, "laptop-ana", "Administrador"))
	if err := cs.WriteChunk(ctx, project, chunkIDFromPayload(payload), "ana", "", payload); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}

	var actor, email, principalID, tokenID, claim, createdBy string
	var verified bool
	if err := cs.db.QueryRow(`SELECT created_by, actor, actor_email, actor_principal_id, actor_token_id, actor_verified, actor_client_claim FROM cloud_chunks WHERE project_name = $1`, project).
		Scan(&createdBy, &actor, &email, &principalID, &tokenID, &verified, &claim); err != nil {
		t.Fatalf("read chunk actor: %v", err)
	}
	if createdBy != "ana" || actor != "ana" || email != "ana@rp.pe" || principalID != ana.PrincipalID || tokenID != ana.TokenIDs["laptop-ana"] || !verified || claim != "Administrador" {
		t.Fatalf("chunk actor = created_by=%q actor=%q email=%q principal=%q token=%q verified=%v claim=%q", createdBy, actor, email, principalID, tokenID, verified, claim)
	}

	history := nexoHistory(t, cs, "obs-ana-1")
	if len(history) != 1 {
		t.Fatalf("history rows = %d, want 1", len(history))
	}
	h := history[0]
	if h.Actor.String != "ana" || h.ActorEmail.String != "ana@rp.pe" || !h.Verified || h.TokenName.String != "laptop-ana" || h.ClientClaim.String != "Administrador" || h.Title.String != "Decisión de Ana" || h.CurrentUsername.String != "ana" {
		t.Fatalf("unexpected history row: %+v", h)
	}

	// CA-06: el mismo usuario desde otro token.
	ctxCI := WithMutationActor(context.Background(), nexoVerifiedActor("ana", ana, "ci", "runner"))
	if _, err := cs.InsertMutationBatch(ctxCI, []MutationEntry{nexoObservationEntry(project, "obs-ana-1", "Decisión de Ana v2")}); err != nil {
		t.Fatalf("InsertMutationBatch (ci): %v", err)
	}
	history = nexoHistory(t, cs, "obs-ana-1")
	if len(history) != 2 || history[1].TokenName.String != "ci" || history[0].TokenName.String != "laptop-ana" {
		t.Fatalf("expected two versions from tokens laptop-ana then ci, got %+v", history)
	}

	// CA-08: el admin cambia el email; la copia histórica no cambia.
	if _, err := cs.db.Exec(`UPDATE cloud_human_users SET email = 'ana.nueva@rp.pe' WHERE principal_id = $1::bigint`, ana.PrincipalID); err != nil {
		t.Fatalf("update email: %v", err)
	}
	history = nexoHistory(t, cs, "obs-ana-1")
	if history[0].ActorEmail.String != "ana@rp.pe" || history[0].CurrentEmail.String != "ana.nueva@rp.pe" {
		t.Fatalf("email copy = %q current = %q", history[0].ActorEmail.String, history[0].CurrentEmail.String)
	}
}

// CA-03, CA-04 (camino de cambios /sync/mutations/push: edición y borrado).
func TestNexoMutationBatchHistoryAcrossAuthorsAndDelete(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ana := nexoSeedUser(t, cs, "ana", "ana@rp.pe", "laptop-ana")
	luis := nexoSeedUser(t, cs, "luis", "luis@rp.pe", "laptop-luis")
	project := "nexo-transversal"

	anaCtx := WithMutationActor(context.Background(), nexoVerifiedActor("ana", ana, "laptop-ana", "ANA-PC"))
	if _, err := cs.InsertMutationBatch(anaCtx, []MutationEntry{nexoSessionEntry(project), nexoObservationEntry(project, "obs-1", "v1")}); err != nil {
		t.Fatalf("ana batch: %v", err)
	}
	luisCtx := WithMutationActor(context.Background(), nexoVerifiedActor("luis", luis, "laptop-luis", "LUIS-PC"))
	if _, err := cs.InsertMutationBatch(luisCtx, []MutationEntry{nexoObservationEntry(project, "obs-1", "v2")}); err != nil {
		t.Fatalf("luis edit: %v", err)
	}
	deleteEntry := MutationEntry{Project: project, Entity: "observation", EntityKey: "obs-1", Op: "delete", Payload: json.RawMessage(`{"sync_id":"obs-1"}`)}
	if _, err := cs.InsertMutationBatch(luisCtx, []MutationEntry{deleteEntry}); err != nil {
		t.Fatalf("luis delete: %v", err)
	}

	history := nexoHistory(t, cs, "obs-1")
	if len(history) != 3 {
		t.Fatalf("history rows = %d, want 3: %+v", len(history), history)
	}
	want := []struct{ op, actor, email string }{{"upsert", "ana", "ana@rp.pe"}, {"upsert", "luis", "luis@rp.pe"}, {"delete", "luis", "luis@rp.pe"}}
	for i, w := range want {
		if history[i].Op != w.op || history[i].Actor.String != w.actor || history[i].ActorEmail.String != w.email || !history[i].Verified {
			t.Fatalf("history[%d] = %+v, want %+v", i, history[i], w)
		}
	}

	// El paquete materializado por el camino de cambios también lleva autor.
	var chunkActors int
	if err := cs.db.QueryRow(`SELECT COUNT(*) FROM cloud_chunks WHERE project_name = $1 AND actor = 'luis' AND actor_verified AND actor_email = 'luis@rp.pe'`, project).Scan(&chunkActors); err != nil {
		t.Fatalf("count chunk actors: %v", err)
	}
	if chunkActors == 0 {
		t.Fatalf("expected materialized chunks stamped with luis")
	}
}

// CA-05: token compartido / sin autenticación → no identificado; lo declarado
// por la PC queda solo como referencia.
func TestNexoUnidentifiedActorKeepsClaimOnlyAsReference(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	project := "nexo-transversal"
	ctx := WithMutationActor(context.Background(), MutationActor{Name: "LEGACY_SYNC", PrincipalID: "legacy:sync", Verified: false, ClientClaim: "Administrador"})
	if _, err := cs.InsertMutationBatch(ctx, []MutationEntry{nexoSessionEntry(project), nexoObservationEntry(project, "obs-x", "x")}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	history := nexoHistory(t, cs, "obs-x")
	if len(history) != 1 {
		t.Fatalf("rows = %d", len(history))
	}
	h := history[0]
	if h.Actor.String != NexoActorUnidentified || h.Verified || h.PrincipalID.Valid || h.TokenID.Valid || h.ActorEmail.Valid || h.ClientClaim.String != "Administrador" {
		t.Fatalf("unidentified row = %+v", h)
	}
}

// CA-07 / RF-08: cambios sin actor (anteriores a NX-01) quedan con autor vacío.
func TestNexoRowsWithoutActorStayEmpty(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	project := "nexo-transversal"
	if _, err := cs.InsertMutationBatch(context.Background(), []MutationEntry{nexoSessionEntry(project), nexoObservationEntry(project, "obs-old", "old")}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	history := nexoHistory(t, cs, "obs-old")
	if len(history) != 1 || history[0].Actor.Valid || history[0].Verified || history[0].ActorEmail.Valid {
		t.Fatalf("legacy row should have empty actor: %+v", history)
	}
	// Una transacción con actor no deja variables de sesión que contaminen
	// a la siguiente sin actor (set_config local a la transacción).
	ana := nexoSeedUser(t, cs, "ana", "ana@rp.pe", "laptop-ana")
	cs.db.SetMaxOpenConns(1)
	if _, err := cs.InsertMutationBatch(WithMutationActor(context.Background(), nexoVerifiedActor("ana", ana, "laptop-ana", "")), []MutationEntry{nexoObservationEntry(project, "obs-new", "new")}); err != nil {
		t.Fatalf("batch with actor: %v", err)
	}
	if _, err := cs.InsertMutationBatch(context.Background(), []MutationEntry{nexoObservationEntry(project, "obs-after", "after")}); err != nil {
		t.Fatalf("batch after: %v", err)
	}
	after := nexoHistory(t, cs, "obs-after")
	if len(after) != 1 || after[0].Actor.Valid {
		t.Fatalf("actor leaked across transactions: %+v", after)
	}
}

func TestNexoTruncateClaim(t *testing.T) {
	long := strings.Repeat("ñ", 300)
	if got := NexoTruncateClaim("  " + long + "  "); len([]rune(got)) != nexoMaxClientClaimRunes {
		t.Fatalf("claim runes = %d, want %d", len([]rune(got)), nexoMaxClientClaimRunes)
	}
}
