package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func intPtr(v int) *int { return &v }

func newProberManager() *Manager {
	m := NewManager(nil, nil, nil)
	m.SetProberParentContext(context.Background())
	return m
}

type proberTestExecutor struct {
	provider      string
	statusCode    *int
	body          string
	err           error
	calls         atomic.Int32
	respondStatus int
	deadlineSet   atomic.Bool
	ctx           context.Context
	blockUntil    chan struct{}
	mu            sync.Mutex
	lastURL       string

	refreshCalled atomic.Bool
	refreshToken  string
	refreshStatus int
	manager       *Manager
	replaceAuth   bool
}

func (e *proberTestExecutor) Identifier() string { return e.provider }

func (e *proberTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *proberTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *proberTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	if e.refreshToken == "" {
		return nil, nil
	}
	e.refreshCalled.Store(true)
	refreshed := auth.Clone()
	if refreshed.Metadata == nil {
		refreshed.Metadata = map[string]any{}
	}
	refreshed.Metadata["access_token"] = e.refreshToken
	return refreshed, nil
}

func (e *proberTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *proberTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	e.calls.Add(1)
	e.ctx = ctx
	if req != nil {
		e.mu.Lock()
		e.lastURL = req.URL.String()
		e.mu.Unlock()
	}
	if e.manager != nil && e.replaceAuth && auth != nil {
		e.manager.Register(ctx, auth.Clone())
	}
	if _, ok := ctx.Deadline(); ok {
		e.deadlineSet.Store(true)
	}
	if e.blockUntil != nil {
		<-e.blockUntil
	}
	if e.err != nil {
		return nil, e.err
	}
	e.mu.Lock()
	status := 0
	if e.statusCode != nil {
		status = *e.statusCode
	}
	if status <= 0 {
		status = e.respondStatus
	}
	if status <= 0 {
		status = http.StatusOK
	}
	if e.refreshCalled.Load() && e.refreshStatus > 0 {
		status = e.refreshStatus
	}
	body := e.body
	if body == "" {
		body = "{}"
	}
	e.mu.Unlock()
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestProberDisabledByDefault(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test"}
	m.RegisterExecutor(exec)

	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: false}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)
	if exec.calls.Load() != 0 {
		t.Fatalf("prober called disabled executor %d times", exec.calls.Load())
	}
}

func TestProberSkipsDisabledAuth(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test", err: fmt.Errorf("boom")}
	m.RegisterExecutor(exec)

	auth := &Auth{ID: "a1", Provider: "test", Status: StatusDisabled, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)
	if exec.calls.Load() != 0 {
		t.Fatalf("prober probed disabled auth %d times", exec.calls.Load())
	}
}

func TestProberMarksAuthUnavailableOnFailure(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test", err: fmt.Errorf("upstream unreachable")}
	m.RegisterExecutor(exec)

	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	// wait for the immediate first sweep
	time.Sleep(100 * time.Millisecond)

	m.mu.RLock()
	updated := m.auths["a1"]
	m.mu.RUnlock()

	if updated == nil {
		t.Fatal("auth disappeared")
	}
	if exec.calls.Load() != 1 {
		t.Fatalf("prober calls = %d, want 1", exec.calls.Load())
	}
	if !updated.Unavailable {
		t.Fatalf("auth.Unavailable = %v, want true", updated.Unavailable)
	}
	if updated.Status != StatusError {
		t.Fatalf("auth.Status = %v, want %v", updated.Status, StatusError)
	}
	if updated.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter not set after prober failure")
	}
}

func TestProberLeavesAuthActiveOnSuccess(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test", statusCode: intPtr(http.StatusOK)}
	m.RegisterExecutor(exec)

	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)

	m.mu.RLock()
	updated := m.auths["a1"]
	m.mu.RUnlock()

	if updated == nil {
		t.Fatal("auth disappeared")
	}
	if exec.calls.Load() != 1 {
		t.Fatalf("prober calls = %d, want 1", exec.calls.Load())
	}
	if updated.Unavailable {
		t.Fatalf("auth.Unavailable = %v, want false", updated.Unavailable)
	}
	if updated.Status != StatusActive {
		t.Fatalf("auth.Status = %v, want %v", updated.Status, StatusActive)
	}
}

func TestProberUsesCanonicalExecutorKey(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()

	// Executor is registered under the lower-cased canonical key.
	exec := &proberTestExecutor{provider: "openai", statusCode: intPtr(http.StatusOK)}
	m.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "a1",
		Provider: "OpenAI",
		Status:   StatusActive,
		Attributes: map[string]string{
			"base_url": "https://example.com",
		},
	}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)

	if exec.calls.Load() != 1 {
		t.Fatalf("prober calls = %d, want 1", exec.calls.Load())
	}
}

func TestProberUsesCompatNameExecutorKey(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()

	exec := &proberTestExecutor{provider: "openai-compatible-custom", statusCode: intPtr(http.StatusOK)}
	m.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "a1",
		Provider: "openai-compatibility",
		Label:    "custom",
		Status:   StatusActive,
		Attributes: map[string]string{
			"base_url": "https://example.com",
		},
	}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)

	if exec.calls.Load() != 1 {
		t.Fatalf("prober calls = %d, want 1", exec.calls.Load())
	}
}

func TestProberAcceptsNoContentResponse(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test", statusCode: intPtr(http.StatusNoContent)}
	m.RegisterExecutor(exec)

	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)

	m.mu.RLock()
	updated := m.auths["a1"]
	m.mu.RUnlock()

	if updated == nil {
		t.Fatal("auth disappeared")
	}
	if updated.Unavailable {
		t.Fatalf("auth.Unavailable = %v, want false", updated.Unavailable)
	}
	if updated.proberBackoff != 0 {
		t.Fatalf("auth.proberBackoff = %d, want 0", updated.proberBackoff)
	}
}

func TestProberDropsContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()

	m := newProberManager()
	exec := &proberTestExecutor{provider: "test", statusCode: intPtr(http.StatusOK)}
	m.RegisterExecutor(exec)

	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)

	m.StopProber()

	if exec.calls.Load() == 0 {
		t.Fatal("prober did not call executor")
	}
	if exec.deadlineSet.Load() {
		t.Fatal("prober passed a context with a deadline to the executor")
	}
	if exec.ctx != nil {
		if _, ok := exec.ctx.Deadline(); ok {
			t.Fatal("prober passed a context with a deadline to the executor")
		}
	}
}

func TestProberStopWaitsForInFlightProbe(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	block := make(chan struct{})
	exec := &proberTestExecutor{provider: "test", statusCode: intPtr(http.StatusOK), blockUntil: block}
	m.RegisterExecutor(exec)

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.StartProber(ctx, cfg)

	for exec.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	stopDone := make(chan struct{})
	go func() {
		m.StopProber()
		close(stopDone)
	}()

	// StopProber must not return while the probe is still blocked.
	select {
	case <-stopDone:
		t.Fatal("StopProber returned before the in-flight probe completed")
	case <-time.After(100 * time.Millisecond):
	}

	close(block)
	<-stopDone
}

func TestProberRestartDuringStopIsBlocked(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	block := make(chan struct{})
	first := &proberTestExecutor{provider: "test", statusCode: intPtr(http.StatusOK), blockUntil: block}
	m.RegisterExecutor(first)

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.StartProber(ctx, cfg)

	for first.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	stopDone := make(chan struct{})
	go func() {
		m.StopProber()
		close(stopDone)
	}()

	startDone := make(chan struct{})
	go func() {
		m.StartProber(ctx, cfg)
		close(startDone)
	}()

	// StartProber must not start a new prober while StopProber holds the lock.
	select {
	case <-startDone:
		t.Fatal("StartProber returned before StopProber released the lock")
	case <-time.After(50 * time.Millisecond):
	}

	close(block)
	<-stopDone
	<-startDone
}

func TestSetConfigDoesNotDeadlockWithFailingProbe(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	m.SetCooldownStateStore(&recordingCooldownStateStore{})

	block := make(chan struct{})
	exec := &proberTestExecutor{provider: "test", err: fmt.Errorf("upstream unreachable"), blockUntil: block}
	m.RegisterExecutor(exec)

	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	for exec.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	done := make(chan struct{})
	go func() {
		m.SetConfig(&internalconfig.Config{CredentialProber: cfg})
		close(done)
	}()

	// Give SetConfig time to reach the restart, then let the probe finish.
	time.Sleep(50 * time.Millisecond)
	close(block)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SetConfig deadlocked with failing probe")
	}
}

func TestProberDoesNotStartBeforeParentContext(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)
	exec := &proberTestExecutor{provider: "test", statusCode: intPtr(http.StatusOK)}
	m.RegisterExecutor(exec)

	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)
	if exec.calls.Load() != 0 {
		t.Fatalf("prober calls = %d, want 0 before parent context is set", exec.calls.Load())
	}

	m.SetProberParentContext(ctx)
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)
	if exec.calls.Load() != 1 {
		t.Fatalf("prober calls = %d, want 1 after parent context is set", exec.calls.Load())
	}
}

func TestProberUsesProviderSpecificProbePath(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "gemini", statusCode: intPtr(http.StatusOK)}
	m.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "g1",
		Provider: "gemini",
		Status:   StatusActive,
		Attributes: map[string]string{
			"base_url": "https://generativelanguage.googleapis.com",
		},
	}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)
	if exec.calls.Load() != 1 {
		t.Fatalf("prober calls = %d, want 1", exec.calls.Load())
	}
	exec.mu.Lock()
	lastURL := exec.lastURL
	exec.mu.Unlock()
	if !strings.Contains(lastURL, "/v1beta/models") {
		t.Fatalf("probe URL = %q, want /v1beta/models path", lastURL)
	}
}

func TestProberIgnoresCancellationResult(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	block := make(chan struct{})
	exec := &proberTestExecutor{provider: "test", blockUntil: block}
	m.RegisterExecutor(exec)

	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.StartProber(ctx, cfg)

	for exec.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	stopDone := make(chan struct{})
	go func() {
		m.StopProber()
		close(stopDone)
	}()

	time.Sleep(50 * time.Millisecond)
	close(block)

	select {
	case <-stopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("StopProber timed out")
	}

	m.mu.RLock()
	updated := m.auths["a1"]
	m.mu.RUnlock()
	if updated != nil && (updated.Unavailable || !updated.NextRetryAfter.IsZero()) {
		t.Fatalf("canceled probe should not mark auth unavailable; got Unavailable=%v NextRetryAfter=%v", updated.Unavailable, updated.NextRetryAfter)
	}
}

func TestProberDiscardsResultIfAuthReplaced(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	block := make(chan struct{})
	exec := &proberTestExecutor{provider: "test", err: fmt.Errorf("unauthorized"), blockUntil: block}
	m.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "a1",
		Provider: "test",
		Status:   StatusActive,
		Attributes: map[string]string{
			"base_url": "https://example.com",
		},
	}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.StartProber(ctx, cfg)

	for exec.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	auth2 := auth.Clone()
	auth2.Metadata = map[string]any{"token": "refreshed"}
	if _, err := m.Register(ctx, auth2); err != nil {
		t.Fatalf("Register replacement: %v", err)
	}

	close(block)
	time.Sleep(100 * time.Millisecond)
	m.StopProber()

	updated, ok := m.GetByID("a1")
	if !ok {
		t.Fatal("auth not found")
	}
	if updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("probe result for stale credential should have been discarded; got Unavailable=%v NextRetryAfter=%v", updated.Unavailable, updated.NextRetryAfter)
	}
}

func TestProberBackoffFor(t *testing.T) {
	l := newAuthProberLoop(nil, internalconfig.CredentialProberConfig{BackoffBase: 30 * time.Second, BackoffMax: 5 * time.Minute})
	cases := []struct {
		level int
		want  time.Duration
	}{
		{0, 30 * time.Second},
		{1, 1 * time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 5 * time.Minute},
		{5, 5 * time.Minute},
		{10, 5 * time.Minute},
	}
	for _, c := range cases {
		got := l.proberBackoffFor(c.level)
		if got != c.want {
			t.Fatalf("proberBackoffFor(%d) = %v, want %v", c.level, got, c.want)
		}
	}
}

func TestProberBackoffOverridesStatusCooldowns(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	max := 5 * time.Minute
	m.MarkResult(ctx, Result{
		AuthID:          auth.ID,
		Provider:        auth.Provider,
		Success:         false,
		CredentialScope: true,
		Error: &Error{
			Code:       ErrorCodeForceCooldown,
			Message:    "prober: upstream returned 401",
			HTTPStatus: http.StatusUnauthorized,
			Retryable:  true,
		},
		RetryAfter: durationPtr(30 * time.Second),
	})

	m.mu.RLock()
	updated := m.auths["a1"]
	m.mu.RUnlock()
	if updated == nil {
		t.Fatal("auth disappeared")
	}
	if got := time.Until(updated.NextRetryAfter); got > max || got < 0 {
		t.Fatalf("NextRetryAfter = %v, want <= %v", updated.NextRetryAfter, max)
	}
}

func TestProberBackoffEscalates(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{BackoffBase: 30 * time.Second, BackoffMax: 5 * time.Minute}
	l := newAuthProberLoop(m, cfg)

	for i := 0; i < 6; i++ {
		d := l.proberBackoffFor(i)
		m.MarkResult(ctx, Result{
			AuthID:          auth.ID,
			Provider:        auth.Provider,
			Success:         false,
			CredentialScope: true,
			Error:           &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusUnauthorized, Retryable: true},
			RetryAfter:      &d,
		})
	}

	m.mu.RLock()
	updated := m.auths["a1"]
	m.mu.RUnlock()
	if updated == nil {
		t.Fatal("auth disappeared")
	}
	if got := time.Until(updated.NextRetryAfter); got > cfg.BackoffMax {
		t.Fatalf("NextRetryAfter = %v, want <= %v", updated.NextRetryAfter, cfg.BackoffMax)
	}
	if updated.proberBackoff != 6 {
		t.Fatalf("proberBackoff = %d, want 6", updated.proberBackoff)
	}
}

func durationPtr(d time.Duration) *time.Duration { return &d }

func TestProberProbePathForProvider(t *testing.T) {
	cases := []struct {
		provider   string
		configured string
		want       string
	}{
		{"gemini", "/models", "/v1beta/models"},
		{"Gemini", "", "/v1beta/models"},
		{"aistudio", "/models", "/v1beta/models"},
		{"xai", "/models", "/v1/models"},
		{"kimi", "/models", "/v1/models"},
		{"openai-compatible-groq", "/models", "/v1/models"},
		{"openai-compatibility", "", "/v1/models"},
		{"test", "/models", "/models"},
		{"test", "", ""},
	}
	for _, c := range cases {
		if got := proberProbePathForProvider(c.provider, c.configured); got != c.want {
			t.Fatalf("proberProbePathForProvider(%q, %q) = %q, want %q", c.provider, c.configured, got, c.want)
		}
	}
}

func TestProberRedactsTransportErrorURL(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test", err: fmt.Errorf("Get \"https://example.com/v1/models?api_key=super-secret-token&x=1\": connection refused")}
	m.RegisterExecutor(exec)

	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)

	updated, _ := m.GetByID(auth.ID)
	if updated == nil || updated.LastError == nil {
		t.Fatal("expected LastError")
	}
	if strings.Contains(updated.LastError.Message, "api_key=super-secret-token") || strings.Contains(updated.LastError.Message, "super-secret") {
		t.Fatalf("LastError message leaks token: %s", updated.LastError.Message)
	}
}

func TestProberUsesDefaultBaseURL(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "claude"}
	m.RegisterExecutor(exec)

	auth := &Auth{ID: "a1", Provider: "claude", Status: StatusActive}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)

	if exec.calls.Load() != 1 {
		t.Fatalf("prober calls = %d, want 1", exec.calls.Load())
	}
	exec.mu.Lock()
	url := exec.lastURL
	exec.mu.Unlock()
	if url != "https://api.anthropic.com/v1/models" {
		t.Fatalf("prober URL = %q, want %q", url, "https://api.anthropic.com/v1/models")
	}
}

func TestProberDoesNotCountStats(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test", statusCode: intPtr(http.StatusInternalServerError)}
	m.RegisterExecutor(exec)

	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)

	updated, _ := m.GetByID(auth.ID)
	if updated == nil {
		t.Fatal("auth disappeared")
	}
	if !updated.Unavailable {
		t.Fatalf("auth.Unavailable = %v, want true", updated.Unavailable)
	}
	if updated.Success != 0 || updated.Failed != 0 {
		t.Fatalf("auth stats = %d/%d, want 0/0", updated.Success, updated.Failed)
	}
}

func TestProberClearsProberStateOnRecovery(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test", statusCode: intPtr(http.StatusInternalServerError)}
	m.RegisterExecutor(exec)

	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{
		Enabled:            true,
		Interval:           50 * time.Millisecond,
		MaxConcurrency:     1,
		RateLimitPerMinute: 1000,
		BackoffBase:        25 * time.Millisecond,
		BackoffMax:         100 * time.Millisecond,
	}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)

	updated, _ := m.GetByID(auth.ID)
	if updated == nil || !updated.Unavailable {
		t.Fatal("auth should be unavailable after first failure")
	}

	exec.mu.Lock()
	*exec.statusCode = http.StatusOK
	exec.mu.Unlock()
	time.Sleep(200 * time.Millisecond)

	updated, _ = m.GetByID(auth.ID)
	if updated == nil {
		t.Fatal("auth disappeared")
	}
	if updated.Unavailable {
		t.Fatalf("auth.Unavailable = %v, want false", updated.Unavailable)
	}
	if updated.proberBackoff != 0 {
		t.Fatalf("auth.proberBackoff = %d, want 0", updated.proberBackoff)
	}
	if updated.LastError != nil && strings.Contains(updated.LastError.Message, "prober:") {
		t.Fatalf("auth.LastError not cleared: %v", updated.LastError)
	}
}

func TestProberClampsRateLimit(t *testing.T) {
	m := newProberManager()
	m.SetConfig(&internalconfig.Config{CredentialProber: internalconfig.CredentialProberConfig{
		Enabled:            true,
		Interval:           time.Hour,
		MaxConcurrency:     1,
		RateLimitPerMinute: 100_000_000_000,
	}})
	time.Sleep(50 * time.Millisecond)
	m.mu.RLock()
	l := m.proberLoop
	m.mu.RUnlock()
	if l == nil {
		t.Fatal("prober not started")
	}
	if l.cfg.RateLimitPerMinute != proberMaxRateLimitPerMinute {
		t.Fatalf("RateLimitPerMinute = %d, want %d", l.cfg.RateLimitPerMinute, proberMaxRateLimitPerMinute)
	}
}

func TestProberRefreshOn401(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{
		provider:      "test",
		statusCode:    intPtr(http.StatusUnauthorized),
		refreshToken:  "new-token",
		refreshStatus: http.StatusOK,
	}
	m.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "a1",
		Provider: "test",
		Status:   StatusActive,
		Attributes: map[string]string{
			"base_url": "https://example.com",
		},
		Metadata: map[string]any{"refresh_token": "old-token"},
	}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(200 * time.Millisecond)

	if !exec.refreshCalled.Load() {
		t.Fatal("Refresh not called on 401")
	}
	if exec.calls.Load() < 2 {
		t.Fatalf("prober calls = %d, want >= 2", exec.calls.Load())
	}
	updated, _ := m.GetByID(auth.ID)
	if updated == nil || updated.Unavailable {
		t.Fatalf("auth should recover after refresh; Unavailable = %v", updated.Unavailable)
	}
}

func TestProberDiscardsStaleAuthResult(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test", statusCode: intPtr(http.StatusInternalServerError), manager: m, replaceAuth: true}
	m.RegisterExecutor(exec)

	auth := &Auth{ID: "a1", Provider: "test", Status: StatusActive, Attributes: map[string]string{"base_url": "https://example.com"}}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)

	updated, _ := m.GetByID(auth.ID)
	if updated == nil {
		t.Fatal("auth disappeared")
	}
	if updated.Unavailable {
		t.Fatalf("stale probe result should be discarded; Unavailable = %v", updated.Unavailable)
	}
}

type proberCallbackHook struct {
	didCall int32
	manager *Manager
	cfg     internalconfig.CredentialProberConfig
}

func (h *proberCallbackHook) OnAuthRegistered(ctx context.Context, auth *Auth) {
	atomic.AddInt32(&h.didCall, 1)
	h.manager.SetConfig(&internalconfig.Config{CredentialProber: h.cfg})
}

func (h *proberCallbackHook) OnAuthUpdated(ctx context.Context, auth *Auth) {}
func (h *proberCallbackHook) OnResult(ctx context.Context, result Result) {
	if !result.Success {
		h.manager.SetConfig(&internalconfig.Config{CredentialProber: h.cfg})
	}
}

func TestProberSetConfigFromCallbackDoesNotDeadlock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	h := &proberCallbackHook{cfg: cfg}
	m := NewManager(nil, nil, h)
	h.manager = m
	m.SetProberParentContext(ctx)

	exec := &proberTestExecutor{
		provider:      "test",
		statusCode:    intPtr(http.StatusUnauthorized),
		refreshToken:  "new-token",
		refreshStatus: http.StatusOK,
	}
	m.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "a1",
		Provider: "test",
		Status:   StatusActive,
		Attributes: map[string]string{
			"base_url": "https://example.com",
		},
		Metadata: map[string]any{"refresh_token": "old-token"},
	}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	done := make(chan struct{})
	go func() {
		m.SetConfig(&internalconfig.Config{CredentialProber: cfg})
		time.Sleep(100 * time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("deadlock: prober callback blocked on SetConfig/RestartProber")
	}
}

func TestResolveProbeURL(t *testing.T) {
	cases := []struct {
		baseURL string
		path    string
		want    string
	}{
		{"https://api.openai.com/v1", "/models", "https://api.openai.com/v1/models"},
		{"https://api.x.ai/v1", "/models", "https://api.x.ai/v1/models"},
		{"https://generativelanguage.googleapis.com/v1beta", "/models", "https://generativelanguage.googleapis.com/v1beta/models"},
		{"https://example.com", "/models", "https://example.com/models"},
		{"https://example.com/v1", "/v1/models", "https://example.com/v1/models"},
		{"https://example.com/v1/", "/v1/models", "https://example.com/v1/models"},
		{"https://example.com/v1/models", "/models", "https://example.com/v1/models"},
		{"https://example.com", "/v1/models", "https://example.com/v1/models"},
	}

	for _, c := range cases {
		got, err := resolveProbeURL(c.baseURL, c.path)
		if err != nil {
			t.Fatalf("resolveProbeURL(%q, %q): %v", c.baseURL, c.path, err)
		}
		if got != c.want {
			t.Fatalf("resolveProbeURL(%q, %q) = %q, want %q", c.baseURL, c.path, got, c.want)
		}
	}
}
