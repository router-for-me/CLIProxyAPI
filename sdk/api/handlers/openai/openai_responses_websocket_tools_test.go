package openai

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func toolsReplayTestHandler(enabled bool) *OpenAIResponsesAPIHandler {
	cfg := &internalconfig.Config{}
	if enabled {
		cfg.ResponsesTools.Enabled = true
		cfg.ResponsesTools.Routes = []internalconfig.ResponsesToolsRoute{{
			Match: internalconfig.ResponsesToolsMatch{
				Provider: "codex", AuthKind: "oauth",
				UpstreamModel: "gpt-5.6-sol", UpstreamFormat: "codex",
			},
			ClientSearch: "bridge", CustomTools: "inherit",
		}}
	}
	cfg.NormalizeResponsesToolsConfig()
	mgr := coreauth.NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)
	return NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, mgr))
}

func TestResponsesWebsocketRequiresToolsReplay(t *testing.T) {
	h := toolsReplayTestHandler(true)
	searchTurn := []byte("{\"type\": \"response.create\", \"tools\": [{\"type\": \"tool_search\"}], \"input\": []}")
	if !h.responsesWebsocketRequiresToolsReplay(searchTurn) {
		t.Fatalf("search turn must force tools-replay when enabled")
	}
	plainTurn := []byte("{\"type\": \"response.create\", \"input\": \"hello\"}")
	if h.responsesWebsocketRequiresToolsReplay(plainTurn) {
		t.Fatalf("plain turn must not force replay")
	}
	off := toolsReplayTestHandler(false)
	if off.responsesWebsocketRequiresToolsReplay(searchTurn) {
		t.Fatalf("disabled feature must never force replay")
	}
	bare := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
	if bare.responsesWebsocketRequiresToolsReplay(searchTurn) {
		t.Fatalf("without manager must not force replay")
	}
}
