package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
)

func TestNexoSearchRequestLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  map[string]any
		limit string
	}{
		{"default", map[string]any{"query": "reintentos"}, "10"},
		{"explicit", map[string]any{"query": "reintentos", "limit": 3}, "3"},
		{"maximum", map[string]any{"query": "reintentos", "limit": 99}, "20"},
		{"minimum", map[string]any{"query": "reintentos", "limit": 0}, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/nexo/kb/v1/sync" {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(client.SyncPage{})
					return
				}
				if r.URL.Path != "/nexo/kb/v1/search" {
					http.NotFound(w, r)
					return
				}
				limits <- r.URL.Query().Get("limit")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"results":[],"no_results":true}`))
			}))
			defer srv.Close()
			local, err := client.OpenLocal(filepath.Join(t.TempDir(), "kb.db"))
			if err != nil {
				t.Fatal(err)
			}
			kb := client.NewKB(client.NewAPI(client.Settings{Server: srv.URL}, time.Second), local)
			t.Cleanup(func() { _ = kb.Close() })
			if _, err := kb.SyncNow(context.Background()); err != nil {
				t.Fatal(err)
			}
			c := mcpFor(t, kb, map[string]bool{"nexo_search": true})
			if _, raw, isErr := call(t, c, "nexo_search", tc.args); isErr {
				t.Fatalf("search: %s", raw)
			}
			select {
			case got := <-limits:
				if got != tc.limit {
					t.Fatalf("HTTP search limit = %s, want %s", got, tc.limit)
				}
			default:
				t.Fatal("search did not reach the server")
			}
		})
	}
}

func TestNexoSearchDefaultLimitOffline(t *testing.T) {
	l, err := client.OpenLocal(filepath.Join(t.TempDir(), "kb.db"))
	if err != nil {
		t.Fatal(err)
	}
	kb := client.NewKB(nil, l)
	t.Cleanup(func() { _ = kb.Close() })
	from := time.Now().UTC().Add(-time.Hour)
	page := client.SyncPage{Seq: 12, VisibleSpaces: []client.SyncSpace{{ID: "backend", Slug: "backend", Name: "Backend", Role: "lector"}}}
	for i := 1; i <= 12; i++ {
		key := fmt.Sprintf("guia-%02d", i)
		page.Changes = append(page.Changes, client.SyncChange{Seq: int64(i), Kind: "upsert", Space: "backend",
			Document: &client.SyncDocument{ID: key, Space: "backend", DocKey: key, Title: "Reintentos", Category: "lineamiento",
				Status: "active", CurrentVersion: 1, ValidFrom: from, Sections: []client.SyncSection{{Version: 1, Anchor: "reintentos",
					HeadingPath: []string{"Reintentos"}, Body: "Reintentos ante errores temporales.", ValidFrom: from}}}})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer srv.Close()
	api := client.NewAPI(client.Settings{Server: srv.URL}, time.Second)
	if _, err := client.NewSyncer(api, l).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	// No limit passed to KB.Search: the default must also apply without MCP or a server.
	res, err := kb.Search(context.Background(), client.SearchParams{Query: "reintentos", LocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != client.SourceLocal || len(res.Results) != 10 {
		t.Fatalf("offline search: source=%s, results=%d; want local copy with 10 results", res.Source, len(res.Results))
	}
	c := mcpFor(t, kb, map[string]bool{"nexo_search": true})
	m, raw, isErr := call(t, c, "nexo_search", map[string]any{"query": "reintentos"})
	if isErr || len(results(m)) != 10 {
		t.Fatalf("offline MCP search: %s", raw)
	}
}
