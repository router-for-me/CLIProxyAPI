package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coresession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
)

type quotaAttemptIsolationSelector struct{}

func (quotaAttemptIsolationSelector) Pick(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	var selected *Auth
	for _, auth := range auths {
		if auth != nil && (selected == nil || auth.ID < selected.ID) {
			selected = auth
		}
	}
	return selected, nil
}

type quotaAttemptIsolationExecutor struct{}

func (*quotaAttemptIsolationExecutor) Identifier() string { return "codex" }

func (*quotaAttemptIsolationExecutor) ShouldPrepareRequestAuth(auth *Auth) bool {
	return auth != nil && strings.HasSuffix(auth.ID, "-b")
}

func (*quotaAttemptIsolationExecutor) PrepareRequestAuth(context.Context, *Auth) (*Auth, error) {
	return nil, &Error{HTTPStatus: http.StatusInternalServerError, Message: "prepare failed before upstream response"}
}

func (*quotaAttemptIsolationExecutor) Execute(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	setQuotaAttemptIsolationHeaders(ctx)
	return cliproxyexecutor.Response{}, quotaAttemptIsolationError()
}

func (*quotaAttemptIsolationExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	setQuotaAttemptIsolationHeaders(ctx)
	return nil, quotaAttemptIsolationError()
}

func (*quotaAttemptIsolationExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*quotaAttemptIsolationExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*quotaAttemptIsolationExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func setQuotaAttemptIsolationHeaders(ctx context.Context) {
	internallogging.SetResponseHeaders(ctx, http.Header{
		"X-Codex-Plan-Type":                   []string{"pro"},
		"X-Codex-Primary-Used-Percent":        []string{"91"},
		"X-Codex-Primary-Window-Minutes":      []string{"10080"},
		"X-Codex-Primary-Reset-After-Seconds": []string{"3600"},
	})
}

func quotaAttemptIsolationError() error {
	return &Error{HTTPStatus: http.StatusInternalServerError, Message: "first upstream attempt failed"}
}

func TestExecutionAttemptsDoNotReuseQuotaResponseHeaders(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Manager, context.Context, string) error
	}{
		{
			name: "non-stream",
			run: func(manager *Manager, ctx context.Context, model string) error {
				_, errExecute := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "stream",
			run: func(manager *Manager, ctx context.Context, model string) error {
				_, errExecute := manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				return errExecute
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager(nil, quotaAttemptIsolationSelector{}, nil)
			manager.RegisterExecutor(&quotaAttemptIsolationExecutor{})
			model := "gpt-quota-attempt-isolation-" + test.name
			firstID := "quota-attempt-" + test.name + "-a"
			secondID := "quota-attempt-" + test.name + "-b"
			for _, id := range []string{firstID, secondID} {
				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID:       id,
					Provider: "codex",
					Status:   StatusActive,
				}); errRegister != nil {
					t.Fatalf("Register(%s) error = %v", id, errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}

			ctx := internallogging.WithResponseHeadersHolder(context.Background())
			if errExecute := test.run(manager, ctx, model); errExecute == nil {
				t.Fatal("execution error = nil, want terminal prepare error")
			}

			first, okFirst := manager.GetByID(firstID)
			second, okSecond := manager.GetByID(secondID)
			if !okFirst || first == nil || !okSecond || second == nil {
				t.Fatalf("auth lookup failed: first=%#v second=%#v", first, second)
			}
			if got := first.Quota.Signals["X-Codex-Primary-Used-Percent"]; got != "91" {
				t.Fatalf("first attempt observation = %q, want 91; quota=%#v", got, first.Quota)
			}
			if len(second.Quota.Signals) != 0 || !second.Quota.ObservedAt.IsZero() {
				t.Fatalf("pre-response failure inherited earlier attempt headers: %#v", second.Quota)
			}
			if headers := internallogging.GetResponseHeaders(ctx); len(headers) != 0 {
				t.Fatalf("attempt headers leaked into request holder: %#v", headers)
			}
		})
	}
}

func TestSyncMetadataSessionToContext(t *testing.T) {
	// 1. Canonical session from metadata overrides raw/pre-alias context session
	ctxExplicit := internallogging.WithClientRequestMetadata(context.Background(), internallogging.ClientRequestMetadata{
		SessionID: "raw-alias-sid",
	})
	res1 := syncMetadataSessionToContext(ctxExplicit, map[string]any{
		cliproxyexecutor.CanonicalSessionIDMetadataKey: "canonical-override",
	})
	meta1 := internallogging.GetClientRequestMetadata(res1)
	if meta1.SessionID != "canonical-override" {
		t.Fatalf("sessionID = %q, want canonical-override", meta1.SessionID)
	}

	// 2. Syncs canonical and parent session from metadata
	res2 := syncMetadataSessionToContext(context.Background(), map[string]any{
		cliproxyexecutor.CanonicalSessionIDMetadataKey: "lcp:branch-123",
		cliproxyexecutor.ParentSessionIDMetadataKey:    "lcp:trunk-000",
	})
	meta2 := internallogging.GetClientRequestMetadata(res2)
	if meta2.SessionID != "lcp:branch-123" || meta2.ParentSessionID != "lcp:trunk-000" {
		t.Fatalf("synced session = (%q, %q), want (lcp:branch-123, lcp:trunk-000)", meta2.SessionID, meta2.ParentSessionID)
	}

	// 3. Eliminates self-loop
	res3 := syncMetadataSessionToContext(context.Background(), map[string]any{
		cliproxyexecutor.CanonicalSessionIDMetadataKey: "same-loop",
		cliproxyexecutor.ParentSessionIDMetadataKey:    "same-loop",
	})
	meta3 := internallogging.GetClientRequestMetadata(res3)
	if meta3.SessionID != "same-loop" || meta3.ParentSessionID != "" {
		t.Fatalf("self-loop session = (%q, %q), want (same-loop, empty)", meta3.SessionID, meta3.ParentSessionID)
	}

	// 4. Clears stale parent when syncing a new root session without parent metadata
	ctxWithStaleParent := internallogging.WithClientRequestMetadata(context.Background(), internallogging.ClientRequestMetadata{
		SessionID:       "old-child",
		ParentSessionID: "old-stale-parent",
	})
	res4 := syncMetadataSessionToContext(ctxWithStaleParent, map[string]any{
		cliproxyexecutor.CanonicalSessionIDMetadataKey: "new-root-session",
	})
	meta4 := internallogging.GetClientRequestMetadata(res4)
	if meta4.SessionID != "new-root-session" || meta4.ParentSessionID != "" {
		t.Fatalf("stale parent not cleared: (%q, %q), want (new-root-session, empty)", meta4.SessionID, meta4.ParentSessionID)
	}

	// 5. Execution session key fallback with prefix
	res5 := syncMetadataSessionToContext(context.Background(), map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "call-live-123",
	})
	meta5 := internallogging.GetClientRequestMetadata(res5)
	if meta5.SessionID != "execution:call-live-123" {
		t.Fatalf("execution session = %q, want execution:call-live-123", meta5.SessionID)
	}

	// 6. Derived session key fallback gets derived: prefix
	res6 := syncMetadataSessionToContext(context.Background(), map[string]any{
		cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:raw-hash",
	})
	meta6 := internallogging.GetClientRequestMetadata(res6)
	if meta6.SessionID != "derived:ctx:v1:raw-hash" {
		t.Fatalf("derived session = %q, want derived:ctx:v1:raw-hash", meta6.SessionID)
	}

	// 7. Clears hierarchy completely when metadata has no canonical session
	ctxWithExistingSession := internallogging.WithClientRequestMetadata(context.Background(), internallogging.ClientRequestMetadata{
		SessionID:       "old-stale-session",
		ParentSessionID: "old-stale-parent",
	})
	res7 := syncMetadataSessionToContext(ctxWithExistingSession, map[string]any{})
	meta7 := internallogging.GetClientRequestMetadata(res7)
	if meta7.SessionID != "" || meta7.ParentSessionID != "" {
		t.Fatalf("hierarchy not cleared when metadata empty: (%q, %q), want empty", meta7.SessionID, meta7.ParentSessionID)
	}
}

func TestApplyRequestAfterAuthInterceptorSessionClearing(t *testing.T) {
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: nil,
	}
	opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Session-ID": []string{"initial-session"},
		},
		Metadata: map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "session:initial-session",
			cliproxyexecutor.ParentSessionIDMetadataKey:    "session:initial-parent",
		},
		RequestAfterAuthInterceptor: func(ctx context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{
				ClearHeaders: []string{"X-Session-ID"},
			}
		},
	}

	finalReq, finalOpts, err := applyRequestAfterAuthInterceptor(context.Background(), nil, "openai", req, opts, "gpt-5.6-luna")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(finalOpts.Headers) != 0 {
		t.Fatalf("headers not cleared: %v", finalOpts.Headers)
	}
	if _, ok := finalOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; ok {
		t.Fatalf("canonical session was not cleared from metadata after interceptor cleared headers")
	}
	if _, ok := finalOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; ok {
		t.Fatalf("parent session was not cleared from metadata after interceptor cleared headers")
	}

	// Context synced from cleared metadata also has empty session
	ctxWithSession := internallogging.WithClientRequestMetadata(context.Background(), internallogging.ClientRequestMetadata{
		SessionID:       "session:initial-session",
		ParentSessionID: "session:initial-parent",
	})
	syncedCtx := syncMetadataSessionToContext(ctxWithSession, finalOpts.Metadata)
	meta := internallogging.GetClientRequestMetadata(syncedCtx)
	if meta.SessionID != "" || meta.ParentSessionID != "" {
		t.Fatalf("context retained stale session after interceptor cleared headers: (%q, %q)", meta.SessionID, meta.ParentSessionID)
	}
	_ = finalReq
}

func TestGhostParentElimination(t *testing.T) {
	// Request has explicit root session (no parent), but options metadata carries a stale parent key
	req := cliproxyexecutor.Request{}
	opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Session-ID": []string{"clean-root-session"},
		},
		Metadata: map[string]any{
			cliproxyexecutor.ParentSessionIDMetadataKey: "ghost-parent-from-prior-turn",
		},
	}

	_, enrichedOpts := coresession.Enrich(req, opts)
	if _, hasGhost := enrichedOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; hasGhost {
		t.Fatalf("ghost parent was not removed by session.Enrich")
	}
	if canonical := enrichedOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; canonical != "header:clean-root-session" {
		t.Fatalf("canonical session = %v, want header:clean-root-session", canonical)
	}
}

func TestApplyRequestAfterAuthInterceptorPreservesOriginalRequestSessionOnUnrelatedHeaderChange(t *testing.T) {
	bodyWithSession := []byte(`{"session_id":"important-session"}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: nil, // Translated or omitted payload
	}
	opts := cliproxyexecutor.Options{
		OriginalRequest: bodyWithSession,
		Headers:         make(http.Header),
		Metadata: map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "session:important-session",
		},
		RequestAfterAuthInterceptor: func(ctx context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{
				Headers: http.Header{
					"X-Trace-ID": []string{"trace-abc"},
				},
			}
		},
	}

	_, finalOpts, err := applyRequestAfterAuthInterceptor(context.Background(), nil, "openai", req, opts, "gpt-5.6-luna")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	canonical, ok := finalOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
	if !ok || canonical != "session:important-session" {
		t.Fatalf("canonical session = %q (ok=%v), want session:important-session", canonical, ok)
	}
}

func TestApplyRequestAfterAuthInterceptorPreservesLCPHierarchyOnUnrelatedHeaderChange(t *testing.T) {
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		Headers: make(http.Header),
		Metadata: map[string]any{
			cliproxyexecutor.LCPAffinitySessionIDMetadataKey: "lcp:v1:child-fork-123",
			cliproxyexecutor.ParentSessionIDMetadataKey:      "lcp:v1:parent-trunk-000",
			cliproxyexecutor.CanonicalSessionIDMetadataKey:   "lcp:v1:child-fork-123",
		},
		RequestAfterAuthInterceptor: func(ctx context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{
				Headers: http.Header{
					"X-Trace-ID": []string{"trace-xyz"},
				},
			}
		},
	}

	_, finalOpts, err := applyRequestAfterAuthInterceptor(context.Background(), nil, "openai", req, opts, "gpt-5.6-luna")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	canonical, ok := finalOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
	if !ok || canonical != "lcp:v1:child-fork-123" {
		t.Fatalf("canonical session = %q (ok=%v), want lcp:v1:child-fork-123", canonical, ok)
	}
	parent, okParent := finalOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey].(string)
	if !okParent || parent != "lcp:v1:parent-trunk-000" {
		t.Fatalf("parent session = %q (ok=%v), want lcp:v1:parent-trunk-000", parent, okParent)
	}

	syncedCtx := syncMetadataSessionToContext(context.Background(), finalOpts.Metadata)
	meta := internallogging.GetClientRequestMetadata(syncedCtx)
	if meta.SessionID != "lcp:v1:child-fork-123" || meta.ParentSessionID != "lcp:v1:parent-trunk-000" {
		t.Fatalf("synced context hierarchy = (%q, %q), want (lcp:v1:child-fork-123, lcp:v1:parent-trunk-000)", meta.SessionID, meta.ParentSessionID)
	}
}
