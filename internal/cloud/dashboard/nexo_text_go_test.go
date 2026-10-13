package dashboard

// Nexo IA · NX-10 (panel, OP-06) — los textos en inglés que salen de código Go
// de Engram (no de plantillas) tienen traducción: títulos y mensajes de las
// páginas de error, el "Never" de las fechas y los mensajes de http.Error.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func readGo(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func setBrand(t *testing.T, on bool) {
	t.Helper()
	was := NexoBrand()
	t.Cleanup(func() { nexoBrandOn.Store(was) })
	nexoBrandOn.Store(on)
}

// Cada cadena fija que el código Go de Engram pasa a una página (EmptyState,
// Layout, classifyStoreError, renderStoreError, helpers) está en un
// diccionario de Nexo.
func TestNexoGoTextsAreTranslated(t *testing.T) {
	files := []string{"dashboard.go", "helpers.go", "../cloudserver/dashboard_admin_users.go"}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`EmptyState\("([^"]+)", "([^"]+)"\)`),
		regexp.MustCompile(`EmptyState\("([^"]+)", [a-z]+\)`),
		regexp.MustCompile(`Layout\("([^"]+)"`),
		regexp.MustCompile(`dashboardAdminLayout\(r, "([^"]+)"`),
		regexp.MustCompile(`renderStoreError\(w, r, "[a-z]+", "([^"]+)"`),
		regexp.MustCompile(`return http\.Status\w+, "([^"]+)", "([^"]+)"`),
		regexp.MustCompile(`message := "([^"]+)"`),
		regexp.MustCompile(`heading = "([^"]+)"`),
		regexp.MustCompile(`return nx\("([^"]+)"\)`),
	}
	found := 0
	for _, f := range files {
		src := readGo(t, f)
		for _, re := range patterns {
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				for _, key := range m[1:] {
					found++
					if _, ok := nexoLookup(key); !ok {
						t.Errorf("%s: sin traducción para %q", f, key)
					}
				}
			}
		}
	}
	if found < 40 {
		t.Fatalf("solo se encontraron %d textos de Go: ¿cambió la forma de las llamadas?", found)
	}
	for k, v := range nexoTextGo {
		if strings.TrimSpace(v) == "" || strings.ContainsAny(v, "{}%") {
			t.Errorf("nexoTextGo[%q] = %q: vacío o con marcador", k, v)
		}
	}
	// "Never" de formatTimePtr y formatTimestampStr.
	if src := readGo(t, "helpers.go"); strings.Count(src, `return "Never"`) != 0 {
		t.Error(`helpers.go devuelve "Never" sin pasar por nx()`)
	}
}

func TestNexoGoTextsAppearOnlyWithBrand(t *testing.T) {
	setBrand(t, false)
	for _, s := range []string{"Session Not Found", "Never", "No content captured.", "Stats unavailable"} {
		if got := nx(s); got != s {
			t.Errorf("sin la marca nx(%q) = %q, debe quedar igual", s, got)
		}
	}
	setBrand(t, true)
	for en, es := range map[string]string{
		"Session Not Found":            "Sesión no encontrada",
		"Stats unavailable":            "Estadísticas no disponible",
		"Project sessions unavailable": "Sesiones del proyecto no disponible",
		"Enable this managed user before creating a managed token. User bob is currently disabled.": "Habilita a esta persona antes de crearle un token. La persona bob está deshabilitada.",
	} {
		if got := nx(en); got != es {
			t.Errorf("nx(%q) = %q, quería %q", en, got, es)
		}
	}
	if got := formatTimePtr(nil); got != "Nunca" {
		t.Errorf("formatTimePtr(nil) = %q", got)
	}
	if got := formatTimestampStr(""); got != "Nunca" {
		t.Errorf("formatTimestampStr(\"\") = %q", got)
	}
	setBrand(t, false)
	if got := formatTimePtr(nil); got != "Never" {
		t.Errorf("sin la marca formatTimePtr(nil) = %q, debe seguir en inglés", got)
	}
}

// Todo mensaje fijo de http.Error de los handlers de Engram tiene su texto en
// español; los que llevan "%v" se traducen por su prefijo.
func TestNexoErrorTextCoversEngramMessages(t *testing.T) {
	files := []string{"dashboard.go", "../cloudserver/dashboard_admin_users.go", "../cloudserver/dashboard_session.go"}
	literal := regexp.MustCompile(`http\.Error\(w, "([^"]+)", http\.Status`)
	sprintf := regexp.MustCompile(`http\.Error\(w, fmt\.Sprintf\("([^"%]*)(?:%[a-z])?([^"]*)"`)
	found := 0
	for _, f := range files {
		src := readGo(t, f)
		for _, m := range literal.FindAllStringSubmatch(src, -1) {
			found++
			msg := m[1]
			if strings.Contains(msg, "%") {
				continue
			}
			if es, ok := NexoErrorText(http400, msg); !ok || es == "" || es == msg {
				t.Errorf("%s: sin traducción para http.Error(%q)", f, msg)
			}
		}
		for _, m := range sprintf.FindAllStringSubmatch(src, -1) {
			found++
			prefix := m[1]
			if strings.HasSuffix(prefix, ": ") || strings.HasPrefix(m[2], " ") || m[2] == "" || strings.Contains(prefix, "payload too large") {
				sample := prefix + "ejemplo" + m[2]
				if strings.Contains(prefix, "payload too large") {
					sample = strings.Split(prefix, " (max")[0] + " (max 12 bytes)"
				}
				if es, ok := NexoErrorText(http400, sample); !ok || es == "" {
					t.Errorf("%s: sin traducción para http.Error(Sprintf(%q))", f, prefix)
				}
			}
		}
	}
	if found < 30 {
		t.Fatalf("solo se encontraron %d http.Error: ¿cambió la forma de las llamadas?", found)
	}
}

const (
	http400 = 400
	http500 = 500
)

func TestNexoErrorTextRules(t *testing.T) {
	cases := []struct {
		status int
		in     string
		want   string
	}{
		{http400, "forbidden", "No tienes permiso"},
		{http400, "login payload too large (max 4096 bytes)", "demasiado grande (máximo 4096 bytes)"},
		{http400, "create user: ERROR: duplicate key value violates unique constraint \"cloud_human_users_username_key\" (SQLSTATE 23505)", "Ya existe un registro"},
		{http400, "create user: algo raro de la base", "No se pudo crear el usuario"},
		{http500, "create user: boom", NexoInternalErrorText},
		{http500, "list users: pq: connection refused", NexoInternalErrorText},
		{http500, "record audit: boom", NexoInternalErrorText},
		{http400, "invalid_time_format: \"x\" is not RFC3339 or YYYY-MM-DD", "AAAA-MM-DD"},
		{403, "forbidden: platform.users.manage is required (role_insufficient)", "capacidad platform.users.manage"},
		{409, `managed user "7" is disabled; enable the user before creating a managed token`, "La persona 7 está deshabilitada"},
		{http400, "expires_in_days must be between 1 and 365 (0 = no expiration, service accounts only)", "entre 1 y 365"},
		{http500, "authorization unavailable: internal error", NexoInternalErrorText},
	}
	for _, c := range cases {
		got, ok := NexoErrorText(c.status, c.in)
		if !ok || !strings.Contains(got, c.want) {
			t.Errorf("NexoErrorText(%d, %q) = %q (%v), quería algo con %q", c.status, c.in, got, ok, c.want)
		}
		for _, leak := range []string{"SQLSTATE", "duplicate key", "pq:", "connection refused", "boom", "cloud_human_users"} {
			if strings.Contains(got, leak) {
				t.Errorf("NexoErrorText(%q) deja ver %q: %q", c.in, leak, got)
			}
		}
	}
	for _, in := range []string{"El formulario venció o no es válido.", "algo que no se conoce", ""} {
		if got, ok := NexoErrorText(http400, in); ok {
			t.Errorf("NexoErrorText(%q) = %q: no debía traducir lo que no conoce", in, got)
		}
	}
}
