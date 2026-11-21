package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func usageCeilingConfig(weekly, short float64) *internalconfig.Config {
	return &internalconfig.Config{Routing: internalconfig.RoutingConfig{UsageCeiling: internalconfig.UsageCeilingConfig{
		WeeklyPercent:      weekly,
		ShortWindowPercent: short,
	}}}
}

func claudeUsageSignals(util5h, util7d string, reset5h, reset7d time.Time) map[string]string {
	return map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization": util5h,
		"Anthropic-Ratelimit-Unified-5h-Reset":       strconv.FormatInt(reset5h.Unix(), 10),
		"Anthropic-Ratelimit-Unified-7d-Utilization": util7d,
		"Anthropic-Ratelimit-Unified-7d-Reset":       strconv.FormatInt(reset7d.Unix(), 10),
	}
}

func TestUsageCeilingBreach(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	in2h := now.Add(2 * time.Hour)
	in3d := now.Add(72 * time.Hour)

	tests := []struct {
		name      string
		auth      *Auth
		cfg       *internalconfig.Config
		wantBlock bool
		wantRetry time.Duration
	}{
		{
			name: "claude weekly over cap",
			auth: &Auth{Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: claudeUsageSignals("0.10", "0.85", in2h, in3d)}},
			cfg:  usageCeilingConfig(80, 0), wantBlock: true, wantRetry: 72 * time.Hour,
		},
		{
			name: "claude 5h over cap",
			auth: &Auth{Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: claudeUsageSignals("0.95", "0.10", in2h, in3d)}},
			cfg:  usageCeilingConfig(0, 90), wantBlock: true, wantRetry: 2 * time.Hour,
		},
		{
			name: "claude both over cap uses latest reset",
			auth: &Auth{Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: claudeUsageSignals("0.95", "0.85", in2h, in3d)}},
			cfg:  usageCeilingConfig(80, 90), wantBlock: true, wantRetry: 72 * time.Hour,
		},
		{
			name: "claude under cap",
			auth: &Auth{Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: claudeUsageSignals("0.50", "0.79", in2h, in3d)}},
			cfg:  usageCeilingConfig(80, 90),
		},
		{
			name: "claude window already reset",
			auth: &Auth{Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: claudeUsageSignals("0.10", "0.85", in2h, now.Add(-time.Minute))}},
			cfg:  usageCeilingConfig(80, 0),
		},
		{
			name: "no cap configured",
			auth: &Auth{Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: claudeUsageSignals("0.99", "0.99", in2h, in3d)}},
			cfg:  nil,
		},
		{
			name: "auth file cap without global config",
			auth: &Auth{
				Provider: "claude",
				Metadata: map[string]any{"usage_ceiling": map[string]any{"weekly_percent": float64(80)}},
				Quota:    QuotaState{ObservedAt: now, Signals: claudeUsageSignals("0.10", "0.85", in2h, in3d)},
			},
			cfg: nil, wantBlock: true, wantRetry: 72 * time.Hour,
		},
		{
			name: "auth file zero turns global cap off",
			auth: &Auth{
				Provider: "claude",
				Metadata: map[string]any{"usage_ceiling": map[string]any{"weekly_percent": json.Number("0")}},
				Quota:    QuotaState{ObservedAt: now, Signals: claudeUsageSignals("0.10", "0.85", in2h, in3d)},
			},
			cfg: usageCeilingConfig(80, 0),
		},
		{
			name: "auth file raises cap above usage",
			auth: &Auth{
				Provider: "claude",
				Metadata: map[string]any{"usage-ceiling": map[string]any{"weekly-percent": "95"}},
				Quota:    QuotaState{ObservedAt: now, Signals: claudeUsageSignals("0.10", "0.85", in2h, in3d)},
			},
			cfg: usageCeilingConfig(80, 0),
		},
		{
			name: "auth file overrides only the key it sets",
			auth: &Auth{
				Provider: "claude",
				Metadata: map[string]any{"usage_ceiling": map[string]any{"weekly_percent": float64(95)}},
				Quota:    QuotaState{ObservedAt: now, Signals: claudeUsageSignals("0.95", "0.85", in2h, in3d)},
			},
			cfg: usageCeilingConfig(80, 90), wantBlock: true, wantRetry: 2 * time.Hour,
		},
		{
			name: "codex weekly window by minutes",
			auth: &Auth{Provider: "codex", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
				"X-Codex-Primary-Used-Percent":          "40",
				"X-Codex-Primary-Window-Minutes":        "300",
				"X-Codex-Primary-Reset-After-Seconds":   "3600",
				"X-Codex-Secondary-Used-Percent":        "81",
				"X-Codex-Secondary-Window-Minutes":      "10080",
				"X-Codex-Secondary-Reset-After-Seconds": "86400",
			}}},
			cfg: usageCeilingConfig(80, 90), wantBlock: true, wantRetry: 24 * time.Hour,
		},
		{
			name: "codex 5h window by minutes",
			auth: &Auth{Provider: "codex", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
				"X-Codex-Primary-Used-Percent":   "92",
				"X-Codex-Primary-Window-Minutes": "300",
				"X-Codex-Primary-Reset-At":       strconv.FormatInt(in2h.Unix(), 10),
			}}},
			cfg: usageCeilingConfig(80, 90), wantBlock: true, wantRetry: 2 * time.Hour,
		},
		{
			name: "devin weekly remaining percent",
			auth: &Auth{Provider: "devin", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
				"daily_quota_remaining_percent":  "90%",
				"weekly_quota_remaining_percent": "15%",
				"weekly_quota_reset_at":          in3d.UTC().Format(time.RFC3339),
			}}},
			cfg: usageCeilingConfig(80, 0), wantBlock: true, wantRetry: 72 * time.Hour,
		},
		{
			name: "unsupported provider",
			auth: &Auth{Provider: "gemini", Quota: QuotaState{ObservedAt: now, Signals: claudeUsageSignals("0.99", "0.99", in2h, in3d)}},
			cfg:  usageCeilingConfig(80, 90),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errCeiling := usageCeilingBreach(test.auth, test.cfg, now)
			if !test.wantBlock {
				if errCeiling != nil {
					t.Fatalf("usageCeilingBreach() = %v, want nil", errCeiling)
				}
				return
			}
			if errCeiling == nil {
				t.Fatal("usageCeilingBreach() = nil, want error")
			}
			if got := statusCodeFromError(errCeiling); got != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429", got)
			}
			if !isCredentialScopedError(errCeiling) {
				t.Fatal("error is not credential scoped")
			}
			retryAfter := retryAfterFromError(errCeiling)
			if retryAfter == nil || *retryAfter != test.wantRetry {
				t.Fatalf("retry after = %v, want %v", retryAfter, test.wantRetry)
			}
		})
	}
}

type usageCeilingExecutor struct {
	mu    sync.Mutex
	calls []string
}

func (*usageCeilingExecutor) Identifier() string { return "claude" }

func (e *usageCeilingExecutor) record(auth *Auth) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, auth.ID)
}

func (e *usageCeilingExecutor) called() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func (e *usageCeilingExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth)
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *usageCeilingExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.record(auth)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*usageCeilingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }

func (*usageCeilingExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*usageCeilingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func newUsageCeilingManager(t *testing.T, model string, auths ...*Auth) (*Manager, *usageCeilingExecutor) {
	t.Helper()
	manager := NewManager(nil, quotaAttemptIsolationSelector{}, nil)
	manager.SetConfig(usageCeilingConfig(80, 90))
	executor := &usageCeilingExecutor{}
	manager.RegisterExecutor(executor)
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
		id := auth.ID
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	return manager, executor
}

func TestUsageCeilingSkipsCappedCredential(t *testing.T) {
	now := time.Now()
	reset := now.Add(72 * time.Hour)
	tests := []struct {
		name string
		run  func(*Manager, string) error
	}{
		{
			name: "non-stream",
			run: func(manager *Manager, model string) error {
				_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "stream",
			run: func(manager *Manager, model string) error {
				result, errExecute := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				if errExecute == nil && result != nil {
					discardStreamChunks(result.Chunks)
				}
				return errExecute
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := "claude-usage-ceiling-" + test.name
			cappedID := "usage-ceiling-" + test.name + "-a"
			healthyID := "usage-ceiling-" + test.name + "-b"
			manager, executor := newUsageCeilingManager(t, model,
				&Auth{ID: cappedID, Provider: "claude", Status: StatusActive, Quota: QuotaState{
					ObservedAt: now, Signals: claudeUsageSignals("0.10", "0.85", now.Add(time.Hour), reset),
				}},
				&Auth{ID: healthyID, Provider: "claude", Status: StatusActive, Quota: QuotaState{
					ObservedAt: now, Signals: claudeUsageSignals("0.10", "0.20", now.Add(time.Hour), reset),
				}},
			)

			if errExecute := test.run(manager, model); errExecute != nil {
				t.Fatalf("execution error = %v, want nil", errExecute)
			}
			if calls := executor.called(); len(calls) != 1 || calls[0] != healthyID {
				t.Fatalf("executor calls = %v, want only %s", calls, healthyID)
			}
			capped, ok := manager.GetByID(cappedID)
			if !ok {
				t.Fatal("capped auth missing")
			}
			if !capped.Quota.Exceeded || capped.Quota.NextRecoverAt.Before(reset.Add(-time.Minute)) {
				t.Fatalf("capped quota = %+v, want cooldown until about %v", capped.Quota, reset)
			}
			if capped.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Utilization"] != "0.85" {
				t.Fatalf("capped signals changed: %v", capped.Quota.Signals)
			}

			// A second request goes straight to the healthy credential.
			if errExecute := test.run(manager, model); errExecute != nil {
				t.Fatalf("second execution error = %v, want nil", errExecute)
			}
			if calls := executor.called(); len(calls) != 2 || calls[1] != healthyID {
				t.Fatalf("executor calls = %v, want healthy credential again", calls)
			}
		})
	}
}

func TestUsageCeilingAllCredentialsCappedReturns429(t *testing.T) {
	now := time.Now()
	model := "claude-usage-ceiling-all-capped"
	manager, executor := newUsageCeilingManager(t, model,
		&Auth{ID: "usage-ceiling-all-a", Provider: "claude", Status: StatusActive, Quota: QuotaState{
			ObservedAt: now, Signals: claudeUsageSignals("0.95", "0.10", now.Add(2*time.Hour), now.Add(72*time.Hour)),
		}},
		&Auth{ID: "usage-ceiling-all-b", Provider: "claude", Status: StatusActive, Quota: QuotaState{
			ObservedAt: now, Signals: claudeUsageSignals("0.10", "0.90", now.Add(2*time.Hour), now.Add(72*time.Hour)),
		}},
	)

	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("execution error = nil, want 429")
	}
	if got := statusCodeFromError(errExecute); got != http.StatusTooManyRequests {
		t.Fatalf("status = %d (%v), want 429", got, errExecute)
	}
	if calls := executor.called(); len(calls) != 0 {
		t.Fatalf("executor calls = %v, want none", calls)
	}
}
