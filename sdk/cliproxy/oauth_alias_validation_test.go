package cliproxy

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestValidateOAuthAliasExclusions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		aliases  map[string][]config.OAuthModelAlias
		excluded map[string][]string
		// wantSubstrings asserts each substring appears in at least one returned warning.
		wantSubstrings []string
		wantCount      int
	}{
		{
			name:      "empty aliases returns nil",
			aliases:   nil,
			excluded:  map[string][]string{"claude": {"claude-*"}},
			wantCount: 0,
		},
		{
			name: "empty exclusions returns nil",
			aliases: map[string][]config.OAuthModelAlias{
				"claude": {{Name: "claude-opus-4-6-thinking", Alias: "opus"}},
			},
			excluded:  nil,
			wantCount: 0,
		},
		{
			name: "wildcard matches alias upstream — same channel and provider",
			aliases: map[string][]config.OAuthModelAlias{
				"antigravity": {{Name: "claude-opus-4-6-thinking", Alias: "opus"}},
			},
			excluded:  map[string][]string{"antigravity": {"claude-*"}},
			wantCount: 1,
			wantSubstrings: []string{
				`alias="opus"`,
				`channel="antigravity"`,
				`upstream="claude-opus-4-6-thinking"`,
				`pattern="claude-*"`,
			},
		},
		{
			name: "exact match — no wildcard",
			aliases: map[string][]config.OAuthModelAlias{
				"claude": {{Name: "claude-sonnet-4-5", Alias: "sonnet"}},
			},
			excluded:  map[string][]string{"claude": {"claude-sonnet-4-5"}},
			wantCount: 1,
			wantSubstrings: []string{
				`alias="sonnet"`,
				`upstream="claude-sonnet-4-5"`,
				`pattern="claude-sonnet-4-5"`,
			},
		},
		{
			name: "non-matching upstream produces no warning",
			aliases: map[string][]config.OAuthModelAlias{
				"claude": {{Name: "claude-haiku-4-5", Alias: "haiku"}},
			},
			excluded:  map[string][]string{"claude": {"claude-opus-*"}},
			wantCount: 0,
		},
		{
			name: "exclusion on different channel does not fire",
			aliases: map[string][]config.OAuthModelAlias{
				"antigravity": {{Name: "claude-opus-4-6-thinking", Alias: "opus"}},
			},
			excluded:  map[string][]string{"claude": {"claude-*"}},
			wantCount: 0,
		},
		{
			name: "case-insensitive matching against mixed-case upstream",
			aliases: map[string][]config.OAuthModelAlias{
				"claude": {{Name: "Claude-Opus-4-6", Alias: "opus"}},
			},
			excluded:  map[string][]string{"claude": {"CLAUDE-*"}},
			wantCount: 1,
			wantSubstrings: []string{
				`upstream="Claude-Opus-4-6"`,
				`pattern="CLAUDE-*"`,
			},
		},
		{
			name: "thinking-suffix preserved in upstream — wildcard still matches",
			aliases: map[string][]config.OAuthModelAlias{
				"claude": {{Name: "claude-sonnet-4-5-20250514(low)", Alias: "sonnet"}},
			},
			excluded:  map[string][]string{"claude": {"claude-sonnet-*"}},
			wantCount: 1,
		},
		{
			name: "multiple aliases, only one collides",
			aliases: map[string][]config.OAuthModelAlias{
				"claude": {
					{Name: "claude-opus-4-6-thinking", Alias: "opus"},
					{Name: "claude-haiku-4-5", Alias: "haiku"},
				},
			},
			excluded:  map[string][]string{"claude": {"claude-opus-*"}},
			wantCount: 1,
			wantSubstrings: []string{
				`alias="opus"`,
			},
		},
		{
			name: "empty pattern is skipped",
			aliases: map[string][]config.OAuthModelAlias{
				"claude": {{Name: "claude-opus-4-6", Alias: "opus"}},
			},
			excluded:  map[string][]string{"claude": {"", "claude-*"}},
			wantCount: 1,
		},
		{
			name: "empty alias name is skipped",
			aliases: map[string][]config.OAuthModelAlias{
				"claude": {{Name: "", Alias: "opus"}, {Name: "claude-opus-4-6", Alias: ""}},
			},
			excluded:  map[string][]string{"claude": {"claude-*"}},
			wantCount: 0,
		},
		{
			name: "channel key is normalized to lowercase",
			aliases: map[string][]config.OAuthModelAlias{
				" CLAUDE ": {{Name: "claude-opus-4-6", Alias: "opus"}},
			},
			excluded:  map[string][]string{"claude": {"claude-*"}},
			wantCount: 1,
		},
		{
			name: "Daniel's documented case — antigravity per-channel exclusion blocks claude aliases (self-alias filtered)",
			aliases: map[string][]config.OAuthModelAlias{
				"antigravity": {
					{Name: "claude-opus-4-6-thinking", Alias: "opus"},
					{Name: "claude-opus-4-6-thinking", Alias: "opus[1m]"},
					// Self-alias is filtered to mirror compileOAuthModelAliasTable's
					// behavior — never enters the runtime table.
					{Name: "claude-opus-4-6-thinking", Alias: "claude-opus-4-6-thinking"},
				},
			},
			excluded:  map[string][]string{"antigravity": {"claude-*"}},
			wantCount: 2,
			wantSubstrings: []string{
				`alias="opus"`,
				`alias="opus[1m]"`,
			},
		},
		{
			name: "self-alias is filtered (case-insensitive) regardless of exclusion match",
			aliases: map[string][]config.OAuthModelAlias{
				"claude": {{Name: "Claude-Opus-4-6", Alias: "claude-opus-4-6"}},
			},
			excluded:  map[string][]string{"claude": {"claude-*"}},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := validateOAuthAliasExclusions(tt.aliases, tt.excluded)
			if len(got) != tt.wantCount {
				t.Errorf("warning count: got %d, want %d; warnings=%v", len(got), tt.wantCount, got)
			}
			for _, want := range tt.wantSubstrings {
				found := false
				for _, w := range got {
					if strings.Contains(w, want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected warning containing %q, got %v", want, got)
				}
			}
		})
	}
}
