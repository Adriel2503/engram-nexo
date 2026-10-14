package wizard

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/ui"
)

func TestStackWizard(t *testing.T) {
	for _, choice := range []string{"1", "2", "3"} {
		var out bytes.Buffer
		called := ""
		action := func(context.Context, *ui.UI) error { called = "1"; return nil }
		if e := Start(context.Background(), ui.New(strings.NewReader(choice+"\n"), &out, true), Actions{action, func(context.Context, *ui.UI) error { called = "2"; return nil }, func(context.Context, *ui.UI) error { called = "3"; return nil }}); e != nil || called != choice {
			t.Fatal("flujo guiado")
		}
	}
}
