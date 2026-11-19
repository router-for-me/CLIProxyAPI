package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// The management round trip (GET, edit another channel, PUT) must keep the
// empty Antigravity list that opts out of the default stop rule.
func TestSanitizedOAuthRequestScopedErrorsKeepsAntigravityOptOut(t *testing.T) {
	got := sanitizedOAuthRequestScopedErrors(map[string][]config.RequestScopedErrorRule{
		"Antigravity": {},
		"claude":      {},
		"gemini":      {{Status: 400, Match: []string{"x"}, Action: "stop"}},
	})
	if rules, ok := got["antigravity"]; !ok || len(rules) != 0 {
		t.Fatalf("antigravity = %#v (present %v), want kept and empty", rules, ok)
	}
	if _, ok := got["claude"]; ok {
		t.Fatal("empty list of another channel was kept")
	}
	if len(got["gemini"]) != 1 {
		t.Fatalf("gemini rules = %#v", got["gemini"])
	}
}
