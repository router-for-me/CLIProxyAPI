package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestApplyOAuthModelAlias_ReasoningEffortAppliesFixedSuffix(t *testing.T) {
	t.Parallel()

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {{
			Name:            "gpt-5.5",
			Alias:           "gpt-5.5-high",
			ReasoningEffort: "high",
			Fork:            true,
		}},
	})

	auth := createAuthForChannel("codex")
	resolvedModel := mgr.applyOAuthModelAlias(auth, "gpt-5.5-high")
	if resolvedModel != "gpt-5.5(high)" {
		t.Fatalf("applyOAuthModelAlias() model = %q, want %q", resolvedModel, "gpt-5.5(high)")
	}
}

func TestApplyOAuthModelAlias_ReasoningEffortOverridesRequestSuffix(t *testing.T) {
	t.Parallel()

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {{
			Name:            "gpt-5.5",
			Alias:           "gpt-5.5-high",
			ReasoningEffort: "high",
			Fork:            true,
		}},
	})

	auth := createAuthForChannel("codex")
	resolvedModel := mgr.applyOAuthModelAlias(auth, "gpt-5.5-high(low)")
	if resolvedModel != "gpt-5.5(high)" {
		t.Fatalf("applyOAuthModelAlias() model = %q, want fixed alias effort %q", resolvedModel, "gpt-5.5(high)")
	}
}

func TestApplyOAuthModelAlias_PerAuthReasoningEffort(t *testing.T) {
	t.Parallel()

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	auth := &Auth{
		ID:       "codex-auth-id",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind":     "oauth",
			"model_aliases": `[{"name":"gpt-5.5","alias":"gpt-5.5-medium","reasoning-effort":"medium"}]`,
		},
	}

	resolvedModel := mgr.applyOAuthModelAlias(auth, "gpt-5.5-medium")
	if resolvedModel != "gpt-5.5(medium)" {
		t.Fatalf("applyOAuthModelAlias() model = %q, want %q", resolvedModel, "gpt-5.5(medium)")
	}
}
