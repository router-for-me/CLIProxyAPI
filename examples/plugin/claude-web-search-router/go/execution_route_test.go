package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestBuildExecutionPlansForExecuteRespectsRouteTavily(t *testing.T) {
	currentConfig.Store(pluginConfig{
		Enabled:       true,
		Route:         string(backendTavily),
		TavilyAPIKeys: []string{"tvly-test"},
	})
	cfg := loadedConfig()
	req := pluginapi.ModelRouteRequest{
		SourceFormat:       "claude",
		RequestedModel:     "claude-sonnet-4-6",
		AvailableProviders: []string{"antigravity", "codex", "xai"},
	}
	plans := buildExecutionPlansForExecute(cfg, req)
	if len(plans) != 1 {
		t.Fatalf("plans len = %d, want 1 for route=tavily", len(plans))
	}
	if plans[0].backend != backendTavily {
		t.Fatalf("backend = %q, want tavily", plans[0].backend)
	}
}
