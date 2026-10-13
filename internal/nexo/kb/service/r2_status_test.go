package service

import (
	"strings"
	"testing"
)

func TestCleanProviderMessageNeverKeepsKey(t *testing.T) {
	const key = "AIzaSyFAKE-canary-0123456789abcdefghij"
	for name, in := range map[string]string{
		"valor":     "request failed for " + key + " (400)",
		"parametro": "GET https://x/v1beta/models/m:embedContent?key=zzzzzzzzzzzzzzzzzz&alt=json: 403",
		"bearer":    "Authorization: Bearer abcdefgh12345678ijkl rejected",
		"forma":     "token sk-abcdefghijklmnopqrstuvwx is invalid",
		"managed":   "egc_live_0123abcd_" + strings.Repeat("Ab-9", 8),
	} {
		got := cleanProviderMessage(in, key)
		for _, bad := range []string{key, "zzzzzzzzzzzzzzzzzz", "abcdefgh12345678ijkl", "sk-abcdefghijklmnopqrstuvwx", "0123abcd_Ab"} {
			if strings.Contains(got, bad) {
				t.Errorf("%s: el mensaje conserva %q: %q", name, bad, got)
			}
		}
	}
	long := cleanProviderMessage(strings.Repeat("palabra ", 200), "")
	if n := len([]rune(long)); n > statusErrorRunes+3 {
		t.Errorf("mensaje largo sin recortar: %d", n)
	}
	if got := cleanProviderMessage("línea 1\n\n   línea 2", ""); got != "línea 1 línea 2" {
		t.Errorf("espacios: %q", got)
	}
}
