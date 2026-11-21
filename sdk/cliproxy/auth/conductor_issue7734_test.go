package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7734OrderedSelector struct {
	order []string
}

func (s *issue7734OrderedSelector) Pick(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	for _, id := range s.order {
		for _, candidate := range auths {
			if candidate != nil && candidate.ID == id {
				return candidate, nil
			}
		}
	}
	return auths[0], nil
}

type issue7734StatusError struct {
	code       int
	msg        string
	retryAfter *time.Duration
}

func (e issue7734StatusError) Error() string              { return e.msg }
func (e issue7734StatusError) StatusCode() int            { return e.code }
func (e issue7734StatusError) RetryAfter() *time.Duration { return e.retryAfter }

type issue7734Executor struct {
	identifier string

	mu            sync.Mutex
	prepareCalls  []string
	streamCalls   []string
	refreshCalls  int
	shouldPrepFn  func(auth *Auth) bool
	prepareAuthFn func(ctx context.Context, auth *Auth) (*Auth, error)
	streamFn      func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, call int) (*cliproxyexecutor.StreamResult, error)
	refreshFn     func(ctx context.Context, auth *Auth) (*Auth, error)
}

func (e *issue7734Executor) Identifier() string { return e.identifier }

func (e *issue7734Executor) ShouldPrepareRequestAuth(auth *Auth) bool {
	if e.shouldPrepFn != nil {
		return e.shouldPrepFn(auth)
	}
	return false
}

func (e *issue7734Executor) PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth != nil {
		e.mu.Lock()
		e.prepareCalls = append(e.prepareCalls, auth.ID)
		e.mu.Unlock()
	}
	if e.prepareAuthFn != nil {
		return e.prepareAuthFn(ctx, auth)
	}
	return auth, nil
}

func (e *issue7734Executor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "Execute not implemented"}
}

func (e *issue7734Executor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	call := len(e.streamCalls)
	e.streamCalls = append(e.streamCalls, auth.ID+"|"+req.Model)
	e.mu.Unlock()
	return e.streamFn(ctx, auth, req, opts, call)
}

func (e *issue7734Executor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	e.refreshCalls++
	e.mu.Unlock()
	if e.refreshFn != nil {
		return e.refreshFn(ctx, auth)
	}
	cloned := auth.Clone()
	if cloned.Metadata == nil {
		cloned.Metadata = make(map[string]any)
	}
	cloned.Metadata["access_token"] = "refreshed-token"
	return cloned, nil
}

func (e *issue7734Executor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *issue7734Executor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *issue7734Executor) RefreshCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refreshCalls
}

func (e *issue7734Executor) StreamCallCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.streamCalls)
}

func (e *issue7734Executor) StreamCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamCalls))
	copy(out, e.streamCalls)
	return out
}

func (e *issue7734Executor) PrepareCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.prepareCalls))
	copy(out, e.prepareCalls)
	return out
}

func assertIssue7734StringSlice(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s[%d] = %q, want %q (full got=%v want=%v)", label, i, got[i], want[i], got, want)
		}
	}
}

func registerIssue7734Auth(t *testing.T, m *Manager, authID, provider, token, model string) *Auth {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          provider,
			"access_token":  token,
			"refresh_token": "issue7734-refresh-" + authID,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", authID, errRegister)
	}
	return auth
}

func assertIssue7734BootstrapResult(t *testing.T, streamRes *cliproxyexecutor.StreamResult, errStream error, wantErr error, wantStatus int, wantTraceID, wantRetryAfter string) {
	t.Helper()
	if errStream != nil {
		t.Fatalf("ExecuteStream() direct error = %v, want error stream carrying *streamBootstrapError", errStream)
	}
	if streamRes == nil || streamRes.Chunks == nil {
		t.Fatalf("ExecuteStream() returned nil stream result, want error stream")
	}
	if got := streamRes.Headers.Get("X-Trace-Id"); got != wantTraceID {
		t.Fatalf("streamRes.Headers.Get(X-Trace-Id) = %q, want %q", got, wantTraceID)
	}
	if wantRetryAfter != "" {
		if got := streamRes.Headers.Get("Retry-After"); got != wantRetryAfter {
			t.Fatalf("streamRes.Headers.Get(Retry-After) = %q, want %q", got, wantRetryAfter)
		}
	}
	var chunkErr error
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil && chunkErr == nil {
			chunkErr = chunk.Err
		}
	}
	if chunkErr == nil {
		t.Fatal("error stream produced no chunk error")
	}
	var bootstrap *streamBootstrapError
	if !errors.As(chunkErr, &bootstrap) || bootstrap == nil {
		t.Fatalf("chunk error %T (%v) does not wrap *streamBootstrapError", chunkErr, chunkErr)
	}
	if wantErr != nil && !errors.Is(chunkErr, wantErr) {
		t.Fatalf("chunk error = %v, want errors.Is(_, %v)", chunkErr, wantErr)
	}
	if got := statusCodeFromError(chunkErr); got != wantStatus {
		t.Fatalf("statusCodeFromError(chunkErr) = %d, want %d", got, wantStatus)
	}
	if got := bootstrap.Headers().Get("X-Trace-Id"); got != wantTraceID {
		t.Fatalf("bootstrap.Headers().Get(X-Trace-Id) = %q, want %q", got, wantTraceID)
	}
	if wantRetryAfter != "" {
		trusted := trustedRetryAfterFromError(chunkErr)
		if trusted == nil || *trusted <= 0 || *trusted > 2*time.Minute {
			t.Fatalf("trustedRetryAfterFromError(chunkErr) = %v, want positive hint from earlier bootstrap", trusted)
		}
	}
}

// TestIssue7734_ExecuteStreamMixedOncePreservesEarlierCredentialBootstrap verifies
// that executeStreamMixedOnce keeps Credential A's *streamBootstrapError and
// upstream HTTP headers when Credential B subsequently fails with a headerless
// stream-start error, an empty_stream, or an upstream prepareRequestAuth error.
func TestIssue7734_ExecuteStreamMixedOncePreservesEarlierCredentialBootstrap(t *testing.T) {
	const (
		provider = "issue7734-multi-cred"
		model    = "issue7734-model"
	)

	retryHintA := 45 * time.Second
	errA := issue7734StatusError{
		code:       http.StatusTooManyRequests,
		msg:        "credential A rate limited",
		retryAfter: &retryHintA,
	}
	headersA := http.Header{
		"Retry-After": []string{"45"},
		"X-Trace-Id":  []string{"trace-cred-a"},
	}

	cases := []struct {
		name             string
		credBShouldPrp   bool
		credBPrepErr     error
		wantStreamCalls  []string
		wantPrepareCalls []string
		credBStreamFn    func(ctx context.Context) (*cliproxyexecutor.StreamResult, error)
	}{
		{
			name:            "headerless stream-start error",
			wantStreamCalls: []string{"cred-a|" + model, "cred-b|" + model},
			credBStreamFn: func(ctx context.Context) (*cliproxyexecutor.StreamResult, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				return nil, &Error{HTTPStatus: http.StatusBadGateway, Code: "upstream_error", Message: "credential B stream start failed", Retryable: true}
			},
		},
		{
			name:            "empty_stream",
			wantStreamCalls: []string{"cred-a|" + model, "cred-b|" + model},
			credBStreamFn: func(ctx context.Context) (*cliproxyexecutor.StreamResult, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				ch := make(chan cliproxyexecutor.StreamChunk)
				close(ch)
				return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Trace-Id": {"trace-cred-b-empty"}}, Chunks: ch}, nil
			},
		},
		{
			name:             "prepareRequestAuth upstream error",
			credBShouldPrp:   true,
			credBPrepErr:     &Error{HTTPStatus: http.StatusServiceUnavailable, Code: "upstream_unavailable", Message: "credential B prepare failed", Retryable: true},
			wantStreamCalls:  []string{"cred-a|" + model},
			wantPrepareCalls: []string{"cred-b"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selector := &issue7734OrderedSelector{order: []string{"cred-a", "cred-b"}}
			m := NewManager(nil, selector, nil)
			m.SetRetryConfig(0, 0, 0)

			registerIssue7734Auth(t, m, "cred-a", provider, "token-a", model)
			registerIssue7734Auth(t, m, "cred-b", provider, "token-b", model)

			exec := &issue7734Executor{
				identifier: provider,
				shouldPrepFn: func(auth *Auth) bool {
					return tc.credBShouldPrp && auth != nil && auth.ID == "cred-b"
				},
				prepareAuthFn: func(ctx context.Context, auth *Auth) (*Auth, error) {
					if tc.credBPrepErr != nil && auth != nil && auth.ID == "cred-b" {
						cliproxyexecutor.MarkUpstreamAttempt(ctx)
						return nil, tc.credBPrepErr
					}
					return auth, nil
				},
				streamFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options, _ int) (*cliproxyexecutor.StreamResult, error) {
					if auth.ID == "cred-a" {
						cliproxyexecutor.MarkUpstreamAttempt(ctx)
						ch := make(chan cliproxyexecutor.StreamChunk, 1)
						ch <- cliproxyexecutor.StreamChunk{Err: errA}
						close(ch)
						return &cliproxyexecutor.StreamResult{Headers: headersA.Clone(), Chunks: ch}, nil
					}
					return tc.credBStreamFn(ctx)
				},
			}
			m.RegisterExecutor(exec)

			streamRes, errStream := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
			assertIssue7734StringSlice(t, "StreamCalls()", exec.StreamCalls(), tc.wantStreamCalls)
			assertIssue7734StringSlice(t, "PrepareCalls()", exec.PrepareCalls(), tc.wantPrepareCalls)
			assertIssue7734BootstrapResult(t, streamRes, errStream, errA, http.StatusTooManyRequests, "trace-cred-a", "45")
		})
	}
}

// TestIssue7734_ExecuteStreamWithModelPoolPreservesEarlierModelBootstrap covers
// all five raw upstreamErr overwrite sites in executeStreamWithModelPool:
// initial stream-start error, post-401-refresh stream-start error, initial/post-refresh
// bootstrap error, and empty_stream.
func TestIssue7734_ExecuteStreamWithModelPoolPreservesEarlierModelBootstrap(t *testing.T) {
	const (
		compatName  = "issue7734-pool"
		providerKey = "openai-compatible-issue7734-pool"
		alias       = "issue7734-pooled-alias"
		modelA      = "issue7734-model-a"
		modelB      = "issue7734-model-b"
	)

	retryHintA := 60 * time.Second
	errModelA := issue7734StatusError{
		code:       http.StatusTooManyRequests,
		msg:        "model A rate limited",
		retryAfter: &retryHintA,
	}
	headersA := http.Header{
		"Retry-After": []string{"60"},
		"X-Trace-Id":  []string{"trace-model-a"},
	}

	foreignHint := 30 * time.Minute

	cases := []struct {
		name             string
		wantRefreshCalls int
		modelBFn         func(ctx context.Context, modelBCall int) (*cliproxyexecutor.StreamResult, error)
	}{
		{
			name: "stream-start error",
			modelBFn: func(ctx context.Context, _ int) (*cliproxyexecutor.StreamResult, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				return nil, issue7734StatusError{code: http.StatusBadGateway, msg: "model B stream start failed", retryAfter: &foreignHint}
			},
		},
		{
			name:             "stream-start 401 refresh retry error",
			wantRefreshCalls: 1,
			modelBFn: func(ctx context.Context, modelBCall int) (*cliproxyexecutor.StreamResult, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				if modelBCall == 0 {
					return nil, &Error{HTTPStatus: http.StatusUnauthorized, Code: "unauthorized", Message: "model B token expired", Retryable: true}
				}
				return nil, issue7734StatusError{code: http.StatusServiceUnavailable, msg: "model B post-refresh stream start failed", retryAfter: &foreignHint}
			},
		},
		{
			name: "later bootstrap error",
			modelBFn: func(ctx context.Context, _ int) (*cliproxyexecutor.StreamResult, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Err: issue7734StatusError{code: http.StatusInternalServerError, msg: "model B bootstrap 500", retryAfter: &foreignHint}}
				close(ch)
				return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Trace-Id": {"trace-model-b"}}, Chunks: ch}, nil
			},
		},
		{
			name:             "later bootstrap 401 refresh retry error",
			wantRefreshCalls: 1,
			modelBFn: func(ctx context.Context, modelBCall int) (*cliproxyexecutor.StreamResult, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				if modelBCall == 0 {
					ch <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusUnauthorized, Code: "unauthorized", Message: "model B bootstrap 401", Retryable: true}}
				} else {
					ch <- cliproxyexecutor.StreamChunk{Err: issue7734StatusError{code: http.StatusServiceUnavailable, msg: "model B post-refresh bootstrap 503", retryAfter: &foreignHint}}
				}
				close(ch)
				return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Trace-Id": {"trace-model-b-refresh"}}, Chunks: ch}, nil
			},
		},
		{
			name: "empty_stream",
			modelBFn: func(ctx context.Context, _ int) (*cliproxyexecutor.StreamResult, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				ch := make(chan cliproxyexecutor.StreamChunk)
				close(ch)
				return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Trace-Id": {"trace-model-b-empty"}}, Chunks: ch}, nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &internalconfig.Config{
				OpenAICompatibility: []internalconfig.OpenAICompatibility{{
					Name: compatName,
					Models: []internalconfig.OpenAICompatibilityModel{
						{Name: modelA, Alias: alias},
						{Name: modelB, Alias: alias},
					},
				}},
			}
			m := NewManager(nil, nil, nil)
			m.SetConfig(cfg)
			m.SetRetryConfig(0, 0, 0)

			var modelBMu sync.Mutex
			modelBCalls := 0
			exec := &issue7734Executor{
				identifier: providerKey,
				streamFn: func(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options, _ int) (*cliproxyexecutor.StreamResult, error) {
					if req.Model == modelA {
						cliproxyexecutor.MarkUpstreamAttempt(ctx)
						ch := make(chan cliproxyexecutor.StreamChunk, 1)
						ch <- cliproxyexecutor.StreamChunk{Err: errModelA}
						close(ch)
						return &cliproxyexecutor.StreamResult{Headers: headersA.Clone(), Chunks: ch}, nil
					}
					modelBMu.Lock()
					idx := modelBCalls
					modelBCalls++
					modelBMu.Unlock()
					return tc.modelBFn(ctx, idx)
				},
			}
			m.RegisterExecutor(exec)

			auth := &Auth{
				ID:       "issue7734-pool-auth-" + t.Name(),
				Provider: providerKey,
				Status:   StatusActive,
				Attributes: map[string]string{
					"api_key":      "test-key",
					"compat_name":  compatName,
					"provider_key": providerKey,
				},
				Metadata: map[string]any{
					"access_token":  "initial-pool-token",
					"refresh_token": "pool-refresh-token",
				},
			}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, providerKey, []*registry.ModelInfo{{ID: alias}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

			streamRes, errStream := m.ExecuteStream(context.Background(), []string{providerKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{Stream: true})
			wantStreamCalls := []string{auth.ID + "|" + modelA, auth.ID + "|" + modelB}
			if tc.wantRefreshCalls > 0 {
				wantStreamCalls = append(wantStreamCalls, auth.ID+"|"+modelB)
			}
			assertIssue7734StringSlice(t, "StreamCalls()", exec.StreamCalls(), wantStreamCalls)
			assertIssue7734BootstrapResult(t, streamRes, errStream, errModelA, http.StatusTooManyRequests, "trace-model-a", "60")
			if got := exec.RefreshCalls(); got != tc.wantRefreshCalls {
				t.Fatalf("RefreshCalls() = %d, want %d", got, tc.wantRefreshCalls)
			}
		})
	}
}

// TestIssue7734_Bootstrap401RefreshUsesPostRefreshOutcome ensures that an
// upstream-marked 401 bootstrap error followed by a successful token refresh
// does not lock upstreamErr to the stale pre-refresh 401 when the post-refresh
// retry fails with a real 429 or closes with empty_stream.
func TestIssue7734_Bootstrap401RefreshUsesPostRefreshOutcome(t *testing.T) {
	const (
		provider = "issue7734-bootstrap-401"
		model    = "issue7734-401-model"
	)

	errPost429 := &Error{HTTPStatus: http.StatusTooManyRequests, Code: "rate_limited", Message: "post-refresh 429", Retryable: true}

	cases := []struct {
		name          string
		postRefreshFn func(ctx context.Context) (*cliproxyexecutor.StreamResult, error)
		wantErr       error
		wantStatus    int
		wantCode      string
		wantTraceID   string
	}{
		{
			name: "post-refresh 429 bootstrap",
			postRefreshFn: func(ctx context.Context) (*cliproxyexecutor.StreamResult, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Err: errPost429}
				close(ch)
				return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Trace-Id": {"post-refresh-429"}}, Chunks: ch}, nil
			},
			wantErr:     errPost429,
			wantStatus:  http.StatusTooManyRequests,
			wantTraceID: "post-refresh-429",
		},
		{
			name: "post-refresh empty_stream",
			postRefreshFn: func(ctx context.Context) (*cliproxyexecutor.StreamResult, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				ch := make(chan cliproxyexecutor.StreamChunk)
				close(ch)
				return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Trace-Id": {"post-refresh-empty"}}, Chunks: ch}, nil
			},
			wantCode:    "empty_stream",
			wantTraceID: "post-refresh-empty",
		},
		{
			name: "post-refresh local failure falls back to pre-refresh 401",
			postRefreshFn: func(_ context.Context) (*cliproxyexecutor.StreamResult, error) {
				return nil, errors.New("local retry error before upstream")
			},
			wantStatus:  http.StatusUnauthorized,
			wantCode:    "unauthorized",
			wantTraceID: "pre-refresh-401",
		},
		{
			name: "post-refresh local empty_stream falls back to pre-refresh 401",
			postRefreshFn: func(_ context.Context) (*cliproxyexecutor.StreamResult, error) {
				ch := make(chan cliproxyexecutor.StreamChunk)
				close(ch)
				return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Trace-Id": {"local-empty"}}, Chunks: ch}, nil
			},
			wantStatus:  http.StatusUnauthorized,
			wantCode:    "unauthorized",
			wantTraceID: "pre-refresh-401",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			m.SetRetryConfig(0, 0, 0)
			registerIssue7734Auth(t, m, "cred-401-"+t.Name(), provider, "expired-token", model)

			exec := &issue7734Executor{
				identifier: provider,
				streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options, call int) (*cliproxyexecutor.StreamResult, error) {
					if call == 0 {
						cliproxyexecutor.MarkUpstreamAttempt(ctx)
						ch := make(chan cliproxyexecutor.StreamChunk, 1)
						ch <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusUnauthorized, Code: "unauthorized", Message: "pre-refresh 401"}}
						close(ch)
						return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Trace-Id": {"pre-refresh-401"}}, Chunks: ch}, nil
					}
					return tc.postRefreshFn(ctx)
				},
			}
			m.RegisterExecutor(exec)

			streamRes, errStream := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
			if errStream != nil {
				t.Fatalf("ExecuteStream() direct error = %v, want error stream", errStream)
			}
			if streamRes == nil || streamRes.Chunks == nil {
				t.Fatal("ExecuteStream() returned nil stream result")
			}
			if got := streamRes.Headers.Get("X-Trace-Id"); got != tc.wantTraceID {
				t.Fatalf("streamRes.Headers.Get(X-Trace-Id) = %q, want %q", got, tc.wantTraceID)
			}
			var chunkErr error
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil && chunkErr == nil {
					chunkErr = chunk.Err
				}
			}
			if chunkErr == nil {
				t.Fatal("error stream produced no chunk error")
			}
			if tc.wantErr != nil && !errors.Is(chunkErr, tc.wantErr) {
				t.Fatalf("chunk error = %v, want %v", chunkErr, tc.wantErr)
			}
			if tc.wantStatus != 0 {
				if got := statusCodeFromError(chunkErr); got != tc.wantStatus {
					t.Fatalf("statusCodeFromError = %d, want %d", got, tc.wantStatus)
				}
			}
			if tc.wantCode != "" {
				var authErr *Error
				if !errors.As(chunkErr, &authErr) || authErr.Code != tc.wantCode {
					t.Fatalf("chunk error = %v, want *Error with Code=%q", chunkErr, tc.wantCode)
				}
			}
			if got := exec.RefreshCalls(); got != 1 {
				t.Fatalf("RefreshCalls() = %d, want 1", got)
			}
		})
	}
}

// TestIssue7734_ExecuteStreamPreservesBootstrapAcrossRetryRounds verifies that
// the outer ExecuteStream retry loop keeps an earlier round's *streamBootstrapError
// and headers rather than overwriting preferredUpstreamErr with a later round's
// headerless stream-start failure.
func TestIssue7734_ExecuteStreamPreservesBootstrapAcrossRetryRounds(t *testing.T) {
	const (
		provider = "issue7734-retry-rounds"
		model    = "issue7734-retry-model"
	)

	errRound0 := &Error{HTTPStatus: http.StatusServiceUnavailable, Code: "upstream_unavailable", Message: "round 0 bootstrap 503", Retryable: true}
	headersRound0 := http.Header{"X-Trace-Id": {"trace-round-0"}}

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(1, 0, 0)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("cred-round", provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient("cred-round") })
	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:       "cred-round",
		Provider: provider,
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":            provider,
			"access_token":    "token-round",
			"request_retry":   1,
			"disable_cooling": true,
		},
	}); errRegister != nil {
		t.Fatalf("Register(cred-round) error = %v", errRegister)
	}

	exec := &issue7734Executor{
		identifier: provider,
		streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options, call int) (*cliproxyexecutor.StreamResult, error) {
			cliproxyexecutor.MarkUpstreamAttempt(ctx)
			if call == 0 {
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Err: errRound0}
				close(ch)
				return &cliproxyexecutor.StreamResult{Headers: headersRound0.Clone(), Chunks: ch}, nil
			}
			return nil, &Error{HTTPStatus: http.StatusBadGateway, Code: "upstream_error", Message: "round 1 headerless stream-start 502", Retryable: true}
		},
	}
	m.RegisterExecutor(exec)

	streamRes, errStream := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if got := exec.StreamCallCount(); got != 2 {
		t.Fatalf("StreamCallCount() = %d, want 2 (round 0 bootstrap + round 1 stream-start)", got)
	}
	assertIssue7734BootstrapResult(t, streamRes, errStream, errRound0, http.StatusServiceUnavailable, "trace-round-0", "")
}

// TestIssue7734_EmptyStreamDoesNotMaskLaterRealBootstrapError verifies that an
// earlier synthetic empty_stream wrapper does not prevent a subsequent real
// upstream bootstrap failure (with response headers) from becoming preferred.
func TestIssue7734_EmptyStreamDoesNotMaskLaterRealBootstrapError(t *testing.T) {
	const (
		provider = "issue7734-empty-then-429"
		model    = "issue7734-empty-then-429-model"
	)

	retryHintB := 30 * time.Second
	errB := issue7734StatusError{
		code:       http.StatusTooManyRequests,
		msg:        "credential B rate limited",
		retryAfter: &retryHintB,
	}
	headersB := http.Header{
		"Retry-After": []string{"30"},
		"X-Trace-Id":  []string{"trace-cred-b"},
	}

	selector := &issue7734OrderedSelector{order: []string{"cred-a", "cred-b"}}
	m := NewManager(nil, selector, nil)
	m.SetRetryConfig(0, 0, 0)
	registerIssue7734Auth(t, m, "cred-a", provider, "token-a", model)
	registerIssue7734Auth(t, m, "cred-b", provider, "token-b", model)

	exec := &issue7734Executor{
		identifier: provider,
		streamFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options, _ int) (*cliproxyexecutor.StreamResult, error) {
			cliproxyexecutor.MarkUpstreamAttempt(ctx)
			if auth.ID == "cred-a" {
				ch := make(chan cliproxyexecutor.StreamChunk)
				close(ch)
				return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Trace-Id": {"trace-cred-a-empty"}}, Chunks: ch}, nil
			}
			ch := make(chan cliproxyexecutor.StreamChunk, 1)
			ch <- cliproxyexecutor.StreamChunk{Err: errB}
			close(ch)
			return &cliproxyexecutor.StreamResult{Headers: headersB.Clone(), Chunks: ch}, nil
		},
	}
	m.RegisterExecutor(exec)

	streamRes, errStream := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	assertIssue7734StringSlice(t, "StreamCalls()", exec.StreamCalls(), []string{"cred-a|" + model, "cred-b|" + model})
	assertIssue7734BootstrapResult(t, streamRes, errStream, errB, http.StatusTooManyRequests, "trace-cred-b", "30")
}

// TestIssue7734_ExecuteStreamDirectInvalidRequestOverridesEarlierRoundBootstrap
// verifies that when round 0 fails with a retryable bootstrap 503 (+ headers)
// and round 1 returns a request-invalid 400 (either direct stream-start or
// bootstrap wrapped in streamHeaderError), ExecuteStream returns the terminal
// 400 directly instead of turning it into round 0's 503 error stream.
func TestIssue7734_ExecuteStreamDirectInvalidRequestOverridesEarlierRoundBootstrap(t *testing.T) {
	const (
		provider = "issue7734-round-invalid"
		model    = "issue7734-round-invalid-model"
	)

	errRound0 := &Error{HTTPStatus: http.StatusServiceUnavailable, Code: "upstream_unavailable", Message: "round 0 bootstrap 503", Retryable: true}
	errRound1 := NewRequestScopedError("round 1 invalid request", http.StatusBadRequest)

	cases := []struct {
		name        string
		round1Fn    func(ctx context.Context) (*cliproxyexecutor.StreamResult, error)
		wantTraceID string
	}{
		{
			name: "bootstrap 400 with headers",
			round1Fn: func(ctx context.Context) (*cliproxyexecutor.StreamResult, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Err: errRound1}
				close(ch)
				return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Trace-Id": {"trace-round-1-400"}}, Chunks: ch}, nil
			},
			wantTraceID: "trace-round-1-400",
		},
		{
			name: "direct stream-start 400",
			round1Fn: func(ctx context.Context) (*cliproxyexecutor.StreamResult, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				return nil, errRound1
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			m.SetRetryConfig(1, 0, 0)
			authID := "cred-invalid-" + tc.name
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(authID) })
			if _, errRegister := m.Register(context.Background(), &Auth{
				ID:       authID,
				Provider: provider,
				Status:   StatusActive,
				Metadata: map[string]any{
					"type":            provider,
					"access_token":    "token-round-invalid",
					"request_retry":   1,
					"disable_cooling": true,
				},
			}); errRegister != nil {
				t.Fatalf("Register(%s) error = %v", authID, errRegister)
			}

			exec := &issue7734Executor{
				identifier: provider,
				streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options, call int) (*cliproxyexecutor.StreamResult, error) {
					if call == 0 {
						cliproxyexecutor.MarkUpstreamAttempt(ctx)
						ch := make(chan cliproxyexecutor.StreamChunk, 1)
						ch <- cliproxyexecutor.StreamChunk{Err: errRound0}
						close(ch)
						return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Trace-Id": {"trace-round-0"}}, Chunks: ch}, nil
					}
					return tc.round1Fn(ctx)
				},
			}
			m.RegisterExecutor(exec)

			streamRes, errStream := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
			assertIssue7734StringSlice(t, "StreamCalls()", exec.StreamCalls(), []string{authID + "|" + model, authID + "|" + model})
			if streamRes != nil {
				t.Fatalf("ExecuteStream() streamRes = %+v, want nil (terminal 400 must stay a direct error)", streamRes)
			}
			if errStream == nil {
				t.Fatal("ExecuteStream() error = nil, want direct 400 error")
			}
			if !errors.Is(errStream, errRound1) {
				t.Fatalf("ExecuteStream() error = %v, want %v", errStream, errRound1)
			}
			if got := statusCodeFromError(errStream); got != http.StatusBadRequest {
				t.Fatalf("statusCodeFromError(errStream) = %d, want 400", got)
			}
			var bootstrap *streamBootstrapError
			if errors.As(errStream, &bootstrap) && bootstrap != nil {
				t.Fatalf("ExecuteStream() error must not wrap *streamBootstrapError: %T", bootstrap)
			}
			if tc.wantTraceID != "" {
				var headerCarrier interface{ Headers() http.Header }
				if !errors.As(errStream, &headerCarrier) || headerCarrier == nil {
					t.Fatalf("ExecuteStream() error %T does not expose Headers()", errStream)
				}
				if got := headerCarrier.Headers().Get("X-Trace-Id"); got != tc.wantTraceID {
					t.Fatalf("Headers().Get(X-Trace-Id) = %q, want %q", got, tc.wantTraceID)
				}
			}
		})
	}
}
