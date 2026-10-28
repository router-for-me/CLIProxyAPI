package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
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
	if decoded["frontend_auth_provider_omit_body"] != nil && decoded["frontend_auth_provider_omit_body"] != false {
		t.Fatalf("frontend_auth_provider_omit_body = %#v, want omitted or false", decoded["frontend_auth_provider_omit_body"])
	}
	if decoded["frontend_auth_omit_body"] != nil && decoded["frontend_auth_omit_body"] != false {
		t.Fatalf("frontend_auth_omit_body = %#v, want omitted or false", decoded["frontend_auth_omit_body"])
	}
}

func TestRPCCapabilitiesIncludeFrontendAuthProviderOmitBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps pluginapi.Capabilities
	}{
		{
			name: "FrontendAuthProviderOmitBody",
			caps: pluginapi.Capabilities{
				FrontendAuthProvider:         frontendAuthProviderFunc{identifier: "omit-auth"},
				FrontendAuthProviderOmitBody: true,
			},
		},
		{
			name: "FrontendAuthOmitBody",
			caps: pluginapi.Capabilities{
				FrontendAuthProvider: frontendAuthProviderFunc{identifier: "omit-auth"},
				FrontendAuthOmitBody: true,
			},
		},
		{
			name: "method hook",
			caps: pluginapi.Capabilities{
				FrontendAuthProvider: omitBodyFrontendAuthProvider{
					frontendAuthProviderFunc: frontendAuthProviderFunc{identifier: "omit-auth"},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plugin := pluginapi.Plugin{Capabilities: tc.caps}
			caps := rpcCapabilitiesFromPlugin(plugin)
			if !caps.FrontendAuthProvider {
				t.Fatal("FrontendAuthProvider = false, want true")
			}
			if !caps.FrontendAuthProviderOmitBody || !caps.FrontendAuthOmitBody {
				t.Fatalf("omit body flags = provider:%v short:%v, want both true", caps.FrontendAuthProviderOmitBody, caps.FrontendAuthOmitBody)
			}
			raw, errMarshal := json.Marshal(caps)
			if errMarshal != nil {
				t.Fatalf("Marshal() error = %v", errMarshal)
			}
			var decoded map[string]any
			if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
				t.Fatalf("Unmarshal() error = %v", errUnmarshal)
			}
			if decoded["frontend_auth_provider_omit_body"] != true {
				t.Fatalf("frontend_auth_provider_omit_body = %#v, want true", decoded["frontend_auth_provider_omit_body"])
			}
			if decoded["frontend_auth_omit_body"] != true {
				t.Fatalf("frontend_auth_omit_body = %#v, want true", decoded["frontend_auth_omit_body"])
			}
		})
	}
}

func TestRPCFrontendAuthOmitsBodyOnTheWireForSchema7(t *testing.T) {
	for _, tc := range []struct {
		name          string
		schemaVersion uint32
		omitBody      bool
		shortFlagOnly bool
		wantBodyLen   int
	}{
		{name: "schema 7 omit body", schemaVersion: pluginabi.SchemaVersionFrontendAuthOmitBody, omitBody: true, wantBodyLen: 0},
		{name: "schema 7 short flag omit body", schemaVersion: pluginabi.SchemaVersionFrontendAuthOmitBody, omitBody: true, shortFlagOnly: true, wantBodyLen: 0},
		{name: "schema 7 default includes body", schemaVersion: pluginabi.SchemaVersionFrontendAuthOmitBody, omitBody: false, wantBodyLen: len("request")},
		{name: "schema 6 ignores omit body", schemaVersion: 6, omitBody: true, wantBodyLen: len("request")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const key = "plugin:auth-wire:quota"
			sdkaccess.UnregisterProvider(key)
			defer sdkaccess.UnregisterProvider(key)

			var wireGot pluginapi.FrontendAuthRequest
			var wireCalls int
			plugin := validTestPlugin("auth-wire")
			plugin.Capabilities.FrontendAuthProvider = frontendAuthProviderFunc{
				identifier: "quota",
				authenticate: func(_ context.Context, req pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
					wireCalls++
					wireGot = req
					return pluginapi.FrontendAuthResponse{Authenticated: true, Principal: "wire-user"}, nil
				},
			}
			if tc.omitBody && !tc.shortFlagOnly {
				plugin.Capabilities.FrontendAuthProviderOmitBody = true
			}
			lookup := newTestSymbolLookup(&testPlugin{registerResult: plugin})
			lookup.schemaVersion = tc.schemaVersion

			var client pluginClient = lookup
			if tc.shortFlagOnly {
				client = &shortFrontendAuthOmitBodyClient{inner: lookup}
			}

			registered, errRegister := registerRPCPlugin(context.Background(), nil, "auth-wire", client, pluginabi.MethodPluginRegister, nil)
			if errRegister != nil {
				t.Fatalf("registerRPCPlugin() error = %v", errRegister)
			}
			if registered.SchemaVersion != tc.schemaVersion {
				t.Fatalf("registered SchemaVersion = %d, want %d", registered.SchemaVersion, tc.schemaVersion)
			}
			if registered.Capabilities.FrontendAuthProviderOmitBody != tc.omitBody || registered.Capabilities.FrontendAuthOmitBody != tc.omitBody {
				t.Fatalf("registered omit flags = provider:%v short:%v, want %v", registered.Capabilities.FrontendAuthProviderOmitBody, registered.Capabilities.FrontendAuthOmitBody, tc.omitBody)
			}

			host := newHostWithRecords(capabilityRecord{id: "auth-wire", plugin: registered})
			host.RegisterFrontendAuthProviders()

			var provider sdkaccess.Provider
			for _, candidate := range sdkaccess.RegisteredProviders() {
				if candidate.Identifier() == key {
					provider = candidate
					break
				}
			}
			if provider == nil {
				t.Fatalf("provider %q was not registered", key)
			}

			counter := &countingAuthBodyReader{reader: strings.NewReader("request")}
			req, errNewReq := http.NewRequest(http.MethodPost, "http://example.test/v1/responses", io.NopCloser(counter))
			if errNewReq != nil {
				t.Fatalf("NewRequest() error = %v", errNewReq)
			}
			req.Header.Set("Authorization", "Bearer wire-token")
			res, authErr := provider.Authenticate(context.Background(), req)
			if authErr != nil {
				t.Fatalf("Authenticate() error = %v, want nil", authErr)
			}
			if res == nil || res.Principal != "wire-user" {
				t.Fatalf("Authenticate() result = %#v, want wire-user", res)
			}
			if wireCalls != 1 {
				t.Fatalf("wire calls = %d, want 1", wireCalls)
			}
			if len(wireGot.Body) != tc.wantBodyLen {
				t.Fatalf("wire body len = %d, want %d", len(wireGot.Body), tc.wantBodyLen)
			}
			if counter.readBytes != tc.wantBodyLen {
				t.Fatalf("counter.readBytes = %d, want %d", counter.readBytes, tc.wantBodyLen)
			}
		})
	}
}

type shortFrontendAuthOmitBodyClient struct {
	inner pluginClient
}

func (c *shortFrontendAuthOmitBodyClient) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	if method == pluginabi.MethodPluginRegister || method == pluginabi.MethodPluginReconfigure {
		if _, errCall := c.inner.Call(ctx, method, request); errCall != nil {
			return nil, errCall
		}
		return []byte(`{"ok":true,"result":{"schema_version":7,"metadata":{"Name":"auth-wire","Version":"1.0.0","Author":"test","GitHubRepository":"https://github.com/router-for-me/CLIProxyAPI"},"capabilities":{"frontend_auth_provider":true,"frontend_auth_omit_body":true}}}`), nil
	}
	return c.inner.Call(ctx, method, request)
}

func (c *shortFrontendAuthOmitBodyClient) Shutdown() {
	c.inner.Shutdown()
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
	if decoded["scheduler_across_priorities"] != nil && decoded["scheduler_across_priorities"] != false {
		t.Fatalf("scheduler_across_priorities = %#v, want omitted or false", decoded["scheduler_across_priorities"])
	}
}

func TestRPCCapabilitiesIncludeSchedulerAcrossPriorities(t *testing.T) {
	plugin := pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{}, nil
			}),
			SchedulerAcrossPriorities: true,
		},
	}

	caps := rpcCapabilitiesFromPlugin(plugin)
	if !caps.Scheduler {
		t.Fatal("Scheduler = false, want true")
	}
	if !caps.SchedulerAcrossPriorities {
		t.Fatal("SchedulerAcrossPriorities = false, want true")
	}

	raw, errMarshal := json.Marshal(caps)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if decoded["scheduler_across_priorities"] != true {
		t.Fatalf("scheduler_across_priorities = %#v, want true", decoded["scheduler_across_priorities"])
	}
}

type testAcrossPrioritiesSchedulerFunc struct {
	schedulerFunc
}

func (testAcrossPrioritiesSchedulerFunc) SchedulerWantsAcrossPriorities() bool {
	return true
}

func TestRPCCapabilitiesIncludeSchedulerAcrossPrioritiesViaMethod(t *testing.T) {
	plugin := pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			Scheduler: testAcrossPrioritiesSchedulerFunc{
				schedulerFunc: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{}, nil
				}),
			},
		},
	}

	caps := rpcCapabilitiesFromPlugin(plugin)
	if !caps.SchedulerAcrossPriorities {
		t.Fatal("SchedulerAcrossPriorities = false, want true")
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

func TestRPCCapabilitiesIncludeRequestInterceptorOmitBody(t *testing.T) {
	plugin := pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			RequestInterceptor: requestInterceptorFunc(func(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
				return pluginapi.RequestInterceptResponse{}, nil
			}),
			RequestInterceptorOmitBodyBefore: true,
			RequestInterceptorOmitBodyAfter:  true,
		},
	}

	caps := rpcCapabilitiesFromPlugin(plugin)
	if !caps.RequestInterceptor {
		t.Fatal("RequestInterceptor = false, want true")
	}
	if !caps.RequestInterceptorOmitBodyBefore || !caps.RequestInterceptorOmitBodyAfter {
		t.Fatal("RequestInterceptorOmitBody flags not propagated")
	}

	raw, errMarshal := json.Marshal(caps)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if decoded["request_interceptor_omit_body_before"] != true {
		t.Fatalf("request_interceptor_omit_body_before = %#v, want true", decoded["request_interceptor_omit_body_before"])
	}
	if decoded["request_interceptor_omit_body_after"] != true {
		t.Fatalf("request_interceptor_omit_body_after = %#v, want true", decoded["request_interceptor_omit_body_after"])
	}
}

func TestRPCRequestInterceptorOmitsBodyOnTheWireForSchema7(t *testing.T) {
	for _, tc := range []struct {
		name       string
		omitBefore bool
		omitAfter  bool
		wantBefore int
		wantAfter  int
	}{
		{name: "both phases", omitBefore: true, omitAfter: true, wantBefore: 0, wantAfter: 0},
		{name: "before only", omitBefore: true, wantBefore: 0, wantAfter: len("request")},
		{name: "after only", omitAfter: true, wantBefore: len("request"), wantAfter: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &phaseRecordingInterceptor{}
			plugin := validTestPlugin("omit-wire")
			plugin.Capabilities.RequestInterceptor = recorder
			plugin.Capabilities.RequestInterceptorOmitBodyBefore = tc.omitBefore
			plugin.Capabilities.RequestInterceptorOmitBodyAfter = tc.omitAfter
			lookup := newTestSymbolLookup(&testPlugin{registerResult: plugin})
			lookup.schemaVersion = pluginabi.SchemaVersionRequestInterceptorOmitBody

			registered, errRegister := registerRPCPlugin(context.Background(), nil, "omit-wire", lookup, pluginabi.MethodPluginRegister, nil)
			if errRegister != nil {
				t.Fatalf("registerRPCPlugin() error = %v", errRegister)
			}
			if registered.SchemaVersion != pluginabi.SchemaVersionRequestInterceptorOmitBody {
				t.Fatalf("registered SchemaVersion = %d, want %d", registered.SchemaVersion, pluginabi.SchemaVersionRequestInterceptorOmitBody)
			}
			if registered.Capabilities.RequestInterceptorOmitBodyBefore != tc.omitBefore || registered.Capabilities.RequestInterceptorOmitBodyAfter != tc.omitAfter {
				t.Fatalf("registered omit flags = before:%v after:%v, want before:%v after:%v", registered.Capabilities.RequestInterceptorOmitBodyBefore, registered.Capabilities.RequestInterceptorOmitBodyAfter, tc.omitBefore, tc.omitAfter)
			}
			host := newHostWithRecords(capabilityRecord{id: "omit-wire", plugin: registered})
			_ = host.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{RequestID: "req-before", Body: []byte("request")})
			if recorder.beforeCalls != 1 {
				t.Fatalf("wire before calls = %d, want 1", recorder.beforeCalls)
			}
			if recorder.beforeGot.RequestID != "req-before" {
				t.Fatalf("wire before RequestID = %q, want req-before", recorder.beforeGot.RequestID)
			}
			if len(recorder.beforeGot.Body) != tc.wantBefore {
				t.Fatalf("wire before body len = %d, want %d", len(recorder.beforeGot.Body), tc.wantBefore)
			}
			_ = host.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{RequestID: "req-after", Body: []byte("request")})
			if recorder.afterCalls != 1 {
				t.Fatalf("wire after calls = %d, want 1", recorder.afterCalls)
			}
			if recorder.afterGot.RequestID != "req-after" {
				t.Fatalf("wire after RequestID = %q, want req-after", recorder.afterGot.RequestID)
			}
			if len(recorder.afterGot.Body) != tc.wantAfter {
				t.Fatalf("wire after body len = %d, want %d", len(recorder.afterGot.Body), tc.wantAfter)
			}
		})
	}
}

type phaseRecordingInterceptor struct {
	beforeGot   pluginapi.RequestInterceptRequest
	afterGot    pluginapi.RequestInterceptRequest
	beforeCalls int
	afterCalls  int
}

func (p *phaseRecordingInterceptor) InterceptRequestBeforeAuth(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	p.beforeCalls++
	p.beforeGot = req
	return pluginapi.RequestInterceptResponse{}, nil
}

func (p *phaseRecordingInterceptor) InterceptRequestAfterAuth(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	p.afterCalls++
	p.afterGot = req
	return pluginapi.RequestInterceptResponse{}, nil
}

func TestRPCCapabilitiesIncludeQuotaProvider(t *testing.T) {
	plugin := pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			QuotaProvider: &testQuotaProvider{identifier: "quota-test"},
		},
	}

	caps := rpcCapabilitiesFromPlugin(plugin)
	if !caps.QuotaProvider {
		t.Fatal("QuotaProvider = false, want true")
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
	if decoded["quota_provider"] != true {
		t.Fatalf("quota_provider = %#v, want true", decoded["quota_provider"])
	}
}

func TestRegisterRPCPluginSendsHostSchemaVersion(t *testing.T) {
	lookup := newTestSymbolLookup(&testPlugin{
		registerResult: validTestPlugin("schema"),
	})

	registered, errRegister := registerRPCPlugin(context.Background(), nil, "schema", lookup, pluginabi.MethodPluginRegister, []byte("mode: test"))
	if errRegister != nil {
		t.Fatalf("registerRPCPlugin() error = %v", errRegister)
	}
	if lookup.lastLifecycle.SchemaVersion != pluginabi.SchemaVersion {
		t.Fatalf("lifecycle schema_version = %d, want %d", lookup.lastLifecycle.SchemaVersion, pluginabi.SchemaVersion)
	}
	if registered.SchemaVersion != pluginabi.SchemaVersion {
		t.Fatalf("registered SchemaVersion = %d, want %d", registered.SchemaVersion, pluginabi.SchemaVersion)
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
	if registered.SchemaVersion != 1 {
		t.Fatalf("registered SchemaVersion = %d, want 1", registered.SchemaVersion)
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

func TestRPCSchedulerAndUsageSelectionIDCompatibility(t *testing.T) {
	pickReq := pluginapi.SchedulerPickRequest{
		Provider: "openai",
		Model:    "gpt-5.4",
		Options: pluginapi.SchedulerOptions{
			Metadata: map[string]any{
				pluginapi.SelectionIDMetadataKey: "sel-rpc-123",
			},
		},
	}
	rawPick, errMarshalPick := json.Marshal(sanitizePluginRequest(pickReq))
	if errMarshalPick != nil {
		t.Fatalf("Marshal(SchedulerPickRequest) error = %v", errMarshalPick)
	}
	var decodedPick pluginapi.SchedulerPickRequest
	if errUnmarshalPick := json.Unmarshal(rawPick, &decodedPick); errUnmarshalPick != nil {
		t.Fatalf("Unmarshal(SchedulerPickRequest) error = %v", errUnmarshalPick)
	}
	if got := decodedPick.Options.Metadata[pluginapi.SelectionIDMetadataKey]; got != "sel-rpc-123" {
		t.Fatalf("decodedPick selection_id = %#v, want %q", got, "sel-rpc-123")
	}

	usageRec := pluginapi.UsageRecord{
		Provider:    "openai",
		Model:       "gpt-5.4",
		RequestID:   "req-1",
		TraceID:     "trace-1",
		SelectionID: "sel-rpc-123",
	}
	rawUsage, errMarshalUsage := json.Marshal(usageRec)
	if errMarshalUsage != nil {
		t.Fatalf("Marshal(UsageRecord) error = %v", errMarshalUsage)
	}
	var decodedUsage pluginapi.UsageRecord
	if errUnmarshalUsage := json.Unmarshal(rawUsage, &decodedUsage); errUnmarshalUsage != nil {
		t.Fatalf("Unmarshal(UsageRecord) error = %v", errUnmarshalUsage)
	}
	if decodedUsage.SelectionID != "sel-rpc-123" {
		t.Fatalf("decodedUsage.SelectionID = %q, want %q", decodedUsage.SelectionID, "sel-rpc-123")
	}

	// Old plugins compiled against a legacy UsageRecord without SelectionID must still unmarshal cleanly.
	type legacyUsageRecord struct {
		Provider  string
		Model     string
		RequestID string
		TraceID   string
	}
	var legacy legacyUsageRecord
	if errLegacy := json.Unmarshal(rawUsage, &legacy); errLegacy != nil {
		t.Fatalf("Unmarshal(legacyUsageRecord) error = %v", errLegacy)
	}
	if legacy.Provider != "openai" || legacy.RequestID != "req-1" || legacy.TraceID != "trace-1" {
		t.Fatalf("legacyUsageRecord = %+v", legacy)
	}
}

type recordingRawPatchClient struct {
	schemaVersion uint32
	methods       []string
	rawRequests   [][]byte
	rawRequestPtr []uintptr
	patchFunc     func(method string, rawBody []byte) (pluginapi.RequestBodyPatchResponse, error)
}

func (c *recordingRawPatchClient) Call(_ context.Context, method string, request []byte) ([]byte, error) {
	c.methods = append(c.methods, method)
	c.rawRequests = append(c.rawRequests, bytes.Clone(request))
	if len(request) > 0 {
		c.rawRequestPtr = append(c.rawRequestPtr, uintptr(reflect.ValueOf(request).Pointer()))
	} else {
		c.rawRequestPtr = append(c.rawRequestPtr, 0)
	}
	switch method {
	case pluginabi.MethodPluginRegister:
		return marshalRPCResult(rpcRegistration{
			Metadata: pluginapi.Metadata{
				Name:             "Timezone Patcher",
				Version:          "1.0.0",
				Author:           "test",
				GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			},
			SchemaVersion: c.schemaVersion,
			Capabilities: rpcCapabilities{
				RequestInterceptorPatchBodyBefore: true,
				RequestInterceptorPatchBodyAfter:  true,
			},
		})
	case pluginabi.MethodRequestPatchBodyBefore, pluginabi.MethodRequestPatchBodyAfter:
		resp, err := c.patchFunc(method, request)
		if err != nil {
			return nil, err
		}
		return marshalRPCResult(resp)
	case pluginabi.MethodRequestInterceptBefore, pluginabi.MethodRequestInterceptAfter:
		var req pluginapi.RequestInterceptRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, err
		}
		return marshalRPCResult(pluginapi.RequestInterceptResponse{
			Body: bytes.ReplaceAll(req.Body, []byte("<timezone>UTC</timezone>"), []byte("<timezone>Legacy</timezone>")),
		})
	default:
		return marshalRPCResult(struct{}{})
	}
}

func (c *recordingRawPatchClient) Shutdown()    {}
func (c *recordingRawPatchClient) Close() error { return nil }

func TestRPCRequestBodyPatchRawCABIAndSchemaVersionGate(t *testing.T) {
	rawInput := []byte(`{"model":"gpt-5","input":[{"role":"developer","content":[{"type":"input_text","text":"<environment_context>\n  <current_date>2026-04-19</current_date>\n  <timezone>UTC</timezone>\n</environment_context>"}]}]}`)

	clientV8 := &recordingRawPatchClient{
		schemaVersion: pluginabi.SchemaVersionRequestInterceptorBodyPatch,
		patchFunc: func(method string, rawBody []byte) (pluginapi.RequestBodyPatchResponse, error) {
			if method == pluginabi.MethodRequestPatchBodyBefore {
				target := []byte("<timezone>UTC</timezone>")
				idx := bytes.Index(rawBody, target)
				if idx < 0 {
					return pluginapi.RequestBodyPatchResponse{}, nil
				}
				return pluginapi.RequestBodyPatchResponse{
					Edits: []pluginapi.RequestBodyEdit{
						{
							Start:       idx,
							End:         idx + len(target),
							Replacement: "<timezone>Asia/Shanghai</timezone>",
						},
					},
				}, nil
			}
			return pluginapi.RequestBodyPatchResponse{}, nil
		},
	}

	host := New()
	pluginV8, errReg := registerRPCPlugin(context.Background(), host, "tz-patcher", clientV8, pluginabi.MethodPluginRegister, nil)
	if errReg != nil {
		t.Fatalf("registerRPCPlugin(v8) error = %v", errReg)
	}
	if !validPlugin(pluginV8) {
		t.Fatalf("validPlugin(patch-only pluginV8) = false, want true")
	}
	setHostSnapshotForTest(host, true, capabilityRecord{id: "tz-patcher", plugin: pluginV8})

	if !host.HasRequestInterceptors() {
		t.Fatal("HasRequestInterceptors() = false, want true for patch-only plugin")
	}

	resp := host.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		Model: "gpt-5",
		Body:  rawInput,
	})
	if len(clientV8.methods) != 2 || clientV8.methods[1] != pluginabi.MethodRequestPatchBodyBefore {
		t.Fatalf("called methods = %v, want [%s %s]", clientV8.methods, pluginabi.MethodPluginRegister, pluginabi.MethodRequestPatchBodyBefore)
	}
	// Crucial requirement 1: C ABI receives the raw request body directly, not a JSON/Base64 envelope,
	// and without a pre-call bytes.Clone on the host side.
	if !bytes.Equal(clientV8.rawRequests[1], rawInput) {
		t.Fatalf("raw C ABI request = %s, want exact rawInput %s", clientV8.rawRequests[1], rawInput)
	}
	if gotPtr, wantPtr := clientV8.rawRequestPtr[1], uintptr(reflect.ValueOf(rawInput).Pointer()); gotPtr != wantPtr {
		t.Fatalf("raw C ABI request slice pointer = %#x, want original rawInput pointer %#x (zero pre-call copy)", gotPtr, wantPtr)
	}
	wantPatched := `{"model":"gpt-5","input":[{"role":"developer","content":[{"type":"input_text","text":"<environment_context>\n  <current_date>2026-04-19</current_date>\n  <timezone>Asia/Shanghai</timezone>\n</environment_context>"}]}]}`
	if string(resp.Body) != wantPatched {
		t.Fatalf("patched body = %s, want %s", resp.Body, wantPatched)
	}

	// Crucial requirement 3: When plugin returns empty edits, host reuses original body (resp.Body == nil).
	afterResp := host.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		Model: "gpt-5",
		Body:  rawInput,
	})
	if afterResp.Body != nil {
		t.Fatalf("no-op patch returned non-nil Body = %s, want nil to reuse original body", afterResp.Body)
	}

	// Schema v7 must ignore request_interceptor_patch_body_before.
	clientV7 := &recordingRawPatchClient{
		schemaVersion: pluginabi.SchemaVersionRequestInterceptorOmitBody,
		patchFunc: func(string, []byte) (pluginapi.RequestBodyPatchResponse, error) {
			t.Fatal("patchFunc must not be called on schema v7")
			return pluginapi.RequestBodyPatchResponse{}, nil
		},
	}
	pluginV7, errRegV7 := registerRPCPlugin(context.Background(), host, "tz-patcher-v7", clientV7, pluginabi.MethodPluginRegister, nil)
	if errRegV7 != nil {
		t.Fatalf("registerRPCPlugin(v7) error = %v", errRegV7)
	}
	if pluginV7.Capabilities.RequestBodyPatcherBefore != nil || pluginV7.Capabilities.RequestInterceptorPatchBodyBefore {
		t.Fatalf("schema v7 kept patch capability: %+v", pluginV7.Capabilities)
	}
}
