package stack

import (
	"bytes"
	"context"
	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestStackTourActions(t *testing.T) {
	s := state.State{Dir: t.TempDir(), Mode: "demo", ServerURL: "http://127.0.0.1:18490"}
	access := []render.Access{}
	tokens := map[string]string{}
	for _, name := range []string{"proponente", "publicador", "auditor"} {
		tok, _ := cloudauth.GenerateManagedToken("live")
		tokens[name] = tok.Raw
		access = append(access, render.Access{Username: name, Token: tok.Raw})
	}
	if e := render.AccessFile(render.Data{State: s, Access: access}); e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	a := New(strings.NewReader(""), &out, io.Discard, t.TempDir(), "test")
	seen := map[string]bool{}
	a.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		body := "{}"
		role := "publicador"
		if strings.HasSuffix(r.URL.Path, "/proposals") {
			role = "proponente"
			body = "{\"id\":\"123\"}"
		}
		if r.URL.Path == "/nexo/admin/audit" {
			role = "auditor"
			body = "{\"items\":[{}]}"
		}
		if r.Header.Get("Authorization") != "Bearer "+tokens[role] {
			t.Fatal("rol equivocado")
		}
		seen[role] = true
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	for _, action := range []string{"proponer", "aprobar", "auditoria"} {
		if e := a.tourAction(context.Background(), s, Options{Action: action, ProposalID: "123"}); e != nil {
			t.Fatal(e)
		}
	}
	if len(seen) != 3 {
		t.Fatal("faltó cambio de rol")
	}
	for _, tok := range tokens {
		if strings.Contains(out.String(), tok) {
			t.Fatal("se imprimió un token")
		}
	}
}
