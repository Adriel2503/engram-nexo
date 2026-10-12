package cloudstore

// Nexo IA · NX-02 — tests de integración (Postgres, CLOUDSTORE_TEST_DSN).

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

type nexoPresenceRow struct {
	Actor       string
	Email       sql.NullString
	Verified    bool
	LastIP      sql.NullString
	LastUA      sql.NullString
	LastClaim   sql.NullString
	LastProject sql.NullString
	PushCount   int64
	PullCount   int64
	FirstSeen   string
	LastSeen    string
}

func nexoPresence(t *testing.T, cs *CloudStore, key string) nexoPresenceRow {
	t.Helper()
	var r nexoPresenceRow
	if err := cs.db.QueryRow(`SELECT actor, actor_email, actor_verified, last_ip, last_user_agent, last_client_claim, last_project,
		push_count, pull_count, first_seen_at::text, last_seen_at::text FROM nexo_actor_presence WHERE presence_key = $1`, key).
		Scan(&r.Actor, &r.Email, &r.Verified, &r.LastIP, &r.LastUA, &r.LastClaim, &r.LastProject, &r.PushCount, &r.PullCount, &r.FirstSeen, &r.LastSeen); err != nil {
		t.Fatalf("read presence %q: %v", key, err)
	}
	return r
}

func nexoActivityCount(t *testing.T, cs *CloudStore, where string, args ...any) int {
	t.Helper()
	var n int
	if err := cs.db.QueryRow(`SELECT COUNT(*) FROM nexo_activity_log WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatalf("count activity: %v", err)
	}
	return n
}

// CA-10, CA-11, CA-12, CA-13, RF-14.
func TestNexoRecordActivityDetailAndPresence(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	ana := nexoSeedUser(t, cs, "ana", "ana@rp.pe", "laptop-ana")
	actor := nexoVerifiedActor("ana", ana, "laptop-ana", "ANA-PC")

	if err := cs.RecordActivity(ctx, ActivityEvent{Actor: actor, Operation: NexoOpPushMutations, Project: "nexo-transversal", ItemCount: 3, ClientIP: "10.0.0.15", UserAgent: "Go-http-client/1.1", LogDetail: true}); err != nil {
		t.Fatalf("RecordActivity push: %v", err)
	}
	if err := cs.RecordActivity(ctx, ActivityEvent{Actor: actor, Operation: NexoOpPullMutations, Project: "nexo-transversal", ItemCount: 3, ClientIP: "10.0.0.15", LogDetail: true}); err != nil {
		t.Fatalf("RecordActivity pull: %v", err)
	}
	for i := 0; i < 100; i++ {
		if err := cs.RecordActivity(ctx, ActivityEvent{Actor: actor, Operation: NexoOpPullMutations, Project: "nexo-transversal", ClientIP: "10.0.0.16", LogDetail: false}); err != nil {
			t.Fatalf("RecordActivity empty pull: %v", err)
		}
	}

	var actorName, email, principalID, tokenID, op, project, ip, ua, claim string
	var count int
	var verified bool
	if err := cs.db.QueryRow(`SELECT actor, actor_email, actor_principal_id, actor_token_id, actor_verified, operation, project, item_count, client_ip, user_agent, client_claim
		FROM nexo_activity_log WHERE operation = 'push_mutations'`).Scan(&actorName, &email, &principalID, &tokenID, &verified, &op, &project, &count, &ip, &ua, &claim); err != nil {
		t.Fatalf("read push row: %v", err)
	}
	if actorName != "ana" || email != "ana@rp.pe" || principalID != ana.PrincipalID || tokenID != ana.TokenIDs["laptop-ana"] || !verified || project != "nexo-transversal" || count != 3 || ip != "10.0.0.15" || ua != "Go-http-client/1.1" || claim != "ANA-PC" {
		t.Fatalf("push row = %q %q %q %q %v %q %q %d %q %q %q", actorName, email, principalID, tokenID, verified, op, project, count, ip, ua, claim)
	}
	if n := nexoActivityCount(t, cs, `operation = 'pull_mutations'`); n != 1 {
		t.Fatalf("pull_mutations rows = %d, want 1 (empty pulls must not log detail)", n)
	}

	p := nexoPresence(t, cs, ana.PrincipalID+"|"+ana.TokenIDs["laptop-ana"])
	if p.Actor != "ana" || p.Email.String != "ana@rp.pe" || !p.Verified || p.PushCount != 1 || p.PullCount != 1 || p.LastIP.String != "10.0.0.16" || p.LastClaim.String != "ANA-PC" || p.LastUA.String != "Go-http-client/1.1" || p.LastProject.String != "nexo-transversal" {
		t.Fatalf("presence = %+v", p)
	}
	if p.LastSeen < p.FirstSeen {
		t.Fatalf("last_seen %s before first_seen %s", p.LastSeen, p.FirstSeen)
	}

	// RF-14: ninguna columna guarda valores de token.
	var leaked int
	if err := cs.db.QueryRow(`SELECT COUNT(*) FROM nexo_activity_log WHERE row_to_json(nexo_activity_log)::text LIKE '%egc_%'`).Scan(&leaked); err != nil {
		t.Fatalf("leak check: %v", err)
	}
	if leaked != 0 {
		t.Fatalf("activity rows contain token-like values")
	}
}

func TestNexoRecordActivityUnidentifiedPresence(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	for _, claim := range []string{"PC-1", "PC-2"} {
		if err := cs.RecordActivity(ctx, ActivityEvent{Actor: MutationActor{Name: "LEGACY_SYNC", PrincipalID: "legacy:sync", ClientClaim: claim}, Operation: NexoOpPushChunk, Project: "p", ItemCount: 1, LogDetail: true}); err != nil {
			t.Fatalf("RecordActivity: %v", err)
		}
	}
	p := nexoPresence(t, cs, NexoActorUnidentified)
	if p.Actor != NexoActorUnidentified || p.Verified || p.PushCount != 2 || p.LastClaim.String != "PC-2" || p.Email.Valid {
		t.Fatalf("unidentified presence = %+v", p)
	}
	if n := nexoActivityCount(t, cs, `actor = $1 AND actor_principal_id IS NULL AND NOT actor_verified`, NexoActorUnidentified); n != 2 {
		t.Fatalf("unidentified rows = %d", n)
	}
}

// CA-16 / RF-13 y RF-15.
func TestNexoActivityLogIsImmutableAndPurgeable(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := cs.RecordActivity(ctx, ActivityEvent{Actor: MutationActor{}, Operation: NexoOpPushChunk, Project: "p", LogDetail: true}); err != nil {
			t.Fatalf("RecordActivity: %v", err)
		}
	}
	for _, stmt := range []string{
		`UPDATE nexo_activity_log SET actor = 'otro'`,
		`DELETE FROM nexo_activity_log`,
		`TRUNCATE nexo_activity_log`,
	} {
		_, err := cs.db.ExecContext(ctx, stmt)
		if err == nil || !strings.Contains(err.Error(), "solo inserción") {
			t.Fatalf("%s: expected rejection, got %v", stmt, err)
		}
	}
	if n := nexoActivityCount(t, cs, `TRUE`); n != 3 {
		t.Fatalf("rows after rejected writes = %d, want 3", n)
	}

	// Depuración: solo borra lo más antiguo que el umbral. Para simular filas
	// viejas se desactiva el trigger en una transacción de prueba.
	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE nexo_activity_log DISABLE TRIGGER nexo_activity_log_no_update_delete`); err != nil {
		t.Fatalf("disable trigger: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nexo_activity_log SET occurred_at = NOW() - INTERVAL '200 days' WHERE id IN (SELECT id FROM nexo_activity_log ORDER BY id LIMIT 2)`); err != nil {
		t.Fatalf("age rows: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE nexo_activity_log ENABLE TRIGGER nexo_activity_log_no_update_delete`); err != nil {
		t.Fatalf("enable trigger: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if deleted, err := cs.PurgeActivityOlderThan(ctx, 0); err != nil || deleted != 0 {
		t.Fatalf("purge(0) = %d, %v; want no-op", deleted, err)
	}
	deleted, err := cs.PurgeActivityOlderThan(ctx, 180)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("purged = %d, want 2", deleted)
	}
	if n := nexoActivityCount(t, cs, `TRUE`); n != 1 {
		t.Fatalf("rows after purge = %d, want 1", n)
	}
	// La presencia se conserva siempre.
	if p := nexoPresence(t, cs, NexoActorUnidentified); p.PushCount != 3 {
		t.Fatalf("presence after purge = %+v", p)
	}
	// Fuera de la depuración, DELETE sigue rechazado.
	if _, err := cs.db.ExecContext(ctx, `DELETE FROM nexo_activity_log`); err == nil {
		t.Fatalf("DELETE must stay rejected after purge")
	}
}
