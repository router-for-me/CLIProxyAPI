package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type providerCapTestExecutor struct {
	id         string
	terminated bool

	mu        sync.Mutex
	attempted []string
}

func (e *providerCapTestExecutor) Identifier() string { return e.id }

func (e *providerCapTestExecutor) record(auth *Auth) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if auth != nil {
		e.attempted = append(e.attempted, auth.ID)
	}
}

func (e *providerCapTestExecutor) Attempted() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.attempted...)
}

func (e *providerCapTestExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth)
	if e.terminated {
		return cliproxyexecutor.Response{}, &credentialTerminatedError{}
	}
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusInternalServerError, Message: "synthetic upstream failure"}
}

func (e *providerCapTestExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.record(auth)
	if e.terminated {
		return nil, &credentialTerminatedError{}
	}
	return nil, &Error{HTTPStatus: http.StatusInternalServerError, Message: "synthetic upstream failure"}
}

func (e *providerCapTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *providerCapTestExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth)
	if e.terminated {
		return cliproxyexecutor.Response{}, &credentialTerminatedError{}
	}
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusInternalServerError, Message: "synthetic upstream failure"}
}

func (e *providerCapTestExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	return nil, nil
}

func newProviderCapTestManager(t *testing.T, cap int, providers []string, keysPerProvider int) (*Manager, string, map[string]*providerCapTestExecutor) {
	t.Helper()
	model := "provider-cap-" + uuid.NewString()
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, 6)
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: cap})

	executors := make(map[string]*providerCapTestExecutor, len(providers))
	for _, provider := range providers {
		executor := &providerCapTestExecutor{id: provider}
		executors[provider] = executor
		m.RegisterExecutor(executor)
		for i := 0; i < keysPerProvider; i++ {
			authID := fmt.Sprintf("%s-%d-%s", provider, i, uuid.NewString())
			registry.GetGlobalRegistry().RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
			auth := &Auth{
				ID:       authID,
				Provider: provider,
				Status:   StatusActive,
				Attributes: map[string]string{
					AttributeAPIKey: "test-key-" + authID,
				},
			}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth %s: %v", authID, errRegister)
			}
		}
	}
	return m, model, executors
}

func countProviderAttempts(t *testing.T, executors map[string]*providerCapTestExecutor) map[string]int {
	t.Helper()
	counts := make(map[string]int, len(executors))
	for provider, executor := range executors {
		counts[provider] = len(executor.Attempted())
	}
	return counts
}

func TestExecuteMixedOnceCapsAttemptsPerProvider(t *testing.T) {
	tests := []struct {
		name       string
		cap        int
		wantCounts map[string]int
	}{
		{name: "cap one", cap: 1, wantCounts: map[string]int{"alpha": 1, "beta": 1}},
		{name: "cap two", cap: 2, wantCounts: map[string]int{"alpha": 2, "beta": 2}},
		{name: "cap disabled", cap: 0, wantCounts: map[string]int{"alpha": 3, "beta": 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, model, executors := newProviderCapTestManager(t, tt.cap, []string{"alpha", "beta"}, 3)
			_, errExecute := m.executeMixedOnce(context.Background(), []string{"alpha", "beta"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}, 6, 0, 0)
			if errExecute == nil {
				t.Fatalf("executeMixedOnce() error = nil, want upstream failure")
			}
			got := countProviderAttempts(t, executors)
			for provider, want := range tt.wantCounts {
				if got[provider] != want {
					t.Fatalf("provider %s attempts = %d, want %d (all=%v)", provider, got[provider], want, got)
				}
			}
		})
	}
}

func TestExecuteKeepsProviderCapAcrossRetryRounds(t *testing.T) {
	m, model, executors := newProviderCapTestManager(t, 1, []string{"alpha", "beta"}, 3)
	m.SetRetryConfig(1, 0, 6)

	_, errExecute := m.Execute(context.Background(), []string{"alpha", "beta"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatalf("Execute() error = nil, want upstream failure")
	}
	if statusCodeFromError(errExecute) != http.StatusInternalServerError {
		t.Fatalf("Execute() error = %v, want the original 500 instead of a masked auth_not_found", errExecute)
	}
	got := countProviderAttempts(t, executors)
	if got["alpha"] != 1 || got["beta"] != 1 {
		t.Fatalf("retry rounds re-entered exhausted provider groups: %v", got)
	}
}

func TestExecuteCountMixedOnceCapsAttemptsPerProvider(t *testing.T) {
	m, model, executors := newProviderCapTestManager(t, 1, []string{"alpha", "beta"}, 3)
	_, errExecute := m.executeCountMixedOnce(context.Background(), []string{"alpha", "beta"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}, 6, 0, 0)
	if errExecute == nil {
		t.Fatalf("executeCountMixedOnce() error = nil, want upstream failure")
	}
	got := countProviderAttempts(t, executors)
	if got["alpha"] != 1 || got["beta"] != 1 {
		t.Fatalf("count-token attempts = %v, want one per provider", got)
	}
}

func TestExecuteStreamMixedOnceCapsAttemptsPerProvider(t *testing.T) {
	m, model, executors := newProviderCapTestManager(t, 1, []string{"alpha", "beta"}, 3)
	homeRetryLimit := -1
	_, errExecute := m.executeStreamMixedOnce(context.Background(), []string{"alpha", "beta"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}, 6, &homeRetryLimit, 0, 0)
	if errExecute == nil {
		t.Fatalf("executeStreamMixedOnce() error = nil, want upstream failure")
	}
	got := countProviderAttempts(t, executors)
	if got["alpha"] != 1 || got["beta"] != 1 {
		t.Fatalf("stream attempts = %v, want one per provider", got)
	}
}

func TestProviderCapDoesNotChargeCredentialTerminatedAttempts(t *testing.T) {
	tests := []struct {
		name string
		call func(*Manager, string) error
	}{
		{
			name: "non-stream",
			call: func(m *Manager, model string) error {
				_, err := m.executeMixedOnce(context.Background(), []string{"alpha", "beta"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}, 6, 0, 0)
				return err
			},
		},
		{
			name: "count",
			call: func(m *Manager, model string) error {
				_, err := m.executeCountMixedOnce(context.Background(), []string{"alpha", "beta"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}, 6, 0, 0)
				return err
			},
		},
		{
			name: "stream",
			call: func(m *Manager, model string) error {
				homeRetryLimit := -1
				_, err := m.executeStreamMixedOnce(context.Background(), []string{"alpha", "beta"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}, 6, &homeRetryLimit, 0, 0)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, model, executors := newProviderCapTestManager(t, 1, []string{"alpha", "beta"}, 3)
			for _, executor := range executors {
				executor.terminated = true
			}
			if errExecute := tt.call(m, model); errExecute == nil {
				t.Fatalf("%s error = nil, want credential terminated error", tt.name)
			}
			got := countProviderAttempts(t, executors)
			if got["alpha"] != 3 || got["beta"] != 3 {
				t.Fatalf("%s terminated attempts = %v, want all keys of both providers (no provider cap charge)", tt.name, got)
			}
		})
	}
}

func TestExecuteKeepsSnapshottedProviderCapAfterReload(t *testing.T) {
	m, model, executors := newProviderCapTestManager(t, 1, []string{"alpha", "beta"}, 3)
	ctx := m.WithProviderAttemptBudget(context.Background())
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: 0})
	_, errExecute := m.executeMixedOnce(ctx, []string{"alpha", "beta"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}, 6, 0, 0)
	if errExecute == nil {
		t.Fatalf("executeMixedOnce() error = nil, want upstream failure")
	}
	got := countProviderAttempts(t, executors)
	if got["alpha"] != 1 || got["beta"] != 1 {
		t.Fatalf("attempts after reload = %v, want snapshotted cap of one per provider", got)
	}
}
