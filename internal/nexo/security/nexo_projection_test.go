package security

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type nexoTypedOrigin struct {
	Kind      string `json:"kind"`
	SourceURL string `json:"source_url"`
	Order     int64  `json:"order"`
}

func nexoJSONOf(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Caso de la revisión: settings={"cache":{"name":"texto reservado"}} se copiaba a los detalles
// y la lista global de nombres lo dejaba pasar bajo "name".
func TestNexoProjectOmitsUnknownObjectsWithoutScope(t *testing.T) {
	const reserved = "texto reservado del documento"
	meta := map[string]any{
		"doc_key": "guia/uno", "version": 3,
		"before":   map[string]any{"name": reserved, "categories": []any{"guia"}, "settings": map[string]any{"cache": map[string]any{"name": reserved}}},
		"after":    map[string]any{"name": reserved, "settings": map[string]any{"cache": map[string]any{"name": reserved, "ttl": 5, "activo": true}}},
		"settings": map[string]any{"cache": map[string]any{"name": reserved}},
		"extra":    map[string]any{"name": reserved, "source": reserved, "reason": reserved},
		"name":     reserved, "note": reserved,
	}
	hidden := nexoJSONOf(t, NexoProject(NexoStreamKB, meta, false))
	if strings.Contains(hidden, reserved) || strings.Contains(hidden, "settings") || strings.Contains(hidden, "ttl") || strings.Contains(hidden, "extra") {
		t.Fatalf("la proyección sin permiso filtra texto o claves desconocidas: %s", hidden)
	}
	for _, want := range []string{`"doc_key":"guia/uno"`, `"version":3`, `"payload_withheld":true`, `"categories":["guia"]`} {
		if !strings.Contains(hidden, want) {
			t.Fatalf("falta %s en %s", want, hidden)
		}
	}
	visible := nexoJSONOf(t, NexoProject(NexoStreamKB, meta, true))
	if !strings.Contains(visible, reserved) || !strings.Contains(visible, "settings") {
		t.Fatalf("con permiso se entrega todo: %s", visible)
	}
	// Un flujo sin esquema no entrega nada sin permiso.
	if got := NexoProject(NexoStream("desconocido"), meta, false); len(got) != 1 || got["payload_withheld"] != true {
		t.Fatalf("flujo sin esquema: %v", got)
	}
}

// Las credenciales no salen nunca, ni con permiso, ni en claves, ni en valores tipados (structs, fechas).
func TestNexoProjectNeverReturnsCredentials(t *testing.T) {
	code, _ := GenerateActivationCode()
	token := "egc_live_0123abcd_" + strings.Repeat("Zy9-", 11)
	meta := map[string]any{
		"user_agent": code, "username": "ana " + token, "reason": "ver postgres://u:clave@db/x",
		"origin":       nexoTypedOrigin{Kind: "git", SourceURL: "https://x/?code=" + code, Order: 9007199254740993},
		"key " + token: "x",
		"api_key":      "secreto", "code_hash": "abc", "password_hint": "p",
		"nested": []any{map[string]any{"a": code}},
		"at":     time.Date(2026, 10, 14, 1, 2, 3, 0, time.UTC),
	}
	for _, allowed := range []bool{false, true} {
		for _, stream := range []NexoStream{NexoStreamAudit, NexoStreamKB, NexoStreamRoles, NexoStreamAlert} {
			out := nexoJSONOf(t, NexoProject(stream, meta, allowed))
			for _, secret := range []string{code, token, "clave@db", "secreto", "code_hash", "api_key", "password_hint"} {
				if strings.Contains(out, secret) {
					t.Fatalf("stream %s allowed=%v: sale %q en %s", stream, allowed, secret, out)
				}
			}
		}
	}
	// El origen tipado se normaliza a JSON antes de limpiar y no pierde precisión numérica.
	visible := nexoJSONOf(t, NexoProject(NexoStreamKB, meta, true))
	if !strings.Contains(visible, `"order":9007199254740993`) || !strings.Contains(visible, "[credencial oculta]") {
		t.Fatalf("origen tipado: %s", visible)
	}
}

// Cada tipo declarado valida su valor; lo que no cumple se omite.
func TestNexoProjectFieldKinds(t *testing.T) {
	meta := map[string]any{
		"source": "bearer_token", "client_ip": "10.0.0.7", "route": "GET /nexo/admin/audit", "expires_at": "2026-10-14T01:02:03Z", "result_count": 4,
		"project": "texto libre con espacios", "scope": "Authorization: Basic Zm9v", "minute": "no es fecha", "ok": "no es número",
		"username": "  ana  ", "tables": []any{"cloud_auth_audit_log", "con espacio"},
		"filters": map[string]any{"action": "x", "q": "reservado", "otro": 1},
	}
	out := NexoProject(NexoStreamAudit, meta, false)
	raw := nexoJSONOf(t, out)
	for _, bad := range []string{"texto libre", "Basic", "no es fecha", "no es número", "reservado", "otro", "con espacio"} {
		if strings.Contains(raw, bad) {
			t.Fatalf("pasó %q: %s", bad, raw)
		}
	}
	for _, want := range []string{`"source":"bearer_token"`, `"client_ip":"10.0.0.7"`, `"route":"GET /nexo/admin/audit"`, `"username":"ana"`, `"result_count":4`, `"tables":["cloud_auth_audit_log"]`, `"action":"x"`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("falta %s en %s", want, raw)
		}
	}
	if NexoProject(NexoStreamAudit, nil, false) != nil {
		t.Fatal("nil debe seguir siendo nil")
	}
}

func TestNexoProjectDoesNotMutateInput(t *testing.T) {
	code, _ := GenerateActivationCode()
	in := map[string]any{"user_agent": code, "nested": map[string]any{"x": code}}
	_ = NexoProject(NexoStreamAudit, in, false)
	_ = NexoProject(NexoStreamAudit, in, true)
	if in["user_agent"] != code || in["nested"].(map[string]any)["x"] != code {
		t.Fatal("la proyección modificó la entrada")
	}
}

func TestNexoExportRowDeclaredColumnsOnly(t *testing.T) {
	code, _ := GenerateActivationCode()
	row := map[string]any{
		"id": 7, "occurred_at": "2026-10-14T01:02:03Z", "actor_source": "request", "action": "x", "outcome": "ok",
		"metadata":      map[string]any{"source": "request", "settings": map[string]any{"name": "reservado"}, "user_agent": code},
		"columna_nueva": "reservado", "summary": "reservado", "notes": "reservado",
	}
	raw := nexoJSONOf(t, NexoExportRow("cloud_auth_audit_log", row))
	if strings.Contains(raw, "reservado") || strings.Contains(raw, code) || strings.Contains(raw, "columna_nueva") || !strings.Contains(raw, `"payload_withheld":true`) || !strings.Contains(raw, `"action":"x"`) {
		t.Fatalf("exportación: %s", raw)
	}
	if got := NexoExportRow("tabla_sin_esquema", row); len(got) != 1 {
		t.Fatalf("tabla sin esquema: %v", got)
	}
}
