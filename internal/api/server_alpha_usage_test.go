package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type alphaSearchUsageCapture struct {
	mu      sync.Mutex
	authID  string
	records []usage.Record
}

func (*alphaSearchUsageCapture) Synchronous() bool { return true }
func (p *alphaSearchUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if record.AuthID == p.authID {
		p.records = append(p.records, record)
	}
}

func TestAlphaSearchUsageCoversAttemptAndReadFailures(t *testing.T) {
	const response = `{"results":[],"usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}`
	for _, scenario := range []struct {
		name       string
		prepareErr error
		httpErr    error
		readErr    bool
		status     int
		wantStatus int
		wantFailed bool
		wantTokens int64
	}{
		{name: "success", status: 200, wantStatus: 200, wantTokens: 110},
		{name: "upstream_error_usage", status: 500, wantStatus: 500, wantFailed: true, wantTokens: 110},
		{name: "request_preparation", prepareErr: errors.New("prepare failed"), wantStatus: 502, wantFailed: true},
		{name: "connection_error", httpErr: errors.New("upstream connection failed"), wantStatus: 502, wantFailed: true},
		{name: "body_read_error_with_usage", status: 200, readErr: true, wantStatus: 502, wantFailed: true, wantTokens: 110},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := newTestServer(t)
			capture := &alphaSearchUsageCapture{authID: t.Name()}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), &alphaSearchUsageCapture{}) })
			var upstreamReached time.Time
			executor := &codexSearchCaptureExecutor{prepareErr: scenario.prepareErr, httpErr: scenario.httpErr, statuses: []int{scenario.status}, responseBody: io.NopCloser(strings.NewReader(response)), beforeReturn: func() { upstreamReached = time.Now() }}
			if scenario.readErr {
				executor.responseBody = &errorSearchResponseBody{payload: []byte(response)}
			}
			server.handlers.AuthManager.RegisterExecutor(executor)
			credential := &auth.Auth{ID: t.Name(), Provider: "codex", Status: auth.StatusActive, Metadata: map[string]any{"access_token": "test-token"}}
			if _, err := server.handlers.AuthManager.Register(context.Background(), credential); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(credential.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.6-sol"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential.ID) })
			rr := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rr)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"model":"gpt-5.6-sol","query":"test"}`))
			server.codexAlphaSearch(c)
			if rr.Code != scenario.wantStatus {
				t.Fatalf("HTTP status=%d want %d: %s", rr.Code, scenario.wantStatus, rr.Body.String())
			}
			capture.mu.Lock()
			defer capture.mu.Unlock()
			if len(capture.records) != 1 {
				t.Fatalf("got %d selected-credential usage records, want 1", len(capture.records))
			}
			r := capture.records[0]
			if r.Failed != scenario.wantFailed || r.Detail.TotalTokens != scenario.wantTokens || r.Model != "gpt-5.6-sol" {
				t.Fatalf("incorrect attempt usage: %+v", r)
			}
			if !upstreamReached.IsZero() && (r.RequestedAt.After(upstreamReached) || r.Latency < upstreamReached.Sub(r.RequestedAt)) {
				t.Fatalf("usage timer started after upstream attempt: requested=%v upstream=%v latency=%v", r.RequestedAt, upstreamReached, r.Latency)
			}
		})
	}
}

func TestAlphaSearchHomeUnauthorizedPublishesOneAttempt(t *testing.T) {
	const response = `{"error":{"message":"access token expired"},"usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}`
	for _, mode := range []string{"response", "read_failure", "bind_failure"} {
		t.Run(mode, func(t *testing.T) {
			server := newTestServer(t)
			capture := &alphaSearchUsageCapture{authID: t.Name()}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), &alphaSearchUsageCapture{}) })
			attemptRegistry := executionregistry.New()
			server.handlers.AuthManager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
			server.handlers.AuthManager.PublishHomeDispatch(&codexSearchHomeDispatcher{authID: t.Name()}, attemptRegistry, 1)
			executor := &codexSearchCaptureExecutor{statuses: []int{http.StatusUnauthorized}, responseBody: io.NopCloser(strings.NewReader(response))}
			if mode == "read_failure" {
				executor.responseBody = &errorSearchResponseBody{payload: []byte(response)}
			}
			if mode == "bind_failure" {
				executor.beforeReturn = func() { _ = attemptRegistry.Close() }
			}
			server.handlers.AuthManager.RegisterExecutor(executor)
			rr := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rr)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"model":"gpt-5.6-sol","query":"test"}`))
			server.codexAlphaSearch(c)
			capture.mu.Lock()
			defer capture.mu.Unlock()
			if len(capture.records) != 1 {
				t.Fatalf("one upstream attempt generated %d accounting events", len(capture.records))
			}
			r := capture.records[0]
			wantBody, wantTokens := response, int64(110)
			if mode == "bind_failure" {
				wantBody, wantTokens = "upstream unauthorized", 0
			}
			if !r.Failed || r.Fail.StatusCode != http.StatusUnauthorized || r.Fail.Body != wantBody || r.Detail.TotalTokens != wantTokens {
				t.Fatalf("incorrect Home 401 failure: %+v", r)
			}
			if r.AuthIndex == "" || r.AccessTokenSHA256 == "" {
				t.Fatal("Home result lost credential attribution")
			}
		})
	}
}
