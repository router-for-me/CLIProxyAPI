package openai

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
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

// A turn that carries tool metadata the core protocol would pass through
// unchanged must not be forced into tools-replay: replay drops the duplex
// input channel, so live steering would start failing on turns that never
// needed any rewriting.
func TestResponsesWebsocketPassThroughTurnKeepsNativePassthrough(t *testing.T) {
	h := toolsReplayTestHandlerConventions()
	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "namespace only",
			payload: `{"type":"response.create","tools":[{"type":"namespace","name":"fs","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}],"input":[]}`,
		},
		{
			name:    "eager function only",
			payload: `{"type":"response.create","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}],"input":[]}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			for _, provider := range []string{"xai", "codex", "meta", "gemini"} {
				payload := []byte(testCase.payload)
				if h.responsesWebsocketRequiresToolsReplayForRoute(payload, provider, "review-model") {
					t.Errorf("provider %q: a pass-through turn must not force tools-replay", provider)
				}
			}
		})
	}
}

// The counterpart guard: tightening the pass-through case must not let a turn
// the core protocol really rewrites slip into native duplex passthrough, where
// the executor would reject the wire contract it carries.
func TestResponsesWebsocketToolProtocolTurnStillForcesReplay(t *testing.T) {
	h := toolsReplayTestHandlerConventions()
	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "tool_search declaration",
			payload: `{"type":"response.create","tools":[{"type":"tool_search"}],"input":[]}`,
		},
		{
			name:    "tool_search history",
			payload: `{"type":"response.create","input":[{"type":"tool_search_call","call_id":"s","arguments":{}},{"type":"tool_search_output","call_id":"s","tools":[]}]}`,
		},
		{
			name:    "custom declaration",
			payload: `{"type":"response.create","tools":[{"type":"custom","name":"apply_patch"}],"input":[]}`,
		},
		{
			name:    "custom history",
			payload: `{"type":"response.create","input":[{"type":"custom_tool_call","call_id":"c","name":"apply_patch","input":"x"},{"type":"custom_tool_call_output","call_id":"c","output":"ok"}]}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// xai strips the built-in and needs the bridge, so it must replay.
			if !h.responsesWebsocketRequiresToolsReplayForRoute([]byte(testCase.payload), "xai", "review-model") {
				t.Error("a rewritten turn on the xai route must force tools-replay")
			}
			// A non-native provider must keep replaying as well.
			if !h.responsesWebsocketRequiresToolsReplayForRoute([]byte(testCase.payload), "gemini", "gemini-3.8-flash") {
				t.Error("a rewritten turn on a non-native route must force tools-replay")
			}
		})
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
