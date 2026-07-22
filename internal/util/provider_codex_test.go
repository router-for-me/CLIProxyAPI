package util

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestResolveCodexIntegrationModel(t *testing.T) {
	integration := config.DefaultCodexIntegrationConfig()
	integration.Enabled = true

	tests := []struct {
		name         string
		model        string
		wantProvider string
		wantUpstream string
		wantMatched  bool
		wantErr      bool
	}{
		{name: "grok", model: "xai/grok-4.5", wantProvider: "xai", wantUpstream: "grok-4.5", wantMatched: true},
		{name: "gemini suffix", model: "antigravity/gemini-3.1-pro(high)", wantProvider: "antigravity", wantUpstream: "gemini-pro-agent(high)", wantMatched: true},
		{name: "custom credential prefix", model: "teamA/gemini-3.1-pro", wantUpstream: "teamA/gemini-3.1-pro"},
		{name: "unknown reserved slug", model: "xai/missing", wantMatched: true, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, matched, err := ResolveCodexIntegrationModel(tt.model, &integration)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ResolveCodexIntegrationModel() error = %v, wantErr %v", err, tt.wantErr)
			}
			if matched != tt.wantMatched {
				t.Fatalf("matched = %v, want %v", matched, tt.wantMatched)
			}
			if err == nil && (resolved.Provider != tt.wantProvider || resolved.UpstreamModel != tt.wantUpstream) {
				t.Fatalf("resolved = %#v, want provider=%q upstream=%q", resolved, tt.wantProvider, tt.wantUpstream)
			}
		})
	}
}

func TestResolveCodexIntegrationModelDisabledPreservesReservedPrefix(t *testing.T) {
	integration := config.DefaultCodexIntegrationConfig()
	resolved, matched, err := ResolveCodexIntegrationModel("xai/grok-4.5", &integration)
	if err != nil || matched || resolved.UpstreamModel != "xai/grok-4.5" {
		t.Fatalf("ResolveCodexIntegrationModel() = %#v, %v, %v", resolved, matched, err)
	}
}
