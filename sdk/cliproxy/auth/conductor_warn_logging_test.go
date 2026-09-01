package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func setupTestLoggerHook(t *testing.T) *logtest.Hook {
	_, hook := logtest.NewNullLogger()
	oldLevel := log.GetLevel()
	log.SetLevel(log.WarnLevel)

	// Deep-clone existing hooks
	savedHooks := make(log.LevelHooks)
	for lvl, hs := range log.StandardLogger().Hooks {
		savedHooks[lvl] = append([]log.Hook(nil), hs...)
	}

	log.AddHook(hook)
	t.Cleanup(func() {
		log.SetLevel(oldLevel)
		log.StandardLogger().ReplaceHooks(savedHooks)
	})
	return hook
}

type statusErrorLogTestError struct {
	message    string
	statusCode int
}

func (e statusErrorLogTestError) Error() string { return e.message }

func (e statusErrorLogTestError) StatusCode() int { return e.statusCode }

type markedDiagnosticStatusError struct {
	message    string
	diagnostic string
	statusCode int
}

func (e markedDiagnosticStatusError) Error() string { return e.message }

func (e markedDiagnosticStatusError) StatusCode() int { return e.statusCode }

func (e markedDiagnosticStatusError) LogDiagnostic() string { return e.diagnostic }

func TestWarnLogUpstreamFailureIncludesStructuredStatusCodeAndSafeDiagnostic(t *testing.T) {
	hook := setupTestLoggerHook(t)
	diagnostic := `  antigravity refresh: upstream request failed with status 400 error="invalid_request" error_description="Malformed request, retry & inspect"  `

	warnLogUpstreamFailure(
		context.Background(),
		nil,
		"antigravity",
		"gemini-3.7-flash-high",
		&Auth{ID: "auth-1", Provider: "antigravity"},
		83*time.Millisecond,
		statusErrorLogTestError{message: diagnostic, statusCode: http.StatusServiceUnavailable},
	)

	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "upstream execution failed") {
			if !strings.HasPrefix(entry.Message, "503 |          83ms | upstream execution failed: provider=antigravity") || strings.Contains(entry.Message, "status=503") || !strings.HasSuffix(entry.Message, "err="+strings.TrimSpace(diagnostic)) {
				t.Fatalf("unexpected Warn log content: %s", entry.Message)
			}
			return
		}
	}
	t.Fatalf("expected upstream failure Warn log, got logs: %#v", hook.AllEntries())
}

func TestWarnLogUpstreamFailureUsesMarkedHomeDiagnostic(t *testing.T) {
	hook := setupTestLoggerHook(t)
	errRefresh := markedDiagnosticStatusError{
		message:    "credential refresh temporarily unavailable",
		diagnostic: "antigravity refresh failed: stage=transport err=EOF access_token=provider-secret",
		statusCode: http.StatusServiceUnavailable,
	}

	warnLogUpstreamFailure(
		context.Background(),
		nil,
		"antigravity",
		"gemini-3.7-flash-high",
		&Auth{ID: "auth-1", Provider: "antigravity"},
		129*time.Millisecond,
		errRefresh,
	)

	for _, entry := range hook.AllEntries() {
		if entry.Level != log.WarnLevel || !strings.Contains(entry.Message, "upstream execution failed") {
			continue
		}
		if !strings.Contains(entry.Message, "stage=transport") || !strings.Contains(entry.Message, "err=EOF") {
			t.Fatalf("Warn log lost marked Home diagnostic: %q", entry.Message)
		}
		if strings.Contains(entry.Message, "provider-secret") || strings.Contains(entry.Message, errRefresh.message) {
			t.Fatalf("Warn log exposed secret or used generic client message: %q", entry.Message)
		}
		return
	}
	t.Fatalf("expected upstream failure Warn log, got logs: %#v", hook.AllEntries())
}

func TestHomeCredentialBoundaryWarnLogUsesSafeDiagnostic(t *testing.T) {
	diagnostic := "antigravity refresh: access token expired access_token=provider-secret\nforged log line"
	upstreamErr := markedDiagnosticStatusError{
		message:    "credential refresh temporarily unavailable",
		diagnostic: diagnostic,
		statusCode: http.StatusServiceUnavailable,
	}
	hook := setupTestLoggerHook(t)
	auth := &Auth{ID: "auth-1", Provider: "antigravity"}
	executor := &requestPrepareExecutor{prepareErr: upstreamErr}
	selection := &HomeDispatchSelection{Auth: auth, Executor: executor, Provider: "antigravity"}
	_, errPrepare := NewManager(nil, nil, nil).prepareHomeRequestAuth(context.Background(), executor, selection)
	if errPrepare == nil || errPrepare.Error() != upstreamErr.message {
		t.Fatalf("operation error = %v, want generic error %q", errPrepare, upstreamErr.message)
	}

	matches := 0
	for _, entry := range hook.AllEntries() {
		if entry.Level != log.WarnLevel || !strings.HasPrefix(entry.Message, "Home credential operation failed: err=") {
			continue
		}
		matches++
		if !strings.Contains(entry.Message, "access token expired") {
			t.Fatalf("Warn log lost access-token-expired diagnostic: %q", entry.Message)
		}
		if strings.Contains(entry.Message, "provider-secret") || strings.ContainsAny(entry.Message, "\r\n") {
			t.Fatalf("Warn log included unsafe provider detail: %q", entry.Message)
		}
		if entry.Data["operation"] != "request_auth_preparation" || entry.Data["provider"] != "antigravity" || entry.Data["status"] != http.StatusServiceUnavailable {
			t.Fatalf("Warn log fields = %#v, want operation=request_auth_preparation provider=antigravity status=503", entry.Data)
		}
	}
	if matches != 1 {
		t.Fatalf("matching Warn logs = %d, want 1; logs=%#v", matches, hook.AllEntries())
	}
}

func TestWarnLogOnAuthUnavailable_SingleProvider(t *testing.T) {
	previousCooldown := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousCooldown) })

	hook := setupTestLoggerHook(t)
	m := NewManager(nil, nil, nil)

	now := time.Now()
	auth1 := &Auth{
		ID:            "auth-cooling-1",
		Provider:      "claude",
		Status:        StatusActive,
		FileName:      "claude-key-1.json",
		StatusMessage: "rate_limit_exceeded",
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "rate_limit_exceeded",
			NextRecoverAt: now.Add(45 * time.Second),
		},
		NextRetryAfter: now.Add(45 * time.Second),
	}
	auth2 := &Auth{
		ID:            "auth-cooling-2",
		Provider:      "claude",
		Status:        StatusActive,
		FileName:      "claude-key-2.json",
		StatusMessage: "quota_exceeded",
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota_exceeded",
			NextRecoverAt: now.Add(90 * time.Second),
		},
		NextRetryAfter: now.Add(90 * time.Second),
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3-5-sonnet"}})
	reg.RegisterClient(auth2.ID, "claude", []*registry.ModelInfo{{ID: "claude-3-5-sonnet"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	exec := &mockCustomErrorExecutor{
		identifier: "claude",
	}
	m.RegisterExecutor(exec)

	hook.Reset()

	req := cliproxyexecutor.Request{Model: "claude-3-5-sonnet"}
	opts := cliproxyexecutor.Options{}

	_, errExec := m.Execute(context.Background(), []string{"claude"}, req, opts)
	if errExec == nil {
		t.Fatal("expected error from Execute, got nil")
	}

	// Verify exactly one Warn line was emitted explaining the cooling auths
	warnCount := 0
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "auth unavailable") {
			warnCount++
			if !strings.Contains(entry.Message, "claude-key-1.json") ||
				!strings.Contains(entry.Message, "rate_limit_exceeded") ||
				!strings.Contains(entry.Message, "claude-key-2.json") ||
				!strings.Contains(entry.Message, "quota_exceeded") ||
				!strings.Contains(entry.Message, "remaining=") {
				t.Fatalf("unexpected Warn log content: %s", entry.Message)
			}
		}
	}
	if warnCount != 1 {
		t.Fatalf("expected exactly 1 Warn log, got %d. Logs: %#v", warnCount, hook.AllEntries())
	}
}

func TestWarnLogOnAuthUnavailable_SessionAffinityLegacyPath(t *testing.T) {
	previousCooldown := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousCooldown) })

	hook := setupTestLoggerHook(t)
	m := NewManager(nil, nil, nil)
	affinity := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer affinity.Stop()
	m.SetSelector(affinity)

	now := time.Now()
	auth1 := &Auth{
		ID:            "auth-legacy-cooling-1",
		Provider:      "claude",
		Status:        StatusActive,
		FileName:      "claude-legacy.json",
		StatusMessage: "rate_limit_exceeded",
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "rate_limit_exceeded",
			NextRecoverAt: now.Add(45 * time.Second),
		},
		NextRetryAfter: now.Add(45 * time.Second),
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3-5-sonnet"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}

	exec := &mockCustomErrorExecutor{
		identifier: "claude",
	}
	m.RegisterExecutor(exec)

	hook.Reset()

	req := cliproxyexecutor.Request{Model: "claude-3-5-sonnet"}
	opts := cliproxyexecutor.Options{}

	_, errExec := m.Execute(context.Background(), []string{"claude"}, req, opts)
	if errExec == nil {
		t.Fatal("expected error from Execute, got nil")
	}

	warnCount := 0
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "auth unavailable") {
			warnCount++
			if !strings.Contains(entry.Message, "claude-legacy.json") ||
				!strings.Contains(entry.Message, "rate_limit_exceeded") {
				t.Fatalf("unexpected Warn log content: %s", entry.Message)
			}
		}
	}
	if warnCount != 1 {
		t.Fatalf("expected exactly 1 Warn log from legacy path, got %d. Logs: %#v", warnCount, hook.AllEntries())
	}
}

func TestWarnLogOnAuthUnavailable_MixedProviders(t *testing.T) {
	previousCooldown := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousCooldown) })

	hook := setupTestLoggerHook(t)
	m := NewManager(nil, nil, nil)

	now := time.Now()
	auth1 := &Auth{
		ID:            "auth-claude-cooling",
		Provider:      "claude",
		Status:        StatusActive,
		FileName:      "claude.json",
		StatusMessage: "rate_limit",
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "rate_limit",
			NextRecoverAt: now.Add(30 * time.Second),
		},
		NextRetryAfter: now.Add(30 * time.Second),
	}
	auth2 := &Auth{
		ID:            "auth-codex-cooling",
		Provider:      "codex",
		Status:        StatusActive,
		FileName:      "codex.json",
		StatusMessage: "quota_exceeded",
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota_exceeded",
			NextRecoverAt: now.Add(60 * time.Second),
		},
		NextRetryAfter: now.Add(60 * time.Second),
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "gpt-5"}})
	reg.RegisterClient(auth2.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude"})
	m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex"})

	hook.Reset()

	req := cliproxyexecutor.Request{Model: "gpt-5"}
	opts := cliproxyexecutor.Options{}

	_, errExec := m.Execute(context.Background(), []string{"claude", "codex"}, req, opts)
	if errExec == nil {
		t.Fatal("expected error from Execute, got nil")
	}

	warnCount := 0
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "auth unavailable") {
			warnCount++
			if !strings.Contains(entry.Message, "claude.json") ||
				!strings.Contains(entry.Message, "codex.json") ||
				!strings.Contains(entry.Message, "providers=claude,codex") {
				t.Fatalf("unexpected mixed Warn log: %s", entry.Message)
			}
		}
	}
	if warnCount != 1 {
		t.Fatalf("expected exactly 1 mixed Warn log, got %d. Logs: %#v", warnCount, hook.AllEntries())
	}
}

func TestWarnLogOnUpstreamFailure_NonStream(t *testing.T) {
	previousCooldown := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousCooldown) })

	hook := setupTestLoggerHook(t)
	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:         "auth-test-upstream",
		Provider:   "codex",
		FileName:   "codex-prod.json",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-4o"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	exec := &mockCustomErrorExecutor{
		identifier: "codex",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			time.Sleep(5 * time.Millisecond)
			return cliproxyexecutor.Response{}, errors.New("500 Internal Server Error: upstream timeout")
		},
	}
	m.RegisterExecutor(exec)

	hook.Reset()

	req := cliproxyexecutor.Request{Model: "gpt-4o"}
	opts := cliproxyexecutor.Options{}

	_, errExec := m.Execute(context.Background(), []string{"codex"}, req, opts)
	if errExec == nil {
		t.Fatal("expected error, got nil")
	}

	foundWarn := false
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "upstream execution failed") {
			if strings.Contains(entry.Message, "provider=codex") &&
				strings.Contains(entry.Message, "model=gpt-4o") &&
				strings.Contains(entry.Message, "codex-prod.json") &&
				strings.Contains(entry.Message, "duration=") &&
				strings.Contains(entry.Message, "upstream timeout") {
				foundWarn = true
				break
			}
		}
	}
	if !foundWarn {
		t.Fatalf("expected Warn log detailing upstream failure, got logs: %#v", hook.AllEntries())
	}
}

func TestWarnLogOnUpstreamFailure_401RefreshSuccess_DoesNotLogWarn(t *testing.T) {
	previousCooldown := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousCooldown) })

	hook := setupTestLoggerHook(t)
	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:         "auth-test-401-refresh",
		Provider:   "codex",
		FileName:   "codex-oauth.json",
		Status:     StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth", "priority": "10"},
		Metadata:   map[string]any{"access_token": "old-token", "refresh_token": "valid-refresh-token"},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-4o"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	callCount := 0
	exec := &mockCustomErrorExecutor{
		identifier: "codex",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			callCount++
			if callCount == 1 {
				return cliproxyexecutor.Response{}, customStatusError{code: http.StatusUnauthorized, msg: "401 unauthorized"}
			}
			return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
		},
	}
	m.RegisterExecutor(exec)

	hook.Reset()

	req := cliproxyexecutor.Request{Model: "gpt-4o"}
	opts := cliproxyexecutor.Options{}

	resp, errExec := m.Execute(context.Background(), []string{"codex"}, req, opts)
	if errExec != nil {
		t.Fatalf("unexpected error from Execute: %v", errExec)
	}
	if string(resp.Payload) != `{"ok":true}` {
		t.Fatalf("unexpected response payload: %s", string(resp.Payload))
	}

	// 401 refresh was successful, so no upstream failure warning should be logged
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "upstream execution failed") {
			t.Fatalf("did not expect upstream failure warning when 401 refresh succeeded, got: %s", entry.Message)
		}
	}
}

func TestWarnLogOnUpstreamFailure_ClientCanceled_DoesNotLogWarn(t *testing.T) {
	previousCooldown := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousCooldown) })

	hook := setupTestLoggerHook(t)
	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:         "auth-test-canceled",
		Provider:   "codex",
		FileName:   "codex-prod.json",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-4o"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	exec := &mockCustomErrorExecutor{
		identifier: "codex",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, ctx.Err()
		},
	}
	m.RegisterExecutor(exec)

	hook.Reset()

	req := cliproxyexecutor.Request{Model: "gpt-4o"}
	opts := cliproxyexecutor.Options{}

	_, _ = m.Execute(ctx, []string{"codex"}, req, opts)

	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "upstream execution failed") {
			t.Fatalf("did not expect upstream failure warning on client cancellation, got: %s", entry.Message)
		}
	}
}

func TestWarnLogOnStreamUpstreamFailure(t *testing.T) {
	previousCooldown := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousCooldown) })

	hook := setupTestLoggerHook(t)
	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:         "auth-test-stream-upstream",
		Provider:   "claude",
		FileName:   "claude-stream.json",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "claude-sonnet-4"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	exec := &mockStreamErrorExecutor{
		identifier: "claude",
		executeStreamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			time.Sleep(5 * time.Millisecond)
			return nil, errors.New("502 Bad Gateway: connection dropped")
		},
	}
	m.RegisterExecutor(exec)

	hook.Reset()

	req := cliproxyexecutor.Request{Model: "claude-sonnet-4"}
	opts := cliproxyexecutor.Options{}

	_, errStream := m.ExecuteStream(context.Background(), []string{"claude"}, req, opts)
	if errStream == nil {
		t.Fatal("expected error from ExecuteStream, got nil")
	}

	foundWarn := false
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "upstream execution failed") {
			if strings.Contains(entry.Message, "provider=claude") &&
				strings.Contains(entry.Message, "model=claude-sonnet-4") &&
				strings.Contains(entry.Message, "claude-stream.json") &&
				strings.Contains(entry.Message, "duration=") &&
				strings.Contains(entry.Message, "connection dropped") {
				foundWarn = true
				break
			}
		}
	}
	if !foundWarn {
		t.Fatalf("expected Warn log detailing stream upstream failure, got logs: %#v", hook.AllEntries())
	}
}

func TestWarnLogOnStreamBootstrapFailure(t *testing.T) {
	previousCooldown := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousCooldown) })

	hook := setupTestLoggerHook(t)
	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:         "auth-test-bootstrap-upstream",
		Provider:   "claude",
		FileName:   "claude-bootstrap.json",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "claude-sonnet-4"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	exec := &mockStreamErrorExecutor{
		identifier: "claude",
		executeStreamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			ch := make(chan cliproxyexecutor.StreamChunk, 1)
			ch <- cliproxyexecutor.StreamChunk{Err: errors.New("504 Gateway Timeout: ttfb timeout")}
			close(ch)
			return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
		},
	}
	m.RegisterExecutor(exec)

	hook.Reset()

	req := cliproxyexecutor.Request{Model: "claude-sonnet-4"}
	opts := cliproxyexecutor.Options{}

	res, errStream := m.ExecuteStream(context.Background(), []string{"claude"}, req, opts)
	if errStream != nil {
		t.Fatalf("unexpected ExecuteStream bootstrap error: %v", errStream)
	}
	if res == nil || res.Chunks == nil {
		t.Fatal("expected non-nil StreamResult")
	}
	firstChunk := <-res.Chunks
	if firstChunk.Err == nil {
		t.Fatal("expected bootstrap chunk error, got nil")
	}

	foundWarn := false
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "upstream execution failed") {
			if strings.Contains(entry.Message, "provider=claude") &&
				strings.Contains(entry.Message, "model=claude-sonnet-4") &&
				strings.Contains(entry.Message, "claude-bootstrap.json") &&
				strings.Contains(entry.Message, "ttfb timeout") {
				foundWarn = true
				break
			}
		}
	}
	if !foundWarn {
		t.Fatalf("expected Warn log detailing stream bootstrap failure, got logs: %#v", hook.AllEntries())
	}
}

type mockStreamErrorExecutor struct {
	identifier      string
	executeStreamFn func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

func (e *mockStreamErrorExecutor) Identifier() string {
	if e.identifier != "" {
		return e.identifier
	}
	return "mock-stream"
}

func (e *mockStreamErrorExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *mockStreamErrorExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.executeStreamFn != nil {
		return e.executeStreamFn(ctx, auth, req, opts)
	}
	return nil, errors.New("not implemented")
}

func (e *mockStreamErrorExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *mockStreamErrorExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *mockStreamErrorExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}
