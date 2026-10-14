package redact

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type tipoOrigen struct {
	Repo      string    `json:"repo"`
	SourceURL string    `json:"source_url"`
	Order     int64     `json:"order"`
	At        time.Time `json:"at"`
	Lista     []string  `json:"lista"`
}

type nivel string

// Punto 5: redactValue devolvía intactos los structs (y los tipos con nombre), así que un metadato tipado
// (por ejemplo domain.Origin en los detalles de un evento) eludía la redacción.
func TestMetadataNormalizesTypedValuesBeforeRedacting(t *testing.T) {
	code := "nxa_" + strings.Repeat("Ab1-", 11)
	token := "egc_live_0123abcd_" + strings.Repeat("Zz9-", 11)
	meta := map[string]any{
		"origin": tipoOrigen{Repo: "r " + token, SourceURL: "https://x/?code=" + code, Order: 9007199254740993, Lista: []string{code}},
		"nivel":  nivel("Bearer " + token),
		"mapa":   map[string]int{"k " + code: 7},
		"punt":   &tipoOrigen{Repo: code},
		"lista":  []tipoOrigen{{Repo: token}},
		"nil":    nil, "n": 3, "f": 2.5, "b": true,
	}
	out := Metadata(meta)
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), code) || strings.Contains(string(raw), token) {
		t.Fatalf("un valor tipado eludió la redacción: %s", raw)
	}
	for _, want := range []string{`"order":9007199254740993`, `"n":3`, `"f":2.5`, `"b":true`, `"nil":null`, CredentialMark} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("falta %s en %s", want, raw)
		}
	}
	if meta["origin"].(tipoOrigen).Repo != "r "+token {
		t.Fatal("la entrada se modificó")
	}
}

func TestHasDetectsConnectionStringsAndHasAnyWalksValues(t *testing.T) {
	for _, in := range []string{"postgres://u:p@h/db", "usa postgresql://u@h/db?sslmode=disable", "host=h password=secreto"} {
		if !Has(in) {
			t.Errorf("Has debía reconocer %q", in)
		}
	}
	if Has("el campo password es obligatorio") || Has("postgres es una base de datos") {
		t.Error("Has marcó texto legítimo")
	}
	code := "nxa_" + strings.Repeat("Ab1-", 11)
	if !HasAny(map[string]any{"a": []any{map[string]any{"b": "ok", "c": code}}}) || !HasAny(map[string]any{"k " + code: 1}) || !HasAny(tipoOrigen{Repo: code}) || !HasAny([]string{"x", code}) {
		t.Error("HasAny debía encontrar la credencial anidada, en claves o en structs")
	}
	if HasAny(map[string]any{"a": []any{"ok", 3, nil, true}}) || HasAny(nil) || HasAny(5) {
		t.Error("HasAny marcó valores sin credencial")
	}
}

type fila struct {
	Texto   string
	Lista   []string
	Mapa    map[string]any
	Crudo   json.RawMessage
	Ptr     *string
	Interna struct{ Otro string }
	Hora    time.Time
	Numero  int
	oculto  string
}

func TestStructRedactsEveryTextField(t *testing.T) {
	code := "nxa_" + strings.Repeat("Ab1-", 11)
	token := "egc_live_0123abcd_" + strings.Repeat("Zz9-", 11)
	p := "p " + token
	f := fila{Texto: "t " + code, Lista: []string{"a", code}, Mapa: map[string]any{"k " + token: []any{code, map[string]any{"x": "Bearer " + token}}},
		Crudo: json.RawMessage(`{"a":"` + code + `","b":{"` + token + `":[1,"x ` + code + `"]},"n":9007199254740993}`), Ptr: &p, Hora: time.Unix(1, 0), Numero: 4, oculto: code}
	f.Interna.Otro = "o " + token
	Struct(&f)
	raw, _ := json.Marshal(f)
	if strings.Contains(string(raw), code) || strings.Contains(string(raw), token) || !strings.Contains(string(raw), `"n":9007199254740993`) || f.Numero != 4 || !f.Hora.Equal(time.Unix(1, 0)) {
		t.Fatalf("Struct dejó pasar texto o alteró datos: %s", raw)
	}
	if f.oculto != code {
		t.Fatal("un campo no exportado no se toca")
	}
	items := []fila{{Texto: code}, {Texto: "ok"}}
	Struct(&items)
	if strings.Contains(items[0].Texto, code) || items[1].Texto != "ok" {
		t.Fatalf("lista de estructuras: %+v", items)
	}
	Struct(nil)
	Struct(fila{}) // sin puntero: no hace nada
	if got := JSON([]byte("no es json")); got != nil {
		t.Fatalf("JSON inválido debe devolver nil: %s", got)
	}
}
