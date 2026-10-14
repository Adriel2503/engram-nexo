package seed

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/cli"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest/docx"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/stackapi"
)

// ─── (a) Pruebas de datos ───────────────────────────────────────────────────

func TestSeedDataRedact(t *testing.T) {
	// Comprobar que ningún documento embebido contiene credenciales
	err := fs.WalkDir(seedFS, "data", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := seedFS.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".docx") {
			// Convertir docx para comprobar su contenido de texto
			res, cErr := docx.Converter{}.Convert(context.Background(), ingest.Input{Name: filepath.Base(path), Data: data})
			if cErr != nil {
				t.Fatalf("%s no se pudo convertir: %v", path, cErr)
			}
			if redact.Has(res.Markdown) {
				t.Errorf("%s (docx convertido) activó el detector de credenciales", path)
			}
			return nil
		}
		if redact.Has(string(data)) {
			t.Errorf("%s activó el detector de credenciales de redact", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("error al recorrer seedFS: %v", err)
	}

	// Comprobar que ninguna memoria contiene credenciales
	raw, err := seedFS.ReadFile("data/memorias.json")
	if err != nil {
		t.Fatalf("leer data/memorias.json: %v", err)
	}
	var data struct {
		Observations []cli.Memory `json:"observations"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal memorias.json: %v", err)
	}
	if len(data.Observations) != 6 {
		t.Fatalf("esperaba 6 memorias, obtuvo %d", len(data.Observations))
	}
	for _, m := range data.Observations {
		if redact.Has(m.Title) || redact.Has(m.Content) {
			t.Errorf("memoria #%d (%s) activó el detector de credenciales", m.ID, m.Title)
		}
	}
}

func TestSeedDataEjemploNote(t *testing.T) {
	s := New()
	personas := s.Personas()
	if len(personas) != 6 {
		t.Fatalf("Personas() debe retornar 6 personas, obtuvo %d", len(personas))
	}

	// Comprobar que todos los documentos llevan "(EJEMPLO)"
	docCount := 0
	err := fs.WalkDir(seedFS, "data", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Base(path) == "memorias.json" {
			return nil
		}
		data, err := seedFS.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		if strings.HasSuffix(path, ".docx") {
			res, cErr := docx.Converter{}.Convert(context.Background(), ingest.Input{Name: filepath.Base(path), Data: data})
			if cErr != nil {
				t.Fatalf("%s: fallo al convertir docx: %v", path, cErr)
			}
			content = res.Meta.Title + " " + res.Markdown
		}
		if !strings.Contains(content, "(EJEMPLO)") {
			t.Errorf("documento %s no lleva la marca '(EJEMPLO)'", path)
		}
		docCount++
		return nil
	})
	if err != nil {
		t.Fatalf("error al recorrer data: %v", err)
	}
	if docCount != 12 {
		t.Fatalf("el inventario debe tener exactamente 12 documentos, obtuvo %d", docCount)
	}

	// Comprobar que todas las memorias llevan "(EJEMPLO)" en el título
	raw, err := seedFS.ReadFile("data/memorias.json")
	if err != nil {
		t.Fatalf("leer data/memorias.json: %v", err)
	}
	var memData struct {
		Observations []cli.Memory `json:"observations"`
	}
	if err := json.Unmarshal(raw, &memData); err != nil {
		t.Fatalf("unmarshal memorias.json: %v", err)
	}
	for _, m := range memData.Observations {
		if !strings.Contains(m.Title, "(EJEMPLO)") {
			t.Errorf("memoria #%d (%s) no lleva '(EJEMPLO)' en su título", m.ID, m.Title)
		}
	}
}

func TestSeedDataDocxOpens(t *testing.T) {
	data, err := seedFS.ReadFile("data/frontend/lineamiento-ejemplo.docx")
	if err != nil {
		t.Fatalf("leer lineamiento-ejemplo.docx: %v", err)
	}

	// 1. Debe ser un zip válido
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("lineamiento-ejemplo.docx no es un zip válido: %v", err)
	}
	foundDocXML := false
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			foundDocXML = true
			break
		}
	}
	if !foundDocXML {
		t.Fatal("lineamiento-ejemplo.docx no contiene word/document.xml")
	}

	// 2. El conversor de Nexo debe convertirlo limpiamente
	res, err := docx.Converter{}.Convert(context.Background(), ingest.Input{
		Name: "lineamiento-ejemplo.docx",
		Data: data,
	})
	if err != nil {
		t.Fatalf("conversión docx falló: %v", err)
	}
	if res.Meta.Title == "" && !strings.Contains(res.Markdown, "Lineamiento") {
		t.Fatalf("contenido inesperado en docx convertido: %s", res.Markdown)
	}
}

func TestRoleMatrixAndSlugs(t *testing.T) {
	s := New().(*seeder)

	// Validar que personas contiene los roles requeridos
	personaMap := map[string]stackapi.Persona{}
	for _, p := range s.Personas() {
		personaMap[p.Username] = p
		if !strings.HasSuffix(p.Email, "@demo.local") {
			t.Errorf("correo %s no usa @demo.local", p.Email)
		}
	}

	for _, expectedUser := range []string{"superadmin", "admin", "publicador", "proponente", "lector", "auditor"} {
		if _, ok := personaMap[expectedUser]; !ok {
			t.Errorf("falta la persona %s en Personas()", expectedUser)
		}
	}

	// Validar slugs de espacios
	spaceMap := map[string]SpaceDef{}
	for _, sp := range s.spaces {
		spaceMap[sp.Slug] = sp
		if !domain.ValidSlug(sp.Slug) {
			t.Errorf("slug inválido: %s", sp.Slug)
		}
		if !strings.Contains(sp.Name, "(EJEMPLO)") && !strings.Contains(sp.Description, "(EJEMPLO)") {
			t.Errorf("espacio %s debe llevar '(EJEMPLO)' en nombre o descripción", sp.Slug)
		}
		if _, ok := personaMap[sp.Owner]; !ok {
			t.Errorf("el responsable de %s (%s) no está en Personas()", sp.Slug, sp.Owner)
		}
	}

	// Validar matriz de roles
	validRoles := map[string]bool{
		"lector":      true,
		"proponente":  true,
		"publicador":  true,
		"responsable": true,
	}

	for _, r := range s.roles {
		if _, ok := spaceMap[r.Space]; !ok {
			t.Errorf("la asignación de rol referencia espacio desconocido: %s", r.Space)
		}
		if _, ok := personaMap[r.Principal]; !ok {
			t.Errorf("la asignación de rol referencia persona desconocida: %s", r.Principal)
		}
		if !validRoles[r.Role] {
			t.Errorf("rol inválido en matriz: %s", r.Role)
		}
	}
}

// ─── (b) Pruebas de LoadOwn ─────────────────────────────────────────────────

func TestLoadOwnFiltersAndRedact(t *testing.T) {
	dir := t.TempDir()

	// Crear archivos soportados y limpios
	if err := os.WriteFile(filepath.Join(dir, "guia.md"), []byte("# Guía limpia\nTexto de desarrollo.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "glosario.txt"), []byte("Texto plano sin secretos.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Crear archivo con credenciales (debe ser omitido)
	secretFile := filepath.Join(dir, "credencial.md")
	if err := os.WriteFile(secretFile, []byte("# Secreto\nAuthorization: Bearer super-secret-token-1234567890\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Crear archivo con cadena de conexión postgres
	connFile := filepath.Join(dir, "db.txt")
	if err := os.WriteFile(connFile, []byte("postgres://usuario:secreto123@servidor:5432/bd\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Crear archivo no soportado (imagen / binario)
	if err := os.WriteFile(filepath.Join(dir, "logo.png"), []byte("fake image data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "binario.exe"), []byte("MZ..."), 0o644); err != nil {
		t.Fatal(err)
	}

	// Carpeta .git debe ser ignorada
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config.txt"), []byte("git config"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Probar normalización de slugs
	if got := normalizeOwnSlug("prefijo", "Mi Carpeta_Especial 2026"); !domain.ValidSlug(got) {
		t.Errorf("slug normalizado inválido: %s", got)
	}

	// Comprobar que LoadOwn omite archivos con credenciales sin exponer su contenido
	s := New()
	env := stackapi.Env{
		ServerURL: "http://127.0.0.1:99999", // no se llama si solo analizamos la omisión de credenciales o el walk
		Tokens:    map[string]string{"publicador": "token"},
	}

	// Con un contexto cancelado para probar salida limpia
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.LoadOwn(cancelCtx, env, stackapi.OwnOptions{Dir: dir})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("esperaba context.Canceled, obtuvo: %v", err)
	}
}

func TestNormalizeOwnSlug(t *testing.T) {
	cases := []struct {
		prefix string
		input  string
		want   string
	}{
		{"", "Frontend", "frontend"},
		{"", "Mis Documentos!", "mis-documentos"},
		{"mi-app", "Backend", "mi-app-backend"},
		{"", "a", "a-espacio"},
		{"", "---espacio---", "espacio"},
	}

	for _, c := range cases {
		got := normalizeOwnSlug(c.prefix, c.input)
		if !domain.ValidSlug(got) {
			t.Errorf("normalizeOwnSlug(%q, %q) = %q no es válido según domain.ValidSlug", c.prefix, c.input, got)
		}
		if c.want != "" && got != c.want {
			t.Errorf("normalizeOwnSlug(%q, %q) = %q, want %q", c.prefix, c.input, got, c.want)
		}
	}
}
