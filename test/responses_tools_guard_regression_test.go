package test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/responsestools"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// responsesToolsClaudeStream is the smallest Claude SSE body the executor
// accepts as a completed turn.
const responsesToolsClaudeStream = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"review-model","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

const responsesToolsCodexStream = "data: " +
	`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"

// responsesToolsEagerNamespaceBody declares a tool_search built-in plus one
// eager namespaced tool whose name starts with its namespace, the shape that
// the outbound guard used to reject before the request was sent.
func responsesToolsEagerNamespaceBody(model string) []byte {
	return []byte(`{"model":"` + model + `","tools":[` +
		`{"type":"tool_search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}},` +
		`{"type":"namespace","name":"fs","tools":[{"type":"function","name":"fs_read","parameters":{"type":"object"}}]}` +
		`],"input":[{"role":"user","content":"hello"}]}`)
}

// responsesToolsDeferredBody declares a large deferred tool that the bridge
// must keep out of the first upstream packet.
func responsesToolsDeferredBody(model string) []byte {
	return []byte(`{"model":"` + model + `","tools":[` +
		`{"type":"tool_search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}},` +
		`{"type":"namespace","name":"fs","tools":[{"type":"function","name":"read","defer_loading":true,"description":"` +
		strings.Repeat("x", 2048) + `","parameters":{"type":"object"}}]}` +
		`],"input":[{"role":"user","content":"read the file"}]}`)
}

// responsesToolsRecorder captures the last upstream request. The mutex is
// required: the handler runs on the server goroutine while the test reads the
// captured body.
type responsesToolsRecorder struct {
	mu   sync.Mutex
	body []byte
}

func (r *responsesToolsRecorder) set(body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.body = body
}

func (r *responsesToolsRecorder) get() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body
}

func newResponsesToolsProviderServer(t *testing.T, response string) (*httptest.Server, *responsesToolsRecorder) {
	t.Helper()
	received := new(responsesToolsRecorder)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
		}
		received.set(body)
		w.Header().Set("Content-Type", "text/event-stream")
		if _, err := w.Write([]byte(response)); err != nil {
			t.Errorf("write upstream response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server, received
}

func registerResponsesToolsProvider(t *testing.T, mgr *auth.Manager, authID, provider, model, baseURL string) {
	t.Helper()
	registry.GetGlobalRegistry().RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, err := mgr.Register(context.Background(), &auth.Auth{
		ID: authID, Provider: provider, Status: auth.StatusActive,
		Attributes: map[string]string{"base_url": baseURL, "api_key": "test", "auth_kind": "api-key"},
		Metadata:   map[string]any{"disable_cooling": true},
	}); err != nil {
		t.Fatalf("register %s auth: %v", provider, err)
	}
}

// TestResponsesToolsAcceptsEagerNamespacedToolOnGuardedRoutes locks the
// contract between the outbound guard and each provider translator: a valid
// eager namespaced tool must reach the upstream whether the core tool
// protocol is enabled or not.
func TestResponsesToolsAcceptsEagerNamespacedToolOnGuardedRoutes(t *testing.T) {
	cases := []struct {
		name       string
		provider   string
		newManager func(*config.Config) *auth.Manager
		stream     string
	}{
		{
			name: "claude", provider: "claude", stream: responsesToolsClaudeStream,
			newManager: func(cfg *config.Config) *auth.Manager {
				mgr := auth.NewManager(nil, nil, nil)
				mgr.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(cfg))
				return mgr
			},
		},
		{
			name: "xai", provider: "xai", stream: responsesToolsCodexStream,
			newManager: func(cfg *config.Config) *auth.Manager {
				mgr := auth.NewManager(nil, nil, nil)
				mgr.RegisterExecutor(runtimeexecutor.NewXAIExecutor(cfg))
				return mgr
			},
		},
	}
	for _, testCase := range cases {
		for _, enabled := range []bool{false, true} {
			name := testCase.name
			if enabled {
				name += "/core-on"
			} else {
				name += "/core-off"
			}
			t.Run(name, func(t *testing.T) {
				server, received := newResponsesToolsProviderServer(t, testCase.stream)
				cfg := &config.Config{}
				cfg.ResponsesTools.Enabled = &enabled
				cfg.NormalizeResponsesToolsConfig()
				if err := cfg.ValidateResponsesToolsConfig(); err != nil {
					t.Fatalf("config: %v", err)
				}
				mgr := testCase.newManager(cfg)
				mgr.SetRetryConfig(0, 0, 0)
				mgr.SetConfig(cfg)
				registerResponsesToolsProvider(t, mgr, "guard-auth-"+testCase.provider, testCase.provider, "review-model", server.URL)

				body := responsesToolsEagerNamespaceBody("review-model")
				if _, err := mgr.Execute(context.Background(), []string{testCase.provider},
					executor.Request{Model: "review-model", Payload: body},
					executor.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: body}); err != nil {
					t.Fatalf("valid eager namespaced tool rejected: %v", err)
				}
				if !strings.Contains(string(received.get()), "fs_read") {
					t.Fatalf("upstream request lost the namespaced tool: %s", received.get())
				}
			})
		}
	}
}

// TestResponsesToolsPrunesDeferredToolOnNonNativeProvider covers the PR goal
// on a provider whose executor strips the tool_search built-in: the bridge
// must keep the search entry point and drop the deferred schema from the
// first packet instead of forwarding the full declaration.
func TestResponsesToolsPrunesDeferredToolOnNonNativeProvider(t *testing.T) {
	server, received := newResponsesToolsProviderServer(t, responsesToolsCodexStream)
	cfg := &config.Config{}
	cfg.NormalizeResponsesToolsConfig()
	if err := cfg.ValidateResponsesToolsConfig(); err != nil {
		t.Fatalf("config: %v", err)
	}
	mgr := auth.NewManager(nil, nil, nil)
	mgr.RegisterExecutor(runtimeexecutor.NewXAIExecutor(cfg))
	mgr.SetRetryConfig(0, 0, 0)
	mgr.SetConfig(cfg)
	registerResponsesToolsProvider(t, mgr, "prune-xai-auth", "xai", "grok-review", server.URL)

	body := responsesToolsDeferredBody("grok-review")
	if _, err := mgr.Execute(context.Background(), []string{"xai"},
		executor.Request{Model: "grok-review", Payload: body},
		executor.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: body}); err != nil {
		t.Fatalf("deferred request failed: %v", err)
	}
	upstream := received.get()
	tools := gjson.GetBytes(upstream, "tools").Array()
	if len(tools) != 1 {
		t.Fatalf("first packet tools = %s, want only the search entry point", gjson.GetBytes(upstream, "tools").Raw)
	}
	if name := tools[0].Get("name").String(); name != "tool_search" {
		t.Fatalf("first packet tool = %q, want the tool_search entry point", name)
	}
	if strings.Contains(string(upstream), "defer_loading") {
		t.Fatalf("deferred schema leaked into the first packet: %s", upstream)
	}
}

// TestResponsesToolsGuardAcceptsNamespacedToolOnCodexBridgeRoute exercises the
// outbound guard on a route where it really runs: the native convention leaves
// the attempt inactive, so only an explicit bridge rule attaches a wire
// contract. The Responses-family executors keep the namespace container on the
// wire, so the guard must accept a tool whose local name shares its namespace.
func TestResponsesToolsGuardAcceptsNamespacedToolOnCodexBridgeRoute(t *testing.T) {
	server, received := newResponsesToolsProviderServer(t, responsesToolsCodexStream)
	const model = "gpt-5.6-sol"
	cfg := &config.Config{}
	enabled := true
	cfg.ResponsesTools.Enabled = &enabled
	cfg.ResponsesTools.Routes = []config.ResponsesToolsRoute{{
		Match: config.ResponsesToolsMatch{
			Provider: "codex", AuthKind: "oauth",
			UpstreamModel: model, UpstreamFormat: "codex",
		},
		ClientSearch: "bridge", CustomTools: "inherit",
	}}
	cfg.NormalizeResponsesToolsConfig()
	if err := cfg.ValidateResponsesToolsConfig(); err != nil {
		t.Fatalf("config: %v", err)
	}
	mgr := auth.NewManager(nil, nil, nil)
	mgr.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
	mgr.SetRetryConfig(0, 0, 0)
	mgr.SetConfig(cfg)
	registerResponsesToolsProvider(t, mgr, "guard-codex-auth", "codex", model, server.URL)

	body := []byte(`{"model":"` + model + `","tools":[` +
		`{"type":"tool_search"},` +
		`{"type":"namespace","name":"fs","tools":[{"type":"function","name":"fs_read","parameters":{"type":"object"}}]}` +
		`],"input":[{"role":"user","content":"hi"}]}`)
	if _, err := mgr.Execute(context.Background(), []string{"codex"},
		executor.Request{Model: model, Payload: body},
		executor.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: body}); err != nil {
		t.Fatalf("guard rejected a valid namespaced tool on the codex bridge route: %v", err)
	}
	if !strings.Contains(string(received.get()), "fs_read") {
		t.Fatalf("upstream request lost the namespaced tool: %s", received.get())
	}
}

// TestResponsesToolsCodexWebsocketCustomFollowup covers a Codex websocket
// followup that carries only a custom tool result plus previous_response_id,
// with no tool declarations in the new turn. The turn must still be routed
// through the tool adapter, the opaque response id must be expanded into
// replayable history, and the custom call must reach the upstream as its
// bridged function alias instead of leaking custom protocol items.
func TestResponsesToolsCodexWebsocketCustomFollowup(t *testing.T) {
	const model = "gpt-5.6-sol"
	customAlias := responsestools.CustomAliasFor("", "apply_patch")
	h, _ := newResponsesToolsIntegrationHarnessWithCustomMode(t, model, []string{
		responsesToolsSSECompleted("cc_1", customAlias, `{"input":"patch text"}`),
		responsesToolsSSECompletedMessage("done"),
	}, "function")
	conn := openResponsesIntegrationWebsocket(t, h.handler)

	turn1 := `{"type":"response.create","model":"` + model + `","tools":[{"type":"custom","name":"apply_patch","description":"apply a patch"}],` +
		`"input":[{"type":"message","role":"user","content":"patch it"}]}`
	events1 := postResponsesIntegrationWebsocketTurn(t, conn, turn1)
	first := websocketOutputItemDone(t, events1)
	if first["type"] != "custom_tool_call" || first["name"] != "apply_patch" || first["input"] != "patch text" {
		t.Fatalf("turn1 did not restore the custom tool call: %#v", first)
	}
	responseID := integrationResponseID(t, completedWebsocketEvent(t, events1))

	// The followup declares no tools at all: it only returns the tool result
	// and points at the previous response.
	turn2 := `{"type":"response.create","previous_response_id":"` + responseID + `","model":"` + model + `",` +
		`"input":[{"type":"custom_tool_call_output","call_id":"cc_1","output":"applied"}]}`
	postResponsesIntegrationWebsocketTurn(t, conn, turn2)

	h.mu.Lock()
	wireBodies := append([]string(nil), h.wireBodies...)
	h.mu.Unlock()
	if len(wireBodies) != 2 {
		t.Fatalf("expected two upstream calls, got %d", len(wireBodies))
	}
	followup := wireBodies[1]
	if strings.Contains(followup, "custom_tool_call") || strings.Contains(followup, `"type":"custom"`) {
		t.Fatalf("custom protocol leaked into the followup wire body: %s", followup)
	}
	if !strings.Contains(followup, customAlias) {
		t.Fatalf("followup lost the bridged custom alias %q: %s", customAlias, followup)
	}
	if !strings.Contains(followup, `"type":"function_call_output"`) {
		t.Fatalf("followup did not carry the tool result as a function output: %s", followup)
	}
	if strings.Contains(followup, "previous_response_id") {
		t.Fatalf("followup left the opaque response id unresolved: %s", followup)
	}
}
