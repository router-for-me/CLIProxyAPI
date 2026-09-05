package auth

import (
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestApplyOAuthModelAlias_WildcardAliasMatchesDatedModelID(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "test-auth-id", Provider: "codex"}

	if got := mgr.applyOAuthModelAlias(auth, "claude-haiku-4-5-20251001"); got != "gpt-5.6-luna" {
		t.Errorf("applyOAuthModelAlias() model = %q, want %q", got, "gpt-5.6-luna")
	}
	if got := mgr.applyOAuthModelAlias(auth, "claude-haiku-4-5-20260401"); got != "gpt-5.6-luna" {
		t.Errorf("later dated id: model = %q, want %q", got, "gpt-5.6-luna")
	}
}

func TestApplyOAuthModelAlias_WildcardAliasIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-luna", Alias: "Claude-Haiku-4-5-*"}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "test-auth-id", Provider: "codex"}

	if got := mgr.applyOAuthModelAlias(auth, "CLAUDE-HAIKU-4-5-20251001"); got != "gpt-5.6-luna" {
		t.Errorf("applyOAuthModelAlias() model = %q, want %q", got, "gpt-5.6-luna")
	}
}

func TestApplyOAuthModelAlias_WildcardAliasPreservesThinkingSuffix(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "test-auth-id", Provider: "codex"}

	if got := mgr.applyOAuthModelAlias(auth, "claude-haiku-4-5-20251001(high)"); got != "gpt-5.6-luna(high)" {
		t.Errorf("applyOAuthModelAlias() model = %q, want %q", got, "gpt-5.6-luna(high)")
	}
}

func TestApplyOAuthModelAlias_ExactAliasWinsOverWildcard(t *testing.T) {
	t.Parallel()

	// The wildcard is declared first on purpose: precedence must come from the match
	// kind, not from configuration order.
	aliases := map[string][]internalconfig.OAuthModelAlias{
		"codex": {
			{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"},
			{Name: "gpt-5.6-terra", Alias: "claude-haiku-4-5-20251001"},
		},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "test-auth-id", Provider: "codex"}

	if got := mgr.applyOAuthModelAlias(auth, "claude-haiku-4-5-20251001"); got != "gpt-5.6-terra" {
		t.Errorf("exact alias should win: model = %q, want %q", got, "gpt-5.6-terra")
	}
	if got := mgr.applyOAuthModelAlias(auth, "claude-haiku-4-5-20260401"); got != "gpt-5.6-luna" {
		t.Errorf("wildcard should still cover other ids: model = %q, want %q", got, "gpt-5.6-luna")
	}
}

func TestApplyOAuthModelAlias_WildcardFirstMatchWins(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"codex": {
			{Name: "gpt-5.6-luna", Alias: "claude-haiku-*"},
			{Name: "gpt-5.6-terra", Alias: "claude-*"},
		},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "test-auth-id", Provider: "codex"}

	if got := mgr.applyOAuthModelAlias(auth, "claude-haiku-4-5-20251001"); got != "gpt-5.6-luna" {
		t.Errorf("first matching pattern should win: model = %q, want %q", got, "gpt-5.6-luna")
	}
	if got := mgr.applyOAuthModelAlias(auth, "claude-sonnet-5"); got != "gpt-5.6-terra" {
		t.Errorf("broader pattern should still apply: model = %q, want %q", got, "gpt-5.6-terra")
	}
}

func TestApplyOAuthModelAlias_WildcardAliasDoesNotMatchOtherModels(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "test-auth-id", Provider: "codex"}

	// A bare id without the trailing segment must not match, and neither must an
	// unrelated model.
	if got := mgr.applyOAuthModelAlias(auth, "claude-haiku-4-5"); got != "claude-haiku-4-5" {
		t.Errorf("bare id should not match: model = %q, want unchanged", got)
	}
	if got := mgr.applyOAuthModelAlias(auth, "gpt-5.6-sol"); got != "gpt-5.6-sol" {
		t.Errorf("unrelated model should not match: model = %q, want unchanged", got)
	}
}

func TestApplyOAuthModelAlias_PerAuthWildcardAlias(t *testing.T) {
	t.Parallel()

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})

	auth := &Auth{ID: "test-auth-id", Provider: "codex"}
	SetOAuthModelAliasesAttribute(auth, []internalconfig.OAuthModelAlias{
		{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"},
	})

	if got := mgr.applyOAuthModelAlias(auth, "claude-haiku-4-5-20251001"); got != "gpt-5.6-luna" {
		t.Errorf("applyOAuthModelAlias() model = %q, want %q", got, "gpt-5.6-luna")
	}
}

func TestApplyOAuthModelAliasWithResult_WildcardForceMappingUsesRequestedModel(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "test-auth-id", Provider: "codex"}

	result := mgr.applyOAuthModelAliasWithResult(auth, "claude-haiku-4-5-20251001")
	if result.UpstreamModel != "gpt-5.6-luna" {
		t.Errorf("UpstreamModel = %q, want %q", result.UpstreamModel, "gpt-5.6-luna")
	}
	if result.ForceMapping {
		t.Errorf("ForceMapping = true, want false")
	}
	if result.OriginalAlias != "claude-haiku-4-5-20251001" {
		t.Errorf("OriginalAlias = %q, want the requested model", result.OriginalAlias)
	}
}

func TestApplyOAuthModelAliasWithResult_WildcardForceMappingEchoesRequestedModel(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*", ForceMapping: true}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "test-auth-id", Provider: "codex"}

	result := mgr.applyOAuthModelAliasWithResult(auth, "claude-haiku-4-5-20251001")
	if result.UpstreamModel != "gpt-5.6-luna" {
		t.Errorf("UpstreamModel = %q, want %q", result.UpstreamModel, "gpt-5.6-luna")
	}
	if !result.ForceMapping {
		t.Errorf("ForceMapping = false, want true")
	}
	// The pattern is not a model id, so it must never reach a response model field.
	if result.OriginalAlias != "claude-haiku-4-5-20251001" {
		t.Errorf("OriginalAlias = %q, want the concrete requested model", result.OriginalAlias)
	}
	if strings.Contains(result.OriginalAlias, "*") {
		t.Errorf("OriginalAlias = %q, must not contain the wildcard pattern", result.OriginalAlias)
	}
}

func TestApplyOAuthModelAliasWithResult_PerAuthWildcardForceMappingEchoesRequestedModel(t *testing.T) {
	t.Parallel()

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})

	auth := &Auth{ID: "test-auth-id", Provider: "codex"}
	SetOAuthModelAliasesAttribute(auth, []internalconfig.OAuthModelAlias{
		{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*", ForceMapping: true},
	})

	result := mgr.applyOAuthModelAliasWithResult(auth, "claude-haiku-4-5-20251001")
	if result.UpstreamModel != "gpt-5.6-luna" {
		t.Errorf("UpstreamModel = %q, want %q", result.UpstreamModel, "gpt-5.6-luna")
	}
	if result.OriginalAlias != "claude-haiku-4-5-20251001" {
		t.Errorf("OriginalAlias = %q, want the concrete requested model", result.OriginalAlias)
	}
}

func TestApplyOAuthModelAliasWithResult_ExactForceMappingStillUsesConfiguredAlias(t *testing.T) {
	t.Parallel()

	// The wildcard fix must not change how an exact alias reports itself.
	aliases := map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-20251001", ForceMapping: true}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "test-auth-id", Provider: "codex"}

	result := mgr.applyOAuthModelAliasWithResult(auth, "claude-haiku-4-5-20251001")
	if result.OriginalAlias != "claude-haiku-4-5-20251001" {
		t.Errorf("OriginalAlias = %q, want the configured alias", result.OriginalAlias)
	}
}

func TestApplyOAuthModelAliasWithResult_WildcardForceMappingDropsThinkingSuffix(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*", ForceMapping: true}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "test-auth-id", Provider: "codex"}

	result := mgr.applyOAuthModelAliasWithResult(auth, "claude-haiku-4-5-20251001(high)")
	if result.UpstreamModel != "gpt-5.6-luna(high)" {
		t.Errorf("UpstreamModel = %q, want %q", result.UpstreamModel, "gpt-5.6-luna(high)")
	}
	// OriginalAlias is written verbatim into the response model field, so it must be a
	// model id, exactly as an exact alias would report.
	if result.OriginalAlias != "claude-haiku-4-5-20251001" {
		t.Errorf("OriginalAlias = %q, want the suffix-free requested model", result.OriginalAlias)
	}
}

func TestApplyOAuthModelAliasWithResult_PerAuthWildcardForceMappingDropsThinkingSuffix(t *testing.T) {
	t.Parallel()

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})

	auth := &Auth{ID: "test-auth-suffix", Provider: "codex"}
	SetOAuthModelAliasesAttribute(auth, []internalconfig.OAuthModelAlias{
		{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*", ForceMapping: true},
	})

	result := mgr.applyOAuthModelAliasWithResult(auth, "claude-haiku-4-5-20251001(high)")
	if result.UpstreamModel != "gpt-5.6-luna(high)" {
		t.Errorf("UpstreamModel = %q, want %q", result.UpstreamModel, "gpt-5.6-luna(high)")
	}
	if result.OriginalAlias != "claude-haiku-4-5-20251001" {
		t.Errorf("OriginalAlias = %q, want the suffix-free requested model", result.OriginalAlias)
	}
}
