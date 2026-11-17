package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type resetTriggerTestExecutor struct {
	mu       sync.Mutex
	calls    []string
	payloads [][]byte
	sessions []string
	fail     bool
	reset    time.Time
}

func (e *resetTriggerTestExecutor) Identifier() string { return "claude" }

func (e *resetTriggerTestExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, auth.ID)
	e.payloads = append(e.payloads, req.Payload)
	e.sessions = append(e.sessions, opts.Headers.Get("X-Session-ID"))
	if e.fail {
		return cliproxyexecutor.Response{}, errors.New("upstream unavailable")
	}
	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(e.reset.Unix(), 10))
	headers.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(e.reset.Add(72*time.Hour).Unix(), 10))
	// Real executors publish upstream headers this way; quota observation reads them.
	internallogging.SetResponseHeaders(ctx, headers)
	return cliproxyexecutor.Response{Payload: []byte(`{}`), Headers: headers}, nil
}

func (e *resetTriggerTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *resetTriggerTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *resetTriggerTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *resetTriggerTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *resetTriggerTestExecutor) snapshot() ([]string, [][]byte, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...), append([][]byte(nil), e.payloads...), append([]string(nil), e.sessions...)
}

func TestResetTriggerSettingsFromConfig(t *testing.T) {
	defaults := resetTriggerSettingsFromConfig(&config.Config{})
	if defaults.enabled || defaults.claudeModel != resetTriggerDefaultClaudeModel || defaults.codexModel != resetTriggerDefaultCodexModel ||
		defaults.minDelay != resetTriggerDefaultMinDelay || defaults.maxDelay != resetTriggerDefaultMaxDelay {
		t.Fatalf("defaults = %+v", defaults)
	}
	cfg := &config.Config{}
	cfg.Routing.ResetTrigger = config.ResetTriggerConfig{Enabled: true, ClaudeModel: "claude-x", CodexModel: "gpt-x", MinDelay: "10m", MaxDelay: "1m"}
	got := resetTriggerSettingsFromConfig(cfg)
	if !got.enabled || got.claudeModel != "claude-x" || got.codexModel != "gpt-x" || got.minDelay != 10*time.Minute || got.maxDelay != 10*time.Minute {
		t.Fatalf("settings = %+v", got)
	}
}

func TestBuildResetTriggerPayload(t *testing.T) {
	claude, format, errClaude := buildResetTriggerPayload("claude", "claude-haiku-5-5", "hi")
	if errClaude != nil || format.String() != "claude" {
		t.Fatalf("claude payload err=%v format=%q", errClaude, format)
	}
	if ojson.GetBytes(claude, "messages.0.content").String() != "hi" || ojson.GetBytes(claude, "max_tokens").Int() != 16 {
		t.Fatalf("claude payload = %s", claude)
	}
	codex, format, errCodex := buildResetTriggerPayload("codex", "gpt-5.6-luna", "hi")
	if errCodex != nil || format.String() != "openai-response" {
		t.Fatalf("codex payload err=%v format=%q", errCodex, format)
	}
	if ojson.GetBytes(codex, "input.0.content.0.text").String() != "hi" || ojson.GetBytes(codex, "reasoning.effort").String() != "low" {
		t.Fatalf("codex payload = %s", codex)
	}
	if _, _, errOther := buildResetTriggerPayload("gemini", "m", "hi"); errOther == nil {
		t.Fatal("unsupported provider: want error")
	}
}

func TestResetTriggerStartsIdleWindowsOnce(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1787279282, 0)
	clock := now
	lapsed := now.Add(-time.Minute)
	model := resetTriggerDefaultClaudeModel

	executor := &resetTriggerTestExecutor{reset: now.Add(5 * time.Hour)}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	signals := func(fiveHour time.Time) map[string]string {
		return map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Status": "allowed",
			"Anthropic-Ratelimit-Unified-5h-Reset":  strconv.FormatInt(fiveHour.Unix(), 10),
			"Anthropic-Ratelimit-Unified-7d-Status": "allowed",
			"Anthropic-Ratelimit-Unified-7d-Reset":  strconv.FormatInt(now.Add(72*time.Hour).Unix(), 10),
		}
	}
	register := func(id string, fiveHour time.Time) {
		auth := &coreauth.Auth{
			ID: id, Provider: "claude", Status: coreauth.StatusActive,
			Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
			Quota:      coreauth.QuotaState{ObservedAt: now.Add(-5 * time.Hour), Signals: signals(fiveHour)},
		}
		if _, errRegister := manager.Register(coreauth.WithSkipPersist(ctx), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", id, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	register("idle", lapsed)
	register("busy", now.Add(2*time.Hour))

	enabled := true
	trigger := newResetTrigger(manager, func() resetTriggerSettings {
		return resetTriggerSettings{enabled: enabled, claudeModel: model, codexModel: "codex-model", minDelay: time.Minute, maxDelay: 3 * time.Minute}
	})
	trigger.now = func() time.Time { return clock }
	// Pick the upper bound so the delay is deterministic: reset + 3m.
	trigger.randInt = func(n int64) int64 { return n - 1 }

	trigger.tick(ctx)
	trigger.wg.Wait()
	if calls, _, _ := executor.snapshot(); len(calls) != 0 {
		t.Fatalf("calls before delay = %v, want none", calls)
	}

	clock = lapsed.Add(3 * time.Minute)
	trigger.tick(ctx)
	trigger.wg.Wait()
	calls, payloads, sessions := executor.snapshot()
	if len(calls) != 1 || calls[0] != "idle" {
		t.Fatalf("calls = %v, want only idle", calls)
	}
	if got := ojson.GetBytes(payloads[0], "model").String(); got != model {
		t.Fatalf("trigger model = %q, want %q", got, model)
	}
	if sessions[0] == "" {
		t.Fatal("trigger request carries no explicit session id")
	}
	idle, _ := manager.GetByID("idle")
	if state := coreauth.ResetTriggerStateForAuth(idle, model, clock); state.Due {
		t.Fatalf("idle credential still due after trigger: %+v", state)
	}

	clock = clock.Add(time.Hour)
	trigger.tick(ctx)
	trigger.wg.Wait()
	if calls, _, _ := executor.snapshot(); len(calls) != 1 {
		t.Fatalf("calls after window started = %v, want 1", calls)
	}

	// Disabling stops all triggering even when "busy" lapses.
	enabled = false
	clock = now.Add(3 * time.Hour)
	trigger.tick(ctx)
	trigger.wg.Wait()
	if calls, _, _ := executor.snapshot(); len(calls) != 1 {
		t.Fatalf("calls while disabled = %v, want 1", calls)
	}
}

func TestResetTriggerRetriesFailuresWithBackoff(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1787279282, 0)
	clock := now
	var calls int
	auth := &coreauth.Auth{
		ID: "a", Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(ctx), auth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	trigger := newResetTrigger(manager, func() resetTriggerSettings {
		return resetTriggerSettings{enabled: true, claudeModel: "m"}
	})
	trigger.now = func() time.Time { return clock }
	trigger.randInt = func(int64) int64 { return 0 }
	trigger.execute = func(_ context.Context, providers []string, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		calls++
		if pinned := opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey]; pinned != "a" || len(providers) != 1 || providers[0] != "claude" {
			t.Errorf("pinned=%v providers=%v", pinned, providers)
		}
		return cliproxyexecutor.Response{}, errors.New("boom")
	}

	setCooldown := func(until time.Time) {
		current, _ := manager.GetByID("a")
		current.Unavailable = !until.IsZero()
		current.NextRetryAfter = until
		if _, errUpdate := manager.Update(coreauth.WithSkipPersist(ctx), current); errUpdate != nil {
			t.Fatalf("Update: %v", errUpdate)
		}
	}
	for i := 0; i < 2*resetTriggerMaxAttempts; i++ {
		trigger.tick(ctx)
		trigger.wg.Wait()
		clock = clock.Add(resetTriggerRetryBackoff / 2)
		// A cooldown that ends before the backoff must not reset the plan.
		setCooldown(clock.Add(time.Minute))
		trigger.tick(ctx)
		setCooldown(time.Time{})
		trigger.tick(ctx)
		trigger.wg.Wait()
		clock = clock.Add(resetTriggerRetryBackoff / 2)
	}
	if calls != resetTriggerMaxAttempts {
		t.Fatalf("calls = %d, want %d", calls, resetTriggerMaxAttempts)
	}
}
