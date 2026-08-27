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

func TestClassifyClaudeUpstreamError_FableOnlyRejectionIsModelScoped(t *testing.T) {
	// Given
	headers := http.Header{
		"Anthropic-Ratelimit-Unified-Status":       []string{"rejected"},
		"Anthropic-Ratelimit-Unified-5h-Status":    []string{"allowed"},
		"Anthropic-Ratelimit-Unified-7d-Status":    []string{"allowed"},
		"Anthropic-Ratelimit-Unified-7d_oi-Status": []string{"rejected"},
		"Retry-After": []string{"120"},
	}

	// When
	err := classifyClaudeUpstreamError(http.StatusTooManyRequests, headers, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Fable usage window rejected."}}`))

	// Then
	var scoped interface{ IsCredentialScoped() bool }
	if !errors.As(err, &scoped) || scoped == nil {
		t.Fatalf("expected %T to expose credential scope", err)
	}
	if scoped.IsCredentialScoped() {
		t.Fatal("Fable-only 7d_oi rejection was credential-scoped; want model-scoped")
	}
}

func TestClassifyClaudeUpstreamError_SharedOrAmbiguousRejectionRemainsCredentialScoped(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
	}{
		{
			name: "explicit 5h rejection",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-5h-Status": []string{"rejected"},
				"Anthropic-Ratelimit-Unified-7d-Status": []string{"allowed"},
			},
		},
		{
			name: "explicit shared 7d rejection",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-5h-Status": []string{"allowed"},
				"Anthropic-Ratelimit-Unified-7d-Status": []string{"rejected"},
			},
		},
		{
			name: "aggregate rejection with shared statuses missing",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-Status": []string{"rejected"},
			},
		},
		{
			name: "aggregate rejection with shared statuses malformed",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-Status":    []string{"rejected"},
				"Anthropic-Ratelimit-Unified-5h-Status": []string{"unknown"},
				"Anthropic-Ratelimit-Unified-7d-Status": []string{"invalid"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			err := classifyClaudeUpstreamError(http.StatusTooManyRequests, tt.headers, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Shared usage window rejected."}}`))

			// Then
			var scoped interface{ IsCredentialScoped() bool }
			if !errors.As(err, &scoped) || scoped == nil {
				t.Fatalf("expected %T to expose credential scope", err)
			}
			if !scoped.IsCredentialScoped() {
				t.Fatal("shared or ambiguous rejection was model-scoped; want credential-scoped")
			}
		})
	}
}

func TestClassifyClaudeUpstreamError_FableRetryDuration(t *testing.T) {
	t.Run("retry-after header is respected", func(t *testing.T) {
		headers := http.Header{
			"Anthropic-Ratelimit-Unified-Status":       []string{"rejected"},
			"Anthropic-Ratelimit-Unified-5h-Status":    []string{"allowed"},
			"Anthropic-Ratelimit-Unified-7d-Status":    []string{"allowed"},
			"Anthropic-Ratelimit-Unified-7d_oi-Status": []string{"rejected"},
			"Anthropic-Ratelimit-Unified-7d_oi-Reset":  []string{strconv.FormatInt(time.Now().Add(7*24*time.Hour).Unix(), 10)},
			"Retry-After": []string{"120"},
		}

		err := classifyClaudeUpstreamError(http.StatusTooManyRequests, headers, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Fable usage window rejected."}}`))

		var retry retryAfterProvider
		if !errors.As(err, &retry) || retry == nil || retry.RetryAfter() == nil {
			t.Fatalf("expected Fable rate-limit error to retain a retry duration, got %v", err)
		}
		if got := *retry.RetryAfter(); got < 2*time.Minute || got > 2*time.Minute+30*time.Second {
			t.Fatalf("RetryAfter = %v, want ~120s with fuzz, but not 7d", got)
		}
	})

	t.Run("7d_oi reset only does not set week-long retry duration", func(t *testing.T) {
		headers := http.Header{
			"Anthropic-Ratelimit-Unified-Status":       []string{"rejected"},
			"Anthropic-Ratelimit-Unified-5h-Status":    []string{"allowed"},
			"Anthropic-Ratelimit-Unified-7d-Status":    []string{"allowed"},
			"Anthropic-Ratelimit-Unified-7d_oi-Status": []string{"rejected"},
			"Anthropic-Ratelimit-Unified-7d_oi-Reset":  []string{strconv.FormatInt(time.Now().Add(7*24*time.Hour).Unix(), 10)},
		}

		err := classifyClaudeUpstreamError(http.StatusTooManyRequests, headers, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Fable usage window rejected."}}`))

		var retry retryAfterProvider
		if errors.As(err, &retry) && retry != nil && retry.RetryAfter() != nil {
			t.Fatalf("expected Fable 7d_oi-only reset to yield nil RetryAfter, got %v", *retry.RetryAfter())
		}
	})
}

func TestClaudeExecutor_AuthManager_FableOnlyRejectionDoesNotBlockOpus(t *testing.T) {
	var fableAttempts, opusAttempts atomic.Int32
	reset := time.Now().Add(7 * 24 * time.Hour).Unix()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			http.Error(w, "failed to read sanitized test request", http.StatusBadRequest)
			return
		}
		switch {
		case strings.Contains(string(body), `"model":"claude-fable-5"`):
			fableAttempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
			w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
			w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
			w.Header().Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
			w.Header().Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", strconv.FormatInt(reset, 10))
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Fable usage window rejected."}}`))
		case strings.Contains(string(body), `"model":"claude-opus-5"`):
			opusAttempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg-opus-ok","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"ok"}]}`))
		default:
			http.Error(w, "unexpected sanitized test model", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(NewClaudeExecutor(&config.Config{DisableCooling: false}))

	auth := &cliproxyauth.Auth{
		ID:       uuid.NewString() + "-fable-model-scope",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "sanitized-test-key",
			"base_url": server.URL,
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "claude-fable-5"}, {ID: "claude-opus-5"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	payloadFable := []byte(`{"model":"claude-fable-5","messages":[{"role":"user","content":[{"type":"text","text":"test"}]}]}`)
	_, errFable := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   "claude-fable-5",
		Payload: payloadFable,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errFable == nil {
		t.Fatal("expected Fable request to be rate limited")
	}
	if got := fableAttempts.Load(); got != 1 {
		t.Fatalf("Fable upstream attempts = %d, want 1", got)
	}

	// Verify that Fable model state cooldown is driven by Retry-After (~120s) and not 7 days.
	updatedAuth, ok := manager.GetByID(auth.ID)
	if !ok || updatedAuth == nil {
		t.Fatal("auth not found")
	}
	fableState := updatedAuth.ModelStates["claude-fable-5"]
	if fableState == nil {
		t.Fatal("fable model state not found")
	}
	if fableState.Quota.NextRecoverAt.After(time.Now().Add(5 * time.Minute)) {
		t.Fatalf("fable model state cooldown too long: NextRecoverAt = %v (want ~120s, not 7 days)", fableState.Quota.NextRecoverAt)
	}

	payloadOpus := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":[{"type":"text","text":"test"}]}]}`)
	_, errOpus := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: payloadOpus,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errOpus != nil {
		t.Fatalf("expected Opus to reach upstream on the same credential, got: %v", errOpus)
	}
	if got := opusAttempts.Load(); got != 1 {
		t.Fatalf("Opus upstream attempts = %d, want 1", got)
	}
}
