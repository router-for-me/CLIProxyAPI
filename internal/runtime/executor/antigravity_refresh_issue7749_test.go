package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type issue7749ContextKey struct{}

type issue7749MemoryStore struct {
	mu    sync.Mutex
	auths map[string]*cliproxyauth.Auth
	saves int
}

func newIssue7749MemoryStore() *issue7749MemoryStore {
	return &issue7749MemoryStore{auths: make(map[string]*cliproxyauth.Auth)}
}

func (s *issue7749MemoryStore) List(context.Context) ([]*cliproxyauth.Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*cliproxyauth.Auth, 0, len(s.auths))
	for _, a := range s.auths {
		out = append(out, a.Clone())
	}
	return out, nil
}

func (s *issue7749MemoryStore) Save(_ context.Context, auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auths[auth.ID] = auth.Clone()
	s.saves++
	return auth.ID, nil
}

func (s *issue7749MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.auths, id)
	return nil
}

func (s *issue7749MemoryStore) get(id string) *cliproxyauth.Auth {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.auths[id]; a != nil {
		return a.Clone()
	}
	return nil
}

type issue7749ResultHook struct {
	cliproxyauth.NoopHook
	mu      sync.Mutex
	results []cliproxyauth.Result
}

func (h *issue7749ResultHook) OnResult(_ context.Context, result cliproxyauth.Result) {
	h.mu.Lock()
	h.results = append(h.results, result)
	h.mu.Unlock()
}

func (h *issue7749ResultHook) snapshot() []cliproxyauth.Result {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]cliproxyauth.Result, len(h.results))
	copy(out, h.results)
	return out
}

type issue7749StaticRTProvider struct {
	rt http.RoundTripper
}

func (p issue7749StaticRTProvider) RoundTripperFor(*cliproxyauth.Auth) http.RoundTripper {
	return p.rt
}

func TestIssue7749AntigravityExpiringTokenPersistsOnceAcrossConsecutiveRequests(t *testing.T) {
	resetAntigravityRefreshGroupForTest()
	t.Cleanup(resetAntigravityRefreshGroupForTest)
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var (
		tokenCalls    atomic.Int32
		generateCalls atomic.Int32
	)
	const (
		oldAccessToken  = "expiring-access-7749"
		newAccessToken  = "refreshed-access-7749"
		oldRefreshToken = "old-refresh-7749"
		newRefreshToken = "rotated-refresh-7749"
		geminiModelID   = "gemini-3.6-flash"
		claudeModelID   = "claude-sonnet-4-5"
	)

	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.String() == "https://oauth2.googleapis.com/token":
			tokenCalls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(fmt.Sprintf(
					`{"access_token":%q,"refresh_token":%q,"expires_in":3600,"token_type":"Bearer"}`,
					newAccessToken, newRefreshToken,
				))),
			}, nil

		case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"paidTier":{"id":"tier-7749","availableCredits":[]}}`)),
			}, nil

		case strings.Contains(req.URL.Path, "streamGenerateContent"):
			generateCalls.Add(1)
			if got := req.Header.Get("Authorization"); got != "Bearer "+newAccessToken {
				t.Errorf("streamGenerate Authorization = %q, want Bearer %s", got, newAccessToken)
			}
			sse := "data: " + `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}}` + "\n\n"
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(sse)),
			}, nil

		case strings.Contains(req.URL.Path, "generateContent"):
			generateCalls.Add(1)
			if got := req.Header.Get("Authorization"); got != "Bearer "+newAccessToken {
				t.Errorf("generate Authorization = %q, want Bearer %s", got, newAccessToken)
			}
			body := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil

		default:
			return nil, fmt.Errorf("unexpected HTTP request: %s %s", req.Method, req.URL.String())
		}
	})

	store := newIssue7749MemoryStore()
	hook := &issue7749ResultHook{}
	exec := NewAntigravityExecutor(&config.Config{})
	manager := cliproxyauth.NewManager(store, nil, hook)
	manager.SetRoundTripperProvider(issue7749StaticRTProvider{rt: transport})
	manager.RegisterExecutor(exec)

	authID := "ag-issue-7749-persist-" + uuid.NewString()
	initialExpiry := time.Now().UTC().Add(antigravityRequestTokenSafetyWindow - time.Minute)
	auth := &cliproxyauth.Auth{
		ID:       authID,
		FileName: authID + ".json",
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"type":          "antigravity",
			"email":         "ag-7749@example.com",
			"project_id":    "proj-existing-7749",
			"access_token":  oldAccessToken,
			"refresh_token": oldRefreshToken + "-" + uuid.NewString(),
			"expired":       initialExpiry.Format(time.RFC3339),
		},
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register auth: %v", errReg)
	}
	if exec.ShouldPrepareRequestAuth(nil) {
		t.Fatal("ShouldPrepareRequestAuth(nil) = true, want false")
	}

	registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{
		{ID: geminiModelID},
		{ID: claudeModelID},
	})
	t.Cleanup(func() {
		if value, ok := antigravityCreditsHintRefreshByID.Load(authID); ok {
			if state, okState := value.(*antigravityCreditsHintRefreshState); okState && state != nil {
				waitForAntigravityCreditsRefresh(t, state)
			}
		}
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	}

	// Request 1: non-stream Gemini (Execute).
	if _, errExec := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
		Model:   geminiModelID,
		Payload: payload,
	}, opts); errExec != nil {
		t.Fatalf("Execute #1 (gemini) error = %v", errExec)
	}

	// Request 2: non-stream Claude (executeClaudeNonStream).
	if _, errExec := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
		Model:   claudeModelID,
		Payload: payload,
	}, opts); errExec != nil {
		t.Fatalf("Execute #2 (claude) error = %v", errExec)
	}

	// Request 3: streaming Gemini (ExecuteStream).
	streamOpts := opts
	streamOpts.Stream = true
	streamRes, errStream := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
		Model:   geminiModelID,
		Payload: payload,
	}, streamOpts)
	if errStream != nil {
		t.Fatalf("ExecuteStream #3 error = %v", errStream)
	}
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			t.Fatalf("ExecuteStream #3 chunk error = %v", chunk.Err)
		}
	}

	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("POST https://oauth2.googleapis.com/token calls = %d, want 1 across 3 consecutive requests", got)
	}
	if got := generateCalls.Load(); got != 3 {
		t.Fatalf("generateContent calls = %d, want 3", got)
	}

	managed, okManaged := manager.GetByID(authID)
	if !okManaged || managed == nil {
		t.Fatalf("managed auth %s not found", authID)
	}
	if got := metaStringValue(managed.Metadata, "access_token"); got != newAccessToken {
		t.Fatalf("managed access_token = %q, want %q", got, newAccessToken)
	}
	if got := metaStringValue(managed.Metadata, "refresh_token"); got != newRefreshToken {
		t.Fatalf("managed refresh_token = %q, want rotated %q", got, newRefreshToken)
	}
	if exp, okExp := managed.ExpirationTime(); !okExp || !exp.After(time.Now().Add(antigravityRequestTokenSafetyWindow)) {
		t.Fatalf("managed expiry = %v (ok=%v), want > now+%s", exp, okExp, antigravityRequestTokenSafetyWindow)
	}

	persisted := store.get(authID)
	if persisted == nil {
		t.Fatal("persisted auth not found in store")
	}
	if got := metaStringValue(persisted.Metadata, "access_token"); got != newAccessToken {
		t.Fatalf("persisted access_token = %q, want %q", got, newAccessToken)
	}
	if got := metaStringValue(persisted.Metadata, "refresh_token"); got != newRefreshToken {
		t.Fatalf("persisted refresh_token = %q, want rotated %q", got, newRefreshToken)
	}

	results := hook.snapshot()
	if len(results) != 3 {
		t.Fatalf("recorded MarkResult count = %d, want 3", len(results))
	}
	for i, res := range results {
		if res.AccessToken != newAccessToken {
			t.Fatalf("results[%d].AccessToken = %q, want %q", i, res.AccessToken, newAccessToken)
		}
		if res.RefreshToken != newRefreshToken {
			t.Fatalf("results[%d].RefreshToken = %q, want %q", i, res.RefreshToken, newRefreshToken)
		}
	}
}

func TestIssue7749AntigravityFailedRequestRecordsRefreshedAccessTokenInMarkResult(t *testing.T) {
	resetAntigravityRefreshGroupForTest()
	t.Cleanup(resetAntigravityRefreshGroupForTest)
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	const (
		oldAccessToken = "stale-access-7749-fail"
		newAccessToken = "fresh-access-7749-fail"
		modelID        = "gemini-3.6-flash"
	)

	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.String() == "https://oauth2.googleapis.com/token":
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(fmt.Sprintf(
					`{"access_token":%q,"expires_in":3600,"token_type":"Bearer"}`,
					newAccessToken,
				))),
			}, nil

		case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"paidTier":{"id":"tier-7749","availableCredits":[]}}`)),
			}, nil

		case strings.Contains(strings.ToLower(req.URL.Path), "generatecontent"):
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"code":500,"message":"upstream failure"}}`)),
			}, nil

		default:
			return nil, fmt.Errorf("unexpected HTTP request: %s %s", req.Method, req.URL.String())
		}
	})

	hook := &issue7749ResultHook{}
	exec := NewAntigravityExecutor(&config.Config{})
	manager := cliproxyauth.NewManager(newIssue7749MemoryStore(), nil, hook)
	manager.SetRoundTripperProvider(issue7749StaticRTProvider{rt: transport})
	manager.RegisterExecutor(exec)

	authID := "ag-issue-7749-fail-" + uuid.NewString()
	auth := &cliproxyauth.Auth{
		ID:       authID,
		FileName: authID + ".json",
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"type":          "antigravity",
			"project_id":    "proj-fail-7749",
			"access_token":  oldAccessToken,
			"refresh_token": "refresh-fail-7749-" + uuid.NewString(),
			"expired":       time.Now().UTC().Add(antigravityRequestTokenSafetyWindow - time.Minute).Format(time.RFC3339),
		},
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register auth: %v", errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{
		{ID: modelID},
	})
	t.Cleanup(func() {
		if value, ok := antigravityCreditsHintRefreshByID.Load(authID); ok {
			if state, okState := value.(*antigravityCreditsHintRefreshState); okState && state != nil {
				waitForAntigravityCreditsRefresh(t, state)
			}
		}
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	_, errExec := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
		Model:   modelID,
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if errExec == nil {
		t.Fatal("Execute() error = nil, want upstream 500 failure")
	}

	results := hook.snapshot()
	if len(results) == 0 {
		t.Fatal("expected at least one MarkResult call")
	}
	last := results[len(results)-1]
	if last.AccessToken != newAccessToken {
		t.Fatalf("failed Result.AccessToken = %q, want refreshed %q", last.AccessToken, newAccessToken)
	}

	// Verify ExecuteStream failure also records the refreshed AccessToken in MarkResult.
	streamAuthID := "ag-issue-7749-fail-stream-" + uuid.NewString()
	streamAuth := &cliproxyauth.Auth{
		ID:       streamAuthID,
		FileName: streamAuthID + ".json",
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"type":          "antigravity",
			"email":         "ag-7749-fail-stream@example.com",
			"project_id":    "proj-fail-stream-7749",
			"access_token":  oldAccessToken,
			"refresh_token": "old-refresh-fail-stream-" + uuid.NewString(),
			"expired":       time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339),
		},
	}
	registry.GetGlobalRegistry().RegisterClient(streamAuthID, "antigravity", []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		if value, ok := antigravityCreditsHintRefreshByID.Load(streamAuthID); ok {
			if state, okState := value.(*antigravityCreditsHintRefreshState); okState && state != nil {
				waitForAntigravityCreditsRefresh(t, state)
			}
		}
		registry.GetGlobalRegistry().UnregisterClient(streamAuthID)
	})
	streamHook := &issue7749ResultHook{}
	streamManager := cliproxyauth.NewManager(newIssue7749MemoryStore(), nil, streamHook)
	streamManager.SetRoundTripperProvider(issue7749StaticRTProvider{rt: transport})
	streamManager.RegisterExecutor(exec)
	if _, errReg := streamManager.Register(context.Background(), streamAuth); errReg != nil {
		t.Fatalf("Register stream auth: %v", errReg)
	}
	if _, errStream := streamManager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
		Model:   modelID,
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
		Stream:       true,
	}); errStream == nil {
		t.Fatal("ExecuteStream() error = nil, want upstream 500 failure")
	}
	streamResults := streamHook.snapshot()
	if len(streamResults) == 0 {
		t.Fatal("expected at least one MarkResult call for failed ExecuteStream")
	}
	if lastStream := streamResults[len(streamResults)-1]; lastStream.AccessToken != newAccessToken {
		t.Fatalf("failed ExecuteStream Result.AccessToken = %q, want refreshed %q", lastStream.AccessToken, newAccessToken)
	}
}

func TestIssue7749EnsureAccessTokenPreservesContextValuesAndExcludesRequestProxy(t *testing.T) {
	resetAntigravityRefreshGroupForTest()
	t.Cleanup(resetAntigravityRefreshGroupForTest)
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	t.Run("preserves decorator and context values while clearing RequestProxyURL", func(t *testing.T) {
		var (
			decoratorCalled atomic.Bool
			seenCustomVal   atomic.Value
			seenReqProxy    atomic.Value
		)
		baseRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case req.URL.String() == "https://oauth2.googleapis.com/token":
				seenCustomVal.Store(fmt.Sprint(req.Context().Value(issue7749ContextKey{})))
				seenReqProxy.Store(cliproxyexecutor.RequestProxyURL(req.Context()))
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"access_token":"tok-ctx-7749","expires_in":3600}`)),
				}, nil
			case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"paidTier":{"id":"tier","availableCredits":[]}}`)),
				}, nil
			default:
				return nil, fmt.Errorf("unexpected request: %s", req.URL)
			}
		})

		ctx := context.Background()
		ctx = context.WithValue(ctx, issue7749ContextKey{}, "marker-7749")
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(baseRT))
		ctx = cliproxyexecutor.WithRequestProxyURL(ctx, "http://request-proxy.example:8081")
		ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
			return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() == "https://oauth2.googleapis.com/token" {
					decoratorCalled.Store(true)
				}
				return next.RoundTrip(req)
			})
		})

		authID := "ag-ctx-7749-" + uuid.NewString()
		auth := &cliproxyauth.Auth{
			ID:       authID,
			Provider: "antigravity",
			Metadata: map[string]any{
				"project_id":    "proj-ctx-7749",
				"access_token":  "expiring",
				"refresh_token": "refresh-ctx-7749-" + uuid.NewString(),
				"expired":       time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
			},
		}
		t.Cleanup(func() {
			if value, ok := antigravityCreditsHintRefreshByID.Load(authID); ok {
				if state, okState := value.(*antigravityCreditsHintRefreshState); okState && state != nil {
					waitForAntigravityCreditsRefresh(t, state)
				}
			}
		})

		exec := NewAntigravityExecutor(&config.Config{})
		token, updated, errToken := exec.ensureAccessToken(ctx, auth)
		if errToken != nil || token != "tok-ctx-7749" || updated == nil {
			t.Fatalf("ensureAccessToken() = (%q, %v, %v), want refreshed token", token, updated, errToken)
		}
		if !decoratorCalled.Load() {
			t.Fatal("ensureAccessToken dropped httptransport.Decorator from refresh context")
		}
		if got, _ := seenCustomVal.Load().(string); got != "marker-7749" {
			t.Fatalf("refresh context custom value = %q, want marker-7749", got)
		}
		if got, _ := seenReqProxy.Load().(string); got != "" {
			t.Fatalf("refresh context RequestProxyURL = %q, want empty (credential/global proxy only)", got)
		}
	})

	t.Run("routes OAuth refresh through credential proxy rather than RequestProxyURL", func(t *testing.T) {
		var requestProxyHits atomic.Int32
		var authProxyHits atomic.Int32
		var globalProxyHits atomic.Int32
		requestProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requestProxyHits.Add(1)
			http.Error(w, "request proxy should not be used for OAuth refresh", http.StatusBadGateway)
		}))
		defer requestProxy.Close()
		authProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			authProxyHits.Add(1)
			http.Error(w, "auth proxy reached", http.StatusBadGateway)
		}))
		defer authProxy.Close()
		globalProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			globalProxyHits.Add(1)
			http.Error(w, "global proxy should not override auth proxy", http.StatusBadGateway)
		}))
		defer globalProxy.Close()

		ctx := cliproxyexecutor.WithRequestProxyURL(context.Background(), requestProxy.URL)
		exec := NewAntigravityExecutor(&config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: globalProxy.URL},
		})
		auth := &cliproxyauth.Auth{
			ID:       "ag-proxy-iso-7749-" + uuid.NewString(),
			Provider: "antigravity",
			ProxyURL: authProxy.URL,
			Metadata: map[string]any{
				"project_id":    "proj-proxy-iso-7749",
				"access_token":  "expiring",
				"refresh_token": "refresh-proxy-iso-7749-" + uuid.NewString(),
				"expired":       time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
			},
		}

		_, _, _ = exec.ensureAccessToken(ctx, auth)
		if requestProxyHits.Load() != 0 {
			t.Fatalf("OAuth refresh used RequestProxyURL %d times, want 0", requestProxyHits.Load())
		}
		if globalProxyHits.Load() != 0 {
			t.Fatalf("OAuth refresh used global proxy %d times, want 0", globalProxyHits.Load())
		}
		if authProxyHits.Load() == 0 {
			t.Fatal("OAuth refresh did not use auth.ProxyURL")
		}
	})
}

func TestIssue7749CreditsRefreshPreservesContextValuesAndExcludesRequestProxy(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	newAuth := func(prefix string) *cliproxyauth.Auth {
		return &cliproxyauth.Auth{
			ID:       prefix + "-" + uuid.NewString(),
			Provider: "antigravity",
			Metadata: map[string]any{
				"project_id":   "proj-credits-7749",
				"access_token": "warm-token-7749",
				"expired":      time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			},
		}
	}

	t.Run("local queue timeout>0", func(t *testing.T) {
		var (
			decoratorCalled atomic.Bool
			seenCustomVal   atomic.Value
			seenReqProxy    atomic.Value
		)
		baseRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			seenCustomVal.Store(fmt.Sprint(req.Context().Value(issue7749ContextKey{})))
			seenReqProxy.Store(cliproxyexecutor.RequestProxyURL(req.Context()))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"paidTier":{"id":"tier","availableCredits":[]}}`)),
			}, nil
		})
		ctx := context.WithValue(context.Background(), issue7749ContextKey{}, "credits-local-7749")
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(baseRT))
		ctx = cliproxyexecutor.WithRequestProxyURL(ctx, "http://request-proxy.example:8081")
		ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
			return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				decoratorCalled.Store(true)
				return next.RoundTrip(req)
			})
		})

		auth := newAuth("ag-credits-local-7749")
		exec := NewAntigravityExecutor(&config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		})
		exec.maybeRefreshAntigravityCreditsHint(ctx, auth, "warm-token-7749")

		value, ok := antigravityCreditsHintRefreshByID.Load(auth.ID)
		if !ok {
			t.Fatal("expected credits refresh task state")
		}
		waitForAntigravityCreditsRefresh(t, value.(*antigravityCreditsHintRefreshState))

		if !decoratorCalled.Load() {
			t.Fatal("queueAntigravityCreditsRefresh(timeout>0) dropped httptransport.Decorator")
		}
		if got, _ := seenCustomVal.Load().(string); got != "credits-local-7749" {
			t.Fatalf("credits context custom value = %q, want credits-local-7749", got)
		}
		if got, _ := seenReqProxy.Load().(string); got != "" {
			t.Fatalf("credits context RequestProxyURL = %q, want empty", got)
		}
	})

	t.Run("home kv branch", func(t *testing.T) {
		kv := newFakeAntigravityKVClient()
		useFakeAntigravityKVClient(t, kv, true, nil)

		done := make(chan struct{})
		var (
			decoratorCalled atomic.Bool
			seenCustomVal   atomic.Value
			seenReqProxy    atomic.Value
		)
		baseRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			seenCustomVal.Store(fmt.Sprint(req.Context().Value(issue7749ContextKey{})))
			seenReqProxy.Store(cliproxyexecutor.RequestProxyURL(req.Context()))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body: &closeSignalReadCloser{
					ReadCloser: io.NopCloser(strings.NewReader(`{"paidTier":{"id":"tier","availableCredits":[]}}`)),
					closed:     done,
				},
			}, nil
		})
		ctx := context.WithValue(context.Background(), issue7749ContextKey{}, "credits-home-7749")
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(baseRT))
		ctx = cliproxyexecutor.WithRequestProxyURL(ctx, "http://request-proxy.example:8081")
		ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
			return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				decoratorCalled.Store(true)
				return next.RoundTrip(req)
			})
		})

		auth := newAuth("ag-credits-home-7749")
		exec := NewAntigravityExecutor(&config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		})
		exec.maybeRefreshAntigravityCreditsHint(ctx, auth, "warm-token-7749")

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for home kv background credits refresh")
		}

		if !decoratorCalled.Load() {
			t.Fatal("maybeRefreshAntigravityCreditsHint(home kv) dropped httptransport.Decorator")
		}
		if got, _ := seenCustomVal.Load().(string); got != "credits-home-7749" {
			t.Fatalf("home kv credits context custom value = %q, want credits-home-7749", got)
		}
		if got, _ := seenReqProxy.Load().(string); got != "" {
			t.Fatalf("home kv credits context RequestProxyURL = %q, want empty", got)
		}
	})

	t.Run("post-refresh queue timeout==0 excludes RequestProxyURL", func(t *testing.T) {
		var (
			decoratorCalled atomic.Bool
			seenCustomVal   atomic.Value
			seenReqProxy    atomic.Value
		)
		baseRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			seenCustomVal.Store(fmt.Sprint(req.Context().Value(issue7749ContextKey{})))
			seenReqProxy.Store(cliproxyexecutor.RequestProxyURL(req.Context()))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"paidTier":{"id":"tier","availableCredits":[]}}`)),
			}, nil
		})
		ctx := context.WithValue(context.Background(), issue7749ContextKey{}, "credits-postrefresh-7749")
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(baseRT))
		ctx = cliproxyexecutor.WithRequestProxyURL(ctx, "http://request-proxy.example:8081")
		ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
			return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				decoratorCalled.Store(true)
				return next.RoundTrip(req)
			})
		})

		auth := newAuth("ag-credits-postrefresh-7749")
		exec := NewAntigravityExecutor(&config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		})
		exec.queueAntigravityCreditsRefresh(ctx, auth, "warm-token-7749", 0)

		value, ok := antigravityCreditsHintRefreshByID.Load(auth.ID)
		if !ok {
			t.Fatal("expected post-refresh credits task state")
		}
		waitForAntigravityCreditsRefresh(t, value.(*antigravityCreditsHintRefreshState))

		if !decoratorCalled.Load() {
			t.Fatal("queueAntigravityCreditsRefresh(timeout==0) did not invoke httptransport.Decorator")
		}
		if got, _ := seenCustomVal.Load().(string); got != "credits-postrefresh-7749" {
			t.Fatalf("post-refresh credits context custom value = %q, want credits-postrefresh-7749", got)
		}
		if got, _ := seenReqProxy.Load().(string); got != "" {
			t.Fatalf("post-refresh credits context RequestProxyURL = %q, want empty", got)
		}
	})
}

func TestIssue7749ClientCancelDuringRefreshStillPersistsRotatedToken(t *testing.T) {
	cases := []struct {
		name      string
		projectID string
	}{
		{name: "existing project_id via Execute", projectID: "proj-cancel-7749"},
		{name: "missing project_id via PrepareRequestAuth", projectID: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const (
				modelID        = "gemini-3.6-flash"
				rotatedAccess  = "access-rotated-after-cancel-7749"
				rotatedRefresh = "refresh-rotated-after-cancel-7749"
				discoveredProj = "proj-discovered-after-cancel-7749"
			)
			authID := "ag-cancel-7749-" + uuid.NewString()
			initialRefresh := "refresh-initial-cancel-7749-" + uuid.NewString()

			oauthStarted := make(chan struct{})
			releaseOAuth := make(chan struct{})
			var (
				oauthCalls      atomic.Int32
				upstreamCalls   atomic.Int32
				seenAuthHeaders sync.Mutex
				authHeaders     []string
			)

			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case req.URL.String() == "https://oauth2.googleapis.com/token":
					call := oauthCalls.Add(1)
					if call == 1 {
						close(oauthStarted)
						<-releaseOAuth
					}
					body := fmt.Sprintf(`{"access_token":%q,"refresh_token":%q,"expires_in":3600}`, rotatedAccess, rotatedRefresh)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(body)),
						Request:    req,
					}, nil
				case strings.Contains(req.URL.Path, "loadCodeAssist"):
					body := fmt.Sprintf(`{"cloudaicompanionProject":%q,"paidTier":{"id":"tier","availableCredits":[]}}`, discoveredProj)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(body)),
						Request:    req,
					}, nil
				case strings.Contains(req.URL.Path, "generateContent"):
					upstreamCalls.Add(1)
					seenAuthHeaders.Lock()
					authHeaders = append(authHeaders, req.Header.Get("Authorization"))
					seenAuthHeaders.Unlock()
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}}`)),
						Request:    req,
					}, nil
				default:
					return nil, fmt.Errorf("unexpected request: %s", req.URL.String())
				}
			})

			store := newIssue7749MemoryStore()
			manager := cliproxyauth.NewManager(store, nil, nil)
			manager.SetRoundTripperProvider(issue7749StaticRTProvider{rt: transport})
			exec := NewAntigravityExecutor(&config.Config{})
			manager.RegisterExecutor(exec)

			meta := map[string]any{
				"type":          "antigravity",
				"email":         "ag-cancel-7749@example.com",
				"access_token":  "expiring-cancel-7749",
				"refresh_token": initialRefresh,
				"expired":       time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339),
			}
			if tc.projectID != "" {
				meta["project_id"] = tc.projectID
			}
			auth := &cliproxyauth.Auth{
				ID:       authID,
				FileName: authID + ".json",
				Provider: "antigravity",
				Status:   cliproxyauth.StatusActive,
				Metadata: meta,
			}
			if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
				t.Fatalf("Register auth: %v", errReg)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: modelID}})
			t.Cleanup(func() {
				if value, ok := antigravityCreditsHintRefreshByID.Load(authID); ok {
					if state, okState := value.(*antigravityCreditsHintRefreshState); okState && state != nil {
						waitForAntigravityCreditsRefresh(t, state)
					}
				}
				registry.GetGlobalRegistry().UnregisterClient(authID)
			})

			payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}

			reqCtx, cancelReq := context.WithCancel(context.Background())
			errCh := make(chan error, 1)
			go func() {
				_, errExec := manager.Execute(reqCtx, []string{"antigravity"}, cliproxyexecutor.Request{
					Model:   modelID,
					Payload: payload,
				}, opts)
				errCh <- errExec
			}()

			select {
			case <-oauthStarted:
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for OAuth refresh to start")
			}

			cancelReq()
			close(releaseOAuth)

			var errCanceled error
			select {
			case errCanceled = <-errCh:
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for canceled Execute to return")
			}
			if !errors.Is(errCanceled, context.Canceled) {
				t.Fatalf("canceled Execute error = %v, want context.Canceled", errCanceled)
			}
			if got := upstreamCalls.Load(); got != 0 {
				t.Fatalf("upstream generateContent calls after cancel = %d, want 0", got)
			}

			gotAuth, ok := manager.GetByID(authID)
			if !ok || gotAuth == nil {
				t.Fatalf("manager missing auth %s after canceled refresh", authID)
			}
			if got := metaStringValue(gotAuth.Metadata, "access_token"); got != rotatedAccess {
				t.Fatalf("manager access_token after cancel = %q, want %q", got, rotatedAccess)
			}
			if got := metaStringValue(gotAuth.Metadata, "refresh_token"); got != rotatedRefresh {
				t.Fatalf("manager refresh_token after cancel = %q, want %q", got, rotatedRefresh)
			}
			savedAuth := store.get(authID)
			if savedAuth == nil {
				t.Fatal("store did not persist auth after canceled refresh")
			}
			if got := metaStringValue(savedAuth.Metadata, "refresh_token"); got != rotatedRefresh {
				t.Fatalf("store refresh_token after cancel = %q, want %q", got, rotatedRefresh)
			}

			// A follow-up request must reuse the persisted refreshed token with zero extra OAuth calls.
			if _, errNext := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
				Model:   modelID,
				Payload: payload,
			}, opts); errNext != nil {
				t.Fatalf("follow-up Execute error = %v", errNext)
			}
			if got := oauthCalls.Load(); got != 1 {
				t.Fatalf("total OAuth calls across canceled + follow-up request = %d, want 1", got)
			}
			seenAuthHeaders.Lock()
			headers := append([]string(nil), authHeaders...)
			seenAuthHeaders.Unlock()
			if len(headers) != 1 || headers[0] != "Bearer "+rotatedAccess {
				t.Fatalf("follow-up Authorization headers = %v, want [Bearer %s]", headers, rotatedAccess)
			}
		})
	}
}

func TestIssue7749ProjectDiscoveryFailureStillPersistsRotatedToken(t *testing.T) {
	cases := []struct {
		name           string
		failStatusCode int
		failBody       string
	}{
		{name: "loadCodeAssist HTTP 500", failStatusCode: http.StatusInternalServerError, failBody: `{"error":"boom"}`},
		{name: "loadCodeAssist empty project_id", failStatusCode: http.StatusOK, failBody: `{}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const (
				modelID        = "gemini-3.6-flash"
				rotatedAccess  = "access-rotated-projfail-7749"
				rotatedRefresh = "refresh-rotated-projfail-7749"
				recoveredProj  = "proj-recovered-7749"
			)
			authID := "ag-projfail-7749-" + uuid.NewString()
			initialRefresh := "refresh-initial-projfail-7749-" + uuid.NewString()

			var (
				oauthCalls       atomic.Int32
				allowDiscovery   atomic.Bool
				seenRefreshToken sync.Mutex
				refreshTokens    []string
			)

			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case req.URL.String() == "https://oauth2.googleapis.com/token":
					bodyBytes, _ := io.ReadAll(req.Body)
					seenRefreshToken.Lock()
					refreshTokens = append(refreshTokens, string(bodyBytes))
					seenRefreshToken.Unlock()
					call := oauthCalls.Add(1)
					if call > 1 {
						return &http.Response{
							StatusCode: http.StatusBadRequest,
							Header:     http.Header{"Content-Type": {"application/json"}},
							Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant"}`)),
							Request:    req,
						}, nil
					}
					body := fmt.Sprintf(`{"access_token":%q,"refresh_token":%q,"expires_in":3600}`, rotatedAccess, rotatedRefresh)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(body)),
						Request:    req,
					}, nil
				case strings.Contains(req.URL.Path, "onboardUser"):
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"done":true,"response":{}}`)),
						Request:    req,
					}, nil
				case strings.Contains(req.URL.Path, "loadCodeAssist"):
					if !allowDiscovery.Load() {
						return &http.Response{
							StatusCode: tc.failStatusCode,
							Header:     http.Header{"Content-Type": {"application/json"}},
							Body:       io.NopCloser(strings.NewReader(tc.failBody)),
							Request:    req,
						}, nil
					}
					body := fmt.Sprintf(`{"cloudaicompanionProject":%q,"paidTier":{"id":"tier","availableCredits":[]}}`, recoveredProj)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(body)),
						Request:    req,
					}, nil
				case strings.Contains(req.URL.Path, "generateContent"):
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"recovered"}]}}]}}`)),
						Request:    req,
					}, nil
				default:
					return nil, fmt.Errorf("unexpected request: %s", req.URL.String())
				}
			})

			store := newIssue7749MemoryStore()
			manager := cliproxyauth.NewManager(store, nil, nil)
			manager.SetRoundTripperProvider(issue7749StaticRTProvider{rt: transport})
			exec := NewAntigravityExecutor(&config.Config{})
			manager.RegisterExecutor(exec)

			auth := &cliproxyauth.Auth{
				ID:       authID,
				FileName: authID + ".json",
				Provider: "antigravity",
				Status:   cliproxyauth.StatusActive,
				Metadata: map[string]any{
					"type":          "antigravity",
					"email":         "ag-projfail-7749@example.com",
					"access_token":  "expiring-projfail-7749",
					"refresh_token": initialRefresh,
					"expired":       time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339),
				},
			}
			if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
				t.Fatalf("Register auth: %v", errReg)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: modelID}})
			t.Cleanup(func() {
				if value, ok := antigravityCreditsHintRefreshByID.Load(authID); ok {
					if state, okState := value.(*antigravityCreditsHintRefreshState); okState && state != nil {
						waitForAntigravityCreditsRefresh(t, state)
					}
				}
				registry.GetGlobalRegistry().UnregisterClient(authID)
			})

			payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}

			if _, errFirst := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
				Model:   modelID,
				Payload: payload,
			}, opts); errFirst == nil {
				t.Fatal("first Execute unexpectedly succeeded when project discovery failed")
			}

			gotAuth, ok := manager.GetByID(authID)
			if !ok || gotAuth == nil {
				t.Fatalf("manager missing auth %s after project discovery failure", authID)
			}
			if got := metaStringValue(gotAuth.Metadata, "access_token"); got != rotatedAccess {
				t.Fatalf("manager access_token after project discovery failure = %q, want %q", got, rotatedAccess)
			}
			if got := metaStringValue(gotAuth.Metadata, "refresh_token"); got != rotatedRefresh {
				t.Fatalf("manager refresh_token after project discovery failure = %q, want %q", got, rotatedRefresh)
			}
			savedAuth := store.get(authID)
			if savedAuth == nil {
				t.Fatal("store did not persist rotated token after project discovery failure")
			}
			if got := metaStringValue(savedAuth.Metadata, "refresh_token"); got != rotatedRefresh {
				t.Fatalf("store refresh_token after project discovery failure = %q, want %q", got, rotatedRefresh)
			}

			// Clear transient cooldown from the failed discovery attempt and verify recovery.
			clearIssue7749Cooldown(t, manager, gotAuth, modelID)

			allowDiscovery.Store(true)
			if _, errSecond := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
				Model:   modelID,
				Payload: payload,
			}, opts); errSecond != nil {
				t.Fatalf("recovery Execute error = %v", errSecond)
			}
			if got := oauthCalls.Load(); got != 1 {
				t.Fatalf("OAuth calls across failed discovery + recovery = %d, want 1", got)
			}
			recoveredAuth, _ := manager.GetByID(authID)
			if got := metaStringValue(recoveredAuth.Metadata, "project_id"); got != recoveredProj {
				t.Fatalf("recovered project_id = %q, want %q", got, recoveredProj)
			}
		})
	}
}

func TestIssue7749TokenCrossingSafetyWindowAfterPrepareStillPersistsAndUpdatesMarkResult(t *testing.T) {
	const (
		modelID         = "gemini-3.6-flash"
		staleAccess     = "stale-window-7749"
		refreshedAccess = "fresh-window-7749"
		rotatedRefresh  = "rotated-window-7749"
	)
	authID := "ag-window-7749-" + uuid.NewString()
	initialRefresh := "initial-window-7749-" + uuid.NewString()

	baseNow := time.Now().UTC().Truncate(time.Second)
	var clockMu sync.Mutex
	currentNow := baseNow
	origNow := antigravityNow
	antigravityNow = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return currentNow
	}
	t.Cleanup(func() {
		antigravityNow = origNow
	})

	var (
		oauthCalls atomic.Int32
		failNext   atomic.Bool
	)
	failNext.Store(true)

	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.String() == "https://oauth2.googleapis.com/token":
			oauthCalls.Add(1)
			body := fmt.Sprintf(`{"access_token":%q,"refresh_token":%q,"expires_in":3600}`, refreshedAccess, rotatedRefresh)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		case strings.Contains(req.URL.Path, "loadCodeAssist"):
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"paidTier":{"id":"tier","availableCredits":[]}}`)),
				Request:    req,
			}, nil
		case strings.Contains(req.URL.Path, "generateContent"):
			if failNext.Load() {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":{"code":500,"message":"upstream failure"}}`)),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}}`)),
				Request:    req,
			}, nil
		default:
			return nil, fmt.Errorf("unexpected request: %s", req.URL.String())
		}
	})

	store := newIssue7749MemoryStore()
	hook := &issue7749ResultHook{}
	manager := cliproxyauth.NewManager(store, nil, hook)
	manager.SetRoundTripperProvider(issue7749StaticRTProvider{rt: transport})
	exec := NewAntigravityExecutor(&config.Config{})
	manager.RegisterExecutor(exec)

	// Token starts 10s OUTSIDE the 5m safety window during PrepareRequestAuth.
	auth := &cliproxyauth.Auth{
		ID:       authID,
		FileName: authID + ".json",
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"type":          "antigravity",
			"email":         "ag-window-7749@example.com",
			"project_id":    "proj-window-7749",
			"access_token":  staleAccess,
			"refresh_token": initialRefresh,
			"expired":       baseNow.Add(antigravityRequestTokenSafetyWindow + 10*time.Second).Format(time.RFC3339),
		},
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register auth: %v", errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		if value, ok := antigravityCreditsHintRefreshByID.Load(authID); ok {
			if state, okState := value.(*antigravityCreditsHintRefreshState); okState && state != nil {
				waitForAntigravityCreditsRefresh(t, state)
			}
		}
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
		RequestAfterAuthInterceptor: func(_ context.Context, _ cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			// Advance clock across the 5m safety window AFTER PrepareRequestAuth and BEFORE Execute.
			clockMu.Lock()
			currentNow = baseNow.Add(20 * time.Second)
			clockMu.Unlock()
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
		},
	}

	if _, errFirst := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
		Model:   modelID,
		Payload: payload,
	}, opts); errFirst == nil {
		t.Fatal("first Execute unexpectedly succeeded on upstream 500")
	}

	results := hook.snapshot()
	if len(results) == 0 {
		t.Fatal("expected MarkResult to record failed attempt")
	}
	if got := results[0].AccessToken; got != refreshedAccess {
		t.Fatalf("failed Result.AccessToken after window crossing = %q, want %q", got, refreshedAccess)
	}

	gotAuth, ok := manager.GetByID(authID)
	if !ok || gotAuth == nil {
		t.Fatalf("manager missing auth %s", authID)
	}
	if got := metaStringValue(gotAuth.Metadata, "access_token"); got != refreshedAccess {
		t.Fatalf("manager access_token after window crossing = %q, want %q", got, refreshedAccess)
	}
	if got := metaStringValue(gotAuth.Metadata, "refresh_token"); got != rotatedRefresh {
		t.Fatalf("manager refresh_token after window crossing = %q, want %q", got, rotatedRefresh)
	}
	savedAuth := store.get(authID)
	if savedAuth == nil || metaStringValue(savedAuth.Metadata, "refresh_token") != rotatedRefresh {
		t.Fatalf("store did not persist rotated refresh_token %q after window crossing", rotatedRefresh)
	}

	// Second request must reuse the persisted refreshed token with zero additional OAuth calls.
	failNext.Store(false)
	clearIssue7749Cooldown(t, manager, gotAuth, modelID)
	if _, errSecond := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
		Model:   modelID,
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}); errSecond != nil {
		t.Fatalf("second Execute error = %v", errSecond)
	}
	if got := oauthCalls.Load(); got != 1 {
		t.Fatalf("OAuth calls across window-crossing + follow-up request = %d, want 1", got)
	}
}

func TestIssue7749UnsupportedAttachmentWithExpiringTokenFailsLocallyWithoutOAuth(t *testing.T) {
	const (
		geminiModelID = "gemini-3.7-flash"
		claudeModelID = "claude-sonnet-4-5"
		claudeFile    = `{"max_tokens":8,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"container_upload","file_id":"file_not_stored"}]}]}`
		openAIFile    = `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"file","file":{"file_id":"file-not-stored"}}]}]}`
	)
	authID := "ag-unsupported-7749-" + uuid.NewString()

	var httpCalls atomic.Int32
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		httpCalls.Add(1)
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":"oauth_must_not_be_called"}`)),
			Request:    req,
		}, nil
	})

	store := newIssue7749MemoryStore()
	manager := cliproxyauth.NewManager(store, nil, nil)
	manager.SetRoundTripperProvider(issue7749StaticRTProvider{rt: transport})
	exec := NewAntigravityExecutor(&config.Config{})
	manager.RegisterExecutor(exec)

	auth := &cliproxyauth.Auth{
		ID:       authID,
		FileName: authID + ".json",
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"type":          "antigravity",
			"email":         "ag-unsupported-7749@example.com",
			"project_id":    "proj-unsupported-7749",
			"access_token":  "expiring-unsupported-7749",
			"refresh_token": "refresh-unsupported-7749-" + uuid.NewString(),
			"expired":       time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339),
		},
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register auth: %v", errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{
		{ID: geminiModelID},
		{ID: claudeModelID},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	cases := []struct {
		name     string
		model    string
		source   string
		payload  string
		wantPart string
	}{
		{"claude source to gemini model", geminiModelID, "claude", claudeFile, "container_upload"},
		{"claude source to claude model", claudeModelID, "claude", claudeFile, "container_upload"},
		{"openai source to gemini model", geminiModelID, "openai", openAIFile, "file"},
	}

	for _, tc := range cases {
		req := cliproxyexecutor.Request{Model: tc.model, Payload: []byte(tc.payload)}
		opts := func(stream bool) cliproxyexecutor.Options {
			return cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(tc.source), Stream: stream}
		}
		calls := map[string]func() error{
			"Execute": func() error {
				_, err := manager.Execute(context.Background(), []string{"antigravity"}, req, opts(false))
				return err
			},
			"ExecuteStream": func() error {
				_, err := manager.ExecuteStream(context.Background(), []string{"antigravity"}, req, opts(true))
				return err
			},
			"CountTokens": func() error {
				_, err := manager.ExecuteCount(context.Background(), []string{"antigravity"}, req, opts(false))
				return err
			},
		}
		for method, call := range calls {
			err := call()
			var part *translatorcommon.UnsupportedPartError
			if !errors.As(err, &part) || part.Type != tc.wantPart {
				t.Errorf("%s %s: err = %v, want UnsupportedPartError(%s)", tc.name, method, err, tc.wantPart)
			}
		}
	}

	if got := httpCalls.Load(); got != 0 {
		t.Fatalf("HTTP/OAuth calls for locally-refused unsupported attachments = %d, want 0", got)
	}
}

func clearIssue7749Cooldown(t *testing.T, manager *cliproxyauth.Manager, auth *cliproxyauth.Auth, modelID string) {
	t.Helper()
	manager.MarkResult(context.Background(), cliproxyauth.Result{
		AuthID:            auth.ID,
		Provider:          auth.Provider,
		Model:             modelID,
		RouteModel:        modelID,
		Success:           true,
		CompletedResponse: true,
		CredentialVersion: auth.CredentialVersion,
		RegistrationEpoch: auth.RegistrationEpoch,
	})
}

func TestIssue7749ConcurrentRequestsRefreshOnceAndSharePersistedToken(t *testing.T) {
	const (
		modelID        = "gemini-3.6-flash"
		rotatedAccess  = "access-concurrent-7749"
		rotatedRefresh = "refresh-concurrent-7749"
	)
	authID := "ag-concurrent-7749-" + uuid.NewString()
	initialRefresh := "refresh-concurrent-init-7749-" + uuid.NewString()

	oauthStarted := make(chan struct{})
	releaseOAuth := make(chan struct{})
	var oauthCalls atomic.Int32

	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.String() == "https://oauth2.googleapis.com/token":
			if call := oauthCalls.Add(1); call == 1 {
				close(oauthStarted)
				<-releaseOAuth
				body := fmt.Sprintf(`{"access_token":%q,"refresh_token":%q,"expires_in":3600}`, rotatedAccess, rotatedRefresh)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant"}`)),
				Request:    req,
			}, nil
		case strings.Contains(req.URL.Path, "loadCodeAssist"):
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"paidTier":{"id":"tier","availableCredits":[]}}`)),
				Request:    req,
			}, nil
		case strings.Contains(req.URL.Path, "generateContent"):
			if got := req.Header.Get("Authorization"); got != "Bearer "+rotatedAccess {
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":"stale token"}`)),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}}`)),
				Request:    req,
			}, nil
		default:
			return nil, fmt.Errorf("unexpected request: %s", req.URL.String())
		}
	})

	store := newIssue7749MemoryStore()
	manager := cliproxyauth.NewManager(store, nil, nil)
	manager.SetRoundTripperProvider(issue7749StaticRTProvider{rt: transport})
	exec := NewAntigravityExecutor(&config.Config{})
	manager.RegisterExecutor(exec)

	auth := &cliproxyauth.Auth{
		ID:       authID,
		FileName: authID + ".json",
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"type":          "antigravity",
			"email":         "ag-concurrent-7749@example.com",
			"project_id":    "proj-concurrent-7749",
			"access_token":  "expiring-concurrent-7749",
			"refresh_token": initialRefresh,
			"expired":       time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339),
		},
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register auth: %v", errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		if value, ok := antigravityCreditsHintRefreshByID.Load(authID); ok {
			if state, okState := value.(*antigravityCreditsHintRefreshState); okState && state != nil {
				waitForAntigravityCreditsRefresh(t, state)
			}
		}
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}

	// Delay the second request inside RequestAfterAuthInterceptor until the first
	// request is already inside OAuth, then release both to exercise both the
	// lock-wait and post-commit re-read paths.
	secondEnteredInterceptor := make(chan struct{})
	releaseSecond := make(chan struct{})

	errCh := make(chan error, 2)
	go func() {
		_, errExec := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
			Model:   modelID,
			Payload: payload,
		}, opts)
		errCh <- errExec
	}()

	select {
	case <-oauthStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first request to enter OAuth")
	}

	go func() {
		secondOpts := opts
		secondOpts.RequestAfterAuthInterceptor = func(_ context.Context, _ cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			close(secondEnteredInterceptor)
			<-releaseSecond
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
		}
		_, errExec := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
			Model:   modelID,
			Payload: payload,
		}, secondOpts)
		errCh <- errExec
	}()

	select {
	case <-secondEnteredInterceptor:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for second request to capture stale pre-refresh auth snapshot")
	}

	// Let first request finish OAuth and commit, then release the second request
	// which still holds the pre-refresh auth clone.
	close(releaseOAuth)
	if errFirst := <-errCh; errFirst != nil {
		t.Fatalf("first concurrent Execute error = %v", errFirst)
	}
	close(releaseSecond)
	if errSecond := <-errCh; errSecond != nil {
		t.Fatalf("second concurrent Execute error = %v", errSecond)
	}
	if got := oauthCalls.Load(); got != 1 {
		t.Fatalf("concurrent OAuth calls = %d, want 1", got)
	}
	gotAuth, _ := manager.GetByID(authID)
	if got := metaStringValue(gotAuth.Metadata, "refresh_token"); got != rotatedRefresh {
		t.Fatalf("persisted refresh_token = %q, want %q", got, rotatedRefresh)
	}
}

func TestIssue7749PrepareHttpRequestPersistsRefreshedToken(t *testing.T) {
	const (
		rotatedAccess  = "access-prep-http-7749"
		rotatedRefresh = "refresh-prep-http-7749"
	)
	authID := "ag-prep-http-7749-" + uuid.NewString()
	initialRefresh := "refresh-prep-http-init-7749-" + uuid.NewString()

	var oauthCalls atomic.Int32
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.String() == "https://oauth2.googleapis.com/token":
			oauthCalls.Add(1)
			body := fmt.Sprintf(`{"access_token":%q,"refresh_token":%q,"expires_in":3600}`, rotatedAccess, rotatedRefresh)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		case strings.Contains(req.URL.Path, "loadCodeAssist"):
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"paidTier":{"id":"tier","availableCredits":[]}}`)),
				Request:    req,
			}, nil
		default:
			return nil, fmt.Errorf("unexpected request: %s", req.URL.String())
		}
	})

	store := newIssue7749MemoryStore()
	manager := cliproxyauth.NewManager(store, nil, nil)
	exec := NewAntigravityExecutor(&config.Config{})
	manager.RegisterExecutor(exec)

	auth := &cliproxyauth.Auth{
		ID:       authID,
		FileName: authID + ".json",
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"type":          "antigravity",
			"email":         "ag-prep-http-7749@example.com",
			"project_id":    "proj-prep-http-7749",
			"access_token":  "expiring-prep-http-7749",
			"refresh_token": initialRefresh,
			"expired":       time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339),
		},
	}
	registered, errReg := manager.Register(context.Background(), auth)
	if errReg != nil {
		t.Fatalf("Register auth: %v", errReg)
	}
	t.Cleanup(func() {
		if value, ok := antigravityCreditsHintRefreshByID.Load(authID); ok {
			if state, okState := value.(*antigravityCreditsHintRefreshState); okState && state != nil {
				waitForAntigravityCreditsRefresh(t, state)
			}
		}
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	req1, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/one", nil)
	if errPrep := manager.PrepareHttpRequest(ctx, registered.Clone(), req1); errPrep != nil {
		t.Fatalf("PrepareHttpRequest #1 error = %v", errPrep)
	}
	if got := req1.Header.Get("Authorization"); got != "Bearer "+rotatedAccess {
		t.Fatalf("req1 Authorization = %q, want Bearer %s", got, rotatedAccess)
	}

	latest, _ := manager.GetByID(authID)
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/two", nil)
	if errPrep := manager.PrepareHttpRequest(ctx, latest, req2); errPrep != nil {
		t.Fatalf("PrepareHttpRequest #2 error = %v", errPrep)
	}
	if got := req2.Header.Get("Authorization"); got != "Bearer "+rotatedAccess {
		t.Fatalf("req2 Authorization = %q, want Bearer %s", got, rotatedAccess)
	}
	if got := oauthCalls.Load(); got != 1 {
		t.Fatalf("PrepareHttpRequest OAuth calls = %d, want 1", got)
	}
	savedAuth := store.get(authID)
	if savedAuth == nil || metaStringValue(savedAuth.Metadata, "refresh_token") != rotatedRefresh {
		t.Fatalf("store did not persist rotated refresh_token %q from PrepareHttpRequest", rotatedRefresh)
	}
}

func TestIssue7749PrepareRequestAuthProjectDiscoveryExcludesRequestProxy(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	t.Run("strips RequestProxyURL on both OAuth refresh and project discovery while preserving decorator", func(t *testing.T) {
		var (
			oauthReqProxy      atomic.Value
			discoveryReqProxy  atomic.Value
			decoratorCallCount atomic.Int32
		)
		baseRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(req.URL.String(), "oauth2.googleapis.com/token"):
				oauthReqProxy.Store(cliproxyexecutor.RequestProxyURL(req.Context()))
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"access_token":"fresh-disc-access","refresh_token":"rotated-disc-refresh","expires_in":3600,"token_type":"Bearer"}`)),
				}, nil
			case strings.Contains(req.URL.String(), "loadCodeAssist"):
				discoveryReqProxy.Store(cliproxyexecutor.RequestProxyURL(req.Context()))
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"cloudaicompanionProject":"discovered-proj-ctx-7749","paidTier":{"id":"tier","availableCredits":[]}}`)),
				}, nil
			default:
				return nil, fmt.Errorf("unexpected url: %s", req.URL.String())
			}
		})

		ctx := cliproxyexecutor.WithRequestProxyURL(context.Background(), "http://127.0.0.1:19999")
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(baseRT))
		ctx = httptransport.WithDecorator(ctx, func(base http.RoundTripper) http.RoundTripper {
			return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				decoratorCallCount.Add(1)
				return base.RoundTrip(req)
			})
		})

		authID := "ag-proj-ctx-7749-" + uuid.NewString()
		auth := &cliproxyauth.Auth{
			ID:       authID,
			Provider: "antigravity",
			Metadata: map[string]any{
				"access_token":  "stale-disc-access",
				"refresh_token": "old-disc-refresh-" + uuid.NewString(),
				"expired":       time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339),
			},
		}
		t.Cleanup(func() {
			if value, ok := antigravityCreditsHintRefreshByID.Load(authID); ok {
				if state, okState := value.(*antigravityCreditsHintRefreshState); okState && state != nil {
					waitForAntigravityCreditsRefresh(t, state)
				}
			}
		})

		exec := NewAntigravityExecutor(&config.Config{})
		prepared, errPrep := exec.PrepareRequestAuth(ctx, auth)
		if errPrep != nil {
			t.Fatalf("PrepareRequestAuth() error = %v", errPrep)
		}
		if got := antigravityProjectIDFromAuth(prepared); got != "discovered-proj-ctx-7749" {
			t.Fatalf("project_id = %q, want discovered-proj-ctx-7749", got)
		}
		if got, _ := oauthReqProxy.Load().(string); got != "" {
			t.Fatalf("OAuth refresh RequestProxyURL = %q, want empty", got)
		}
		if got, _ := discoveryReqProxy.Load().(string); got != "" {
			t.Fatalf("project discovery RequestProxyURL = %q, want empty", got)
		}
		if decoratorCallCount.Load() < 2 {
			t.Fatalf("decorator called %d times, want >= 2 (OAuth + project discovery)", decoratorCallCount.Load())
		}
	})

	t.Run("routes project discovery through credential proxy rather than RequestProxyURL and honors caller cancel", func(t *testing.T) {
		var (
			requestProxyHits atomic.Int32
			authProxyHits    atomic.Int32
			globalProxyHits  atomic.Int32
		)
		requestProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requestProxyHits.Add(1)
			http.Error(w, "request proxy must not be used for project discovery", http.StatusBadGateway)
		}))
		defer requestProxy.Close()

		authProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			authProxyHits.Add(1)
			http.Error(w, "auth proxy reached", http.StatusBadGateway)
		}))
		defer authProxy.Close()

		globalProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			globalProxyHits.Add(1)
			http.Error(w, "global proxy should not override auth proxy", http.StatusBadGateway)
		}))
		defer globalProxy.Close()

		exec := NewAntigravityExecutor(&config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: globalProxy.URL},
		})
		authID := "ag-proj-proxy-7749-" + uuid.NewString()
		auth := &cliproxyauth.Auth{
			ID:       authID,
			Provider: "antigravity",
			ProxyURL: authProxy.URL,
			Metadata: map[string]any{
				"access_token": "valid-proj-proxy-access",
				"expired":      time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			},
		}
		t.Cleanup(func() {
			if value, ok := antigravityCreditsHintRefreshByID.Load(authID); ok {
				if state, okState := value.(*antigravityCreditsHintRefreshState); okState && state != nil {
					waitForAntigravityCreditsRefresh(t, state)
				}
			}
		})

		ctx := cliproxyexecutor.WithRequestProxyURL(context.Background(), requestProxy.URL)
		_, _ = exec.PrepareRequestAuth(ctx, auth)
		if requestProxyHits.Load() != 0 {
			t.Fatalf("project discovery used RequestProxyURL %d times, want 0", requestProxyHits.Load())
		}
		if globalProxyHits.Load() != 0 {
			t.Fatalf("project discovery used global proxy %d times, want 0", globalProxyHits.Load())
		}
		if authProxyHits.Load() == 0 {
			t.Fatal("project discovery did not use auth.ProxyURL")
		}

		// Caller cancellation must still abort project discovery.
		canceledCtx, cancel := context.WithCancel(cliproxyexecutor.WithRequestProxyURL(context.Background(), requestProxy.URL))
		cancel()
		if _, errCancel := exec.PrepareRequestAuth(canceledCtx, auth.Clone()); !errors.Is(errCancel, context.Canceled) {
			t.Fatalf("PrepareRequestAuth(canceledCtx) error = %v, want context.Canceled", errCancel)
		}
	})
}
