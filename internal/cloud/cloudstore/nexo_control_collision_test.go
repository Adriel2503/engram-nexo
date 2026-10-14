package cloudstore_test

// NX-07: los controles se filtran con el proyecto real, no con su etiqueta.
import (
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"strings"
	"testing"
)

func TestNexoControlScopeRedactionCollisionPG(t *testing.T) {
	env := kbtest.New(t)
	original := "nxa_" + strings.Repeat("a", 16)
	if _, err := env.DB.Exec("INSERT INTO cloud_project_controls(project,sync_enabled) VALUES($1,false),('credencial-oculta',true)", original); err != nil {
		t.Fatal(err)
	}
	scoped, err := env.CS.DashboardStoreForProjects([]string{"credencial-oculta"})
	if err != nil {
		t.Fatal(err)
	}
	controls, err := scoped.ListProjectSyncControls()
	if err != nil {
		t.Fatal(err)
	}
	if len(controls) != 1 || !controls[0].SyncEnabled {
		t.Error("el proyecto autorizado recibió controles del original redactado")
	}
}
