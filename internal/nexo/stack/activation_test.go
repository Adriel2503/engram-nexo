package stack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/secrets"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
)

func TestStackRealActivationClient(t *testing.T) {
	admin, _ := cloudauth.GenerateManagedToken("live")
	person, _ := cloudauth.GenerateManagedToken("live")
	part, _ := secrets.Hex(32)
	code := "nxa_" + part
	canje := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/nexo/authz/v1/activate" {
			if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" && auth != "Bearer" {
				t.Error("canje autenticado como emisor")
			}
			var b struct{ Code string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b.Code != code {
				t.Error("código equivocado")
			}
			canje = true
			_ = json.NewEncoder(w).Encode(map[string]string{"raw_token": person.Raw})
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+admin.Raw {
			t.Error("emisión sin superadmin")
		}
		if strings.HasSuffix(r.URL.Path, "/tokens") {
			_ = json.NewEncoder(w).Encode(map[string]string{"activation_code": code})
		} else {
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()
	s := state.State{Dir: t.TempDir(), Mode: "demo", ServerURL: srv.URL}
	if e := render.AccessFile(render.Data{State: s, Access: []render.Access{{Username: "superadmin", Role: "superadmin", Token: admin.Raw}}}); e != nil {
		t.Fatal(e)
	}
	if e := ensurePeople(context.Background(), s, []Person{{Username: "lector", Email: "lector@demo.local"}}, client.Activate, api(s, admin.Raw)); e != nil {
		t.Fatal(e)
	}
	a, e := render.ReadAccess(s.Dir)
	if e != nil || len(a) != 2 || !canje || a[1].Token != person.Raw {
		t.Fatal("no se guardó el token canjeado")
	}
}
func TestStackRerankKeyValidation(t *testing.T) {
	key, _ := secrets.Hex(32)
	t.Setenv("NEXO_KB_RERANK_API_KEY", key)
	a := New(strings.NewReader(""), io.Discard, io.Discard, t.TempDir(), "test")
	a.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Fatal("falta clave")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{\"results\":[{\"index\":0,\"relevance_score\":1},{\"index\":1,\"relevance_score\":0.5}]}"))}, nil
	})}
	s := state.State{}
	m := map[string]string{}
	o := SearchOptions{Embedder: "off", Reranker: "cohere", R1: "all"}
	if e := a.searchConfig(context.Background(), &s, &o, m); e != nil {
		t.Fatal(e)
	}
	if m["NEXO_KB_RERANK_API_KEY"] != key || m["NEXO_KB_RERANK_DEFAULT"] != "on" {
		t.Fatal("configuración")
	}
}
