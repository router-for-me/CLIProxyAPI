package pluginhost

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const (
	homePluginID    = "test-alternate-executor"
	homePluginAuth  = "home-credential-a"
	homePluginIndex = homePluginAuth
	homePluginModel = "gemini-2.5-pro"
	homePluginAlias = "acp/" + homePluginModel
)

func homeExecutorRecord(executor pluginapi.ProviderExecutor, providers ...string) capabilityRecord {
	return capabilityRecord{id: homePluginID, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
		Executor:              executor,
		ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
		ExecutorAuthProviders: providers,
		ExecutorInputFormats:  []string{"chat-completions"},
		ExecutorOutputFormats: []string{"chat-completions"},
	}}}
}

func homeExecutorTestAuth(token, project string, expiry time.Time) coreauth.Auth {
	return coreauth.Auth{ID: homePluginAuth, Provider: "antigravity", Status: coreauth.StatusActive,
		Attributes: map[string]string{"source": "home"},
		Metadata:   map[string]any{"type": "antigravity", "access_token": token, "project_id": project, "expired": expiry.UTC().Format(time.RFC3339)}}
}

func homeExecutorDispatchJSON(t *testing.T, auth coreauth.Auth, pluginID string) []byte {
	t.Helper()
	authIndex := auth.ID
	if authIndex == "" {
		authIndex = homePluginIndex
	}
	response := map[string]any{
		"model": homePluginModel, "provider": auth.Provider, "auth_index": authIndex, "auth": auth,
		"request_retry": 0, "concurrency": map[string]any{"accounted": true, "credential_id": auth.ID, "model": homePluginModel},
	}
	if pluginID != "" {
		response["executor_plugin"] = pluginID
	}
	raw, errMarshal := json.Marshal(response)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	return raw
}

type homeExecutorTestTransport func(*http.Request) (*http.Response, error)

func (transport homeExecutorTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

func TestHomePluginDispatchNormalizesHomeOnlyModelThinkingSuffix(t *testing.T) {
	const (
		model    = "home-only-thinking-model"
		provider = "home-only-auth-provider"
	)
	for _, operation := range []string{"execute", "stream", "count"} {
		t.Run(operation, func(t *testing.T) {
			var calls int
			checkRequest := func(req pluginapi.ExecutorRequest) {
				calls++
				if req.Model != model {
					t.Errorf("plugin model = %q, want %q", req.Model, model)
				}
				if got := ojson.GetBytesNoCopy(req.Payload, "model").String(); got != model {
					t.Errorf("payload model = %q, want %q", got, model)
				}
				if got := ojson.GetBytesNoCopy(req.Payload, "reasoning_effort").String(); got != "high" {
					t.Errorf("reasoning_effort = %q, want high; payload=%s", got, req.Payload)
				}
			}
			respond := func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
				checkRequest(req)
				return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}],"input_tokens":3}`)}, nil
			}
			plugin := &fakeExecutor{identifier: "home-only-executor-provider", execute: respond, countTokens: respond,
				executeStream: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
					checkRequest(req)
					chunks := make(chan pluginapi.ExecutorStreamChunk, 1)
					chunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")}
					close(chunks)
					return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
				}}
			host := newHostWithRecords(homeExecutorRecord(plugin, provider))
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
			manager.SetHomePluginExecutorResolver(host)
			payload, errMarshal := json.Marshal(map[string]any{
				"model": model + "(high)", "provider": provider, "executor_plugin": homePluginID, "auth_index": homePluginIndex, "request_retry": 0,
				"auth":       coreauth.Auth{ID: homePluginAuth, Provider: provider, Status: coreauth.StatusActive, Attributes: map[string]string{"source": "home"}},
				"model_info": map[string]any{"id": model, "thinking": map[string]any{"levels": []string{"low", "medium", "high"}}},
			})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			manager.PublishHomeDispatch(&homeExecutorTestDispatcher{payload: payload}, executionregistry.New(), 1)
			req := coreexecutor.Request{Model: model + "(high)", Payload: []byte(`{"model":"` + model + `(high)","messages":[{"role":"user","content":"hi"}]}`)}
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: operation == "stream"}
			switch operation {
			case "execute":
				if _, errExecute := manager.Execute(t.Context(), []string{"home"}, req, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
			case "stream":
				result, errStream := manager.ExecuteStream(t.Context(), []string{"home"}, req, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			case "count":
				if _, errCount := manager.ExecuteCount(t.Context(), []string{"home"}, req, opts); errCount != nil {
					t.Fatal(errCount)
				}
			}
			if calls != 1 {
				t.Fatalf("plugin calls = %d, want 1", calls)
			}
		})
	}
}

func TestHomePluginDispatchUsesSelectedAntigravityCredential(t *testing.T) {
	for _, tt := range []struct {
		operation string
		project   string
		expiresIn time.Duration
		refresh   bool
	}{
		{operation: "execute", expiresIn: time.Hour},
		{operation: "stream", project: "existing-project", expiresIn: time.Minute, refresh: true},
		{operation: "count", project: "existing-project", expiresIn: -time.Minute, refresh: true},
	} {
		t.Run(tt.operation, func(t *testing.T) {
			now := time.Now()
			selected := homeExecutorTestAuth("selected-token", tt.project, now.Add(tt.expiresIn))
			pluginDispatch := homeExecutorDispatchJSON(t, selected, homePluginID)
			fresh := homeExecutorTestAuth("fresh-token", "existing-project", now.Add(time.Hour))
			nativeDispatch := homeExecutorDispatchJSON(t, fresh, "")
			legacyDispatch, errLegacy := json.Marshal(fresh)
			if errLegacy != nil {
				t.Fatal(errLegacy)
			}
			refreshReply, errRefresh := json.Marshal(map[string]any{"auth": fresh, "auth_index": homePluginIndex})
			if errRefresh != nil {
				t.Fatal(errRefresh)
			}
			var refreshes atomic.Int32
			client := newHomeExecutorTestClient(t, func(command, argument string) []byte {
				switch command {
				case "RPOP":
					var dispatch struct {
						Model   string              `json:"model"`
						Plugins map[string][]string `json:"executor_plugins"`
						Headers map[string]string   `json:"headers"`
					}
					if errDecode := json.Unmarshal([]byte(argument), &dispatch); errDecode != nil {
						t.Error(errDecode)
					}
					if len(dispatch.Plugins) != 1 || !slices.Equal(dispatch.Plugins[homePluginID], []string{"antigravity", "other"}) {
						t.Errorf("Home readiness = %v", dispatch.Plugins)
					}
					if dispatch.Model == homePluginAlias {
						return pluginDispatch
					}
					if dispatch.Model != homePluginModel {
						t.Errorf("unexpected Home dispatch model %q", dispatch.Model)
					}
					if dispatch.Headers["x-test-legacy"] == "true" {
						return legacyDispatch
					}
					return nativeDispatch
				case "GET":
					if strings.HasPrefix(argument, "cpa:antigravity:reasoning-replay:") {
						return nil
					}
					refreshes.Add(1)
					if ojson.Get(argument, "type").String() != "refresh" || ojson.Get(argument, "auth_index").String() != homePluginIndex ||
						ojson.Get(argument, "access_token_sha256").String() != coreauth.AccessTokenSHA256(&selected) {
						t.Errorf("unexpected Home refresh request %s", argument)
					}
					return refreshReply
				default:
					t.Errorf("unexpected Home command %s", command)
					return nil
				}
			})
			var generations, discoveries atomic.Int32
			transport := homeExecutorTestTransport(func(req *http.Request) (*http.Response, error) {
				var body string
				switch {
				case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
					discoveries.Add(1)
					if req.Header.Get("Authorization") != "Bearer selected-token" {
						t.Error("unexpected project discovery credential")
					}
					body = `{"cloudaicompanionProject":"discovered-project"}`
				case strings.HasSuffix(req.URL.Path, ":generateContent"):
					generations.Add(1)
					if req.Header.Get("Authorization") != "Bearer fresh-token" {
						t.Error("unexpected native generation credential")
					}
					body = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"native"}]},"finishReason":"STOP"}]}}`
				default:
					return nil, fmt.Errorf("unexpected Antigravity request %s", req.URL.Path)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
			var calls []string
			checkRequest := func(operation string, req pluginapi.ExecutorRequest) {
				calls = append(calls, operation)
				wantToken, wantProject := "selected-token", "discovered-project"
				if tt.refresh {
					wantToken, wantProject = "fresh-token", tt.project
				}
				if req.AuthID != homePluginAuth || req.AuthProvider != "antigravity" || req.Model != homePluginModel || req.Stream != (operation == "stream") {
					t.Errorf("plugin request auth=%q provider=%q model=%q stream=%v", req.AuthID, req.AuthProvider, req.Model, req.Stream)
				}
				if req.AuthMetadata["access_token"] != wantToken || req.AuthMetadata["project_id"] != wantProject ||
					ojson.GetBytesNoCopy(req.StorageJSON, "access_token").String() != wantToken {
					t.Error("plugin received an unprepared credential")
				}
				if req.AuthMetadata["refresh_token"] != nil || ojson.GetBytesNoCopy(req.StorageJSON, "refresh_token").Exists() {
					t.Error("plugin received a refresh token")
				}
				if ojson.GetBytesNoCopy(req.Payload, "model").String() != homePluginModel ||
					ojson.GetBytesNoCopy(req.Payload, "messages.1.content").String() != "keep" || ojson.GetBytesNoCopy(req.Payload, "messages.2").Exists() {
					t.Errorf("plugin payload = %s, want upstream model and one payload filter application", req.Payload)
				}
			}
			plugin := &fakeExecutor{identifier: "plugin-native-provider",
				execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
					checkRequest("execute", req)
					return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"plugin"}}],"usage":{"prompt_tokens":2}}`)}, nil
				},
				executeStream: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
					checkRequest("stream", req)
					chunks := make(chan pluginapi.ExecutorStreamChunk, 1)
					chunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"plugin\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")}
					close(chunks)
					return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
				},
				countTokens: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
					checkRequest("count", req)
					return pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":3}`)}, nil
				},
			}
			host := newHostWithRecords(homeExecutorRecord(plugin, " Antigravity ", "other", "antigravity"))
			host.runtimeConfig = &config.Config{Payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{
				Models: []config.PayloadModelRule{{Name: homePluginModel}}, Params: []string{"messages.1"},
			}}}}
			cfg := &config.Config{Home: config.HomeConfig{Enabled: true}, DisableCooling: true}
			hook := &homeExecutorResultHook{}
			manager := coreauth.NewManager(nil, nil, hook)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 1)
			manager.RegisterExecutor(runtimeexecutor.NewAntigravityExecutor(cfg))
			manager.SetHomePluginExecutorResolver(host)
			registry := executionregistry.New()
			manager.PublishHomeDispatch(client, registry, 1)
			capture := newTestUsageCapturePlugin("antigravity")
			registerTestUsagePlugin(t, t.Name(), capture)
			if tt.operation == "execute" {
				for _, legacy := range []bool{false, true} {
					resp, errExecute := manager.Execute(ctx, []string{"home"}, coreexecutor.Request{Model: homePluginModel,
						Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)}, coreexecutor.Options{
						SourceFormat: sdktranslator.FormatAntigravity, Headers: http.Header{"X-Test-Legacy": {strconv.FormatBool(legacy)}},
					})
					if errExecute != nil || !strings.Contains(string(resp.Payload), "native") {
						t.Fatalf("native execution (legacy=%v) payload=%s error=%v", legacy, resp.Payload, errExecute)
					}
					record := capture.waitRecord(t, 5*time.Second)
					if record.AuthID != homePluginAuth || record.Model != homePluginModel || record.ExecutorType != "AntigravityExecutor" || record.Failed {
						t.Errorf("incorrect native usage attribution: %+v", record)
					}
				}
				if generations.Load() != 2 || len(calls) != 0 {
					t.Fatalf("native generations=%d plugin calls=%v", generations.Load(), calls)
				}
			}
			req := coreexecutor.Request{Model: "client-routing-placeholder", Payload: []byte(`{"model":"` + homePluginAlias + `","messages":[{"content":"first"},{"content":"remove"},{"content":"keep"}]}`)}
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: tt.operation == "stream", Metadata: map[string]any{
				coreexecutor.AuthSelectionModelMetadataKey: homePluginAlias,
				coreexecutor.RequestedModelMetadataKey:     homePluginAlias,
			}}
			switch tt.operation {
			case "execute":
				if _, errExecute := manager.Execute(ctx, []string{"home"}, req, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
			case "stream":
				result, errStream := manager.ExecuteStream(ctx, []string{"home"}, req, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			case "count":
				resp, errCount := manager.ExecuteCount(ctx, []string{"home"}, req, opts)
				if errCount != nil || ojson.GetBytesNoCopy(resp.Payload, "input_tokens").Int() != 3 {
					t.Fatalf("count payload=%s error=%v", resp.Payload, errCount)
				}
			}
			if !slices.Equal(calls, []string{tt.operation}) {
				t.Fatalf("plugin calls=%v, want %s", calls, tt.operation)
			}
			if len(hook.results) == 0 {
				t.Fatal("Home result hook received no execution result")
			}
			result := hook.results[len(hook.results)-1]
			if result.AuthID != homePluginAuth || result.Provider != "antigravity" || result.Model != homePluginModel || result.RouteModel != homePluginAlias || !result.Success {
				t.Errorf("incorrect Home result attribution: auth=%q provider=%q model=%q route=%q success=%v", result.AuthID, result.Provider, result.Model, result.RouteModel, result.Success)
			}
			wantRefreshes, wantDiscoveries := int32(0), int32(1)
			if tt.refresh {
				wantRefreshes, wantDiscoveries = 1, 0
			}
			if refreshes.Load() != wantRefreshes || discoveries.Load() != wantDiscoveries {
				t.Errorf("Home refreshes=%d project discoveries=%d, want %d and %d", refreshes.Load(), discoveries.Load(), wantRefreshes, wantDiscoveries)
			}
			if tt.operation != "count" {
				record := capture.waitRecord(t, 5*time.Second)
				if record.Provider != "antigravity" || record.Model != homePluginModel || record.Alias != homePluginAlias ||
					record.AuthID != homePluginAuth || record.AuthIndex != homePluginIndex || record.ExecutorType != "plugin:"+homePluginID || record.Failed {
					t.Errorf("incorrect plugin usage attribution: %+v", record)
				}
			}
			if snapshot := registry.FreezeInFlight(time.Now()); len(snapshot.Executions) != 0 {
				t.Fatalf("Home execution leaked: %+v", snapshot.Executions)
			}
		})
	}
}

type homeExecutorTestDispatcher struct {
	payload []byte
	calls   int
}

func (*homeExecutorTestDispatcher) HeartbeatOK() bool { return true }

func (*homeExecutorTestDispatcher) AbortAmbiguousDispatch() {}

func (d *homeExecutorTestDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	d.calls++
	return d.payload, nil
}

type homeExecutorResultHook struct {
	coreauth.NoopHook
	results []coreauth.Result
}

func (h *homeExecutorResultHook) OnResult(_ context.Context, result coreauth.Result) {
	h.results = append(h.results, result)
}

func TestHomePluginDispatchRejectsUnavailableExecutor(t *testing.T) {
	for _, tt := range []struct {
		name      string
		operation string
		providers []string
		wantError string
	}{
		{name: "missing", operation: "execute", wantError: "missing"},
		{name: "disabled", operation: "count", providers: []string{"antigravity"}, wantError: "disabled"},
		{name: "fused", operation: "stream", providers: []string{"antigravity"}, wantError: "fused"},
		{name: "undeclared-oauth-executor", operation: "execute", wantError: "does not declare support"},
		{name: "different-provider", operation: "stream", providers: []string{"other"}, wantError: "does not declare support"},
		{name: "declaration-revoked-after-selection", operation: "stream", providers: []string{"antigravity"}, wantError: "is unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			unexpectedCall := func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
				t.Error("unavailable plugin was called")
				return pluginapi.ExecutorResponse{}, errors.New("unexpected plugin invocation")
			}
			plugin := &fakeExecutor{identifier: "plugin-native-provider", execute: unexpectedCall, countTokens: unexpectedCall,
				executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
					_, errExecute := unexpectedCall(ctx, req)
					return pluginapi.ExecutorStreamResponse{}, errExecute
				}}
			record := homeExecutorRecord(plugin, tt.providers...)
			host := newHostWithRecords(record)
			cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
			hook := &homeExecutorResultHook{}
			manager := coreauth.NewManager(nil, nil, hook)
			manager.SetConfig(cfg)
			manager.SetHomePluginExecutorResolver(host)
			manager.SetRetryConfig(2, 0, 2)
			dispatchID := homePluginID
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: tt.operation == "stream"}
			switch tt.name {
			case "missing":
				dispatchID = "missing-executor"
			case "disabled":
				setHostSnapshotForTest(host, false)
			case "fused":
				host.fused[homePluginID] = "test fuse"
			case "declaration-revoked-after-selection":
				opts.RequestAfterAuthInterceptor = func(context.Context, coreexecutor.RequestAfterAuthInterceptRequest) coreexecutor.RequestAfterAuthInterceptResponse {
					setHostSnapshotForTest(host, true, homeExecutorRecord(plugin, "other"))
					return coreexecutor.RequestAfterAuthInterceptResponse{}
				}
			}
			auth := homeExecutorTestAuth("selected-token", "existing-project", time.Now().Add(time.Hour))
			dispatcher := &homeExecutorTestDispatcher{payload: homeExecutorDispatchJSON(t, auth, dispatchID)}
			registry := executionregistry.New()
			var releases int64
			registry.SetReleaseSink(func(group executionregistry.ReleaseGroup, sequence int64) {
				if group.CredentialID != homePluginAuth || group.Model != homePluginModel {
					t.Errorf("unexpected release group: %+v", group)
				}
				releases = sequence
			})
			manager.PublishHomeDispatch(dispatcher, registry, 1)
			req := coreexecutor.Request{Model: homePluginAlias}
			var errExecution error
			switch tt.operation {
			case "execute":
				_, errExecution = manager.Execute(t.Context(), []string{"home"}, req, opts)
			case "stream":
				_, errExecution = manager.ExecuteStream(t.Context(), []string{"home"}, req, opts)
			case "count":
				_, errExecution = manager.ExecuteCount(t.Context(), []string{"home"}, req, opts)
			}
			var localErr *coreauth.Error
			if !errors.As(errExecution, &localErr) || localErr.Code != coreauth.ErrorCodeHomeExecutorPluginUnavailable ||
				localErr.StatusCode() != http.StatusServiceUnavailable || !strings.Contains(localErr.Message, tt.wantError) {
				t.Fatalf("error = %v, want a local plugin error containing %q", errExecution, tt.wantError)
			}
			if len(hook.results) != 0 || dispatcher.calls != 1 || releases != 1 || len(registry.FreezeInFlight(time.Now()).Executions) != 0 {
				t.Fatalf("results=%d dispatches=%d releases=%d", len(hook.results), dispatcher.calls, releases)
			}
			if tt.name == "disabled" || tt.name == "fused" || tt.name == "undeclared-oauth-executor" {
				if ready := host.HomeExecutorPlugins(); len(ready) != 0 {
					t.Errorf("unavailable plugin advertised as ready: %v", ready)
				}
			}
		})
	}
}

func TestHomePluginDispatchRegistrationCapability(t *testing.T) {
	for _, tt := range []struct {
		name      string
		providers []string
	}{
		{name: "legacy"},
		{name: "explicit", providers: []string{"antigravity"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var identifiers int
			executor := &fakeExecutor{identifierFunc: func() string {
				identifiers++
				return "test-provider"
			}}
			plugin := validTestPlugin(homePluginID)
			plugin.Capabilities = homeExecutorRecord(executor, tt.providers...).plugin.Capabilities
			lookup := newTestSymbolLookup(&testPlugin{registerResult: plugin})
			lookup.schemaVersion = 1
			host := New()
			registered, errRegister := registerRPCPlugin(t.Context(), host, homePluginID, lookup, pluginabi.MethodPluginRegister, nil)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			if registered.Capabilities.ExecutorModelScope != pluginapi.ExecutorModelScopeOAuth || !slices.Equal(registered.Capabilities.ExecutorAuthProviders, tt.providers) {
				t.Fatalf("registration scope=%q auth providers=%v", registered.Capabilities.ExecutorModelScope, registered.Capabilities.ExecutorAuthProviders)
			}
			setHostSnapshotForTest(host, true, capabilityRecord{id: homePluginID, plugin: registered})
			for range 2 {
				if ready := host.HomeExecutorPlugins(); !slices.Equal(ready[homePluginID], tt.providers) {
					t.Errorf("Home readiness = %v, want %v", ready, tt.providers)
				}
			}
			if identifiers != len(tt.providers) {
				t.Errorf("executor identifier calls=%d, want %d", identifiers, len(tt.providers))
			}
		})
	}
}

type homeExecutorIdentifierTestClient struct {
	pluginClient
	identifierResponse []byte
	identifierErr      error
	identifierCalls    int
}

func (c *homeExecutorIdentifierTestClient) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	if method == pluginabi.MethodExecutorIdentifier {
		c.identifierCalls++
		return c.identifierResponse, c.identifierErr
	}
	return c.pluginClient.Call(ctx, method, request)
}

func TestHomePluginDispatchRegistrationPropagatesIdentifierFailure(t *testing.T) {
	transportErr := errors.New("identifier transport failed")
	for _, method := range []string{pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure} {
		for _, tt := range []struct {
			name string
			raw  []byte
			err  error
		}{
			{name: "transport", err: transportErr},
			{name: "rpc", raw: marshalRPCError("identifier_unavailable", "identifier unavailable", http.StatusServiceUnavailable)},
			{name: "envelope-decode", raw: []byte(`{"ok":`)},
			{name: "result-decode", raw: []byte(`{"ok":true,"result":{"identifier":42}}`)},
		} {
			t.Run(method+"/"+tt.name, func(t *testing.T) {
				plugin := validTestPlugin(homePluginID)
				plugin.Capabilities = homeExecutorRecord(&fakeExecutor{identifier: "test-provider"}, "antigravity").plugin.Capabilities
				client := &homeExecutorIdentifierTestClient{
					pluginClient:       newTestSymbolLookup(&testPlugin{registerResult: plugin, reconfigureResult: plugin}),
					identifierResponse: tt.raw, identifierErr: tt.err,
				}
				registered, errRegister := registerRPCPlugin(t.Context(), nil, homePluginID, client, method, nil)
				if errRegister == nil {
					t.Fatal("registration succeeded despite executor identifier failure")
				}
				if tt.err != nil && !errors.Is(errRegister, tt.err) {
					t.Fatalf("registration error = %v, want wrapped transport error", errRegister)
				}
				if tt.name == "rpc" {
					var pluginErr rpcError
					if !errors.As(errRegister, &pluginErr) || pluginErr.Code != "identifier_unavailable" {
						t.Fatalf("registration error = %v, want preserved RPC error", errRegister)
					}
				}
				if registered.Capabilities.Executor != nil || client.identifierCalls != 1 {
					t.Fatalf("failed registration returned executor=%v after %d identifier calls", registered.Capabilities.Executor, client.identifierCalls)
				}
				response, errMarshal := marshalRPCResult(rpcIdentifierResponse{Identifier: " test-provider "})
				if errMarshal != nil {
					t.Fatal(errMarshal)
				}
				client.identifierResponse, client.identifierErr = response, nil
				registered, errRegister = registerRPCPlugin(t.Context(), nil, homePluginID, client, method, nil)
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				for range 2 {
					if got := registered.Capabilities.Executor.Identifier(); got != "test-provider" {
						t.Errorf("cached identifier = %q, want test-provider", got)
					}
				}
				if client.identifierCalls != 2 {
					t.Errorf("identifier calls = %d, want 2 across failed and successful registrations", client.identifierCalls)
				}
			})
		}
	}
}

func TestHomeDispatchReportsAntigravityProjectDiscoveryFailure(t *testing.T) {
	for _, tt := range []struct {
		name, operation string
		status          int
		delegated       bool
		refresh         bool
		wantRecord      bool
	}{
		{name: "execute-401", operation: "execute", status: http.StatusUnauthorized, delegated: true, wantRecord: true},
		{name: "stream-429-after-refresh", operation: "stream", status: http.StatusTooManyRequests, delegated: true, refresh: true, wantRecord: true},
		{name: "count-401-after-refresh", operation: "count", status: http.StatusUnauthorized, delegated: true, refresh: true, wantRecord: true},
		{name: "native-429", operation: "execute", status: http.StatusTooManyRequests, wantRecord: true},
		{name: "other-status", operation: "execute", status: http.StatusServiceUnavailable, delegated: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(runtimeexecutor.NewAntigravityExecutor(cfg))
			manager.SetRetryConfig(0, 0, 1)
			pluginID, routeModel := "", homePluginModel
			if tt.delegated {
				pluginID, routeModel = homePluginID, homePluginAlias
				plugin := &fakeExecutor{identifier: "plugin-native-provider"}
				manager.SetHomePluginExecutorResolver(newHostWithRecords(homeExecutorRecord(plugin, "antigravity")))
			}
			now := time.Now()
			selected := homeExecutorTestAuth("selected-token", "", now.Add(time.Hour))
			effective := selected
			var refreshCalls atomic.Int32
			if tt.refresh {
				selected = homeExecutorTestAuth("selected-token", "", now.Add(time.Minute))
				effective = homeExecutorTestAuth("fresh-token", "", now.Add(time.Hour))
				reply, errMarshal := json.Marshal(map[string]any{"auth": effective, "auth_index": homePluginIndex})
				if errMarshal != nil {
					t.Fatal(errMarshal)
				}
				newHomeExecutorTestClient(t, func(command, argument string) []byte {
					if command != "GET" || ojson.Get(argument, "type").String() != "refresh" {
						t.Errorf("unexpected Home request: %s %s", command, argument)
						return nil
					}
					refreshCalls.Add(1)
					return reply
				})
			}
			manager.PublishHomeDispatch(&homeExecutorTestDispatcher{payload: homeExecutorDispatchJSON(t, selected, pluginID)}, executionregistry.New(), 1)
			capture := newTestUsageCapturePlugin("")
			registerTestUsagePlugin(t, t.Name(), capture)
			const body = `{"error":{"message":"project discovery rejected"}}`
			var projectCalls atomic.Int32
			transport := homeExecutorTestTransport(func(req *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(req.URL.Path, ":loadCodeAssist") {
					return nil, fmt.Errorf("unexpected Antigravity request %s", req.URL.Path)
				}
				projectCalls.Add(1)
				if req.Header.Get("Authorization") != "Bearer "+effective.Metadata["access_token"].(string) {
					t.Error("project discovery did not use the expected token")
				}
				return &http.Response{StatusCode: tt.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
			req := coreexecutor.Request{Model: routeModel, Payload: []byte(`{}`)}
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: tt.operation == "stream"}
			var errExecute error
			switch tt.operation {
			case "execute":
				_, errExecute = manager.Execute(ctx, []string{"home"}, req, opts)
			case "stream":
				_, errExecute = manager.ExecuteStream(ctx, []string{"home"}, req, opts)
			case "count":
				_, errExecute = manager.ExecuteCount(ctx, []string{"home"}, req, opts)
			}
			var statusErr interface{ StatusCode() int }
			if !errors.As(errExecute, &statusErr) || statusErr.StatusCode() != tt.status || projectCalls.Load() != 1 {
				t.Fatalf("project calls = %d, execution error = %v, want one project call and status %d", projectCalls.Load(), errExecute, tt.status)
			}
			if calls := refreshCalls.Load(); (tt.refresh && calls != 1) || (!tt.refresh && calls != 0) {
				t.Fatalf("refresh calls = %d, refresh expected = %v", calls, tt.refresh)
			}
			// Publication is FIFO; the barrier lets the test detect duplicates without sleeping.
			barrier := t.Name()
			coreusage.PublishRecord(ctx, coreusage.Record{Provider: barrier})
			var records []coreusage.Record
			for {
				record := capture.waitRecord(t, 5*time.Second)
				if record.Provider == barrier {
					break
				}
				if record.AuthID == homePluginAuth {
					records = append(records, record)
				}
			}
			if !tt.wantRecord {
				if len(records) != 0 {
					t.Fatalf("got %d unexpected failure records", len(records))
				}
				return
			}
			if len(records) != 1 {
				t.Fatalf("failure records = %d, want 1", len(records))
			}
			record := records[0]
			if !record.Failed || record.Fail.StatusCode != tt.status || record.Fail.Body != body || record.Provider != "antigravity" ||
				record.AuthIndex != homePluginIndex || record.AccessTokenSHA256 != coreauth.AccessTokenSHA256(&effective) ||
				record.Model != homePluginModel || record.Alias != routeModel || record.Generate == nil || *record.Generate {
				t.Errorf("incorrect project-discovery failure record: %+v", record)
			}
		})
	}
}

func TestHomePluginDispatchReportsUpstreamFailure(t *testing.T) {
	for _, operation := range []string{"execute", "stream", "count"} {
		for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
			t.Run(fmt.Sprintf("%s-%d", operation, status), func(t *testing.T) {
				body := `{"error":{"status":"UNAUTHENTICATED","message":"invalid token"}}`
				if status == http.StatusTooManyRequests {
					body = `{"error":{"status":"RESOURCE_EXHAUSTED","message":"quota exhausted","details":[{"reason":"QUOTA_EXHAUSTED"}]}}`
				}
				upstreamErr := pluginabi.NewError("upstream_error", body, status)
				fail := func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
					return pluginapi.ExecutorResponse{}, upstreamErr
				}
				plugin := &fakeExecutor{identifier: "plugin-native-provider", execute: fail, countTokens: fail,
					executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
						return pluginapi.ExecutorStreamResponse{}, upstreamErr
					}}
				host := newHostWithRecords(homeExecutorRecord(plugin, "antigravity"))
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
				manager.SetHomePluginExecutorResolver(host)
				manager.SetRetryConfig(0, 0, 1)
				auth := homeExecutorTestAuth("selected-token", "existing-project", time.Now().Add(time.Hour))
				dispatcher := &homeExecutorTestDispatcher{payload: homeExecutorDispatchJSON(t, auth, homePluginID)}
				manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
				capture := newTestUsageCapturePlugin("antigravity")
				registerTestUsagePlugin(t, t.Name(), capture)
				req := coreexecutor.Request{Model: homePluginAlias}
				opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: operation == "stream"}
				var errExecution error
				switch operation {
				case "execute":
					_, errExecution = manager.Execute(t.Context(), []string{"home"}, req, opts)
				case "stream":
					_, errExecution = manager.ExecuteStream(t.Context(), []string{"home"}, req, opts)
				case "count":
					_, errExecution = manager.ExecuteCount(t.Context(), []string{"home"}, req, opts)
				}
				var statusErr interface{ StatusCode() int }
				if !errors.As(errExecution, &statusErr) || statusErr.StatusCode() != status {
					t.Fatalf("error = %v, want upstream status %d", errExecution, status)
				}
				record := capture.waitRecord(t, 5*time.Second)
				if !record.Failed || record.Fail.StatusCode != status || record.Fail.Body != body || record.ExecutorType != "plugin:"+homePluginID ||
					record.Provider != "antigravity" || record.Model != homePluginModel || record.Alias != homePluginAlias || record.AuthID != homePluginAuth ||
					record.AuthIndex != homePluginIndex || record.AccessTokenSHA256 != coreauth.AccessTokenSHA256(&auth) || record.Generate == nil || *record.Generate != (operation != "count") {
					t.Errorf("incorrect Home failure attribution: %+v", record)
				}
			})
		}
	}
}

func TestHomePluginDispatchReleasesOnStreamCancellation(t *testing.T) {
	canceled := make(chan struct{})
	plugin := &fakeExecutor{identifier: "plugin-native-provider",
		executeStream: func(ctx context.Context, _ pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			chunks := make(chan pluginapi.ExecutorStreamChunk, 1)
			chunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"plugin\"}}]}\n\n")}
			go func() {
				<-ctx.Done()
				close(chunks)
				close(canceled)
			}()
			return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
		}}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	manager.SetHomePluginExecutorResolver(newHostWithRecords(homeExecutorRecord(plugin, "antigravity")))
	auth := homeExecutorTestAuth("selected-token", "existing-project", time.Now().Add(time.Hour))
	dispatcher := &homeExecutorTestDispatcher{payload: homeExecutorDispatchJSON(t, auth, homePluginID)}
	registry := executionregistry.New()
	released := make(chan executionregistry.ReleaseGroup, 1)
	registry.SetReleaseSink(func(group executionregistry.ReleaseGroup, _ int64) { released <- group })
	manager.PublishHomeDispatch(dispatcher, registry, 1)
	capture := newTestUsageCapturePlugin("antigravity")
	registerTestUsagePlugin(t, t.Name(), capture)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result, errStream := manager.ExecuteStream(ctx, []string{"home"}, coreexecutor.Request{Model: homePluginAlias}, coreexecutor.Options{
		Stream: true, SourceFormat: sdktranslator.FormatOpenAI,
	})
	if errStream != nil {
		t.Fatal(errStream)
	}
	if snapshot := registry.FreezeInFlight(time.Now()); len(snapshot.Executions) != 1 {
		t.Fatalf("expected active Home stream, got %+v", snapshot.Executions)
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("plugin context was not canceled")
	}
	select {
	case group := <-released:
		if group.CredentialID != homePluginAuth || group.Model != homePluginModel {
			t.Errorf("unexpected Home release group: %+v", group)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Home admission was not released")
	}
	select {
	case _, open := <-result.Chunks:
		if open {
			t.Error("Home stream delivered a chunk after release")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Home stream did not close")
	}
	if snapshot := registry.FreezeInFlight(time.Now()); len(snapshot.Executions) != 0 {
		t.Fatalf("Home stream execution leaked: %+v", snapshot.Executions)
	}
	record := capture.waitRecord(t, 5*time.Second)
	if record.AuthID != homePluginAuth || !record.Failed {
		t.Errorf("unexpected cancellation usage record: %+v", record)
	}
}

func newHomeExecutorTestClient(t *testing.T, reply func(command, argument string) []byte) *home.Client {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	var server sync.WaitGroup
	server.Go(func() {
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			server.Go(func() {
				defer func() { _ = conn.Close() }()
				reader := bufio.NewReader(conn)
				for {
					var count int
					if _, errRead := fmt.Fscanf(reader, "*%d\n", &count); errRead != nil || count < 1 {
						return
					}
					args := make([]string, count)
					for i := range args {
						var length int
						if _, errRead := fmt.Fscanf(reader, "$%d\n", &length); errRead != nil || length < 0 {
							return
						}
						raw := make([]byte, length+2)
						if _, errRead := io.ReadFull(reader, raw); errRead != nil {
							return
						}
						args[i] = string(raw[:length])
					}
					response := "+OK\r\n"
					switch command := strings.ToUpper(args[0]); command {
					case "HELLO":
						response = "%2\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n"
					case "SUBSCRIBE":
						response = ">3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
					case "PING":
						response = "+PONG\r\n"
					case "GET", "RPOP":
						var payload []byte
						if args[1] == "config" {
							payload = []byte("{}")
						} else {
							payload = reply(command, args[1])
						}
						response = "$-1\r\n"
						if payload != nil {
							response = fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)
						}
					}
					if _, errWrite := io.WriteString(conn, response); errWrite != nil {
						return
					}
				}
			})
		}
	})
	client := home.New(config.HomeConfig{Enabled: true, Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, DisableClusterDiscovery: true})
	previous := home.Current()
	home.SetCurrent(client)
	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- client.RunConfigSubscriberLifetime(ctx, func([]byte) error { return nil }, func() { close(ready) })
	}()
	t.Cleanup(func() {
		cancel()
		client.Close()
		home.SetCurrent(previous)
		_ = listener.Close()
		server.Wait()
	})
	select {
	case <-ready:
		return client
	case errClient := <-done:
		t.Fatalf("mock Home subscriber failed: %v", errClient)
	case <-time.After(10 * time.Second):
		t.Fatal("mock Home subscriber was not ready")
	}
	return nil
}
