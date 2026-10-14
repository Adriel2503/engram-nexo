package nexocli

// Ayuda de `engram nexo` y de `engram nexo kb` (sin servidor).

import (
	"bytes"
	"strings"
	"testing"
)

func TestNexoHelpListsCommands(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"help"}, t.TempDir(), &out, &errb); code != 0 {
		t.Fatalf("help: %d %s", code, errb.String())
	}
	for _, want := range []string{"start", "stack", "tour", "connect", "disconnect", "setup", "reindex", "import-transversal", "audit", "--token-stdin"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("engram nexo help sin %q", want)
		}
	}
	out.Reset()
	if code := Run([]string{"kb", "help"}, t.TempDir(), &out, &errb); code != 0 {
		t.Fatalf("kb help: %d", code)
	}
	for _, want := range []string{"reindex", "NEXO_KB_LOCAL_DB", "$NEXO_HOME/kb.db", "si el servidor no responde"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("engram nexo kb help sin %q", want)
		}
	}
}
