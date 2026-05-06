package synthesizer

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestWarnAuthAliasExclusionConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		auth           *coreauth.Auth
		cfg            *config.Config
		perKey         []string
		authKind       string
		wantCount      int
		wantSubstrings []string
	}{
		{
			name: "Daniel's documented case — antigravity per-account claude-* blocks claude-opus aliases",
			auth: &coreauth.Auth{ID: "antigravity-larry@ebsources.com.json", Provider: "antigravity"},
			cfg: &config.Config{
				OAuthModelAlias: map[string][]config.OAuthModelAlias{
					"antigravity": {
						{Name: "claude-opus-4-6-thinking", Alias: "opus"},
						{Name: "claude-opus-4-6-thinking", Alias: "opus[1m]"},
					},
				},
			},
			perKey:    []string{"claude-*"},
			authKind:  "oauth",
			wantCount: 2,
			wantSubstrings: []string{
				`auth="antigravity-larry@ebsources.com.json"`,
				`channel="antigravity"`,
				`alias="opus"`,
				`alias="opus[1m]"`,
				`pattern="claude-*"`,
			},
		},
		{
			name:      "no aliases configured for channel — no warnings",
			auth:      &coreauth.Auth{ID: "claude-acct.json", Provider: "claude"},
			cfg:       &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"antigravity": {{Name: "x", Alias: "y"}}}},
			perKey:    []string{"claude-*"},
			authKind:  "oauth",
			wantCount: 0,
		},
		{
			name:      "empty perKey — no warnings",
			auth:      &coreauth.Auth{ID: "x", Provider: "antigravity"},
			cfg:       &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"antigravity": {{Name: "claude-opus", Alias: "opus"}}}},
			perKey:    nil,
			authKind:  "oauth",
			wantCount: 0,
		},
		{
			name:      "apikey authKind has no channel — no warnings even with matching pattern",
			auth:      &coreauth.Auth{ID: "claude-key", Provider: "claude"},
			cfg:       &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"claude": {{Name: "claude-opus-4-6", Alias: "opus"}}}},
			perKey:    []string{"claude-*"},
			authKind:  "apikey",
			wantCount: 0,
		},
		{
			name:      "self-alias is filtered (case-insensitive)",
			auth:      &coreauth.Auth{ID: "x", Provider: "antigravity"},
			cfg:       &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"antigravity": {{Name: "Claude-Opus-4-6", Alias: "claude-opus-4-6"}}}},
			perKey:    []string{"claude-*"},
			authKind:  "oauth",
			wantCount: 0,
		},
		{
			name:      "non-matching upstream produces no warning",
			auth:      &coreauth.Auth{ID: "x", Provider: "antigravity"},
			cfg:       &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"antigravity": {{Name: "gemini-2.5-pro", Alias: "pro"}}}},
			perKey:    []string{"claude-*"},
			authKind:  "oauth",
			wantCount: 0,
		},
		{
			name:      "case-insensitive match against mixed-case upstream",
			auth:      &coreauth.Auth{ID: "x", Provider: "claude"},
			cfg:       &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"claude": {{Name: "Claude-Sonnet-4-5", Alias: "sonnet"}}}},
			perKey:    []string{"CLAUDE-*"},
			authKind:  "oauth",
			wantCount: 1,
		},
		{
			name:      "exact match without wildcard",
			auth:      &coreauth.Auth{ID: "x", Provider: "claude"},
			cfg:       &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"claude": {{Name: "claude-opus-4-6", Alias: "opus"}}}},
			perKey:    []string{"claude-opus-4-6"},
			authKind:  "oauth",
			wantCount: 1,
		},
		{
			name:      "auth with empty ID falls back to <unknown>",
			auth:      &coreauth.Auth{ID: "", Provider: "antigravity"},
			cfg:       &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"antigravity": {{Name: "claude-opus", Alias: "opus"}}}},
			perKey:    []string{"claude-*"},
			authKind:  "oauth",
			wantCount: 1,
			wantSubstrings: []string{
				`auth="<unknown>"`,
			},
		},
		{
			name:      "nil cfg returns nil",
			auth:      &coreauth.Auth{ID: "x", Provider: "claude"},
			cfg:       nil,
			perKey:    []string{"claude-*"},
			authKind:  "oauth",
			wantCount: 0,
		},
		{
			name:      "nil auth returns nil",
			auth:      nil,
			cfg:       &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"claude": {{Name: "x", Alias: "y"}}}},
			perKey:    []string{"x"},
			authKind:  "oauth",
			wantCount: 0,
		},
		{
			name: "unsupported provider (gemini) yields empty channel — no warnings",
			auth: &coreauth.Auth{ID: "x", Provider: "gemini"},
			cfg: &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
				"gemini": {{Name: "gemini-2.5-pro-exp", Alias: "pro"}},
			}},
			perKey:    []string{"gemini-*"},
			authKind:  "oauth",
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := WarnAuthAliasExclusionConflicts(tt.auth, tt.cfg, tt.perKey, tt.authKind)
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

func TestMatchExclusionWildcard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"", "anything", false},
		{"claude-opus-4-6", "claude-opus-4-6", true},
		{"claude-opus-4-6", "claude-opus-4-7", false},
		{"claude-*", "claude-opus-4-6-thinking", true},
		{"claude-*", "gemini-2.5-pro", false},
		{"*-thinking", "claude-opus-4-6-thinking", true},
		{"*-thinking", "claude-opus-4-6", false},
		{"claude-*-thinking", "claude-opus-4-6-thinking", true},
		{"claude-*-thinking", "claude--thinking", true}, // empty middle segment between literal dashes
		{"claude-*-thinking", "claude-thinking", false}, // missing inner dash separator
		{"claude-*-*-thinking", "claude-opus-4-6-thinking", true},
		{"*", "anything", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_vs_"+tt.value, func(t *testing.T) {
			t.Parallel()
			got := matchExclusionWildcard(tt.pattern, tt.value)
			if got != tt.want {
				t.Errorf("matchExclusionWildcard(%q, %q): got %v, want %v", tt.pattern, tt.value, got, tt.want)
			}
		})
	}
}
