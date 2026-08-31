package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestApplyOAuthModelAliasEntries_WildcardAliasIsNotPublished(t *testing.T) {
	t.Parallel()

	aliases := []config.OAuthModelAlias{
		{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"},
	}
	models := []*ModelInfo{{ID: "gpt-5.6-luna"}}

	out := applyOAuthModelAliasEntries(aliases, models)
	if len(out) != 1 {
		t.Fatalf("model count = %d, want 1", len(out))
	}
	if out[0].ID != "gpt-5.6-luna" {
		t.Errorf("model id = %q, want the upstream model to keep its catalog entry", out[0].ID)
	}
}

func TestApplyOAuthModelAliasEntries_ExactAliasStillPublished(t *testing.T) {
	t.Parallel()

	aliases := []config.OAuthModelAlias{
		{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-20251001"},
	}
	models := []*ModelInfo{{ID: "gpt-5.6-luna"}}

	out := applyOAuthModelAliasEntries(aliases, models)
	if len(out) != 1 {
		t.Fatalf("model count = %d, want 1", len(out))
	}
	if out[0].ID != "claude-haiku-4-5-20251001" {
		t.Errorf("model id = %q, want the exact alias", out[0].ID)
	}
}

func TestCollectModelAliasPatterns(t *testing.T) {
	t.Parallel()

	patterns := collectModelAliasPatterns(map[string][]config.OAuthModelAlias{
		"codex": {
			{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"},
			{Name: "gpt-5.6-sol", Alias: "claude-opus-5"},
		},
		"antigravity": {
			{Name: "gemini-pro-agent", Alias: "gemini-*-preview"},
			{Name: "", Alias: "broken-*"},
		},
	})

	if len(patterns) != 2 {
		t.Fatalf("pattern count = %d, want 2", len(patterns))
	}
	// Channels are walked in sorted order, so antigravity comes first.
	if patterns[0].Pattern != "gemini-*-preview" || patterns[0].Target != "gemini-pro-agent" {
		t.Errorf("first pattern = %+v, want the antigravity entry", patterns[0])
	}
	if patterns[1].Pattern != "claude-haiku-4-5-*" || patterns[1].Target != "gpt-5.6-luna" {
		t.Errorf("second pattern = %+v, want the codex entry", patterns[1])
	}
}

func TestCollectModelAliasPatterns_NoWildcards(t *testing.T) {
	t.Parallel()

	patterns := collectModelAliasPatterns(map[string][]config.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-sol", Alias: "claude-opus-5"}},
	})
	if patterns != nil {
		t.Errorf("patterns = %+v, want nil when no wildcard alias is configured", patterns)
	}
}
