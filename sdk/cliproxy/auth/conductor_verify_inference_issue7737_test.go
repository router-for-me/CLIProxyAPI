package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7737RoundTripper struct {
	name string
}

func (rt *issue7737RoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected live http call in unit test")
}

type issue7737RoundTripperProvider struct {
	mu    sync.Mutex
	calls int
	rt    http.RoundTripper
}

func (p *issue7737RoundTripperProvider) RoundTripperFor(*Auth) http.RoundTripper {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.rt
}

func (p *issue7737RoundTripperProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type issue7737ResultHook struct {
	mu      sync.Mutex
	results []Result
}

func (*issue7737ResultHook) OnAuthRegistered(context.Context, *Auth) {}
func (*issue7737ResultHook) OnAuthUpdated(context.Context, *Auth)    {}
func (h *issue7737ResultHook) OnResult(_ context.Context, res Result) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.results = append(h.results, res)
}

func (h *issue7737ResultHook) snapshot() []Result {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Result, len(h.results))
	copy(out, h.results)
	return out
}

type issue7737VerifyExecutor struct {
	id string

	mu sync.Mutex

	shouldPrepare func(*Auth) bool
	onPrepare     func(context.Context, *Auth) (*Auth, error)
	onStream      func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
	onExecute     func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	onRefresh     func(context.Context, *Auth) (*Auth, error)

	prepareCalls atomic.Int32
	streamCalls  atomic.Int32
	executeCalls atomic.Int32
	refreshCalls atomic.Int32

	lastModel      string
	lastPayload    []byte
	lastStreamAuth *Auth
}

func (e *issue7737VerifyExecutor) Identifier() string { return e.id }

func (e *issue7737VerifyExecutor) ShouldPrepareRequestAuth(auth *Auth) bool {
	if e.shouldPrepare != nil {
		return e.shouldPrepare(auth)
	}
	return false
}

func (e *issue7737VerifyExecutor) PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error) {
	e.prepareCalls.Add(1)
	if e.onPrepare != nil {
		return e.onPrepare(ctx, auth)
	}
	return auth, nil
}

func (e *issue7737VerifyExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls.Add(1)
	e.mu.Lock()
	e.lastModel = req.Model
	e.lastPayload = append([]byte(nil), req.Payload...)
	e.mu.Unlock()
	if e.onExecute != nil {
		return e.onExecute(ctx, auth, req, opts)
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"id":"resp_ok","status":"completed"}`)}, nil
}

func issue7737CompletedStream() <-chan cliproxyexecutor.StreamChunk {
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n")}
	close(ch)
	return ch
}

func (e *issue7737VerifyExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls.Add(1)
	e.mu.Lock()
	e.lastModel = req.Model
	e.lastPayload = append([]byte(nil), req.Payload...)
	if auth != nil {
		e.lastStreamAuth = auth.Clone()
	}
	e.mu.Unlock()
	if e.onStream != nil {
		return e.onStream(ctx, auth, req, opts)
	}
	return &cliproxyexecutor.StreamResult{Chunks: issue7737CompletedStream()}, nil
}

func (e *issue7737VerifyExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	if e.onRefresh != nil {
		return e.onRefresh(ctx, auth)
	}
	return auth, nil
}

func (*issue7737VerifyExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (*issue7737VerifyExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestVerifyInference_AttachesRoundTripperAndPreparesRequestAuth_Issue7737(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	customRT := &issue7737RoundTripper{name: "ag-custom-rt"}
	rtProvider := &issue7737RoundTripperProvider{rt: customRT}

	var (
		prepareSawKeyRT    bool
		prepareSawStringRT bool
		streamSawKeyRT     bool
		streamSawStringRT  bool
		streamProjectID    string
		streamAccessToken  string
		prepareRanBefore   bool
	)

	exec := &issue7737VerifyExecutor{
		id: "antigravity",
		shouldPrepare: func(auth *Auth) bool {
			if auth == nil || auth.Metadata == nil {
				return true
			}
			pid, _ := auth.Metadata["project_id"].(string)
			return pid == ""
		},
	}
	exec.onPrepare = func(prepareCtx context.Context, auth *Auth) (*Auth, error) {
		prepareSawKeyRT = prepareCtx.Value(roundTripperContextKey{}) == customRT
		prepareSawStringRT = prepareCtx.Value("cliproxy.roundtripper") == customRT
		updated := auth.Clone()
		if updated.Metadata == nil {
			updated.Metadata = make(map[string]any)
		}
		updated.Metadata["project_id"] = "ag-proj-7737"
		updated.Metadata["access_token"] = "fresh-ag-access-token"
		return updated, nil
	}
	exec.onStream = func(streamCtx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
		prepareRanBefore = exec.prepareCalls.Load() == 1
		streamSawKeyRT = streamCtx.Value(roundTripperContextKey{}) == customRT
		streamSawStringRT = streamCtx.Value("cliproxy.roundtripper") == customRT
		if auth != nil && auth.Metadata != nil {
			streamProjectID, _ = auth.Metadata["project_id"].(string)
			streamAccessToken, _ = auth.Metadata["access_token"].(string)
		}
		ch := make(chan cliproxyexecutor.StreamChunk, 1)
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n")}
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}

	m := NewManager(nil, nil, nil)
	m.SetRoundTripperProvider(rtProvider)
	m.RegisterExecutor(exec)

	authID := "ag-verify-prepare-" + uuid.NewString()
	auth := &Auth{
		ID:       authID,
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "antigravity",
			"access_token":  "stale-ag-access-token",
			"refresh_token": "ag-refresh-token",
		},
	}
	if _, errReg := m.Register(ctx, auth); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: "gemini-3.8-flash-high"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	verified, errVerify := m.VerifyInference(ctx, authID, "")
	if errVerify != nil {
		t.Fatalf("VerifyInference error = %v, want nil", errVerify)
	}
	if rtProvider.callCount() == 0 {
		t.Fatal("expected RoundTripperProvider.RoundTripperFor to be called")
	}
	if exec.prepareCalls.Load() != 1 {
		t.Fatalf("PrepareRequestAuth calls = %d, want 1", exec.prepareCalls.Load())
	}
	if !prepareRanBefore {
		t.Fatal("PrepareRequestAuth must run before ExecuteStream")
	}
	if !prepareSawKeyRT || !prepareSawStringRT {
		t.Fatalf("PrepareRequestAuth context missing roundTripper: key=%v string=%v", prepareSawKeyRT, prepareSawStringRT)
	}
	if !streamSawKeyRT || !streamSawStringRT {
		t.Fatalf("ExecuteStream context missing roundTripper: key=%v string=%v", streamSawKeyRT, streamSawStringRT)
	}
	if streamProjectID != "ag-proj-7737" || streamAccessToken != "fresh-ag-access-token" {
		t.Fatalf("ExecuteStream received project_id=%q access_token=%q, want prepared values", streamProjectID, streamAccessToken)
	}
	if verified == nil || !verified.InferenceVerified {
		t.Fatalf("expected InferenceVerified=true after VerifyInference, got %+v", verified)
	}
}

func TestVerifyInference_RefreshesAfterUnauthorized_Issue7737(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	customRT := &issue7737RoundTripper{name: "ag-401-rt"}
	rtProvider := &issue7737RoundTripperProvider{rt: customRT}

	var refreshSawRT bool
	exec := &issue7737VerifyExecutor{id: "antigravity"}
	exec.onRefresh = func(refreshCtx context.Context, auth *Auth) (*Auth, error) {
		refreshSawRT = refreshCtx.Value("cliproxy.roundtripper") == customRT
		updated := auth.Clone()
		updated.Metadata["access_token"] = "rotated-ag-access-token"
		return updated, nil
	}
	exec.onStream = func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
		tok, _ := auth.Metadata["access_token"].(string)
		if tok != "rotated-ag-access-token" {
			return nil, &Error{Code: "unauthorized", Message: "token expired", HTTPStatus: http.StatusUnauthorized}
		}
		ch := make(chan cliproxyexecutor.StreamChunk, 1)
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n")}
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}

	m := NewManager(nil, nil, nil)
	m.SetRoundTripperProvider(rtProvider)
	m.RegisterExecutor(exec)

	authID := "ag-verify-401-" + uuid.NewString()
	auth := &Auth{
		ID:       authID,
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "antigravity",
			"project_id":    "ag-proj-existing",
			"access_token":  "revoked-ag-access-token",
			"refresh_token": "ag-refresh-token",
		},
	}
	if _, errReg := m.Register(ctx, auth); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: "gemini-3.8-flash-high"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	verified, errVerify := m.VerifyInference(ctx, authID, "")
	if errVerify != nil {
		t.Fatalf("VerifyInference error = %v, want nil after 401 refresh", errVerify)
	}
	if exec.refreshCalls.Load() != 1 {
		t.Fatalf("Refresh calls = %d, want 1", exec.refreshCalls.Load())
	}
	if !refreshSawRT {
		t.Fatal("Refresh context missing cliproxy.roundtripper")
	}
	if exec.streamCalls.Load() != 2 {
		t.Fatalf("ExecuteStream calls = %d, want 2 (initial 401 + post-refresh retry)", exec.streamCalls.Load())
	}
	if verified == nil || !verified.InferenceVerified {
		t.Fatalf("expected InferenceVerified=true after 401 refresh retry, got %+v", verified)
	}
}

func TestVerifyInference_PrepareFailureRecordsResultAndSkipsStream_Issue7737(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	hook := &issue7737ResultHook{}
	exec := &issue7737VerifyExecutor{
		id:            "antigravity",
		shouldPrepare: func(*Auth) bool { return true },
		onPrepare: func(context.Context, *Auth) (*Auth, error) {
			return nil, &Error{Code: "prepare_failed", Message: "missing project_id", HTTPStatus: http.StatusServiceUnavailable}
		},
	}

	m := NewManager(nil, nil, hook)
	m.RegisterExecutor(exec)

	authID := "ag-verify-prepare-fail-" + uuid.NewString()
	auth := &Auth{
		ID:       authID,
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{"type": "antigravity", "access_token": "at"},
	}
	if _, errReg := m.Register(ctx, auth); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}

	_, errVerify := m.VerifyInference(ctx, authID, "gemini-3.8-flash-high")
	if errVerify == nil {
		t.Fatal("expected PrepareRequestAuth error")
	}
	if exec.streamCalls.Load() != 0 {
		t.Fatalf("ExecuteStream calls = %d, want 0 after PrepareRequestAuth failure", exec.streamCalls.Load())
	}
	results := hook.snapshot()
	if len(results) != 1 || results[0].Success {
		t.Fatalf("expected 1 failed MarkResult after PrepareRequestAuth failure, got %+v", results)
	}
}

func TestVerifyInference_RefreshesAfterUnauthorizedOnFallbackAndChunk_Issue7737(t *testing.T) {
	t.Run("501 fallback Execute retries once after 401 refresh", func(t *testing.T) {
		ctx := WithSkipPersist(context.Background())
		exec := &issue7737VerifyExecutor{id: "antigravity"}
		exec.onStream = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			return nil, &Error{Code: "not_implemented", Message: "stream unsupported", HTTPStatus: http.StatusNotImplemented}
		}
		exec.onRefresh = func(_ context.Context, auth *Auth) (*Auth, error) {
			updated := auth.Clone()
			updated.Metadata["access_token"] = "fresh-fallback-at"
			return updated, nil
		}
		exec.onExecute = func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			tok, _ := auth.Metadata["access_token"].(string)
			if tok != "fresh-fallback-at" {
				return cliproxyexecutor.Response{}, &Error{Code: "unauthorized", Message: "expired", HTTPStatus: http.StatusUnauthorized}
			}
			return cliproxyexecutor.Response{Payload: []byte(`{"id":"resp_ok","status":"completed"}`)}, nil
		}

		m := NewManager(nil, nil, nil)
		m.RegisterExecutor(exec)

		authID := "ag-verify-401-fallback-" + uuid.NewString()
		auth := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"type": "antigravity", "project_id": "ag-proj-1", "access_token": "stale-at", "refresh_token": "rt"},
		}
		if _, errReg := m.Register(ctx, auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		verified, errVerify := m.VerifyInference(ctx, authID, "gemini-3.8-flash-high")
		if errVerify != nil {
			t.Fatalf("VerifyInference error = %v, want nil", errVerify)
		}
		if exec.refreshCalls.Load() != 1 || exec.executeCalls.Load() != 2 {
			t.Fatalf("refreshCalls=%d executeCalls=%d, want 1 and 2", exec.refreshCalls.Load(), exec.executeCalls.Load())
		}
		if verified == nil || !verified.InferenceVerified {
			t.Fatalf("expected InferenceVerified=true, got %+v", verified)
		}
	})

	t.Run("stream chunk 401 retries once after refresh", func(t *testing.T) {
		ctx := WithSkipPersist(context.Background())
		exec := &issue7737VerifyExecutor{id: "antigravity"}
		exec.onRefresh = func(_ context.Context, auth *Auth) (*Auth, error) {
			updated := auth.Clone()
			updated.Metadata["access_token"] = "fresh-chunk-at"
			return updated, nil
		}
		exec.onStream = func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			ch := make(chan cliproxyexecutor.StreamChunk, 1)
			tok, _ := auth.Metadata["access_token"].(string)
			if tok != "fresh-chunk-at" {
				ch <- cliproxyexecutor.StreamChunk{Err: &Error{Code: "unauthorized", Message: "expired chunk", HTTPStatus: http.StatusUnauthorized}}
			} else {
				ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n")}
			}
			close(ch)
			return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
		}

		m := NewManager(nil, nil, nil)
		m.RegisterExecutor(exec)

		authID := "ag-verify-401-chunk-" + uuid.NewString()
		auth := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"type": "antigravity", "project_id": "ag-proj-1", "access_token": "stale-at", "refresh_token": "rt"},
		}
		if _, errReg := m.Register(ctx, auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		verified, errVerify := m.VerifyInference(ctx, authID, "gemini-3.8-flash-high")
		if errVerify != nil {
			t.Fatalf("VerifyInference error = %v, want nil", errVerify)
		}
		if exec.refreshCalls.Load() != 1 || exec.streamCalls.Load() != 2 {
			t.Fatalf("refreshCalls=%d streamCalls=%d, want 1 and 2", exec.refreshCalls.Load(), exec.streamCalls.Load())
		}
		if verified == nil || !verified.InferenceVerified {
			t.Fatalf("expected InferenceVerified=true, got %+v", verified)
		}
	})
}

func TestVerifyInference_EmptyRegistryAntigravityDoesNotSendGPT5_Issue7737(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	hook := &issue7737ResultHook{}
	exec := &issue7737VerifyExecutor{id: "antigravity"}

	m := NewManager(nil, nil, hook)
	m.RegisterExecutor(exec)

	authID := "ag-verify-empty-models-" + uuid.NewString()
	auth := &Auth{
		ID:       authID,
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"project_id":   "ag-proj-1",
			"access_token": "valid-at",
		},
	}
	if _, errReg := m.Register(ctx, auth); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}

	_, errVerify := m.VerifyInference(ctx, authID, "")
	if exec.streamCalls.Load() != 0 || exec.executeCalls.Load() != 0 {
		t.Fatalf("expected no upstream execution with empty antigravity registry, got streamCalls=%d executeCalls=%d lastModel=%q",
			exec.streamCalls.Load(), exec.executeCalls.Load(), exec.lastModel)
	}
	if errVerify == nil {
		t.Fatal("expected validation error when no model is specified and no models are registered for antigravity")
	}
	var authErr *Error
	if !errors.As(errVerify, &authErr) || authErr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("expected 400 *Error, got %T (%v)", errVerify, errVerify)
	}
	if results := hook.snapshot(); len(results) != 0 {
		t.Fatalf("expected no MarkResult failure recorded for missing model validation error, got %d results", len(results))
	}
}

func TestVerifyInference_ModelSelectionAndThinkingBudget_Issue7737(t *testing.T) {
	ctx := WithSkipPersist(context.Background())

	t.Run("prefers non-thinking model over thinking model when model omitted", func(t *testing.T) {
		exec := &issue7737VerifyExecutor{id: "antigravity"}
		m := NewManager(nil, nil, nil)
		m.RegisterExecutor(exec)

		authID := "ag-verify-model-pick-" + uuid.NewString()
		auth := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"type": "antigravity", "project_id": "ag-proj-1", "access_token": "at"},
		}
		if _, errReg := m.Register(ctx, auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{
			{ID: "claude-opus-4-6-thinking", Thinking: &registry.ThinkingSupport{Min: 1024, Max: 64000, ZeroAllowed: true}},
			{ID: "gemini-3.1-flash-image", Thinking: &registry.ThinkingSupport{Min: 128, Max: 32768}},
			{ID: "gpt-oss-120b-medium"},
		})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

		if _, errVerify := m.VerifyInference(ctx, authID, ""); errVerify != nil {
			t.Fatalf("VerifyInference error = %v", errVerify)
		}
		if exec.lastModel != "gpt-oss-120b-medium" {
			t.Fatalf("VerifyInference selected model %q, want non-thinking gpt-oss-120b-medium", exec.lastModel)
		}
	})

	t.Run("non-codex thinking model does not enforce 16-token max_output_tokens cap", func(t *testing.T) {
		exec := &issue7737VerifyExecutor{id: "antigravity"}
		m := NewManager(nil, nil, nil)
		m.RegisterExecutor(exec)

		authID := "ag-verify-budget-" + uuid.NewString()
		auth := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"type": "antigravity", "project_id": "ag-proj-1", "access_token": "at"},
		}
		if _, errReg := m.Register(ctx, auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{
			{ID: "claude-opus-5-5-high", Thinking: &registry.ThinkingSupport{Min: 1024, Max: 64000, ZeroAllowed: true, Levels: []string{"low", "medium", "high"}}},
		})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

		if _, errVerify := m.VerifyInference(ctx, authID, "claude-opus-5-5-high"); errVerify != nil {
			t.Fatalf("VerifyInference error = %v", errVerify)
		}
		if maxOut := ojson.GetBytes(exec.lastPayload, "max_output_tokens"); maxOut.Exists() && maxOut.Int() <= 16 {
			t.Fatalf("antigravity thinking model verification must not enforce <=16 max_output_tokens cap, got payload=%s", string(exec.lastPayload))
		}
		if ojson.GetBytes(exec.lastPayload, "store").Bool() {
			t.Fatalf("expected store=false in payload=%s", string(exec.lastPayload))
		}
	})

	t.Run("codex retains max_output_tokens=16 floor", func(t *testing.T) {
		exec := &issue7737VerifyExecutor{id: "codex"}
		m := NewManager(nil, nil, nil)
		m.RegisterExecutor(exec)

		authID := "codex-verify-budget-" + uuid.NewString()
		auth := &Auth{
			ID:       authID,
			Provider: "codex",
			Status:   StatusActive,
			Metadata: map[string]any{"type": "codex", "access_token": "at"},
		}
		if _, errReg := m.Register(ctx, auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "gpt-5.6-sol"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

		if _, errVerify := m.VerifyInference(ctx, authID, "gpt-5.6-sol"); errVerify != nil {
			t.Fatalf("VerifyInference error = %v", errVerify)
		}
		if got := ojson.GetBytes(exec.lastPayload, "max_output_tokens").Int(); got != 16 {
			t.Fatalf("codex verify max_output_tokens = %d, want 16; payload=%s", got, string(exec.lastPayload))
		}
	})
}

func TestVerifyInference_CallerCancellationHonoredWithoutFailurePenalty_Issue7737(t *testing.T) {
	t.Run("canceling while draining stream chunks returns context.Canceled and discards chunks", func(t *testing.T) {
		hook := &issue7737ResultHook{}
		chunks := make(chan cliproxyexecutor.StreamChunk)
		streamStarted := make(chan struct{})
		producerUnblocked := make(chan struct{})

		exec := &issue7737VerifyExecutor{
			id: "antigravity",
			onStream: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				close(streamStarted)
				return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
			},
		}

		m := NewManager(nil, nil, hook)
		m.RegisterExecutor(exec)

		authID := "ag-verify-cancel-stream-" + uuid.NewString()
		auth := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"type": "antigravity", "project_id": "ag-proj-1", "access_token": "at"},
		}
		if _, errReg := m.Register(WithSkipPersist(context.Background()), auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		verifyCtx, cancel := context.WithCancel(WithSkipPersist(context.Background()))
		errCh := make(chan error, 1)
		go func() {
			_, errV := m.VerifyInference(verifyCtx, authID, "gemini-3.8-flash-high")
			errCh <- errV
		}()

		<-streamStarted
		cancel()

		select {
		case errVerify := <-errCh:
			if !errors.Is(errVerify, context.Canceled) {
				t.Fatalf("VerifyInference error = %v, want context.Canceled", errVerify)
			}
		case <-time.After(2 * time.Second):
			close(chunks)
			t.Fatal("VerifyInference did not return promptly after ctx cancellation while reading streamRes.Chunks")
		}

		// Verify discardStreamChunks is draining the channel so the producer cannot deadlock.
		go func() {
			chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
			close(chunks)
			close(producerUnblocked)
		}()
		select {
		case <-producerUnblocked:
		case <-time.After(2 * time.Second):
			t.Fatal("streamRes.Chunks was not drained by discardStreamChunks after cancellation")
		}

		if results := hook.snapshot(); len(results) != 0 {
			t.Fatalf("expected no MarkResult recorded on caller cancellation, got %+v", results)
		}
	})

	t.Run("canceling during ExecuteStream does not record failure", func(t *testing.T) {
		hook := &issue7737ResultHook{}
		verifyCtx, cancel := context.WithCancel(WithSkipPersist(context.Background()))
		exec := &issue7737VerifyExecutor{
			id: "antigravity",
			onStream: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				cancel()
				return nil, ctx.Err()
			},
		}

		m := NewManager(nil, nil, hook)
		m.RegisterExecutor(exec)

		authID := "ag-verify-cancel-start-" + uuid.NewString()
		auth := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"type": "antigravity", "project_id": "ag-proj-1", "access_token": "at"},
		}
		if _, errReg := m.Register(WithSkipPersist(context.Background()), auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		_, errVerify := m.VerifyInference(verifyCtx, authID, "gemini-3.8-flash-high")
		if !errors.Is(errVerify, context.Canceled) {
			t.Fatalf("VerifyInference error = %v, want context.Canceled", errVerify)
		}
		if results := hook.snapshot(); len(results) != 0 {
			t.Fatalf("expected no MarkResult recorded on ExecuteStream cancellation, got %+v", results)
		}
	})

	t.Run("canceling during 501 fallback Execute does not record failure", func(t *testing.T) {
		hook := &issue7737ResultHook{}
		verifyCtx, cancel := context.WithCancel(WithSkipPersist(context.Background()))
		exec := &issue7737VerifyExecutor{
			id: "antigravity",
			onStream: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				return nil, &Error{Code: "not_implemented", Message: "stream unsupported", HTTPStatus: http.StatusNotImplemented}
			},
			onExecute: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				cancel()
				return cliproxyexecutor.Response{}, ctx.Err()
			},
		}

		m := NewManager(nil, nil, hook)
		m.RegisterExecutor(exec)

		authID := "ag-verify-cancel-exec-" + uuid.NewString()
		auth := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"type": "antigravity", "project_id": "ag-proj-1", "access_token": "at"},
		}
		if _, errReg := m.Register(WithSkipPersist(context.Background()), auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		_, errVerify := m.VerifyInference(verifyCtx, authID, "gemini-3.8-flash-high")
		if !errors.Is(errVerify, context.Canceled) {
			t.Fatalf("VerifyInference error = %v, want context.Canceled", errVerify)
		}
		if results := hook.snapshot(); len(results) != 0 {
			t.Fatalf("expected no MarkResult recorded on fallback Execute cancellation, got %+v", results)
		}
	})

	t.Run("canceling with chunk error does not record failure", func(t *testing.T) {
		hook := &issue7737ResultHook{}
		verifyCtx, cancel := context.WithCancel(WithSkipPersist(context.Background()))
		exec := &issue7737VerifyExecutor{
			id: "antigravity",
			onStream: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				cancel()
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Err: ctx.Err()}
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			},
		}

		m := NewManager(nil, nil, hook)
		m.RegisterExecutor(exec)

		authID := "ag-verify-cancel-chunk-" + uuid.NewString()
		auth := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"type": "antigravity", "project_id": "ag-proj-1", "access_token": "at"},
		}
		if _, errReg := m.Register(WithSkipPersist(context.Background()), auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		_, errVerify := m.VerifyInference(verifyCtx, authID, "gemini-3.8-flash-high")
		if !errors.Is(errVerify, context.Canceled) {
			t.Fatalf("VerifyInference error = %v, want context.Canceled", errVerify)
		}
		if results := hook.snapshot(); len(results) != 0 {
			t.Fatalf("expected no MarkResult recorded on chunk cancellation, got %+v", results)
		}
	})

	t.Run("canceling during PrepareRequestAuth does not call ExecuteStream or record failure", func(t *testing.T) {
		hook := &issue7737ResultHook{}
		verifyCtx, cancel := context.WithCancel(WithSkipPersist(context.Background()))
		exec := &issue7737VerifyExecutor{
			id:            "antigravity",
			shouldPrepare: func(*Auth) bool { return true },
			onPrepare: func(ctx context.Context, auth *Auth) (*Auth, error) {
				cancel()
				return auth, ctx.Err()
			},
		}

		m := NewManager(nil, nil, hook)
		m.RegisterExecutor(exec)

		authID := "ag-verify-cancel-prepare-" + uuid.NewString()
		auth := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"type": "antigravity", "access_token": "at"},
		}
		if _, errReg := m.Register(WithSkipPersist(context.Background()), auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		_, errVerify := m.VerifyInference(verifyCtx, authID, "gemini-3.8-flash-high")
		if !errors.Is(errVerify, context.Canceled) {
			t.Fatalf("VerifyInference error = %v, want context.Canceled", errVerify)
		}
		if exec.streamCalls.Load() != 0 {
			t.Fatalf("ExecuteStream calls = %d, want 0 after PrepareRequestAuth cancellation", exec.streamCalls.Load())
		}
		if results := hook.snapshot(); len(results) != 0 {
			t.Fatalf("expected no MarkResult recorded on PrepareRequestAuth cancellation, got %+v", results)
		}
	})

	t.Run("canceling with RetryAfter 429 error does not record credential failure or cooldown", func(t *testing.T) {
		hook := &issue7737ResultHook{}
		verifyCtx, cancel := context.WithCancel(WithSkipPersist(context.Background()))
		retryAfter := 45 * time.Second
		exec := &issue7737VerifyExecutor{
			id: "antigravity",
			onStream: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				cancel()
				return nil, issue7737RetryAfterError{
					status:     http.StatusTooManyRequests,
					msg:        "upstream 429 during cancellation",
					retryAfter: &retryAfter,
				}
			},
		}

		m := NewManager(nil, nil, hook)
		m.RegisterExecutor(exec)

		authID := "ag-verify-cancel-429-" + uuid.NewString()
		auth := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"type": "antigravity", "project_id": "ag-proj-1", "access_token": "at"},
		}
		if _, errReg := m.Register(WithSkipPersist(context.Background()), auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		_, errVerify := m.VerifyInference(verifyCtx, authID, "gemini-3.8-flash-high")
		if !errors.Is(errVerify, context.Canceled) {
			t.Fatalf("VerifyInference error = %v, want context.Canceled", errVerify)
		}
		if results := hook.snapshot(); len(results) != 0 {
			t.Fatalf("expected no MarkResult recorded on canceled 429 with RetryAfter, got %+v", results)
		}
		stored, ok := m.GetByID(authID)
		if !ok || stored == nil {
			t.Fatal("stored auth missing")
		}
		if stored.Failed != 0 || stored.Unavailable {
			t.Fatalf("expected Failed=0 Unavailable=false after cancellation, got Failed=%d Unavailable=%v", stored.Failed, stored.Unavailable)
		}
	})

	t.Run("canceling during 401 Refresh returning 429 does not mark credential unavailable or record error", func(t *testing.T) {
		hook := &issue7737ResultHook{}
		verifyCtx, cancel := context.WithCancel(WithSkipPersist(context.Background()))
		retryAfter := 45 * time.Second
		exec := &issue7737VerifyExecutor{
			id: "antigravity",
			onStream: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				return nil, &Error{Code: "unauthorized", Message: "token revoked", HTTPStatus: http.StatusUnauthorized}
			},
			onRefresh: func(context.Context, *Auth) (*Auth, error) {
				cancel()
				return nil, issue7737RetryAfterError{
					status:     http.StatusTooManyRequests,
					msg:        "oauth refresh 429 during cancellation",
					retryAfter: &retryAfter,
				}
			},
		}

		m := NewManager(nil, nil, hook)
		m.RegisterExecutor(exec)

		authID := "ag-verify-cancel-refresh-429-" + uuid.NewString()
		auth := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"type":          "antigravity",
				"project_id":    "ag-proj-1",
				"access_token":  "revoked-at",
				"refresh_token": "valid-rt",
			},
		}
		if _, errReg := m.Register(WithSkipPersist(context.Background()), auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		_, errVerify := m.VerifyInference(verifyCtx, authID, "gemini-3.8-flash-high")
		if !errors.Is(errVerify, context.Canceled) {
			t.Fatalf("VerifyInference error = %v, want context.Canceled", errVerify)
		}
		if results := hook.snapshot(); len(results) != 0 {
			t.Fatalf("expected no MarkResult recorded on canceled refresh 429, got %+v", results)
		}
		stored, ok := m.GetByID(authID)
		if !ok || stored == nil {
			t.Fatal("stored auth missing")
		}
		if stored.Unavailable || stored.LastError != nil || stored.Failed != 0 || !stored.NextRefreshAfter.IsZero() {
			t.Fatalf("expected credential untouched after canceled refresh 429, got Unavailable=%v LastError=%v Failed=%d NextRefreshAfter=%v",
				stored.Unavailable, stored.LastError, stored.Failed, stored.NextRefreshAfter)
		}
	})
}

type issue7737RetryAfterError struct {
	status     int
	msg        string
	retryAfter *time.Duration
}

func (e issue7737RetryAfterError) Error() string              { return e.msg }
func (e issue7737RetryAfterError) StatusCode() int            { return e.status }
func (e issue7737RetryAfterError) RetryAfter() *time.Duration { return e.retryAfter }

type issue7737CredentialScopedError struct {
	status int
	msg    string
}

func (e issue7737CredentialScopedError) Error() string            { return e.msg }
func (e issue7737CredentialScopedError) StatusCode() int          { return e.status }
func (e issue7737CredentialScopedError) IsCredentialScoped() bool { return true }

func TestVerifyInference_UnauthorizedAfterReplacementReturnsSupersededWithoutRetry_Issue7737(t *testing.T) {
	ctx := WithSkipPersist(context.Background())

	t.Run("in-place Update replacement while first probe is in flight returns 409 credential_superseded without retry", func(t *testing.T) {
		firstStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		exec := &issue7737VerifyExecutor{
			id: "antigravity",
			onStream: func(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if tok, _ := a.Metadata["access_token"].(string); tok == "old-at" {
					close(firstStarted)
					<-releaseFirst
					return nil, &Error{Code: "unauthorized", Message: "token revoked", HTTPStatus: http.StatusUnauthorized}
				}
				return &cliproxyexecutor.StreamResult{Chunks: issue7737CompletedStream()}, nil
			},
			onRefresh: func(_ context.Context, a *Auth) (*Auth, error) {
				cloned := a.Clone()
				cloned.Metadata["access_token"] = "unexpected-refresh-at"
				return cloned, nil
			},
		}

		m := NewManager(nil, nil, nil)
		m.RegisterExecutor(exec)

		authID := "ag-verify-superseded-update-" + uuid.NewString()
		initial := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"type":          "antigravity",
				"project_id":    "ag-proj-1",
				"access_token":  "old-at",
				"refresh_token": "old-rt",
			},
		}
		if _, errReg := m.Register(ctx, initial); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		type verifyRes struct {
			auth *Auth
			err  error
		}
		resCh := make(chan verifyRes, 1)
		go func() {
			got, errV := m.VerifyInference(ctx, authID, "gemini-3.8-flash-high")
			resCh <- verifyRes{auth: got, err: errV}
		}()

		<-firstStarted
		replaced := initial.Clone()
		replaced.Metadata["access_token"] = "admin-new-at"
		replaced.Metadata["refresh_token"] = "admin-new-rt"
		if _, errUpd := m.Update(ctx, replaced); errUpd != nil {
			t.Fatalf("Update replacement error: %v", errUpd)
		}
		close(releaseFirst)

		res := <-resCh
		var authErr *Error
		if !errors.As(res.err, &authErr) || authErr.HTTPStatus != http.StatusConflict || authErr.Code != "credential_superseded" {
			t.Fatalf("VerifyInference error = %v, want 409 credential_superseded", res.err)
		}
		if exec.streamCalls.Load() != 1 {
			t.Fatalf("ExecuteStream calls = %d, want 1 (must not retry on replaced credential)", exec.streamCalls.Load())
		}
		if exec.refreshCalls.Load() != 0 {
			t.Fatalf("Refresh calls = %d, want 0 (must not refresh replaced credential)", exec.refreshCalls.Load())
		}
		current, ok := m.GetByID(authID)
		if !ok || current == nil {
			t.Fatal("replacement auth missing")
		}
		if current.InferenceVerified {
			t.Fatal("replacement credential must not be marked InferenceVerified by stale verification attempt")
		}
		if current.Failed != 0 || current.Unavailable {
			t.Fatalf("replacement credential must stay untouched, got Failed=%d Unavailable=%v", current.Failed, current.Unavailable)
		}
	})

	t.Run("remove and re-register replacement while first probe is in flight returns 409 credential_superseded without retry", func(t *testing.T) {
		firstStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		exec := &issue7737VerifyExecutor{
			id: "antigravity",
			onStream: func(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if tok, _ := a.Metadata["access_token"].(string); tok == "epoch1-at" {
					close(firstStarted)
					<-releaseFirst
					return nil, &Error{Code: "unauthorized", Message: "token revoked", HTTPStatus: http.StatusUnauthorized}
				}
				return &cliproxyexecutor.StreamResult{Chunks: issue7737CompletedStream()}, nil
			},
			onRefresh: func(_ context.Context, a *Auth) (*Auth, error) {
				cloned := a.Clone()
				cloned.Metadata["access_token"] = "unexpected-epoch-refresh-at"
				return cloned, nil
			},
		}

		m := NewManager(nil, nil, nil)
		m.RegisterExecutor(exec)

		authID := "ag-verify-superseded-reregister-" + uuid.NewString()
		initial := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"type":          "antigravity",
				"project_id":    "ag-proj-1",
				"access_token":  "epoch1-at",
				"refresh_token": "epoch1-rt",
			},
		}
		if _, errReg := m.Register(ctx, initial); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		type verifyRes struct {
			auth *Auth
			err  error
		}
		resCh := make(chan verifyRes, 1)
		go func() {
			got, errV := m.VerifyInference(ctx, authID, "gemini-3.8-flash-high")
			resCh <- verifyRes{auth: got, err: errV}
		}()

		<-firstStarted
		m.Remove(ctx, authID)
		recreated := initial.Clone()
		recreated.Metadata["access_token"] = "epoch2-at"
		recreated.Metadata["refresh_token"] = "epoch2-rt"
		if _, errReg := m.Register(ctx, recreated); errReg != nil {
			t.Fatalf("re-Register error: %v", errReg)
		}
		close(releaseFirst)

		res := <-resCh
		var authErr *Error
		if !errors.As(res.err, &authErr) || authErr.HTTPStatus != http.StatusConflict || authErr.Code != "credential_superseded" {
			t.Fatalf("VerifyInference error = %v, want 409 credential_superseded", res.err)
		}
		if exec.streamCalls.Load() != 1 {
			t.Fatalf("ExecuteStream calls = %d, want 1", exec.streamCalls.Load())
		}
		current, ok := m.GetByID(authID)
		if !ok || current == nil {
			t.Fatal("replacement auth missing")
		}
		if current.InferenceVerified {
			t.Fatal("re-registered credential must not be marked InferenceVerified")
		}
	})

	t.Run("in-place Update replacement while Refresh is in flight returns 409 credential_superseded without retry", func(t *testing.T) {
		refreshStarted := make(chan struct{})
		releaseRefresh := make(chan struct{})
		exec := &issue7737VerifyExecutor{
			id: "antigravity",
			onStream: func(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if tok, _ := a.Metadata["access_token"].(string); tok == "old-at-2" {
					return nil, &Error{Code: "unauthorized", Message: "token revoked", HTTPStatus: http.StatusUnauthorized}
				}
				return &cliproxyexecutor.StreamResult{Chunks: issue7737CompletedStream()}, nil
			},
			onRefresh: func(_ context.Context, a *Auth) (*Auth, error) {
				close(refreshStarted)
				<-releaseRefresh
				cloned := a.Clone()
				cloned.Metadata["access_token"] = "stale-refresh-at"
				return cloned, nil
			},
		}

		m := NewManager(nil, nil, nil)
		m.RegisterExecutor(exec)

		authID := "ag-verify-superseded-during-refresh-" + uuid.NewString()
		initial := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"type":          "antigravity",
				"project_id":    "ag-proj-1",
				"access_token":  "old-at-2",
				"refresh_token": "old-rt-2",
			},
		}
		if _, errReg := m.Register(ctx, initial); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		type verifyRes struct {
			auth *Auth
			err  error
		}
		resCh := make(chan verifyRes, 1)
		go func() {
			got, errV := m.VerifyInference(ctx, authID, "gemini-3.8-flash-high")
			resCh <- verifyRes{auth: got, err: errV}
		}()

		<-refreshStarted
		replaced := initial.Clone()
		replaced.Metadata["access_token"] = "admin-replaced-during-refresh-at"
		replaced.Metadata["refresh_token"] = "admin-replaced-during-refresh-rt"
		if _, errUpd := m.Update(ctx, replaced); errUpd != nil {
			t.Fatalf("Update replacement error: %v", errUpd)
		}
		close(releaseRefresh)

		res := <-resCh
		var authErr *Error
		if !errors.As(res.err, &authErr) || authErr.HTTPStatus != http.StatusConflict || authErr.Code != "credential_superseded" {
			t.Fatalf("VerifyInference error = %v, want 409 credential_superseded", res.err)
		}
		if exec.streamCalls.Load() != 1 {
			t.Fatalf("ExecuteStream calls = %d, want 1 (must not send second probe on replaced credential)", exec.streamCalls.Load())
		}
		current, ok := m.GetByID(authID)
		if !ok || current == nil {
			t.Fatal("replacement auth missing")
		}
		if current.InferenceVerified {
			t.Fatal("replaced credential must not be marked InferenceVerified")
		}
	})

	t.Run("Codex partial CodexRefreshPatch merge after in-place SIWC Update returns 409 credential_superseded without retry", func(t *testing.T) {
		refreshStarted := make(chan struct{})
		releaseRefresh := make(chan struct{})
		exec := &issue7737VerifyExecutor{
			id: "codex",
			onStream: func(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if tok, _ := a.Metadata["access_token"].(string); tok == "outer-old-at" {
					return nil, &Error{Code: "unauthorized", Message: "outer token revoked", HTTPStatus: http.StatusUnauthorized}
				}
				return &cliproxyexecutor.StreamResult{Chunks: issue7737CompletedStream()}, nil
			},
			onRefresh: func(_ context.Context, a *Auth) (*Auth, error) {
				close(refreshStarted)
				<-releaseRefresh
				cloned := a.Clone()
				cloned.Metadata["access_token"] = "outer-rotated-at"
				cloned.Metadata["refresh_token"] = "outer-rotated-rt"
				cloned.CodexRefreshPatch = codex.NewCredentialRefreshPatch(a.Metadata, cloned.Metadata, true, false)
				return cloned, nil
			},
		}

		m := NewManager(nil, nil, nil)
		m.RegisterExecutor(exec)

		authID := "codex-verify-partial-patch-superseded-" + uuid.NewString()
		initial := &Auth{
			ID:       authID,
			Provider: "codex",
			Status:   StatusActive,
			Metadata: map[string]any{
				"type":          "codex",
				"access_token":  "outer-old-at",
				"refresh_token": "outer-old-rt",
				"siwc": map[string]any{
					"access_token":  "siwc-old-at",
					"refresh_token": "siwc-old-rt",
				},
			},
		}
		if _, errReg := m.Register(ctx, initial); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		type verifyRes struct {
			auth *Auth
			err  error
		}
		resCh := make(chan verifyRes, 1)
		go func() {
			got, errV := m.VerifyInference(ctx, authID, "gpt-5.6-sol")
			resCh <- verifyRes{auth: got, err: errV}
		}()

		<-refreshStarted
		replacedSIWC := initial.Clone()
		replacedSIWC.Metadata["siwc"] = map[string]any{
			"access_token":  "siwc-admin-new-at",
			"refresh_token": "siwc-admin-new-rt",
		}
		if _, errUpd := m.Update(ctx, replacedSIWC); errUpd != nil {
			t.Fatalf("Update SIWC replacement error: %v", errUpd)
		}
		close(releaseRefresh)

		res := <-resCh
		var authErr *Error
		if !errors.As(res.err, &authErr) || authErr.HTTPStatus != http.StatusConflict || authErr.Code != "credential_superseded" {
			t.Fatalf("VerifyInference error = %v, want 409 credential_superseded", res.err)
		}
		if exec.streamCalls.Load() != 1 {
			t.Fatalf("ExecuteStream calls = %d, want 1 (must not retry after partial patch on superseded credential)", exec.streamCalls.Load())
		}
		current, ok := m.GetByID(authID)
		if !ok || current == nil {
			t.Fatal("replacement auth missing")
		}
		if current.InferenceVerified {
			t.Fatal("externally updated Codex credential must not be marked InferenceVerified")
		}
		// Outer rotated token should still be safely preserved by CodexRefreshPatch merge.
		if gotOuter, _ := current.Metadata["refresh_token"].(string); gotOuter != "outer-rotated-rt" {
			t.Fatalf("outer rotated refresh_token = %q, want outer-rotated-rt preserved", gotOuter)
		}
	})

	t.Run("in-place Update with project_id between prepare 401 refresh and retry PrepareRequestAuth returns 409 credential_superseded without retry", func(t *testing.T) {
		var prepareCalls atomic.Int32
		var m *Manager
		authID := "ag-verify-prepare-retry-superseded-" + uuid.NewString()
		initial := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"type":          "antigravity",
				"access_token":  "revoked-unexpired-at",
				"refresh_token": "valid-rt",
				"expired":       time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			},
		}

		exec := &issue7737VerifyExecutor{
			id: "antigravity",
			shouldPrepare: func(a *Auth) bool {
				proj, _ := a.Metadata["project_id"].(string)
				return proj == ""
			},
			onPrepare: func(_ context.Context, a *Auth) (*Auth, error) {
				prepareCalls.Add(1)
				if tok, _ := a.Metadata["access_token"].(string); tok == "revoked-unexpired-at" {
					return nil, issue7737CredentialScopedError{status: http.StatusUnauthorized, msg: "loadCodeAssist 401"}
				}
				cloned := a.Clone()
				cloned.Metadata["project_id"] = "ag-proj-prepared"
				return cloned, nil
			},
			onRefresh: func(_ context.Context, a *Auth) (*Auth, error) {
				cloned := a.Clone()
				cloned.Metadata["access_token"] = "refreshed-at"
				return cloned, nil
			},
		}

		// Replace the credential right after Refresh commits via hook OnAuthUpdated,
		// before the prepare retry acquires PrepareRequestAuth.
		var replacedOnce atomic.Bool
		hook := &issue7737UpdateHook{
			onUpdated: func(updated *Auth) {
				if updated == nil || updated.ID != authID {
					return
				}
				if tok, _ := updated.Metadata["access_token"].(string); tok == "refreshed-at" && replacedOnce.CompareAndSwap(false, true) {
					replacement := updated.Clone()
					replacement.Metadata["access_token"] = "admin-replaced-at"
					replacement.Metadata["refresh_token"] = "admin-replaced-rt"
					replacement.Metadata["project_id"] = "admin-replaced-proj"
					if _, errUpd := m.Update(ctx, replacement); errUpd != nil {
						t.Errorf("in-place replacement Update error: %v", errUpd)
					}
				}
			},
		}

		m = NewManager(nil, nil, hook)
		m.RegisterExecutor(exec)
		if _, errReg := m.Register(ctx, initial); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		_, errVerify := m.VerifyInference(ctx, authID, "gemini-3.8-flash-high")
		var authErr *Error
		if !errors.As(errVerify, &authErr) || authErr.HTTPStatus != http.StatusConflict || authErr.Code != "credential_superseded" {
			t.Fatalf("VerifyInference error = %v, want 409 credential_superseded", errVerify)
		}
		if prepareCalls.Load() != 1 {
			t.Fatalf("PrepareRequestAuth calls = %d, want 1 (must not prepare replacement credential)", prepareCalls.Load())
		}
		if exec.streamCalls.Load() != 0 {
			t.Fatalf("ExecuteStream calls = %d, want 0 (must not probe replacement credential)", exec.streamCalls.Load())
		}
		current, ok := m.GetByID(authID)
		if !ok || current == nil {
			t.Fatal("replacement auth missing")
		}
		if current.InferenceVerified {
			t.Fatal("replacement credential must not be marked InferenceVerified")
		}
	})

	t.Run("in-place Update during PrepareRequestAuth followed by 503 error returns 409 credential_superseded", func(t *testing.T) {
		prepareStarted := make(chan struct{})
		releasePrepare := make(chan struct{})
		exec := &issue7737VerifyExecutor{
			id:            "antigravity",
			shouldPrepare: func(*Auth) bool { return true },
			onPrepare: func(_ context.Context, _ *Auth) (*Auth, error) {
				close(prepareStarted)
				<-releasePrepare
				return nil, issue7737CredentialScopedError{status: http.StatusServiceUnavailable, msg: "loadCodeAssist 503"}
			},
		}

		m := NewManager(nil, nil, nil)
		m.RegisterExecutor(exec)

		authID := "ag-verify-prepare-503-superseded-" + uuid.NewString()
		initial := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"type": "antigravity", "access_token": "old-at", "refresh_token": "old-rt"},
		}
		if _, errReg := m.Register(ctx, initial); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		errCh := make(chan error, 1)
		go func() {
			_, errV := m.VerifyInference(ctx, authID, "gemini-3.8-flash-high")
			errCh <- errV
		}()

		<-prepareStarted
		replaced := initial.Clone()
		replaced.Metadata["access_token"] = "admin-new-at"
		replaced.Metadata["refresh_token"] = "admin-new-rt"
		replaced.Metadata["project_id"] = "admin-proj"
		if _, errUpd := m.Update(ctx, replaced); errUpd != nil {
			t.Fatalf("Update replacement error: %v", errUpd)
		}
		close(releasePrepare)

		errVerify := <-errCh
		var authErr *Error
		if !errors.As(errVerify, &authErr) || authErr.HTTPStatus != http.StatusConflict || authErr.Code != "credential_superseded" {
			t.Fatalf("VerifyInference error = %v, want 409 credential_superseded", errVerify)
		}
		if exec.streamCalls.Load() != 0 {
			t.Fatalf("ExecuteStream calls = %d, want 0", exec.streamCalls.Load())
		}
		current, ok := m.GetByID(authID)
		if !ok || current == nil || current.Failed != 0 || current.Unavailable {
			t.Fatalf("replacement credential must stay untouched, got %+v", current)
		}
	})

	t.Run("in-place Update during PrepareRequestAuth returning (nil, nil) returns 409 credential_superseded without probing", func(t *testing.T) {
		prepareStarted := make(chan struct{})
		releasePrepare := make(chan struct{})
		exec := &issue7737VerifyExecutor{
			id:            "antigravity",
			shouldPrepare: func(*Auth) bool { return true },
			onPrepare: func(_ context.Context, _ *Auth) (*Auth, error) {
				close(prepareStarted)
				<-releasePrepare
				return nil, nil
			},
		}

		m := NewManager(nil, nil, nil)
		m.RegisterExecutor(exec)

		authID := "ag-verify-prepare-nil-superseded-" + uuid.NewString()
		initial := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"type": "antigravity", "access_token": "old-at", "refresh_token": "old-rt"},
		}
		if _, errReg := m.Register(ctx, initial); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		errCh := make(chan error, 1)
		go func() {
			_, errV := m.VerifyInference(ctx, authID, "gemini-3.8-flash-high")
			errCh <- errV
		}()

		<-prepareStarted
		replaced := initial.Clone()
		replaced.Metadata["access_token"] = "admin-new-at"
		replaced.Metadata["refresh_token"] = "admin-new-rt"
		replaced.Metadata["project_id"] = "admin-proj"
		if _, errUpd := m.Update(ctx, replaced); errUpd != nil {
			t.Fatalf("Update replacement error: %v", errUpd)
		}
		close(releasePrepare)

		errVerify := <-errCh
		var authErr *Error
		if !errors.As(errVerify, &authErr) || authErr.HTTPStatus != http.StatusConflict || authErr.Code != "credential_superseded" {
			t.Fatalf("VerifyInference error = %v, want 409 credential_superseded", errVerify)
		}
		if exec.streamCalls.Load() != 0 {
			t.Fatalf("ExecuteStream calls = %d, want 0 (must not probe stale snapshot)", exec.streamCalls.Load())
		}
		current, ok := m.GetByID(authID)
		if !ok || current == nil || current.InferenceVerified {
			t.Fatalf("replacement credential must not be verified, got %+v", current)
		}
	})
}

type issue7737UpdateHook struct {
	onUpdated func(*Auth)
}

func (*issue7737UpdateHook) OnAuthRegistered(context.Context, *Auth) {}
func (h *issue7737UpdateHook) OnAuthUpdated(_ context.Context, auth *Auth) {
	if h.onUpdated != nil {
		h.onUpdated(auth)
	}
}
func (*issue7737UpdateHook) OnResult(context.Context, Result) {}

func TestVerifyInference_PrepareUnauthorizedRefreshesOnceAndSucceeds_Issue7737(t *testing.T) {
	ctx := WithSkipPersist(context.Background())

	var prepareAttempts atomic.Int32
	exec := &issue7737VerifyExecutor{
		id: "antigravity",
		shouldPrepare: func(a *Auth) bool {
			proj, _ := a.Metadata["project_id"].(string)
			return proj == ""
		},
		onPrepare: func(_ context.Context, a *Auth) (*Auth, error) {
			prepareAttempts.Add(1)
			if tok, _ := a.Metadata["access_token"].(string); tok == "revoked-unexpired-at" {
				return nil, issue7737CredentialScopedError{status: http.StatusUnauthorized, msg: "loadCodeAssist 401 unauthorized"}
			}
			cloned := a.Clone()
			cloned.Metadata["project_id"] = "ag-proj-after-refresh"
			return cloned, nil
		},
		onRefresh: func(_ context.Context, a *Auth) (*Auth, error) {
			cloned := a.Clone()
			cloned.Metadata["access_token"] = "refreshed-for-prepare-at"
			return cloned, nil
		},
	}

	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(exec)

	authID := "ag-verify-prepare-401-" + uuid.NewString()
	auth := &Auth{
		ID:       authID,
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "antigravity",
			"access_token":  "revoked-unexpired-at",
			"refresh_token": "valid-rt",
			"expired":       time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		},
	}
	if _, errReg := m.Register(ctx, auth); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}

	verified, errVerify := m.VerifyInference(ctx, authID, "gemini-3.8-flash-high")
	if errVerify != nil {
		t.Fatalf("VerifyInference error = %v, want nil after prepare 401 refresh", errVerify)
	}
	if verified == nil || !verified.InferenceVerified {
		t.Fatalf("expected InferenceVerified=true, got %+v", verified)
	}
	if prepareAttempts.Load() != 2 {
		t.Fatalf("PrepareRequestAuth attempts = %d, want 2", prepareAttempts.Load())
	}
	if exec.refreshCalls.Load() != 1 {
		t.Fatalf("Refresh calls = %d, want 1", exec.refreshCalls.Load())
	}
	if exec.streamCalls.Load() != 1 {
		t.Fatalf("ExecuteStream calls = %d, want 1", exec.streamCalls.Load())
	}
	if exec.lastStreamAuth == nil || exec.lastStreamAuth.Metadata["project_id"] != "ag-proj-after-refresh" {
		t.Fatalf("expected prepared project_id=ag-proj-after-refresh on ExecuteStream, got %+v", exec.lastStreamAuth)
	}
	if exec.lastStreamAuth.Metadata["access_token"] != "refreshed-for-prepare-at" {
		t.Fatalf("expected refreshed access_token on ExecuteStream, got %+v", exec.lastStreamAuth.Metadata)
	}
}

func TestVerifyInference_PrepareFailureAppliesRequestScopedErrorRule_Issue7737(t *testing.T) {
	hook := &issue7737ResultHook{}
	exec := &issue7737VerifyExecutor{
		id:            "antigravity",
		shouldPrepare: func(*Auth) bool { return true },
		onPrepare: func(_ context.Context, _ *Auth) (*Auth, error) {
			return nil, issue7737CredentialScopedError{status: http.StatusForbidden, msg: "custom_prepare_stop_rule_match"}
		},
	}

	m := NewManager(nil, nil, hook)
	m.RegisterExecutor(exec)

	cfg := &internalconfig.Config{
		OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
			"antigravity": {
				{
					Action: RequestScopedActionStop,
					Status: http.StatusForbidden,
					Match:  []string{"custom_prepare_stop_rule_match"},
				},
			},
		},
	}
	ctx := WithRequestScopedErrorConfig(WithSkipPersist(context.Background()), cfg)

	authID := "ag-verify-prepare-req-scoped-" + uuid.NewString()
	auth := &Auth{
		ID:       authID,
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{"type": "antigravity", "access_token": "at"},
	}
	if _, errReg := m.Register(ctx, auth); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}

	_, errVerify := m.VerifyInference(ctx, authID, "gemini-3.8-flash-high")
	if errVerify == nil {
		t.Fatal("expected VerifyInference error on prepare failure")
	}

	results := hook.snapshot()
	if len(results) != 1 || results[0].Error == nil || results[0].Error.Code != ErrorCodeRequestScoped {
		t.Fatalf("expected recorded Result error code %q, got %+v", ErrorCodeRequestScoped, results)
	}
	stored, ok := m.GetByID(authID)
	if !ok || stored == nil {
		t.Fatal("stored auth missing")
	}
	if stored.Unavailable || !stored.NextRetryAfter.IsZero() {
		t.Fatalf("expected request-scoped stop rule to avoid credential cooldown, got Unavailable=%v NextRetryAfter=%v", stored.Unavailable, stored.NextRetryAfter)
	}
}

func TestVerifyInference_ModelSelectionSkipsNonTextOutputModels_Issue7737(t *testing.T) {
	ctx := WithSkipPersist(context.Background())

	exec := &issue7737VerifyExecutor{id: "xai"}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(exec)

	authID := "xai-verify-video-skip-" + uuid.NewString()
	auth := &Auth{
		ID:       authID,
		Provider: "xai",
		Status:   StatusActive,
		Metadata: map[string]any{"type": "xai", "access_token": "at"},
	}
	if _, errReg := m.Register(ctx, auth); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "xai", []*registry.ModelInfo{
		{
			ID:                        "grok-4.7",
			SupportedInputModalities:  []string{"text"},
			SupportedOutputModalities: []string{"text"},
			Thinking:                  &registry.ThinkingSupport{Min: 1, Max: 32768},
		},
		{
			ID:                        "grok-imagine-video",
			SupportedInputModalities:  []string{"text"},
			SupportedOutputModalities: []string{"video"},
		},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	if _, errVerify := m.VerifyInference(ctx, authID, ""); errVerify != nil {
		t.Fatalf("VerifyInference error = %v", errVerify)
	}
	if exec.lastModel != "grok-4.7" {
		t.Fatalf("VerifyInference selected model %q, want text-capable grok-4.7 instead of video-only grok-imagine-video", exec.lastModel)
	}
}
