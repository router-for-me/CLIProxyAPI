package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const searchBindingHeader = "X-Test-Search-Binding"

type searchLifecycleHost struct {
	t                         *testing.T
	active                    string
	before, after, completed  int
	rejectBefore, rejectAfter bool
	completion                pluginapi.RequestCompletion
}

func (h *searchLifecycleHost) InterceptRequestBeforeAuth(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	h.before++
	if req.RequestID == "" || req.RequestID == "client-forged" {
		h.t.Fatal("search must use a host-generated lifecycle ID")
	}
	h.active = req.RequestID
	if h.rejectBefore {
		return pluginapi.RequestInterceptResponse{Terminate: true, StatusCode: 403, ResponseBody: []byte(`{"error":"key_disabled"}`)}
	}
	headers := req.Headers.Clone()
	headers.Set(searchBindingHeader, req.RequestID)
	return pluginapi.RequestInterceptResponse{Headers: headers}
}

func (h *searchLifecycleHost) Pick(_ context.Context, _, _ string, opts coreexecutor.Options, candidates []*auth.Auth) (*auth.Auth, error) {
	if h.active == "" || opts.Headers.Get(searchBindingHeader) != h.active {
		return nil, errors.New("missing_request_binding")
	}
	for _, candidate := range candidates {
		if candidate.ID == "bound-auth" {
			return candidate, nil
		}
	}
	return nil, errors.New("bound_account_unavailable")
}

func (h *searchLifecycleHost) InterceptRequestAfterAuth(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	h.after++
	if req.RequestID != h.active || req.Metadata["selected_auth_id"] != "bound-auth" {
		h.t.Fatal("after-auth hook lost the request binding or selected account")
	}
	if h.rejectAfter {
		return pluginapi.RequestInterceptResponse{Terminate: true, StatusCode: 403, ResponseBody: []byte(`{"error":"binding_changed"}`)}
	}
	return pluginapi.RequestInterceptResponse{Body: []byte(`{"query":"intercepted","prompt_cache_key":"remove-me"}`)}
}

func (h *searchLifecycleHost) InterceptResponse(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{}
}

func (h *searchLifecycleHost) InterceptStreamChunk(context.Context, pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	return pluginapi.StreamChunkInterceptResponse{}
}

func (h *searchLifecycleHost) CompleteRequest(_ context.Context, completion pluginapi.RequestCompletion) {
	h.completed++
	if completion.RequestID != h.active {
		h.t.Fatal("completion lost the request binding")
	}
	h.completion = completion
	h.active = ""
}

func TestCodexAlphaSearchRequestLifecycle(t *testing.T) {
	for _, path := range []string{"/v1/alpha/search", "/backend-api/codex/alpha/search"} {
		for _, tc := range []struct {
			name                                                       string
			rejectBefore, rejectAfter, noAuth, upstreamFailure, cancel bool
			status, after                                              int
			outcome                                                    pluginapi.RequestCompletionOutcome
		}{
			{name: "success", status: 200, after: 1, outcome: pluginapi.RequestCompletionSucceeded},
			{name: "before rejection", rejectBefore: true, status: 403, outcome: pluginapi.RequestCompletionRejected},
			{name: "after rejection", rejectAfter: true, status: 403, after: 1, outcome: pluginapi.RequestCompletionRejected},
			{name: "no credential", noAuth: true, status: 503, outcome: pluginapi.RequestCompletionFailed},
			{name: "upstream failure", upstreamFailure: true, status: 502, after: 1, outcome: pluginapi.RequestCompletionFailed},
			{name: "canceled", cancel: true, outcome: pluginapi.RequestCompletionCanceled},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				server := newTestServer(t)
				host := &searchLifecycleHost{t: t, rejectBefore: tc.rejectBefore, rejectAfter: tc.rejectAfter}
				server.handlers.SetPluginHost(host)
				server.handlers.AuthManager.SetSelector(host)
				executor := &codexSearchCaptureExecutor{}
				if tc.upstreamFailure {
					executor.httpErr = errors.New("upstream unavailable")
				}
				server.handlers.AuthManager.RegisterExecutor(executor)
				if !tc.noAuth {
					for _, id := range []string{"other-auth", "bound-auth"} {
						_, err := server.handlers.AuthManager.Register(context.Background(), &auth.Auth{ID: id, Provider: "codex", Status: auth.StatusActive, Metadata: map[string]any{"access_token": id}})
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"query":"original"}`))
				req.Header.Set("Authorization", "Bearer test-key")
				req.Header.Set(searchBindingHeader, "client-forged")
				if tc.cancel {
					ctx, cancel := context.WithCancel(req.Context())
					cancel()
					req = req.WithContext(ctx)
				}
				rr := httptest.NewRecorder()
				server.engine.ServeHTTP(rr, req)
				if !tc.cancel && rr.Code != tc.status {
					t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.status, rr.Body.String())
				}
				if host.before != 1 || host.completed != 1 || host.active != "" {
					t.Fatalf("before=%d complete=%d active=%q", host.before, host.completed, host.active)
				}
				if !tc.cancel && host.after != tc.after {
					t.Fatalf("after=%d want %d", host.after, tc.after)
				}
				if host.completion.Outcome != tc.outcome {
					t.Fatalf("outcome=%s want %s", host.completion.Outcome, tc.outcome)
				}
				if tc.outcome == pluginapi.RequestCompletionSucceeded {
					if len(executor.authIDs) != 1 || executor.authIDs[0] != "bound-auth" {
						t.Fatalf("selected accounts=%v", executor.authIDs)
					}
					if string(executor.body) != `{"query":"intercepted"}` {
						t.Fatalf("upstream body=%s", executor.body)
					}
					if executor.request.Header.Get(searchBindingHeader) != "" {
						t.Fatal("binding header leaked upstream")
					}
				} else if executor.request != nil {
					t.Fatal("rejected or failed request reached upstream")
				}
			})
		}
	}
}
