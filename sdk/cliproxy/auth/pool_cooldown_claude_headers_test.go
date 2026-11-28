package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type headerCarrierTestError struct {
	headers http.Header
}

func (e headerCarrierTestError) Error() string        { return "upstream 429" }
func (e headerCarrierTestError) StatusCode() int      { return http.StatusTooManyRequests }
func (e headerCarrierTestError) Headers() http.Header { return e.headers }

func claudeFiveHourSignals() map[string]string {
	return map[string]string{
		"Anthropic-Ratelimit-Unified-Status":    "rejected",
		"Anthropic-Ratelimit-Unified-5h-Status": "rejected",
		"Anthropic-Ratelimit-Unified-Reset":     strconv.FormatInt(time.Now().Add(5*time.Hour).Unix(), 10),
	}
}

// lastAttemptHeaders are the headers of one credential's own 429: a 5h limit
// that resets later than the pool does.
func lastAttemptHeaders() http.Header {
	return http.Header{
		"Anthropic-Ratelimit-Unified-Status":    {"rejected"},
		"Anthropic-Ratelimit-Unified-5h-Status": {"rejected"},
		"Anthropic-Ratelimit-Unified-Reset":     {strconv.FormatInt(time.Now().Add(5*time.Hour).Unix(), 10)},
		"X-Upstream":                            {"kept"},
	}
}

func headersOf(err error) http.Header {
	var carrier interface{ Headers() http.Header }
	if !errors.As(err, &carrier) || carrier == nil {
		return nil
	}
	return carrier.Headers()
}

func TestPoolCooldownTerminalErrorDescribesClaudePool(t *testing.T) {
	deadline := time.Now().Add(time.Hour)
	err := &poolCooldownTerminalError{
		cause:          headerCarrierTestError{headers: lastAttemptHeaders()},
		deadline:       deadline,
		claudeCooldown: true,
		claudeSignals:  claudeFiveHourSignals(),
	}

	headers := headersOf(err)
	if got := headers.Get("Anthropic-Ratelimit-Unified-Status"); got != "rejected" {
		t.Fatalf("Anthropic-Ratelimit-Unified-Status = %q, want rejected", got)
	}
	reset, errReset := strconv.ParseInt(headers.Get("Anthropic-Ratelimit-Unified-Reset"), 10, 64)
	if errReset != nil || reset < deadline.Unix()-2 || reset > deadline.Unix()+2 {
		t.Fatalf("Anthropic-Ratelimit-Unified-Reset = %q, want the pool deadline %d, not the last credential's", headers.Get("Anthropic-Ratelimit-Unified-Reset"), deadline.Unix())
	}
	if got := headers.Get("X-Upstream"); got != "kept" {
		t.Fatalf("X-Upstream = %q, want the cause's other headers kept", got)
	}
}

func TestPoolCooldownTerminalErrorDropsLastAttemptUnifiedHeaders(t *testing.T) {
	// The pool is not entirely in a Claude usage-limit cooldown, so no usage
	// limit is described, and the last credential's own one must not leak.
	err := &poolCooldownTerminalError{
		cause:    headerCarrierTestError{headers: lastAttemptHeaders()},
		deadline: time.Now().Add(time.Minute),
	}
	headers := headersOf(err)
	if len(headers) != 1 || headers.Get("X-Upstream") != "kept" {
		t.Fatalf("headers = %v, want only the non-unified cause header", headers)
	}

	onlyUnified := &poolCooldownTerminalError{
		cause:    headerCarrierTestError{headers: http.Header{"Anthropic-Ratelimit-Unified-Status": {"rejected"}}},
		deadline: time.Now().Add(time.Minute),
	}
	if headers := headersOf(onlyUnified); headers != nil {
		t.Fatalf("headers = %v, want nil", headers)
	}
}

// markClaudeUsageLimit records an upstream 5h usage-limit 429 for authID the
// way the conductor does, so the credential carries Claude cooldown signals.
func markClaudeUsageLimit(t *testing.T, manager *Manager, authID, provider, model string, credentialScope bool) {
	t.Helper()
	retryDelay := 2 * time.Hour
	reset := strconv.FormatInt(time.Now().Add(retryDelay).Unix(), 10)
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{
		"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
		"Anthropic-Ratelimit-Unified-5h-Status":            {"rejected"},
		"Anthropic-Ratelimit-Unified-5h-Reset":             {reset},
		"Anthropic-Ratelimit-Unified-Representative-Claim": {"five_hour"},
		"Anthropic-Ratelimit-Unified-Reset":                {reset},
	})
	manager.MarkResult(ctx, Result{
		AuthID:          authID,
		Provider:        provider,
		Model:           model,
		Success:         false,
		CredentialScope: credentialScope,
		RetryAfter:      &retryDelay,
		Error:           &Error{Code: ErrorCodeCredentialQuota, Message: "rate_limit_error", HTTPStatus: http.StatusTooManyRequests},
	})
}

func newPoolHeadersManager(t *testing.T, model string, auths ...*Auth) *Manager {
	t.Helper()
	withQuotaCooldownEnabled(t)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	providers := map[string]bool{}
	for _, auth := range auths {
		if !providers[auth.Provider] {
			providers[auth.Provider] = true
			manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: auth.Provider})
		}
		authID := auth.ID
		registry.GetGlobalRegistry().RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", authID, errRegister)
		}
	}
	return manager
}

func TestPoolCooldownForModelDescribesClaudeLimitOnlyWhenEveryCandidateCools(t *testing.T) {
	t.Run("every candidate in a usage-limit cooldown", func(t *testing.T) {
		const model = "pool-headers-all-cooling"
		manager := newPoolHeadersManager(t, model,
			&Auth{ID: "pool-headers-all-a", Provider: "claude", Status: StatusActive},
			&Auth{ID: "pool-headers-all-b", Provider: "claude", Status: StatusActive},
		)
		markClaudeUsageLimit(t, manager, "pool-headers-all-a", "claude", model, true)
		markClaudeUsageLimit(t, manager, "pool-headers-all-b", "claude", model, true)

		pool := manager.poolCooldownForModelAt(context.Background(), []string{"claude"}, model, cliproxyexecutor.Options{}, time.Now())
		if !pool.ok || !pool.claudeCooldown || len(pool.claudeSignals) == 0 {
			t.Fatalf("pool = %+v, want a Claude usage-limit pool with signals", pool)
		}
	})

	t.Run("one candidate blocked for another reason", func(t *testing.T) {
		const model = "pool-headers-other-block"
		manager := newPoolHeadersManager(t, model,
			&Auth{ID: "pool-headers-other-a", Provider: "claude", Status: StatusActive},
			&Auth{ID: "pool-headers-other-b", Provider: "claude", Status: StatusActive},
		)
		markClaudeUsageLimit(t, manager, "pool-headers-other-a", "claude", model, true)
		// B carries a model-level usage limit, but a credential-wide retry
		// hint blocks it for another reason.
		markClaudeUsageLimit(t, manager, "pool-headers-other-b", "claude", model, false)
		manager.mu.Lock()
		manager.auths["pool-headers-other-b"].credentialRetryAfter = time.Now().Add(30 * time.Minute)
		manager.mu.Unlock()

		manager.mu.RLock()
		blocked, reason, _ := isAuthBlockedForModelForRequest(context.Background(), manager.auths["pool-headers-other-b"], model, time.Now())
		claudeB, _ := claudeUnifiedCooldownForAuth(manager.auths["pool-headers-other-b"], model, time.Now())
		manager.mu.RUnlock()
		if !blocked || reason == blockReasonCooldown || !claudeB {
			t.Fatalf("setup: B blocked=%v reason=%v claudeCooldown=%v, want blocked for another reason while holding Claude signals", blocked, reason, claudeB)
		}

		pool := manager.poolCooldownForModelAt(context.Background(), []string{"claude"}, model, cliproxyexecutor.Options{}, time.Now())
		if !pool.ok {
			t.Fatalf("pool = %+v, want a pool cooldown", pool)
		}
		if pool.claudeCooldown {
			t.Fatalf("pool = %+v, want no Claude usage limit while a candidate is not cooling down", pool)
		}
	})

	t.Run("mixed providers", func(t *testing.T) {
		const model = "pool-headers-mixed"
		manager := newPoolHeadersManager(t, model,
			&Auth{ID: "pool-headers-mixed-claude", Provider: "claude", Status: StatusActive},
			&Auth{ID: "pool-headers-mixed-codex", Provider: "codex", Status: StatusActive},
		)
		markClaudeUsageLimit(t, manager, "pool-headers-mixed-claude", "claude", model, true)
		markClaudeUsageLimit(t, manager, "pool-headers-mixed-codex", "codex", model, true)

		pool := manager.poolCooldownForModelAt(context.Background(), []string{"claude", "codex"}, model, cliproxyexecutor.Options{}, time.Now())
		if !pool.ok {
			t.Fatalf("pool = %+v, want a pool cooldown", pool)
		}
		if pool.claudeCooldown {
			t.Fatalf("pool = %+v, want no Claude usage limit for a mixed-provider pool", pool)
		}
	})
}
