package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravityCreditsRemovedBalanceDoesNotResurrect(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	authID := fmt.Sprintf("auth-issue8158-local-%d", time.Now().UnixNano())
	auth := &cliproxyauth.Auth{ID: authID}
	t.Cleanup(func() {
		cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{})
	})

	var step atomic.Int32
	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"1000","minimumCreditAmountForUsage":"10"}]}}`
		if step.Load() > 0 {
			body = `{"paidTier":{"id":"g1-pro-tier","availableCredits":[]}}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}))

	// Step 1: Cache a positive GOOGLE_ONE_AI balance.
	exec.updateAntigravityCreditsBalanceForTask(ctx, auth, "token", nil)
	cacheKey := newAntigravityCreditsCacheKey(auth.ID, cliproxyauth.AntigravityCreditsScope(auth))
	if _, found := antigravityCreditsBalanceByAuth.Load(cacheKey); !found {
		t.Fatal("expected positive credits balance to be cached in antigravityCreditsBalanceByAuth")
	}

	// Step 2: Credits are removed from loadCodeAssist response.
	step.Store(1)
	exec.updateAntigravityCreditsBalanceForTask(ctx, auth, "token", nil)

	if _, found := antigravityCreditsBalanceByAuth.Load(cacheKey); found {
		t.Fatal("stale positive credits balance remained in antigravityCreditsBalanceByAuth after credits removal")
	}
	hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(auth)
	if !ok || !hint.Known || hint.Available {
		t.Fatalf("hint after removal = (%#v, %v), want Known=true Available=false", hint, ok)
	}

	// Step 3: After the unavailable hint is cleared, checking credits must not resurrect the stale positive balance.
	cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{})
	if _, errCredits := antigravityAuthHasCreditsRequired(ctx, auth); errCredits != nil {
		t.Fatalf("antigravityAuthHasCreditsRequired() error = %v", errCredits)
	}
	resurrected, okAfter := cliproxyauth.GetAntigravityCreditsHintForAuth(auth)
	if okAfter && resurrected.Known && resurrected.Available {
		t.Fatalf("stale positive balance resurrected into fresh hint: %#v", resurrected)
	}
}

func TestAntigravityCreditsRemovedBalanceDoesNotResurrectHomeKV(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)

	authID := fmt.Sprintf("auth-issue8158-home-%d", time.Now().UnixNano())
	auth := &cliproxyauth.Auth{ID: authID}

	var step atomic.Int32
	exec := NewAntigravityExecutor(&config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"1000","minimumCreditAmountForUsage":"10"}]}}`
		if step.Load() > 0 {
			body = `{"paidTier":{"id":"g1-pro-tier","availableCredits":[]}}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}))

	balanceKey := antigravityCreditsBalanceKey(auth.ID, cliproxyauth.AntigravityCreditsScope(auth))

	// Step 1: Cache a positive GOOGLE_ONE_AI balance in Home KV.
	exec.updateAntigravityCreditsBalanceForTask(ctx, auth, "token", nil)
	if _, found := client.values[balanceKey]; !found {
		t.Fatalf("expected positive credits balance in Home KV key %s", balanceKey)
	}

	// Step 2: Credits are removed from loadCodeAssist response.
	step.Store(1)
	exec.updateAntigravityCreditsBalanceForTask(ctx, auth, "token", nil)

	if client.delCount == 0 {
		t.Fatal("expected KVDel to be called when GOOGLE_ONE_AI credits are absent")
	}
	if _, found := client.values[balanceKey]; found {
		t.Fatalf("stale positive credits balance remained in Home KV key %s after credits removal", balanceKey)
	}
	hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(auth)
	if !ok || !hint.Known || hint.Available {
		t.Fatalf("hint after removal = (%#v, %v), want Known=true Available=false", hint, ok)
	}

	// Step 3: After the unavailable hint is cleared, checking credits must not resurrect the stale Home KV balance.
	cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{})
	if _, errCredits := antigravityAuthHasCreditsRequired(ctx, auth); errCredits != nil {
		t.Fatalf("antigravityAuthHasCreditsRequired() error = %v", errCredits)
	}
	resurrected, okAfter := cliproxyauth.GetAntigravityCreditsHintForAuth(auth)
	if okAfter && resurrected.Known && resurrected.Available {
		t.Fatalf("stale Home KV positive balance resurrected into fresh hint: %#v", resurrected)
	}
}

func TestAntigravityCreditsClearFailureStateDeletesCachedBalance(t *testing.T) {
	t.Run("standalone", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		authID := fmt.Sprintf("auth-issue8158-clear-local-%d", time.Now().UnixNano())
		auth := &cliproxyauth.Auth{ID: authID}
		t.Cleanup(func() {
			cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{})
		})

		storeAntigravityCreditsBalanceBestEffort(auth, antigravityCreditsBalance{
			CreditAmount:    1000,
			MinCreditAmount: 10,
			PaidTierID:      "g1-pro-tier",
			Known:           true,
		})
		cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{
			Known:     true,
			Available: false,
			UpdatedAt: time.Now().Add(-cliproxyauth.AntigravityCreditsHintRefreshInterval - time.Minute),
		})

		clearAntigravityCreditsFailureState(auth)

		cacheKey := newAntigravityCreditsCacheKey(auth.ID, cliproxyauth.AntigravityCreditsScope(auth))
		if _, found := antigravityCreditsBalanceByAuth.Load(cacheKey); found {
			t.Fatal("cached balance remained in antigravityCreditsBalanceByAuth after clearAntigravityCreditsFailureState reset hint")
		}
		if _, errCredits := antigravityAuthHasCreditsRequired(context.Background(), auth); errCredits != nil {
			t.Fatalf("antigravityAuthHasCreditsRequired() error = %v", errCredits)
		}
		hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(auth)
		if ok && hint.Known {
			t.Fatalf("stale cached balance resurrected into hint after clearAntigravityCreditsFailureState: %#v", hint)
		}
	})

	t.Run("home kv", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		client := newFakeAntigravityKVClient()
		useFakeAntigravityKVClient(t, client, true, nil)

		authID := fmt.Sprintf("auth-issue8158-clear-home-%d", time.Now().UnixNano())
		auth := &cliproxyauth.Auth{ID: authID}

		storeAntigravityCreditsBalanceBestEffort(auth, antigravityCreditsBalance{
			CreditAmount:    1000,
			MinCreditAmount: 10,
			PaidTierID:      "g1-pro-tier",
			Known:           true,
		})
		cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{
			Known:     true,
			Available: false,
			UpdatedAt: time.Now().Add(-cliproxyauth.AntigravityCreditsHintRefreshInterval - time.Minute),
		})

		clearAntigravityCreditsFailureState(auth)

		balanceKey := antigravityCreditsBalanceKey(auth.ID, cliproxyauth.AntigravityCreditsScope(auth))
		if raw, found := client.values[balanceKey]; found && !isClearedAntigravityCreditsBalancePayload(raw) {
			t.Fatalf("cached balance remained active in Home KV key %s after clearAntigravityCreditsFailureState reset hint: %s", balanceKey, string(raw))
		}
		if _, errCredits := antigravityAuthHasCreditsRequired(context.Background(), auth); errCredits != nil {
			t.Fatalf("antigravityAuthHasCreditsRequired() error = %v", errCredits)
		}
		hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(auth)
		if ok && hint.Known {
			t.Fatalf("stale Home KV balance resurrected into hint after clearAntigravityCreditsFailureState: %#v", hint)
		}
	})

	t.Run("after permanent failure reset", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		authID := fmt.Sprintf("auth-issue8158-clear-perm-%d", time.Now().UnixNano())
		auth := &cliproxyauth.Auth{ID: authID}
		t.Cleanup(func() {
			cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{})
		})

		markAntigravityCreditsPermanentlyDisabled(auth)
		clearAntigravityCreditsFailureState(auth)

		cacheKey := newAntigravityCreditsCacheKey(auth.ID, cliproxyauth.AntigravityCreditsScope(auth))
		if _, found := antigravityCreditsBalanceByAuth.Load(cacheKey); found {
			t.Fatal("exhausted balance remained in antigravityCreditsBalanceByAuth after permanent failure clear")
		}
		hasCredits, errCredits := antigravityAuthHasCreditsRequired(context.Background(), auth)
		if errCredits != nil {
			t.Fatalf("antigravityAuthHasCreditsRequired() error = %v", errCredits)
		}
		if !hasCredits {
			t.Fatal("antigravityAuthHasCreditsRequired() = false after failure clear, want optimistic true")
		}
		if hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(auth); ok && hint.Known {
			t.Fatalf("exhausted balance resurrected into fresh hint after permanent failure clear: %#v", hint)
		}
	})
}

type syncHookAntigravityKVClient struct {
	mu         sync.Mutex
	values     map[string][]byte
	onGet      func(key string)
	getErrFor  func(key string) error
	setErrFor  func(key string) error
	onAfterCAS func(key string, swapped bool)
	casErrFor  func(key string) error
}

func useSyncHookAntigravityKVClient(t *testing.T, client *syncHookAntigravityKVClient) {
	t.Helper()
	previous := currentAntigravityKVClient
	currentAntigravityKVClient = func() (antigravityKVClient, bool, error) {
		return client, true, nil
	}
	kvstore.RegisterHomeProvider(func() (kvstore.KVClient, bool, error) {
		return client, true, nil
	})
	t.Cleanup(func() {
		currentAntigravityKVClient = previous
		kvstore.RegisterHomeProvider(func() (kvstore.KVClient, bool, error) { return nil, false, nil })
	})
}

func (c *syncHookAntigravityKVClient) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	getErrFor := c.getErrFor
	if getErrFor != nil {
		if errGet := getErrFor(key); errGet != nil {
			c.mu.Unlock()
			return nil, false, errGet
		}
	}
	value, ok := c.values[key]
	var copied []byte
	if ok {
		copied = append([]byte(nil), value...)
	}
	onGet := c.onGet
	c.mu.Unlock()
	if onGet != nil {
		onGet(key)
	}
	return copied, ok, nil
}

func (c *syncHookAntigravityKVClient) KVSet(_ context.Context, key string, value []byte, _ homekv.KVSetOptions) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.setErrFor != nil {
		if errSet := c.setErrFor(key); errSet != nil {
			return false, errSet
		}
	}
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *syncHookAntigravityKVClient) KVSetNX(_ context.Context, key string, value []byte, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.values[key]; ok {
		return false, nil
	}
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *syncHookAntigravityKVClient) KVCompareAndSwap(_ context.Context, key string, expected []byte, expectedExists bool, value []byte, _ time.Duration) (bool, error) {
	c.mu.Lock()
	casErrFor := c.casErrFor
	onAfterCAS := c.onAfterCAS
	if casErrFor != nil {
		if errCAS := casErrFor(key); errCAS != nil {
			c.mu.Unlock()
			return false, errCAS
		}
	}
	current, ok := c.values[key]
	swapped := false
	if ok == expectedExists && (!expectedExists || string(current) == string(expected)) {
		c.values[key] = append([]byte(nil), value...)
		swapped = true
	}
	c.mu.Unlock()
	if onAfterCAS != nil {
		onAfterCAS(key, swapped)
	}
	return swapped, nil
}

func (c *syncHookAntigravityKVClient) KVDel(_ context.Context, keys ...string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var deleted int64
	for _, key := range keys {
		if _, ok := c.values[key]; ok {
			delete(c.values, key)
			deleted++
		}
	}
	return deleted, nil
}

func (c *syncHookAntigravityKVClient) KVExpire(context.Context, string, time.Duration) (bool, error) {
	return false, nil
}

func (c *syncHookAntigravityKVClient) KVTTL(context.Context, string) (time.Duration, bool, error) {
	return 0, false, nil
}

func (c *syncHookAntigravityKVClient) KVIncrBy(context.Context, string, int64) (int64, error) {
	return 0, nil
}

func (c *syncHookAntigravityKVClient) KVMGet(context.Context, ...string) ([][]byte, []bool, error) {
	return nil, nil, nil
}

func (c *syncHookAntigravityKVClient) KVMSet(context.Context, map[string][]byte) error {
	return nil
}

func TestAntigravityCreditsClearFailureStateHomeKVDeleteErrorPreservesUnavailableHint(t *testing.T) {
	t.Run("expired unavailable hint", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		client := &syncHookAntigravityKVClient{values: make(map[string][]byte)}
		useSyncHookAntigravityKVClient(t, client)

		authID := fmt.Sprintf("auth-issue8158-delerr-expired-%d", time.Now().UnixNano())
		auth := &cliproxyauth.Auth{ID: authID}
		balanceKey := antigravityCreditsBalanceKey(auth.ID, cliproxyauth.AntigravityCreditsScope(auth))

		storeAntigravityCreditsBalanceBestEffort(auth, antigravityCreditsBalance{
			CreditAmount:    1000,
			MinCreditAmount: 10,
			PaidTierID:      "g1-pro-tier",
			Known:           true,
		})
		cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{
			Known:     true,
			Available: false,
			UpdatedAt: time.Now().Add(-cliproxyauth.AntigravityCreditsHintRefreshInterval - time.Minute),
		})

		client.mu.Lock()
		client.casErrFor = func(key string) error {
			if key == balanceKey {
				return errors.New("home kv balance invalidation failed")
			}
			return nil
		}
		client.mu.Unlock()
		clearAntigravityCreditsFailureState(auth)

		hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(auth)
		if !ok || !hint.Known || hint.Available {
			t.Fatalf("hint after failed Home KV balance invalidation = (%#v, %v), want Known=true Available=false preserved", hint, ok)
		}
		hasCredits, errCredits := antigravityAuthHasCreditsRequired(context.Background(), auth)
		if errCredits != nil {
			t.Fatalf("antigravityAuthHasCreditsRequired() error = %v", errCredits)
		}
		if hasCredits {
			t.Fatal("antigravityAuthHasCreditsRequired() = true after failed Home KV balance invalidation, want false")
		}

		// Retry once Home KV balance invalidation succeeds.
		client.mu.Lock()
		client.casErrFor = nil
		client.mu.Unlock()
		clearAntigravityCreditsFailureState(auth)

		client.mu.Lock()
		rawAfterRetry, foundAfterRetry := client.values[balanceKey]
		client.mu.Unlock()
		if foundAfterRetry && !isClearedAntigravityCreditsBalancePayload(rawAfterRetry) {
			t.Fatalf("cached balance remained active in Home KV key %s after retry: %s", balanceKey, string(rawAfterRetry))
		}
		hintAfterRetry, okAfterRetry := cliproxyauth.GetAntigravityCreditsHintForAuth(auth)
		if okAfterRetry && hintAfterRetry.Known {
			t.Fatalf("hint after successful retry = %#v, want Known=false", hintAfterRetry)
		}
	})

	t.Run("permanent failure retains retry state on invalidation error", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		client := &syncHookAntigravityKVClient{values: make(map[string][]byte)}
		useSyncHookAntigravityKVClient(t, client)

		authID := fmt.Sprintf("auth-issue8158-delerr-perm-%d", time.Now().UnixNano())
		auth := &cliproxyauth.Auth{ID: authID}
		balanceKey := antigravityCreditsBalanceKey(auth.ID, cliproxyauth.AntigravityCreditsScope(auth))

		markAntigravityCreditsPermanentlyDisabled(auth)
		// Overwrite Home KV balance with a stale positive entry to prove it cannot resurrect.
		storeAntigravityCreditsBalanceBestEffort(auth, antigravityCreditsBalance{
			CreditAmount:    1000,
			MinCreditAmount: 10,
			PaidTierID:      "g1-pro-tier",
			Known:           true,
		})

		client.mu.Lock()
		client.casErrFor = func(key string) error {
			if key == balanceKey {
				return errors.New("home kv balance invalidation failed")
			}
			return nil
		}
		client.mu.Unlock()
		clearAntigravityCreditsFailureState(auth)

		hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(auth)
		if !ok || !hint.Known || hint.Available {
			t.Fatalf("hint after failed Home KV balance invalidation = (%#v, %v), want Known=true Available=false preserved", hint, ok)
		}
		cacheKey := newAntigravityCreditsCacheKey(auth.ID, cliproxyauth.AntigravityCreditsScope(auth))
		if _, stillFailed := antigravityCreditsFailureByAuth.Load(cacheKey); !stillFailed {
			t.Fatal("failure state was cleared despite Home KV balance invalidation error; retry opportunity lost")
		}

		// Retry once Home KV balance invalidation succeeds.
		client.mu.Lock()
		client.casErrFor = nil
		client.mu.Unlock()
		clearAntigravityCreditsFailureState(auth)

		client.mu.Lock()
		rawAfterRetry, foundAfterRetry := client.values[balanceKey]
		client.mu.Unlock()
		if foundAfterRetry && !isClearedAntigravityCreditsBalancePayload(rawAfterRetry) {
			t.Fatalf("cached balance remained active in Home KV key %s after retry: %s", balanceKey, string(rawAfterRetry))
		}
		if _, stillFailed := antigravityCreditsFailureByAuth.Load(cacheKey); stillFailed {
			t.Fatal("failure state remained after successful retry")
		}
		hintAfterRetry, okAfterRetry := cliproxyauth.GetAntigravityCreditsHintForAuth(auth)
		if okAfterRetry && hintAfterRetry.Known {
			t.Fatalf("hint after successful retry = %#v, want Known=false", hintAfterRetry)
		}
	})

	t.Run("unsupported CAS does not blindly delete balance or clear hint", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		client := &syncHookAntigravityKVClient{values: make(map[string][]byte)}
		useSyncHookAntigravityKVClient(t, client)

		authID := fmt.Sprintf("auth-issue8158-cas-unsupported-%d", time.Now().UnixNano())
		auth := &cliproxyauth.Auth{ID: authID}
		balanceKey := antigravityCreditsBalanceKey(auth.ID, cliproxyauth.AntigravityCreditsScope(auth))

		storeAntigravityCreditsBalanceBestEffort(auth, antigravityCreditsBalance{
			CreditAmount:    1000,
			MinCreditAmount: 10,
			PaidTierID:      "g1-pro-tier",
			Known:           true,
		})
		cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{
			Known:     true,
			Available: false,
			UpdatedAt: time.Now().Add(-cliproxyauth.AntigravityCreditsHintRefreshInterval - time.Minute),
		})

		client.mu.Lock()
		client.casErrFor = func(string) error { return kvstore.ErrCompareAndSwapUnsupported }
		client.mu.Unlock()
		clearAntigravityCreditsFailureState(auth)

		client.mu.Lock()
		rawBal, foundBal := client.values[balanceKey]
		client.mu.Unlock()
		if !foundBal || isClearedAntigravityCreditsBalancePayload(rawBal) {
			t.Fatalf("balance in Home KV key %s was blindly modified when CAS is unsupported", balanceKey)
		}
		hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(auth)
		if !ok || !hint.Known || hint.Available {
			t.Fatalf("hint when CAS is unsupported = (%#v, %v), want Known=true Available=false preserved", hint, ok)
		}
	})

	t.Run("transient hint recheck error after CAS mismatch retains failure state", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		client := &syncHookAntigravityKVClient{values: make(map[string][]byte)}
		useSyncHookAntigravityKVClient(t, client)

		authID := fmt.Sprintf("auth-issue8158-recheck-err-%d", time.Now().UnixNano())
		auth := &cliproxyauth.Auth{ID: authID}
		balanceKey := antigravityCreditsBalanceKey(auth.ID, cliproxyauth.AntigravityCreditsScope(auth))
		hintKey := cliproxyauth.AntigravityCreditsHintKVKey(auth)

		markAntigravityCreditsPermanentlyDisabled(auth)
		storeAntigravityCreditsBalanceBestEffort(auth, antigravityCreditsBalance{
			CreditAmount:    1000,
			MinCreditAmount: 10,
			PaidTierID:      "g1-pro-tier",
			Known:           true,
		})

		var hintGetCalls atomic.Int32
		var injectedMismatch sync.Once
		client.mu.Lock()
		// Force balance CAS mismatch by swapping balanceKey right after guard CAS,
		// then fail the subsequent hintKey recheck read.
		client.onAfterCAS = func(key string, swapped bool) {
			if swapped && key == hintKey {
				injectedMismatch.Do(func() {
					storeAntigravityCreditsBalanceBestEffort(auth, antigravityCreditsBalance{
						CreditAmount:    1000,
						MinCreditAmount: 10,
						PaidTierID:      "g1-pro-tier",
						Known:           true,
					})
				})
			}
		}
		client.getErrFor = func(key string) error {
			if key == hintKey && hintGetCalls.Add(1) > 1 {
				return errors.New("transient hint recheck error")
			}
			return nil
		}
		client.mu.Unlock()

		clearAntigravityCreditsFailureState(auth)

		cacheKey := newAntigravityCreditsCacheKey(auth.ID, cliproxyauth.AntigravityCreditsScope(auth))
		if _, stillFailed := antigravityCreditsFailureByAuth.Load(cacheKey); !stillFailed {
			t.Fatal("failure state was cleared despite transient hint recheck read error")
		}

		// Once transient read error clears, retry succeeds and clears both balance and hint.
		client.mu.Lock()
		client.onAfterCAS = nil
		client.getErrFor = nil
		client.mu.Unlock()

		clearAntigravityCreditsFailureState(auth)

		client.mu.Lock()
		rawAfterRetry, foundAfterRetry := client.values[balanceKey]
		client.mu.Unlock()
		if foundAfterRetry && !isClearedAntigravityCreditsBalancePayload(rawAfterRetry) {
			t.Fatalf("cached balance remained active in Home KV key %s after retry: %s", balanceKey, string(rawAfterRetry))
		}
		if _, stillFailed := antigravityCreditsFailureByAuth.Load(cacheKey); stillFailed {
			t.Fatal("failure state remained after successful retry")
		}
		hintAfterRetry, okAfterRetry := cliproxyauth.GetAntigravityCreditsHintForAuth(auth)
		if okAfterRetry && hintAfterRetry.Known {
			t.Fatalf("hint after successful retry = %#v, want Known=false", hintAfterRetry)
		}
	})
}

func TestAntigravityCreditsClearFailureStateHomeKVConcurrentRefreshDoesNotWipeFreshBalance(t *testing.T) {
	runCase := func(t *testing.T, pauseAfterGuardCAS bool, refreshedAmount float64) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		client := &syncHookAntigravityKVClient{values: make(map[string][]byte)}
		useSyncHookAntigravityKVClient(t, client)

		authID := fmt.Sprintf("auth-issue8158-concurrent-refresh-%v-%.0f-%d", pauseAfterGuardCAS, refreshedAmount, time.Now().UnixNano())
		auth := &cliproxyauth.Auth{ID: authID}

		storeAntigravityCreditsBalanceBestEffort(auth, antigravityCreditsBalance{
			CreditAmount:    1000,
			MinCreditAmount: 10,
			PaidTierID:      "g1-pro-tier",
			Known:           true,
		})
		cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{
			Known:     true,
			Available: false,
			UpdatedAt: time.Now().Add(-cliproxyauth.AntigravityCreditsHintRefreshInterval - time.Minute),
		})

		cleanerPaused := make(chan struct{})
		refreshPublished := make(chan struct{})
		var hookedOnce sync.Once
		var balanceCASWrote atomic.Bool

		client.mu.Lock()
		if pauseAfterGuardCAS {
			client.onAfterCAS = func(key string, swapped bool) {
				if swapped && strings.Contains(key, ":credits-balance:") {
					balanceCASWrote.Store(true)
				}
				if swapped && strings.Contains(key, ":credits-hint:") {
					hookedOnce.Do(func() {
						close(cleanerPaused)
						<-refreshPublished
					})
				}
			}
		} else {
			client.onGet = func(key string) {
				if strings.Contains(key, ":credits-hint:") {
					hookedOnce.Do(func() {
						close(cleanerPaused)
						<-refreshPublished
					})
				}
			}
		}
		client.mu.Unlock()

		cleanerDone := make(chan struct{})
		go func() {
			defer close(cleanerDone)
			clearAntigravityCreditsFailureState(auth)
		}()

		<-cleanerPaused

		exec := NewAntigravityExecutor(&config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			body := fmt.Sprintf(`{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"%.0f","minimumCreditAmountForUsage":"10"}]}}`, refreshedAmount)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}))
		exec.updateAntigravityCreditsBalanceForTask(ctx, auth, "token", nil)
		close(refreshPublished)

		<-cleanerDone

		if balanceCASWrote.Load() {
			t.Fatal("balance CAS unexpectedly overwrote freshly published balance payload")
		}
		balanceKey := antigravityCreditsBalanceKey(auth.ID, cliproxyauth.AntigravityCreditsScope(auth))
		client.mu.Lock()
		rawBal, foundBal := client.values[balanceKey]
		client.mu.Unlock()
		if !foundBal || isClearedAntigravityCreditsBalancePayload(rawBal) {
			t.Fatalf("fresh positive balance in Home KV key %s was wiped by stale clearAntigravityCreditsFailureState: found=%v raw=%s", balanceKey, foundBal, string(rawBal))
		}
		var storedBal antigravityCreditsBalance
		if errUnmarshal := json.Unmarshal(rawBal, &storedBal); errUnmarshal != nil {
			t.Fatalf("unmarshal Home KV balance: %v", errUnmarshal)
		}
		if !storedBal.Known || storedBal.CreditAmount != refreshedAmount || storedBal.MinCreditAmount != 10 || storedBal.PaidTierID != "g1-pro-tier" {
			t.Fatalf("Home KV balance = %#v, want CreditAmount=%.0f MinCreditAmount=10 PaidTierID=g1-pro-tier Known=true", storedBal, refreshedAmount)
		}

		hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(auth)
		if !ok || !hint.Known || !hint.Available || hint.CreditAmount != refreshedAmount {
			t.Fatalf("fresh available hint was overwritten by stale clearAntigravityCreditsFailureState: (%#v, %v)", hint, ok)
		}
	}

	t.Run("refresh completes before guard CAS", func(t *testing.T) {
		runCase(t, false, 500)
	})
	t.Run("refresh completes after guard CAS before balance invalidation with changed amount", func(t *testing.T) {
		runCase(t, true, 500)
	})
	t.Run("refresh completes after guard CAS before balance invalidation with identical amount", func(t *testing.T) {
		runCase(t, true, 1000)
	})

	t.Run("rolling upgrade unversioned legacy balance with identical amount", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		client := &syncHookAntigravityKVClient{values: make(map[string][]byte)}
		useSyncHookAntigravityKVClient(t, client)

		authID := fmt.Sprintf("auth-issue8158-concurrent-legacy-%d", time.Now().UnixNano())
		auth := &cliproxyauth.Auth{ID: authID}
		balanceKey := antigravityCreditsBalanceKey(auth.ID, cliproxyauth.AntigravityCreditsScope(auth))
		legacyRaw := mustAntigravityJSON(t, antigravityCreditsBalance{
			CreditAmount:    1000,
			MinCreditAmount: 10,
			PaidTierID:      "g1-pro-tier",
			Known:           true,
		})

		client.mu.Lock()
		client.values[balanceKey] = append([]byte(nil), legacyRaw...)
		client.mu.Unlock()
		cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{
			Known:     true,
			Available: false,
			UpdatedAt: time.Now().Add(-cliproxyauth.AntigravityCreditsHintRefreshInterval - time.Minute),
		})

		cleanerPaused := make(chan struct{})
		refreshPublished := make(chan struct{})
		var hookedOnce sync.Once
		var balanceCASWrote atomic.Bool

		client.mu.Lock()
		client.onGet = func(key string) {
			if key == balanceKey {
				hookedOnce.Do(func() {
					close(cleanerPaused)
					<-refreshPublished
				})
			}
		}
		client.onAfterCAS = func(key string, swapped bool) {
			if swapped && key == balanceKey {
				balanceCASWrote.Store(true)
			}
		}
		client.mu.Unlock()

		cleanerDone := make(chan struct{})
		go func() {
			defer close(cleanerDone)
			clearAntigravityCreditsFailureState(auth)
		}()

		<-cleanerPaused

		// Simulate a pre-upgrade Home KV writer republishing the identical unversioned balance and fresh available hint.
		client.mu.Lock()
		client.values[balanceKey] = append([]byte(nil), legacyRaw...)
		client.mu.Unlock()
		cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{
			Known:           true,
			Available:       true,
			CreditAmount:    1000,
			MinCreditAmount: 10,
			PaidTierID:      "g1-pro-tier",
			UpdatedAt:       time.Now(),
		})
		close(refreshPublished)

		<-cleanerDone

		if balanceCASWrote.Load() {
			t.Fatal("balance CAS unexpectedly overwrote unversioned legacy balance payload")
		}
		client.mu.Lock()
		rawBal, foundBal := client.values[balanceKey]
		client.mu.Unlock()
		if !foundBal || isClearedAntigravityCreditsBalancePayload(rawBal) {
			t.Fatalf("legacy positive balance in Home KV key %s was wiped: found=%v raw=%s", balanceKey, foundBal, string(rawBal))
		}
		var storedBal antigravityCreditsBalance
		if errUnmarshal := json.Unmarshal(rawBal, &storedBal); errUnmarshal != nil {
			t.Fatalf("unmarshal Home KV balance: %v", errUnmarshal)
		}
		if !storedBal.Known || storedBal.CreditAmount != 1000 || storedBal.MinCreditAmount != 10 || storedBal.PaidTierID != "g1-pro-tier" {
			t.Fatalf("Home KV balance = %#v, want CreditAmount=1000 MinCreditAmount=10 PaidTierID=g1-pro-tier Known=true", storedBal)
		}
		hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(auth)
		if !ok || !hint.Known || !hint.Available || hint.CreditAmount != 1000 {
			t.Fatalf("fresh available hint from legacy writer was overwritten: (%#v, %v)", hint, ok)
		}
	})

	t.Run("refresh skips balance write when hint write fails in Home KV", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		client := &syncHookAntigravityKVClient{values: make(map[string][]byte)}
		useSyncHookAntigravityKVClient(t, client)

		authID := fmt.Sprintf("auth-issue8158-hint-write-fail-%d", time.Now().UnixNano())
		auth := &cliproxyauth.Auth{ID: authID}
		balanceKey := antigravityCreditsBalanceKey(auth.ID, cliproxyauth.AntigravityCreditsScope(auth))
		hintKey := cliproxyauth.AntigravityCreditsHintKVKey(auth)

		cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{
			Known:     true,
			Available: false,
			UpdatedAt: time.Now().Add(-cliproxyauth.AntigravityCreditsHintRefreshInterval - time.Minute),
		})

		client.mu.Lock()
		client.setErrFor = func(key string) error {
			if key == hintKey {
				return errors.New("home kv hint set failed")
			}
			return nil
		}
		client.mu.Unlock()

		exec := NewAntigravityExecutor(&config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			body := `{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"500","minimumCreditAmountForUsage":"10"}]}}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}))
		exec.updateAntigravityCreditsBalanceForTask(ctx, auth, "token", nil)

		client.mu.Lock()
		_, foundBal := client.values[balanceKey]
		client.mu.Unlock()
		if foundBal {
			t.Fatalf("balanceKey %s was written even though hintKey write failed", balanceKey)
		}
	})
}
