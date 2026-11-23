package auth

import (
	"context"
	"maps"
	"net/http"
	"strings"
	"sync"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	internalutil "github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7852ExecutionObservation struct {
	creditsRequested bool
	metadata         map[string]any
	ctxSessionID     string
}

type issue7852CreditsExecutor struct {
	mu           sync.Mutex
	executeCalls []issue7852ExecutionObservation
	streamCalls  []issue7852ExecutionObservation
}

func (e *issue7852CreditsExecutor) Identifier() string { return "antigravity" }

func (e *issue7852CreditsExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	obs := issue7852ExecutionObservation{
		creditsRequested: AntigravityCreditsRequested(ctx),
		metadata:         maps.Clone(opts.Metadata),
		ctxSessionID:     internalutil.SessionIDFromContext(ctx),
	}
	e.mu.Lock()
	e.executeCalls = append(e.executeCalls, obs)
	e.mu.Unlock()
	if !obs.creditsRequested {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
	}
	return cliproxyexecutor.Response{Payload: []byte("credits-ok:" + auth.ID + ":" + req.Model)}, nil
}

func (e *issue7852CreditsExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	obs := issue7852ExecutionObservation{
		creditsRequested: AntigravityCreditsRequested(ctx),
		metadata:         maps.Clone(opts.Metadata),
		ctxSessionID:     internalutil.SessionIDFromContext(ctx),
	}
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, obs)
	e.mu.Unlock()
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if !obs.creditsRequested {
		ch <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}}
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("credits-stream-ok:" + auth.ID + ":" + req.Model)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *issue7852CreditsExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *issue7852CreditsExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *issue7852CreditsExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func newIssue7852CreditsManager(t *testing.T, authID, model string) (*Manager, *issue7852CreditsExecutor) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
		SetAntigravityCreditsHint(authID, AntigravityCreditsHint{})
	})

	exec := &issue7852CreditsExecutor{}
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	m.RegisterExecutor(exec)
	if _, errReg := m.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "antigravity",
		Status:   StatusActive,
	}); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}
	return m, exec
}

func TestTryAntigravityCreditsExecute_PopulatesCanonicalSessionMetadata_Issue7852(t *testing.T) {
	const model = "claude-opus-5-5-high"

	t.Run("header session matches streaming credits fallback without mutating caller metadata", func(t *testing.T) {
		m, exec := newIssue7852CreditsManager(t, "ag-issue7852-header", model)

		req := cliproxyexecutor.Request{
			Model:   model,
			Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		}
		callerMeta := map[string]any{}
		opts := cliproxyexecutor.Options{
			Headers:  http.Header{"X-Session-Id": []string{"sess-credits-1"}},
			Metadata: callerMeta,
		}

		_, okExec, errExec := m.tryAntigravityCreditsExecute(context.Background(), req, opts)
		if errExec != nil || !okExec {
			t.Fatalf("tryAntigravityCreditsExecute() = (ok=%v, err=%v), want (true, nil)", okExec, errExec)
		}
		if _, leaked := callerMeta[cliproxyexecutor.CanonicalSessionIDMetadataKey]; leaked {
			t.Fatalf("caller metadata mutated with %s", cliproxyexecutor.CanonicalSessionIDMetadataKey)
		}

		streamOpts := opts
		streamOpts.Stream = true
		streamRes, okStream, errStream := m.tryAntigravityCreditsExecuteStream(context.Background(), req, streamOpts)
		if errStream != nil || !okStream || streamRes == nil {
			t.Fatalf("tryAntigravityCreditsExecuteStream() = (res=%v, ok=%v, err=%v), want (non-nil, true, nil)", streamRes != nil, okStream, errStream)
		}
		for chunk := range streamRes.Chunks {
			if chunk.Err != nil {
				t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
			}
		}

		if len(exec.executeCalls) != 1 || len(exec.streamCalls) != 1 {
			t.Fatalf("calls = (exec=%d, stream=%d), want (1, 1)", len(exec.executeCalls), len(exec.streamCalls))
		}
		gotExec, _ := exec.executeCalls[0].metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
		gotStream, _ := exec.streamCalls[0].metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
		if gotExec != "header:sess-credits-1" {
			t.Fatalf("Execute CanonicalSessionIDMetadataKey = %q, want %q", gotExec, "header:sess-credits-1")
		}
		if gotStream != "header:sess-credits-1" {
			t.Fatalf("ExecuteStream CanonicalSessionIDMetadataKey = %q, want %q", gotStream, "header:sess-credits-1")
		}
		if exec.executeCalls[0].ctxSessionID != "header:sess-credits-1" {
			t.Fatalf("Execute context session ID = %q, want %q", exec.executeCalls[0].ctxSessionID, "header:sess-credits-1")
		}
	})

	t.Run("prefers OriginalRequest over Payload and falls back to Payload", func(t *testing.T) {
		m, exec := newIssue7852CreditsManager(t, "ag-issue7852-payload", model)

		reqWithOrig := cliproxyexecutor.Request{
			Model:   model,
			Payload: []byte(`{"sessionId":"payload-session-ignored","messages":[{"role":"user","content":"hi"}]}`),
		}
		optsWithOrig := cliproxyexecutor.Options{
			OriginalRequest: []byte(`{"sessionId":"orig-session-1","messages":[{"role":"user","content":"hi"}]}`),
			Metadata:        map[string]any{},
		}
		if _, okExec, errExec := m.tryAntigravityCreditsExecute(context.Background(), reqWithOrig, optsWithOrig); errExec != nil || !okExec {
			t.Fatalf("tryAntigravityCreditsExecute(OriginalRequest) = (ok=%v, err=%v), want (true, nil)", okExec, errExec)
		}

		reqPayloadOnly := cliproxyexecutor.Request{
			Model:   model,
			Payload: []byte(`{"sessionId":"payload-session-2","messages":[{"role":"user","content":"hi"}]}`),
		}
		if _, okExec, errExec := m.tryAntigravityCreditsExecute(context.Background(), reqPayloadOnly, cliproxyexecutor.Options{Metadata: map[string]any{}}); errExec != nil || !okExec {
			t.Fatalf("tryAntigravityCreditsExecute(Payload fallback) = (ok=%v, err=%v), want (true, nil)", okExec, errExec)
		}

		if len(exec.executeCalls) != 2 {
			t.Fatalf("executeCalls = %d, want 2", len(exec.executeCalls))
		}
		if got, _ := exec.executeCalls[0].metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string); got != "session:orig-session-1" {
			t.Fatalf("OriginalRequest CanonicalSessionIDMetadataKey = %q, want %q", got, "session:orig-session-1")
		}
		if got, _ := exec.executeCalls[1].metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string); got != "session:payload-session-2" {
			t.Fatalf("Payload fallback CanonicalSessionIDMetadataKey = %q, want %q", got, "session:payload-session-2")
		}
	})

	t.Run("runs before RequestAfterAuthInterceptor and respects ClearHeaders", func(t *testing.T) {
		m, exec := newIssue7852CreditsManager(t, "ag-issue7852-interceptor", model)

		var interceptedCanonical any
		opts := cliproxyexecutor.Options{
			Headers:  http.Header{"X-Session-Id": []string{"sess-intercept-1"}},
			Metadata: map[string]any{},
			RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				interceptedCanonical = req.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{
					ClearHeaders: []string{"X-Session-Id"},
				}
			},
		}
		req := cliproxyexecutor.Request{
			Model:   model,
			Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		}
		if _, okExec, errExec := m.tryAntigravityCreditsExecute(context.Background(), req, opts); errExec != nil || !okExec {
			t.Fatalf("tryAntigravityCreditsExecute() = (ok=%v, err=%v), want (true, nil)", okExec, errExec)
		}
		if interceptedCanonical != "header:sess-intercept-1" {
			t.Fatalf("interceptor CanonicalSessionIDMetadataKey = %v, want %q", interceptedCanonical, "header:sess-intercept-1")
		}
		if len(exec.executeCalls) != 1 {
			t.Fatalf("executeCalls = %d, want 1", len(exec.executeCalls))
		}
		if got, exists := exec.executeCalls[0].metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; exists {
			t.Fatalf("CanonicalSessionIDMetadataKey was not cleared after ClearHeaders: %v", got)
		}
	})
}

func TestManager_Execute_AntigravityCreditsFallbackCanonicalSessionParity_Issue7852(t *testing.T) {
	const model = "claude-opus-5-5-high"
	m, exec := newIssue7852CreditsManager(t, "ag-issue7852-e2e", model)

	// Plain payload without explicit session headers/fields: cliproxysession.Enrich sets
	// DerivedSessionIDMetadataKey only, so non-stream credits fallback must promote it into
	// CanonicalSessionIDMetadataKey just like executeMixedOnce.
	req := cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"messages":[{"role":"user","content":"hello issue 7852"}]}`),
	}
	resp, errExec := m.Execute(context.Background(), []string{"antigravity"}, req, cliproxyexecutor.Options{})
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}
	if got := string(resp.Payload); got != "credits-ok:ag-issue7852-e2e:"+model {
		t.Fatalf("response payload = %q, want %q", got, "credits-ok:ag-issue7852-e2e:"+model)
	}

	if len(exec.executeCalls) != 2 {
		t.Fatalf("executeCalls = %d, want 2 (initial quota failure + credits fallback)", len(exec.executeCalls))
	}
	normalCall := exec.executeCalls[0]
	creditsCall := exec.executeCalls[1]
	if normalCall.creditsRequested || !creditsCall.creditsRequested {
		t.Fatalf("creditsRequested = (%v, %v), want (false, true)", normalCall.creditsRequested, creditsCall.creditsRequested)
	}

	normalCanonical, _ := normalCall.metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
	creditsCanonical, _ := creditsCall.metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
	if !strings.HasPrefix(normalCanonical, "derived:") {
		t.Fatalf("normal Execute CanonicalSessionIDMetadataKey = %q, want derived:*", normalCanonical)
	}
	if creditsCanonical != normalCanonical {
		t.Fatalf("credits fallback CanonicalSessionIDMetadataKey = %q, want %q", creditsCanonical, normalCanonical)
	}
	if creditsCall.ctxSessionID != normalCanonical {
		t.Fatalf("credits fallback context session ID = %q, want %q", creditsCall.ctxSessionID, normalCanonical)
	}
}
