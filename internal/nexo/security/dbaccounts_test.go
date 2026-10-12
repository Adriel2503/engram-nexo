package security

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
)

// RS-02: la lista incluye las tablas de evidencia de los requisitos.
func TestEvidenceTablesCoverRequirements(t *testing.T) {
	have := map[string]bool{}
	for _, table := range EvidenceTables {
		if have[table] {
			t.Fatalf("tabla repetida: %s", table)
		}
		have[table] = true
	}
	for _, table := range []string{
		"nexo_activity_log", "nexo_authz_events", "cloud_auth_audit_log", "cloud_mutations",
		"nexo_kb_events", "nexo_kb_versions",
	} {
		if !have[table] {
			t.Fatalf("falta la tabla de evidencia %s (RS-02)", table)
		}
	}
	// Tablas que Engram o Nexo actualizan en ejecución nunca son evidencia.
	for _, table := range []string{"nexo_actor_presence", "cloud_principals", "cloud_principal_tokens", "cloud_project_grants", "cloud_project_controls", "nexo_role_bindings", "nexo_scopes"} {
		if have[table] {
			t.Fatalf("%s se actualiza en ejecución; no puede ser de solo inserción", table)
		}
	}
}

func TestAppAccountProblems(t *testing.T) {
	if p := (AppAccountReport{Role: "nexo_app", Schema: "public", TempTables: true}).Problems(); len(p) != 0 {
		t.Fatalf("cuenta apta con problemas: %v", p)
	}
	r := AppAccountReport{Role: "x", Schema: "public", Superuser: true, OwnsObjects: []string{"a", "b", "c", "d", "e", "f"}, CreateRole: true, CreateDB: true, SchemaCreate: true, DatabaseCreate: true}
	p := strings.Join(r.Problems(), "; ")
	for _, want := range []string{"superusuario", "dueña de tablas (a, b, c, d, e, ...)", "CREATEROLE", "CREATEDB", "crear objetos", "crear esquemas"} {
		if !strings.Contains(p, want) {
			t.Fatalf("falta %q en %q", want, p)
		}
	}
	if w := (AppAccountReport{TempTables: true, BypassRLS: true}).Warnings(); len(w) != 2 {
		t.Fatalf("advertencias = %v", w)
	}
}

func TestRetentionEventActionMatchesAuthz(t *testing.T) {
	if ActionRetentionPurged != authz.ActionRetentionPurged {
		t.Fatalf("acción de evento distinta: %s vs %s", ActionRetentionPurged, authz.ActionRetentionPurged)
	}
	if RetentionMinimumDays != 30 {
		t.Fatalf("mínimo de retención = %d, RS-03 exige 30", RetentionMinimumDays)
	}
}
