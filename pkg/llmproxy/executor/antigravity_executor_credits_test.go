package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	homekv "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/home"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/runtime/executor/helps"
	cliproxyauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/kooshapari/CLIProxyAPI/v7/sdk/translator"
)

func resetAntigravityCreditsRetryState() {
	antigravityCreditsFailureByAuth = sync.Map{}
	antigravityShortCooldownByAuth = sync.Map{}
	antigravityCreditsBalanceByAuth = sync.Map{}
	antigravityCreditsHintRefreshByID = sync.Map{}
}

type fakeAntigravityKVClient struct {
	values       map[string][]byte
	getErr       error
	setErr       error
	setNXErr     error
	delErr       error
	setNXResult  bool
	getCount     int
	setCount     int
	setNXCount   int
	delCount     int
	lastSetTTL   time.Duration
	lastSetNXTTL time.Duration
	lastSetNXKey string
	lastSetKey   string
}

func newFakeAntigravityKVClient() *fakeAntigravityKVClient {
	return &fakeAntigravityKVClient{
		values:      make(map[string][]byte),
		setNXResult: true,
	}
}

func (c *fakeAntigravityKVClient) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	c.getCount++
	if c.getErr != nil {
		return nil, false, c.getErr
	}
	value, ok := c.values[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), value...), true, nil
}

func (c *fakeAntigravityKVClient) KVSet(_ context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error) {
	c.setCount++
	c.lastSetKey = key
	c.lastSetTTL = opts.EX
	if c.setErr != nil {
		return false, c.setErr
	}
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *fakeAntigravityKVClient) KVSetNX(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	c.setNXCount++
	c.lastSetNXKey = key
	c.lastSetNXTTL = ttl
	if c.setNXErr != nil {
		return false, c.setNXErr
	}
	if _, ok := c.values[key]; ok {
		return false, nil
	}
	if c.setNXResult {
		c.values[key] = append([]byte(nil), value...)
		return true, nil
	}
	return false, nil
}

func (c *fakeAntigravityKVClient) KVDel(_ context.Context, keys ...string) (int64, error) {
	c.delCount++
	if c.delErr != nil {
		return 0, c.delErr
	}
	var deleted int64
	for _, key := range keys {
		if _, ok := c.values[key]; ok {
			delete(c.values, key)
			deleted++
		}
	}
	return deleted, nil
}

func useFakeAntigravityKVClient(t *testing.T, client *fakeAntigravityKVClient, homeMode bool, errClient error) {
	t.Helper()
	previous := currentAntigravityKVClient
	currentAntigravityKVClient = func() (antigravityKVClient, bool, error) {
		return client, homeMode, errClient
	}
	t.Cleanup(func() {
		currentAntigravityKVClient = previous
	})
}

func mustAntigravityJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatalf("marshal value: %v", errMarshal)
	}
	return raw
}

func TestClassifyAntigravity429(t *testing.T) {
	t.Run("quota exhausted", func(t *testing.T) {
		body := []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"QUOTA_EXHAUSTED"}}`)
		if got := classifyAntigravity429(body); got != antigravity429QuotaExhausted {
			t.Fatalf("classifyAntigravity429() = %q, want %q", got, antigravity429QuotaExhausted)
		}
	})

	t.Run("standard antigravity rate limit with ui message stays rate limited", func(t *testing.T) {
		body := []byte(`{
			"error": {
				"code": 429,
				"message": "You have exhausted your capacity on this model. Your quota will reset after 0s.",
				"status": "RESOURCE_EXHAUSTED",
				"details": [
					{
						"@type": "type.googleapis.com/google.rpc.ErrorInfo",
						"reason": "RATE_LIMIT_EXCEEDED",
						"domain": "cloudcode-pa.googleapis.com",
						"metadata": {
							"model": "claude-opus-4-6-thinking",
							"quotaResetDelay": "479.417207ms",
							"quotaResetTimeStamp": "2026-04-20T09:19:49Z",
							"uiMessage": "true"
						}
					},
					{
						"@type": "type.googleapis.com/google.rpc.RetryInfo",
						"retryDelay": "0.479417207s"
					}
				]
			}
		}`)
		if got := classifyAntigravity429(body); got != antigravity429RateLimited {
			t.Fatalf("classifyAntigravity429() = %q, want %q", got, antigravity429RateLimited)
		}
		decision := decideAntigravity429(body)
		if decision.kind != antigravity429DecisionInstantRetrySameAuth {
			t.Fatalf("decideAntigravity429().kind = %q, want %q", decision.kind, antigravity429DecisionInstantRetrySameAuth)
		}
		if decision.retryAfter == nil {
			t.Fatal("decideAntigravity429().retryAfter = nil")
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

func TestAntigravityShouldRetryNoCapacity_Standard503(t *testing.T) {
	body := []byte(`{
		"error": {
			"code": 503,
			"message": "No capacity available for model gemini-3.1-flash-image on the server",
			"status": "UNAVAILABLE",
			"details": [
				{
					"@type": "type.googleapis.com/google.rpc.ErrorInfo",
					"reason": "MODEL_CAPACITY_EXHAUSTED",
					"domain": "cloudcode-pa.googleapis.com",
					"metadata": {
						"model": "gemini-3.1-flash-image"
					}
				}
			]
		}
	}`)
	if !antigravityShouldRetryNoCapacity(http.StatusServiceUnavailable, body) {
		t.Fatal("antigravityShouldRetryNoCapacity() = false, want true")
	}
}

func TestInjectEnabledCreditTypes(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","request":{}}`)
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

func TestParseRetryDelay_HumanReadableDuration(t *testing.T) {
	body := []byte(`{"error":{"message":"You have exhausted your capacity on this model. Your quota will reset after 1h43m56s."}}`)
	retryAfter, err := helps.ParseRetryDelay(body)
	if err != nil {
		t.Fatalf("helps.ParseRetryDelay() error = %v", err)
	}
	if retryAfter == nil {
		t.Fatal("helps.ParseRetryDelay() returned nil")
	}
	want := time.Hour + 43*time.Minute + 56*time.Second
	if *retryAfter != want {
		t.Fatalf("helps.ParseRetryDelay() = %v, want %v", *retryAfter, want)
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
		Model:   "claude-sonnet-4-6",
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

func TestAntigravityExecute_CreditsInjectedWhenConductorRequests(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if r.URL.Path == "/v1internal:loadCodeAssist" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"paidTier":{"id":"tier-1","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"25000","minimumCreditAmountForUsage":"50"}]}}`))
			return
		}
		requestBodies = append(requestBodies, string(body))

		if !strings.Contains(string(body), `"enabledCreditTypes":["GOOGLE_ONE_AI"]`) {
			t.Fatalf("request body missing enabledCreditTypes: %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	})
	auth := &cliproxyauth.Auth{
		ID: "auth-credits-conductor",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}

	// Simulate conductor setting credits requested flag in context
	ctx := cliproxyauth.WithAntigravityCredits(context.Background())

	resp, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
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
	if len(requestBodies) != 1 {
		t.Fatalf("request count = %d, want 1", len(requestBodies))
	}
}

func TestAntigravityExecute_NoCreditsWithoutConductorFlag(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if r.URL.Path == "/v1internal:loadCodeAssist" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"paidTier":{"id":"tier-1","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"25000","minimumCreditAmountForUsage":"50"}]}}`))
			return
		}
		requestBodies = append(requestBodies, string(body))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"QUOTA_EXHAUSTED"}}`))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	})
	auth := &cliproxyauth.Auth{
		ID: "auth-no-conductor-flag",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}

	// No conductor credits flag set in context
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
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
	// Should NOT contain credits since conductor didn't request them
	if strings.Contains(requestBodies[0], `"enabledCreditTypes"`) {
		t.Fatalf("request should not contain enabledCreditTypes without conductor flag: %s", requestBodies[0])
	}
}

func TestAntigravityAuthHasCredits(t *testing.T) {
	t.Run("sufficient balance", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		auth := &cliproxyauth.Auth{ID: "test-sufficient"}
		antigravityCreditsBalanceByAuth.Store("test-sufficient", antigravityCreditsBalance{
			CreditAmount:    25000,
			MinCreditAmount: 50,
			Known:           true,
		})
		if !antigravityAuthHasCredits(auth) {
			t.Fatal("antigravityAuthHasCredits() = false, want true")
		}
	})

	t.Run("insufficient balance", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		auth := &cliproxyauth.Auth{ID: "test-insufficient"}
		antigravityCreditsBalanceByAuth.Store("test-insufficient", antigravityCreditsBalance{
			CreditAmount:    30,
			MinCreditAmount: 50,
			Known:           true,
		})
		if antigravityAuthHasCredits(auth) {
			t.Fatal("antigravityAuthHasCredits() = true, want false")
		}
	})

	t.Run("no balance stored returns true (optimistic)", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		auth := &cliproxyauth.Auth{ID: "test-no-balance"}
		if !antigravityAuthHasCredits(auth) {
			t.Fatal("antigravityAuthHasCredits() = false with no balance stored, want true (optimistic default)")
		}
	})

	t.Run("nil auth returns false", func(t *testing.T) {
		if antigravityAuthHasCredits(nil) {
			t.Fatal("antigravityAuthHasCredits(nil) = true, want false")
		}
	})

	t.Run("empty ID returns false", func(t *testing.T) {
		auth := &cliproxyauth.Auth{}
		if antigravityAuthHasCredits(auth) {
			t.Fatal("antigravityAuthHasCredits(empty ID) = true, want false")
		}
	})

	t.Run("unknown balance returns false", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		auth := &cliproxyauth.Auth{ID: "test-unknown"}
		antigravityCreditsBalanceByAuth.Store("test-unknown", antigravityCreditsBalance{
			Known: false,
		})
		if antigravityAuthHasCredits(auth) {
			t.Fatal("antigravityAuthHasCredits() = true for unknown balance, want false")
		}
	})
}

func TestAntigravityAuthHasCreditsRequiredHomeBalanceUsesKV(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)
	const authID = "home-balance-auth"
	client := newFakeAntigravityKVClient()
	client.values[antigravityCreditsBalanceKey(authID)] = mustAntigravityJSON(t, antigravityCreditsBalance{
		CreditAmount:    10,
		MinCreditAmount: 50,
		Known:           true,
	})
	useFakeAntigravityKVClient(t, client, true, nil)
	antigravityCreditsBalanceByAuth.Store(authID, antigravityCreditsBalance{
		CreditAmount:    25000,
		MinCreditAmount: 50,
		Known:           true,
	})

	ok, errCredits := antigravityAuthHasCreditsRequired(context.Background(), &cliproxyauth.Auth{ID: authID})
	if errCredits != nil {
		t.Fatalf("antigravityAuthHasCreditsRequired() error = %v", errCredits)
	}
	if ok {
		t.Fatalf("antigravityAuthHasCreditsRequired() = true, want Home KV balance to win over local cache")
	}
	if client.getCount != 1 {
		t.Fatalf("KVGet count = %d, want 1", client.getCount)
	}
}

func TestStoreAntigravityCreditsBalanceBestEffortHomeKV(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)
	const authID = "home-balance-write-auth"
	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)

	storeAntigravityCreditsBalanceBestEffort(authID, antigravityCreditsBalance{
		CreditAmount:    25000,
		MinCreditAmount: 50,
		Known:           true,
	})

	if client.setCount != 1 || client.lastSetKey != antigravityCreditsBalanceKey(authID) || client.lastSetTTL != 30*time.Minute {
		t.Fatalf("KVSet count/key/ttl = %d/%s/%v, want 1/%s/30m", client.setCount, client.lastSetKey, client.lastSetTTL, antigravityCreditsBalanceKey(authID))
	}
	if _, ok := antigravityCreditsBalanceByAuth.Load(authID); ok {
		t.Fatalf("local balance cache was populated in Home mode")
	}
}

func TestAntigravityShortCooldownRequiredHomeKV(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)
	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)
	auth := &cliproxyauth.Auth{ID: "home-cooldown-auth"}
	now := time.Now()
	duration := 30 * time.Second

	if errMark := markAntigravityShortCooldownRequired(context.Background(), auth, "claude-sonnet-4-5", now, duration); errMark != nil {
		t.Fatalf("markAntigravityShortCooldownRequired() error = %v", errMark)
	}
	if client.setCount != 1 || client.lastSetTTL != duration+5*time.Second {
		t.Fatalf("KVSet count/ttl = %d/%v, want 1/%v", client.setCount, client.lastSetTTL, duration+5*time.Second)
	}
	antigravityShortCooldownByAuth = sync.Map{}
	inCooldown, remaining, errRead := antigravityIsInShortCooldownRequired(context.Background(), auth, "claude-sonnet-4-5", now.Add(5*time.Second))
	if errRead != nil {
		t.Fatalf("antigravityIsInShortCooldownRequired() error = %v", errRead)
	}
	if !inCooldown || remaining <= 0 {
		t.Fatalf("cooldown = %v remaining %v, want active Home KV cooldown", inCooldown, remaining)
	}
}

func TestAntigravityShortCooldownRequiredHomeKVFailures(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "home-cooldown-failure-auth"}
	for _, tc := range []struct {
		name   string
		client *fakeAntigravityKVClient
		write  bool
	}{
		{name: "read", client: &fakeAntigravityKVClient{values: make(map[string][]byte), getErr: errors.New("get failed")}},
		{name: "write", client: &fakeAntigravityKVClient{values: make(map[string][]byte), setErr: errors.New("set failed")}, write: true},
		{name: "delete-expired", client: &fakeAntigravityKVClient{
			values: map[string][]byte{
				antigravityShortCooldownKVKey(auth, "claude-sonnet-4-5"): []byte("1"),
			},
			delErr: errors.New("delete failed"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useFakeAntigravityKVClient(t, tc.client, true, nil)
			if tc.write {
				if errMark := markAntigravityShortCooldownRequired(context.Background(), auth, "claude-sonnet-4-5", time.Now(), time.Second); errMark == nil {
					t.Fatalf("markAntigravityShortCooldownRequired() error = nil, want error")
				}
				return
			}
			if _, _, errRead := antigravityIsInShortCooldownRequired(context.Background(), auth, "claude-sonnet-4-5", time.Now()); errRead == nil {
				t.Fatalf("antigravityIsInShortCooldownRequired() error = nil, want error")
			}
		})
	}
}

func TestMaybeRefreshAntigravityCreditsHintHomeRefreshThrottleUsesSetNX(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)
	client := newFakeAntigravityKVClient()
	client.setNXResult = false
	useFakeAntigravityKVClient(t, client, true, nil)
	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	})
	auth := &cliproxyauth.Auth{ID: "home-refresh-throttle-auth"}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("refresh request should not run when Home KV throttle lock is not acquired")
		return nil, nil
	}))

	exec.maybeRefreshAntigravityCreditsHint(ctx, auth, "access-token")

	if client.setNXCount != 1 || client.lastSetNXKey != antigravityCreditsRefreshLockKey(auth.ID) || client.lastSetNXTTL != antigravityCreditsHintRefreshInterval {
		t.Fatalf("KVSetNX count/key/ttl = %d/%s/%v, want 1/%s/%v", client.setNXCount, client.lastSetNXKey, client.lastSetNXTTL, antigravityCreditsRefreshLockKey(auth.ID), antigravityCreditsHintRefreshInterval)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestEnsureAccessToken_WarmTokenLoadsCreditsHint(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	})
	auth := &cliproxyauth.Auth{
		ID: "auth-warm-token-credits",
		Metadata: map[string]any{
			"access_token": "token",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
			t.Fatalf("unexpected request url %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"paidTier":{"id":"tier-1","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"25000","minimumCreditAmountForUsage":"50"}]}}`)),
		}, nil
	}))

	token, updatedAuth, err := exec.ensureAccessToken(ctx, auth)
	if err != nil {
		t.Fatalf("ensureAccessToken() error = %v", err)
	}
	if token != "token" {
		t.Fatalf("ensureAccessToken() token = %q, want %q", token, "token")
	}
	if updatedAuth != nil {
		t.Fatalf("ensureAccessToken() updatedAuth = %v, want nil", updatedAuth)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !cliproxyauth.HasKnownAntigravityCreditsHint(auth.ID) {
		time.Sleep(10 * time.Millisecond)
	}
	if !cliproxyauth.HasKnownAntigravityCreditsHint(auth.ID) {
		t.Fatal("expected credits hint to be populated for warm token auth")
	}
	hint, ok := cliproxyauth.GetAntigravityCreditsHint(auth.ID)
	if !ok {
		t.Fatal("expected credits hint lookup to succeed")
	}
	if !hint.Available {
		t.Fatalf("hint.Available = %v, want true", hint.Available)
	}
	if hint.CreditAmount != 25000 || hint.MinCreditAmount != 50 {
		t.Fatalf("hint amounts = (%v, %v), want (25000, 50)", hint.CreditAmount, hint.MinCreditAmount)
	}
}

func TestUpdateAntigravityCreditsBalance_LoadCodeAssistUserAgent(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	exec := NewAntigravityExecutor(&config.Config{})
	const configuredUserAgent = "antigravity/1.23.2 windows/amd64 google-api-nodejs-client/10.3.0"
	const loadCodeAssistUserAgent = "antigravity/1.23.2 windows/amd64"
	auth := &cliproxyauth.Auth{
		ID:         "auth-load-code-assist-ua",
		Attributes: map[string]string{"user_agent": configuredUserAgent},
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
			t.Fatalf("unexpected request url %s", req.URL.String())
		}
		if got := req.Header.Get("User-Agent"); got != loadCodeAssistUserAgent {
			t.Fatalf("User-Agent = %q, want %q", got, loadCodeAssistUserAgent)
		}
		if got := req.Header.Get("X-Goog-Api-Client"); got != "" {
			t.Fatalf("X-Goog-Api-Client = %q, want empty", got)
		}
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if string(body) != `{"metadata":{"ideType":"ANTIGRAVITY"}}` {
			t.Fatalf("loadCodeAssist body = %s", string(body))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"paidTier":{"id":"tier-1","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"25000","minimumCreditAmountForUsage":"50"}]}}`)),
		}, nil
	}))

	exec.updateAntigravityCreditsBalance(ctx, auth, "token")
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
