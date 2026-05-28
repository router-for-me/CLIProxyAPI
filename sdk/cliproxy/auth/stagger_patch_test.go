package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// minimalExecutor satisfies ProviderExecutor with a configurable provider name.
type minimalExecutor struct{ provider string }

func (e *minimalExecutor) Identifier() string { return e.provider }
func (e *minimalExecutor) Execute(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *minimalExecutor) ExecuteStream(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (e *minimalExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) { return a, nil }
func (e *minimalExecutor) CountTokens(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *minimalExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	return nil, nil
}

// ---- stableJitter tests ----

func TestStableJitter_Determinism(t *testing.T) {
	window := 10 * time.Minute
	first := stableJitter("user@example.com", window)
	second := stableJitter("user@example.com", window)
	if first != second {
		t.Fatalf("stableJitter not deterministic: got %v then %v", first, second)
	}
}

func TestStableJitter_Range(t *testing.T) {
	window := 30 * time.Minute
	ids := []string{"a", "b", "alpha@example.com", "z9999", "long-auth-id-with-dashes"}
	for _, id := range ids {
		got := stableJitter(id, window)
		if got < 0 || got >= window {
			t.Fatalf("stableJitter(%q, %v) = %v, want [0, %v)", id, window, got, window)
		}
	}
}

func TestStableJitter_ZeroWindow(t *testing.T) {
	got := stableJitter("any-id", 0)
	if got != 0 {
		t.Fatalf("stableJitter with zero window: got %v, want 0", got)
	}
}

func TestStableJitter_NegativeWindow(t *testing.T) {
	got := stableJitter("any-id", -5*time.Minute)
	if got != 0 {
		t.Fatalf("stableJitter with negative window: got %v, want 0", got)
	}
}

func TestStableJitter_EmptyID(t *testing.T) {
	got := stableJitter("", 10*time.Minute)
	if got != 0 {
		t.Fatalf("stableJitter with empty ID: got %v, want 0", got)
	}
}

// ---- Per-provider semaphore tests ----

func TestRebuildProviderRefreshSems_CapacityMatchesConfig(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(&minimalExecutor{provider: "testprovider"})

	cfg := &internalconfig.Config{AuthMaxConcurrentRefreshPerProvider: 2}
	m.rebuildProviderRefreshSems(cfg)

	sem := m.providerRefreshSem("testprovider")
	if sem == nil {
		t.Fatal("expected semaphore for testprovider, got nil")
	}
	if cap(sem) != 2 {
		t.Fatalf("expected semaphore capacity 2, got %d", cap(sem))
	}
}

func TestRebuildProviderRefreshSems_DefaultCapacity(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(&minimalExecutor{provider: "myprovider"})

	// Passing nil uses the package default.
	m.rebuildProviderRefreshSems(nil)

	sem := m.providerRefreshSem("myprovider")
	if sem == nil {
		t.Fatal("expected semaphore for myprovider, got nil")
	}
	if cap(sem) != defaultMaxConcurrentRefreshPerProvider {
		t.Fatalf("expected capacity %d, got %d", defaultMaxConcurrentRefreshPerProvider, cap(sem))
	}
}

func TestProviderRefreshSem_UnknownProvider(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(&minimalExecutor{provider: "known"})
	m.rebuildProviderRefreshSems(nil)

	sem := m.providerRefreshSem("unknown")
	if sem != nil {
		t.Fatalf("expected nil semaphore for unknown provider, got %v", sem)
	}
}

func TestProviderRefreshSem_CaseInsensitive(t *testing.T) {
	m := NewManager(nil, nil, nil)
	// Executor registers as "Claude" (mixed case).
	m.RegisterExecutor(&minimalExecutor{provider: "Claude"})
	m.rebuildProviderRefreshSems(nil)

	// All case variants should return the same semaphore.
	for _, name := range []string{"claude", "CLAUDE", "Claude"} {
		sem := m.providerRefreshSem(name)
		if sem == nil {
			t.Fatalf("expected semaphore for provider name %q, got nil", name)
		}
	}
}
