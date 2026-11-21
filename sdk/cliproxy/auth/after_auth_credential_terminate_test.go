package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type recordingResultHook struct {
	mu      sync.Mutex
	results []Result
}

func (h *recordingResultHook) OnAuthRegistered(context.Context, *Auth) {}
func (h *recordingResultHook) OnAuthUpdated(context.Context, *Auth)    {}
func (h *recordingResultHook) OnResult(_ context.Context, result Result) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.results = append(h.results, result)
}

func (h *recordingResultHook) Results() []Result {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Result, len(h.results))
	copy(out, h.results)
	return out
}

type afterAuthTestExecutor struct {
	id               string
	mu               sync.Mutex
	calls            []string
	failByAuth       map[string]error
	markAttempt      map[string]bool
	prepareErrByAuth map[string]error
}

func (e *afterAuthTestExecutor) ShouldPrepareRequestAuth(auth *Auth) bool {
	return auth != nil && e.prepareErrByAuth[auth.ID] != nil
}

func (e *afterAuthTestExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	if auth != nil && e.prepareErrByAuth[auth.ID] != nil {
		return nil, e.prepareErrByAuth[auth.ID]
	}
	return auth, nil
}

func (e *afterAuthTestExecutor) Identifier() string { return e.id }

func (e *afterAuthTestExecutor) record(auth *Auth) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if auth != nil {
		e.calls = append(e.calls, auth.ID)
	}
}

func (e *afterAuthTestExecutor) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.calls))
	copy(out, e.calls)
	return out
}

func (e *afterAuthTestExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth)
	if auth != nil && e.markAttempt[auth.ID] {
		cliproxyexecutor.MarkUpstreamAttempt(ctx)
	}
	if auth != nil && e.failByAuth[auth.ID] != nil {
		return cliproxyexecutor.Response{}, e.failByAuth[auth.ID]
	}
	return cliproxyexecutor.Response{Payload: []byte("ok:" + auth.ID)}, nil
}

func (e *afterAuthTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.record(auth)
	if auth != nil && e.markAttempt[auth.ID] {
		cliproxyexecutor.MarkUpstreamAttempt(ctx)
	}
	if auth != nil && e.failByAuth[auth.ID] != nil {
		return nil, e.failByAuth[auth.ID]
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok:" + auth.ID)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *afterAuthTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *afterAuthTestExecutor) CountTokens(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth)
	if auth != nil && e.markAttempt[auth.ID] {
		cliproxyexecutor.MarkUpstreamAttempt(ctx)
	}
	if auth != nil && e.failByAuth[auth.ID] != nil {
		return cliproxyexecutor.Response{}, e.failByAuth[auth.ID]
	}
	return cliproxyexecutor.Response{Payload: []byte("count:" + auth.ID)}, nil
}

func (e *afterAuthTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func invokeAfterAuthMode(manager *Manager, provider, model, mode string, opts cliproxyexecutor.Options) ([]byte, error) {
	req := cliproxyexecutor.Request{Model: model}
	switch mode {
	case "execute":
		resp, errExec := manager.Execute(context.Background(), []string{provider}, req, opts)
		return resp.Payload, errExec
	case "count":
		resp, errCount := manager.ExecuteCount(context.Background(), []string{provider}, req, opts)
		return resp.Payload, errCount
	case "stream":
		opts.Stream = true
		stream, errStream := manager.ExecuteStream(context.Background(), []string{provider}, req, opts)
		if errStream != nil {
			return nil, errStream
		}
		var out []byte
		for chunk := range stream.Chunks {
			if chunk.Err != nil {
				return nil, chunk.Err
			}
			out = append(out, chunk.Payload...)
		}
		return out, nil
	default:
		panic("unsupported mode: " + mode)
	}
}

func TestAfterAuthCredentialTerminateFailover(t *testing.T) {
	for _, homeMode := range []bool{false, true} {
		for _, mode := range []string{"execute", "count", "stream"} {
			t.Run(fmt.Sprintf("home=%t/%s", homeMode, mode), func(t *testing.T) {
				provider := "home-retry-contract"
				if !homeMode {
					provider = fmt.Sprintf("after-auth-cred-failover-%s", mode)
				}
				model := fmt.Sprintf("after-auth-cred-failover-model-%t-%s", homeMode, mode)
				executor := &afterAuthTestExecutor{id: provider}
				hook := &recordingResultHook{}
				manager := NewManager(nil, &FillFirstSelector{}, hook)
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: homeMode}})
				manager.SetRetryConfig(2, 0, 5)
				manager.RegisterExecutor(executor)
				executions := executionregistry.New()
				var dispatcher *retryContractHomeDispatcher
				if homeMode {
					dispatcher = &retryContractHomeDispatcher{authIDs: []string{"cred-a", "cred-b"}}
					manager.PublishHomeDispatch(dispatcher, executions, 1)
				} else {
					for _, id := range []string{"cred-a", "cred-b"} {
						registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
						if _, errReg := manager.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive}); errReg != nil {
							t.Fatal(errReg)
						}
					}
				}

				var seenAuths []string
				opts := cliproxyexecutor.Options{
					RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
						authID, _ := req.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string)
						seenAuths = append(seenAuths, authID)
						if authID == "cred-a" {
							return cliproxyexecutor.RequestAfterAuthInterceptResponse{
								Terminate:       true,
								TerminateScope:  "credential",
								StatusCode:      http.StatusForbidden,
								ResponseHeaders: http.Header{"X-Refused": {"cred-a"}},
								ResponseBody:    []byte("cred-a refused"),
							}
						}
						return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
					},
				}

				payload, errInvoke := invokeAfterAuthMode(manager, provider, model, mode, opts)
				if errInvoke != nil {
					t.Fatalf("invokeAfterAuthMode() error = %v", errInvoke)
				}
				if !reflect.DeepEqual(seenAuths, []string{"cred-a", "cred-b"}) {
					t.Fatalf("interceptor saw %v, want [cred-a cred-b]", seenAuths)
				}
				if calls := executor.Calls(); !reflect.DeepEqual(calls, []string{"cred-b"}) {
					t.Fatalf("executor calls = %v, want [cred-b]", calls)
				}
				wantPayload := "ok:cred-b"
				if mode == "count" {
					wantPayload = "count:cred-b"
				}
				if string(payload) != wantPayload {
					t.Fatalf("payload = %q, want %q", string(payload), wantPayload)
				}
				for _, res := range hook.Results() {
					if res.AuthID == "cred-a" {
						t.Fatalf("unexpected MarkResult/reportHomeResult for refused credential: %+v", res)
					}
				}
				if homeMode {
					if active := executions.FreezeInFlight(time.Time{}).Executions; len(active) != 0 {
						t.Fatalf("Home execution scopes leaked: %v", active)
					}
					dispatcher.mu.Lock()
					excluded := append([][]string(nil), dispatcher.excluded...)
					dispatcher.mu.Unlock()
					if len(excluded) != 2 || len(excluded[0]) != 0 || !reflect.DeepEqual(excluded[1], []string{"cred-a"}) {
						t.Fatalf("Home excluded_auth_ids = %v, want [[] [cred-a]]", excluded)
					}
				} else {
					credA, ok := manager.GetByID("cred-a")
					if !ok || credA == nil {
						t.Fatal("cred-a missing")
					}
					if credA.Unavailable || !credA.NextRetryAfter.IsZero() || credA.Failed != 0 {
						t.Fatalf("refused cred-a state mutated: unavailable=%v nextRetry=%v failed=%d", credA.Unavailable, credA.NextRetryAfter, credA.Failed)
					}
				}
			})
		}
	}
}

func TestAfterAuthCredentialTerminateAllRefusedReturnsInterceptorResponse(t *testing.T) {
	for _, homeMode := range []bool{false, true} {
		for _, mode := range []string{"execute", "count", "stream"} {
			t.Run(fmt.Sprintf("home=%t/%s", homeMode, mode), func(t *testing.T) {
				provider := "home-retry-contract"
				if !homeMode {
					provider = fmt.Sprintf("after-auth-all-refused-%s", mode)
				}
				model := fmt.Sprintf("after-auth-all-refused-model-%t-%s", homeMode, mode)
				executor := &afterAuthTestExecutor{id: provider}
				hook := &recordingResultHook{}
				manager := NewManager(nil, &FillFirstSelector{}, hook)
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: homeMode}})
				manager.SetRetryConfig(2, 0, 5)
				manager.RegisterExecutor(executor)
				executions := executionregistry.New()
				if homeMode {
					manager.PublishHomeDispatch(&retryContractHomeDispatcher{authIDs: []string{"cred-a", "cred-b"}}, executions, 1)
				} else {
					for _, id := range []string{"cred-a", "cred-b"} {
						registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
						if _, errReg := manager.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive}); errReg != nil {
							t.Fatal(errReg)
						}
					}
				}

				var seenAuths []string
				opts := cliproxyexecutor.Options{
					RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
						authID, _ := req.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string)
						seenAuths = append(seenAuths, authID)
						return cliproxyexecutor.RequestAfterAuthInterceptResponse{
							Terminate:       true,
							TerminateScope:  "credential",
							StatusCode:      http.StatusForbidden,
							ResponseHeaders: http.Header{"X-Refused-By": {authID}},
							ResponseBody:    []byte("refused:" + authID),
						}
					},
				}

				_, errInvoke := invokeAfterAuthMode(manager, provider, model, mode, opts)
				var terminated *cliproxyexecutor.RequestTerminatedError
				if !errors.As(errInvoke, &terminated) || terminated == nil {
					t.Fatalf("error = %v (%T), want *RequestTerminatedError", errInvoke, errInvoke)
				}
				if terminated.StatusCode() != http.StatusForbidden ||
					terminated.ResponseHeaders().Get("X-Refused-By") != "cred-b" ||
					string(terminated.ResponseBody()) != "refused:cred-b" {
					t.Fatalf("terminated = status:%d headers:%v body:%q", terminated.StatusCode(), terminated.ResponseHeaders(), string(terminated.ResponseBody()))
				}
				if !reflect.DeepEqual(seenAuths, []string{"cred-a", "cred-b"}) {
					t.Fatalf("interceptor saw %v, want single pass [cred-a cred-b] without extra retry rounds", seenAuths)
				}
				if len(executor.Calls()) != 0 {
					t.Fatalf("executor should not be called, got %v", executor.Calls())
				}
				if len(hook.Results()) != 0 {
					t.Fatalf("unexpected result reports for all-refused request: %+v", hook.Results())
				}
				if homeMode {
					if active := executions.FreezeInFlight(time.Time{}).Executions; len(active) != 0 {
						t.Fatalf("Home execution scopes leaked: %v", active)
					}
				}
			})
		}
	}
}

func TestAfterAuthCredentialTerminateUpstreamErrorPrecedence(t *testing.T) {
	for _, homeMode := range []bool{false, true} {
		for _, mode := range []string{"execute", "count", "stream"} {
			t.Run(fmt.Sprintf("home=%t/%s", homeMode, mode), func(t *testing.T) {
				provider := "home-retry-contract"
				if !homeMode {
					provider = fmt.Sprintf("after-auth-upstream-prec-%s", mode)
				}
				model := fmt.Sprintf("after-auth-upstream-prec-model-%t-%s", homeMode, mode)
				upstreamErr := &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream 503"}
				executor := &afterAuthTestExecutor{
					id:          provider,
					failByAuth:  map[string]error{"cred-a": upstreamErr},
					markAttempt: map[string]bool{"cred-a": true},
				}
				manager := NewManager(nil, &FillFirstSelector{}, nil)
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: homeMode}})
				manager.SetRetryConfig(0, 0, 5)
				manager.RegisterExecutor(executor)
				executions := executionregistry.New()
				if homeMode {
					manager.PublishHomeDispatch(&retryContractHomeDispatcher{authIDs: []string{"cred-a", "cred-b"}}, executions, 1)
				} else {
					for _, id := range []string{"cred-a", "cred-b"} {
						registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
						if _, errReg := manager.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive}); errReg != nil {
							t.Fatal(errReg)
						}
					}
				}

				opts := cliproxyexecutor.Options{
					RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
						authID, _ := req.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string)
						if authID == "cred-b" {
							return cliproxyexecutor.RequestAfterAuthInterceptResponse{
								Terminate:      true,
								TerminateScope: "credential",
								StatusCode:     http.StatusForbidden,
								ResponseBody:   []byte("cred-b refused"),
							}
						}
						return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
					},
				}

				_, errInvoke := invokeAfterAuthMode(manager, provider, model, mode, opts)
				var terminated *cliproxyexecutor.RequestTerminatedError
				if errors.As(errInvoke, &terminated) {
					t.Fatalf("expected upstream 503 error to take precedence over credential refusal, got RequestTerminatedError: %v", errInvoke)
				}
				if statusCodeFromError(errInvoke) != http.StatusServiceUnavailable {
					t.Fatalf("status = %d (%v), want 503", statusCodeFromError(errInvoke), errInvoke)
				}
			})
		}
	}
}

func TestAfterAuthCredentialTerminateCountsTowardMaxRetryCredentials(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			provider := fmt.Sprintf("after-auth-max-retry-%s", mode)
			model := fmt.Sprintf("after-auth-max-retry-model-%s", mode)
			executor := &afterAuthTestExecutor{id: provider}
			manager := NewManager(nil, &FillFirstSelector{}, nil)
			manager.SetRetryConfig(0, 0, 2)
			manager.RegisterExecutor(executor)
			for _, id := range []string{"cred-a", "cred-b", "cred-c"} {
				registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				if _, errReg := manager.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive}); errReg != nil {
					t.Fatal(errReg)
				}
			}

			var seenAuths []string
			opts := cliproxyexecutor.Options{
				RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
					authID, _ := req.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string)
					seenAuths = append(seenAuths, authID)
					if authID == "cred-a" || authID == "cred-b" {
						return cliproxyexecutor.RequestAfterAuthInterceptResponse{
							Terminate:      true,
							TerminateScope: "credential",
							StatusCode:     http.StatusForbidden,
							ResponseBody:   []byte("refused:" + authID),
						}
					}
					return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
				},
			}

			_, errInvoke := invokeAfterAuthMode(manager, provider, model, mode, opts)
			var terminated *cliproxyexecutor.RequestTerminatedError
			if !errors.As(errInvoke, &terminated) || terminated == nil {
				t.Fatalf("error = %v, want *RequestTerminatedError", errInvoke)
			}
			if !reflect.DeepEqual(seenAuths, []string{"cred-a", "cred-b"}) {
				t.Fatalf("interceptor saw %v, want [cred-a cred-b] capped by max-retry-credentials=2", seenAuths)
			}
			if len(executor.Calls()) != 0 {
				t.Fatalf("executor calls = %v, want none", executor.Calls())
			}
		})
	}
}

func TestAfterAuthTerminateScopeRequestAndUnknownStayRequestFinal(t *testing.T) {
	for idx, scope := range []string{"", "request", "CREDENTIAL", "Credential", " credential ", "unknown-scope"} {
		for _, mode := range []string{"execute", "count", "stream"} {
			t.Run(fmt.Sprintf("scope=%q/%s", scope, mode), func(t *testing.T) {
				provider := fmt.Sprintf("after-auth-req-final-%d-%s", idx, mode)
				model := fmt.Sprintf("after-auth-req-final-model-%d-%s", idx, mode)
				executor := &afterAuthTestExecutor{id: provider}
				manager := NewManager(nil, &FillFirstSelector{}, nil)
				manager.SetRetryConfig(1, 0, 5)
				manager.RegisterExecutor(executor)
				for _, id := range []string{"cred-a", "cred-b"} {
					registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					if _, errReg := manager.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive}); errReg != nil {
						t.Fatal(errReg)
					}
				}

				var seenAuths []string
				opts := cliproxyexecutor.Options{
					RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
						authID, _ := req.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string)
						seenAuths = append(seenAuths, authID)
						return cliproxyexecutor.RequestAfterAuthInterceptResponse{
							Terminate:      true,
							TerminateScope: scope,
							StatusCode:     http.StatusForbidden,
							ResponseBody:   []byte("blocked"),
						}
					},
				}

				_, errInvoke := invokeAfterAuthMode(manager, provider, model, mode, opts)
				var terminated *cliproxyexecutor.RequestTerminatedError
				if !errors.As(errInvoke, &terminated) || terminated == nil || terminated.StatusCode() != http.StatusForbidden {
					t.Fatalf("error = %v, want 403 *RequestTerminatedError", errInvoke)
				}
				if !reflect.DeepEqual(seenAuths, []string{"cred-a"}) {
					t.Fatalf("interceptor saw %v, want [cred-a]", seenAuths)
				}
			})
		}
	}
}

type flakyAfterAuthTestExecutor struct {
	id          string
	mu          sync.Mutex
	callsByAuth map[string]int
	failFirstN  map[string]int
	failErr     error
}

func (e *flakyAfterAuthTestExecutor) Identifier() string { return e.id }

func (e *flakyAfterAuthTestExecutor) attempt(ctx context.Context, auth *Auth) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.callsByAuth == nil {
		e.callsByAuth = make(map[string]int)
	}
	e.callsByAuth[auth.ID]++
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	if e.callsByAuth[auth.ID] <= e.failFirstN[auth.ID] {
		return e.failErr
	}
	return nil
}

func (e *flakyAfterAuthTestExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if errAttempt := e.attempt(ctx, auth); errAttempt != nil {
		return cliproxyexecutor.Response{}, errAttempt
	}
	return cliproxyexecutor.Response{Payload: []byte("ok:" + auth.ID)}, nil
}

func (e *flakyAfterAuthTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if errAttempt := e.attempt(ctx, auth); errAttempt != nil {
		return nil, errAttempt
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok:" + auth.ID)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *flakyAfterAuthTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *flakyAfterAuthTestExecutor) CountTokens(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if errAttempt := e.attempt(ctx, auth); errAttempt != nil {
		return cliproxyexecutor.Response{}, errAttempt
	}
	return cliproxyexecutor.Response{Payload: []byte("count:" + auth.ID)}, nil
}

func (e *flakyAfterAuthTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestAfterAuthCredentialTerminateAllowsNormalRetryAndPreservesAcrossRounds(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run("normal-retry-succeeds/"+mode, func(t *testing.T) {
			provider := fmt.Sprintf("after-auth-retry-ok-%s", mode)
			model := fmt.Sprintf("after-auth-retry-ok-model-%s", mode)
			prevTransient := transientErrorCooldownSeconds.Load()
			transientErrorCooldownSeconds.Store(-1)
			t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })
			executor := &flakyAfterAuthTestExecutor{
				id:         provider,
				failFirstN: map[string]int{"cred-a": 1},
				failErr:    &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "transient 503"},
			}
			manager := NewManager(nil, &FillFirstSelector{}, nil)
			manager.SetRetryConfig(1, 0, 5)
			manager.RegisterExecutor(executor)
			for _, id := range []string{"cred-a", "cred-b"} {
				registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				if _, errReg := manager.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive}); errReg != nil {
					t.Fatal(errReg)
				}
			}

			opts := cliproxyexecutor.Options{
				RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
					authID, _ := req.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string)
					if authID == "cred-b" {
						return cliproxyexecutor.RequestAfterAuthInterceptResponse{
							Terminate:      true,
							TerminateScope: "credential",
							StatusCode:     http.StatusForbidden,
							ResponseBody:   []byte("cred-b refused"),
						}
					}
					return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
				},
			}

			payload, errInvoke := invokeAfterAuthMode(manager, provider, model, mode, opts)
			if errInvoke != nil {
				t.Fatalf("expected round 1 retry on cred-a to succeed, got error: %v", errInvoke)
			}
			wantPayload := "ok:cred-a"
			if mode == "count" {
				wantPayload = "count:cred-a"
			}
			if string(payload) != wantPayload {
				t.Fatalf("payload = %q, want %q", string(payload), wantPayload)
			}
		})

		t.Run("later-round-all-refused-keeps-earlier-upstream-error/"+mode, func(t *testing.T) {
			provider := fmt.Sprintf("after-auth-multiround-prec-%s", mode)
			model := fmt.Sprintf("after-auth-multiround-prec-model-%s", mode)
			prevTransient := transientErrorCooldownSeconds.Load()
			transientErrorCooldownSeconds.Store(-1)
			t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })
			executor := &flakyAfterAuthTestExecutor{
				id:         provider,
				failFirstN: map[string]int{"cred-a": 10},
				failErr:    &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream 503"},
			}
			manager := NewManager(nil, &FillFirstSelector{}, nil)
			manager.SetRetryConfig(1, 0, 5)
			manager.RegisterExecutor(executor)
			for _, id := range []string{"cred-a", "cred-b"} {
				registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				if _, errReg := manager.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive}); errReg != nil {
					t.Fatal(errReg)
				}
			}

			credASeen := 0
			opts := cliproxyexecutor.Options{
				RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
					authID, _ := req.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string)
					if authID == "cred-a" {
						credASeen++
						if credASeen == 1 {
							return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
						}
					}
					return cliproxyexecutor.RequestAfterAuthInterceptResponse{
						Terminate:      true,
						TerminateScope: "credential",
						StatusCode:     http.StatusForbidden,
						ResponseBody:   []byte("refused:" + authID),
					}
				},
			}

			_, errInvoke := invokeAfterAuthMode(manager, provider, model, mode, opts)
			var terminated *cliproxyexecutor.RequestTerminatedError
			if errors.As(errInvoke, &terminated) {
				t.Fatalf("expected round 0 upstream 503 to take precedence over round 1 credential refusals, got %v", errInvoke)
			}
			if statusCodeFromError(errInvoke) != http.StatusServiceUnavailable {
				t.Fatalf("status = %d (%v), want 503", statusCodeFromError(errInvoke), errInvoke)
			}
		})
	}
}

func TestAfterAuthCredentialTerminateAntigravityCreditsFallback(t *testing.T) {
	for _, mode := range []string{"execute", "stream"} {
		t.Run("failover/"+mode, func(t *testing.T) {
			model := "claude-antigravity-cred-term-failover-" + mode
			executor := &afterAuthTestExecutor{
				id: "antigravity",
				failByAuth: map[string]error{
					"ag-a": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota_exhausted"},
					"ag-b": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota_exhausted"},
				},
				markAttempt: map[string]bool{"ag-a": true, "ag-b": true},
			}
			manager := NewManager(nil, &FillFirstSelector{}, nil)
			cfg := &internalconfig.Config{}
			cfg.QuotaExceeded.AntigravityCredits = true
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 5)
			manager.RegisterExecutor(executor)
			for _, id := range []string{"ag-a", "ag-b"} {
				registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(id)
					SetAntigravityCreditsHint(id, AntigravityCreditsHint{})
				})
				if _, errReg := manager.Register(context.Background(), &Auth{
					ID:         id,
					Provider:   "antigravity",
					Status:     StatusActive,
					Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
				}); errReg != nil {
					t.Fatal(errReg)
				}
			}

			opts := cliproxyexecutor.Options{
				RequestAfterAuthInterceptor: func(ctx context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
					authID, _ := req.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string)
					if AntigravityCreditsRequested(ctx) {
						if authID == "ag-a" {
							return cliproxyexecutor.RequestAfterAuthInterceptResponse{
								Terminate:      true,
								TerminateScope: "credential",
								StatusCode:     http.StatusForbidden,
								ResponseBody:   []byte("credits ag-a refused"),
							}
						}
						executor.mu.Lock()
						delete(executor.failByAuth, "ag-b")
						executor.mu.Unlock()
					}
					return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
				},
			}

			payload, errInvoke := invokeAfterAuthMode(manager, "antigravity", model, mode, opts)
			if errInvoke != nil {
				t.Fatalf("expected credits fallback to fail over to ag-b, got error: %v", errInvoke)
			}
			if string(payload) != "ok:ag-b" {
				t.Fatalf("payload = %q, want ok:ag-b", string(payload))
			}
		})

		t.Run("all-refused-preserves-main-429/"+mode, func(t *testing.T) {
			model := "claude-antigravity-cred-term-all-refused-" + mode
			executor := &afterAuthTestExecutor{
				id: "antigravity",
				failByAuth: map[string]error{
					"ag-a": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota_exhausted"},
				},
				markAttempt: map[string]bool{"ag-a": true},
			}
			manager := NewManager(nil, &FillFirstSelector{}, nil)
			cfg := &internalconfig.Config{}
			cfg.QuotaExceeded.AntigravityCredits = true
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 5)
			manager.RegisterExecutor(executor)
			registry.GetGlobalRegistry().RegisterClient("ag-a", "antigravity", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient("ag-a")
				SetAntigravityCreditsHint("ag-a", AntigravityCreditsHint{})
			})
			if _, errReg := manager.Register(context.Background(), &Auth{
				ID:         "ag-a",
				Provider:   "antigravity",
				Status:     StatusActive,
				Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
			}); errReg != nil {
				t.Fatal(errReg)
			}

			creditsIntercepts := 0
			opts := cliproxyexecutor.Options{
				RequestAfterAuthInterceptor: func(ctx context.Context, _ cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
					if AntigravityCreditsRequested(ctx) {
						creditsIntercepts++
						return cliproxyexecutor.RequestAfterAuthInterceptResponse{
							Terminate:      true,
							TerminateScope: "credential",
							StatusCode:     http.StatusForbidden,
							ResponseBody:   []byte("credits refused"),
						}
					}
					return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
				},
			}

			_, errInvoke := invokeAfterAuthMode(manager, "antigravity", model, mode, opts)
			if creditsIntercepts != 1 {
				t.Fatalf("creditsIntercepts = %d, want 1", creditsIntercepts)
			}
			var terminated *cliproxyexecutor.RequestTerminatedError
			if errors.As(errInvoke, &terminated) {
				t.Fatalf("expected main-round 429 to be preserved when credits candidates are refused, got %v", errInvoke)
			}
			if statusCodeFromError(errInvoke) != http.StatusTooManyRequests {
				t.Fatalf("status = %d (%v), want 429", statusCodeFromError(errInvoke), errInvoke)
			}
		})
	}
}

func TestAfterAuthCredentialTerminateRefusedPersistsAcrossRetryRounds(t *testing.T) {
	for _, homeMode := range []bool{false, true} {
		for _, mode := range []string{"execute", "count", "stream"} {
			t.Run(fmt.Sprintf("home=%t/%s", homeMode, mode), func(t *testing.T) {
				prevTransient := transientErrorCooldownSeconds.Load()
				transientErrorCooldownSeconds.Store(-1)
				t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

				provider := "home-retry-contract"
				if !homeMode {
					provider = fmt.Sprintf("after-auth-persist-refused-%s", mode)
				}
				model := fmt.Sprintf("after-auth-persist-refused-model-%t-%s", homeMode, mode)
				executor := &flakyAfterAuthTestExecutor{
					id:         provider,
					failFirstN: map[string]int{"cred-b": 1},
					failErr:    &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "transient 503"},
				}
				manager := NewManager(nil, &FillFirstSelector{}, nil)
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: homeMode}})
				manager.SetRetryConfig(1, 0, 5)
				manager.RegisterExecutor(executor)
				executions := executionregistry.New()
				if homeMode {
					manager.PublishHomeDispatch(&retryContractHomeDispatcher{authIDs: []string{"cred-a", "cred-b"}}, executions, 1)
				} else {
					for _, id := range []string{"cred-a", "cred-b"} {
						registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
						if _, errReg := manager.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive}); errReg != nil {
							t.Fatal(errReg)
						}
					}
				}

				credAIntercepts := 0
				opts := cliproxyexecutor.Options{
					RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
						authID, _ := req.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string)
						if authID == "cred-a" {
							credAIntercepts++
							if credAIntercepts == 1 {
								return cliproxyexecutor.RequestAfterAuthInterceptResponse{
									Terminate:      true,
									TerminateScope: "credential",
									StatusCode:     http.StatusForbidden,
									ResponseBody:   []byte("cred-a refused"),
								}
							}
						}
						return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
					},
				}

				payload, errInvoke := invokeAfterAuthMode(manager, provider, model, mode, opts)
				if errInvoke != nil {
					t.Fatalf("expected round 1 retry on cred-b to succeed, got error: %v", errInvoke)
				}
				wantPayload := "ok:cred-b"
				if mode == "count" {
					wantPayload = "count:cred-b"
				}
				if string(payload) != wantPayload {
					t.Fatalf("payload = %q, want %q", string(payload), wantPayload)
				}
				if credAIntercepts != 1 {
					t.Fatalf("refused cred-a was re-selected across retry rounds: intercepts = %d, want 1", credAIntercepts)
				}
				executor.mu.Lock()
				credACalls := executor.callsByAuth["cred-a"]
				credBCalls := executor.callsByAuth["cred-b"]
				executor.mu.Unlock()
				if credACalls != 0 || credBCalls != 2 {
					t.Fatalf("executor calls cred-a=%d cred-b=%d, want cred-a=0 cred-b=2", credACalls, credBCalls)
				}
			})
		}
	}
}

func TestAfterAuthCredentialTerminateBeatsLocalPrepareError(t *testing.T) {
	for _, homeMode := range []bool{false, true} {
		for _, mode := range []string{"execute", "count", "stream"} {
			t.Run(fmt.Sprintf("home=%t/%s", homeMode, mode), func(t *testing.T) {
				provider := "home-retry-contract"
				if !homeMode {
					provider = fmt.Sprintf("after-auth-prepare-err-%s", mode)
				}
				model := fmt.Sprintf("after-auth-prepare-err-model-%t-%s", homeMode, mode)
				executor := &afterAuthTestExecutor{
					id: provider,
					prepareErrByAuth: map[string]error{
						"cred-a": &Error{HTTPStatus: http.StatusInternalServerError, Message: "cred-a local prepare failed"},
						"cred-c": &Error{HTTPStatus: http.StatusInternalServerError, Message: "cred-c local prepare failed"},
					},
				}
				manager := NewManager(nil, &FillFirstSelector{}, nil)
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: homeMode}})
				manager.SetRetryConfig(0, 0, 5)
				manager.RegisterExecutor(executor)
				executions := executionregistry.New()
				if homeMode {
					manager.PublishHomeDispatch(&retryContractHomeDispatcher{authIDs: []string{"cred-a", "cred-b", "cred-c"}}, executions, 1)
				} else {
					for _, id := range []string{"cred-a", "cred-b", "cred-c"} {
						registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
						if _, errReg := manager.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive}); errReg != nil {
							t.Fatal(errReg)
						}
					}
				}

				opts := cliproxyexecutor.Options{
					RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
						authID, _ := req.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string)
						if authID == "cred-b" {
							return cliproxyexecutor.RequestAfterAuthInterceptResponse{
								Terminate:       true,
								TerminateScope:  "credential",
								StatusCode:      http.StatusForbidden,
								ResponseHeaders: http.Header{"X-Refused": {"cred-b"}},
								ResponseBody:    []byte("cred-b refused"),
							}
						}
						return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
					},
				}

				_, errInvoke := invokeAfterAuthMode(manager, provider, model, mode, opts)
				var terminated *cliproxyexecutor.RequestTerminatedError
				if !errors.As(errInvoke, &terminated) || terminated == nil {
					t.Fatalf("error = %v (%T), want cred-b *RequestTerminatedError over earlier and later local prepare errors", errInvoke, errInvoke)
				}
				if terminated.StatusCode() != http.StatusForbidden ||
					terminated.ResponseHeaders().Get("X-Refused") != "cred-b" ||
					string(terminated.ResponseBody()) != "cred-b refused" {
					t.Fatalf("terminated = status:%d headers:%v body:%q", terminated.StatusCode(), terminated.ResponseHeaders(), string(terminated.ResponseBody()))
				}
			})
		}
	}
}

func TestAfterAuthCredentialTerminateStreamModelPoolRefusalPersistsAcrossRetryRounds(t *testing.T) {
	prevTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	provider := "openai-compatible-after-auth-stream-pool-persist"
	routeModel := "after-auth-stream-pool-alias"
	model1 := "after-auth-stream-pool-m1"
	model2 := "after-auth-stream-pool-m2"

	executor := &flakyAfterAuthTestExecutor{
		id:         provider,
		failFirstN: map[string]int{"cred-a": 1, "cred-b": 1},
		failErr:    &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "transient 503"},
	}
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name: "pool-a",
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: model1, Alias: routeModel},
					{Name: model2, Alias: routeModel},
				},
			},
			{
				Name: "pool-b",
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: model1, Alias: routeModel},
				},
			},
		},
	})
	manager.SetRetryConfig(1, 0, 5)
	manager.RegisterExecutor(executor)
	for _, spec := range []struct {
		id         string
		compatName string
	}{
		{id: "cred-a", compatName: "pool-a"},
		{id: "cred-b", compatName: "pool-b"},
	} {
		auth := &Auth{
			ID:       spec.id,
			Provider: provider,
			Status:   StatusActive,
			Attributes: map[string]string{
				"api_key":      "test-key-" + spec.id,
				"compat_name":  spec.compatName,
				"provider_key": provider,
			},
		}
		if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
			t.Fatal(errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(spec.id, provider, []*registry.ModelInfo{{ID: routeModel}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(spec.id) })
	}

	credAInterceptModels := make([]string, 0, 2)
	opts := cliproxyexecutor.Options{
		RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			authID, _ := req.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string)
			if authID == "cred-a" {
				credAInterceptModels = append(credAInterceptModels, req.Model)
				if req.Model == model2 {
					return cliproxyexecutor.RequestAfterAuthInterceptResponse{
						Terminate:      true,
						TerminateScope: "credential",
						StatusCode:     http.StatusForbidden,
						ResponseBody:   []byte("cred-a model2 refused"),
					}
				}
			}
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
		},
	}

	payload, errStream := invokeAfterAuthMode(manager, provider, routeModel, "stream", opts)
	if errStream != nil {
		t.Fatalf("expected round 1 retry on cred-b to succeed, got error: %v", errStream)
	}
	if string(payload) != "ok:cred-b" {
		t.Fatalf("payload = %q, want %q", string(payload), "ok:cred-b")
	}
	if len(credAInterceptModels) != 2 || credAInterceptModels[0] != model1 || credAInterceptModels[1] != model2 {
		t.Fatalf("cred-a should not be re-selected in round 2 after model2 refusal: intercepted models = %v, want [%s %s]", credAInterceptModels, model1, model2)
	}
	executor.mu.Lock()
	credACalls := executor.callsByAuth["cred-a"]
	credBCalls := executor.callsByAuth["cred-b"]
	executor.mu.Unlock()
	if credACalls != 1 || credBCalls != 2 {
		t.Fatalf("executor calls cred-a=%d cred-b=%d, want cred-a=1 cred-b=2", credACalls, credBCalls)
	}
}
