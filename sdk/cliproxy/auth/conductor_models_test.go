package auth

import (
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestExecutionModelCandidates_APIKeyAliasPoolRotates(t *testing.T) {
	cfg := &internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{{
		APIKey: "test-key",
		Prefix: "tenant",
		Models: []internalconfig.ClaudeModel{
			{Name: "claude-sonnet-4", Alias: "public"},
			{Name: "claude-sonnet-3.5", Alias: "public"},
		},
	}}}

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(cfg)

	auth := &Auth{
		ID:       "auth-claude-pool",
		Provider: "claude",
		Prefix:   "tenant",
		Attributes: map[string]string{
			AttributeAuthKind:    AuthKindAPIKey,
			AttributeAPIKey:      "test-key",
			AttributeSource:      "config:claude[0]",
			AttributeConfigIndex: "0",
		},
	}

	first := manager.executionModelCandidates(auth, "tenant/public")
	if len(first) != 2 || first[0] != "claude-sonnet-4" || first[1] != "claude-sonnet-3.5" {
		t.Fatalf("first candidates = %v, want [claude-sonnet-4 claude-sonnet-3.5]", first)
	}

	second := manager.executionModelCandidates(auth, "tenant/public")
	if len(second) != 2 || second[0] != "claude-sonnet-3.5" || second[1] != "claude-sonnet-4" {
		t.Fatalf("second candidates = %v, want [claude-sonnet-3.5 claude-sonnet-4]", second)
	}

	third := manager.executionModelCandidates(auth, "tenant/public")
	if len(third) != 2 || third[0] != "claude-sonnet-4" || third[1] != "claude-sonnet-3.5" {
		t.Fatalf("third candidates = %v, want [claude-sonnet-4 claude-sonnet-3.5]", third)
	}
}

func TestExecutionModelCandidates_APIKeyAliasPoolRotatesWithSuffix(t *testing.T) {
	cfg := &internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{{
		APIKey: "test-key",
		Prefix: "tenant",
		Models: []internalconfig.ClaudeModel{
			{Name: "claude-sonnet-4", Alias: "public"},
			{Name: "claude-sonnet-3.5", Alias: "public"},
		},
	}}}

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(cfg)

	auth := &Auth{
		ID:       "auth-claude-pool-suffix",
		Provider: "claude",
		Prefix:   "tenant",
		Attributes: map[string]string{
			AttributeAuthKind:    AuthKindAPIKey,
			AttributeAPIKey:      "test-key",
			AttributeSource:      "config:claude[0]",
			AttributeConfigIndex: "0",
		},
	}

	first := manager.executionModelCandidates(auth, "tenant/public(8192)")
	want := []string{"claude-sonnet-4(8192)", "claude-sonnet-3.5(8192)"}
	if len(first) != 2 || first[0] != want[0] || first[1] != want[1] {
		t.Fatalf("first candidates = %v, want %v", first, want)
	}
}

func TestPreparedExecutionModels_APIKeyPoolSkipsBlockedMembers(t *testing.T) {
	cfg := &internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{{
		APIKey: "test-key",
		Prefix: "tenant",
		Models: []internalconfig.ClaudeModel{
			{Name: "claude-sonnet-4", Alias: "public"},
			{Name: "claude-sonnet-3.5", Alias: "public"},
		},
	}}}

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(cfg)

	auth := &Auth{
		ID:       "auth-claude-pool-blocked",
		Provider: "claude",
		Prefix:   "tenant",
		Attributes: map[string]string{
			AttributeAuthKind:    AuthKindAPIKey,
			AttributeAPIKey:      "test-key",
			AttributeSource:      "config:claude[0]",
			AttributeConfigIndex: "0",
		},
		ModelStates: map[string]*ModelState{
			"claude-sonnet-4": {
				Unavailable:    true,
				NextRetryAfter: time.Now().Add(time.Hour),
			},
		},
	}

	models, pooled := manager.preparedExecutionModels(auth, "tenant/public")
	if !pooled {
		t.Fatalf("pooled = false, want true")
	}
	if len(models) != 1 || models[0] != "claude-sonnet-3.5" {
		t.Fatalf("filtered models = %v, want [claude-sonnet-3.5]", models)
	}
}

func TestExecutionModelCandidates_APIKeySingleModelUnchanged(t *testing.T) {
	cfg := &internalconfig.Config{GeminiKey: []internalconfig.GeminiKey{{
		APIKey: "gemini-key",
		Prefix: "team",
		Models: []internalconfig.GeminiModel{
			{Name: "gemini-2.5-pro", Alias: "public"},
		},
	}}}

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(cfg)

	auth := &Auth{
		ID:       "auth-gemini-single",
		Provider: "gemini",
		Prefix:   "team",
		Attributes: map[string]string{
			AttributeAuthKind:    AuthKindAPIKey,
			AttributeAPIKey:      "gemini-key",
			AttributeSource:      "config:gemini[0]",
			AttributeConfigIndex: "0",
		},
	}

	got := manager.executionModelCandidates(auth, "team/public")
	if len(got) != 1 || got[0] != "gemini-2.5-pro" {
		t.Fatalf("single model candidates = %v, want [gemini-2.5-pro]", got)
	}
}

func TestExecutionModelCandidates_APIKeyPoolForCodex(t *testing.T) {
	cfg := &internalconfig.Config{CodexKey: []internalconfig.CodexKey{{
		APIKey: "codex-key",
		Prefix: "team",
		Models: []internalconfig.CodexModel{
			{Name: "deepseek-v4", Alias: "fast"},
			{Name: "gpt-5.4", Alias: "fast"},
		},
	}}}

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(cfg)

	auth := &Auth{
		ID:       "auth-codex-pool",
		Provider: "codex",
		Prefix:   "team",
		Attributes: map[string]string{
			AttributeAuthKind:    AuthKindAPIKey,
			AttributeAPIKey:      "codex-key",
			AttributeSource:      "config:codex[0]",
			AttributeConfigIndex: "0",
		},
	}

	first := manager.executionModelCandidates(auth, "team/fast")
	if len(first) != 2 || first[0] != "deepseek-v4" || first[1] != "gpt-5.4" {
		t.Fatalf("first codex candidates = %v, want [deepseek-v4 gpt-5.4]", first)
	}
}
