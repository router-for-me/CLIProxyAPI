package pluginhost

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
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

func TestRPCCapabilitiesIncludeModelRouter(t *testing.T) {
	plugin := pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			ModelRouter: modelRouterFunc(func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
				return pluginapi.ModelRouteResponse{}, nil
			}),
		},
	}

	caps := rpcCapabilitiesFromPlugin(plugin)
	if !caps.ModelRouter {
		t.Fatal("ModelRouter = false, want true")
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
	if decoded["model_router"] != true {
		t.Fatalf("model_router = %#v, want true", decoded["model_router"])
	}
}

func TestRegisterRPCPluginSendsHostSchemaVersion(t *testing.T) {
	lookup := newTestSymbolLookup(&testPlugin{
		registerResult: validTestPlugin("schema"),
	})

	if _, errRegister := registerRPCPlugin(context.Background(), nil, "schema", lookup, pluginabi.MethodPluginRegister, []byte("mode: test")); errRegister != nil {
		t.Fatalf("registerRPCPlugin() error = %v", errRegister)
	}
	if lookup.lastLifecycle.SchemaVersion != pluginabi.SchemaVersion {
		t.Fatalf("lifecycle schema_version = %d, want %d", lookup.lastLifecycle.SchemaVersion, pluginabi.SchemaVersion)
	}
	if string(lookup.lastLifecycle.ConfigYAML) != "mode: test" {
		t.Fatalf("lifecycle config = %q, want input config", lookup.lastLifecycle.ConfigYAML)
	}
}

func TestRegisterRPCPluginRejectsFutureSchemaVersion(t *testing.T) {
	lookup := newTestSymbolLookup(&testPlugin{
		registerResult: validTestPlugin("future-schema"),
	})
	lookup.schemaVersion = pluginabi.SchemaVersion + 1

	_, errRegister := registerRPCPlugin(context.Background(), nil, "future-schema", lookup, pluginabi.MethodPluginRegister, nil)
	if errRegister == nil || !strings.Contains(errRegister.Error(), "schema version") {
		t.Fatalf("registerRPCPlugin() error = %v, want unsupported schema version", errRegister)
	}
}

func TestRegisterRPCPluginAcceptsModelRouterOnSchema1(t *testing.T) {
	plugin := validTestPlugin("router-schema1")
	plugin.Capabilities.ModelRouter = modelRouterFunc(func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
		return pluginapi.ModelRouteResponse{}, nil
	})
	lookup := newTestSymbolLookup(&testPlugin{registerResult: plugin})
	lookup.schemaVersion = 1

	registered, errRegister := registerRPCPlugin(context.Background(), nil, "router-schema1", lookup, pluginabi.MethodPluginRegister, nil)
	if errRegister != nil {
		t.Fatalf("registerRPCPlugin() error = %v, want model_router on schema 1", errRegister)
	}
	if registered.Capabilities.ModelRouter == nil {
		t.Fatal("ModelRouter = nil, want adapter")
	}
}

func TestRPCModelRouteUsesAdapter(t *testing.T) {
	var routeCalls int
	var gotReq pluginapi.ModelRouteRequest
	lookup := newTestSymbolLookup(&testPlugin{
		registerResult: pluginapi.Plugin{
			Metadata: pluginapi.Metadata{
				Name:             "router",
				Version:          "1.0.0",
				Author:           "test",
				GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			},
			Capabilities: pluginapi.Capabilities{
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					routeCalls++
					gotReq = req
					return pluginapi.ModelRouteResponse{
						Handled:    true,
						TargetKind: pluginapi.ModelRouteTargetExecutor,
						Target:     "claude-websearch-plugin",
						Reason:     "typed websearch",
					}, nil
				}),
			},
		},
	})

	plugin, errRegister := registerRPCPlugin(context.Background(), nil, "router", lookup, pluginabi.MethodPluginRegister, nil)
	if errRegister != nil {
		t.Fatalf("registerRPCPlugin() error = %v", errRegister)
	}
	if plugin.Capabilities.ModelRouter == nil {
		t.Fatal("ModelRouter = nil, want adapter")
	}

	req := pluginapi.ModelRouteRequest{
		SourceFormat:   "anthropic",
		RequestedModel: "claude-sonnet",
		Stream:         true,
		Headers:        map[string][]string{"X-Test": {"one", "two"}},
		Query:          map[string][]string{"beta": {"true"}},
		Body:           []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`),
		Metadata: map[string]any{
			"keep": "value",
		},
	}
	resp, errRoute := plugin.Capabilities.ModelRouter.RouteModel(context.Background(), req)
	if errRoute != nil {
		t.Fatalf("ModelRouter.RouteModel() error = %v", errRoute)
	}
	if !resp.Handled || resp.Target != "claude-websearch-plugin" || resp.Reason != "typed websearch" {
		t.Fatalf("ModelRouter.RouteModel() response = %#v", resp)
	}
	if routeCalls != 1 {
		t.Fatalf("route calls = %d, want 1", routeCalls)
	}
	if gotReq.SourceFormat != req.SourceFormat || gotReq.RequestedModel != req.RequestedModel ||
		gotReq.Stream != req.Stream || string(gotReq.Body) != string(req.Body) {
		t.Fatalf("route request main fields = %#v, want %#v", gotReq, req)
	}
	if !reflect.DeepEqual(gotReq.Headers, req.Headers) {
		t.Fatalf("route request headers = %#v, want %#v", gotReq.Headers, req.Headers)
	}
	if !reflect.DeepEqual(gotReq.Query, req.Query) {
		t.Fatalf("route request query = %#v, want %#v", gotReq.Query, req.Query)
	}
	if gotReq.Metadata["keep"] != "value" {
		t.Fatalf("route request metadata = %#v", gotReq.Metadata)
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
