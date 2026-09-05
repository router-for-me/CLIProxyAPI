package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestAntigravityDisableCooling_ShortCooldownBypassedInHomeMode(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	cliproxyauth.SetQuotaCooldownDisabled(true)
	t.Cleanup(func() { cliproxyauth.SetQuotaCooldownDisabled(false) })

	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)

	cfg := &config.Config{
		DisableCooling: true,
		Home: config.HomeConfig{
			Enabled: true,
		},
	}
	exec := NewAntigravityExecutor(cfg)
	auth := &cliproxyauth.Auth{
		ID: "home-cooling-disabled-auth",
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "test-project",
		},
	}

	modelName := "claude-sonnet-4-5"
	now := time.Now()
	duration := 30 * time.Second

	// 1. In Home mode with DisableCooling, marking short cooldown should be a no-op
	if errMark := markAntigravityShortCooldownRequired(context.Background(), auth, modelName, now, duration); errMark != nil {
		t.Fatalf("markAntigravityShortCooldownRequired() error = %v", errMark)
	}
	if client.setCount != 0 {
		t.Fatalf("KVSet count = %d, want 0 when DisableCooling is true", client.setCount)
	}

	// 2. Pre-populate KV key manually to simulate existing key; read should still return false when cooling disabled
	antigravityShortCooldownByAuth = sync.Map{}
	client.values[antigravityShortCooldownKVKey(auth, modelName)] = []byte("9999999999999999999")
	inCooldown, remaining, errRead := antigravityIsInShortCooldownRequired(context.Background(), auth, modelName, now)
	if errRead != nil {
		t.Fatalf("antigravityIsInShortCooldownRequired() error = %v", errRead)
	}
	if inCooldown || remaining > 0 {
		t.Fatalf("inCooldown = %v, remaining = %v, want false/0 when DisableCooling is true", inCooldown, remaining)
	}

	// 3. In Execute, upstream 429 RATE_LIMIT_EXCEEDED should not record short cooldown to KV
	client.setCount = 0
	upstreamResp := `{"error":{"code":429,"message":"Rate limit exceeded","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`
	execCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Retry-After", "10")
		h.Set("Content-Type", "application/json")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     h,
			Body:       io.NopCloser(strings.NewReader(upstreamResp)),
		}, nil
	}))

	req := cliproxyexecutor.Request{
		Model:   modelName,
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
	}
	_, _ = exec.Execute(execCtx, auth, req, opts)
	if client.setCount != 0 {
		t.Fatalf("Execute recorded short cooldown to KV store (setCount = %d), want 0 when cooling disabled", client.setCount)
	}
}

func TestAntigravityDisableCooling_ExecuteStreamBypassesShortCooldown(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)

	cfg := &config.Config{
		DisableCooling: true,
		Home: config.HomeConfig{
			Enabled: true,
		},
	}
	exec := NewAntigravityExecutor(cfg)
	auth := &cliproxyauth.Auth{
		ID: "home-cooling-disabled-auth-stream",
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "test-project",
		},
	}

	modelName := "claude-sonnet-4-5"
	upstreamResp := `{"error":{"code":429,"message":"Rate limit exceeded","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`
	execCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Retry-After", "10")
		h.Set("Content-Type", "application/json")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     h,
			Body:       io.NopCloser(strings.NewReader(upstreamResp)),
		}, nil
	}))

	req := cliproxyexecutor.Request{
		Model:   modelName,
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
	}
	_, _ = exec.ExecuteStream(execCtx, auth, req, opts)
	if client.setCount != 0 {
		t.Fatalf("ExecuteStream recorded short cooldown to KV store (setCount = %d), want 0 when cooling disabled", client.setCount)
	}
}

func TestAntigravityDisableCooling_AuthOverrideBypassesShortCooldown(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, false, nil)

	cfg := &config.Config{
		DisableCooling: false,
	}
	exec := NewAntigravityExecutor(cfg)
	auth := &cliproxyauth.Auth{
		ID: "override-cooling-disabled-auth",
		Metadata: map[string]any{
			"disable_cooling": true,
			"access_token":    "token",
			"project_id":      "test-project",
		},
	}

	modelName := "claude-sonnet-4-5"
	now := time.Now()
	duration := 30 * time.Second

	// In memory map: marking short cooldown should be skipped for auth with disable_cooling override
	if errMark := markAntigravityShortCooldownRequired(context.Background(), auth, modelName, now, duration); errMark != nil {
		t.Fatalf("markAntigravityShortCooldownRequired() error = %v", errMark)
	}
	if _, loaded := antigravityShortCooldownByAuth.Load(antigravityShortCooldownKey(auth, modelName)); loaded {
		t.Fatalf("antigravityShortCooldownByAuth stored key, want skipped for auth with disable_cooling override")
	}

	// Pre-populate in-memory map; read should return false
	antigravityShortCooldownByAuth.Store(antigravityShortCooldownKey(auth, modelName), now.Add(time.Hour))
	inCooldown, remaining, errRead := antigravityIsInShortCooldownRequired(context.Background(), auth, modelName, now)
	if errRead != nil {
		t.Fatalf("antigravityIsInShortCooldownRequired() error = %v", errRead)
	}
	if inCooldown || remaining > 0 {
		t.Fatalf("inCooldown = %v, remaining = %v, want false/0 when auth has disable_cooling override", inCooldown, remaining)
	}

	// In Execute, upstream 429 RATE_LIMIT_EXCEEDED should not record short cooldown to memory map
	antigravityShortCooldownByAuth = sync.Map{}
	upstreamResp := `{"error":{"code":429,"message":"Rate limit exceeded","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`
	execCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Retry-After", "10")
		h.Set("Content-Type", "application/json")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     h,
			Body:       io.NopCloser(strings.NewReader(upstreamResp)),
		}, nil
	}))

	req := cliproxyexecutor.Request{
		Model:   modelName,
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
	}
	_, _ = exec.Execute(execCtx, auth, req, opts)
	if _, loaded := antigravityShortCooldownByAuth.Load(antigravityShortCooldownKey(auth, modelName)); loaded {
		t.Fatalf("Execute recorded short cooldown to memory map, want skipped when auth has disable_cooling override")
	}
}

func TestAntigravityDisableCooling_CreditsHintRefreshBypassedInHomeMode(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)

	cfg := &config.Config{
		DisableCooling: true,
		Home: config.HomeConfig{
			Enabled: true,
		},
		QuotaExceeded: config.QuotaExceeded{
			AntigravityCredits: true,
		},
	}
	exec := NewAntigravityExecutor(cfg)
	auth := &cliproxyauth.Auth{
		ID: "home-refresh-cooling-disabled-auth",
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "test-project",
		},
	}

	exec.maybeRefreshAntigravityCreditsHint(context.Background(), auth, "token")
	if client.setNXCount != 0 {
		t.Fatalf("KVSetNX count = %d, want 0 when DisableCooling is true", client.setNXCount)
	}
}

func TestAntigravityDisableCooling_CreditsPermanentlyDisabledBypassed(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)

	auth := &cliproxyauth.Auth{
		ID: "home-permanently-disabled-auth",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}

	markAntigravityCreditsPermanentlyDisabled(auth)
	if client.setCount != 0 {
		t.Fatalf("KVSet count = %d, want 0 when DisableCooling is true", client.setCount)
	}
	if cliproxyauth.HasKnownAntigravityCreditsHint(auth.ID) {
		t.Fatalf("credits hint was stored for auth with DisableCooling true")
	}
}
