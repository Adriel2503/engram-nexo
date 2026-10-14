package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

// El temporizador es externo: cancelar limpia la operación bloqueada, pero
// DeadlineExceeded o una salida por cancelación nunca cuentan como éxito.
func op13Run(t *testing.T, fn func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fn(ctx) }()
	select {
	case err := <-done:
		if err != nil || ctx.Err() != nil {
			t.Fatalf("operación falló: %v (contexto: %v)", err, ctx.Err())
		}
	case <-time.After(5 * time.Second):
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		t.Fatal("operación bloqueada: venció el temporizador externo")
	}
}

func TestNexoOP13PublishColdModeSingleConnection(t *testing.T) {
	f := setup(t)
	id := f.env.ID(f.tm.Ana)
	svc := service.New(f.env.DB, f.env.Roles)
	f.env.DB.SetMaxOpenConns(1) // después de preparar datos e identidades
	op13Run(t, func(ctx context.Context) error {
		res, err := svc.PublishBatch(authz.WithRequestCache(ctx), id, space, service.BatchRequest{
			Documents: []service.PublishDoc{{DocKey: "op13-publicacion", Category: "lineamiento", Content: "# Validación\n\nValidar los formularios antes de enviarlos."}},
		})
		if err != nil {
			return err
		}
		if len(res.Results) != 1 || res.Results[0].Status != domain.ResultCreated {
			return fmt.Errorf("publicación inesperada: %s", statuses(res))
		}
		return nil
	})
	if n := f.env.DB.Stats().InUse; n != 0 {
		t.Fatalf("conexiones retenidas: %d", n)
	}
}

func TestNexoOP13ApproveColdModeSingleConnection(t *testing.T) {
	f := setup(t)
	author, reviewer := f.env.ID(f.tm.Ana), f.env.ID(f.tm.Ana)
	proposal, err := f.env.KB.CreateProposal(f.env.Ctx(), author, space, service.ProposalInput{
		DocKey: "op13-propuesta", Title: "Validación", Category: "lineamiento", Content: "# Validación\n\nValidar los formularios antes de enviarlos.",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.New(f.env.DB, f.env.Roles)
	f.env.DB.SetMaxOpenConns(1)
	op13Run(t, func(ctx context.Context) error {
		res, err := svc.ApproveProposal(authz.WithRequestCache(ctx), reviewer, proposal.ID, service.ApproveInput{})
		if err != nil {
			return err
		}
		if res.Proposal.Status != "approved" || res.Result.Status != domain.ResultCreated {
			return fmt.Errorf("la propuesta no se publicó y aprobó")
		}
		return nil
	})
	if n := f.env.DB.Stats().InUse; n != 0 {
		t.Fatalf("conexiones retenidas: %d", n)
	}
}

func TestNexoOP13ConcurrentMixedPoolFour(t *testing.T) {
	fake := kbtest.NewFakeEmbedder()
	f := setup(t, service.WithEmbedder(fake, fake.FakeConfig()), service.WithR2(r2Options()))
	author, reader := f.env.ID(f.tm.Ana), f.env.ID(f.tm.Luis)
	content := "# Validación\n\nValidar los formularios antes de enviarlos."
	f.publish(t, f.tm.Ana, []service.PublishDoc{{DocKey: "op13-base", Category: "lineamiento", Content: content}})
	if _, _, err := f.env.KB.EnsureVectorIndex(f.env.Ctx()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.KB.DrainVectors(f.env.Ctx()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.env.KB.Store().ActiveVectorIndex(f.env.Ctx()); err != nil || !ok {
		t.Fatal("no se preparó un índice vectorial activo")
	}
	proposals := make([]string, 4)
	for i := range proposals {
		p, err := f.env.KB.CreateProposal(f.env.Ctx(), author, space, service.ProposalInput{DocKey: fmt.Sprintf("op13-approve-%d", i), Title: "Validación", Category: "lineamiento", Content: content})
		if err != nil {
			t.Fatal(err)
		}
		proposals[i] = p.ID
	}
	payload, err := chunkcodec.CanonicalizeForProject([]byte(`{"sessions":[{"id":"op13-session","project":"op13-project","directory":"/tmp","started_at":"2026-10-14T00:00:00Z"}],"observations":[],"prompts":[]}`), "op13-project")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	chunkID := hex.EncodeToString(sum[:])[:8]
	// Servicios nuevos: modo, soporte vectorial, índice activo y cachés de
	// autorización fríos. No hay llamadas a svc antes de abrir la barrera.
	roles := authz.NewService(f.env.DB, authz.NewPostgres(f.env.DB))
	svc := service.New(f.env.DB, roles, service.WithEmbedder(fake, fake.FakeConfig()), service.WithR2(r2Options()))
	f.env.DB.SetMaxOpenConns(4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const operations = 24
	ready, start := make(chan struct{}, operations), make(chan struct{})
	done := make(chan error, operations)
	for i := 0; i < operations; i++ {
		go func(i int) {
			ready <- struct{}{}
			<-start
			opCtx := authz.WithRequestCache(ctx)
			var err error
			switch i / 4 {
			case 0:
				var res service.BatchResult
				res, err = svc.PublishBatch(opCtx, author, space, service.BatchRequest{Documents: []service.PublishDoc{{DocKey: fmt.Sprintf("op13-publish-%d", i), Category: "lineamiento", Content: content}}})
				if err == nil && (len(res.Results) != 1 || res.Results[0].Status != domain.ResultCreated) {
					err = fmt.Errorf("publicación inesperada")
				}
			case 1:
				var res service.ApproveResult
				res, err = svc.ApproveProposal(opCtx, author, proposals[i%4], service.ApproveInput{})
				if err == nil && res.Proposal.Status != "approved" {
					err = fmt.Errorf("propuesta sin aprobar")
				}
			case 2:
				var res search.Result
				res, err = svc.Search(opCtx, reader, service.SearchRequest{Q: "validación formularios", Mode: "hybrid", Limit: 5})
				if err == nil && (res.NoResults || len(res.Hits) == 0 || res.Vector == nil || res.Vector.Status != search.VectorUsed) {
					err = fmt.Errorf("búsqueda sin resultados")
				}
			case 3:
				err = f.env.CS.WriteChunk(opCtx, "op13-project", chunkID, "op13", "", payload)
			case 4:
				hash := sha256.Sum256([]byte(fmt.Sprintf("identificador-de-prueba-op13-%d", i)))
				_, err = f.env.CS.CreatePrincipalTokenWithAudit(opCtx, cloudstore.CreatePrincipalTokenParams{
					PrincipalID: f.tm.Luis, TokenPrefix: "prueba", TokenHash: hex.EncodeToString(hash[:]), Name: fmt.Sprintf("op13-%d", i), CreatedByPrincipalID: f.tm.Ana,
				}, cloudstore.AuthAuditEvent{ActorPrincipalID: f.tm.Ana, ActorSource: "token", TargetPrincipalID: f.tm.Luis, Action: "token.created", Outcome: "success"})
			case 5:
				err = f.env.CS.InsertAuthAuditEvent(opCtx, cloudstore.AuthAuditEvent{ActorPrincipalID: f.tm.Ana, ActorSource: "token", Action: "op13.audit", Outcome: "success"})
			}
			if err != nil {
				err = fmt.Errorf("operación %d: %w", i, err)
			}
			done <- err
		}(i)
	}
	for i := 0; i < operations; i++ {
		<-ready
	}
	close(start)
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	var failures []error
	for i := 0; i < operations; i++ {
		select {
		case err := <-done:
			if err != nil {
				failures = append(failures, err)
			}
		case <-timer.C:
			cancel()
			// Recoger las operaciones restantes permite limpiar sin dejar goroutines.
			cleanup := time.NewTimer(3 * time.Second)
			defer cleanup.Stop()
			for j := i; j < operations; j++ {
				select {
				case <-done:
				case <-cleanup.C:
					t.Fatal("las operaciones no terminaron tras cancelar")
				}
			}
			t.Fatal("batería concurrente bloqueada: venció el temporizador externo")
		}
	}
	if ctx.Err() != nil || len(failures) > 0 {
		t.Fatalf("batería concurrente falló: %v", failures)
	}
	if n := f.env.DB.Stats().InUse; n != 0 {
		t.Fatalf("conexiones retenidas al finalizar: %d", n)
	}
	var chunks, tokens, audits, docs int
	for _, check := range []struct {
		q    string
		dest *int
		want int
	}{
		{"SELECT count(*) FROM cloud_chunks WHERE project_name='op13-project'", &chunks, 1},
		{"SELECT count(*) FROM cloud_principal_tokens WHERE name LIKE 'op13-%'", &tokens, 4},
		{"SELECT count(*) FROM cloud_auth_audit_log WHERE action='op13.audit'", &audits, 4},
		{"SELECT count(*) FROM nexo_kb_documents WHERE doc_key LIKE 'op13-publish-%' OR doc_key LIKE 'op13-approve-%'", &docs, 8},
	} {
		if err := f.env.DB.QueryRowContext(ctx, check.q).Scan(check.dest); err != nil {
			t.Fatal(err)
		}
		if *check.dest != check.want {
			t.Fatalf("efecto persistido incorrecto: %d, esperado %d", *check.dest, check.want)
		}
	}
}
