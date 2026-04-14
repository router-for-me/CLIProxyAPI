package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"golang.org/x/net/context"
)

func TestRequestExecutionMetadataUsesIdempotencyHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ginCtx.Request.Header.Set("Idempotency-Key", "client-key")
	logging.SetGinRequestID(ginCtx, "req-ignored")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	meta := requestExecutionMetadata(ctx)

	if got := meta[idempotencyKeyMetadataKey]; got != "client-key" {
		t.Fatalf("idempotency key = %v, want client-key", got)
	}
}

func TestRequestExecutionMetadataFallsBackToRequestID(t *testing.T) {
	ctx := logging.WithRequestID(context.Background(), "req-1234")
	meta := requestExecutionMetadata(ctx)

	if got := meta[idempotencyKeyMetadataKey]; got != "req-1234" {
		t.Fatalf("idempotency key = %v, want req-1234", got)
	}
}

func TestRequestExecutionMetadataIncludesExecutionHints(t *testing.T) {
	base := logging.WithRequestID(context.Background(), "req-5678")
	base = WithPinnedAuthID(base, "auth-1")
	base = WithExecutionSessionID(base, "session-1")

	callbackCalled := false
	base = WithSelectedAuthIDCallback(base, func(authID string) {
		callbackCalled = authID != ""
	})

	meta := requestExecutionMetadata(base)
	if got := meta[idempotencyKeyMetadataKey]; got != "req-5678" {
		t.Fatalf("idempotency key = %v, want req-5678", got)
	}
	if got := meta[coreexecutor.PinnedAuthMetadataKey]; got != "auth-1" {
		t.Fatalf("pinned auth = %v, want auth-1", got)
	}
	if got := meta[coreexecutor.ExecutionSessionMetadataKey]; got != "session-1" {
		t.Fatalf("execution session = %v, want session-1", got)
	}
	callback, ok := meta[coreexecutor.SelectedAuthCallbackMetadataKey].(func(string))
	if !ok || callback == nil {
		t.Fatalf("selected auth callback missing")
	}
	callback("auth-1")
	if !callbackCalled {
		t.Fatalf("selected auth callback was not preserved")
	}
}

func TestBuildExecutionMetadataUsesSessionAffinityBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "auth-1",
		Provider: "gemini",
		Metadata: map[string]any{"token": "x"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	store := NewMemorySessionAffinityStore()
	store.Set(context.Background(), "session-a", "auth-1")
	handler := &BaseAPIHandler{
		AuthManager:          manager,
		SessionAffinityStore: store,
	}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ginCtx.Request.Header.Set(defaultSessionAffinityHeader, "session-a")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	meta := handler.buildExecutionMetadata(ctx, []string{"gemini"}, "", nil)

	if got := meta[coreexecutor.PinnedAuthMetadataKey]; got != "auth-1" {
		t.Fatalf("pinned auth = %v, want auth-1", got)
	}
}

func TestBuildExecutionMetadataDeletesStaleSessionAffinityBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:             "auth-stale",
		Provider:       "gemini",
		Unavailable:    true,
		NextRetryAfter: time.Now().Add(time.Minute),
		Metadata:       map[string]any{"token": "x"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	store := NewMemorySessionAffinityStore()
	store.Set(context.Background(), "session-stale", "auth-stale")
	handler := &BaseAPIHandler{
		AuthManager:          manager,
		SessionAffinityStore: store,
	}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ginCtx.Request.Header.Set(defaultSessionAffinityHeader, "session-stale")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	meta := handler.buildExecutionMetadata(ctx, []string{"gemini"}, "", nil)

	if _, ok := meta[coreexecutor.PinnedAuthMetadataKey]; ok {
		t.Fatalf("unexpected pinned auth for stale binding")
	}
	if _, ok := store.Get(context.Background(), "session-stale"); ok {
		t.Fatalf("expected stale binding to be deleted")
	}
}

func TestBuildExecutionMetadataBindsSelectedAuthToSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "auth-new",
		Provider: "gemini",
		Metadata: map[string]any{"token": "x"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	store := NewMemorySessionAffinityStore()
	handler := &BaseAPIHandler{
		AuthManager:          manager,
		SessionAffinityStore: store,
	}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ginCtx.Request.Header.Set(defaultSessionAffinityHeader, "session-bind")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	meta := handler.buildExecutionMetadata(ctx, []string{"gemini"}, "", nil)
	callback, ok := meta[coreexecutor.SelectedAuthCallbackMetadataKey].(func(string))
	if !ok || callback == nil {
		t.Fatalf("selected auth callback missing")
	}

	callback("auth-new")

	if got, ok := store.Get(context.Background(), "session-bind"); !ok || got != "auth-new" {
		t.Fatalf("binding = %q, %v; want auth-new, true", got, ok)
	}
}

func TestBuildExecutionMetadataPinnedAuthReleaseDeletesSessionBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "auth-release",
		Provider: "gemini",
		Metadata: map[string]any{"token": "x"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	store := NewMemorySessionAffinityStore()
	store.Set(context.Background(), "session-release", "auth-release")
	handler := &BaseAPIHandler{
		AuthManager:          manager,
		SessionAffinityStore: store,
	}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ginCtx.Request.Header.Set(defaultSessionAffinityHeader, "session-release")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	meta := handler.buildExecutionMetadata(ctx, []string{"gemini"}, "", nil)
	callback, ok := meta[coreexecutor.PinnedAuthReleaseCallbackMetadataKey].(func())
	if !ok || callback == nil {
		t.Fatalf("pinned auth release callback missing")
	}

	callback()

	if _, ok := store.Get(context.Background(), "session-release"); ok {
		t.Fatalf("expected session affinity binding to be deleted")
	}
}

func TestBuildExecutionMetadataUsesConfiguredSessionAffinityHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "auth-custom",
		Provider: "gemini",
		Metadata: map[string]any{"token": "x"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	store := NewMemorySessionAffinityStore()
	store.Set(context.Background(), "custom-session", "auth-custom")
	handler := &BaseAPIHandler{
		AuthManager:          manager,
		SessionAffinityStore: store,
		Cfg: &sdkconfig.SDKConfig{
			SessionAffinity: sdkconfig.SessionAffinityConfig{
				Header: "X-Custom-Affinity",
			},
		},
	}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ginCtx.Request.Header.Set("X-Custom-Affinity", "custom-session")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	meta := handler.buildExecutionMetadata(ctx, []string{"gemini"}, "", nil)

	if got := meta[coreexecutor.PinnedAuthMetadataKey]; got != "auth-custom" {
		t.Fatalf("pinned auth = %v, want auth-custom", got)
	}
}

func TestBuildExecutionMetadataDoesNotUseExecutionSessionAsAffinityKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "auth-session",
		Provider: "gemini",
		Metadata: map[string]any{"token": "x"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	store := NewMemorySessionAffinityStore()
	store.Set(context.Background(), "exec-session-1", "auth-session")
	handler := &BaseAPIHandler{
		AuthManager:          manager,
		SessionAffinityStore: store,
	}

	ctx := WithExecutionSessionID(context.Background(), "exec-session-1")
	meta := handler.buildExecutionMetadata(ctx, []string{"gemini"}, "", nil)

	if _, ok := meta[coreexecutor.PinnedAuthMetadataKey]; ok {
		t.Fatalf("execution session id must not act as session affinity key")
	}
}

func TestBuildExecutionMetadataDerivesSessionAffinityKeyFromPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "auth-prev",
		Provider: "gemini",
		Metadata: map[string]any{"token": "x"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	store := NewMemorySessionAffinityStore()
	store.Set(context.Background(), "body:previous_response_id:resp_123", "auth-prev")
	handler := &BaseAPIHandler{
		AuthManager:             manager,
		SessionAffinityStore:    store,
		sessionAffinityHotCache: newSessionAffinityHotCache(defaultSessionAffinityHotCacheTTL),
	}

	meta := handler.buildExecutionMetadata(context.Background(), []string{"gemini"}, "", []byte(`{"model":"gpt-test","previous_response_id":"resp_123"}`))

	if got := meta[coreexecutor.PinnedAuthMetadataKey]; got != "auth-prev" {
		t.Fatalf("pinned auth = %v, want auth-prev", got)
	}
}

func TestBuildExecutionMetadataUsesCodexSessionHeaderForAffinity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "auth-codex",
		Provider: "codex",
		Metadata: map[string]any{"token": "x"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	store := NewMemorySessionAffinityStore()
	store.Set(context.Background(), "019d8a94-b1e5-7b52-8f2f-ef2423b4513e", "auth-codex")
	handler := &BaseAPIHandler{
		AuthManager:             manager,
		SessionAffinityStore:    store,
		sessionAffinityHotCache: newSessionAffinityHotCache(defaultSessionAffinityHotCacheTTL),
	}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ginCtx.Request.Header.Set("Session_id", "019d8a94-b1e5-7b52-8f2f-ef2423b4513e")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	meta := handler.buildExecutionMetadata(ctx, []string{"codex"}, "", []byte(`{"model":"gpt-5-codex"}`))

	if got := meta[coreexecutor.PinnedAuthMetadataKey]; got != "auth-codex" {
		t.Fatalf("pinned auth = %v, want auth-codex", got)
	}
}

func TestBuildExecutionMetadataUsesCodexClientRequestIDForAffinity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "auth-codex",
		Provider: "codex",
		Metadata: map[string]any{"token": "x"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	store := NewMemorySessionAffinityStore()
	store.Set(context.Background(), "019d8a94-b1e5-7b52-8f2f-ef2423b4513e", "auth-codex")
	handler := &BaseAPIHandler{
		AuthManager:             manager,
		SessionAffinityStore:    store,
		sessionAffinityHotCache: newSessionAffinityHotCache(defaultSessionAffinityHotCacheTTL),
	}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ginCtx.Request.Header.Set("X-Client-Request-Id", "019d8a94-b1e5-7b52-8f2f-ef2423b4513e")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	meta := handler.buildExecutionMetadata(ctx, []string{"codex"}, "", []byte(`{"model":"gpt-5-codex"}`))

	if got := meta[coreexecutor.PinnedAuthMetadataKey]; got != "auth-codex" {
		t.Fatalf("pinned auth = %v, want auth-codex", got)
	}
}

func TestBuildExecutionMetadataPrefersStableCodexSessionIDOverPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "auth-session",
		Provider: "codex",
		Metadata: map[string]any{"token": "x"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "auth-prev",
		Provider: "codex",
		Metadata: map[string]any{"token": "x"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	store := NewMemorySessionAffinityStore()
	store.Set(context.Background(), "body:session_id:session-123", "auth-session")
	store.Set(context.Background(), "body:previous_response_id:resp-123", "auth-prev")
	handler := &BaseAPIHandler{
		AuthManager:             manager,
		SessionAffinityStore:    store,
		sessionAffinityHotCache: newSessionAffinityHotCache(defaultSessionAffinityHotCacheTTL),
	}

	meta := handler.buildExecutionMetadata(context.Background(), []string{"codex"}, "", []byte(`{"model":"gpt-5-codex","session_id":"session-123","previous_response_id":"resp-123"}`))

	if got := meta[coreexecutor.PinnedAuthMetadataKey]; got != "auth-session" {
		t.Fatalf("pinned auth = %v, want auth-session", got)
	}
}

func TestBuildExecutionMetadataDoesNotDeriveSessionAffinityKeyFromBodyFingerprint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "auth-a",
		Provider: "gemini",
		Metadata: map[string]any{"token": "x"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	payload := []byte(`{"model":"gpt-test","input":[{"role":"user","content":"hi"}]}`)
	store := NewMemorySessionAffinityStore()
	handler := &BaseAPIHandler{
		AuthManager:             manager,
		SessionAffinityStore:    store,
		sessionAffinityHotCache: newSessionAffinityHotCache(defaultSessionAffinityHotCacheTTL),
	}

	meta := handler.buildExecutionMetadata(context.Background(), []string{"gemini"}, "", payload)

	if _, ok := meta[coreexecutor.PinnedAuthMetadataKey]; ok {
		t.Fatalf("pinned auth must be empty for body fingerprint fallback")
	}
	if _, ok := meta[coreexecutor.InitialStickySessionMetadataKey]; ok {
		t.Fatalf("initial sticky marker must be empty without an explicit session key")
	}
}
