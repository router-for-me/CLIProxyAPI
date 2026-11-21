package auth

import (
	"context"
	"maps"
	"net/http"
	"sync"
	"testing"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	internalutil "github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7735StreamExecutor struct {
	identifier string

	mu                sync.Mutex
	seenHeaders       http.Header
	seenMetadata      map[string]any
	seenLogSessionID  string
	seenLogParentID   string
	seenCtxSessionID  string
	executeStreamCall int
}

func (e *issue7735StreamExecutor) Identifier() string { return e.identifier }

func (e *issue7735StreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"status":"completed"}`)}, nil
}

func (e *issue7735StreamExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	clientMeta := internallogging.GetClientRequestMetadata(ctx)
	e.mu.Lock()
	e.executeStreamCall++
	e.seenHeaders = cloneRequestHeaders(opts.Headers)
	e.seenMetadata = maps.Clone(opts.Metadata)
	e.seenLogSessionID = clientMeta.SessionID
	e.seenLogParentID = clientMeta.ParentSessionID
	e.seenCtxSessionID = internalutil.SessionIDFromContext(ctx)
	e.mu.Unlock()

	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"status":"completed"}

`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *issue7735StreamExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *issue7735StreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *issue7735StreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func TestManager_ExecuteStream_ClearHeadersDoesNotRestoreCanonicalSession_Issue7735(t *testing.T) {
	t.Run("ExecuteStream keeps cleared session empty instead of re-deriving from payload", func(t *testing.T) {
		model := "gpt-5.5-issue7735-stream"
		auth := &Auth{
			ID:       "auth-issue7735-stream",
			Provider: "codex",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token": "token-issue7735",
			},
		}
		executor := &issue7735StreamExecutor{identifier: "codex"}
		m := NewManager(nil, nil, nil)
		m.RegisterExecutor(executor)
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
		if _, errReg := m.Register(context.Background(), auth); errReg != nil {
			t.Fatalf("Register error = %v", errReg)
		}

		payload := []byte(`{"model":"gpt-5.5-issue7735-stream","messages":[{"role":"user","content":"hello issue 7735"}]}`)
		var interceptedSession any
		opts := cliproxyexecutor.Options{
			Stream:          true,
			OriginalRequest: payload,
			Headers: http.Header{
				"X-Session-ID": []string{"initial-stream-session"},
			},
			RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				interceptedSession = req.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{
					ClearHeaders: []string{"X-Session-ID"},
				}
			},
		}

		ctx := internallogging.WithClientRequestMetadata(context.Background(), internallogging.ClientRequestMetadata{
			SessionID:       "header:initial-stream-session",
			ParentSessionID: "header:initial-parent",
		})
		ctx = internalutil.WithSessionID(ctx, "header:initial-stream-session")

		streamResult, errStream := m.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{
			Model:   model,
			Payload: payload,
		}, opts)
		if errStream != nil {
			t.Fatalf("ExecuteStream error = %v", errStream)
		}
		for range streamResult.Chunks {
		}

		if interceptedSession != "header:initial-stream-session" {
			t.Fatalf("interceptor CanonicalSessionID = %v, want header:initial-stream-session", interceptedSession)
		}
		if executor.executeStreamCall != 1 {
			t.Fatalf("ExecuteStream call count = %d, want 1", executor.executeStreamCall)
		}
		if gotHeader := executor.seenHeaders.Get("X-Session-ID"); gotHeader != "" {
			t.Fatalf("X-Session-ID header was not cleared: %q", gotHeader)
		}
		if gotCanonical, ok := executor.seenMetadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; ok {
			t.Fatalf("CanonicalSessionIDMetadataKey was restored after ClearHeaders: %v", gotCanonical)
		}
		if gotParent, ok := executor.seenMetadata[cliproxyexecutor.ParentSessionIDMetadataKey]; ok {
			t.Fatalf("ParentSessionIDMetadataKey was retained after ClearHeaders: %v", gotParent)
		}
		if gotLCP, ok := executor.seenMetadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey]; ok {
			t.Fatalf("LCPAffinitySessionIDMetadataKey was retained after ClearHeaders: %v", gotLCP)
		}
		if executor.seenLogSessionID != "" || executor.seenLogParentID != "" {
			t.Fatalf("logging context retained stale session after ClearHeaders: (%q, %q)", executor.seenLogSessionID, executor.seenLogParentID)
		}
		if executor.seenCtxSessionID != "" {
			t.Fatalf("SessionIDFromContext retained stale session after ClearHeaders: %q", executor.seenCtxSessionID)
		}
	})

	t.Run("direct executeStreamWithModelPool populates canonical session before interceptor and does not restore after ClearHeaders", func(t *testing.T) {
		model := "gpt-5.5-issue7735-pool"
		auth := &Auth{
			ID:       "auth-issue7735-pool",
			Provider: "codex",
			Status:   StatusActive,
		}
		executor := &issue7735StreamExecutor{identifier: "codex"}
		m := NewManager(nil, nil, nil)
		payload := []byte(`{"model":"gpt-5.5-issue7735-pool","messages":[{"role":"user","content":"direct pool call"}]}`)

		var interceptedSession any
		opts := cliproxyexecutor.Options{
			Stream:          true,
			OriginalRequest: payload,
			Headers: http.Header{
				"X-Session-ID": []string{"direct-pool-session"},
			},
			RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				interceptedSession = req.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{
					ClearHeaders: []string{"X-Session-ID"},
				}
			},
		}

		streamResult, errStream := m.executeStreamWithModelPool(
			context.Background(),
			executor,
			auth,
			"codex",
			cliproxyexecutor.Request{Model: model, Payload: payload},
			opts,
			model,
			"",
			[]string{model},
			false,
			OAuthModelAliasResult{},
			nil,
			false,
			false,
		)
		if errStream != nil {
			t.Fatalf("executeStreamWithModelPool error = %v", errStream)
		}
		for range streamResult.Chunks {
		}

		if interceptedSession != "header:direct-pool-session" {
			t.Fatalf("interceptor CanonicalSessionID = %v, want header:direct-pool-session", interceptedSession)
		}
		if gotCanonical, ok := executor.seenMetadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; ok {
			t.Fatalf("CanonicalSessionIDMetadataKey was restored after ClearHeaders: %v", gotCanonical)
		}
	})
}

func TestPrepareRequestAfterAuth_HeadersOverrideClearsStaleParentSessionID_Issue7735(t *testing.T) {
	t.Run("standalone header override clears prior parent session without mutating caller metadata", func(t *testing.T) {
		origMeta := map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "session:old-child",
			cliproxyexecutor.ParentSessionIDMetadataKey:    "session:old-parent",
			"custom_key": "keep",
		}
		req := cliproxyexecutor.Request{
			Model: "gpt-5.6-luna",
		}
		opts := cliproxyexecutor.Options{
			Headers:  make(http.Header),
			Metadata: origMeta,
			RequestAfterAuthInterceptor: func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{
					Headers: http.Header{
						"X-Session-ID": []string{"standalone-session"},
					},
				}
			},
		}

		_, finalOpts, errIntercept := prepareRequestAfterAuth(context.Background(), nil, nil, "openai", req, opts, "gpt-5.6-luna")
		if errIntercept != nil {
			t.Fatalf("prepareRequestAfterAuth error = %v", errIntercept)
		}
		if got := finalOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "header:standalone-session" {
			t.Fatalf("CanonicalSessionIDMetadataKey = %v, want header:standalone-session", got)
		}
		if staleParent, ok := finalOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; ok {
			t.Fatalf("ParentSessionIDMetadataKey should be deleted on standalone session override, got %v", staleParent)
		}
		if got := origMeta[cliproxyexecutor.ParentSessionIDMetadataKey]; got != "session:old-parent" {
			t.Fatalf("caller metadata mutated: ParentSessionID = %v, want session:old-parent", got)
		}
	})

	t.Run("header override matching previous parent clears self-referential parent session", func(t *testing.T) {
		origMeta := map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "header:old-child",
			cliproxyexecutor.ParentSessionIDMetadataKey:    "header:old-parent",
		}
		req := cliproxyexecutor.Request{
			Model: "gpt-5.6-luna",
		}
		opts := cliproxyexecutor.Options{
			Headers: http.Header{
				"X-Session-ID":        []string{"old-child"},
				"X-Parent-Session-ID": []string{"old-parent"},
			},
			Metadata: origMeta,
			RequestAfterAuthInterceptor: func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{
					Headers: http.Header{
						"X-Session-ID": []string{"old-parent"},
					},
				}
			},
		}

		_, finalOpts, errIntercept := prepareRequestAfterAuth(context.Background(), nil, nil, "openai", req, opts, "gpt-5.6-luna")
		if errIntercept != nil {
			t.Fatalf("prepareRequestAfterAuth error = %v", errIntercept)
		}
		if got := finalOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "header:old-parent" {
			t.Fatalf("CanonicalSessionIDMetadataKey = %v, want header:old-parent", got)
		}
		if staleParent, ok := finalOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; ok {
			t.Fatalf("ParentSessionIDMetadataKey should be deleted when equal to SessionID, got %v", staleParent)
		}
	})

	t.Run("header override with explicit distinct parent updates both session and parent", func(t *testing.T) {
		opts := cliproxyexecutor.Options{
			Headers: make(http.Header),
			Metadata: map[string]any{
				cliproxyexecutor.CanonicalSessionIDMetadataKey: "header:old-child",
				cliproxyexecutor.ParentSessionIDMetadataKey:    "header:old-parent",
			},
			RequestAfterAuthInterceptor: func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{
					Headers: http.Header{
						"X-Session-ID":        []string{"new-child"},
						"X-Parent-Session-ID": []string{"new-parent"},
					},
				}
			},
		}

		_, finalOpts, errIntercept := prepareRequestAfterAuth(context.Background(), nil, nil, "openai", cliproxyexecutor.Request{Model: "gpt-5.6-luna"}, opts, "gpt-5.6-luna")
		if errIntercept != nil {
			t.Fatalf("prepareRequestAfterAuth error = %v", errIntercept)
		}
		if got := finalOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "header:new-child" {
			t.Fatalf("CanonicalSessionIDMetadataKey = %v, want header:new-child", got)
		}
		if got := finalOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; got != "header:new-parent" {
			t.Fatalf("ParentSessionIDMetadataKey = %v, want header:new-parent", got)
		}
	})

	t.Run("unrelated header addition preserves body-derived parent session for header session", func(t *testing.T) {
		payload := []byte(`{"parent_session_id":"parent"}`)
		opts := cliproxyexecutor.Options{
			Headers: http.Header{
				"X-Session-ID": []string{"child"},
			},
			OriginalRequest: payload,
			Metadata: map[string]any{
				cliproxyexecutor.CanonicalSessionIDMetadataKey: "header:child",
				cliproxyexecutor.ParentSessionIDMetadataKey:    "header:parent",
			},
			RequestAfterAuthInterceptor: func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{
					Headers: http.Header{
						"X-Trace-ID": []string{"trace-1"},
					},
				}
			},
		}

		_, finalOpts, errIntercept := prepareRequestAfterAuth(context.Background(), nil, nil, "openai", cliproxyexecutor.Request{
			Model:   "gpt-5.6-luna",
			Payload: payload,
		}, opts, "gpt-5.6-luna")
		if errIntercept != nil {
			t.Fatalf("prepareRequestAfterAuth error = %v", errIntercept)
		}
		if got := finalOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "header:child" {
			t.Fatalf("CanonicalSessionIDMetadataKey = %v, want header:child", got)
		}
		if got := finalOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; got != "header:parent" {
			t.Fatalf("ParentSessionIDMetadataKey = %v, want header:parent", got)
		}

		syncedCtx := syncMetadataSessionToContext(context.Background(), finalOpts.Metadata)
		meta := internallogging.GetClientRequestMetadata(syncedCtx)
		if meta.SessionID != "header:child" || meta.ParentSessionID != "header:parent" {
			t.Fatalf("synced context hierarchy = (%q, %q), want (header:child, header:parent)", meta.SessionID, meta.ParentSessionID)
		}
	})
}

func TestPrepareRequestAfterAuth_ClonesMetadataPassedToInterceptor_Issue7735(t *testing.T) {
	t.Run("interceptor mutations do not leak into conductor metadata on no-op response", func(t *testing.T) {
		origMeta := map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "session:orig",
			"keep": "v1",
		}
		opts := cliproxyexecutor.Options{
			Metadata: origMeta,
			RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				req.Metadata["injected"] = "leaked"
				req.Metadata["keep"] = "mutated"
				delete(req.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey)
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
			},
		}

		_, finalOpts, errIntercept := prepareRequestAfterAuth(context.Background(), nil, nil, "openai", cliproxyexecutor.Request{Model: "gpt-5.6-luna"}, opts, "gpt-5.6-luna")
		if errIntercept != nil {
			t.Fatalf("prepareRequestAfterAuth error = %v", errIntercept)
		}
		if _, leaked := finalOpts.Metadata["injected"]; leaked {
			t.Fatalf("interceptor metadata mutation leaked into finalOpts.Metadata: %#v", finalOpts.Metadata)
		}
		if got := finalOpts.Metadata["keep"]; got != "v1" {
			t.Fatalf("finalOpts.Metadata[keep] = %v, want v1", got)
		}
		if got := finalOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "session:orig" {
			t.Fatalf("finalOpts.Metadata[CanonicalSessionID] = %v, want session:orig", got)
		}
		if _, leaked := origMeta["injected"]; leaked {
			t.Fatalf("interceptor metadata mutation leaked into caller origMeta: %#v", origMeta)
		}
	})

	t.Run("interceptor mutations do not leak when response also updates headers", func(t *testing.T) {
		origMeta := map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "session:orig",
			"keep": "v1",
		}
		opts := cliproxyexecutor.Options{
			Headers:  make(http.Header),
			Metadata: origMeta,
			RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				req.Metadata["injected"] = "leaked"
				req.Metadata["keep"] = "mutated"
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{
					Headers: http.Header{
						"X-Trace-ID": []string{"trace-1"},
					},
				}
			},
		}

		_, finalOpts, errIntercept := prepareRequestAfterAuth(context.Background(), nil, nil, "openai", cliproxyexecutor.Request{Model: "gpt-5.6-luna"}, opts, "gpt-5.6-luna")
		if errIntercept != nil {
			t.Fatalf("prepareRequestAfterAuth error = %v", errIntercept)
		}
		if _, leaked := finalOpts.Metadata["injected"]; leaked {
			t.Fatalf("interceptor metadata mutation leaked into finalOpts.Metadata: %#v", finalOpts.Metadata)
		}
		if got := finalOpts.Metadata["keep"]; got != "v1" {
			t.Fatalf("finalOpts.Metadata[keep] = %v, want v1", got)
		}
		if _, leaked := origMeta["injected"]; leaked {
			t.Fatalf("interceptor metadata mutation leaked into caller origMeta: %#v", origMeta)
		}
	})

	t.Run("nil metadata stays nil when passed to interceptor", func(t *testing.T) {
		var sawNil bool
		opts := cliproxyexecutor.Options{
			RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				sawNil = req.Metadata == nil
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
			},
		}
		_, finalOpts, errIntercept := prepareRequestAfterAuth(context.Background(), nil, nil, "openai", cliproxyexecutor.Request{Model: "gpt-5.6-luna"}, opts, "gpt-5.6-luna")
		if errIntercept != nil {
			t.Fatalf("prepareRequestAfterAuth error = %v", errIntercept)
		}
		if !sawNil {
			t.Fatalf("expected interceptor to receive nil Metadata when opts.Metadata is nil")
		}
		if finalOpts.Metadata != nil {
			t.Fatalf("finalOpts.Metadata = %#v, want nil", finalOpts.Metadata)
		}
	})
}
