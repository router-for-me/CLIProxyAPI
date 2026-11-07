package handlers

import (
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func clientKeyRouteConfig() *sdkconfig.SDKConfig {
	return &sdkconfig.SDKConfig{APIKeys: []string{"old-key", "google-key"}, APIKeyRoutes: []internalconfig.APIKeyRoute{{
		APIKey:      "google-key",
		Provider:    "antigravity",
		Model:       "gemini-3.8-flash-high",
		OpusModel:   "gemini-4-argon-high",
		SonnetModel: "gemini-3.8-flash-high",
		HaikuModel:  "gemini-3-flash",
	}}}
}

func TestClientKeyRouteMapping(t *testing.T) {
	h := NewBaseAPIHandlers(clientKeyRouteConfig(), nil)
	for _, tc := range []struct{ requested, want string }{
		{"claude-opus-5-5", "gemini-4-argon-high"},
		{"claude-fable-5-1", "gemini-4-argon-high"},
		{"claude-sonnet-5-5(32768)", "gemini-3.8-flash-high"},
		{"claude-haiku-5-5(high)", "gemini-3-flash"},
		{"gpt-6.1-sol", "gemini-3.8-flash-high"},
		{"antigravity/claude-opus-5-5-high", "gemini-4-argon-high"},
		{"antigravity/claude-sonnet-5-5[1m]", "gemini-3.8-flash-high"},
	} {
		ctx := coreusage.WithAPIKey(context.Background(), "google-key")
		decision := h.applyModelRouter(ctx, "claude", tc.requested, nil, false, modelExecutionOptions{})
		providers, model, errMsg := h.providersForExecution(tc.requested, tc.requested, false, decision, modelExecutionOptions{})
		if errMsg != nil || len(providers) != 1 || providers[0] != "antigravity" || model != tc.want {
			t.Fatalf("%s: providers=%v model=%s error=%v", tc.requested, providers, model, errMsg)
		}
	}
	for _, key := range []string{"old-key", "", "unknown-key"} {
		if _, matched := h.clientKeyRoute(coreusage.WithAPIKey(context.Background(), key), "claude-opus-5-5"); matched {
			t.Fatalf("unexpected policy for key %q", key)
		}
	}
}

func TestClientKeyRouteExecutionPaths(t *testing.T) {
	for _, path := range []string{"messages", "stream", "count"} {
		t.Run(path, func(t *testing.T) {
			executor := &interceptorCaptureExecutor{provider: "antigravity", stream: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
				chunks := make(chan coreexecutor.StreamChunk, 1)
				chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}
				close(chunks)
				return &coreexecutor.StreamResult{Chunks: chunks}, nil
			}}
			h := newInterceptorHandler(t, "gemini-4-argon-high", executor, clientKeyRouteConfig())
			ctx := coreusage.WithAPIKey(context.Background(), "google-key")
			payload := []byte(`{"model":"claude-opus-5-5","messages":[]}`)
			switch path {
			case "messages":
				_, _, errMsg := h.ExecuteWithAuthManager(ctx, "claude", "claude-opus-5-5", payload, "")
				if errMsg != nil {
					t.Fatal(errMsg)
				}
			case "count":
				_, _, errMsg := h.ExecuteCountWithAuthManager(ctx, "claude", "claude-opus-5-5", payload, "")
				if errMsg != nil {
					t.Fatal(errMsg)
				}
			case "stream":
				data, _, errs := h.ExecuteStreamWithAuthManager(ctx, "claude", "claude-opus-5-5", payload, "")
				for range data {
				}
				for errMsg := range errs {
					if errMsg != nil {
						t.Fatal(errMsg)
					}
				}
			}
			req, _ := executor.captured()
			if req.Model != "gemini-4-argon-high" {
				t.Fatalf("executor model = %q", req.Model)
			}
		})
	}
}

func TestClientKeyRouteDoesNotFallBack(t *testing.T) {
	executor := &interceptorCaptureExecutor{provider: "antigravity", execute: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
		return coreexecutor.Response{}, &coreauth.Error{Code: "quota", Message: "quota exhausted", HTTPStatus: http.StatusTooManyRequests}
	}}
	h := newInterceptorHandler(t, "gemini-4-argon-high", executor, clientKeyRouteConfig())
	codexCalled := false
	codex := &interceptorCaptureExecutor{provider: "codex", execute: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
		codexCalled = true
		return coreexecutor.Response{Payload: []byte("wrong backend")}, nil
	}}
	h.AuthManager.RegisterExecutor(codex)
	auth := &coreauth.Auth{ID: "client-key-route-codex", Provider: "codex", Status: coreauth.StatusActive}
	if _, errRegister := h.AuthManager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gemini-4-argon-high"}, {ID: "claude-opus-5-5"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	_, _, errMsg := h.ExecuteWithAuthManager(coreusage.WithAPIKey(context.Background(), "google-key"), "claude", "claude-opus-5-5", nil, "")
	if errMsg == nil || codexCalled {
		t.Fatalf("error=%v codexCalled=%v", errMsg, codexCalled)
	}
	// The original key continues using the ordinary registry route.
	_, _, errMsg = h.ExecuteWithAuthManager(coreusage.WithAPIKey(context.Background(), "old-key"), "claude", "claude-opus-5-5", nil, "")
	if errMsg != nil || !codexCalled {
		t.Fatalf("old key error=%v codexCalled=%v", errMsg, codexCalled)
	}
}

func TestClientKeyRouteOverridesPluginsAndPreparedStream(t *testing.T) {
	for _, path := range []string{"messages", "stream", "count"} {
		t.Run(path, func(t *testing.T) {
			executor := &interceptorCaptureExecutor{provider: "antigravity", stream: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
				chunks := make(chan coreexecutor.StreamChunk, 1)
				chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}
				close(chunks)
				return &coreexecutor.StreamResult{Chunks: chunks}, nil
			}}
			h := newInterceptorHandler(t, "gemini-4-argon-high", executor, clientKeyRouteConfig())
			host := &handlerDirectExecutorRouteHost{}
			host.hasRouters = true
			host.route = func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
				t.Fatal("client key route must precede router plugins")
				return pluginapi.ModelRouteResponse{}, false
			}
			h.SetModelRouterHost(host)
			ctx := coreusage.WithAPIKey(context.Background(), "google-key")
			// A previously prepared plugin route must not bypass the key policy.
			ctx = context.WithValue(ctx, preparedModelRouteContextKey{}, modelRouteDecision{ExecutorPluginID: "other-provider"})
			switch path {
			case "messages":
				_, _, errMsg := h.ExecuteWithAuthManager(ctx, "claude", "claude-opus-5-5", nil, "")
				if errMsg != nil {
					t.Fatal(errMsg)
				}
			case "count":
				_, _, errMsg := h.ExecuteCountWithAuthManager(ctx, "claude", "claude-opus-5-5", nil, "")
				if errMsg != nil {
					t.Fatal(errMsg)
				}
			case "stream":
				data, _, errs := h.ExecuteStreamWithAuthManager(ctx, "claude", "claude-opus-5-5", nil, "")
				for range data {
				}
				for errMsg := range errs {
					if errMsg != nil {
						t.Fatal(errMsg)
					}
				}
			}
			req, _ := executor.captured()
			if req.Model != "gemini-4-argon-high" {
				t.Fatalf("executor model = %q", req.Model)
			}
		})
	}
}

func TestClientKeyRouteInvalidPolicyFailsClosed(t *testing.T) {
	for _, policies := range [][]internalconfig.APIKeyRoute{
		{{APIKey: "google-key", Model: "gemini-4-argon-high"}},
		{{APIKey: "google-key", Provider: "antigravity"}},
		{{APIKey: "google-key", Provider: "antigravity", Model: "gemini-4-argon-high"}, {APIKey: "google-key", Provider: "codex", Model: "gpt-6.1-sol"}},
	} {
		cfg := clientKeyRouteConfig()
		cfg.APIKeyRoutes = policies
		h := NewBaseAPIHandlers(cfg, nil)
		decision, matched := h.clientKeyRoute(coreusage.WithAPIKey(context.Background(), "google-key"), "claude-opus-5-5")
		_, _, errMsg := h.providersForExecution("claude-opus-5-5", "claude-opus-5-5", false, decision, modelExecutionOptions{})
		if !matched || errMsg == nil || errMsg.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("matched=%v error=%v", matched, errMsg)
		}
	}
}

func TestClientKeyRouteFallbackModelOnQuotaExhaustion(t *testing.T) {
	for _, path := range []string{"messages", "stream"} {
		t.Run(path, func(t *testing.T) {
			var attemptedModels []string
			executor := &interceptorCaptureExecutor{
				provider: "antigravity",
				execute: func(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
					attemptedModels = append(attemptedModels, req.Model)
					if req.Model == "gemini-3.8-flash-high" {
						return coreexecutor.Response{}, &coreauth.Error{Code: "quota", Message: "Individual quota reached", HTTPStatus: http.StatusTooManyRequests}
					}
					return coreexecutor.Response{Payload: []byte(`{"id":"msg_fb","type":"message"}`)}, nil
				},
				stream: func(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
					attemptedModels = append(attemptedModels, req.Model)
					if req.Model == "gemini-3.8-flash-high" {
						return nil, &coreauth.Error{Code: "quota", Message: "Individual quota reached", HTTPStatus: http.StatusTooManyRequests}
					}
					chunks := make(chan coreexecutor.StreamChunk, 1)
					chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}
					close(chunks)
					return &coreexecutor.StreamResult{Chunks: chunks}, nil
				},
			}
			cfg := clientKeyRouteConfig()
			cfg.APIKeyRoutes[0].FallbackModel = "claude-sonnet-5-5-high"
			h := newInterceptorHandler(t, "gemini-3.8-flash-high", executor, cfg)
			h.AuthManager.SetRetryConfig(0, 0, 0)
			registry.GetGlobalRegistry().RegisterClient("handler-interceptor-gemini-3.8-flash-high", "antigravity", []*registry.ModelInfo{
				{ID: "gemini-3.8-flash-high"},
				{ID: "claude-sonnet-5-5-high"},
			})
			ctx := coreusage.WithAPIKey(context.Background(), "google-key")
			payload := []byte(`{"model":"claude-sonnet-5-5","messages":[]}`)
			switch path {
			case "messages":
				_, _, errMsg := h.ExecuteWithAuthManager(ctx, "claude", "claude-sonnet-5-5", payload, "")
				if errMsg != nil {
					t.Fatalf("unexpected error: %v", errMsg)
				}
			case "stream":
				data, _, errs := h.ExecuteStreamWithAuthManager(ctx, "claude", "claude-sonnet-5-5", payload, "")
				for range data {
				}
				for errMsg := range errs {
					if errMsg != nil {
						t.Fatalf("unexpected stream error: %v", errMsg)
					}
				}
			}
			if len(attemptedModels) != 2 || attemptedModels[0] != "gemini-3.8-flash-high" || attemptedModels[1] != "claude-sonnet-5-5-high" {
				t.Fatalf("attemptedModels = %v, want [gemini-3.8-flash-high claude-sonnet-5-5-high]", attemptedModels)
			}
		})
	}
}
