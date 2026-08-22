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
	statusSeq     []int
	body          string
	err           error
	calls         atomic.Int32
	respondStatus int
	deadlineSet   atomic.Bool
	ctx           context.Context
	blockUntil    chan struct{}
	mu            sync.Mutex
	lastURL       string
	lastHeaders   http.Header

	refreshCalled   atomic.Bool
	refreshToken    string
	refreshStatus   int
	manager         *Manager
	replaceAuth     bool
	lastRefreshAuth *Auth
}

func (e *proberTestExecutor) Identifier() string { return e.provider }

func (e *proberTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *proberTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *proberTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalled.Store(true)
	e.mu.Lock()
	e.lastRefreshAuth = auth
	e.mu.Unlock()
	if e.refreshToken == "" {
		return nil, nil
	}
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
		e.lastHeaders = req.Header.Clone()
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
	callIdx := int(e.calls.Load() - 1)
	if callIdx >= 0 && callIdx < len(e.statusSeq) {
		status = e.statusSeq[callIdx]
	} else if e.statusCode != nil {
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
	e.mu.Unlock()
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestProberHonorsMetadataBaseURL(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "xai"}
	m.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "a1",
		Provider: "xai",
		Status:   StatusActive,
		Metadata: map[string]any{
			"base_url": "https://custom.x.ai",
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
	url := exec.lastURL
	exec.mu.Unlock()
	if url != "https://custom.x.ai/v1/models" {
		t.Fatalf("prober URL = %q, want %q", url, "https://custom.x.ai/v1/models")
	}
}

func TestProberSetsAnthropicVersionHeaderForClaude(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "claude"}
	m.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "a1",
		Provider: "claude",
		Status:   StatusActive,
		Attributes: map[string]string{
			"base_url": "https://api.anthropic.com",
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
	defer exec.mu.Unlock()
	if exec.lastURL != "https://api.anthropic.com/v1/models" {
		t.Fatalf("prober URL = %q, want %q", exec.lastURL, "https://api.anthropic.com/v1/models")
	}
	if got := exec.lastHeaders.Get("Anthropic-Version"); got != "2023-06-01" {
		t.Fatalf("Anthropic-Version header = %q, want 2023-06-01", got)
	}
}

func TestProberAcceptsEmpty200(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test", statusCode: intPtr(http.StatusOK)}
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
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)

	updated, _ := m.GetByID(auth.ID)
	if updated == nil || updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("empty 200 should not force cooldown; Unavailable=%v NextRetryAfter=%v", updated.Unavailable, updated.NextRetryAfter)
	}
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
			IsProbe:         true,
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

func TestProberClampsMinimumInterval(t *testing.T) {
	m := newProberManager()
	m.SetConfig(&internalconfig.Config{CredentialProber: internalconfig.CredentialProberConfig{
		Enabled:            true,
		Interval:           1,
		MaxConcurrency:     1,
		RateLimitPerMinute: 1000,
	}})
	time.Sleep(20 * time.Millisecond)
	m.mu.RLock()
	l := m.proberLoop
	m.mu.RUnlock()
	if l == nil {
		t.Fatal("prober not started")
	}
	if l.cfg.Interval != proberMinInterval {
		t.Fatalf("Interval = %v, want %v", l.cfg.Interval, proberMinInterval)
	}
}

func TestProberBaseURLCaseInsensitiveOpenAICompatName(t *testing.T) {
	cfg := &internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name:    "MyOpenAI",
				BaseURL: "https://custom.example.com/v1",
			},
		},
	}

	auth := &Auth{
		ID:       "a1",
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"compat_name": "myopenai",
		},
	}
	if got := proberBaseURLForProvider(auth, cfg); got != "https://custom.example.com/v1" {
		t.Fatalf("proberBaseURLForProvider() = %q, want %q", got, "https://custom.example.com/v1")
	}

	auth.Attributes["compat_name"] = "MYOPENAI"
	if got := proberBaseURLForProvider(auth, cfg); got != "https://custom.example.com/v1" {
		t.Fatalf("proberBaseURLForProvider() = %q, want %q", got, "https://custom.example.com/v1")
	}

	auth.Attributes["provider_key"] = "MyOpenAI"
	auth.Attributes["compat_name"] = ""
	if got := proberBaseURLForProvider(auth, cfg); got != "https://custom.example.com/v1" {
		t.Fatalf("proberBaseURLForProvider() = %q, want %q", got, "https://custom.example.com/v1")
	}
}

func TestProberBaseURLIgnoresOpenAICompatIndexForNativeProvider(t *testing.T) {
	cfg := &internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name:    "native-looking",
				BaseURL: "https://compat.example.com/v1",
			},
		},
	}

	auth := &Auth{
		ID:       "a1",
		Provider: "claude",
		Attributes: map[string]string{
			"config_index": "0",
		},
	}
	if got := proberBaseURLForProvider(auth, cfg); got != "https://api.anthropic.com" {
		t.Fatalf("proberBaseURLForProvider() = %q, want %q", got, "https://api.anthropic.com")
	}
	if got := proberOpenAICompatBaseURL(auth, cfg); got != "" {
		t.Fatalf("proberOpenAICompatBaseURL() = %q, want empty", got)
	}
}

func TestProberXAIBaseURL(t *testing.T) {
	cases := []struct {
		name string
		auth *Auth
		want string
	}{
		{
			name: "oauth default skipped",
			auth: &Auth{Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth"}},
			want: "",
		},
		{
			name: "oauth default official base_url skipped",
			auth: &Auth{Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth", "base_url": "https://api.x.ai/v1"}},
			want: "",
		},
		{
			name: "api-key default uses official api",
			auth: &Auth{Provider: "xai", Attributes: map[string]string{"api_key": "secret"}},
			want: "https://api.x.ai/v1",
		},
		{
			name: "using_api true keeps official base",
			auth: &Auth{Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth", "using_api": "true", "base_url": "https://api.x.ai/v1"}},
			want: "https://api.x.ai/v1",
		},
		{
			name: "oauth explicit custom base_url probed",
			auth: &Auth{Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth", "base_url": "https://gateway.example.com/v1"}},
			want: "https://gateway.example.com/v1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := proberBaseURLForProvider(c.auth, nil); got != c.want {
				t.Fatalf("proberBaseURLForProvider() = %q, want %q", got, c.want)
			}
		})
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

func TestProber401DoesNotRefreshAPIKeyCredential(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{
		provider:   "test",
		statusCode: intPtr(http.StatusUnauthorized),
	}
	m.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "a1",
		Provider: "test",
		Status:   StatusActive,
		Attributes: map[string]string{
			"base_url": "https://example.com",
			"api_key":  "secret",
		},
	}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(200 * time.Millisecond)

	if exec.refreshCalled.Load() {
		t.Fatal("Refresh called for API-key credential with no refresh_token")
	}
	updated, _ := m.GetByID(auth.ID)
	if updated == nil || !updated.Unavailable {
		t.Fatalf("API-key 401 should force-cool the credential; Unavailable = %v", updated != nil && updated.Unavailable)
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

func TestProbeHookReceivesDetachedContext(t *testing.T) {
	hookCtxErr := make(chan error, 1)
	hook := &contextCheckingHook{errC: hookCtxErr}
	m := NewManager(nil, nil, hook)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before MarkResult so the synchronous path would see ctx.Err()

	m.MarkResult(ctx, Result{
		AuthID:   "a1",
		Provider: "test",
		Success:  false,
		IsProbe:  true,
		Error: &Error{
			Code:       ErrorCodeForceCooldown,
			Message:    "prober: unreachable",
			HTTPStatus: http.StatusServiceUnavailable,
		},
	})

	select {
	case err := <-hookCtxErr:
		if err != nil {
			t.Fatalf("probe hook received canceled context: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("probe hook was not called")
	}
}

type contextCheckingHook struct {
	errC chan error
}

func (h *contextCheckingHook) OnAuthRegistered(context.Context, *Auth) {}
func (h *contextCheckingHook) OnAuthUpdated(context.Context, *Auth)    {}
func (h *contextCheckingHook) OnResult(ctx context.Context, result Result) {
	if err := ctx.Err(); err != nil {
		h.errC <- err
		return
	}
	h.errC <- nil
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

func TestProberSkipsCodexWithoutBaseURL(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "codex"}
	m.RegisterExecutor(exec)

	auth := &Auth{ID: "a1", Provider: "codex", Status: StatusActive}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)
	if exec.calls.Load() != 0 {
		t.Fatalf("prober calls = %d, want 0 for codex without base_url", exec.calls.Load())
	}
}

func TestProberUsesOpenAICompatBaseURL(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "openai-compatible-my-comp"}
	m.RegisterExecutor(exec)

	cfg := &internalconfig.Config{
		CredentialProber: internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000},
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{Name: "my-comp", BaseURL: "https://custom.example.com"},
		},
	}
	m.SetConfig(cfg)

	auth := &Auth{
		ID:       "a1",
		Provider: "openai-compatibility",
		Status:   StatusActive,
		Attributes: map[string]string{
			"compat_name": "my-comp",
		},
	}
	if _, err := m.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	if exec.calls.Load() != 1 {
		t.Fatalf("prober calls = %d, want 1", exec.calls.Load())
	}
	exec.mu.Lock()
	url := exec.lastURL
	exec.mu.Unlock()
	if url != "https://custom.example.com/v1/models" {
		t.Fatalf("prober URL = %q, want %q", url, "https://custom.example.com/v1/models")
	}
}

func TestProberRefreshOn401WaitsForRateLimitToken(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{
		provider:     "test",
		statusSeq:    []int{http.StatusUnauthorized, http.StatusOK},
		refreshToken: "new-token",
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
	loop := newAuthProberLoop(m, cfg)

	limiter := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		loop.probeWithLimiter(ctx, auth, limiter)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	if got := exec.calls.Load(); got != 1 {
		t.Fatalf("prober calls before retry = %d, want 1", got)
	}

	select {
	case <-done:
		t.Fatal("probe finished before retry token was provided")
	default:
	}

	limiter <- time.Now()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("probe did not complete after retry token")
	}

	if got := exec.calls.Load(); got != 2 {
		t.Fatalf("prober calls = %d, want 2", got)
	}
	if !exec.refreshCalled.Load() {
		t.Fatal("Refresh not called on 401")
	}
}

func TestRestartProberReadsConfigAfterLifecycleLock(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test"}
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

	enabled := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	disabled := internalconfig.CredentialProberConfig{Enabled: false}

	m.SetProberParentContext(ctx)
	m.SetConfig(&internalconfig.Config{CredentialProber: enabled})
	time.Sleep(100 * time.Millisecond)

	m.mu.RLock()
	runningBefore := m.proberLoop != nil
	m.mu.RUnlock()
	if !runningBefore {
		t.Fatal("prober should be running with enabled config")
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); m.SetConfig(&internalconfig.Config{CredentialProber: enabled}) }()
		go func() { defer wg.Done(); m.RestartProber() }()
	}
	wg.Wait()

	m.SetConfig(&internalconfig.Config{CredentialProber: disabled})
	m.RestartProber()
	time.Sleep(100 * time.Millisecond)

	m.mu.RLock()
	runningAfter := m.proberLoop != nil
	m.mu.RUnlock()
	if runningAfter {
		t.Fatal("prober should be stopped with disabled config")
	}
}

func TestProberRefreshOn401DoesNotPassLiveAuthPointer(t *testing.T) {
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
	exec.mu.Lock()
	lastRefreshAuth := exec.lastRefreshAuth
	exec.mu.Unlock()
	if lastRefreshAuth == auth {
		t.Fatal("Refresh received live auth pointer, want a clone")
	}
	updated, _ := m.GetByID(auth.ID)
	if updated == nil || updated.Unavailable {
		t.Fatalf("auth should recover after refresh; Unavailable = %v", updated.Unavailable)
	}
}

func TestProberReResolvesExecutorAfterAuthReplacement(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	oldExec := &proberTestExecutor{provider: "test"}
	newExec := &proberTestExecutor{provider: "new"}
	m.RegisterExecutor(oldExec)
	m.RegisterExecutor(newExec)

	oldAuth := &Auth{
		ID:       "a1",
		Provider: "test",
		Status:   StatusActive,
		Attributes: map[string]string{
			"base_url": "https://old.example.com",
		},
	}
	if _, err := m.Register(ctx, oldAuth); err != nil {
		t.Fatalf("Register old auth: %v", err)
	}

	newAuth := oldAuth.Clone()
	newAuth.Provider = "new"
	newAuth.Attributes = map[string]string{
		"base_url": "https://new.example.com",
	}
	if _, err := m.Register(ctx, newAuth); err != nil {
		t.Fatalf("Register replacement auth: %v", err)
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	loop := newAuthProberLoop(m, cfg)
	loop.probe(ctx, oldAuth)

	if oldExec.calls.Load() != 0 {
		t.Fatalf("old exec calls = %d, want 0", oldExec.calls.Load())
	}
	if newExec.calls.Load() != 1 {
		t.Fatalf("new exec calls = %d, want 1", newExec.calls.Load())
	}
	newExec.mu.Lock()
	url := newExec.lastURL
	newExec.mu.Unlock()
	if url != "https://new.example.com/models" {
		t.Fatalf("prober URL = %q, want %q", url, "https://new.example.com/models")
	}
}

func TestProberRestartGoroutineDoesNotSurviveParentCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test", statusCode: intPtr(http.StatusOK)}
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

	m.SetProberParentContext(ctx)
	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)
	if exec.calls.Load() != 1 {
		t.Fatalf("prober calls = %d, want 1 before parent cancel", exec.calls.Load())
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
	m.StopProber()

	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})
	time.Sleep(100 * time.Millisecond)

	m.mu.RLock()
	loop := m.proberLoop
	m.mu.RUnlock()
	if loop != nil {
		t.Fatal("prober restarted after parent context was canceled")
	}
	if exec.calls.Load() != 1 {
		t.Fatalf("prober calls = %d, want 1 after canceled parent restart", exec.calls.Load())
	}
}

func TestProberCapsMaxConcurrency(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test"}
	m.RegisterExecutor(exec)

	for i := 0; i < 2; i++ {
		auth := &Auth{
			ID:       fmt.Sprintf("a%d", i),
			Provider: "test",
			Status:   StatusActive,
			Attributes: map[string]string{
				"base_url": "https://example.com",
			},
		}
		if _, err := m.Register(ctx, auth); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}

	cfg := internalconfig.CredentialProberConfig{
		Enabled:            true,
		MaxConcurrency:     1 << 30,
		RateLimitPerMinute: 1000,
	}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(200 * time.Millisecond)

	if exec.calls.Load() != 2 {
		t.Fatalf("prober calls = %d, want 2", exec.calls.Load())
	}
}

func TestProberNotBlockedByModelOnlyCooldown(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test"}
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

	m.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Success:  false,
		Model:    "missing-model",
		Error: &Error{
			Code:       "rate_limit",
			Message:    "model rate limited",
			HTTPStatus: http.StatusTooManyRequests,
			Retryable:  true,
		},
	})

	updated, _ := m.GetByID(auth.ID)
	if updated == nil || !updated.Unavailable {
		t.Fatal("expected aggregate Unavailable after model-only failure")
	}

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)

	if exec.calls.Load() != 1 {
		t.Fatalf("prober calls = %d, want 1 for model-only cooldown", exec.calls.Load())
	}
}

func TestProberHonorsAuthLevelCooldown(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test"}
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

	m.MarkResult(ctx, Result{
		AuthID:          auth.ID,
		Provider:        auth.Provider,
		Success:         false,
		CredentialScope: true,
		Error: &Error{
			Code:       "unauthorized",
			Message:    "auth-level 401",
			HTTPStatus: http.StatusUnauthorized,
			Retryable:  true,
		},
	})

	// A model success clears the aggregate Unavailable/NextRetryAfter fields but
	// leaves the auth-level cooldown active, so the prober must still skip it.
	m.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Success:  true,
		Model:    "test-model",
	})

	cfg := internalconfig.CredentialProberConfig{Enabled: true, Interval: time.Hour, MaxConcurrency: 1, RateLimitPerMinute: 1000}
	m.SetConfig(&internalconfig.Config{CredentialProber: cfg})

	time.Sleep(100 * time.Millisecond)

	if exec.calls.Load() != 0 {
		t.Fatalf("prober calls = %d, want 0 while auth-level cooldown active", exec.calls.Load())
	}
}

func TestProberSuccessDoesNotClearNonProberCooldown(t *testing.T) {
	ctx := context.Background()
	m := newProberManager()
	exec := &proberTestExecutor{provider: "test"}
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

	retryAfter := time.Hour
	m.MarkResult(ctx, Result{
		AuthID:          auth.ID,
		Provider:        auth.Provider,
		Success:         false,
		CredentialScope: true,
		IsProbe:         true,
		Error: &Error{
			Code:       ErrorCodeForceCooldown,
			Message:    "prober: unreachable",
			HTTPStatus: http.StatusServiceUnavailable,
			Retryable:  true,
		},
		RetryAfter: &retryAfter,
	})

	// A concurrent non-probe credential-scoped failure overwrites prober ownership.
	m.MarkResult(ctx, Result{
		AuthID:          auth.ID,
		Provider:        auth.Provider,
		Success:         false,
		CredentialScope: true,
		Error: &Error{
			Code:       "unauthorized",
			Message:    "auth-level 401",
			HTTPStatus: http.StatusUnauthorized,
			Retryable:  true,
		},
	})

	// A later successful probe must not clear the non-prober failure.
	m.MarkResult(ctx, Result{
		AuthID:          auth.ID,
		Provider:        auth.Provider,
		Success:         true,
		CredentialScope: true,
		IsProbe:         true,
	})

	updated, _ := m.GetByID(auth.ID)
	if updated == nil || !updated.Unavailable || updated.NextRetryAfter.IsZero() {
		t.Fatalf("non-prober cooldown should survive probe success; Unavailable=%v NextRetryAfter=%v", updated.Unavailable, updated.NextRetryAfter)
	}
	if !updated.authLevelCooldown {
		t.Fatalf("auth-level cooldown should survive probe success")
	}
}
