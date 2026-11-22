package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type claudeQuotaProbeExecutor struct {
	coreauth.ProviderExecutor
	execute func(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
}

func (e claudeQuotaProbeExecutor) Identifier() string { return "claude" }

func (e claudeQuotaProbeExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.execute(ctx, auth, req, opts)
}

func TestObserveClaudeQuotaV8(t *testing.T) {
	const model = "claude-haiku-5-5"
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	modelRegistry := registry.GetGlobalRegistry()
	target := &coreauth.Auth{ID: t.Name() + "-target", Index: "target", Provider: "claude", Prefix: "test", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "test-token"}}
	other := &coreauth.Auth{ID: t.Name() + "-other", Index: "other", Provider: "claude", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "other-token"}}
	for _, auth := range []*coreauth.Auth{target, other} {
		modelRegistry.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}, {ID: "test/" + model}})
		t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
		registerAuthForLookupTest(t, manager, auth)
	}
	registerAuthForLookupTest(t, manager, &coreauth.Auth{ID: t.Name() + "-codex", Index: "codex", Provider: "codex", Status: coreauth.StatusActive})
	registerAuthForLookupTest(t, manager, &coreauth.Auth{ID: t.Name() + "-disabled", Index: "disabled", Provider: "claude", Disabled: true, Metadata: map[string]any{"access_token": "disabled-token"}})

	var calls int
	emitQuota := true
	rateLimited := false
	manager.RegisterExecutor(claudeQuotaProbeExecutor{execute: func(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		calls++
		if auth.ID != target.ID || req.Model != model || opts.SourceFormat != translator.FormatClaude || opts.Stream {
			t.Fatalf("unexpected probe: auth=%q model=%q format=%q streaming=%t", auth.ID, req.Model, opts.SourceFormat, opts.Stream)
		}
		if ojson.GetBytes(req.Payload, "max_tokens").Int() != 1 {
			t.Fatal("expected a one-token probe")
		}
		if rateLimited {
			logging.SetResponseHeaders(ctx, http.Header{
				"Anthropic-Ratelimit-Unified-7d-Utilization": {"1.0"},
				"Anthropic-Ratelimit-Unified-7d-Status":      {"rejected"},
			})
			return cliproxyexecutor.Response{}, &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}
		}
		response := cliproxyexecutor.Response{Headers: http.Header{"Set-Cookie": {"never-disclose-this"}}}
		if emitQuota {
			response.Headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.42")
		}
		return response, nil
	}})
	h := &Handler{authManager: manager}
	request := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v8/management/credentials/claude-quota", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.ObserveClaudeQuotaV8(c)
		return w
	}

	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{}`, http.StatusBadRequest},
		{`{"auth_index":"missing"}`, http.StatusNotFound},
		{`{"auth_index":"codex"}`, http.StatusBadRequest},
		{`{"auth_index":"disabled"}`, http.StatusConflict},
	} {
		if w := request(tc.body); w.Code != tc.status {
			t.Errorf("request %s: status = %d, want %d", tc.body, w.Code, tc.status)
		}
	}
	if calls != 0 {
		t.Fatal("invalid requests reached the executor")
	}
	w := request(`{"auth_index":"target"}`)
	if w.Code != http.StatusOK || calls != 1 {
		t.Fatalf("expected one successful probe, got status %d, %d calls: %s", w.Code, calls, w.Body.String())
	}
	if ojson.GetBytes(w.Body.Bytes(), "quota.signals.Anthropic-Ratelimit-Unified-7d-Utilization").String() != "0.42" ||
		ojson.GetBytes(w.Body.Bytes(), "quota.observed_at").String() == "" || strings.Contains(w.Body.String(), "never-disclose-this") {
		t.Fatalf("unexpected quota observation: %s", w.Body.String())
	}
	emitQuota = false
	if w := request(`{"auth_index":"target"}`); w.Code != http.StatusBadGateway {
		t.Fatalf("expected missing quota headers to fail, got %d: %s", w.Code, w.Body.String())
	}
	rateLimited = true
	w = request(`{"auth_index":"target"}`)
	if w.Code != http.StatusOK || ojson.GetBytes(w.Body.Bytes(), "quota.signals.Anthropic-Ratelimit-Unified-7d-Utilization").String() != "1.0" {
		t.Fatalf("expected the fresh quota from an upstream 429, got %d: %s", w.Code, w.Body.String())
	}
}
