package executor

import (
	"context"
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
	modelCreditsActive = sync.Map{}
	modelCooldowns = sync.Map{}
}

func TestClassifyAntigravity429Rule(t *testing.T) {
	t.Run("rule 1: permanent quota exhausted with e.g.", func(t *testing.T) {
		body := []byte(`{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`)
		if got := classifyAntigravity429Rule(body); got != antigravity429Rule1Permanent {
			t.Fatalf("classifyAntigravity429Rule() = %d, want %d (Rule1Permanent)", got, antigravity429Rule1Permanent)
		}
	})

	t.Run("rule 2: quota exhausted with model metadata", func(t *testing.T) {
		body := []byte(`{
			"error": {
				"status": "RESOURCE_EXHAUSTED",
				"message": "You have exhausted your capacity on this model.",
				"details": [
					{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "QUOTA_EXHAUSTED", "metadata": {"model": "gemini-3.1-flash-image"}}
				]
			}
		}`)
		if got := classifyAntigravity429Rule(body); got != antigravity429Rule2QuotaModel {
			t.Fatalf("classifyAntigravity429Rule() = %d, want %d (Rule2QuotaModel)", got, antigravity429Rule2QuotaModel)
		}
	})

	t.Run("rule 2: quota exhausted without model metadata falls to none", func(t *testing.T) {
		body := []byte(`{
			"error": {
				"status": "RESOURCE_EXHAUSTED",
				"details": [
					{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "QUOTA_EXHAUSTED"}
				]
			}
		}`)
		if got := classifyAntigravity429Rule(body); got != antigravity429RuleNone {
			t.Fatalf("classifyAntigravity429Rule() = %d, want %d (RuleNone)", got, antigravity429RuleNone)
		}
	})

	t.Run("rule 3: rate limit exceeded with model metadata", func(t *testing.T) {
		body := []byte(`{
			"error": {
				"status": "RESOURCE_EXHAUSTED",
				"details": [
					{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "0.847655010s"},
					{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "RATE_LIMIT_EXCEEDED", "metadata": {"model": "claude-opus-4-6"}}
				]
			}
		}`)
		if got := classifyAntigravity429Rule(body); got != antigravity429Rule3RateLimit {
			t.Fatalf("classifyAntigravity429Rule() = %d, want %d (Rule3RateLimit)", got, antigravity429Rule3RateLimit)
		}
	})

	t.Run("unstructured 429 defaults to none", func(t *testing.T) {
		body := []byte(`{"error":{"message":"too many requests"}}`)
		if got := classifyAntigravity429Rule(body); got != antigravity429RuleNone {
			t.Fatalf("classifyAntigravity429Rule() = %d, want %d (RuleNone)", got, antigravity429RuleNone)
		}
	})

	t.Run("empty body defaults to none", func(t *testing.T) {
		if got := classifyAntigravity429Rule(nil); got != antigravity429RuleNone {
			t.Fatalf("classifyAntigravity429Rule(nil) = %d, want %d (RuleNone)", got, antigravity429RuleNone)
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

func TestModelCreditsActiveLifecycle(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	auth := &cliproxyauth.Auth{ID: "test-auth"}
	model := "gemini-2.5-flash"
	now := time.Now()

	if isModelCreditsActive(auth, model, now) {
		t.Fatal("isModelCreditsActive() = true before marking, want false")
	}

	markModelCreditsActive(auth, model, now)
	if !isModelCreditsActive(auth, model, now) {
		t.Fatal("isModelCreditsActive() = false after marking, want true")
	}

	expired := now.Add(creditsDuration + time.Minute)
	if isModelCreditsActive(auth, model, expired) {
		t.Fatal("isModelCreditsActive() = true after expiry, want false")
	}

	markModelCreditsActive(auth, model, now)
	clearModelCreditsActive(auth, model)
	if isModelCreditsActive(auth, model, now) {
		t.Fatal("isModelCreditsActive() = true after clearing, want false")
	}
}

func TestModelCooldownLifecycle(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	auth := &cliproxyauth.Auth{ID: "test-auth"}
	model := "gemini-2.5-flash"
	now := time.Now()

	if inCooldown, _ := isModelInCooldown(auth, model, now); inCooldown {
		t.Fatal("isModelInCooldown() = true before marking, want false")
	}

	markModelCooldown(auth, model, now)
	inCooldown, remaining := isModelInCooldown(auth, model, now)
	if !inCooldown {
		t.Fatal("isModelInCooldown() = false after marking, want true")
	}
	if remaining <= 0 || remaining > modelCooldownDuration {
		t.Fatalf("remaining = %v, want between 0 and %v", remaining, modelCooldownDuration)
	}

	expired := now.Add(modelCooldownDuration + time.Minute)
	if inCooldown, _ := isModelInCooldown(auth, model, expired); inCooldown {
		t.Fatal("isModelInCooldown() = true after expiry, want false")
	}
}

func TestAntigravityExecute_Rule1PermanentBan(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "auth-rule1",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
	if !auth.QuotaExhaustedPermanent {
		t.Fatal("auth.QuotaExhaustedPermanent = false, want true")
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
			_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"You have exhausted your capacity.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"model":"gemini-2.5-flash"}}]}}`))
			return
		}

		if !strings.Contains(string(body), `"enabledCreditTypes":["GOOGLE_ONE_AI"]`) {
			t.Fatalf("second request body missing enabledCreditTypes: %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`))
	}))
	defer server.Close()

	creditsEnabled := true
	exec := NewAntigravityExecutor(&config.Config{
		CreditsEnabled: &creditsEnabled,
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
			_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"model":"gemini-2.5-flash"}}]}}`))
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

	creditsEnabled := true
	exec := NewAntigravityExecutor(&config.Config{
		CreditsEnabled: &creditsEnabled,
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

func TestAntigravityExecute_DoesNotDirectInjectCreditsWhenFlagDisabled(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		requestBodies = append(requestBodies, string(body))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"You have exhausted your capacity.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"model":"gemini-2.5-flash"}}]}}`))
	}))
	defer server.Close()

	creditsDisabled := false
	exec := NewAntigravityExecutor(&config.Config{
		CreditsEnabled: &creditsDisabled,
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
	markModelCreditsActive(auth, "gemini-2.5-flash", time.Now())

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

func TestClearAntigravityAuthState(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	auth := &cliproxyauth.Auth{ID: "test-clear"}
	now := time.Now()

	markModelCreditsActive(auth, "model-a", now)
	markModelCooldown(auth, "model-b", now)

	ClearAntigravityAuthState("test-clear")

	if isModelCreditsActive(auth, "model-a", now) {
		t.Fatal("model-a credits still active after clear")
	}
	if inCooldown, _ := isModelInCooldown(auth, "model-b", now); inCooldown {
		t.Fatal("model-b still in cooldown after clear")
	}
}
