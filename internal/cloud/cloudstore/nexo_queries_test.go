package cloudstore

// Nexo IA · NX-04 / NX-03 — tests del origen en la auditoría y de las consultas
// de trazabilidad. Los de integración usan CLOUDSTORE_TEST_DSN.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// ─── NX-04: origen en la metadata de la auditoría ────────────────────────────

func TestNexoAuditMetadataEnrichment(t *testing.T) {
	orig := map[string]any{"source": "request"}
	if got := nexoAuditMetadata(context.Background(), orig); len(got) != 1 || got["source"] != "request" {
		t.Fatalf("sin origen la metadata no cambia: %v", got)
	}
	if got := nexoAuditMetadata(context.Background(), nil); got != nil {
		t.Fatalf("sin origen y sin metadata debe quedar nil: %v", got)
	}
	ctx := WithNexoRequestOrigin(context.Background(), NexoRequestOrigin{ClientIP: "10.0.0.15", UserAgent: "Mozilla/5.0"})
	got := nexoAuditMetadata(ctx, orig)
	if got["client_ip"] != "10.0.0.15" || got["user_agent"] != "Mozilla/5.0" || got["source"] != "request" {
		t.Fatalf("metadata enriquecida = %v", got)
	}
	if _, mutated := orig["client_ip"]; mutated {
		t.Fatalf("el mapa original no debe modificarse: %v", orig)
	}
	if got := nexoAuditMetadata(ctx, nil); got["client_ip"] != "10.0.0.15" {
		t.Fatalf("metadata nil con origen = %v", got)
	}
	preset := map[string]any{"client_ip": "fijado-por-el-evento"}
	if got := nexoAuditMetadata(ctx, preset); got["client_ip"] != "fijado-por-el-evento" {
		t.Fatalf("no debe pisar claves existentes: %v", got)
	}
	empty := WithNexoRequestOrigin(context.Background(), NexoRequestOrigin{})
	if got := nexoAuditMetadata(empty, orig); len(got) != 1 {
		t.Fatalf("origen vacío no agrega claves: %v", got)
	}
}

// RF-24 / CA-24 (almacenamiento): todo insert de auditoría con origen en el
// contexto guarda client_ip y user_agent; sin origen, la metadata queda igual.
func TestNexoInsertAuthAuditEventStoresOrigin(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ctx := WithNexoRequestOrigin(context.Background(), NexoRequestOrigin{ClientIP: "203.0.113.9", UserAgent: "curl/8.0"})
	if err := cs.InsertAuthAuditEvent(ctx, AuthAuditEvent{ActorSource: "unauthenticated", Action: "dashboard.login", Outcome: "denied", ReasonCode: "invalid_token", Metadata: map[string]any{"source": "unauthenticated"}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := cs.InsertAuthAuditEvent(context.Background(), AuthAuditEvent{ActorSource: "request", Action: "sync.auth", Outcome: "denied", Metadata: map[string]any{"source": "request"}}); err != nil {
		t.Fatalf("insert sin origen: %v", err)
	}
	var ip, ua, source string
	if err := cs.db.QueryRow(`SELECT metadata->>'client_ip', metadata->>'user_agent', metadata->>'source' FROM cloud_auth_audit_log WHERE action = 'dashboard.login'`).Scan(&ip, &ua, &source); err != nil {
		t.Fatal(err)
	}
	if ip != "203.0.113.9" || ua != "curl/8.0" || source != "unauthenticated" {
		t.Fatalf("metadata = ip %q ua %q source %q", ip, ua, source)
	}
	var raw string
	if err := cs.db.QueryRow(`SELECT metadata::text FROM cloud_auth_audit_log WHERE action = 'sync.auth'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "client_ip") {
		t.Fatalf("sin origen no debe agregarse client_ip: %s", raw)
	}
}

// ─── NX-03: consultas ────────────────────────────────────────────────────────

func nexoInsertAudit(t *testing.T, cs *CloudStore, event AuthAuditEvent) {
	t.Helper()
	if err := cs.InsertAuthAuditEvent(context.Background(), event); err != nil {
		t.Fatalf("insert audit: %v", err)
	}
}

// RF-31, RF-35, CA-31, CA-35.
func TestNexoListAuthAuditFiltersAndCursor(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	ana := nexoSeedUser(t, cs, "ana", "ana@rp.pe", "laptop-ana")
	origin := WithNexoRequestOrigin(ctx, NexoRequestOrigin{ClientIP: "10.0.0.15", UserAgent: "Go-http-client/1.1"})
	if err := cs.InsertAuthAuditEvent(origin, AuthAuditEvent{ActorSource: "request", Action: "sync.auth", Outcome: "denied", ReasonCode: "unknown_token", Metadata: map[string]any{"source": "request"}}); err != nil {
		t.Fatal(err)
	}
	nexoInsertAudit(t, cs, AuthAuditEvent{ActorPrincipalID: ana.PrincipalID, ActorSource: "managed_token", Action: "token.create", Outcome: "success", TargetPrincipalID: ana.PrincipalID})
	nexoInsertAudit(t, cs, AuthAuditEvent{ActorPrincipalID: ana.PrincipalID, ActorSource: "managed_token", Action: "nexo.presence.read", Outcome: "success"})
	nexoInsertAudit(t, cs, AuthAuditEvent{ActorPrincipalID: ana.PrincipalID, ActorSource: "managed_token", Action: "nexo.audit.read", Outcome: "success"})

	all, next, err := cs.ListAuthAudit(ctx, NexoAuditQuery{})
	if err != nil || len(all) != 4 || next != "" {
		t.Fatalf("all = %d next=%q err=%v", len(all), next, err)
	}
	if all[0].Action != "nexo.audit.read" || all[3].Action != "sync.auth" {
		t.Fatalf("orden debe ser del más reciente al más antiguo: %s … %s", all[0].Action, all[3].Action)
	}
	denied, _, err := cs.ListAuthAudit(ctx, NexoAuditQuery{Outcome: "denied", From: time.Now().Add(-7 * 24 * time.Hour)})
	if err != nil || len(denied) != 1 {
		t.Fatalf("denied = %+v err=%v", denied, err)
	}
	if d := denied[0]; d.ClientIP != "10.0.0.15" || d.UserAgent != "Go-http-client/1.1" || d.ReasonCode != "unknown_token" || d.OccurredAt.IsZero() {
		t.Fatalf("rechazo sin origen o motivo: %+v", d)
	}
	if got, _, _ := cs.ListAuthAudit(ctx, NexoAuditQuery{Action: "nexo.*"}); len(got) != 2 {
		t.Fatalf("prefijo nexo.* = %d", len(got))
	}
	if got, _, _ := cs.ListAuthAudit(ctx, NexoAuditQuery{Action: "token.create"}); len(got) != 1 || got[0].ActorUsername != "ana" || got[0].ActorEmail != "ana@rp.pe" || got[0].TargetUsername != "ana" {
		t.Fatalf("token.create = %+v", got)
	}
	for _, actor := range []string{"ana", "ANA@rp.pe", ana.PrincipalID} {
		if got, _, err := cs.ListAuthAudit(ctx, NexoAuditQuery{Actor: actor}); err != nil || len(got) != 3 {
			t.Fatalf("actor %q = %d err=%v", actor, len(got), err)
		}
	}
	if got, _, _ := cs.ListAuthAudit(ctx, NexoAuditQuery{Actor: "request"}); len(got) != 1 {
		t.Fatalf("actor_source request = %d", len(got))
	}
	if got, _, _ := cs.ListAuthAudit(ctx, NexoAuditQuery{To: time.Now().Add(-time.Hour)}); len(got) != 0 {
		t.Fatalf("to en el pasado = %d", len(got))
	}
	if _, _, err := cs.ListAuthAudit(ctx, NexoAuditQuery{Cursor: "abc"}); !errors.Is(err, ErrNexoInvalidCursor) {
		t.Fatalf("cursor inválido err = %v", err)
	}
	if got, _, _ := cs.ListAuthAudit(ctx, NexoAuditQuery{Action: "nexo_%"}); len(got) != 0 {
		t.Fatalf("los comodines LIKE deben escaparse: %d", len(got))
	}
}

// CA-35: 1.000 eventos → páginas de hasta 200 con cursor estable, sin
// duplicados ni huecos, aunque lleguen eventos nuevos entre páginas.
func TestNexoListAuthAuditPaginatesThousandEvents(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	if _, err := cs.db.Exec(`INSERT INTO cloud_auth_audit_log (actor_source, action, outcome) SELECT 'request', 'sync.auth', 'denied' FROM generate_series(1, 1000)`); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		items, next, err := cs.ListAuthAudit(ctx, NexoAuditQuery{Limit: 500, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) > NexoQueryMaxLimit {
			t.Fatalf("página con %d filas (máximo 200)", len(items))
		}
		for _, it := range items {
			if seen[it.ID] {
				t.Fatalf("id %s repetido", it.ID)
			}
			seen[it.ID] = true
		}
		pages++
		if pages == 1 {
			// Un evento nuevo no debe colarse ni desplazar las páginas siguientes.
			nexoInsertAudit(t, cs, AuthAuditEvent{ActorSource: "request", Action: "sync.auth", Outcome: "denied"})
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 1000 || pages != 5 {
		t.Fatalf("vistos=%d páginas=%d, want 1000 en 5", len(seen), pages)
	}
}

// RF-32, CA-32, RF-33, CA-33.
func TestNexoListActivityAndPresence(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	ana := nexoSeedUser(t, cs, "ana", "ana@rp.pe", "laptop-ana", "ci")
	luis := nexoSeedUser(t, cs, "luis", "luis@rp.pe", "laptop-luis")
	anaActor := nexoVerifiedActor("ana", ana, "laptop-ana", "ANA-PC")
	anaCI := nexoVerifiedActor("ana", ana, "ci", "runner")
	luisActor := nexoVerifiedActor("luis", luis, "laptop-luis", "LUIS-PC")
	record := func(actor MutationActor, op, project string, n int, ip string) {
		t.Helper()
		if err := cs.RecordActivity(ctx, ActivityEvent{Actor: actor, Operation: op, Project: project, ItemCount: n, ClientIP: ip, UserAgent: "Go-http-client/1.1", LogDetail: true}); err != nil {
			t.Fatal(err)
		}
	}
	record(anaActor, NexoOpPushMutations, "nexo-transversal", 3, "10.0.0.15")
	record(luisActor, NexoOpPushMutations, "nexo-transversal", 2, "10.0.0.20")
	record(luisActor, NexoOpPullMutations, "nexo-transversal,otro", 5, "10.0.0.20")
	record(luisActor, NexoOpPushChunk, "otro", 1, "10.0.0.20")
	record(anaCI, NexoOpPullChunk, "nexo-transversal", 4, "10.0.0.99")
	if err := cs.RecordActivity(ctx, ActivityEvent{Actor: MutationActor{Name: "x"}, Operation: NexoOpPushChunk, Project: "nexo-transversal", ItemCount: 1, LogDetail: true}); err != nil {
		t.Fatal(err)
	}

	luisNT, _, err := cs.ListActivity(ctx, NexoActivityQuery{Actor: "luis", Project: "nexo-transversal"})
	if err != nil || len(luisNT) != 2 {
		t.Fatalf("luis en nexo-transversal = %+v err=%v", luisNT, err)
	}
	if r := luisNT[1]; r.Operation != NexoOpPushMutations || r.ItemCount != 2 || r.ClientIP != "10.0.0.20" || r.TokenName != "laptop-luis" || r.TokenPrefix != "egc_test_laptop-luis" || r.ActorEmail != "luis@rp.pe" || r.ClientClaim != "LUIS-PC" || !r.ActorVerified {
		t.Fatalf("fila de luis = %+v", r)
	}
	if got, _, _ := cs.ListActivity(ctx, NexoActivityQuery{Token: "ci"}); len(got) != 1 || got[0].Operation != NexoOpPullChunk {
		t.Fatalf("token por nombre = %+v", got)
	}
	if got, _, _ := cs.ListActivity(ctx, NexoActivityQuery{Token: ana.TokenIDs["laptop-ana"]}); len(got) != 1 {
		t.Fatalf("token por id = %d", len(got))
	}
	if got, _, _ := cs.ListActivity(ctx, NexoActivityQuery{Operation: NexoOpPushChunk}); len(got) != 2 {
		t.Fatalf("operación push_chunk = %d", len(got))
	}
	if got, _, _ := cs.ListActivity(ctx, NexoActivityQuery{Actor: NexoActorUnidentified}); len(got) != 1 || got[0].Actor != NexoActorUnidentified {
		t.Fatalf("no identificado = %+v", got)
	}
	if got, _, _ := cs.ListActivity(ctx, NexoActivityQuery{Actor: ana.PrincipalID}); len(got) != 2 {
		t.Fatalf("ana por id = %d", len(got))
	}
	if got, _, _ := cs.ListActivity(ctx, NexoActivityQuery{From: time.Now().Add(time.Hour)}); len(got) != 0 {
		t.Fatalf("from futuro = %d", len(got))
	}
	// Paginación de 2 en 2 sobre 6 filas.
	var ids []string
	cursor := ""
	for {
		page, next, err := cs.ListActivity(ctx, NexoActivityQuery{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page {
			ids = append(ids, r.ID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(ids) != 6 {
		t.Fatalf("paginación devolvió %d filas: %v", len(ids), ids)
	}

	// Presencia (CA-33): ana tiene dos tokens, luis uno, más no-identificado.
	presence, next, err := cs.ListPresence(ctx, NexoPresenceQuery{})
	if err != nil || len(presence) != 4 || next != "" {
		t.Fatalf("presencia = %d next=%q err=%v", len(presence), next, err)
	}
	byKey := map[string]NexoPresenceRecord{}
	for _, p := range presence {
		byKey[p.PresenceKey] = p
	}
	l := byKey[luis.PrincipalID+"|"+luis.TokenIDs["laptop-luis"]]
	if l.Actor != "luis" || l.TokenName != "laptop-luis" || l.LastIP != "10.0.0.20" || l.PushCount != 2 || l.PullCount != 1 || l.LastClientClaim != "LUIS-PC" || l.FirstSeenAt.IsZero() || l.LastSeenAt.Before(l.FirstSeenAt) {
		t.Fatalf("presencia de luis = %+v", l)
	}
	if got, _, _ := cs.ListPresence(ctx, NexoPresenceQuery{Actor: "ana"}); len(got) != 2 {
		t.Fatalf("presencia de ana = %d", len(got))
	}
	if got, _, _ := cs.ListPresence(ctx, NexoPresenceQuery{Since: time.Now().Add(time.Hour)}); len(got) != 0 {
		t.Fatalf("since futuro = %d", len(got))
	}
	var keys []string
	cursor = ""
	for {
		page, next, err := cs.ListPresence(ctx, NexoPresenceQuery{Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page {
			keys = append(keys, p.PresenceKey)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(keys) != 4 || strings.Join(keys, ",") != strings.Join(func() []string {
		out := make([]string, 0, len(presence))
		for _, p := range presence {
			out = append(out, p.PresenceKey)
		}
		return out
	}(), ",") {
		t.Fatalf("paginación de presencia = %v", keys)
	}
	if _, _, err := cs.ListPresence(ctx, NexoPresenceQuery{Cursor: "%%%"}); !errors.Is(err, ErrNexoInvalidCursor) {
		t.Fatalf("cursor inválido err = %v", err)
	}
}

// RF-34, CA-34: versiones con autor y email; payload solo si se pide.
func TestNexoMutationHistoryQuery(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	ana := nexoSeedUser(t, cs, "ana", "ana@rp.pe", "laptop-ana")
	luis := nexoSeedUser(t, cs, "luis", "luis@rp.pe", "laptop-luis")
	project := "nexo-transversal"
	anaCtx := WithMutationActor(ctx, nexoVerifiedActor("ana", ana, "laptop-ana", "ANA-PC"))
	luisCtx := WithMutationActor(ctx, nexoVerifiedActor("luis", luis, "laptop-luis", "LUIS-PC"))
	if _, err := cs.InsertMutationBatch(anaCtx, []MutationEntry{nexoSessionEntry(project), nexoObservationEntry(project, "obs-h", "v1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.InsertMutationBatch(luisCtx, []MutationEntry{nexoObservationEntry(project, "obs-h", "v2")}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.InsertMutationBatch(luisCtx, []MutationEntry{{Project: project, Entity: "observation", EntityKey: "obs-h", Op: "delete", Payload: json.RawMessage(`{"sync_id":"obs-h"}`)}}); err != nil {
		t.Fatal(err)
	}

	versions, next, err := cs.MutationHistory(ctx, NexoHistoryQuery{EntityKey: "obs-h"})
	if err != nil || len(versions) != 3 || next != "" {
		t.Fatalf("history = %d next=%q err=%v", len(versions), next, err)
	}
	want := []string{"3/delete/luis/luis@rp.pe", "2/upsert/luis/luis@rp.pe", "1/upsert/ana/ana@rp.pe"}
	for i, v := range versions {
		if got := fmt.Sprintf("%d/%s/%s/%s", v.Version, v.Op, v.Actor, v.ActorEmail); got != want[i] {
			t.Fatalf("version[%d] = %s, want %s", i, got, want[i])
		}
		if v.Payload != nil {
			t.Fatalf("payload sin include_payload: %s", v.Payload)
		}
	}
	if versions[2].Title != "v1" || versions[2].TokenName != "laptop-ana" || versions[2].ClientClaim != "ANA-PC" || versions[2].CurrentEmail != "ana@rp.pe" {
		t.Fatalf("version 1 = %+v", versions[2])
	}
	withPayload, _, err := cs.MutationHistory(ctx, NexoHistoryQuery{EntityKey: "obs-h", IncludePayload: true, Limit: 1})
	if err != nil || len(withPayload) != 1 || !strings.Contains(string(withPayload[0].Payload), `"sync_id"`) {
		t.Fatalf("include_payload = %+v err=%v", withPayload, err)
	}
	page1, cur, _ := cs.MutationHistory(ctx, NexoHistoryQuery{EntityKey: "obs-h", Limit: 2})
	page2, cur2, _ := cs.MutationHistory(ctx, NexoHistoryQuery{EntityKey: "obs-h", Limit: 2, Cursor: cur})
	if len(page1) != 2 || cur == "" || len(page2) != 1 || cur2 != "" || page2[0].Version != 1 {
		t.Fatalf("paginación de historial: p1=%d cur=%q p2=%d cur2=%q", len(page1), cur, len(page2), cur2)
	}
	if got, _, _ := cs.MutationHistory(ctx, NexoHistoryQuery{EntityKey: "obs-h", Project: "otro"}); len(got) != 0 {
		t.Fatalf("filtro de proyecto = %d", len(got))
	}
	if _, _, err := cs.MutationHistory(ctx, NexoHistoryQuery{}); err == nil {
		t.Fatalf("entity key vacío debe fallar")
	}
}

// RNF-05: tiempos con 1 millón de filas sintéticas. Es lento (carga de datos),
// así que solo corre con NEXO_PERF_TEST=1.
func TestNexoQueriesPerformanceWithMillionRows(t *testing.T) {
	if os.Getenv("NEXO_PERF_TEST") != "1" {
		t.Skip("NEXO_PERF_TEST=1 para medir tiempos con 1 millón de filas")
	}
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	ana := nexoSeedUser(t, cs, "ana", "ana@rp.pe", "laptop-ana")
	luis := nexoSeedUser(t, cs, "luis", "luis@rp.pe", "laptop-luis")
	start := time.Now()
	if _, err := cs.db.ExecContext(ctx, `
		INSERT INTO nexo_activity_log (occurred_at, actor, actor_principal_id, actor_token_id, actor_verified, operation, project, item_count, client_ip, user_agent)
		SELECT NOW() - make_interval(secs => g), CASE WHEN g % 2 = 0 THEN 'ana' ELSE 'luis' END,
			CASE WHEN g % 2 = 0 THEN $1 ELSE $2 END, CASE WHEN g % 2 = 0 THEN $3 ELSE $4 END, TRUE,
			(ARRAY['push_chunk','push_mutations','pull_chunk','pull_mutations'])[1 + g % 4],
			CASE WHEN g % 10 = 0 THEN 'otro' ELSE 'nexo-transversal' END, 1 + g % 7, '10.0.0.' || (g % 250), 'Go-http-client/1.1'
		FROM generate_series(1, 1000000) AS g`,
		ana.PrincipalID, luis.PrincipalID, ana.TokenIDs["laptop-ana"], luis.TokenIDs["laptop-luis"]); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.db.ExecContext(ctx, `
		INSERT INTO cloud_auth_audit_log (occurred_at, actor_source, action, outcome, reason_code, metadata)
		SELECT NOW() - make_interval(secs => g), 'request', CASE WHEN g % 3 = 0 THEN 'sync.auth' ELSE 'dashboard.login' END,
			CASE WHEN g % 5 = 0 THEN 'denied' ELSE 'success' END, NULL, jsonb_build_object('client_ip', '10.0.0.1')
		FROM generate_series(1, 1000000) AS g`); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.db.ExecContext(ctx, `ANALYZE nexo_activity_log; ANALYZE cloud_auth_audit_log`); err != nil {
		t.Fatal(err)
	}
	t.Logf("carga de 2 millones de filas: %s", time.Since(start))

	weekAgo := time.Now().Add(-7 * 24 * time.Hour)
	cases := []struct {
		name string
		run  func(cursor string) (int, string, error)
	}{
		{"actividad sin filtros", func(c string) (int, string, error) {
			r, n, err := cs.ListActivity(ctx, NexoActivityQuery{Limit: 200, Cursor: c})
			return len(r), n, err
		}},
		{"actividad de luis en nexo-transversal", func(c string) (int, string, error) {
			r, n, err := cs.ListActivity(ctx, NexoActivityQuery{Actor: "luis", Project: "nexo-transversal", Limit: 200, Cursor: c})
			return len(r), n, err
		}},
		{"actividad por token y operación", func(c string) (int, string, error) {
			r, n, err := cs.ListActivity(ctx, NexoActivityQuery{Token: "laptop-ana", Operation: NexoOpPullChunk, Limit: 200, Cursor: c})
			return len(r), n, err
		}},
		{"auditoría rechazos última semana", func(c string) (int, string, error) {
			r, n, err := cs.ListAuthAudit(ctx, NexoAuditQuery{Outcome: "denied", From: weekAgo, Limit: 200, Cursor: c})
			return len(r), n, err
		}},
		{"auditoría por acción", func(c string) (int, string, error) {
			r, n, err := cs.ListAuthAudit(ctx, NexoAuditQuery{Action: "sync.auth", Limit: 200, Cursor: c})
			return len(r), n, err
		}},
	}
	for _, tc := range cases {
		begin := time.Now()
		n, next, err := tc.run("")
		first := time.Since(begin)
		if err != nil || n == 0 {
			t.Fatalf("%s: n=%d err=%v", tc.name, n, err)
		}
		begin = time.Now()
		if _, _, err := tc.run(next); err != nil {
			t.Fatalf("%s página 2: %v", tc.name, err)
		}
		second := time.Since(begin)
		t.Logf("%s: página 1 %s, página 2 %s (%d filas)", tc.name, first, second, n)
		if first > time.Second || second > time.Second {
			t.Fatalf("%s supera 1 s (RNF-05): %s / %s", tc.name, first, second)
		}
	}
}
