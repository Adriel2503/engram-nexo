package security

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNexoAuditMetadataDoesNotRevealContent(t *testing.T) {
	code, _ := GenerateActivationCode()
	input := map[string]any{"title": "contenido reservado", "payload": map[string]any{"title": "contenido reservado"}, "items": []any{map[string]any{"doc_key": "doc", "title": "contenido reservado"}}, "extra": "contenido reservado", "code": code, "scope": "read"}
	hidden, _ := json.Marshal(NexoAuditMetadata(input, false))
	if strings.Contains(string(hidden), "contenido reservado") || strings.Contains(string(hidden), code) || !strings.Contains(string(hidden), "payload_withheld") {
		t.Fatal("la proyección filtra contenido")
	}
	visible, _ := json.Marshal(NexoAuditMetadata(input, true))
	if !strings.Contains(string(visible), "contenido reservado") || strings.Contains(string(visible), code) {
		t.Fatal("la proyección autorizada filtra credenciales")
	}
}

func TestNexoAuditRedactsCredentialsAtWriteAndLog(t *testing.T) {
	code, _ := GenerateActivationCode()
	input := map[string]any{"user_agent": code, "filters": map[string]any{"actor": code}, "at": time.Now()}
	out := NexoRedactAuditMetadata(input)
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), code) || input["user_agent"] != code {
		t.Fatal("credencial filtrada o entrada modificada")
	}
	if strings.Contains(NexoRedactAuditText("error "+code), code) {
		t.Fatal("código en log")
	}
}

func TestNexoCheckTokenName(t *testing.T) {
	code, _ := GenerateActivationCode()
	for _, bad := range []string{code, "equipo " + code, "egc_live_0123abcd_" + strings.Repeat("Zz9-", 11), "Bearer abcdefgh", "Authorization: Basic Zm9v",
		strings.Repeat("n", NexoMaxTokenNameRunes+1), "con\nsalto", "postgres://u:p@h/db"} {
		if _, err := NexoCheckTokenName(bad); err == nil || strings.Contains(err.Error(), code) {
			t.Errorf("debía rechazarse sin repetir el valor: %.20q", bad)
		}
	}
	for _, ok := range []string{"", "laptop-ana", "  agente-revisor  ", "ci-github", "nombre con authorization policy", strings.Repeat("ñ", NexoMaxTokenNameRunes)} {
		if got, err := NexoCheckTokenName(ok); err != nil || got != strings.TrimSpace(ok) {
			t.Errorf("%.20q: %q %v", ok, got, err)
		}
	}
}

func TestNexoRedactExternalRedactsBeforeTruncating(t *testing.T) {
	code, _ := GenerateActivationCode()
	got := NexoRedactExternal("ua/1.0 "+code, 30)
	if strings.Contains(got, "nxa_") || len([]rune(got)) > 30 || !strings.HasPrefix(got, "ua/1.0 ") {
		t.Fatalf("redacción previa al recorte: %q", got)
	}
}
