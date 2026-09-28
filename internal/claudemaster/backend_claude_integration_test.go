package claudemaster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

const (
	integrationClaudeToken   = "sk-ant-oat01-synthetic-selected-account"
	integrationClaudeAccount = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	integrationClaudeDevice  = "0000000000000000000000000000000000000000000000000000000000000000"
	integrationMasterCanary  = "master-private-identity-canary"
	integrationToolSchema    = `{"type":"object","properties":{"command":{"type":"string","description":"The command to run"}},"required":["command"],"additionalProperties":false}`
	integrationClaudeReply   = `{"id":"msg_synthetic","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"synthetic answer"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":2}}`
)

type claudeIntegrationRequest struct {
	method, target string
	header         http.Header
	body           []byte
}

// This is both the selected-account transport provider and the entire upstream.
// It never delegates to a network transport, including for unexpected URLs.
type claudeIntegrationTransport struct {
	mu          sync.Mutex
	authIDs     []string
	authTokens  []string
	requests    []claudeIntegrationRequest
	expectedURL string
	status      int
	contentType string
	response    string
	respond     func([]byte) (int, string, string)
	unexpected  bool
}

func (rt *claudeIntegrationTransport) RoundTripperFor(auth *coreauth.Auth) http.RoundTripper {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if auth == nil {
		rt.authIDs = append(rt.authIDs, "<nil>")
		rt.authTokens = append(rt.authTokens, "")
	} else {
		rt.authIDs = append(rt.authIDs, auth.ID)
		token, _ := auth.Metadata["access_token"].(string)
		rt.authTokens = append(rt.authTokens, token)
	}
	return rt
}

func (rt *claudeIntegrationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	_ = request.Body.Close()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.requests = append(rt.requests, claudeIntegrationRequest{
		method: request.Method, target: request.URL.String(), header: request.Header.Clone(), body: raw,
	})
	if request.Method != http.MethodPost || request.URL.String() != rt.expectedURL {
		rt.unexpected = true
		return nil, errors.New("synthetic transport rejects unexpected upstream route")
	}
	status, contentType, response := rt.status, rt.contentType, rt.response
	if rt.respond != nil {
		status, contentType, response = rt.respond(raw)
	}
	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"Content-Type": {contentType}, "Request-Id": {"selected-request-id"},
			"Retry-After": {"3"}, "X-Should-Retry": {"false"},
			"Anthropic-Ratelimit-Unified-Status": {"allowed"},
			"Set-Cookie":                         {"selected-private-canary"}, "X-Account-Id": {"selected-private-canary"},
		},
		Body:    io.NopCloser(strings.NewReader(response)),
		Request: request,
	}, nil
}

func TestBackendRealClaudeExecutor(t *testing.T) {
	for _, tc := range []struct {
		name, path, response, contentType string
		stream                            bool
		status                            int
	}{
		{name: "messages", path: "/v1/messages", status: http.StatusOK, contentType: "application/json", response: integrationClaudeReply},
		{name: "stream", path: "/v1/messages", stream: true, status: http.StatusOK, contentType: "text/event-stream", response: integrationClaudeStream()},
		{name: "count_tokens", path: "/v1/messages/count_tokens", status: http.StatusOK, contentType: "application/json", response: `{"input_tokens":17}`},
		{name: "upstream_failure", path: "/v1/messages", status: http.StatusTooManyRequests, contentType: "application/json", response: `{"type":"error","error":{"type":"rate_limit_error","message":"upstream-private-canary"}}`},
		{name: "stream_failure", path: "/v1/messages", stream: true, status: http.StatusTooManyRequests, contentType: "application/json", response: `{"type":"error","error":{"type":"rate_limit_error","message":"upstream-private-canary"}}`},
		{name: "count_failure", path: "/v1/messages/count_tokens", status: http.StatusBadRequest, contentType: "application/json", response: `{"type":"error","error":{"type":"invalid_request_error","message":"upstream-private-canary"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A real, separately loaded profile exercises auth normalization and
			// identity preparation without importing any native master state.
			opts := writeSyntheticBackendCredential(t, canonicalTestTempDir(t), "real-claude-"+tc.name+".json", map[string]any{
				"type": "claude", "access_token": integrationClaudeToken,
				"account_uuid": integrationClaudeAccount, "claude_device_ids": []string{integrationClaudeDevice},
			})
			opts.Provider, opts.Model = "claude", "claude-opus-4-8"
			store, credential, err := loadBackendCredential(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			cfg := backendConfig(opts.AuthDir)
			// Production deliberately uses direct egress. Only this synthetic
			// fixture clears it so the SDK honors our no-network transport.
			cfg.ProxyURL = ""
			executor := runtimeexecutor.NewClaudeExecutor(cfg)
			if executor.ShouldPrepareRequestAuth(credential) {
				t.Fatal("synthetic identity would require a live OAuth profile lookup")
			}
			transport := &claudeIntegrationTransport{
				expectedURL: "https://api.anthropic.com" + tc.path + "?beta=true",
				status:      tc.status, contentType: tc.contentType, response: tc.response,
			}
			// Keep the production selector and config: custom selectors take
			// the manager's mixed-provider path even for one pinned account.
			manager := coreauth.NewManager(store, &backendSelector{authID: opts.AuthID, provider: opts.Provider}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 1)
			manager.SetRoundTripperProvider(transport)
			manager.RegisterExecutor(executor)
			registry.GetGlobalRegistry().RegisterClient(opts.AuthID, opts.Provider, []*registry.ModelInfo{{ID: backendAuthSelectionModel}})
			t.Cleanup(func() {
				store.seal()
				manager.CloseExecutionSession(coreauth.CloseAllExecutionSessionsID)
				registry.GetGlobalRegistry().UnregisterClient(opts.AuthID)
			})
			if _, err := manager.Register(coreauth.WithSkipPersist(t.Context()), credential); err != nil {
				t.Fatal(err)
			}
			// Do not start the background refresh loop: this test has no live
			// OAuth authority and exercises inference only.
			lifetime, cancel := context.WithCancel(t.Context())
			defer cancel()
			handlerOpts := opts
			handlerOpts.Model = ""
			handlerOpts.UseRequestModel = true
			handler := newBackendHandler(lifetime, handlerOpts, handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
			body := fmt.Sprintf(`{"model":%q,"max_tokens":64,"stream":%t,"metadata":{"user_id":%q},"messages":[{"role":"user","content":"integration hello"}],"tools":[{"name":"Bash","description":"Execute a command","input_schema":%s}]}`, opts.Model, tc.stream, integrationMasterCanary, integrationToolSchema)
			request := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com"+tc.path+"?api_key="+integrationMasterCanary, strings.NewReader(body))
			for _, header := range []string{"Authorization", "X-Api-Key", "Cookie", "Proxy-Authorization", "X-Account-Id", "X-Claude-Code-Session-Id", "X-Claude-Remote-Container-Id", "X-Claude-Remote-Session-Id"} {
				request.Header.Set(header, integrationMasterCanary)
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Anthropic-Version", "2023-06-01")
			for key, values := range nativeProtocolFixture() {
				request.Header[key] = append([]string(nil), values...)
			}
			// The CONNECT adapter is the only production opt-in site. Capture the
			// same sanitized snapshot here to exercise all real executor paths.
			request = request.WithContext(coreexecutor.WithNativeClaudeProtocolHeaders(request.Context(), request.Header))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("backend status = %d, want %d: %s", response.Code, tc.status, response.Body.String())
			}
			for _, key := range []string{"Request-Id", "Retry-After", "X-Should-Retry", "Anthropic-Ratelimit-Unified-Status"} {
				if response.Header().Get(key) == "" {
					t.Errorf("native response header %s was lost", key)
				}
			}
			if strings.Contains(fmt.Sprint(response.Header()), "selected-private-canary") {
				t.Fatal("account-bearing upstream header escaped the response boundary")
			}
			if tc.status >= 400 {
				if strings.Contains(response.Body.String(), "upstream-private-canary") || !strings.Contains(response.Body.String(), "no fallback") {
					t.Fatalf("upstream failure was not sanitized: %s", response.Body.String())
				}
			} else if tc.path == "/v1/messages/count_tokens" {
				if gjson.GetBytes(response.Body.Bytes(), "input_tokens").Int() != 17 {
					t.Fatalf("upstream count response was lost: %s", response.Body.String())
				}
			} else if !strings.Contains(response.Body.String(), "synthetic answer") {
				t.Fatalf("upstream response was lost: %s", response.Body.String())
			}
			if tc.stream && tc.status < 400 && (!strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(response.Body.String(), "message_stop")) {
				t.Fatalf("incomplete streaming response: %s", response.Body.String())
			}

			transport.mu.Lock()
			defer transport.mu.Unlock()
			if transport.unexpected || len(transport.requests) != 1 || len(transport.authIDs) == 0 {
				t.Fatalf("expected one inference request and no retry/fallback/profile calls; requests=%d, selected=%v, unexpected=%t", len(transport.requests), transport.authIDs, transport.unexpected)
			}
			for i, id := range transport.authIDs {
				if id != opts.AuthID || transport.authTokens[i] != integrationClaudeToken {
					t.Fatal("transport selection escaped the pinned synthetic account")
				}
			}
			upstream := transport.requests[0]
			for key, want := range nativeProtocolFixture() {
				var got []string
				for wireKey, values := range upstream.header {
					if strings.EqualFold(wireKey, key) {
						got = values
						break
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("native request header %s changed: got %v want %v", key, got, want)
				}
			}
			if upstream.header.Get("Authorization") != "Bearer "+integrationClaudeToken || upstream.header.Get("X-Api-Key") != "" {
				t.Fatal("upstream did not use only the selected OAuth bearer")
			}
			for key, values := range upstream.header {
				if strings.Contains(strings.Join(values, ","), integrationMasterCanary) {
					t.Fatalf("master identity leaked through header %s", key)
				}
			}
			if strings.Contains(string(upstream.body), integrationMasterCanary) || strings.Contains(upstream.target, integrationMasterCanary) {
				t.Fatal("master identity leaked in upstream body or URL")
			}
			if gjson.GetBytes(upstream.body, "model").String() != opts.Model || gjson.GetBytes(upstream.body, "tools.0.name").String() != "Bash" {
				t.Fatalf("native model or tool name changed: %s", upstream.body)
			}
			var wantSchema, gotSchema any
			if err := json.Unmarshal([]byte(integrationToolSchema), &wantSchema); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(gjson.GetBytes(upstream.body, "tools.0.input_schema").Raw), &gotSchema); err != nil || !reflect.DeepEqual(wantSchema, gotSchema) {
				t.Fatalf("native tool input schema changed: %s", upstream.body)
			}
			if tc.path == "/v1/messages/count_tokens" {
				if gjson.GetBytes(upstream.body, "metadata").Exists() {
					t.Fatal("count_tokens must omit metadata unsupported by that endpoint")
				}
			} else {
				identity := gjson.GetBytes(upstream.body, "metadata.user_id").String()
				if gjson.Get(identity, "account_uuid").String() != integrationClaudeAccount || gjson.Get(identity, "device_id").String() != integrationClaudeDevice {
					t.Fatalf("upstream metadata did not use selected account/device: %s", identity)
				}
				if gjson.GetBytes(upstream.body, "stream").Bool() != tc.stream {
					t.Fatal("streaming request mode changed")
				}
			}
		})
	}
}

func TestBackendRealClaudeExecutorPreservesNativeSessionAcrossResponseModes(t *testing.T) {
	opts := writeSyntheticBackendCredential(t, canonicalTestTempDir(t), "real-claude-session-continuity.json", map[string]any{
		"type": "claude", "access_token": integrationClaudeToken,
		"account_uuid": integrationClaudeAccount, "claude_device_ids": []string{integrationClaudeDevice},
	})
	opts.Provider, opts.Model = "claude", "claude-opus-4-8"
	store, credential, err := loadBackendCredential(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	cfg := backendConfig(opts.AuthDir)
	cfg.ProxyURL = ""
	executor := runtimeexecutor.NewClaudeExecutor(cfg)
	if executor.ShouldPrepareRequestAuth(credential) {
		t.Fatal("synthetic identity would require a live OAuth profile lookup")
	}
	transport := &claudeIntegrationTransport{
		expectedURL: "https://api.anthropic.com/v1/messages?beta=true",
		respond: func(body []byte) (int, string, string) {
			if gjson.GetBytes(body, "stream").Bool() {
				return http.StatusOK, "text/event-stream", integrationClaudeStream()
			}
			return http.StatusOK, "application/json", integrationClaudeReply
		},
	}
	manager := coreauth.NewManager(store, &backendSelector{authID: opts.AuthID, provider: opts.Provider}, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, 1)
	manager.SetRoundTripperProvider(transport)
	manager.RegisterExecutor(executor)
	registry.GetGlobalRegistry().RegisterClient(opts.AuthID, opts.Provider, []*registry.ModelInfo{{ID: backendAuthSelectionModel}})
	t.Cleanup(func() {
		store.seal()
		manager.CloseExecutionSession(coreauth.CloseAllExecutionSessionsID)
		registry.GetGlobalRegistry().UnregisterClient(opts.AuthID)
	})
	if _, err = manager.Register(coreauth.WithSkipPersist(t.Context()), credential); err != nil {
		t.Fatal(err)
	}
	lifetime, cancel := context.WithCancel(t.Context())
	defer cancel()
	handlerOpts := opts
	handlerOpts.Model = ""
	handlerOpts.UseRequestModel = true
	handler := newBackendHandler(lifetime, handlerOpts, handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))

	const (
		sessionID     = "11111111-2222-4333-8444-555555555555"
		masterAccount = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		masterDevice  = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	)
	masterIdentity := `{"device_id":"` + masterDevice + `","account_uuid":"` + masterAccount + `","session_id":"` + sessionID + `","native_extra":"preserved"}`
	for i, stream := range []bool{false, true} {
		body := fmt.Sprintf(`{"model":%q,"max_tokens":64,"stream":%t,"metadata":{"user_id":%q},"messages":[{"role":"user","content":"continuity turn %d"}]}`, opts.Model, stream, masterIdentity, i+1)
		request := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?api_key="+integrationMasterCanary, strings.NewReader(body))
		for key, values := range nativeProtocolFixture() {
			request.Header[key] = append([]string(nil), values...)
		}
		request.Header.Del("X-Claude-Code-Agent-Id")
		request.Header.Del("X-Claude-Code-Parent-Agent-Id")
		request.Header.Set("X-Client-Request-Id", fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1))
		request.Header.Set("Authorization", "Bearer "+integrationMasterCanary)
		request.Header.Set("X-Api-Key", integrationMasterCanary)
		if detection := helps.DetectClaudeCodeRequest(request.Header, []byte(body), false, cfg); !detection.Confirmed {
			t.Fatalf("turn %d fixture is not recognized as native Claude Code: %+v", i+1, detection)
		}
		request = request.WithContext(coreexecutor.WithNativeClaudeProtocolHeaders(request.Context(), request.Header))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("turn %d status = %d: %s", i+1, response.Code, response.Body.String())
		}
		if stream && !strings.Contains(response.Body.String(), "message_stop") {
			t.Fatalf("turn %d stream was incomplete: %s", i+1, response.Body.String())
		}
	}

	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.unexpected || len(transport.requests) != 2 {
		t.Fatalf("expected two isolated upstream requests; requests=%d unexpected=%t", len(transport.requests), transport.unexpected)
	}
	for i, upstream := range transport.requests {
		identity := gjson.GetBytes(upstream.body, "metadata.user_id").String()
		if got := upstream.header.Get("X-Claude-Code-Session-Id"); got != sessionID {
			t.Fatalf("turn %d upstream session header = %q, want %q", i+1, got, sessionID)
		}
		if got := gjson.Get(identity, "session_id").String(); got != sessionID {
			t.Fatalf("turn %d session_id = %q, want %q; identity=%s upstream-session-header=%q", i+1, got, sessionID, identity, upstream.header.Get("X-Claude-Code-Session-Id"))
		}
		if got := gjson.Get(identity, "account_uuid").String(); got != integrationClaudeAccount {
			t.Fatalf("turn %d account_uuid = %q, want selected account", i+1, got)
		}
		if got := gjson.Get(identity, "device_id").String(); got != integrationClaudeDevice {
			t.Fatalf("turn %d device_id = %q, want selected device", i+1, got)
		}
		if got := gjson.Get(identity, "native_extra").String(); got != "preserved" {
			t.Fatalf("turn %d native metadata extension = %q, want preserved", i+1, got)
		}
		if strings.Contains(string(upstream.body), integrationMasterCanary) || strings.Contains(fmt.Sprint(upstream.header), integrationMasterCanary) ||
			strings.Contains(string(upstream.body), masterAccount) || strings.Contains(string(upstream.body), masterDevice) {
			t.Fatalf("turn %d leaked native-master identity", i+1)
		}
	}
}

func integrationClaudeStream() string {
	return "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_synthetic","type":"message","role":"assistant","model":"claude-opus-4-8","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"synthetic answer"}}` + "\n\n" +
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}` + "\n\n" +
		"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"
}
