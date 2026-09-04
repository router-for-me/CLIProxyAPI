package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// poolRequestFaultError models the upstream failure shape that motivates
// aggressive in-pool failover: an HTTP 400 whose JSON body carries the generic
// invalid_request_error code. Many OpenAI-compatible providers report quota
// and credential faults in exactly this shape, so clienterror classifies the
// failure as a request fault through the body (hasRequestFaultBody) and the
// conductor's isRequestInvalidError hard-stops credential rotation unless the
// entry's pool opted into a routing strategy. Error() returns the raw body so
// the classification is body-driven, not just status-driven, and so distinct
// per-entry markers stay visible in the surfaced error.
type poolRequestFaultError struct {
	body string
}

func (e *poolRequestFaultError) Error() string   { return e.body }
func (e *poolRequestFaultError) StatusCode() int { return http.StatusBadRequest }

func newPoolRequestFaultError(marker string) error {
	return &poolRequestFaultError{
		body: fmt.Sprintf(`{"error":{"code":"invalid_request_error","message":"simulated provider quota fault: %s"}}`, marker),
	}
}

// poolFailoverTestExecutor is a claude executor stub whose per-auth behavior is
// driven by failMessages: an auth whose ID is a key fails with the
// request-fault 400 carrying the mapped marker; every other auth succeeds with
// a payload identifying the serving auth. Calls are recorded per execution
// mode so tests can assert rotation counts, per-entry invocation, and the
// order entries were attempted in.
type poolFailoverTestExecutor struct {
	failMessages map[string]string

	mu           sync.Mutex
	executeCalls []string
	countCalls   []string
	streamCalls  []string
}

func (*poolFailoverTestExecutor) Identifier() string { return "claude" }

func (e *poolFailoverTestExecutor) failError(authID string) error {
	e.mu.Lock()
	marker := e.failMessages[authID]
	e.mu.Unlock()
	if marker == "" {
		return nil
	}
	return newPoolRequestFaultError(marker)
}

func (e *poolFailoverTestExecutor) record(calls *[]string, authID string) {
	e.mu.Lock()
	*calls = append(*calls, authID)
	e.mu.Unlock()
}

func (e *poolFailoverTestExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(&e.executeCalls, auth.ID)
	if err := e.failError(auth.ID); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *poolFailoverTestExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(&e.countCalls, auth.ID)
	if err := e.failError(auth.ID); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *poolFailoverTestExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.record(&e.streamCalls, auth.ID)
	if err := e.failError(auth.ID); err != nil {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*poolFailoverTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*poolFailoverTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *poolFailoverTestExecutor) snapshot(calls *[]string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(*calls))
	copy(out, *calls)
	return out
}

func (e *poolFailoverTestExecutor) ExecuteCalls() []string { return e.snapshot(&e.executeCalls) }
func (e *poolFailoverTestExecutor) CountCalls() []string   { return e.snapshot(&e.countCalls) }
func (e *poolFailoverTestExecutor) StreamCalls() []string  { return e.snapshot(&e.streamCalls) }

func callsForAuth(calls []string, authID string) int {
	count := 0
	for _, called := range calls {
		if called == authID {
			count++
		}
	}
	return count
}

// newPoolFailoverTestManager registers one claude executor and three
// same-pool auths (provider_key claude:7, entry_provider_key
// claude:7:key-71..73). poolStrategy, when non-empty, is stamped as
// Attributes[AttributePoolStrategy] on every auth. failMessages maps auth IDs
// whose executor invocation returns the request-fault 400 carrying the mapped
// marker; every other auth succeeds.
func newPoolFailoverTestManager(t *testing.T, poolStrategy string, failMessages map[string]string) (*Manager, *poolFailoverTestExecutor, string) {
	t.Helper()
	model := "pool-failover-model-" + uuid.NewString()
	executor := &poolFailoverTestExecutor{failMessages: failMessages}
	manager := NewManager(nil, nil, NoopHook{})
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(executor)

	poolAuths := []*Auth{
		{
			ID:       "pool-failover-auth-a",
			Provider: "claude",
			Status:   StatusActive,
			Attributes: map[string]string{
				"provider_key":            "claude:7",
				AttributeEntryProviderKey: "claude:7:key-71",
				AttributeAuthKind:         "apikey",
				AttributeAPIKey:           "k-a",
			},
		},
		{
			ID:       "pool-failover-auth-b",
			Provider: "claude",
			Status:   StatusActive,
			Attributes: map[string]string{
				"provider_key":            "claude:7",
				AttributeEntryProviderKey: "claude:7:key-72",
				AttributeAuthKind:         "apikey",
				AttributeAPIKey:           "k-b",
			},
		},
		{
			ID:       "pool-failover-auth-c",
			Provider: "claude",
			Status:   StatusActive,
			Attributes: map[string]string{
				"provider_key":            "claude:7",
				AttributeEntryProviderKey: "claude:7:key-73",
				AttributeAuthKind:         "apikey",
				AttributeAPIKey:           "k-c",
			},
		},
	}
	if poolStrategy != "" {
		for _, auth := range poolAuths {
			auth.Attributes[AttributePoolStrategy] = poolStrategy
		}
	}

	reg := registry.GetGlobalRegistry()
	for _, auth := range poolAuths {
		reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}
	t.Cleanup(func() {
		for _, auth := range poolAuths {
			reg.UnregisterClient(auth.ID)
		}
	})
	return manager, executor, model
}

func TestPoolStrategyRotatesOnRequestInvalidError(t *testing.T) {
	manager, executor, model := newPoolFailoverTestManager(t, "round-robin", map[string]string{
		"pool-failover-auth-a": "quota-a",
	})

	resp, errExecute := manager.Execute(context.Background(), []string{"claude:7"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v, want in-pool rotation to serve the request", errExecute)
	}
	calls := executor.ExecuteCalls()
	if len(calls) != 2 {
		t.Fatalf("execute calls = %v, want one failed entry plus one successful retry", calls)
	}
	if got := callsForAuth(calls, "pool-failover-auth-a"); got != 1 {
		t.Fatalf("failing entry called %d times, want 1", got)
	}
	served := string(resp.Payload)
	if served != "pool-failover-auth-b" && served != "pool-failover-auth-c" {
		t.Fatalf("request served by %q, want a non-failing entry of the same pool", served)
	}

	// MarkResult must still record the failed entry even though rotation
	// continued. Request-fault classification stamps the result request_scoped,
	// so shouldSkipCredentialCooldown suppresses model cooldown; the durable
	// trace of MarkResult running is the failure counter.
	failed, ok := manager.GetByID("pool-failover-auth-a")
	if !ok || failed == nil {
		t.Fatalf("GetByID(pool-failover-auth-a) did not return auth")
	}
	if failed.Failed != 1 || failed.Success != 0 {
		t.Fatalf("failed entry counters = (%d failed, %d success), want (1, 0)", failed.Failed, failed.Success)
	}
}

func TestPoolWithoutStrategyKeepsHardStop(t *testing.T) {
	manager, executor, model := newPoolFailoverTestManager(t, "", map[string]string{
		"pool-failover-auth-a": "quota-a",
	})

	_, errExecute := manager.Execute(context.Background(), []string{"claude:7"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want the request-fault 400 to surface")
	}
	if statusCodeFromError(errExecute) != http.StatusBadRequest {
		t.Fatalf("Execute() error = %v, want HTTP 400", errExecute)
	}
	if !isRequestInvalidError(errExecute) {
		t.Fatalf("Execute() error = %v, want request-fault classification (the hard stop must engage for the right reason)", errExecute)
	}
	if !strings.Contains(errExecute.Error(), "quota-a") {
		t.Fatalf("Execute() error = %v, want the failed entry's marker", errExecute)
	}
	if calls := executor.ExecuteCalls(); len(calls) != 1 {
		t.Fatalf("execute calls = %v, want the historical hard stop after the first entry", calls)
	}
}

func TestPoolStrategyExhaustedReturnsLastError(t *testing.T) {
	failMessages := map[string]string{
		"pool-failover-auth-a": "quota-a",
		"pool-failover-auth-b": "quota-b",
		"pool-failover-auth-c": "quota-c",
	}
	manager, executor, model := newPoolFailoverTestManager(t, "round-robin", failMessages)

	_, errExecute := manager.Execute(context.Background(), []string{"claude:7"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want the pool's last entry error after exhaustion")
	}
	calls := executor.ExecuteCalls()
	if len(calls) != 3 {
		t.Fatalf("execute calls = %v, want every pool entry attempted once", calls)
	}
	for _, authID := range []string{"pool-failover-auth-a", "pool-failover-auth-b", "pool-failover-auth-c"} {
		if got := callsForAuth(calls, authID); got != 1 {
			t.Fatalf("entry %s called %d times, want 1", authID, got)
		}
	}
	// The surfaced error must be the LAST attempted entry's upstream error,
	// not the terminal pick failure.
	lastMarker := failMessages[calls[len(calls)-1]]
	if !strings.Contains(errExecute.Error(), lastMarker) {
		t.Fatalf("Execute() error = %v, want the last attempted entry's marker %q", errExecute, lastMarker)
	}
	if strings.Contains(errExecute.Error(), "auth_not_found") || strings.Contains(errExecute.Error(), "no auth available") {
		t.Fatalf("Execute() error = %v, want the upstream error, not a pick failure", errExecute)
	}
}

func TestPoolStrategyRotationStaysInsidePool(t *testing.T) {
	failMessages := map[string]string{
		"pool-failover-auth-a": "quota-a",
		"pool-failover-auth-b": "quota-b",
		"pool-failover-auth-c": "quota-c",
	}
	manager, executor, model := newPoolFailoverTestManager(t, "round-robin", failMessages)

	// A fourth auth from a DIFFERENT pool (claude:99, no pool strategy) also
	// serves the model and would succeed. The request lists both pools, so
	// only pinning rotation to the failed entry's pool keeps it out.
	outside := &Auth{
		ID:       "pool-failover-outside",
		Provider: "claude",
		Status:   StatusActive,
		Attributes: map[string]string{
			"provider_key":    "claude:99",
			AttributeAuthKind: "apikey",
			AttributeAPIKey:   "k-outside",
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(outside.ID, outside.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(outside.ID) })
	if _, errRegister := manager.Register(context.Background(), outside); errRegister != nil {
		t.Fatalf("Register(outside) error = %v", errRegister)
	}

	_, errExecute := manager.Execute(context.Background(), []string{"claude:7", "claude:99"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want the pool's error; the outside auth must not rescue the request")
	}
	calls := executor.ExecuteCalls()
	if len(calls) != 3 {
		t.Fatalf("execute calls = %v, want only the three pool entries attempted", calls)
	}
	if got := callsForAuth(calls, "pool-failover-outside"); got != 0 {
		t.Fatalf("outside auth called %d times, want 0: rotation must stay inside the pinned pool", got)
	}
	lastMarker := failMessages[calls[len(calls)-1]]
	if !strings.Contains(errExecute.Error(), lastMarker) {
		t.Fatalf("Execute() error = %v, want the last attempted pool entry's marker %q", errExecute, lastMarker)
	}
}

func TestPoolOfOneFallbackAuthSurfacesOriginalError(t *testing.T) {
	// Mirrors the synthesizer's zero-entries fallback shape: a single auth
	// carrying the pool strategy with a bare channel key and no
	// entry_provider_key. With no sibling entry to rotate to, the original
	// upstream error must surface (not an auth_not_found pick failure).
	model := "pool-failover-model-" + uuid.NewString()
	executor := &poolFailoverTestExecutor{failMessages: map[string]string{
		"pool-fallback-auth": "quota-fallback",
	}}
	manager := NewManager(nil, nil, NoopHook{})
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "pool-fallback-auth",
		Provider: "claude",
		Status:   StatusActive,
		Attributes: map[string]string{
			"provider_key":        "claude",
			AttributeAuthKind:     "apikey",
			AttributeAPIKey:       "k",
			AttributePoolStrategy: "round-robin",
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want the original request-fault 400")
	}
	if statusCodeFromError(errExecute) != http.StatusBadRequest {
		t.Fatalf("Execute() error = %v, want the original HTTP 400", errExecute)
	}
	if !strings.Contains(errExecute.Error(), "quota-fallback") {
		t.Fatalf("Execute() error = %v, want the original upstream error, not auth_not_found", errExecute)
	}
	if calls := executor.ExecuteCalls(); len(calls) != 1 {
		t.Fatalf("execute calls = %v, want 1", calls)
	}
}

func TestPoolStrategyRotatesOnRequestInvalidErrorStream(t *testing.T) {
	manager, executor, model := newPoolFailoverTestManager(t, "round-robin", map[string]string{
		"pool-failover-auth-a": "quota-a",
	})

	streamResult, errStream := manager.ExecuteStream(context.Background(), []string{"claude:7"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v, want in-pool rotation to serve the request", errStream)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want success from the rotated entry", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	calls := executor.StreamCalls()
	if len(calls) != 2 {
		t.Fatalf("stream calls = %v, want one failed entry plus one successful retry", calls)
	}
	if got := callsForAuth(calls, "pool-failover-auth-a"); got != 1 {
		t.Fatalf("failing entry called %d times, want 1", got)
	}
	served := string(payload)
	if served != "pool-failover-auth-b" && served != "pool-failover-auth-c" {
		t.Fatalf("stream served by %q, want a non-failing entry of the same pool", served)
	}
}

func TestPoolStrategyRotatesOnRequestInvalidErrorCount(t *testing.T) {
	manager, executor, model := newPoolFailoverTestManager(t, "round-robin", map[string]string{
		"pool-failover-auth-a": "quota-a",
	})

	resp, errCount := manager.ExecuteCount(context.Background(), []string{"claude:7"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errCount != nil {
		t.Fatalf("ExecuteCount() error = %v, want in-pool rotation to serve the request", errCount)
	}
	calls := executor.CountCalls()
	if len(calls) != 2 {
		t.Fatalf("count calls = %v, want one failed entry plus one successful retry", calls)
	}
	if got := callsForAuth(calls, "pool-failover-auth-a"); got != 1 {
		t.Fatalf("failing entry called %d times, want 1", got)
	}
	served := string(resp.Payload)
	if served != "pool-failover-auth-b" && served != "pool-failover-auth-c" {
		t.Fatalf("count served by %q, want a non-failing entry of the same pool", served)
	}
}
