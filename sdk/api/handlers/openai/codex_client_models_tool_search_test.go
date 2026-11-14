package openai

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	responsesclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// countingToolSearchExecutor records native tool search capability checks.
type countingToolSearchExecutor struct {
	*executor.ClaudeExecutor
	calls int
}

func (e *countingToolSearchExecutor) SupportsNativeToolSearch(a *auth.Auth, upstreamModel string) bool {
	e.calls++
	return e.ClaudeExecutor.SupportsNativeToolSearch(a, upstreamModel)
}

func TestCodexClientModelsClaudeToolSearchFollowsSwitch(t *testing.T) {
	previous := responsesclaude.ClientToolSearchEnabled()
	t.Cleanup(func() { responsesclaude.SetClientToolSearchEnabled(previous) })

	const model = "catalog-claude-tool-search"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient("catalog-claude-official", "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { modelRegistry.UnregisterClient("catalog-claude-official") })
	manager := auth.NewManager(nil, nil, nil)
	counting := &countingToolSearchExecutor{ClaudeExecutor: executor.NewClaudeExecutor(&config.Config{})}
	manager.RegisterExecutor(counting)
	if _, errRegister := manager.Register(context.Background(), &auth.Auth{ID: "catalog-claude-official", Provider: "claude", Status: auth.StatusActive, Attributes: map[string]string{"api_key": "k"}}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
	searchFlag := func() bool {
		t.Helper()
		for _, entry := range handler.codexClientModelsResponse("0.153.4")["models"].([]map[string]any) {
			if entry["slug"] == model {
				search, _ := entry["supports_search_tool"].(bool)
				return search
			}
		}
		t.Fatalf("missing model %q", model)
		return false
	}

	responsesclaude.SetClientToolSearchEnabled(true)
	if !searchFlag() {
		t.Fatal("official Claude route does not advertise search with upstream.claude.tool-search on")
	}
	responsesclaude.SetClientToolSearchEnabled(false)
	counting.calls = 0
	if searchFlag() {
		t.Fatal("Claude route advertises search with upstream.claude.tool-search off")
	}
	if counting.calls != 0 {
		t.Fatalf("capability consulted %d times with upstream.claude.tool-search off", counting.calls)
	}
}
