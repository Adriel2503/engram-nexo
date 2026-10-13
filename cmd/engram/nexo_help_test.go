package main

// NX-10: `engram help` lista `engram nexo`.

import (
	"strings"
	"testing"
)

func TestNexoInHelpText(t *testing.T) {
	stdout, _ := captureOutput(t, func() { printUsage() })
	if !strings.Contains(stdout, "nexo <subcommand>") || !strings.Contains(stdout, "engram nexo help") {
		t.Fatalf("expected 'nexo <subcommand>' in help text, got: %q", stdout)
	}
}
