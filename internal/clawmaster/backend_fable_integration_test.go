package clawmaster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// The first subscription has used its Fable allowance: Anthropic answers its Fable requests with the Fable
// 429 and serves everything else. The second serves everything.
type fableIntegrationTransport struct {
	mu       sync.Mutex
	exhaust  string // the auth id out of Fable
	reset    time.Time
	requests []string // "auth model" per inference request
}

func (rt *fableIntegrationTransport) RoundTripperFor(auth *coreauth.Auth) http.RoundTripper {
	return fableIntegrationRoundTripper{transport: rt, authID: auth.ID}
}

type fableIntegrationRoundTripper struct {
	transport *fableIntegrationTransport
	authID    string
}

func (rt fableIntegrationRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	_ = request.Body.Close()
	if request.Method == http.MethodGet && request.URL.String() == ClaudeOAuthUsageEndpoint {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"seven_day":{"utilization":10,"resets_at":"2099-01-01T00:00:00Z"}}`)), Request: request}, nil
	}
	if request.Method != http.MethodPost || request.URL.String() != "https://api.anthropic.com/v1/messages?beta=true" {
		return nil, errors.New("synthetic transport rejects unexpected upstream route")
	}
	model := gjson.GetBytes(body, "model").String()
	rt.transport.mu.Lock()
	rt.transport.requests = append(rt.transport.requests, rt.authID+" "+model)
	exhausted := rt.authID == rt.transport.exhaust && isFableModel(model)
	reset := rt.transport.reset
	rt.transport.mu.Unlock()
	if exhausted {
		headers := fableRefusalHeaders(reset)
		headers.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: headers, Request: request,
			Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error","message":"synthetic Fable limit"}}`))}, nil
	}
	reply := fmt.Sprintf(`{"id":"msg_synthetic","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"served by %s"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":2}}`, model, rt.authID)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Request: request,
		Body: io.NopCloser(strings.NewReader(reply))}, nil
}

func (rt *fableIntegrationTransport) take() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	requests := rt.requests
	rt.requests = nil
	return requests
}

func TestBackendRoutesFableAwayFromASubscriptionOutOfFable(t *testing.T) {
	credentials := make([]BackendCredential, 0, 2)
	for index := 1; index <= 2; index++ {
		opts := writeSyntheticBackendCredential(t, canonicalTestTempDir(t), "selected.json", map[string]any{
			"type": "claude", "access_token": fmt.Sprintf("sk-ant-oat01-synthetic-fable-%d", index),
			"account_uuid":      fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa%d", index),
			"claude_device_ids": []string{integrationClaudeDevice},
		})
		credentials = append(credentials, BackendCredential{AuthDir: opts.AuthDir, Provider: "claude", AuthID: opts.AuthID})
	}
	backend, err := NewBackendSeries(t.Context(), BackendSeriesOptions{
		Credentials: credentials,
		QuotaRequest: func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"seven_day":{"utilization":10,"resets_at":"2099-01-01T00:00:00Z"}}`))}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	first, second := backend.authIDs[0], backend.authIDs[1]
	cfg := backendConfig(credentials[0].AuthDir)
	cfg.ProxyURL, cfg.MaxRetryCredentials = "", len(backend.authIDs)
	executor := runtimeexecutor.NewClaudeExecutor(cfg)
	transport := &fableIntegrationTransport{exhaust: first, reset: time.Now().Add(40 * time.Hour).Truncate(time.Second)}
	backend.manager.SetConfig(cfg)
	backend.manager.SetRoundTripperProvider(transport)
	backend.manager.RegisterExecutor(executor)

	send := func(session, model string, stream bool) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"model":%q,"max_tokens":64,"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, model, stream)
		request := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(body))
		request.Header.Set("X-Claude-Code-Session-Id", session)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Anthropic-Version", "2023-06-01")
		request = request.WithContext(coreexecutor.WithNativeClaudeProtocolHeaders(request.Context(), request.Header))
		response := httptest.NewRecorder()
		backend.Handler().ServeHTTP(response, request)
		return response
	}

	// 1. A Fable request lands on the first subscription, which refuses Fable: the same request is answered
	// by the second, and the session never sees the refusal.
	response := send("11111111-2222-4333-8444-000000000001", testFableModel, false)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "served by "+second) {
		t.Fatalf("Fable request: %d %s", response.Code, response.Body.String())
	}
	if got := transport.take(); len(got) != 2 || got[0] != first+" "+testFableModel || got[1] != second+" "+testFableModel {
		t.Fatalf("upstream requests = %v, want the first subscription then the second", got)
	}

	// 2. The first subscription still serves other models.
	response = send("11111111-2222-4333-8444-000000000002", testOtherModel, false)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "served by "+first) {
		t.Fatalf("other model: %d %s", response.Code, response.Body.String())
	}
	transport.take()

	// 3. New Fable requests, plain and streaming, go straight to the second subscription.
	for i, stream := range []bool{false, true} {
		response = send(fmt.Sprintf("11111111-2222-4333-8444-00000000001%d", i), testFableModel, stream)
		if response.Code != http.StatusOK {
			t.Fatalf("later Fable request (stream %v): %d %s", stream, response.Code, response.Body.String())
		}
		if got := transport.take(); len(got) != 1 || got[0] != second+" "+testFableModel {
			t.Fatalf("later Fable request (stream %v) went to %v, want only the second subscription", stream, got)
		}
	}

	// 4. With the second subscription out of Fable too, a Fable request gets Anthropic's Fable 429.
	backend.seriesSelector.observeFableRejection(second, fableRefusalHeaders(transport.reset))
	response = send("11111111-2222-4333-8444-000000000020", testFableModel, false)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Anthropic-Ratelimit-Unified-Representative-Claim") != "seven_day_overage_included" {
		t.Fatalf("every subscription out of Fable: %d %v %s", response.Code, response.Header(), response.Body.String())
	}
	if got := transport.take(); len(got) != 0 {
		t.Fatalf("a request was sent although no subscription has Fable: %v", got)
	}
}
