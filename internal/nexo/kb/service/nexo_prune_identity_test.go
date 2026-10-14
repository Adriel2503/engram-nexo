package service_test

// NX-07: la pertenencia en la poda y la paginación usan el doc_key original.
import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// nexoCredentialShapedDoc publica un documento y cambia su clave a una con formato de credencial
// (una fila histórica anterior a la validación).
func nexoCredentialShapedDoc(t *testing.T, f fixture) string {
	t.Helper()
	f.publish(t, f.tm.Ana, []service.PublishDoc{{DocKey: "normal", Path: "normal.md", Content: "# Prueba\nContenido de prueba suficiente para una sección.", Category: "estandar", Origin: gitOrigin("normal.md", "test")}})
	original := "nxa_" + strings.Repeat("a", 16)
	if _, err := f.env.DB.Exec("UPDATE nexo_kb_documents SET doc_key=$1 WHERE doc_key='normal'", original); err != nil {
		t.Fatal(err)
	}
	return original
}

func TestNexoPruneUsesOriginalDocumentKeyPG(t *testing.T) {
	f := setup(t)
	original := nexoCredentialShapedDoc(t, f)
	res := f.publish(t, f.tm.Ana, nil, func(r *service.BatchRequest) {
		r.DryRun = true
		r.Prune = &service.PruneSpec{OriginKind: "git", OriginRef: "realplaza/lineamientos", Keys: []string{original}, Total: 1, Confirm: true}
	})
	if res.PruneSkipped != "" || len(res.Pruned) != 0 {
		t.Error("la poda consideró ausente una clave original presente")
	}
}

func TestNexoListDocumentsCursorUsesOriginalKeyPG(t *testing.T) {
	f := setup(t)
	original := nexoCredentialShapedDoc(t, f)
	var spaceID string
	if err := f.env.DB.QueryRow("SELECT space_id::text FROM nexo_kb_documents WHERE doc_key=$1", original).Scan(&spaceID); err != nil {
		t.Fatal(err)
	}
	docs, err := store.ListDocuments(f.env.Ctx(), f.env.DB, spaceID, store.DocQuery{Limit: 10})
	if err != nil || len(docs) != 1 {
		t.Fatalf("listado inesperado: %d %v", len(docs), err)
	}
	if strings.Contains(docs[0].DocKey, original) {
		t.Error("la copia de salida conserva la clave con formato de credencial")
	}
	if docs[0].NexoDocKey() != original {
		t.Error("el cursor debe construirse con la clave original")
	}
}
