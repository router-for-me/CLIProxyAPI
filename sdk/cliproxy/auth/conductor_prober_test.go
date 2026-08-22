package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type proberTestExecutor struct {
	provider      string
	statusCode    int
	body          string
	err           error
	calls         atomic.Int32
	respondStatus int
	deadlineSet   atomic.Bool
	ctx           context.Context
}

func (e *proberTestExecutor) Identifier() string { return e.provider }

func (e *proberTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *proberTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *proberTestExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }

func (e *proberTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *proberTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	e.calls.Add(1)
	e.ctx = ctx
	if _, ok := ctx.Deadline(); ok {
		e.deadlineSet.Store(true)
	}
	if e.err != nil {
		return nil, e.err
	}
	status := e.statusCode
	if status <= 0 {
		status = e.respondStatus
	}
	if status <= 0 {
		status = http.StatusOK
	}
	body := e.body
	if body == "" {
		body = "{}"
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestProberDisabledByDefault(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)
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
	m := NewManager(nil, nil, nil)
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
	m := NewManager(nil, nil, nil)
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
	m := NewManager(nil, nil, nil)
	exec := &proberTestExecutor{provider: "test", statusCode: http.StatusOK}
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

func TestProberMarksAuthUnavailableOnEmptyResponse(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)
	exec := &proberTestExecutor{provider: "test", statusCode: http.StatusNoContent}
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
	if !updated.Unavailable {
		t.Fatalf("auth.Unavailable = %v, want true", updated.Unavailable)
	}
}

func TestProberDropsContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()

	m := NewManager(nil, nil, nil)
	exec := &proberTestExecutor{provider: "test", statusCode: http.StatusOK}
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
	m := NewManager(nil, nil, nil)
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
	m := NewManager(nil, nil, nil)
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
