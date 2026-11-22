package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// resetAntigravityCreditsRetryState clears the package-level credits state
// between tests. It empties each map in place instead of assigning a fresh
// sync.Map, because credits hint refreshes run on background goroutines that
// may still be writing these maps when a test's cleanup runs. Replacing the
// variable is an unsynchronized write and races with them; Clear is not.
func resetAntigravityCreditsRetryState() {
	antigravityTOSPauseUntil.Clear()
	antigravityCreditsFailureByAuth.Clear()
	antigravityShortCooldownByAuth.Clear()
	antigravityCreditsBalanceByAuth.Clear()
	antigravityCreditsCurrentScope.Clear()
	antigravityCreditsHintRefreshByID.Clear()
	antigravityCreditsRefreshRegistries.Clear()
}

type closeSignalReadCloser struct {
	io.ReadCloser
	closed chan<- struct{}
}

func (c *closeSignalReadCloser) Close() error {
	errClose := c.ReadCloser.Close()
	close(c.closed)
	return errClose
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

func (c *fakeAntigravityKVClient) KVCompareAndSwap(_ context.Context, key string, expected []byte, expectedExists bool, value []byte, _ time.Duration) (bool, error) {
	current, ok := c.values[key]
	if ok != expectedExists || (expectedExists && string(current) != string(expected)) {
		return false, nil
	}
	c.values[key] = append([]byte(nil), value...)
	return true, nil
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

func (c *fakeAntigravityKVClient) KVExpire(context.Context, string, time.Duration) (bool, error) {
	return false, nil
}

func (c *fakeAntigravityKVClient) KVTTL(context.Context, string) (time.Duration, bool, error) {
	return 0, false, nil
}

func (c *fakeAntigravityKVClient) KVIncrBy(context.Context, string, int64) (int64, error) {
	return 0, nil
}

func (c *fakeAntigravityKVClient) KVMGet(context.Context, ...string) ([][]byte, []bool, error) {
	return nil, nil, nil
}

func (c *fakeAntigravityKVClient) KVMSet(context.Context, map[string][]byte) error {
	return nil
}

func useFakeAntigravityKVClient(t *testing.T, client *fakeAntigravityKVClient, homeMode bool, errClient error) {
	t.Helper()
	previous := currentAntigravityKVClient
	currentAntigravityKVClient = func() (antigravityKVClient, bool, error) {
		return client, homeMode, errClient
	}
	kvstore.RegisterHomeProvider(func() (kvstore.KVClient, bool, error) {
		return client, homeMode, errClient
	})
	t.Cleanup(func() {
		currentAntigravityKVClient = previous
		kvstore.RegisterHomeProvider(func() (kvstore.KVClient, bool, error) { return nil, false, nil })
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

func TestAntigravityExecute_DoesNotUseRequestRetryForInternalRetries(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 3})
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
	if err == nil {
		t.Fatalf("Execute() error = nil, want upstream 429")
	}
	if len(resp.Payload) != 0 {
		t.Fatalf("Execute() returned payload %q, want empty payload", resp.Payload)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
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
		ID: fmt.Sprintf("auth-credits-conductor-%d", time.Now().UnixNano()),
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
	stateValue, ok := antigravityCreditsHintRefreshByID.Load(auth.ID)
	if !ok {
		t.Fatal("expected credits refresh state")
	}
	state, ok := stateValue.(*antigravityCreditsHintRefreshState)
	if !ok || state == nil {
		t.Fatal("credits refresh state has unexpected type")
	}
	waitForAntigravityCreditsRefresh(t, state)
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
	if client.getCount != 2 {
		t.Fatalf("KVGet count = %d, want 2 (hint then balance)", client.getCount)
	}
}

func TestStoreAntigravityCreditsBalanceBestEffortHomeKV(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)
	const authID = "home-balance-write-auth"
	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)

	storeAntigravityCreditsBalanceBestEffort(&cliproxyauth.Auth{ID: authID}, antigravityCreditsBalance{
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
		ID: fmt.Sprintf("auth-warm-token-credits-%d", time.Now().UnixNano()),
		Metadata: map[string]any{
			"access_token": "token",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}
	refreshDone := make(chan struct{})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
			t.Fatalf("unexpected request url %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: &closeSignalReadCloser{
				ReadCloser: io.NopCloser(strings.NewReader(`{"paidTier":{"id":"tier-1","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"25000","minimumCreditAmountForUsage":"50"}]}}`)),
				closed:     refreshDone,
			},
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
	select {
	case <-refreshDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background credits refresh")
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

func TestMaybeRefreshAntigravityCreditsHint_RefreshesExpiredHintAndRecoversCredits(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	})
	auth := &cliproxyauth.Auth{
		ID: fmt.Sprintf("auth-expired-credits-hint-%d", time.Now().UnixNano()),
		Metadata: map[string]any{
			"access_token": "token",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}
	t.Cleanup(func() {
		cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
	})

	markAntigravityCreditsPermanentlyDisabled(auth)
	if _, failed := antigravityCreditsFailureByAuth.Load(auth.ID); !failed {
		t.Fatal("expected auth to be marked permanently disabled for credits")
	}

	var loadCodeAssistCalls int
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
			t.Fatalf("unexpected request url %s", req.URL.String())
		}
		loadCodeAssistCalls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"paidTier":{"id":"tier-1","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"2516","minimumCreditAmountForUsage":"50"}]}}`)),
		}, nil
	}))

	// Fresh hint (< antigravityCreditsHintRefreshInterval) must not trigger a refresh.
	exec.maybeRefreshAntigravityCreditsHint(ctx, auth, "token")
	if loadCodeAssistCalls != 0 {
		t.Fatalf("loadCodeAssist calls for fresh hint = %d, want 0", loadCodeAssistCalls)
	}

	// Once hint.UpdatedAt is older than antigravityCreditsHintRefreshInterval,
	// maybeRefreshAntigravityCreditsHint must re-query loadCodeAssist and recover credits.
	cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{
		Known:           true,
		Available:       false,
		CreditAmount:    0,
		MinCreditAmount: 1,
		UpdatedAt:       time.Now().Add(-antigravityCreditsHintRefreshInterval - time.Second),
	})

	exec.maybeRefreshAntigravityCreditsHint(ctx, auth, "token")
	if value, ok := antigravityCreditsHintRefreshByID.Load(auth.ID); ok {
		if state, ok := value.(*antigravityCreditsHintRefreshState); ok && state != nil {
			waitForAntigravityCreditsRefresh(t, state)
		}
	}

	if loadCodeAssistCalls != 1 {
		t.Fatalf("loadCodeAssist calls for expired hint = %d, want 1", loadCodeAssistCalls)
	}
	hint, ok := cliproxyauth.GetAntigravityCreditsHint(auth.ID)
	if !ok || !hint.Known || !hint.Available {
		t.Fatalf("credits hint after refresh = %#v (ok=%v), want Known=true Available=true", hint, ok)
	}
	if hint.CreditAmount != 2516 || hint.MinCreditAmount != 50 {
		t.Fatalf("hint amounts = (%v, %v), want (2516, 50)", hint.CreditAmount, hint.MinCreditAmount)
	}
	if _, failed := antigravityCreditsFailureByAuth.Load(auth.ID); failed {
		t.Fatal("expected permanently disabled credits state to be cleared after recovery")
	}
}

func TestAntigravityManagerExecuteAndStream_ToppedUpCreditsRecoverDuringQuotaCooldown(t *testing.T) {
	for _, stream := range []bool{false, true} {
		mode := "execute"
		if stream {
			mode = "execute_stream"
		}
		t.Run(mode, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			const model = "claude-sonnet-4-6"
			var toppedUp atomic.Bool
			var loadCodeAssistCalls atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = r.Body.Close()

				if r.URL.Path == "/v1internal:loadCodeAssist" {
					callNum := loadCodeAssistCalls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					if toppedUp.Load() {
						_, _ = w.Write([]byte(`{"paidTier":{"id":"tier-1","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"2516","minimumCreditAmountForUsage":"50"}]}}`))
					} else if callNum == 1 {
						_, _ = w.Write([]byte(`{"paidTier":{"id":"tier-1","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"100","minimumCreditAmountForUsage":"50"}]}}`))
					} else {
						_, _ = w.Write([]byte(`{"paidTier":{"id":"tier-1","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"0","minimumCreditAmountForUsage":"50"}]}}`))
					}
					return
				}

				hasCreditsFlag := strings.Contains(string(body), `"enabledCreditTypes":["GOOGLE_ONE_AI"]`)
				if !hasCreditsFlag {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{
						"error": {
							"code": 429,
							"message": "You have exhausted your capacity on this model. Your quota will reset after 1h0m0s.",
							"status": "RESOURCE_EXHAUSTED",
							"details": [
								{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "QUOTA_EXHAUSTED"},
								{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "3600s"}
							]
						}
					}`))
					return
				}

				if !toppedUp.Load() {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{
						"error": {
							"code": 429,
							"message": "Insufficient Google One AI credits balance.",
							"status": "RESOURCE_EXHAUSTED",
							"details": [
								{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "INSUFFICIENT_G1_CREDITS_BALANCE"}
							]
						}
					}`))
					return
				}

				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"recovered\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}}\n\n"))
			}))
			defer server.Close()

			cfg := &config.Config{
				QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
			}
			exec := NewAntigravityExecutor(cfg)
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(exec)

			authID := fmt.Sprintf("ag-topup-recovery-%s-%d", mode, time.Now().UnixNano())
			registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(authID)
				cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{})
			})

			registered, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
				ID:       authID,
				Provider: "antigravity",
				Attributes: map[string]string{
					"base_url": server.URL,
				},
				Metadata: map[string]any{
					"access_token": "token",
					"project_id":   "project-1",
					"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
				},
			})
			if errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatAntigravity,
				ResponseFormat: sdktranslator.FormatAntigravity,
			}

			// Step 1: Exhaust weekly quota (QUOTA_EXHAUSTED) and credits (INSUFFICIENT_G1_CREDITS_BALANCE).
			cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{
				Known:           true,
				Available:       true,
				CreditAmount:    100,
				MinCreditAmount: 50,
				UpdatedAt:       time.Now(),
			}, registered.RegistrationEpoch)
			if stream {
				if _, errStream := manager.ExecuteStream(context.Background(), []string{"antigravity"}, req, opts); errStream == nil {
					t.Fatal("initial ExecuteStream() succeeded, want 429 after quota and credits exhaustion")
				}
			} else {
				if _, errExec := manager.Execute(context.Background(), []string{"antigravity"}, req, opts); errExec == nil {
					t.Fatal("initial Execute() succeeded, want 429 after quota and credits exhaustion")
				}
			}
			if value, ok := antigravityCreditsHintRefreshByID.Load(authID); ok {
				if state, ok := value.(*antigravityCreditsHintRefreshState); ok && state != nil {
					waitForAntigravityCreditsRefresh(t, state)
				}
			}
			if _, failed := antigravityCreditsFailureByAuth.Load(newAntigravityCreditsCacheKey(authID, cliproxyauth.AntigravityCreditsScope(registered))); !failed {
				t.Fatal("expected auth to be marked permanently disabled after INSUFFICIENT_G1_CREDITS_BALANCE")
			}
			hint, ok := cliproxyauth.GetAntigravityCreditsHint(authID, registered.RegistrationEpoch)
			if !ok || !hint.Known || hint.Available {
				t.Fatalf("hint after exhaustion = %#v (ok=%v), want Known=true Available=false", hint, ok)
			}

			// Step 2: Top up credits and expire the cached hint/throttle window while model remains in 1h quota cooldown.
			toppedUp.Store(true)
			callsBeforeTopUp := loadCodeAssistCalls.Load()
			if value, ok := antigravityCreditsHintRefreshByID.Load(authID); ok {
				if state, ok := value.(*antigravityCreditsHintRefreshState); ok && state != nil {
					state.mu.Lock()
					state.lastAttempt = time.Now().Add(-antigravityCreditsHintRefreshInterval - time.Second)
					state.mu.Unlock()
				}
			}
			cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{
				Known:           true,
				Available:       false,
				CreditAmount:    0,
				MinCreditAmount: 1,
				UpdatedAt:       time.Now().Add(-antigravityCreditsHintRefreshInterval - time.Second),
			}, registered.RegistrationEpoch)

			// Step 3: Request recovers via credits fallback while model cooldown is active and triggers loadCodeAssist refresh.
			if stream {
				streamResult, errStream := manager.ExecuteStream(context.Background(), []string{"antigravity"}, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream() after top-up error = %v", errStream)
				}
				var streamed strings.Builder
				for chunk := range streamResult.Chunks {
					if chunk.Err != nil {
						t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
					}
					streamed.Write(chunk.Payload)
				}
				if !strings.Contains(streamed.String(), "recovered") {
					t.Fatalf("ExecuteStream() payload = %s, want recovered", streamed.String())
				}
			} else {
				resp, errExec := manager.Execute(context.Background(), []string{"antigravity"}, req, opts)
				if errExec != nil {
					t.Fatalf("Execute() after top-up error = %v", errExec)
				}
				if !strings.Contains(string(resp.Payload), "recovered") {
					t.Fatalf("Execute() payload = %s, want recovered", string(resp.Payload))
				}
			}

			if value, ok := antigravityCreditsHintRefreshByID.Load(authID); ok {
				if state, ok := value.(*antigravityCreditsHintRefreshState); ok && state != nil {
					waitForAntigravityCreditsRefresh(t, state)
				}
			}
			if loadCodeAssistCalls.Load() <= callsBeforeTopUp {
				t.Fatalf("loadCodeAssist calls after top-up = %d, want > %d", loadCodeAssistCalls.Load(), callsBeforeTopUp)
			}
			hint, ok = cliproxyauth.GetAntigravityCreditsHint(authID, registered.RegistrationEpoch)
			if !ok || !hint.Known || !hint.Available || hint.CreditAmount != 2516 {
				t.Fatalf("hint after recovery = %#v (ok=%v), want Known=true Available=true CreditAmount=2516", hint, ok)
			}
			if _, failed := antigravityCreditsFailureByAuth.Load(newAntigravityCreditsCacheKey(authID, cliproxyauth.AntigravityCreditsScope(registered))); failed {
				t.Fatal("expected permanently disabled credits state to be cleared after recovery")
			}
		})
	}
}

func TestUpdateAntigravityCreditsBalance_LoadCodeAssistUserAgent(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	})
	const configuredUserAgent = "antigravity/hub/1.23.2 windows/amd64 google-api-nodejs-client/10.3.0"
	const loadCodeAssistUserAgent = "antigravity/hub/1.23.2 windows/amd64"
	auth := &cliproxyauth.Auth{
		ID:         "auth-load-code-assist-ua",
		Attributes: map[string]string{"user_agent": configuredUserAgent},
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
			t.Fatalf("unexpected request url %s", req.URL.String())
		}
		if got := req.Header.Get("User-Agent"); got != loadCodeAssistUserAgent {
			t.Fatalf("User-Agent = %q, want %q", got, loadCodeAssistUserAgent)
		}
		if got := req.Header.Get("Accept"); got != "" {
			t.Fatalf("Accept = %q, want omitted", got)
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

type countingReadCloser struct {
	reader io.Reader
	readN  int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.readN += int64(n)
	return n, err
}

func (c *countingReadCloser) Close() error {
	return nil
}

func TestUpdateAntigravityCreditsBalanceForTask_EmptyAndNonMatchingAvailableCredits(t *testing.T) {
	tests := []struct {
		name           string
		responseBody   string
		seedPriorHint  bool
		wantPaidTierID string
	}{
		{
			name:           "empty availableCredits array",
			responseBody:   `{"paidTier":{"id":"pro","availableCredits":[]}}`,
			wantPaidTierID: "pro",
		},
		{
			name:           "unrelated creditType only",
			responseBody:   `{"paidTier":{"id":"pro","availableCredits":[{"creditType":"OTHER_CREDIT","creditAmount":"500","minimumCreditAmountForUsage":"10"}]}}`,
			wantPaidTierID: "pro",
		},
		{
			name:           "unparseable GOOGLE_ONE_AI entry",
			responseBody:   `{"paidTier":{"id":"pro","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"not-a-number","minimumCreditAmountForUsage":"50"}]}}`,
			wantPaidTierID: "pro",
		},
		{
			name:           "empty availableCredits overwrites stale positive hint",
			responseBody:   `{"paidTier":{"id":"pro","availableCredits":[]}}`,
			seedPriorHint:  true,
			wantPaidTierID: "pro",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			authID := fmt.Sprintf("auth-issue7750-%s-%d", strings.ReplaceAll(tc.name, " ", "-"), time.Now().UnixNano())
			t.Cleanup(func() {
				cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{})
			})

			if tc.seedPriorHint {
				cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{
					Known:           true,
					Available:       true,
					CreditAmount:    25000,
					MinCreditAmount: 50,
					PaidTierID:      "pro",
					UpdatedAt:       time.Now(),
				})
			}

			exec := NewAntigravityExecutor(&config.Config{
				QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
			})
			auth := &cliproxyauth.Auth{ID: authID}
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
					t.Fatalf("unexpected request url %s", req.URL.String())
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(tc.responseBody)),
				}, nil
			}))

			exec.updateAntigravityCreditsBalanceForTask(ctx, auth, "token", nil)

			hint, ok := cliproxyauth.GetAntigravityCreditsHint(auth.ID)
			if !ok || !hint.Known {
				t.Fatalf("GetAntigravityCreditsHint() = (%#v, %v), want Known=true", hint, ok)
			}
			if hint.Available {
				t.Fatalf("hint.Available = true, want false")
			}
			if hint.PaidTierID != tc.wantPaidTierID {
				t.Fatalf("hint.PaidTierID = %q, want %q", hint.PaidTierID, tc.wantPaidTierID)
			}
			if hint.UpdatedAt.IsZero() {
				t.Fatal("hint.UpdatedAt is zero, want non-zero timestamp")
			}

			hasCredits, errCredits := antigravityAuthHasCreditsRequired(ctx, auth)
			if errCredits != nil {
				t.Fatalf("antigravityAuthHasCreditsRequired() error = %v", errCredits)
			}
			if hasCredits {
				t.Fatal("antigravityAuthHasCreditsRequired() = true, want false")
			}
		})
	}
}

func TestUpdateAntigravityCreditsBalanceForTask_MissingAndZeroMinimumCreditAmountForUsage(t *testing.T) {
	tests := []struct {
		name           string
		responseBody   string
		seedDisabled   bool
		wantAvailable  bool
		wantStoredBal  bool
		wantCredit     float64
		wantMinCredit  float64
		wantPaidTierID string
	}{
		{
			name:           "omitted minimumCreditAmountForUsage defaults to zero and marks positive balance available",
			responseBody:   `{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"1000"}]}}`,
			seedDisabled:   true,
			wantAvailable:  true,
			wantStoredBal:  true,
			wantCredit:     1000,
			wantMinCredit:  0,
			wantPaidTierID: "g1-pro-tier",
		},
		{
			name:           "null minimumCreditAmountForUsage defaults to zero",
			responseBody:   `{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"1000","minimumCreditAmountForUsage":null}]}}`,
			wantAvailable:  true,
			wantStoredBal:  true,
			wantCredit:     1000,
			wantMinCredit:  0,
			wantPaidTierID: "g1-pro-tier",
		},
		{
			name:           "empty string minimumCreditAmountForUsage defaults to zero",
			responseBody:   `{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"1000","minimumCreditAmountForUsage":""}]}}`,
			wantAvailable:  true,
			wantStoredBal:  true,
			wantCredit:     1000,
			wantMinCredit:  0,
			wantPaidTierID: "g1-pro-tier",
		},
		{
			name:           "whitespace minimumCreditAmountForUsage defaults to zero",
			responseBody:   `{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"1000","minimumCreditAmountForUsage":"   "}]}}`,
			wantAvailable:  true,
			wantStoredBal:  true,
			wantCredit:     1000,
			wantMinCredit:  0,
			wantPaidTierID: "g1-pro-tier",
		},
		{
			name:           "omitted minimumCreditAmountForUsage with zero creditAmount marks unavailable",
			responseBody:   `{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"0"}]}}`,
			wantAvailable:  false,
			wantStoredBal:  true,
			wantCredit:     0,
			wantMinCredit:  0,
			wantPaidTierID: "g1-pro-tier",
		},
		{
			name:           "explicit zero minimumCreditAmountForUsage with zero creditAmount marks unavailable",
			responseBody:   `{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"0","minimumCreditAmountForUsage":"0"}]}}`,
			wantAvailable:  false,
			wantStoredBal:  true,
			wantCredit:     0,
			wantMinCredit:  0,
			wantPaidTierID: "g1-pro-tier",
		},
		{
			name:           "omitted minimumCreditAmountForUsage with negative creditAmount marks unavailable",
			responseBody:   `{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"-5"}]}}`,
			wantAvailable:  false,
			wantStoredBal:  true,
			wantCredit:     -5,
			wantMinCredit:  0,
			wantPaidTierID: "g1-pro-tier",
		},
		{
			name:           "invalid non-empty minimumCreditAmountForUsage skips entry and publishes unavailable",
			responseBody:   `{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"1000","minimumCreditAmountForUsage":"not-a-number"}]}}`,
			wantAvailable:  false,
			wantStoredBal:  false,
			wantPaidTierID: "g1-pro-tier",
		},
		{
			name:           "negative minimumCreditAmountForUsage skips entry and publishes unavailable",
			responseBody:   `{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"1000","minimumCreditAmountForUsage":"-10"}]}}`,
			wantAvailable:  false,
			wantStoredBal:  false,
			wantPaidTierID: "g1-pro-tier",
		},
		{
			name:           "non-finite creditAmount skips entry and publishes unavailable",
			responseBody:   `{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"Inf","minimumCreditAmountForUsage":"50"}]}}`,
			wantAvailable:  false,
			wantStoredBal:  false,
			wantPaidTierID: "g1-pro-tier",
		},
		{
			name:           "non-finite minimumCreditAmountForUsage skips entry and publishes unavailable",
			responseBody:   `{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"1000","minimumCreditAmountForUsage":"NaN"}]}}`,
			wantAvailable:  false,
			wantStoredBal:  false,
			wantPaidTierID: "g1-pro-tier",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			authID := fmt.Sprintf("auth-issue7798-%s-%d", strings.ReplaceAll(tc.name, " ", "-"), time.Now().UnixNano())
			t.Cleanup(func() {
				cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{})
			})

			auth := &cliproxyauth.Auth{ID: authID}
			if tc.seedDisabled {
				markAntigravityCreditsPermanentlyDisabled(auth)
			}

			exec := NewAntigravityExecutor(&config.Config{
				QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(tc.responseBody)),
				}, nil
			}))

			exec.updateAntigravityCreditsBalanceForTask(ctx, auth, "token", nil)

			hint, ok := cliproxyauth.GetAntigravityCreditsHint(auth.ID)
			if !ok || !hint.Known {
				t.Fatalf("GetAntigravityCreditsHint() = (%#v, %v), want Known=true", hint, ok)
			}
			if hint.Available != tc.wantAvailable {
				t.Fatalf("hint.Available = %v, want %v", hint.Available, tc.wantAvailable)
			}
			if hint.PaidTierID != tc.wantPaidTierID {
				t.Fatalf("hint.PaidTierID = %q, want %q", hint.PaidTierID, tc.wantPaidTierID)
			}
			if hint.UpdatedAt.IsZero() {
				t.Fatal("hint.UpdatedAt is zero, want non-zero timestamp")
			}
			if tc.wantStoredBal {
				if hint.CreditAmount != tc.wantCredit || hint.MinCreditAmount != tc.wantMinCredit {
					t.Fatalf("hint amounts = (%v, %v), want (%v, %v)", hint.CreditAmount, hint.MinCreditAmount, tc.wantCredit, tc.wantMinCredit)
				}
				rawBal, foundBal := antigravityCreditsBalanceByAuth.Load(auth.ID)
				if !foundBal {
					t.Fatal("antigravityCreditsBalanceByAuth missing stored balance, want present")
				}
				bal, validBal := rawBal.(antigravityCreditsBalance)
				if !validBal || !bal.Known || bal.CreditAmount != tc.wantCredit || bal.MinCreditAmount != tc.wantMinCredit {
					t.Fatalf("stored balance = %#v, want Known=true CreditAmount=%v MinCreditAmount=%v", bal, tc.wantCredit, tc.wantMinCredit)
				}
			} else if _, foundBal := antigravityCreditsBalanceByAuth.Load(auth.ID); foundBal {
				t.Fatal("antigravityCreditsBalanceByAuth stored balance for skipped entry, want absent")
			}
			if tc.seedDisabled && tc.wantAvailable {
				if _, stillDisabled := antigravityCreditsFailureByAuth.Load(auth.ID); stillDisabled {
					t.Fatal("antigravityCreditsFailureByAuth still present after positive balance recovery, want cleared")
				}
			}

			hasCredits, errCredits := antigravityAuthHasCreditsRequired(ctx, auth)
			if errCredits != nil {
				t.Fatalf("antigravityAuthHasCreditsRequired() error = %v", errCredits)
			}
			if hasCredits != tc.wantAvailable {
				t.Fatalf("antigravityAuthHasCreditsRequired() = %v, want %v", hasCredits, tc.wantAvailable)
			}
		})
	}
}

func TestAntigravityAuthHasCredits_ZeroAndNonFiniteBalances(t *testing.T) {
	tests := []struct {
		name string
		bal  antigravityCreditsBalance
		want bool
	}{
		{
			name: "positive balance with zero minimum is available",
			bal:  antigravityCreditsBalance{CreditAmount: 1000, MinCreditAmount: 0, Known: true},
			want: true,
		},
		{
			name: "zero balance with zero minimum is unavailable",
			bal:  antigravityCreditsBalance{CreditAmount: 0, MinCreditAmount: 0, Known: true},
			want: false,
		},
		{
			name: "negative balance with zero minimum is unavailable",
			bal:  antigravityCreditsBalance{CreditAmount: -5, MinCreditAmount: 0, Known: true},
			want: false,
		},
		{
			name: "positive balance with negative minimum is unavailable",
			bal:  antigravityCreditsBalance{CreditAmount: 100, MinCreditAmount: -1, Known: true},
			want: false,
		},
		{
			name: "infinite credit amount is unavailable",
			bal:  antigravityCreditsBalance{CreditAmount: math.Inf(1), MinCreditAmount: 0, Known: true},
			want: false,
		},
		{
			name: "NaN credit amount is unavailable",
			bal:  antigravityCreditsBalance{CreditAmount: math.NaN(), MinCreditAmount: 0, Known: true},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			authID := fmt.Sprintf("auth-issue7798-bal-%s-%d", strings.ReplaceAll(tc.name, " ", "-"), time.Now().UnixNano())
			t.Cleanup(func() {
				cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{})
			})

			antigravityCreditsBalanceByAuth.Store(authID, tc.bal)
			if got := antigravityAuthHasCredits(&cliproxyauth.Auth{ID: authID}); got != tc.want {
				t.Fatalf("antigravityAuthHasCredits() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUpdateAntigravityCreditsBalanceForTask_BoundsResponseBodyRead(t *testing.T) {
	t.Run("rejects oversized response without unbounded read", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		authID := fmt.Sprintf("auth-issue7750-oversized-%d", time.Now().UnixNano())
		t.Cleanup(func() {
			cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{})
		})

		const maxExpectedRead = antigravityLoadCodeAssistMaxResponseBytes + 1
		oversizedBody := `{"paidTier":{"id":"pro","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"25000","minimumCreditAmountForUsage":"50"}]},"padding":"` + strings.Repeat("a", antigravityLoadCodeAssistMaxResponseBytes+1024) + `"}`
		body := &countingReadCloser{reader: strings.NewReader(oversizedBody)}

		exec := NewAntigravityExecutor(&config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		})
		auth := &cliproxyauth.Auth{ID: authID}
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       body,
			}, nil
		}))

		exec.updateAntigravityCreditsBalanceForTask(ctx, auth, "token", nil)

		if body.readN > maxExpectedRead {
			t.Fatalf("loadCodeAssist read %d bytes, want <= %d bytes", body.readN, maxExpectedRead)
		}
		if cliproxyauth.HasKnownAntigravityCreditsHint(auth.ID) {
			t.Fatal("oversized loadCodeAssist response should not publish credits hint")
		}
	})

	t.Run("ignores invalid JSON 200 response", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		authID := fmt.Sprintf("auth-issue7750-invalid-json-%d", time.Now().UnixNano())
		t.Cleanup(func() {
			cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{})
		})

		exec := NewAntigravityExecutor(&config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		})
		auth := &cliproxyauth.Auth{ID: authID}
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("<html>bad gateway</html>")),
			}, nil
		}))

		exec.updateAntigravityCreditsBalanceForTask(ctx, auth, "token", nil)

		if cliproxyauth.HasKnownAntigravityCreditsHint(auth.ID) {
			t.Fatal("invalid JSON response should not publish credits hint")
		}
	})
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

type errorAfterReadCloser struct {
	payload []byte
	read    bool
	err     error
}

func (r *errorAfterReadCloser) Read(p []byte) (int, error) {
	if !r.read {
		r.read = true
		n := copy(p, r.payload)
		return n, r.err
	}
	return 0, r.err
}

func (r *errorAfterReadCloser) Close() error {
	return nil
}

func TestUpdateAntigravityCreditsBalanceForTask_HostAndCustomHeaders(t *testing.T) {
	t.Run("applies resolved Host and credential custom headers", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		var gotHost string
		var gotCustom string
		var gotURL string

		exec := NewAntigravityExecutor(&config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		})
		auth := &cliproxyauth.Auth{
			ID: "auth-issue7820-custom-headers",
			Attributes: map[string]string{
				"base_url":               "https://proxy.example.com:8443",
				"header:X-Custom-Header": "custom-123",
			},
		}
		t.Cleanup(func() {
			cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			gotURL = req.URL.String()
			gotHost = req.Host
			gotCustom = req.Header.Get("X-Custom-Header")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"paidTier":{"id":"pro","availableCredits":[]}}`)),
			}, nil
		}))

		exec.updateAntigravityCreditsBalance(ctx, auth, "token")

		if gotURL != "https://proxy.example.com:8443/v1internal:loadCodeAssist" {
			t.Fatalf("request URL = %q, want %q", gotURL, "https://proxy.example.com:8443/v1internal:loadCodeAssist")
		}
		if gotHost != "proxy.example.com:8443" {
			t.Fatalf("req.Host = %q, want %q", gotHost, "proxy.example.com:8443")
		}
		if gotCustom != "custom-123" {
			t.Fatalf("X-Custom-Header = %q, want %q", gotCustom, "custom-123")
		}
	})

	t.Run("allows header:Host attribute to override resolved Host", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		var gotHost string
		var gotCustom string

		exec := NewAntigravityExecutor(&config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		})
		auth := &cliproxyauth.Auth{
			ID: "auth-issue7820-host-override",
			Attributes: map[string]string{
				"base_url":               "https://proxy.example.com:8443",
				"header:Host":            "override.example.com",
				"header:X-Custom-Header": "custom-456",
			},
		}
		t.Cleanup(func() {
			cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			gotHost = req.Host
			gotCustom = req.Header.Get("X-Custom-Header")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"paidTier":{"id":"pro","availableCredits":[]}}`)),
			}, nil
		}))

		exec.updateAntigravityCreditsBalance(ctx, auth, "token")

		if gotHost != "override.example.com" {
			t.Fatalf("req.Host = %q, want %q", gotHost, "override.example.com")
		}
		if gotCustom != "custom-456" {
			t.Fatalf("X-Custom-Header = %q, want %q", gotCustom, "custom-456")
		}
	})

	t.Run("ignores pooled gin context and client templates during background credits refresh", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		var gotStatic string
		var gotDynamicClient string
		var gotDynamicSession string

		ginReq := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		ginReq.Header.Set("X-Client-Secret", "client-secret")
		ginReq = ginReq.WithContext(util.WithSessionID(ginReq.Context(), "gin-session-id"))
		ginCtx := &gin.Context{Request: ginReq}

		stopMutate := make(chan struct{})
		mutatorStarted := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			close(mutatorStarted)
			for {
				select {
				case <-stopMutate:
					return
				default:
					ginCtx.Request = nil
					ginCtx.Request = ginReq
				}
			}
		}()
		<-mutatorStarted
		t.Cleanup(func() {
			close(stopMutate)
			wg.Wait()
		})

		exec := NewAntigravityExecutor(&config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		})
		auth := &cliproxyauth.Auth{
			ID: "auth-issue7820-detached-gin",
			Attributes: map[string]string{
				"header:X-Static-Header":   "static-ok",
				"header:X-Dynamic-Client":  "$X-Client-Secret",
				"header:X-Dynamic-Session": "$CPA-SESSION-ID",
			},
		}
		t.Cleanup(func() {
			cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
		})
		ctx := context.WithValue(context.Background(), "gin", ginCtx)
		ctx = util.WithSessionID(ctx, "ctx-session-id")
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			gotStatic = req.Header.Get("X-Static-Header")
			gotDynamicClient = req.Header.Get("X-Dynamic-Client")
			gotDynamicSession = req.Header.Get("X-Dynamic-Session")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"paidTier":{"id":"pro","availableCredits":[]}}`)),
			}, nil
		}))

		exec.updateAntigravityCreditsBalance(ctx, auth, "token")

		if gotStatic != "static-ok" {
			t.Fatalf("X-Static-Header = %q, want %q", gotStatic, "static-ok")
		}
		if gotDynamicClient != "" {
			t.Fatalf("X-Dynamic-Client = %q, want empty", gotDynamicClient)
		}
		if gotDynamicSession != "" {
			t.Fatalf("X-Dynamic-Session = %q, want empty", gotDynamicSession)
		}
	})
}

func TestUpdateAntigravityCreditsBalanceForTask_ForbiddenTOSViolation(t *testing.T) {
	t.Run("arms tos-pause-window and logs appeal URL on 403 TOS_VIOLATION", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		logger := log.StandardLogger()
		oldLevel := logger.GetLevel()
		oldHooks := logger.ReplaceHooks(make(log.LevelHooks))
		oldOutput := logger.Out
		logger.SetLevel(log.WarnLevel)
		logger.SetOutput(io.Discard)
		hook := logtest.NewLocal(logger)
		t.Cleanup(func() {
			logger.SetLevel(oldLevel)
			logger.ReplaceHooks(oldHooks)
			logger.SetOutput(oldOutput)
		})

		cfg := &config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		}
		cfg.Antigravity.TOSPauseWindow = "1h"
		exec := NewAntigravityExecutor(cfg)
		auth := &cliproxyauth.Auth{
			ID:       "auth-issue7820-tos-403",
			Provider: "antigravity",
		}
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(antigravityTOSBody)),
			}, nil
		}))

		exec.updateAntigravityCreditsBalance(ctx, auth, "token")

		if !antigravityInTOSPause(cfg, auth) {
			t.Fatal("antigravityInTOSPause() = false after 403 TOS_VIOLATION, want true")
		}
		foundAppealLog := false
		for _, entry := range hook.AllEntries() {
			if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "TOS_VIOLATION") && strings.Contains(entry.Message, "https://forms.gle/hGzM9MEUv2azZsrb9") {
				foundAppealLog = true
				break
			}
		}
		if !foundAppealLog {
			t.Fatalf("expected Warn log with TOS_VIOLATION and appeal URL, got %d entries", len(hook.AllEntries()))
		}
	})

	t.Run("does not arm tos-pause-window on plain non-TOS 403", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		cfg := &config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		}
		cfg.Antigravity.TOSPauseWindow = "1h"
		exec := NewAntigravityExecutor(cfg)
		auth := &cliproxyauth.Auth{
			ID:       "auth-issue7820-plain-403",
			Provider: "antigravity",
		}
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SOME_OTHER_REASON"}]}}`)),
			}, nil
		}))

		exec.updateAntigravityCreditsBalance(ctx, auth, "token")

		if antigravityInTOSPause(cfg, auth) {
			t.Fatal("antigravityInTOSPause() = true after plain 403, want false")
		}
	})

	t.Run("bounds 403 response read and ignores oversized 403 body", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		const maxExpectedRead = antigravityLoadCodeAssistMaxResponseBytes + 1
		oversizedTOS := strings.TrimSuffix(strings.TrimSpace(antigravityTOSBody), "}") + `,"padding":"` + strings.Repeat("a", antigravityLoadCodeAssistMaxResponseBytes+1024) + `"}`
		body := &countingReadCloser{reader: strings.NewReader(oversizedTOS)}

		cfg := &config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		}
		cfg.Antigravity.TOSPauseWindow = "1h"
		exec := NewAntigravityExecutor(cfg)
		auth := &cliproxyauth.Auth{
			ID:       "auth-issue7820-oversized-403",
			Provider: "antigravity",
		}
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       body,
			}, nil
		}))

		exec.updateAntigravityCreditsBalance(ctx, auth, "token")

		if body.readN > maxExpectedRead {
			t.Fatalf("403 loadCodeAssist read %d bytes, want <= %d bytes", body.readN, maxExpectedRead)
		}
		if antigravityInTOSPause(cfg, auth) {
			t.Fatal("antigravityInTOSPause() = true for oversized 403 body, want false")
		}
	})

	t.Run("does not arm tos-pause-window when reading 403 body fails", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		cfg := &config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		}
		cfg.Antigravity.TOSPauseWindow = "1h"
		exec := NewAntigravityExecutor(cfg)
		auth := &cliproxyauth.Auth{
			ID:       "auth-issue7820-read-err-403",
			Provider: "antigravity",
		}
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       &errorAfterReadCloser{payload: []byte(antigravityTOSBody), err: errors.New("stream interrupted")},
			}, nil
		}))

		exec.updateAntigravityCreditsBalance(ctx, auth, "token")

		if antigravityInTOSPause(cfg, auth) {
			t.Fatal("antigravityInTOSPause() = true when 403 body read errored, want false")
		}
	})

	t.Run("does not arm tos-pause-window when a superseded queued task delivers late 403 TOS_VIOLATION", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		logger := log.StandardLogger()
		oldLevel := logger.GetLevel()
		oldHooks := logger.ReplaceHooks(make(log.LevelHooks))
		oldOutput := logger.Out
		logger.SetLevel(log.WarnLevel)
		logger.SetOutput(io.Discard)
		hook := logtest.NewLocal(logger)
		t.Cleanup(func() {
			logger.SetLevel(oldLevel)
			logger.ReplaceHooks(oldHooks)
			logger.SetOutput(oldOutput)
		})

		cfg := &config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		}
		cfg.Antigravity.TOSPauseWindow = "1h"
		exec := NewAntigravityExecutor(cfg)

		authID := "auth-issue7820-superseded-tos-403"
		t.Cleanup(func() {
			cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{})
		})

		oldStarted := make(chan struct{})
		releaseOld := make(chan struct{})
		var releaseOnce sync.Once
		t.Cleanup(func() { releaseOnce.Do(func() { close(releaseOld) }) })

		oldAuth := &cliproxyauth.Auth{
			ID:                authID,
			Provider:          "antigravity",
			RegistrationEpoch: 1,
		}
		oldCtx, cancelOld := context.WithCancel(context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			close(oldStarted)
			<-releaseOld
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(antigravityTOSBody)),
			}, nil
		})))
		defer cancelOld()

		exec.queueAntigravityCreditsRefresh(oldCtx, oldAuth, "old-token", 0)
		<-oldStarted

		rawState, ok := antigravityCreditsHintRefreshByID.Load(authID + "|" + cliproxyauth.AntigravityCreditsScope(oldAuth))
		if !ok {
			t.Fatal("expected queued credits refresh state to exist")
		}
		state := rawState.(*antigravityCreditsHintRefreshState)
		state.mu.Lock()
		oldTask := state.task
		state.mu.Unlock()
		if oldTask == nil {
			t.Fatal("expected old queued task to be active")
		}

		// Supersede the credential with a newer registration epoch whose refresh succeeds.
		newAuth := &cliproxyauth.Auth{
			ID:                authID,
			Provider:          "antigravity",
			RegistrationEpoch: 2,
		}
		newCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"paidTier":{"id":"pro","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"1500","minimumCreditAmountForUsage":"50"}]}}`)),
			}, nil
		}))
		exec.queueAntigravityCreditsRefresh(newCtx, newAuth, "new-token", 0)
		newStateValue, okNewState := antigravityCreditsHintRefreshByID.Load(authID + "|" + cliproxyauth.AntigravityCreditsScope(newAuth))
		if !okNewState {
			t.Fatal("expected replacement queued refresh state to exist")
		}
		newState := newStateValue.(*antigravityCreditsHintRefreshState)
		newState.mu.Lock()
		newTask := newState.task
		newState.mu.Unlock()
		if newTask != nil {
			<-newTask.done
		}

		// Release the stale task so it delivers a complete 403 TOS_VIOLATION after being superseded.
		releaseOnce.Do(func() { close(releaseOld) })
		<-oldTask.done

		if antigravityInTOSPause(cfg, newAuth) {
			t.Fatal("antigravityInTOSPause() = true after late 403 TOS_VIOLATION from superseded task, want false")
		}
		for _, entry := range hook.AllEntries() {
			if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "TOS_VIOLATION") {
				t.Fatalf("unexpected TOS_VIOLATION warning from superseded task: %q", entry.Message)
			}
		}
	})

	t.Run("arms tos-pause-window when active queued task receives 403 TOS_VIOLATION", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		cfg := &config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		}
		cfg.Antigravity.TOSPauseWindow = "1h"
		exec := NewAntigravityExecutor(cfg)

		authID := "auth-issue7820-active-queued-tos-403"
		started := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

		auth := &cliproxyauth.Auth{
			ID:                authID,
			Provider:          "antigravity",
			RegistrationEpoch: 1,
		}
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			close(started)
			<-release
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(antigravityTOSBody)),
			}, nil
		}))

		exec.queueAntigravityCreditsRefresh(ctx, auth, "token", 0)
		<-started

		rawState, ok := antigravityCreditsHintRefreshByID.Load(authID + "|" + cliproxyauth.AntigravityCreditsScope(auth))
		if !ok {
			t.Fatal("expected queued credits refresh state to exist")
		}
		state := rawState.(*antigravityCreditsHintRefreshState)
		state.mu.Lock()
		task := state.task
		state.mu.Unlock()
		if task == nil {
			t.Fatal("expected queued task to be active")
		}

		releaseOnce.Do(func() { close(release) })
		<-task.done

		if !antigravityInTOSPause(cfg, auth) {
			t.Fatal("antigravityInTOSPause() = false after active queued task 403 TOS_VIOLATION, want true")
		}
	})

	t.Run("does not arm tos-pause-window when direct call context is already canceled", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		cfg := &config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		}
		cfg.Antigravity.TOSPauseWindow = "1h"
		exec := NewAntigravityExecutor(cfg)
		auth := &cliproxyauth.Auth{
			ID:       "auth-issue7820-canceled-direct-403",
			Provider: "antigravity",
		}
		ctx, cancel := context.WithCancel(context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(antigravityTOSBody)),
			}, nil
		})))
		cancel()

		exec.updateAntigravityCreditsBalance(ctx, auth, "token")

		if antigravityInTOSPause(cfg, auth) {
			t.Fatal("antigravityInTOSPause() = true when direct call context was canceled, want false")
		}
	})
}
