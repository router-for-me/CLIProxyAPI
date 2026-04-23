package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type fakeCircuitBreakerFailureStore struct {
	mu        sync.Mutex
	counts    map[string]int
	events    []CircuitBreakerFailureEvent
	readErr   error
	recordErr error
	resetErr  error
}

func newFakeCircuitBreakerFailureStore() *fakeCircuitBreakerFailureStore {
	return &fakeCircuitBreakerFailureStore{counts: make(map[string]int)}
}

func (s *fakeCircuitBreakerFailureStore) GetFailureCounts(_ context.Context, model string) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	out := make(map[string]int)
	for key, count := range s.counts {
		out[key] = count
	}
	return out, nil
}

func (s *fakeCircuitBreakerFailureStore) RecordFailure(_ context.Context, event CircuitBreakerFailureEvent) (CircuitBreakerFailureState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recordErr != nil {
		return CircuitBreakerFailureState{}, s.recordErr
	}
	key := event.AuthID
	next := s.counts[key] + 1
	s.counts[key] = next
	event.ConsecutiveFailures = next
	s.events = append(s.events, event)
	return CircuitBreakerFailureState{
		Provider:            event.Provider,
		AuthID:              event.AuthID,
		Model:               event.Model,
		ConsecutiveFailures: next,
	}, nil
}

func (s *fakeCircuitBreakerFailureStore) ResetFailure(_ context.Context, provider, authID, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resetErr != nil {
		return s.resetErr
	}
	delete(s.counts, authID)
	return nil
}

func (s *fakeCircuitBreakerFailureStore) setCount(authID string, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[authID] = count
}

func (s *fakeCircuitBreakerFailureStore) count(authID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[authID]
}

type mongoCircuitBreakerTestExecutor struct {
	id      string
	mu      sync.Mutex
	calls   []string
	success map[string]bool
}

type firstCircuitBreakerTestSelector struct{}

func (firstCircuitBreakerTestSelector) Pick(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if len(auths) == 0 {
		return nil, nil
	}
	return auths[0], nil
}

func (e *mongoCircuitBreakerTestExecutor) Identifier() string { return e.id }

func (e *mongoCircuitBreakerTestExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.calls = append(e.calls, e.id+":"+auth.ID)
	success := e.success != nil && e.success[auth.ID]
	e.mu.Unlock()
	if success {
		return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	}
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusInternalServerError, Message: "upstream failed"}
}

func (e *mongoCircuitBreakerTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusInternalServerError, Message: "stream failed"}
}

func (e *mongoCircuitBreakerTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *mongoCircuitBreakerTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusInternalServerError, Message: "count failed"}
}

func (e *mongoCircuitBreakerTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *mongoCircuitBreakerTestExecutor) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.calls))
	copy(out, e.calls)
	return out
}

func newMongoCircuitBreakerTestManager(t *testing.T, store *fakeCircuitBreakerFailureStore, model string) (*Manager, *mongoCircuitBreakerTestExecutor, *mongoCircuitBreakerTestExecutor, string, string) {
	t.Helper()

	m := NewManager(nil, &RoundRobinSelector{}, nil)
	m.SetCircuitBreakerFailureStore(store)
	alphaExec := &mongoCircuitBreakerTestExecutor{id: "alpha"}
	betaExec := &mongoCircuitBreakerTestExecutor{id: "beta"}
	m.RegisterExecutor(alphaExec)
	m.RegisterExecutor(betaExec)

	authA := "mongo-cb-alpha-" + t.Name()
	authB := "mongo-cb-beta-" + t.Name()
	authA = strings.NewReplacer("/", "-", " ", "-").Replace(authA)
	authB = strings.NewReplacer("/", "-", " ", "-").Replace(authB)
	alphaAuth := &Auth{ID: authA, Provider: "alpha", Metadata: map[string]any{"disable_cooling": true}}
	betaAuth := &Auth{ID: authB, Provider: "beta", Metadata: map[string]any{"disable_cooling": true}}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authA, "alpha", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(authB, "beta", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authA)
		reg.UnregisterClient(authB)
	})

	if _, err := m.Register(context.Background(), alphaAuth); err != nil {
		t.Fatalf("register alpha auth: %v", err)
	}
	if _, err := m.Register(context.Background(), betaAuth); err != nil {
		t.Fatalf("register beta auth: %v", err)
	}
	return m, alphaExec, betaExec, authA, authB
}

func TestManager_CircuitBreakerFailureStorePrefersOtherProviderBeforeThirdFailure(t *testing.T) {
	store := newFakeCircuitBreakerFailureStore()
	model := "mongo-cb-model"
	m, alphaExec, betaExec, authA, authB := newMongoCircuitBreakerTestManager(t, store, model)
	store.setCount(authA, registry.DefaultCircuitBreakerFailureThreshold-1)
	betaExec.success = map[string]bool{authB: true}

	resp, err := m.Execute(context.Background(), []string{"alpha", "beta"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected successful beta response payload")
	}
	if calls := alphaExec.Calls(); len(calls) != 0 {
		t.Fatalf("alpha calls = %v, want none before beta is tried", calls)
	}
	if calls := betaExec.Calls(); len(calls) != 1 || calls[0] != "beta:"+authB {
		t.Fatalf("beta calls = %v, want [beta:%s]", calls, authB)
	}
	if got := store.count(authB); got != 0 {
		t.Fatalf("beta failure count after success = %d, want 0", got)
	}
}

func TestManager_CircuitBreakerFailureStorePrefersOtherProviderOnLegacyMixedSelector(t *testing.T) {
	store := newFakeCircuitBreakerFailureStore()
	model := "mongo-cb-legacy"
	m, alphaExec, betaExec, authA, authB := newMongoCircuitBreakerTestManager(t, store, model)
	m.selector = firstCircuitBreakerTestSelector{}
	store.setCount(authA, registry.DefaultCircuitBreakerFailureThreshold-1)
	betaExec.success = map[string]bool{authB: true}

	_, err := m.Execute(context.Background(), []string{"alpha", "beta"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if calls := alphaExec.Calls(); len(calls) != 0 {
		t.Fatalf("alpha calls = %v, want none before beta is tried", calls)
	}
	if calls := betaExec.Calls(); len(calls) != 1 || calls[0] != "beta:"+authB {
		t.Fatalf("beta calls = %v, want [beta:%s]", calls, authB)
	}
}

func TestManager_CircuitBreakerFailureStoreAllowsThirdFailureAfterAllProvidersReachSecondFailure(t *testing.T) {
	store := newFakeCircuitBreakerFailureStore()
	model := "mongo-cb-all-second"
	m, alphaExec, _, authA, _ := newMongoCircuitBreakerTestManager(t, store, model)
	request := cliproxyexecutor.Request{Model: model}

	for i := 0; i < registry.DefaultCircuitBreakerFailureThreshold; i++ {
		_, _ = m.Execute(context.Background(), []string{"alpha", "beta"}, request, cliproxyexecutor.Options{})
	}

	if calls := alphaExec.Calls(); len(calls) < registry.DefaultCircuitBreakerFailureThreshold {
		t.Fatalf("alpha calls = %v, want at least %d calls", calls, registry.DefaultCircuitBreakerFailureThreshold)
	}
	if got := store.count(authA); got < registry.DefaultCircuitBreakerFailureThreshold {
		t.Fatalf("mongo failure count for alpha = %d, want at least %d", got, registry.DefaultCircuitBreakerFailureThreshold)
	}
	if !registry.GetGlobalRegistry().IsCircuitOpen(authA, model) {
		t.Fatal("expected alpha auth+model circuit breaker to be open after third failure")
	}
}

func TestManager_CircuitBreakerFailureStoreReadErrorFailsRequestBeforeUpstreamCall(t *testing.T) {
	store := newFakeCircuitBreakerFailureStore()
	store.readErr = fmt.Errorf("mongo read failed")
	model := "mongo-cb-read-error"
	m, alphaExec, betaExec, _, _ := newMongoCircuitBreakerTestManager(t, store, model)

	_, err := m.Execute(context.Background(), []string{"alpha", "beta"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected Execute() to fail when Mongo failure state cannot be read")
	}
	if calls := alphaExec.Calls(); len(calls) != 0 {
		t.Fatalf("alpha calls = %v, want no upstream calls", calls)
	}
	if calls := betaExec.Calls(); len(calls) != 0 {
		t.Fatalf("beta calls = %v, want no upstream calls", calls)
	}
}
