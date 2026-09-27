package openai

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func toolsReplayTestHandler(enabled bool) *OpenAIResponsesAPIHandler {
	cfg := &internalconfig.Config{}
	if enabled {
		on := true
		cfg.ResponsesTools.Enabled = &on
		cfg.ResponsesTools.Routes = []internalconfig.ResponsesToolsRoute{{
			Match: internalconfig.ResponsesToolsMatch{
				Provider: "codex", AuthKind: "oauth",
				UpstreamModel: "gpt-5.6-sol", UpstreamFormat: "codex",
			},
			ClientSearch: "bridge", CustomTools: "inherit",
		}}
	} else {
		// Absent configuration keeps the convention on, so the emergency gate
		// has to be requested explicitly.
		off := false
		cfg.ResponsesTools.Enabled = &off
	}
	cfg.NormalizeResponsesToolsConfig()
	mgr := coreauth.NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)
	return NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, mgr))
}

// toolsReplayTestHandlerConventions builds a handler with no explicit route
// table, so every route follows the convention policy.
func toolsReplayTestHandlerConventions() *OpenAIResponsesAPIHandler {
	cfg := &internalconfig.Config{}
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
	if h.responsesWebsocketRequiresToolsReplayForRoute(searchTurn, "gemini", "gpt-5.6-sol") {
		t.Fatalf("a different provider route must not force tools-replay")
	}
	if h.responsesWebsocketRequiresToolsReplayForRoute(searchTurn, "codex", "other-model") {
		t.Fatalf("a different model route must not force tools-replay")
	}
	if !h.responsesWebsocketRequiresToolsReplayForRoute(searchTurn, "codex", "gpt-5.6-sol") {
		t.Fatalf("matching provider and model route must force tools-replay")
	}
	bare := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
	if bare.responsesWebsocketRequiresToolsReplay(searchTurn) {
		t.Fatalf("without manager must not force replay")
	}
}

func TestResponsesWebsocketNativeRouteKeepsPassthroughByDefault(t *testing.T) {
	h := toolsReplayTestHandlerConventions()
	searchTurn := []byte(`{"type":"response.create","tools":[{"type":"tool_search"}],"input":[]}`)
	if h.responsesWebsocketRequiresToolsReplayForRoute(searchTurn, "codex", "gpt-5.6-sol") {
		t.Fatal("a native Responses route needs no rewriting and must not be forced into replay")
	}
	if h.responsesWebsocketRequiresToolsReplayForRoute(searchTurn, "meta", "muse") {
		t.Fatal("meta speaks the native Responses format")
	}
	if !h.responsesWebsocketRequiresToolsReplayForRoute(searchTurn, "gemini", "gemini-3.8-flash") {
		t.Fatal("a non-native route takes the bridge and therefore needs replay")
	}
}

func TestResponsesWebsocketExecutorInputIsNotPassedToToolsReplay(t *testing.T) {
	input := make(chan cliproxyexecutor.WebsocketInput)
	if got := responsesWebsocketExecutorInput(input, true); got != nil {
		t.Fatal("tools-replay turn exposed the raw duplex input channel to the executor")
	}
	if got := responsesWebsocketExecutorInput(input, false); got != input {
		t.Fatal("non-replay turn must preserve its duplex input channel")
	}
}

func TestResponsesWebsocketInitialToolTurnUsesReplayBeforeNativePassthrough(t *testing.T) {
	h := toolsReplayTestHandler(true)
	searchTurn := []byte(`{"type":"response.create","model":"gpt-5.6-sol","tools":[{"type":"tool_search"}],"input":[]}`)

	nativePassthrough, toolsReplay := h.responsesWebsocketResolveToolMode(
		searchTurn,
		"gpt-5.6-sol",
		"codex",
		"gpt-5.6-sol",
		"",
		false,
	)
	if nativePassthrough {
		t.Fatal("a bridged first turn must not use native websocket passthrough")
	}
	if !toolsReplay {
		t.Fatal("a bridged first turn must select tools-replay even before native passthrough is established")
	}
	if got := responsesWebsocketExecutorInput(make(chan cliproxyexecutor.WebsocketInput), toolsReplay); got != nil {
		t.Fatal("the first bridged turn must not expose duplex input to the executor")
	}
}

func TestIsResponsesWebsocketToolsReplayControlFrame(t *testing.T) {
	if !isResponsesWebsocketToolsReplayControlFrame([]byte(`{"type":"response.steer","input":"continue"}`)) {
		t.Fatal("response.steer must be rejected in tools-replay mode")
	}
	if isResponsesWebsocketToolsReplayControlFrame([]byte(`{"type":"response.create","input":[]}`)) {
		t.Fatal("ordinary response.create must remain allowed")
	}
}
