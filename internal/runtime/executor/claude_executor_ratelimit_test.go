package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type retryAfterProvider interface {
	RetryAfter() *time.Duration
}

func TestClaudeExecutor_HonorsAnthropicRateLimitHeaders_Execute(t *testing.T) {
	now := time.Now()
	sevenDayReset := now.Add(7 * 24 * time.Hour).Unix()
	fiveHourReset := now.Add(5 * time.Hour).Unix()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(fiveHourReset, 10))
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(sevenDayReset, 10))
		w.Header().Set("Anthropic-Ratelimit-Unified-Representative-Claim", "seven_day")
		w.Header().Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(sevenDayReset, 10))
		w.Header().Set("Retry-After", strconv.FormatInt(7*24*3600, 10))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Number of requests has exceeded your 7-day rate limit."}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "claude-auth-1",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err == nil {
		t.Fatal("expected error from Execute, got nil")
	}

	var rap retryAfterProvider
	if !errors.As(err, &rap) || rap == nil {
		t.Fatalf("expected error %T to implement RetryAfter() *time.Duration", err)
	}

	retryAfter := rap.RetryAfter()
	if retryAfter == nil {
		t.Fatalf("expected non-nil RetryAfter, got nil")
	}

	// Should be at least 7 days (reported reset) and at most 7 days + 35s (fuzz upper bound).
	minExpected := 7*24*time.Hour - 5*time.Second
	maxExpected := 7*24*time.Hour + 35*time.Second
	if *retryAfter < minExpected || *retryAfter > maxExpected {
		t.Fatalf("RetryAfter = %v, want between %v and %v", *retryAfter, minExpected, maxExpected)
	}

	// Verify one-time fuzz stability: repeat calls return exact same value
	if second := rap.RetryAfter(); second == nil || *second != *retryAfter {
		t.Fatalf("RetryAfter changed across calls: %v vs %v", *second, *retryAfter)
	}
}

func TestClaudeExecutor_HonorsAnthropicRateLimitHeaders_ExecuteStream(t *testing.T) {
	now := time.Now()
	fiveHourReset := now.Add(5 * time.Hour).Unix()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(fiveHourReset, 10))
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"5-hour limit exceeded."}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "claude-auth-1",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	_, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err == nil {
		t.Fatal("expected error from ExecuteStream, got nil")
	}

	var rap retryAfterProvider
	if !errors.As(err, &rap) || rap == nil {
		t.Fatalf("expected error %T to implement RetryAfter() *time.Duration", err)
	}

	retryAfter := rap.RetryAfter()
	if retryAfter == nil {
		t.Fatalf("expected non-nil RetryAfter, got nil")
	}

	minExpected := 5*time.Hour - 5*time.Second
	maxExpected := 5*time.Hour + 35*time.Second
	if *retryAfter < minExpected || *retryAfter > maxExpected {
		t.Fatalf("RetryAfter = %v, want between %v and %v (5h window)", *retryAfter, minExpected, maxExpected)
	}
}

func TestClaudeExecutor_RateLimit_BothRejectedUsesLongest(t *testing.T) {
	now := time.Now()
	fiveHourReset := now.Add(5 * time.Hour).Unix()
	sevenDayReset := now.Add(7 * 24 * time.Hour).Unix()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(fiveHourReset, 10))
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(sevenDayReset, 10))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Both limits exceeded."}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "claude-auth-1",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var rap retryAfterProvider
	if !errors.As(err, &rap) || rap == nil {
		t.Fatalf("expected error %T to implement RetryAfter() *time.Duration", err)
	}

	retryAfter := rap.RetryAfter()
	if retryAfter == nil {
		t.Fatalf("expected non-nil RetryAfter, got nil")
	}

	minExpected := 7*24*time.Hour - 5*time.Second
	maxExpected := 7*24*time.Hour + 35*time.Second
	if *retryAfter < minExpected || *retryAfter > maxExpected {
		t.Fatalf("RetryAfter = %v, want between %v and %v", *retryAfter, minExpected, maxExpected)
	}
}

func TestClaudeExecutor_RateLimit_CountTokensHonorsRateLimitReset(t *testing.T) {
	now := time.Now()
	sevenDayReset := now.Add(7 * 24 * time.Hour).Unix()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(sevenDayReset, 10))
		w.Header().Set("Retry-After", "604800")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"7-day rate limit exceeded."}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "claude-auth-1",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	_, err := executor.countTokensUpstream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err == nil {
		t.Fatal("expected error from CountTokens, got nil")
	}

	var rap retryAfterProvider
	if !errors.As(err, &rap) || rap == nil || rap.RetryAfter() == nil {
		t.Fatalf("expected CountTokens rate limit error to implement RetryAfter, got %v", err)
	}

	type credentialScopedProvider interface {
		IsCredentialScoped() bool
	}
	var csp credentialScopedProvider
	if !errors.As(err, &csp) || csp == nil || !csp.IsCredentialScoped() {
		t.Fatalf("expected CountTokens rate limit error to be credential-scoped, got %v", err)
	}

	minExpected := 7*24*time.Hour - 5*time.Second
	maxExpected := 7*24*time.Hour + 35*time.Second
	if *rap.RetryAfter() < minExpected || *rap.RetryAfter() > maxExpected {
		t.Fatalf("RetryAfter = %v, want between %v and %v", *rap.RetryAfter(), minExpected, maxExpected)
	}
}

func TestClaudeExecutor_RateLimit_CaseInsensitiveRawHeaderMap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["anthropic-ratelimit-unified-status"] = []string{"rejected"}
		w.Header()["anthropic-ratelimit-unified-7d-status"] = []string{"rejected"}
		w.Header()["anthropic-ratelimit-unified-7d-reset"] = []string{strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10)}
		w.Header()["retry-after"] = []string{"7200"}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Too many requests."}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "claude-auth-1",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var rap retryAfterProvider
	if !errors.As(err, &rap) || rap == nil || rap.RetryAfter() == nil {
		t.Fatalf("expected RetryAfter for non-canonical header map, got %v", err)
	}

	minExpected := 2*time.Hour - 5*time.Second
	maxExpected := 2*time.Hour + 35*time.Second
	if *rap.RetryAfter() < minExpected || *rap.RetryAfter() > maxExpected {
		t.Fatalf("RetryAfter = %v, want between %v and %v", *rap.RetryAfter(), minExpected, maxExpected)
	}
}

func TestClaudeExecutor_RateLimit_FastModeAuthoritativeRejectionHeadersOverrideBody(t *testing.T) {
	var attemptsCred1 atomic.Int32
	var attemptsCred2 atomic.Int32

	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptsCred1.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(time.Now().Add(7*24*time.Hour).Unix(), 10))
		w.Header().Set("Retry-After", "604800")
		w.WriteHeader(http.StatusTooManyRequests)
		// Body text mentioning fast request rejected, but headers explicitly reject unified quota
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Fast request rejected"}}`))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptsCred2.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg-fast-ok","type":"message","role":"assistant","content":[{"type":"text","text":"hello from cred2"}]}`))
	}))
	defer server2.Close()

	cfg := &config.Config{DisableCooling: false}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 2)

	executor := NewClaudeExecutor(cfg)
	manager.RegisterExecutor(executor)

	baseID := uuid.NewString()
	auth1 := &cliproxyauth.Auth{ID: baseID + "-fast-override-1", Provider: "claude", Attributes: map[string]string{"api_key": "k1", "base_url": server1.URL}}
	auth2 := &cliproxyauth.Auth{ID: baseID + "-fast-override-2", Provider: "claude", Attributes: map[string]string{"api_key": "k2", "base_url": server2.URL}}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3-5-sonnet-20241022"}})
	reg.RegisterClient(auth2.ID, "claude", []*registry.ModelInfo{{ID: "claude-3-5-sonnet-20241022"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := manager.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	payload := []byte(`{"speed":"fast","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("expected failover to cred2 when authoritative rate limit headers present, got: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected response from cred2")
	}

	if attemptsCred1.Load() != 1 {
		t.Fatalf("attempts on cred1 = %d, want 1", attemptsCred1.Load())
	}
	if attemptsCred2.Load() != 1 {
		t.Fatalf("attempts on cred2 = %d, want 1", attemptsCred2.Load())
	}

	// Verify cred1 was cooled down at credential level
	registeredAuth, ok := manager.GetByID(auth1.ID)
	if !ok || registeredAuth == nil {
		t.Fatal("auth1 not found")
	}
	if !registeredAuth.Unavailable || !registeredAuth.Quota.Exceeded {
		t.Fatalf("cred1 was not cooled down: unavailable=%v quota=%+v", registeredAuth.Unavailable, registeredAuth.Quota)
	}
}

func TestClaudeExecutor_RateLimit_FastEntitlementWithRetryAfterRemainsRequestScoped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for fast mode."}}`))
	}))
	defer server.Close()

	cfg := &config.Config{DisableCooling: false}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 2)

	executor := NewClaudeExecutor(cfg)
	manager.RegisterExecutor(executor)

	baseID := uuid.NewString()
	auth := &cliproxyauth.Auth{ID: baseID + "-fast-entitlement", Provider: "claude", Attributes: map[string]string{"api_key": "k1", "base_url": server.URL}}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "claude-3-5-sonnet-20241022"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	payload := []byte(`{"speed":"fast","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	_, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	registeredAuth, ok := manager.GetByID(auth.ID)
	if !ok || registeredAuth == nil {
		t.Fatal("auth not found")
	}
	if registeredAuth.Unavailable || registeredAuth.Quota.Exceeded {
		t.Fatalf("fast entitlement refusal incorrectly cooled down the credential: unavailable=%v quota=%+v", registeredAuth.Unavailable, registeredAuth.Quota)
	}
}

func TestClaudeExecutor_AuthManager_CredentialScopeBlocksAllModelsAndAliases(t *testing.T) {
	var upstreamAttempts atomic.Int32
	now := time.Now()
	sevenDayReset := now.Add(7 * 24 * time.Hour).Unix()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAttempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(sevenDayReset, 10))
		w.Header().Set("Retry-After", strconv.FormatInt(7*24*3600, 10))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"7d limit rejected."}}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		DisableCooling: false,
	}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)

	executor := NewClaudeExecutor(cfg)
	manager.RegisterExecutor(executor)

	baseID := uuid.NewString()
	auth := &cliproxyauth.Auth{
		ID:       baseID + "-claude-cred",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{
		{ID: "claude-3-5-sonnet-20241022"},
		{ID: "claude-3-opus-20240229"},
		{ID: "claude-3-7-sonnet-20250219"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	// 1. Initial request on sonnet triggers 429 and records 7d cooldown
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	_, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err == nil {
		t.Fatal("expected error on first execute, got nil")
	}

	if attempts := upstreamAttempts.Load(); attempts != 1 {
		t.Fatalf("upstream attempts = %d, want 1", attempts)
	}

	// 2. Try requesting a completely different model (opus) on the same credential -> must be blocked locally
	_, errOpus := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   "claude-3-opus-20240229",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errOpus == nil {
		t.Fatal("expected error for opus, got nil")
	}
	if attempts := upstreamAttempts.Load(); attempts != 1 {
		t.Fatalf("upstream attempts after opus = %d, want 1 (must be blocked locally)", attempts)
	}

	// 3. Try requesting a thinking suffix alias on the same credential -> must also be blocked locally
	_, errThinking := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   "claude-3-7-sonnet-20250219-thinking-16k",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errThinking == nil {
		t.Fatal("expected error for thinking suffix, got nil")
	}
	if attempts := upstreamAttempts.Load(); attempts != 1 {
		t.Fatalf("upstream attempts after thinking suffix = %d, want 1 (must be blocked locally)", attempts)
	}

	// 4. Try streaming execution for opus on the same cooling credential -> must also be blocked locally
	_, errStream := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   "claude-3-opus-20240229",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errStream == nil {
		t.Fatal("expected error for streaming opus, got nil")
	}
	if attempts := upstreamAttempts.Load(); attempts != 1 {
		t.Fatalf("upstream attempts after streaming opus = %d, want 1 (must be blocked locally)", attempts)
	}
}

func TestClaudeExecutor_AuthManager_OrdinaryModel429DoesNotBlockSiblingModels(t *testing.T) {
	var attemptsSonnet atomic.Int32
	var attemptsOpus atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "claude-3-5-sonnet") {
			attemptsSonnet.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
			w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Model rate limit exceeded."}}`))
			return
		}
		attemptsOpus.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg-opus","type":"message","role":"assistant","content":[{"type":"text","text":"hello from opus"}]}`))
	}))
	defer server.Close()

	cfg := &config.Config{DisableCooling: false}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)

	executor := NewClaudeExecutor(cfg)
	manager.RegisterExecutor(executor)

	baseID := uuid.NewString()
	auth := &cliproxyauth.Auth{
		ID:       baseID + "-ordinary-429",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{
		{ID: "claude-3-5-sonnet-20241022"},
		{ID: "claude-3-opus-20240229"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// 1. Initial request on sonnet triggers ordinary model 429
	payloadSonnet := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	_, errSonnet := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payloadSonnet,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errSonnet == nil {
		t.Fatal("expected error on sonnet execute, got nil")
	}
	if attemptsSonnet.Load() != 1 {
		t.Fatalf("sonnet attempts = %d, want 1", attemptsSonnet.Load())
	}

	// 2. Request on opus MUST succeed on the same credential (not blocked by ordinary model-level 429)
	payloadOpus := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi opus"}]}]}`)
	respOpus, errOpus := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   "claude-3-opus-20240229",
		Payload: payloadOpus,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errOpus != nil {
		t.Fatalf("expected opus to succeed on same credential, got error: %v", errOpus)
	}
	if len(respOpus.Payload) == 0 {
		t.Fatal("expected non-empty response for opus")
	}
	if attemptsOpus.Load() != 1 {
		t.Fatalf("opus attempts = %d, want 1", attemptsOpus.Load())
	}
}

func TestClaudeExecutor_AuthManager_AlternativeCredentialCanBeSelected(t *testing.T) {
	var attemptsCred1 atomic.Int32
	var attemptsCred2 atomic.Int32
	now := time.Now()
	sevenDayReset := now.Add(7 * 24 * time.Hour).Unix()

	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptsCred1.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(sevenDayReset, 10))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"7d limit rejected."}}`))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptsCred2.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg-123","type":"message","role":"assistant","content":[{"type":"text","text":"hello from cred2"}]}`))
	}))
	defer server2.Close()

	cfg := &config.Config{
		DisableCooling: false,
	}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 2)

	executor := NewClaudeExecutor(cfg)
	manager.RegisterExecutor(executor)

	baseID := uuid.NewString()
	auth1 := &cliproxyauth.Auth{
		ID:       baseID + "-claude-cred-1",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "test-key-1",
			"base_url": server1.URL,
		},
	}
	auth2 := &cliproxyauth.Auth{
		ID:       baseID + "-claude-cred-2",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "test-key-2",
			"base_url": server2.URL,
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3-5-sonnet-20241022"}})
	reg.RegisterClient(auth2.ID, "claude", []*registry.ModelInfo{{ID: "claude-3-5-sonnet-20241022"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := manager.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("expected successful failover to cred2, got error: %v", err)
	}

	if attemptsCred1.Load() != 1 {
		t.Fatalf("attempts on cred1 = %d, want 1", attemptsCred1.Load())
	}
	if attemptsCred2.Load() != 1 {
		t.Fatalf("attempts on cred2 = %d, want 1", attemptsCred2.Load())
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected non-empty response payload from cred2")
	}

	// Next request should directly use cred2 without attempting cred1 (which is cooling down)
	resp2, err2 := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err2 != nil {
		t.Fatalf("expected successful request on cred2, got error: %v", err2)
	}
	if attemptsCred1.Load() != 1 {
		t.Fatalf("attempts on cred1 after 2nd request = %d, want 1 (must stay 1)", attemptsCred1.Load())
	}
	if attemptsCred2.Load() != 2 {
		t.Fatalf("attempts on cred2 after 2nd request = %d, want 2", attemptsCred2.Load())
	}
	if len(resp2.Payload) == 0 {
		t.Fatal("expected non-empty response payload from 2nd request")
	}
}

func TestClaudeExecutor_AuthManager_MultiModelPoolStreamStopsProbingOn429(t *testing.T) {
	var attemptsCred1 atomic.Int32
	var attemptsCred2 atomic.Int32

	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptsCred1.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(time.Now().Add(7*24*time.Hour).Unix(), 10))
		w.Header().Set("Retry-After", "604800")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptsCred2.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"model\":\"claude-3-5-sonnet-20241022\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server2.Close()

	cfg := &config.Config{DisableCooling: false}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 2)

	manager.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{
		"claude": {
			{Name: "claude-3-5-sonnet-20241022", Alias: "claude-pool-alias"},
			{Name: "claude-3-opus-20240229", Alias: "claude-pool-alias"},
		},
	})

	executor := NewClaudeExecutor(cfg)
	manager.RegisterExecutor(executor)

	baseID := uuid.NewString()
	auth1 := &cliproxyauth.Auth{ID: baseID + "-pool-1", Provider: "claude", Attributes: map[string]string{"api_key": "k1", "base_url": server1.URL}}
	auth2 := &cliproxyauth.Auth{ID: baseID + "-pool-2", Provider: "claude", Attributes: map[string]string{"api_key": "k2", "base_url": server2.URL}}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{
		{ID: "claude-pool-alias"},
		{ID: "claude-3-5-sonnet-20241022"},
		{ID: "claude-3-opus-20240229"},
	})
	reg.RegisterClient(auth2.ID, "claude", []*registry.ModelInfo{
		{ID: "claude-pool-alias"},
		{ID: "claude-3-5-sonnet-20241022"},
		{ID: "claude-3-opus-20240229"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := manager.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	res, err := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   "claude-pool-alias",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("ExecuteStream failed: %v", err)
	}
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
	}

	// Must have tried cred1 exactly once (did NOT probe the 2nd model on cred1 after 429) and failed over to cred2
	if got := attemptsCred1.Load(); got != 1 {
		t.Fatalf("attempts on cred1 = %d, want 1 (must not probe other models on cooled cred)", got)
	}
	if got := attemptsCred2.Load(); got != 1 {
		t.Fatalf("attempts on cred2 = %d, want 1", got)
	}
}
