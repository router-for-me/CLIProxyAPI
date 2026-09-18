package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestIsProviderRowLive(t *testing.T) {
	cases := []struct {
		name         string
		row          string
		liveEvidence []string
		cooldown     map[string]bool
		want         bool
	}{
		{"empty row", "", []string{"claude"}, nil, false},
		{"whitespace row", "   ", []string{"claude"}, nil, false},
		{"compound exact match", "claude:42:key-7", []string{"claude:42:key-7"}, nil, true},
		{"compound case-insensitive match", "Claude:42:KEY-7", []string{"claude:42:key-7"}, nil, true},
		{"claude:42 NOT live when only 'claude' is in evidence", "claude:42", []string{"claude"}, nil, false},
		{"openai-compat provider-level fallback (entry live)", "openai-compatible-foo", []string{"openai-compatible-foo:bar"}, nil, true},
		{"openai-compat provider-level fallback (provider live)", "openai-compatible-foo", []string{"openai-compatible-foo"}, nil, true},
		{"openai-compat provider-level fallback (different entry)", "openai-compatible-foo", []string{"openai-compatible-foo:other"}, nil, true},
		{"openai-compat provider-level fallback (different provider)", "openai-compatible-foo", []string{"openai-compatible-baz:bar"}, nil, false},
		{"openai-compat compound is NOT a provider-level route", "openai-compatible-foo:bar", []string{"openai-compatible-foo"}, nil, false},
		{"claude compound partial (key only) is NOT live", "claude:42:key-7", []string{"claude:42"}, nil, false},
		{"cooldown blocks even with live evidence", "claude:42", []string{"claude:42"}, map[string]bool{"claude:42": true}, false},
		{"cooldown blocks compound", "claude:42:key-7", []string{"claude:42:key-7"}, map[string]bool{"claude:42:key-7": true}, false},
		{"no evidence", "claude:42", nil, nil, false},
		{"empty evidence after trim", "claude:42", []string{"", "  "}, nil, false},
		{"bare channel no compound", "claude", []string{"claude"}, nil, true},
		{"different channel no match", "openai:42", []string{"claude"}, nil, false},
		{"empty evidence + cooldown", "claude:42", nil, map[string]bool{"claude:42": true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsProviderRowLive(tc.row, tc.liveEvidence, tc.cooldown)
			if got != tc.want {
				t.Fatalf("IsProviderRowLive(%q, %v, %v) = %v, want %v", tc.row, tc.liveEvidence, tc.cooldown, got, tc.want)
			}
		})
	}
}

func TestCooldownProviderSetEmpty(t *testing.T) {
	if got := CooldownProviderSet(nil); got != nil {
		t.Fatalf("nil snapshot: want nil, got %v", got)
	}
	if got := CooldownProviderSet([]auth.CooldownStateRecord{}); got != nil {
		t.Fatalf("empty snapshot: want nil, got %v", got)
	}
}

func TestCooldownProviderSetFilters(t *testing.T) {
	in := []auth.CooldownStateRecord{
		{Provider: "Claude:42"},
		{Provider: "openai:42"},
		{Provider: ""},          // skipped
		{Provider: "  "},        // skipped
		{Provider: "openai:42"}, // duplicate, idempotent
	}
	got := CooldownProviderSet(in)
	if got == nil {
		t.Fatal("non-empty snapshot: want non-nil map")
	}
	if !got["claude:42"] {
		t.Fatalf("claude:42 should be set: %v", got)
	}
	if !got["openai:42"] {
		t.Fatalf("openai:42 should be set: %v", got)
	}
	if got[""] {
		t.Fatalf("empty provider key should not be set: %v", got)
	}
	if got["  "] {
		t.Fatalf("whitespace provider key should not be set: %v", got)
	}
	if len(got) != 2 {
		t.Fatalf("map size = %d, want 2", len(got))
	}
}
