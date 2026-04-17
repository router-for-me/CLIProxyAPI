package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
)

// TestApplyCloaking_ToolRemapDisabled_KeepsOriginalNames verifies that when
// OAuthRemapToolNames is explicitly set to false, tool names in the request
// are not renamed to Claude Code equivalents.
func TestApplyCloaking_ToolRemapDisabled_KeepsOriginalNames(t *testing.T) {
	disable := false
	cfg := &config.CloakConfig{
		OAuthRemapToolNames: &disable,
	}
	levers := helps.ResolveOAuthLevers(cfg, nil)

	payload := []byte(`{"tools":[{"name":"bash","description":"run bash"},{"name":"read","description":"read file"}]}`)

	// When remap is disabled the lever is false, so remap is skipped.
	if levers.RemapToolNames {
		t.Fatal("expected RemapToolNames=false when OAuthRemapToolNames is explicitly false")
	}

	// Simulate what Execute does: only remap when lever is true.
	var remapped bool
	if levers.RemapToolNames {
		payload, remapped = remapOAuthToolNames(payload)
	}

	if remapped {
		t.Error("remap should not have occurred when lever is disabled")
	}
	// Original names must be unchanged.
	if string(payload) != `{"tools":[{"name":"bash","description":"run bash"},{"name":"read","description":"read file"}]}` {
		t.Errorf("tool names should be unchanged when remap is disabled, got: %s", payload)
	}
}

// TestApplyCloaking_ToolRemapEnabled_RenamesToClaudeCodeNames verifies that when
// OAuthRemapToolNames is nil (default) or true, tool names are renamed to their
// Claude Code equivalents to avoid Anthropic fingerprinting.
func TestApplyCloaking_ToolRemapEnabled_RenamesToClaudeCodeNames(t *testing.T) {
	// nil config => default (remap enabled)
	levers := helps.ResolveOAuthLevers(nil, nil)

	payload := []byte(`{"tools":[{"name":"bash","description":"run bash"},{"name":"read","description":"read file"}]}`)

	if !levers.RemapToolNames {
		t.Fatal("expected RemapToolNames=true when OAuthRemapToolNames is nil (default)")
	}

	// Simulate what Execute does: remap when lever is true.
	var remapped bool
	if levers.RemapToolNames {
		payload, remapped = remapOAuthToolNames(payload)
	}

	if !remapped {
		t.Error("remap should have occurred when lever is enabled (default)")
	}
	// Names should be TitleCase.
	if string(payload) == `{"tools":[{"name":"bash","description":"run bash"},{"name":"read","description":"read file"}]}` {
		t.Error("tool names should have been renamed but were unchanged")
	}
}

// TestResponsePath_ToolRemapDisabled_NoReverseApplied verifies that when remap is
// disabled on the request side, the reverse remap is also skipped on the response,
// because oauthToolNamesRemapped stays false (the flag is never set).
func TestResponsePath_ToolRemapDisabled_NoReverseApplied(t *testing.T) {
	disable := false
	cfg := &config.CloakConfig{
		OAuthRemapToolNames: &disable,
	}
	levers := helps.ResolveOAuthLevers(cfg, nil)

	payload := []byte(`{"tools":[{"name":"bash"}]}`)

	// Simulate request path: lever is false so remap is skipped.
	oauthToolNamesRemapped := false
	if levers.RemapToolNames {
		payload, oauthToolNamesRemapped = remapOAuthToolNames(payload)
	}

	if oauthToolNamesRemapped {
		t.Error("oauthToolNamesRemapped should be false when lever is disabled")
	}

	// Simulate response path: reverse remap is gated on oauthToolNamesRemapped.
	response := []byte(`{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{}}]}`)
	originalResponse := string(response)
	if oauthToolNamesRemapped {
		response = reverseRemapOAuthToolNames(response)
	}

	// Response must be unchanged since reverse remap was skipped.
	if string(response) != originalResponse {
		t.Errorf("response should be unchanged when remap is disabled, got: %s", response)
	}
}
