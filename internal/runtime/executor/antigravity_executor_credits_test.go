package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func resetAntigravityCreditsRetryState() {
	antigravityCreditsFailureByAuth = sync.Map{}
	antigravityPreferCreditsByModel = sync.Map{}
	antigravityShortCooldownByAuth = sync.Map{}
	antigravityModelPools = sync.Map{}
}

func TestClassifyAntigravity429(t *testing.T) {
	t.Run("quota exhausted", func(t *testing.T) {
		body := []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"QUOTA_EXHAUSTED"}}`)
		if got := classifyAntigravity429(body); got != antigravity429QuotaExhausted {
			t.Fatalf("classifyAntigravity429() = %q, want %q", got, antigravity429QuotaExhausted)
		}
	})

	t.Run("structured rate limit", func(t *testing.T) {
		body := []byte(`{
			"error": {
				"status": "RESOURCE_EXHAUSTED",
				"details": [
					{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "RATE_LIMIT_EXCEEDED"},
					{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "0.5s"}
				]
			}
		}`)
		if got := classifyAntigravity429(body); got != antigravity429RateLimited {
			t.Fatalf("classifyAntigravity429() = %q, want %q", got, antigravity429RateLimited)
		}
	})

	t.Run("structured quota exhausted", func(t *testing.T) {
		body := []byte(`{
			"error": {
				"status": "RESOURCE_EXHAUSTED",
				"details": [
					{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "QUOTA_EXHAUSTED"}
				]
			}
		}`)
		if got := classifyAntigravity429(body); got != antigravity429QuotaExhausted {
			t.Fatalf("classifyAntigravity429() = %q, want %q", got, antigravity429QuotaExhausted)
		}
	})

	t.Run("unstructured 429 defaults to soft rate limit", func(t *testing.T) {
		body := []byte(`{"error":{"message":"too many requests"}}`)
		if got := classifyAntigravity429(body); got != antigravity429SoftRateLimit {
			t.Fatalf("classifyAntigravity429() = %q, want %q", got, antigravity429SoftRateLimit)
		}
	})
}

func TestInjectEnabledCreditTypes(t *testing.T) {
	body := []byte(`{"model":"gemini-2.5-flash","request":{}}`)
	got := injectEnabledCreditTypes(body)
	if got == nil {
		t.Fatal("injectEnabledCreditTypes() returned nil")
	}
	if !strings.Contains(string(got), `"enabledCreditTypes":["GOOGLE_ONE_AI"]`) {
		t.Fatalf("injectEnabledCreditTypes() = %s, want enabledCreditTypes", string(got))
	}

	if got := injectEnabledCreditTypes([]byte(`not json`)); got != nil {
		t.Fatalf("injectEnabledCreditTypes() for invalid json = %s, want nil", string(got))
	}
}

func TestAntigravityExecute_RetriesTransient429ResourceExhausted(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		switch requestCount {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`))
		case 2:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`))
		default:
			t.Fatalf("unexpected request count %d", requestCount)
		}
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := &cliproxyauth.Auth{
		ID: "auth-transient-429",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("Execute() returned empty payload")
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want 2", requestCount)
	}
}

func TestAntigravityExecute_RetriesQuotaExhaustedWithCredits(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var (
		mu            sync.Mutex
		requestBodies []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()

		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		reqNum := len(requestBodies)
		mu.Unlock()

		if reqNum == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"QUOTA_EXHAUSTED"}}`))
			return
		}

		if !strings.Contains(string(body), `"enabledCreditTypes":["GOOGLE_ONE_AI"]`) {
			t.Fatalf("second request body missing enabledCreditTypes: %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	})
	auth := &cliproxyauth.Auth{
		ID: "auth-credits-ok",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("Execute() returned empty payload")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requestBodies) != 2 {
		t.Fatalf("request count = %d, want 2", len(requestBodies))
	}
}

func TestAntigravityExecute_SkipsCreditsRetryWhenAlreadyExhausted(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"QUOTA_EXHAUSTED"}}`))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	})
	auth := &cliproxyauth.Auth{
		ID: "auth-credits-exhausted",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}
	recordAntigravityCreditsFailure(auth, time.Now())

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want 429")
	}
	sErr, ok := err.(statusErr)
	if !ok {
		t.Fatalf("Execute() error type = %T, want statusErr", err)
	}
	if got := sErr.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("Execute() status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
	}
}

func TestAntigravityExecute_PrefersCreditsAfterSuccessfulFallback(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var (
		mu            sync.Mutex
		requestBodies []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()

		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		reqNum := len(requestBodies)
		mu.Unlock()

		switch reqNum {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"10s"}]}}`))
		case 2, 3:
			if !strings.Contains(string(body), `"enabledCreditTypes":["GOOGLE_ONE_AI"]`) {
				t.Fatalf("request %d body missing enabledCreditTypes: %s", reqNum, string(body))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`))
		default:
			t.Fatalf("unexpected request count %d", reqNum)
		}
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	})
	auth := &cliproxyauth.Auth{
		ID: "auth-prefer-credits",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}

	request := cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}

	if _, err := exec.Execute(context.Background(), auth, request, opts); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if _, err := exec.Execute(context.Background(), auth, request, opts); err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requestBodies) != 3 {
		t.Fatalf("request count = %d, want 3", len(requestBodies))
	}
	if strings.Contains(requestBodies[0], `"enabledCreditTypes":["GOOGLE_ONE_AI"]`) {
		t.Fatalf("first request unexpectedly used credits: %s", requestBodies[0])
	}
	if !strings.Contains(requestBodies[1], `"enabledCreditTypes":["GOOGLE_ONE_AI"]`) {
		t.Fatalf("fallback request missing credits: %s", requestBodies[1])
	}
	if !strings.Contains(requestBodies[2], `"enabledCreditTypes":["GOOGLE_ONE_AI"]`) {
		t.Fatalf("preferred request missing credits: %s", requestBodies[2])
	}
}

func TestAntigravityExecute_PreservesBaseURLFallbackAfterCreditsRetryFailure(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var (
		mu          sync.Mutex
		firstCount  int
		secondCount int
	)

	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()

		mu.Lock()
		firstCount++
		reqNum := firstCount
		mu.Unlock()

		switch reqNum {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`))
		case 2:
			if !strings.Contains(string(body), `"enabledCreditTypes":["GOOGLE_ONE_AI"]`) {
				t.Fatalf("credits retry missing enabledCreditTypes: %s", string(body))
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"permission denied"}}`))
		default:
			t.Fatalf("unexpected first server request count %d", reqNum)
		}
	}))
	defer firstServer.Close()

	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		secondCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`))
	}))
	defer secondServer.Close()

	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	})
	auth := &cliproxyauth.Auth{
		ID: "auth-baseurl-fallback",
		Attributes: map[string]string{
			"base_url": firstServer.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}

	originalOrder := antigravityBaseURLFallbackOrder
	defer func() { antigravityBaseURLFallbackOrder = originalOrder }()
	antigravityBaseURLFallbackOrder = func(auth *cliproxyauth.Auth) []string {
		return []string{firstServer.URL, secondServer.URL}
	}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("Execute() returned empty payload")
	}
	if firstCount != 2 {
		t.Fatalf("first server request count = %d, want 2", firstCount)
	}
	if secondCount != 1 {
		t.Fatalf("second server request count = %d, want 1", secondCount)
	}
}

func TestAntigravityExecute_DoesNotDirectInjectCreditsWhenFlagDisabled(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		requestBodies = append(requestBodies, string(body))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"QUOTA_EXHAUSTED"}}`))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: false},
	})
	auth := &cliproxyauth.Auth{
		ID: "auth-flag-disabled",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}
	markAntigravityPreferCredits(auth, "gemini-2.5-flash", time.Now(), nil, time.Time{})

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want 429")
	}
	if len(requestBodies) != 1 {
		t.Fatalf("request count = %d, want 1", len(requestBodies))
	}
	if strings.Contains(requestBodies[0], `"enabledCreditTypes":["GOOGLE_ONE_AI"]`) {
		t.Fatalf("request unexpectedly used enabledCreditTypes with flag disabled: %s", requestBodies[0])
	}
}

// --- Pool tracking unit tests ---

func TestAntigravityModelQuotaPool_ShouldActivateCredits(t *testing.T) {
	t.Run("threshold=0 always activates", func(t *testing.T) {
		pool := &antigravityModelQuotaPool{
			seen:      make(map[string]time.Time),
			exhausted: make(map[string]time.Time),
		}
		now := time.Now()
		pool.seen["a1"] = now
		if !pool.shouldActivateCredits(0, 3, now) {
			t.Fatal("threshold=0 should always return true")
		}
	})

	t.Run("insufficient sample returns false", func(t *testing.T) {
		pool := &antigravityModelQuotaPool{
			seen:      make(map[string]time.Time),
			exhausted: make(map[string]time.Time),
		}
		now := time.Now()
		pool.seen["a1"] = now
		pool.exhausted["a1"] = now.Add(1 * time.Hour)
		pool.seen["a2"] = now
		// a2 is NOT exhausted → not all exhausted, so small pool exception doesn't apply
		// 2 seen, minSample=3 → false
		if pool.shouldActivateCredits(0.8, 3, now) {
			t.Fatal("should return false when sample < minSampleSize")
		}
	})

	t.Run("small pool all exhausted activates credits", func(t *testing.T) {
		pool := &antigravityModelQuotaPool{
			seen:      make(map[string]time.Time),
			exhausted: make(map[string]time.Time),
		}
		now := time.Now()
		pool.seen["a1"] = now
		pool.exhausted["a1"] = now.Add(1 * time.Hour)
		pool.seen["a2"] = now
		pool.exhausted["a2"] = now.Add(1 * time.Hour)
		// 2 seen, 2 exhausted (all exhausted) → small pool exception → true
		if !pool.shouldActivateCredits(0.8, 3, now) {
			t.Fatal("should return true when all auths are exhausted (small pool exception)")
		}
	})

	t.Run("below threshold returns false", func(t *testing.T) {
		pool := &antigravityModelQuotaPool{
			seen:      make(map[string]time.Time),
			exhausted: make(map[string]time.Time),
		}
		now := time.Now()
		for i := 0; i < 10; i++ {
			id := "auth-" + strings.Repeat("x", i+1)
			pool.seen[id] = now
		}
		// Only 5 out of 10 exhausted = 50% < 80%
		for i := 0; i < 5; i++ {
			id := "auth-" + strings.Repeat("x", i+1)
			pool.exhausted[id] = now.Add(1 * time.Hour)
		}
		if pool.shouldActivateCredits(0.8, 3, now) {
			t.Fatal("50% exhausted should not activate at 80% threshold")
		}
	})

	t.Run("at threshold returns true", func(t *testing.T) {
		pool := &antigravityModelQuotaPool{
			seen:      make(map[string]time.Time),
			exhausted: make(map[string]time.Time),
		}
		now := time.Now()
		for i := 0; i < 10; i++ {
			id := "auth-" + strings.Repeat("x", i+1)
			pool.seen[id] = now
		}
		// 8 out of 10 exhausted = 80% >= 80%
		for i := 0; i < 8; i++ {
			id := "auth-" + strings.Repeat("x", i+1)
			pool.exhausted[id] = now.Add(1 * time.Hour)
		}
		if !pool.shouldActivateCredits(0.8, 3, now) {
			t.Fatal("80% exhausted should activate at 80% threshold")
		}
	})

	t.Run("expired exhausted entries are not counted", func(t *testing.T) {
		pool := &antigravityModelQuotaPool{
			seen:      make(map[string]time.Time),
			exhausted: make(map[string]time.Time),
		}
		now := time.Now()
		for i := 0; i < 5; i++ {
			id := "auth-" + strings.Repeat("x", i+1)
			pool.seen[id] = now
			// Reset time is in the past
			pool.exhausted[id] = now.Add(-1 * time.Minute)
		}
		if pool.shouldActivateCredits(0.8, 3, now) {
			t.Fatal("expired exhausted entries should not count")
		}
	})

	t.Run("stale seen entries are not counted", func(t *testing.T) {
		pool := &antigravityModelQuotaPool{
			seen:      make(map[string]time.Time),
			exhausted: make(map[string]time.Time),
		}
		now := time.Now()
		// All entries are stale (>12h)
		for i := 0; i < 5; i++ {
			id := "auth-" + strings.Repeat("x", i+1)
			pool.seen[id] = now.Add(-13 * time.Hour)
			pool.exhausted[id] = now.Add(1 * time.Hour)
		}
		if pool.shouldActivateCredits(0.8, 3, now) {
			t.Fatal("stale seen entries should not count")
		}
	})
}

func TestParseQuotaResetTimestamp(t *testing.T) {
	t.Run("valid timestamp", func(t *testing.T) {
		body := []byte(`{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"quotaResetTimeStamp":"2026-04-15T06:32:14Z"}}]}}`)
		ts, ok := parseQuotaResetTimestamp(body)
		if !ok {
			t.Fatal("parseQuotaResetTimestamp() returned false, want true")
		}
		expected, _ := time.Parse(time.RFC3339, "2026-04-15T06:32:14Z")
		if !ts.Equal(expected) {
			t.Fatalf("parseQuotaResetTimestamp() = %v, want %v", ts, expected)
		}
	})

	t.Run("missing metadata", func(t *testing.T) {
		body := []byte(`{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo"}]}}`)
		_, ok := parseQuotaResetTimestamp(body)
		if ok {
			t.Fatal("parseQuotaResetTimestamp() returned true for missing metadata")
		}
	})

	t.Run("missing details", func(t *testing.T) {
		body := []byte(`{"error":{"message":"quota exhausted"}}`)
		_, ok := parseQuotaResetTimestamp(body)
		if ok {
			t.Fatal("parseQuotaResetTimestamp() returned true for missing details")
		}
	})

	t.Run("malformed timestamp", func(t *testing.T) {
		body := []byte(`{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"quotaResetTimeStamp":"not-a-date"}}]}}`)
		_, ok := parseQuotaResetTimestamp(body)
		if ok {
			t.Fatal("parseQuotaResetTimestamp() returned true for malformed timestamp")
		}
	})
}

func TestAntigravityAuthHasCredits(t *testing.T) {
	t.Run("sufficient balance", func(t *testing.T) {
		auth := &cliproxyauth.Auth{
			Metadata: map[string]any{
				"credit_amount":     "25000",
				"min_credit_amount": "50",
			},
		}
		if !antigravityAuthHasCredits(auth) {
			t.Fatal("antigravityAuthHasCredits() = false, want true")
		}
	})

	t.Run("insufficient balance", func(t *testing.T) {
		auth := &cliproxyauth.Auth{
			Metadata: map[string]any{
				"credit_amount":     "30",
				"min_credit_amount": "50",
			},
		}
		if antigravityAuthHasCredits(auth) {
			t.Fatal("antigravityAuthHasCredits() = true, want false")
		}
	})

	t.Run("no credits fields is backward compatible", func(t *testing.T) {
		auth := &cliproxyauth.Auth{
			Metadata: map[string]any{
				"access_token": "token",
			},
		}
		if !antigravityAuthHasCredits(auth) {
			t.Fatal("antigravityAuthHasCredits() = false with no credits fields, want true (backward compat)")
		}
	})

	t.Run("nil auth returns true", func(t *testing.T) {
		if !antigravityAuthHasCredits(nil) {
			t.Fatal("antigravityAuthHasCredits(nil) = false, want true")
		}
	})

	t.Run("nil metadata returns true", func(t *testing.T) {
		auth := &cliproxyauth.Auth{}
		if !antigravityAuthHasCredits(auth) {
			t.Fatal("antigravityAuthHasCredits(nil metadata) = false, want true")
		}
	})
}

func TestAntigravityQuotaResetTime(t *testing.T) {
	now := time.Now()

	t.Run("prefers quotaResetTimeStamp", func(t *testing.T) {
		expected, _ := time.Parse(time.RFC3339, "2026-04-15T06:32:14Z")
		body := []byte(`{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"quotaResetTimeStamp":"2026-04-15T06:32:14Z"}}]}}`)
		d := 10 * time.Minute
		got := antigravityQuotaResetTime(body, &d, now)
		if !got.Equal(expected) {
			t.Fatalf("antigravityQuotaResetTime() = %v, want %v", got, expected)
		}
	})

	t.Run("falls back to retryAfter", func(t *testing.T) {
		body := []byte(`{"error":{"message":"quota exhausted"}}`)
		d := 30 * time.Minute
		got := antigravityQuotaResetTime(body, &d, now)
		expected := now.Add(30 * time.Minute)
		if got.Sub(expected) > time.Second {
			t.Fatalf("antigravityQuotaResetTime() = %v, want ~%v", got, expected)
		}
	})

	t.Run("falls back to 5h default", func(t *testing.T) {
		body := []byte(`{"error":{"message":"quota exhausted"}}`)
		got := antigravityQuotaResetTime(body, nil, now)
		expected := now.Add(5 * time.Hour)
		if got.Sub(expected) > time.Second {
			t.Fatalf("antigravityQuotaResetTime() = %v, want ~%v", got, expected)
		}
	})
}

func TestAntigravityExecute_RetriesQuotaExhaustedWithCredits_ThresholdZeroBackwardCompat(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var (
		mu            sync.Mutex
		requestBodies []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()

		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		reqNum := len(requestBodies)
		mu.Unlock()

		if reqNum == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"QUOTA_EXHAUSTED"}}`))
			return
		}

		if !strings.Contains(string(body), `"enabledCreditTypes":["GOOGLE_ONE_AI"]`) {
			t.Fatalf("second request body missing enabledCreditTypes: %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`))
	}))
	defer server.Close()

	// threshold=0 (default) → backward compatible: credits on first QUOTA_EXHAUSTED
	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{
			AntigravityCredits:          true,
			AntigravityCreditsThreshold: 0,
		},
	})
	auth := &cliproxyauth.Auth{
		ID: "auth-threshold-zero",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("Execute() returned empty payload")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requestBodies) != 2 {
		t.Fatalf("request count = %d, want 2", len(requestBodies))
	}
}

func TestAntigravityExecute_PoolGatesCreditsActivation(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"QUOTA_EXHAUSTED"}}`))
	}))
	defer server.Close()

	// threshold=0.8, MaxRetryCredentials=8 (minSampleSize)
	// Pre-populate pool with non-exhausted auths so small pool exception doesn't apply.
	// 1 exhausted / 4 seen = 25% < 80% and 4 < minSample(8) → credits should NOT activate.
	baseModel := "gemini-2.5-flash"
	pool := getAntigravityModelPool(baseModel)
	now := time.Now()
	for i := range 3 {
		pool.markSeen(fmt.Sprintf("auth-healthy-%d", i), now)
	}

	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{
			AntigravityCredits:          true,
			AntigravityCreditsThreshold: 0.8,
		},
		MaxRetryCredentials: 8,
	})
	auth := &cliproxyauth.Auth{
		ID: "auth-pool-gated",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token":      "token",
			"project_id":        "project-1",
			"expired":           time.Now().Add(1 * time.Hour).Format(time.RFC3339),
			"credit_amount":     "25000",
			"min_credit_amount": "50",
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want 429 (credits should not activate)")
	}
	// Only 1 request (no credits fallback attempt)
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1 (no credits fallback due to pool gating)", requestCount)
	}
}

func TestAntigravityExecute_PoolActivatesCreditsWhenThresholdMet(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var (
		mu            sync.Mutex
		requestBodies []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()

		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		reqNum := len(requestBodies)
		mu.Unlock()

		if reqNum == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"QUOTA_EXHAUSTED"}}`))
			return
		}

		// Second request should have credits injected
		if !strings.Contains(string(body), `"enabledCreditTypes":["GOOGLE_ONE_AI"]`) {
			t.Fatalf("second request body missing enabledCreditTypes: %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`))
	}))
	defer server.Close()

	// threshold=0.8, minSampleSize=5
	// Pre-populate pool so 4/5 auths are exhausted (80% >= 80%)
	baseModel := "gemini-2.5-flash"
	pool := getAntigravityModelPool(baseModel)
	now := time.Now()
	for i := range 5 {
		id := fmt.Sprintf("auth-pool-%d", i)
		pool.markSeen(id, now)
		if i < 4 {
			pool.markExhausted(id, now.Add(1*time.Hour))
		}
	}

	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{
			AntigravityCredits:          true,
			AntigravityCreditsThreshold: 0.8,
		},
		MaxRetryCredentials: 5,
	})
	auth := &cliproxyauth.Auth{
		ID: "auth-pool-4", // This auth is already in the pool as seen but not exhausted
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token":      "token",
			"project_id":        "project-1",
			"expired":           time.Now().Add(1 * time.Hour).Format(time.RFC3339),
			"credit_amount":     "25000",
			"min_credit_amount": "50",
		},
	}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   baseModel,
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("Execute() returned empty payload")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requestBodies) != 2 {
		t.Fatalf("request count = %d, want 2 (credits fallback should activate when pool threshold met)", len(requestBodies))
	}
}

func TestAntigravityExecute_AuthWithoutCreditsSkipsFallback(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"QUOTA_EXHAUSTED"}}`))
	}))
	defer server.Close()

	// threshold=0 → would normally activate credits immediately
	// But auth has insufficient credits balance
	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{
			AntigravityCredits:          true,
			AntigravityCreditsThreshold: 0,
		},
	})
	auth := &cliproxyauth.Auth{
		ID: "auth-no-credits",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token":      "token",
			"project_id":        "project-1",
			"expired":           time.Now().Add(1 * time.Hour).Format(time.RFC3339),
			"credit_amount":     "30",
			"min_credit_amount": "50",
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want 429 (credits should be skipped due to low balance)")
	}
	// Only 1 request (no credits fallback because auth has insufficient credits)
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1 (no credits fallback for low balance auth)", requestCount)
	}
}

func TestParseMetaFloat(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		wantVal float64
		wantOK  bool
	}{
		{"string", "25000", 25000, true},
		{"float64", float64(100), 100, true},
		{"int", int(50), 50, true},
		{"int64", int64(75), 75, true},
		{"empty string", "", 0, false},
		{"invalid string", "abc", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := map[string]any{"key": tt.value}
			got, ok := parseMetaFloat(meta, "key")
			if ok != tt.wantOK {
				t.Fatalf("parseMetaFloat() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.wantVal {
				t.Fatalf("parseMetaFloat() = %f, want %f", got, tt.wantVal)
			}
		})
	}
}
