package pluginhost

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRPCCapabilitiesIncludeFrontendAuthProviderExclusive(t *testing.T) {
	plugin := pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			FrontendAuthProvider:          frontendAuthProviderFunc{identifier: "exclusive-auth"},
			FrontendAuthProviderExclusive: true,
		},
	}

	caps := rpcCapabilitiesFromPlugin(plugin)
	if !caps.FrontendAuthProvider {
		t.Fatal("FrontendAuthProvider = false, want true")
	}
	if !caps.FrontendAuthProviderExclusive {
		t.Fatal("FrontendAuthProviderExclusive = false, want true")
	}

	raw, errMarshal := json.Marshal(caps)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	if !json.Valid(raw) {
		t.Fatalf("marshaled capabilities are invalid JSON: %s", raw)
	}
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if decoded["frontend_auth_provider_exclusive"] != true {
		t.Fatalf("frontend_auth_provider_exclusive = %#v, want true", decoded["frontend_auth_provider_exclusive"])
	}
}

func TestRPCCapabilitiesIncludeScheduler(t *testing.T) {
	plugin := pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{}, nil
			}),
		},
	}

	caps := rpcCapabilitiesFromPlugin(plugin)
	if !caps.Scheduler {
		t.Fatal("Scheduler = false, want true")
	}

	raw, errMarshal := json.Marshal(caps)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	if !json.Valid(raw) {
		t.Fatalf("marshaled capabilities are invalid JSON: %s", raw)
	}
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if decoded["scheduler"] != true {
		t.Fatalf("scheduler = %#v, want true", decoded["scheduler"])
	}
}

func TestRPCCapabilitiesIncludeServerToolHandler(t *testing.T) {
	plugin := pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			ServerToolHandler: serverToolHandlerFunc{
				handle: func(context.Context, pluginapi.ServerToolRequest) (pluginapi.ServerToolResponse, error) {
					return pluginapi.ServerToolResponse{}, nil
				},
				handleStream: func(context.Context, pluginapi.ServerToolRequest) (pluginapi.ServerToolStreamResponse, error) {
					return pluginapi.ServerToolStreamResponse{}, nil
				},
			},
		},
	}

	caps := rpcCapabilitiesFromPlugin(plugin)
	if !caps.ServerToolHandler {
		t.Fatal("ServerToolHandler = false, want true")
	}

	raw, errMarshal := json.Marshal(caps)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	if !json.Valid(raw) {
		t.Fatalf("marshaled capabilities are invalid JSON: %s", raw)
	}
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if decoded["server_tool_handler"] != true {
		t.Fatalf("server_tool_handler = %#v, want true", decoded["server_tool_handler"])
	}
}

func TestRPCSchedulerPickUsesAdapter(t *testing.T) {
	var pickCalls int
	var gotReq pluginapi.SchedulerPickRequest
	lookup := newTestSymbolLookup(&testPlugin{
		registerResult: pluginapi.Plugin{
			Metadata: pluginapi.Metadata{
				Name:             "scheduler",
				Version:          "1.0.0",
				Author:           "test",
				GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			},
			Capabilities: pluginapi.Capabilities{
				Scheduler: schedulerFunc(func(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					pickCalls++
					gotReq = req
					return pluginapi.SchedulerPickResponse{
						AuthID:  "auth-2",
						Handled: true,
					}, nil
				}),
			},
		},
	})

	plugin, errRegister := registerRPCPlugin(context.Background(), nil, "scheduler", lookup, pluginabi.MethodPluginRegister, nil)
	if errRegister != nil {
		t.Fatalf("registerRPCPlugin() error = %v", errRegister)
	}
	if plugin.Capabilities.Scheduler == nil {
		t.Fatal("Scheduler = nil, want adapter")
	}

	req := pluginapi.SchedulerPickRequest{
		Provider:  "openai",
		Providers: []string{"openai", "codex"},
		Model:     "gpt-5.4",
		Stream:    true,
		Options: pluginapi.SchedulerOptions{
			Headers: map[string][]string{"X-Test": {"one", "two"}},
		},
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{
				ID:         "auth-1",
				Provider:   "openai",
				Priority:   10,
				Status:     "ready",
				Attributes: map[string]string{"region": "us"},
			},
			{
				ID:         "auth-2",
				Provider:   "codex",
				Priority:   20,
				Status:     "ready",
				Attributes: map[string]string{"region": "eu"},
			},
		},
	}
	resp, errPick := plugin.Capabilities.Scheduler.Pick(context.Background(), req)
	if errPick != nil {
		t.Fatalf("Scheduler.Pick() error = %v", errPick)
	}
	if resp.AuthID != "auth-2" || !resp.Handled {
		t.Fatalf("Scheduler.Pick() response = %#v, want auth-2 handled", resp)
	}
	if pickCalls != 1 {
		t.Fatalf("scheduler pick calls = %d, want 1", pickCalls)
	}
	if gotReq.Provider != req.Provider || !reflect.DeepEqual(gotReq.Providers, req.Providers) ||
		gotReq.Model != req.Model || gotReq.Stream != req.Stream {
		t.Fatalf("scheduler request main fields = %#v, want %#v", gotReq, req)
	}
	if !reflect.DeepEqual(gotReq.Options.Headers, req.Options.Headers) {
		t.Fatalf("scheduler request headers = %#v, want %#v", gotReq.Options.Headers, req.Options.Headers)
	}
	if len(gotReq.Candidates) != len(req.Candidates) {
		t.Fatalf("scheduler candidates len = %d, want %d", len(gotReq.Candidates), len(req.Candidates))
	}
	for index := range req.Candidates {
		gotCandidate := gotReq.Candidates[index]
		wantCandidate := req.Candidates[index]
		if gotCandidate.ID != wantCandidate.ID ||
			gotCandidate.Provider != wantCandidate.Provider ||
			gotCandidate.Priority != wantCandidate.Priority ||
			gotCandidate.Status != wantCandidate.Status ||
			!reflect.DeepEqual(gotCandidate.Attributes, wantCandidate.Attributes) {
			t.Fatalf("scheduler candidate[%d] = %#v, want %#v", index, gotCandidate, wantCandidate)
		}
	}
}

func TestRPCServerToolHandleUsesAdapter(t *testing.T) {
	var calls int
	var gotReq pluginapi.ServerToolRequest
	lookup := newTestSymbolLookup(&testPlugin{
		registerResult: pluginapi.Plugin{
			Metadata: pluginapi.Metadata{
				Name:             "server-tool",
				Version:          "1.0.0",
				Author:           "test",
				GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			},
			Capabilities: pluginapi.Capabilities{
				ServerToolHandler: serverToolHandlerFunc{
					handle: func(ctx context.Context, req pluginapi.ServerToolRequest) (pluginapi.ServerToolResponse, error) {
						calls++
						gotReq = req
						return pluginapi.ServerToolResponse{
							Handled: true,
							Headers: map[string][]string{"Content-Type": {"application/json"}},
							Payload: []byte(`{"ok":true}`),
							Metadata: map[string]any{
								"handled_by": "rpc-test",
							},
						}, nil
					},
					handleStream: func(context.Context, pluginapi.ServerToolRequest) (pluginapi.ServerToolStreamResponse, error) {
						return pluginapi.ServerToolStreamResponse{}, nil
					},
				},
			},
		},
	})

	plugin, errRegister := registerRPCPlugin(context.Background(), New(), "server-tool", lookup, pluginabi.MethodPluginRegister, nil)
	if errRegister != nil {
		t.Fatalf("registerRPCPlugin() error = %v", errRegister)
	}
	if plugin.Capabilities.ServerToolHandler == nil {
		t.Fatal("ServerToolHandler = nil, want adapter")
	}

	req := pluginapi.ServerToolRequest{
		Provider:        "antigravity",
		AuthID:          "auth-1",
		AuthProvider:    "antigravity",
		RouteModel:      "gemini-3.5-flash",
		UpstreamModel:   "gemini-3.5-flash",
		SourceFormat:    "anthropic",
		Stream:          true,
		Headers:         map[string][]string{"Anthropic-Version": {"2023-06-01"}},
		OriginalRequest: []byte(`{"tools":[{"type":"web_search_20250305"}]}`),
		Payload:         []byte(`{"tools":[{"type":"web_search_20250305"}]}`),
		Metadata:        map[string]any{"request_id": "req-1"},
	}
	resp, errHandle := plugin.Capabilities.ServerToolHandler.HandleServerTool(context.Background(), req)
	if errHandle != nil {
		t.Fatalf("HandleServerTool() error = %v", errHandle)
	}
	if !resp.Handled || string(resp.Payload) != `{"ok":true}` || resp.Metadata["handled_by"] != "rpc-test" {
		t.Fatalf("HandleServerTool() response = %#v", resp)
	}
	if calls != 1 {
		t.Fatalf("server tool calls = %d, want 1", calls)
	}
	if gotReq.Provider != req.Provider || gotReq.AuthID != req.AuthID || gotReq.RouteModel != req.RouteModel ||
		gotReq.UpstreamModel != req.UpstreamModel || gotReq.SourceFormat != req.SourceFormat || !gotReq.Stream {
		t.Fatalf("server tool request main fields = %#v, want %#v", gotReq, req)
	}
	if !reflect.DeepEqual(gotReq.Headers, req.Headers) {
		t.Fatalf("server tool request headers = %#v, want %#v", gotReq.Headers, req.Headers)
	}
}

func TestRPCServerToolHandleStreamUsesAdapter(t *testing.T) {
	lookup := newTestSymbolLookup(&testPlugin{
		registerResult: pluginapi.Plugin{
			Metadata: pluginapi.Metadata{
				Name:             "server-tool",
				Version:          "1.0.0",
				Author:           "test",
				GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			},
			Capabilities: pluginapi.Capabilities{
				ServerToolHandler: serverToolHandlerFunc{
					handle: func(context.Context, pluginapi.ServerToolRequest) (pluginapi.ServerToolResponse, error) {
						return pluginapi.ServerToolResponse{}, nil
					},
					handleStream: func(context.Context, pluginapi.ServerToolRequest) (pluginapi.ServerToolStreamResponse, error) {
						chunks := make(chan pluginapi.ServerToolStreamChunk, 2)
						chunks <- pluginapi.ServerToolStreamChunk{Payload: []byte("event: message_start\n\n")}
						chunks <- pluginapi.ServerToolStreamChunk{Payload: []byte("event: message_stop\n\n")}
						close(chunks)
						return pluginapi.ServerToolStreamResponse{
							Handled: true,
							Headers: map[string][]string{"Content-Type": {"text/event-stream"}},
							Chunks:  chunks,
							Metadata: map[string]any{
								"streamed": true,
							},
						}, nil
					},
				},
			},
		},
	})

	plugin, errRegister := registerRPCPlugin(context.Background(), New(), "server-tool", lookup, pluginabi.MethodPluginRegister, nil)
	if errRegister != nil {
		t.Fatalf("registerRPCPlugin() error = %v", errRegister)
	}
	resp, errHandle := plugin.Capabilities.ServerToolHandler.HandleServerToolStream(context.Background(), pluginapi.ServerToolRequest{
		Provider:     "antigravity",
		SourceFormat: "anthropic",
		Stream:       true,
	})
	if errHandle != nil {
		t.Fatalf("HandleServerToolStream() error = %v", errHandle)
	}
	if !resp.Handled || resp.Headers.Get("Content-Type") != "text/event-stream" || resp.Metadata["streamed"] != true {
		t.Fatalf("HandleServerToolStream() response = %#v", resp)
	}
	var payload string
	for chunk := range resp.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		payload += string(chunk.Payload)
	}
	if payload != "event: message_start\n\nevent: message_stop\n\n" {
		t.Fatalf("stream payload = %q", payload)
	}
}

func TestSanitizePluginRequestScheduler(t *testing.T) {
	req := pluginapi.SchedulerPickRequest{
		Provider:  "openai",
		Providers: []string{"openai", "codex"},
		Model:     "gpt-5.4",
		Stream:    true,
		Options: pluginapi.SchedulerOptions{
			Headers: map[string][]string{"X-Test": {"one", "two"}},
			Metadata: map[string]any{
				"keep": "value",
				"drop": make(chan struct{}),
			},
		},
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{
				ID:         "auth-1",
				Provider:   "openai",
				Priority:   10,
				Status:     "ready",
				Attributes: map[string]string{"region": "us"},
				Metadata: map[string]any{
					"keep": "candidate",
					"drop": make(chan struct{}),
				},
			},
		},
	}

	raw, errMarshal := json.Marshal(sanitizePluginRequest(req))
	if errMarshal != nil {
		t.Fatalf("Marshal(sanitized scheduler request) error = %v", errMarshal)
	}
	var decoded pluginapi.SchedulerPickRequest
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("Unmarshal(sanitized scheduler request) error = %v", errUnmarshal)
	}

	if decoded.Provider != req.Provider || !reflect.DeepEqual(decoded.Providers, req.Providers) ||
		decoded.Model != req.Model || decoded.Stream != req.Stream {
		t.Fatalf("scheduler request main fields = %#v, want %#v", decoded, req)
	}
	if !reflect.DeepEqual(decoded.Options.Headers, req.Options.Headers) {
		t.Fatalf("scheduler request headers = %#v, want %#v", decoded.Options.Headers, req.Options.Headers)
	}
	if decoded.Options.Metadata["keep"] != "value" {
		t.Fatalf("scheduler options metadata keep = %#v, want value", decoded.Options.Metadata["keep"])
	}
	if _, ok := decoded.Options.Metadata["drop"]; ok {
		t.Fatalf("scheduler options metadata drop survived sanitize: %#v", decoded.Options.Metadata)
	}
	if len(decoded.Candidates) != 1 {
		t.Fatalf("scheduler candidates len = %d, want 1", len(decoded.Candidates))
	}
	gotCandidate := decoded.Candidates[0]
	wantCandidate := req.Candidates[0]
	if gotCandidate.ID != wantCandidate.ID ||
		gotCandidate.Provider != wantCandidate.Provider ||
		gotCandidate.Priority != wantCandidate.Priority ||
		gotCandidate.Status != wantCandidate.Status ||
		!reflect.DeepEqual(gotCandidate.Attributes, wantCandidate.Attributes) {
		t.Fatalf("scheduler candidate = %#v, want %#v", gotCandidate, wantCandidate)
	}
	if gotCandidate.Metadata["keep"] != "candidate" {
		t.Fatalf("scheduler candidate metadata keep = %#v, want candidate", gotCandidate.Metadata["keep"])
	}
	if _, ok := gotCandidate.Metadata["drop"]; ok {
		t.Fatalf("scheduler candidate metadata drop survived sanitize: %#v", gotCandidate.Metadata)
	}
}

func TestSanitizePluginRequestServerTool(t *testing.T) {
	req := pluginapi.ServerToolRequest{
		Provider:      "antigravity",
		AuthID:        "auth-1",
		RouteModel:    "gemini-3.5-flash",
		UpstreamModel: "gemini-3.5-flash",
		SourceFormat:  "anthropic",
		Stream:        true,
		Metadata: map[string]any{
			"keep": "request",
			"drop": make(chan struct{}),
		},
		AuthMetadata: map[string]any{
			"project_id": "project-1",
			"drop":       make(chan struct{}),
		},
		HTTPClient: noOpHostHTTPClient{},
	}

	raw, errMarshal := json.Marshal(sanitizePluginRequest(req))
	if errMarshal != nil {
		t.Fatalf("Marshal(sanitized server tool request) error = %v", errMarshal)
	}
	if string(raw) == "" || jsonContains(raw, "HTTPClient") || jsonContains(raw, "http_client") {
		t.Fatalf("sanitized server tool request leaked HTTP client: %s", raw)
	}
	var decoded pluginapi.ServerToolRequest
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("Unmarshal(sanitized server tool request) error = %v", errUnmarshal)
	}
	if decoded.Provider != req.Provider || decoded.AuthID != req.AuthID ||
		decoded.RouteModel != req.RouteModel || decoded.UpstreamModel != req.UpstreamModel ||
		decoded.SourceFormat != req.SourceFormat || !decoded.Stream {
		t.Fatalf("server tool request main fields = %#v, want %#v", decoded, req)
	}
	if decoded.Metadata["keep"] != "request" {
		t.Fatalf("server tool metadata keep = %#v, want request", decoded.Metadata["keep"])
	}
	if _, ok := decoded.Metadata["drop"]; ok {
		t.Fatalf("server tool metadata drop survived sanitize: %#v", decoded.Metadata)
	}
	if decoded.AuthMetadata["project_id"] != "project-1" {
		t.Fatalf("server tool auth metadata project_id = %#v, want project-1", decoded.AuthMetadata["project_id"])
	}
	if _, ok := decoded.AuthMetadata["drop"]; ok {
		t.Fatalf("server tool auth metadata drop survived sanitize: %#v", decoded.AuthMetadata)
	}
}

func jsonContains(raw []byte, needle string) bool {
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		return false
	}
	_, ok := decoded[needle]
	return ok
}

type noOpHostHTTPClient struct{}

func (noOpHostHTTPClient) Do(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return pluginapi.HTTPResponse{}, nil
}

func (noOpHostHTTPClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, nil
}
