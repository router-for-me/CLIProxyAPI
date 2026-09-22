package diff

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestSummarizeOAuthRequestScopedErrors_NormalizesKeys(t *testing.T) {
	out := SummarizeOAuthRequestScopedErrors(map[string][]config.RequestScopedErrorRule{
		" Vertex ": {
			{Status: 400, Match: []string{"error"}, Action: "stop"},
		},
		"": {
			{Status: 500, Match: []string{"err"}, Action: "continue"},
		},
	})
	if len(out) != 1 {
		t.Fatalf("expected 1 normalized entry, got %d", len(out))
	}
	if summary, ok := out["vertex"]; !ok || summary.count != 1 {
		t.Fatalf("unexpected summary for vertex: %#v", summary)
	}

	if outEmpty := SummarizeOAuthRequestScopedErrors(nil); outEmpty != nil {
		t.Fatalf("expected nil summary for nil map, got %#v", outEmpty)
	}
}

func TestDiffOAuthRequestScopedErrorsChanges(t *testing.T) {
	oldMap := map[string][]config.RequestScopedErrorRule{
		"vertex": {
			{Status: 400, Match: []string{"context_length"}, Action: "stop"},
		},
		"claude": {
			{Status: 429, Match: []string{"rate_limit"}, Action: "continue"},
		},
	}
	newMap := map[string][]config.RequestScopedErrorRule{
		"vertex": {
			{Status: 400, Match: []string{"context_length_updated"}, Action: "stop"},
		},
		"codex": {
			{Status: 400, Match: []string{"window_exceeded"}, Action: "stop"},
		},
	}

	changes, affected := DiffOAuthRequestScopedErrorsChanges(oldMap, newMap)

	expectContains(t, changes, "oauth-request-scoped-errors[claude]: removed")
	expectContains(t, changes, "oauth-request-scoped-errors[codex]: added (1 entries)")
	expectContains(t, changes, "oauth-request-scoped-errors[vertex]: updated (1 -> 1 entries)")

	expectContains(t, affected, "claude")
	expectContains(t, affected, "codex")
	expectContains(t, affected, "vertex")
}
