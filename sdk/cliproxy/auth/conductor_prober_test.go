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

func (e *proberTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	e.calls.Add(1)
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
