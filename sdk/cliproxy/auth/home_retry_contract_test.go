package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type retryContractHomeDispatcher struct {
	mu               sync.Mutex
	authIDs          []string
	excluded         [][]string
	metadata         map[string]any
	requestRetry     *int
	websocket        bool
	exhaustedPayload []byte
	exhaustedErr     error
}

type legacyRepeatedStreamDispatcher struct {
	calls atomic.Int32
}

type retryRoundStartCooldownDispatcher struct {
	calls atomic.Int32
}

type retryRoundRepeatedCooldownDispatcher struct {
	calls atomic.Int32
}

type retryRoundLimitDownshiftDispatcher struct {
	calls atomic.Int32
}

type retryRoundSelectionFailureDispatcher struct {
	selectionPayload []byte
	selectionErr     error
}

type aggregateRetryHomeDispatcher struct {
	calls atomic.Int32
}

func (*legacyRepeatedStreamDispatcher) HeartbeatOK() bool { return true }

func (d *legacyRepeatedStreamDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	d.calls.Add(1)
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
		ID:       "home-retry-a",
		Provider: "home-retry-contract",
		Status:   StatusActive,
	}})
}

func (*legacyRepeatedStreamDispatcher) AbortAmbiguousDispatch() {}

func (*retryRoundStartCooldownDispatcher) HeartbeatOK() bool { return true }

func (d *retryRoundStartCooldownDispatcher) RPopAuth(ctx context.Context, model string, sessionID string, headers http.Header, count int) ([]byte, error) {
	return d.RPopAuthWithConstraints(ctx, model, sessionID, headers, count, nil, "")
}

func (d *retryRoundStartCooldownDispatcher) RPopAuthWithConstraints(_ context.Context, _ string, _ string, _ http.Header, _ int, _ []string, _ string) ([]byte, error) {
	if d.calls.Add(1) == 2 {
		return []byte(`{"error":{"type":"model_cooldown","message":"credential is cooling down","retryable":true,"retry_after_ms":1,"request_retry":1}}`), nil
	}
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
		ID:       "home-retry-a",
		Provider: "home-retry-contract",
		Status:   StatusActive,
	}})
}

func (*retryRoundStartCooldownDispatcher) AbortAmbiguousDispatch() {}

func (*retryRoundRepeatedCooldownDispatcher) HeartbeatOK() bool { return true }

func (d *retryRoundRepeatedCooldownDispatcher) RPopAuth(ctx context.Context, model string, sessionID string, headers http.Header, count int) ([]byte, error) {
	return d.RPopAuthWithConstraints(ctx, model, sessionID, headers, count, nil, "")
}

func (d *retryRoundRepeatedCooldownDispatcher) RPopAuthWithConstraints(_ context.Context, _ string, _ string, _ http.Header, _ int, _ []string, _ string) ([]byte, error) {
	if d.calls.Add(1) > 1 {
		return []byte(`{"error":{"type":"model_cooldown","message":"credential is cooling down","retryable":true,"retry_after_ms":1,"request_retry":1}}`), nil
	}
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
		ID:       "home-retry-a",
		Provider: "home-retry-contract",
		Status:   StatusActive,
	}})
}

func (*retryRoundRepeatedCooldownDispatcher) AbortAmbiguousDispatch() {}

func (*retryRoundLimitDownshiftDispatcher) HeartbeatOK() bool { return true }

func (d *retryRoundLimitDownshiftDispatcher) RPopAuth(ctx context.Context, model string, sessionID string, headers http.Header, count int) ([]byte, error) {
	return d.RPopAuthWithConstraints(ctx, model, sessionID, headers, count, nil, "")
}

func (d *retryRoundLimitDownshiftDispatcher) RPopAuthWithConstraints(_ context.Context, _ string, _ string, _ http.Header, _ int, _ []string, _ string) ([]byte, error) {
	switch d.calls.Add(1) {
	case 1:
		retryLimit := 1
		return json.Marshal(homeAuthDispatchResponse{RequestRetry: &retryLimit, Auth: Auth{
			ID:       "home-retry-a",
			Provider: "home-retry-contract",
			Status:   StatusActive,
		}})
	case 2:
		return []byte(`{"error":{"type":"model_cooldown","message":"remaining credentials are cooling down","retryable":true,"retry_after_ms":1,"request_retry":0}}`), nil
	default:
		return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
			ID:       "home-retry-b",
			Provider: "home-retry-contract",
			Status:   StatusActive,
		}})
	}
}

func (*retryRoundLimitDownshiftDispatcher) AbortAmbiguousDispatch() {}

func (*retryRoundSelectionFailureDispatcher) HeartbeatOK() bool { return true }

func (d *retryRoundSelectionFailureDispatcher) RPopAuth(ctx context.Context, model string, sessionID string, headers http.Header, count int) ([]byte, error) {
	return d.RPopAuthWithRetryRoundConstraints(ctx, model, sessionID, headers, count, 0, nil, "")
}

func (d *retryRoundSelectionFailureDispatcher) RPopAuthWithRetryRoundConstraints(_ context.Context, _ string, _ string, _ http.Header, _ int, retryRound int, excludedAuthIDs []string, _ string) ([]byte, error) {
	if retryRound == 0 {
		if len(excludedAuthIDs) > 0 {
			return nil, home.ErrAuthNotFound
		}
		return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
			ID:       "home-retry-a",
			Provider: "home-retry-contract",
			Status:   StatusActive,
		}})
	}
	if len(d.selectionPayload) > 0 {
		return append([]byte(nil), d.selectionPayload...), nil
	}
	return nil, d.selectionErr
}

func (*retryRoundSelectionFailureDispatcher) AbortAmbiguousDispatch() {}

func (*aggregateRetryHomeDispatcher) HeartbeatOK() bool { return true }

func (d *aggregateRetryHomeDispatcher) RPopAuth(ctx context.Context, model string, sessionID string, headers http.Header, count int) ([]byte, error) {
	return d.RPopAuthWithConstraints(ctx, model, sessionID, headers, count, nil, "")
}

func (d *aggregateRetryHomeDispatcher) RPopAuthWithConstraints(_ context.Context, _ string, _ string, _ http.Header, _ int, _ []string, _ string) ([]byte, error) {
	authID := "home-retry-a"
	override := 0
	if d.calls.Add(1) > 1 {
		authID = "home-retry-b"
		override = 2
	}
	requestRetry := 2
	return json.Marshal(homeAuthDispatchResponse{
		RequestRetry: &requestRetry,
		Auth: Auth{
			ID:       authID,
			Provider: "home-retry-contract",
			Status:   StatusActive,
			Metadata: map[string]any{"request_retry": override},
		},
	})
}

func (*aggregateRetryHomeDispatcher) AbortAmbiguousDispatch() {}

func (*retryContractHomeDispatcher) HeartbeatOK() bool { return true }

func (d *retryContractHomeDispatcher) RPopAuth(ctx context.Context, model string, sessionID string, headers http.Header, count int) ([]byte, error) {
	return d.RPopAuthWithConstraints(ctx, model, sessionID, headers, count, nil, "")
}

func (d *retryContractHomeDispatcher) RPopAuthWithConstraints(_ context.Context, _ string, _ string, _ http.Header, _ int, excludedAuthIDs []string, pinnedAuthID string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.excluded = append(d.excluded, append([]string(nil), excludedAuthIDs...))
	excluded := make(map[string]struct{}, len(excludedAuthIDs))
	for _, authID := range excludedAuthIDs {
		excluded[authID] = struct{}{}
	}
	for _, authID := range d.authIDs {
		if pinnedAuthID != "" && authID != pinnedAuthID {
			continue
		}
		if _, okExcluded := excluded[authID]; okExcluded {
			continue
		}
		attributes := map[string]string{}
		if d.websocket {
			attributes["websockets"] = "true"
		}
		return json.Marshal(homeAuthDispatchResponse{
			RequestRetry: d.requestRetry,
			Auth: Auth{
				ID:         authID,
				Provider:   "home-retry-contract",
				Status:     StatusActive,
				Metadata:   d.metadata,
				Attributes: attributes,
			},
		})
	}
	if len(d.exhaustedPayload) > 0 {
		return append([]byte(nil), d.exhaustedPayload...), nil
	}
	if d.exhaustedErr != nil {
		return nil, d.exhaustedErr
	}
	return nil, home.ErrAuthNotFound
}

func (*retryContractHomeDispatcher) AbortAmbiguousDispatch() {}

func (d *retryContractHomeDispatcher) Excluded() [][]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := make([][]string, len(d.excluded))
	for index := range d.excluded {
		result[index] = append([]string(nil), d.excluded[index]...)
	}
	return result
}

type retryContractHomeExecutor struct {
	mu               sync.Mutex
	calls            []string
	failAll          bool
	failure          error
	failures         map[string]error
	internalFailures map[string]bool
	streamBootstrap  bool
	streamHeaders    http.Header
}

func (*retryContractHomeExecutor) Identifier() string { return "home-retry-contract" }

func (e *retryContractHomeExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.calls = append(e.calls, auth.ID)
	e.mu.Unlock()
	if auth.ID == "home-retry-a" || e.failAll {
		e.markFailureAttempt(ctx, auth.ID)
		return cliproxyexecutor.Response{}, e.failureError(auth.ID)
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *retryContractHomeExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.calls = append(e.calls, auth.ID)
	e.mu.Unlock()
	if auth.ID == "home-retry-a" || e.failAll {
		e.markFailureAttempt(ctx, auth.ID)
		errFailure := e.failureError(auth.ID)
		if e.streamBootstrap {
			chunks := make(chan cliproxyexecutor.StreamChunk, 1)
			chunks <- cliproxyexecutor.StreamChunk{Err: errFailure}
			close(chunks)
			return &cliproxyexecutor.StreamResult{Headers: e.streamHeaders.Clone(), Chunks: chunks}, nil
		}
		return nil, errFailure
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *retryContractHomeExecutor) failureError(authID string) error {
	if failure := e.failures[authID]; failure != nil {
		return failure
	}
	if e.failure != nil {
		return e.failure
	}
	return retryContractRateLimitError{}
}

func (*retryContractHomeExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }

func (e *retryContractHomeExecutor) CountTokens(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.calls = append(e.calls, auth.ID)
	e.mu.Unlock()
	if auth.ID == "home-retry-a" || e.failAll {
		e.markFailureAttempt(ctx, auth.ID)
		return cliproxyexecutor.Response{}, e.failureError(auth.ID)
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (*retryContractHomeExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *retryContractHomeExecutor) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func (e *retryContractHomeExecutor) markFailureAttempt(ctx context.Context, authID string) {
	if e != nil && !e.internalFailures[authID] {
		cliproxyexecutor.MarkUpstreamAttempt(ctx)
	}
}

type retryContractRateLimitError struct {
	retryAfter time.Duration
}

func (retryContractRateLimitError) Error() string { return "credential rate limited" }

func (retryContractRateLimitError) StatusCode() int { return http.StatusTooManyRequests }

func (e retryContractRateLimitError) RetryAfter() *time.Duration {
	value := e.retryAfter
	if value == 0 {
		value = time.Millisecond
	}
	return &value
}

type retainingRetryContractHomeExecutor struct {
	*retryContractHomeExecutor
}

func (e *retainingRetryContractHomeExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if lifecycle, ok := opts.ExecutionLifecycle.(interface{ Retain() }); ok {
		lifecycle.Retain()
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func TestHomePinnedAuthRejectsMismatchedDispatch(t *testing.T) {
	dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-b"}}
	executor := &retryContractHomeExecutor{}
	registry := executionregistry.New()
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, registry, 1)
	manager.RegisterExecutor(executor)

	_, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.PinnedAuthMetadataKey: "home-retry-a",
	}})
	var authErr *Error
	if !errors.As(errExecute, &authErr) || authErr == nil || authErr.Code != "auth_not_found" {
		t.Fatalf("Execute() error = %T %v, want pinned auth_not_found", errExecute, errExecute)
	}
	if got := executor.Calls(); len(got) != 0 {
		t.Fatalf("executor calls = %v, want no mismatched credential execution", got)
	}
	if errDrain := registry.Drain(context.Background()); errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}

func TestHomePinnedAuthRetriesOnlyPinnedCredential(t *testing.T) {
	aggregateRetry := 3
	dispatcher := &retryContractHomeDispatcher{
		authIDs:      []string{"home-retry-b", "home-retry-a"},
		metadata:     map[string]any{"request_retry": 1},
		requestRetry: &aggregateRetry,
	}
	executor := &retryContractHomeExecutor{
		failAll: true,
		failure: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream unavailable"},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(0, time.Second, 0)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)

	_, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.PinnedAuthMetadataKey: "home-retry-a",
	}})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want terminal upstream error")
	}
	if got := executor.Calls(); len(got) != 2 || got[0] != "home-retry-a" || got[1] != "home-retry-a" {
		t.Fatalf("executor calls = %v, want pinned auth once in each of two rounds", got)
	}
	excluded := dispatcher.Excluded()
	if len(excluded) != 2 || len(excluded[0]) != 0 || len(excluded[1]) != 0 {
		t.Fatalf("Home excluded auth IDs = %v, want a fresh pinned selection in each round", excluded)
	}
}

func TestHomeExcludedCredentialEndsRetainedWebsocketSelection(t *testing.T) {
	dispatcher := &retryContractHomeDispatcher{
		authIDs:   []string{"home-retry-a", "home-retry-b"},
		websocket: true,
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(0, time.Second, 2)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(&retainingRetryContractHomeExecutor{retryContractHomeExecutor: &retryContractHomeExecutor{}})

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "home-retry-session",
	}}
	if _, errExecute := manager.Execute(ctx, []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, opts); errExecute != nil {
		t.Fatalf("first Execute() error = %v", errExecute)
	}

	pickOpts := withHomeExcludedAuthIDs(opts, map[string]struct{}{"home-retry-a": {}})
	selection, errPick := manager.pickHomeDispatchSelection(ctx, "gpt", pickOpts)
	if errPick != nil {
		t.Fatalf("pickHomeDispatchSelection() error = %v", errPick)
	}
	defer selection.End("test_complete")
	if auth := selection.CloneAuth(); auth == nil || auth.ID != "home-retry-b" {
		t.Fatalf("selected auth = %#v, want home-retry-b", auth)
	}

	excluded := dispatcher.Excluded()
	if len(excluded) != 2 || len(excluded[0]) != 0 || len(excluded[1]) != 1 || excluded[1][0] != "home-retry-a" {
		t.Fatalf("Home excluded auth IDs = %v, want [[], [home-retry-a]]", excluded)
	}
}

func TestHomeRetryRoundTriesFreshCredentialWhenRequestRetryIsZero(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a", "home-retry-b"}}
			executor := &retryContractHomeExecutor{}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(0, time.Second, 2)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)

			if stream {
				result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
				if errExecute != nil {
					t.Fatalf("ExecuteStream() error = %v", errExecute)
				}
				for range result.Chunks {
				}
			} else {
				response, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
				if errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
				if string(response.Payload) != "home-retry-b" {
					t.Fatalf("response payload = %q, want home-retry-b", string(response.Payload))
				}
			}

			if got := executor.Calls(); len(got) != 2 || got[0] != "home-retry-a" || got[1] != "home-retry-b" {
				t.Fatalf("executor calls = %v, want [home-retry-a home-retry-b]", got)
			}
			excluded := dispatcher.Excluded()
			if len(excluded) != 2 || len(excluded[0]) != 0 || len(excluded[1]) != 1 || excluded[1][0] != "home-retry-a" {
				t.Fatalf("Home excluded auth IDs = %v, want [[], [home-retry-a]]", excluded)
			}
		})
	}
}

func TestHomeCountTokensTriesFreshCredentialWhenRequestRetryIsZero(t *testing.T) {
	dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a", "home-retry-b"}}
	executor := &retryContractHomeExecutor{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(0, time.Second, 2)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)

	response, errExecute := manager.ExecuteCount(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("ExecuteCount() error = %v", errExecute)
	}
	if string(response.Payload) != "home-retry-b" {
		t.Fatalf("response payload = %q, want home-retry-b", string(response.Payload))
	}
	if got := executor.Calls(); len(got) != 2 || got[0] != "home-retry-a" || got[1] != "home-retry-b" {
		t.Fatalf("executor calls = %v, want [home-retry-a home-retry-b]", got)
	}
	excluded := dispatcher.Excluded()
	if len(excluded) != 2 || len(excluded[0]) != 0 || len(excluded[1]) != 1 || excluded[1][0] != "home-retry-a" {
		t.Fatalf("Home excluded auth IDs = %v, want [[], [home-retry-a]]", excluded)
	}
}

func TestHomeRetryPolicyAllowsRemoteCooldownWithoutLocalCredentials(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(1, time.Second, 0)
	errRemoteCooldown := &homeDispatchRetryAfterError{
		cause:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "all Home credentials are cooling down"},
		retryAfter: 10 * time.Millisecond,
	}

	wait, shouldRetry := manager.shouldRetryAfterError(errRemoteCooldown, 0, []string{"home-retry-contract"}, "gpt", time.Second)
	if !shouldRetry || wait != 10*time.Millisecond {
		t.Fatalf("shouldRetryAfterError() = (%v, %t), want (10ms, true)", wait, shouldRetry)
	}
	if _, shouldRetry = manager.shouldRetryAfterError(errRemoteCooldown, 1, []string{"home-retry-contract"}, "gpt", time.Second); shouldRetry {
		t.Fatal("shouldRetryAfterError() retried after the configured Home retry round")
	}
	wait, shouldRetry = manager.shouldRetryAfterError(errRemoteCooldown, 0, []string{"home-retry-contract"}, "gpt", 0)
	if shouldRetry || wait != 0 {
		t.Fatalf("shouldRetryAfterError() with zero wait interval = (%v, %t), want (0, false)", wait, shouldRetry)
	}
	errRoundExhausted := markHomeRetryRoundExhausted(&Error{HTTPStatus: http.StatusBadGateway, Message: "upstream unavailable"}, nil, false)
	wait, shouldRetry = manager.shouldRetryAfterError(errRoundExhausted, 0, []string{"home-retry-contract"}, "gpt", 0)
	if !shouldRetry || wait != 0 {
		t.Fatalf("shouldRetryAfterError() immediate round = (%v, %t), want (0, true)", wait, shouldRetry)
	}
	var invalidTiming homeRetryRoundTiming
	invalidTiming.Observe(retryContractRateLimitError{retryAfter: -time.Millisecond})
	errInvalidWait := markHomeRetryRoundExhausted(retryContractRateLimitError{retryAfter: -time.Millisecond}, invalidTiming.RetryAfter(), false)
	if wait, shouldRetry = manager.shouldRetryAfterError(errInvalidWait, 0, []string{"home-retry-contract"}, "gpt", 0); shouldRetry || wait != 0 {
		t.Fatalf("shouldRetryAfterError() negative wait = (%v, %t), want (0, false)", wait, shouldRetry)
	}
}

func TestRetryIntervalFiltersCooldownCredentials(t *testing.T) {
	tests := []struct {
		name        string
		cooldowns   []time.Duration
		wantRetry   bool
		maxWantWait time.Duration
	}{
		{name: "short and long cooldowns", cooldowns: []time.Duration{10 * time.Second, time.Minute}, wantRetry: true, maxWantWait: 10 * time.Second},
		{name: "only long cooldown", cooldowns: []time.Duration{time.Minute}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const (
				provider = "retry-interval-contract"
				model    = "gpt"
			)
			manager := NewManager(nil, nil, nil)
			manager.SetRetryConfig(1, 30*time.Second, 0)
			now := time.Now()
			for index, cooldown := range test.cooldowns {
				authID := fmt.Sprintf("retry-interval-%s-%d", strings.ReplaceAll(test.name, " ", "-"), index)
				deadline := now.Add(cooldown)
				auth := &Auth{
					ID:       authID,
					Provider: provider,
					Status:   StatusActive,
					ModelStates: map[string]*ModelState{
						model: {
							Status:         StatusError,
							Unavailable:    true,
							NextRetryAfter: deadline,
							LastError:      &Error{HTTPStatus: http.StatusTooManyRequests},
							Quota:          QuotaState{Exceeded: true, NextRecoverAt: deadline},
						},
					},
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
				if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
					t.Fatalf("Register() error = %v", errRegister)
				}
			}

			wait, shouldRetry := manager.shouldRetryAfterError(&Error{HTTPStatus: http.StatusTooManyRequests}, 0, []string{provider}, model, 30*time.Second)
			if shouldRetry != test.wantRetry {
				t.Fatalf("shouldRetryAfterError() = (%v, %t), want retry %t", wait, shouldRetry, test.wantRetry)
			}
			if test.wantRetry && (wait <= 0 || wait > test.maxWantWait) {
				t.Fatalf("shouldRetryAfterError() wait = %v, want the earliest cooldown within %v", wait, test.maxWantWait)
			}
		})
	}
}

func TestHomeRetryPolicyUsesRemoteCredentialOverrideBeforeSelection(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(0, time.Second, 0)
	errRemoteCooldown := &homeDispatchRetryAfterError{
		cause:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "all Home credentials are cooling down"},
		retryAfter:      10 * time.Millisecond,
		requestRetry:    1,
		hasRequestRetry: true,
	}

	wait, shouldRetry := manager.shouldRetryAfterErrorWithHomeRetryLimit(context.Background(), cliproxyexecutor.Options{}, errRemoteCooldown, 0, []string{"home-retry-contract"}, "gpt", time.Second, -1, 0)
	if !shouldRetry || wait != 10*time.Millisecond {
		t.Fatalf("remote credential override retry = (%v, %t), want (10ms, true)", wait, shouldRetry)
	}
	if _, shouldRetry = manager.shouldRetryAfterErrorWithHomeRetryLimit(context.Background(), cliproxyexecutor.Options{}, errRemoteCooldown, 1, []string{"home-retry-contract"}, "gpt", time.Second, -1, 0); shouldRetry {
		t.Fatal("remote credential override allowed more than one additional round")
	}
	pinnedOpts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.PinnedAuthMetadataKey: "home-retry-a",
	}}
	if _, shouldRetry = manager.shouldRetryAfterErrorWithHomeRetryLimit(context.Background(), pinnedOpts, errRemoteCooldown, 0, []string{"home-retry-contract"}, "gpt", time.Second, -1, 0); shouldRetry {
		t.Fatal("aggregate retry limit from unpinned Home credentials affected a pinned request")
	}

	errRemoteCooldown.requestRetry = 0
	manager.SetRetryConfig(3, time.Second, 0)
	if _, shouldRetry = manager.shouldRetryAfterErrorWithHomeRetryLimit(context.Background(), cliproxyexecutor.Options{}, errRemoteCooldown, 0, []string{"home-retry-contract"}, "gpt", time.Second, -1, 0); shouldRetry {
		t.Fatal("explicit remote credential override 0 did not suppress the global retry setting")
	}
	retryLimit := 3
	observeHomeCooldownRetryLimit(errRemoteCooldown, &retryLimit, true)
	if retryLimit != 0 {
		t.Fatalf("observed remote cooldown retry limit = %d, want authoritative 0", retryLimit)
	}
}

func TestHomeRetryRoundCredentialLimitStartsNextRoundImmediately(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(1, time.Second, 1)
	retryAfter := 5 * time.Second
	errRoundExhausted := markHomeRetryRoundExhausted(
		retryContractRateLimitError{retryAfter: retryAfter},
		&retryAfter,
		true,
	)

	wait, shouldRetry := manager.shouldRetryAfterError(errRoundExhausted, 0, []string{"home-retry-contract"}, "gpt", time.Second)
	if !shouldRetry || wait != 0 {
		t.Fatalf("credential-limit retry = (%v, %t), want immediate next round", wait, shouldRetry)
	}
	if got := SafeResponseHeaders(errRoundExhausted).Get("Retry-After"); got != "5" {
		t.Fatalf("safe Retry-After header = %q, want 5", got)
	}
}

func TestHomeCredentialLimitWaitsBeforeConsumingAdditionalRound(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			dispatcher := &retryRoundStartCooldownDispatcher{}
			executor := &retryContractHomeExecutor{failAll: true}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(1, time.Second, 1)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)

			if stream {
				result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
				if errExecute == nil || result != nil {
					t.Fatalf("ExecuteStream() = result %#v, error %v; want terminal retry error", result, errExecute)
				}
			} else {
				if _, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{}); errExecute == nil {
					t.Fatal("Execute() error = nil, want terminal retry error")
				}
			}
			if got := executor.Calls(); len(got) != 2 {
				t.Fatalf("executor calls = %v, want one execution in each of two rounds", got)
			}
			if got := dispatcher.calls.Load(); got != 3 {
				t.Fatalf("Home dispatch calls = %d, want selection, cooldown wait, selection", got)
			}
		})
	}
}

func TestHomePendingRetryRoundStopsWhenRemoteLimitDrops(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			dispatcher := &retryRoundLimitDownshiftDispatcher{}
			executor := &retryContractHomeExecutor{}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(1, time.Second, 1)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)

			if stream {
				result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
				if errExecute == nil || result != nil {
					t.Fatalf("ExecuteStream() = result %#v, error %v; want terminal cooldown error", result, errExecute)
				}
			} else {
				if _, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{}); errExecute == nil {
					t.Fatal("Execute() error = nil, want terminal cooldown error")
				}
			}
			if got := executor.Calls(); len(got) != 1 || got[0] != "home-retry-a" {
				t.Fatalf("executor calls = %v, want only the initial credential", got)
			}
			if got := dispatcher.calls.Load(); got != 2 {
				t.Fatalf("Home dispatch calls = %d, want initial selection and one cooldown response", got)
			}
		})
	}
}

func TestHomePendingRetryRoundStopsAfterRepeatedCooldown(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			dispatcher := &retryRoundRepeatedCooldownDispatcher{}
			executor := &retryContractHomeExecutor{failAll: true}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(1, time.Second, 1)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)

			var errExecute error
			if stream {
				var result *cliproxyexecutor.StreamResult
				result, errExecute = manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
				if errExecute == nil || result != nil {
					t.Fatalf("ExecuteStream() = result %#v, error %v; want terminal cooldown error", result, errExecute)
				}
			} else {
				_, errExecute = manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
				if errExecute == nil {
					t.Fatal("Execute() error = nil, want terminal cooldown error")
				}
			}
			var cooldown *homeDispatchRetryAfterError
			if !errors.As(errExecute, &cooldown) || cooldown == nil || cooldown.cause == nil || cooldown.cause.Code != "model_cooldown" {
				t.Fatalf("terminal error = %#v, want current Home model cooldown", errExecute)
			}
			if got := executor.Calls(); len(got) != 1 {
				t.Fatalf("executor calls = %v, want only the initial round execution", got)
			}
			if got := dispatcher.calls.Load(); got != 3 {
				t.Fatalf("Home dispatch calls = %d, want selection and two cooldown responses", got)
			}
		})
	}
}

func TestHomeRetryRoundUsesEarliestCredentialRetryAfter(t *testing.T) {
	dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a", "home-retry-b"}}
	executor := &retryContractHomeExecutor{
		failAll: true,
		failures: map[string]error{
			"home-retry-a": retryContractRateLimitError{retryAfter: 5 * time.Millisecond},
			"home-retry-b": retryContractRateLimitError{retryAfter: 50 * time.Millisecond},
		},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(1, time.Second, 2)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)

	retryLimit := -1
	_, errExecute := manager.executeHomeOnce(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{}, false, 2, &retryLimit)
	if !isHomeRetryRoundExhausted(errExecute) {
		t.Fatalf("executeHomeOnce() error = %v, want exhausted retry round", errExecute)
	}
	retryAfter := retryAfterFromError(errExecute)
	if retryAfter == nil || *retryAfter != 5*time.Millisecond {
		t.Fatalf("retry after = %v, want earliest credential delay 5ms", retryAfter)
	}
}

func TestHomePreferredErrorIgnoresLaterInternalExecutorFailure(t *testing.T) {
	upstreamErr := &Error{Message: "model upstream failed", HTTPStatus: http.StatusBadGateway}
	internalErr := errors.New("Home KV lookup failed")
	dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a", "home-retry-b"}}
	executor := &retryContractHomeExecutor{
		failAll: true,
		failures: map[string]error{
			"home-retry-a": upstreamErr,
			"home-retry-b": internalErr,
		},
		internalFailures: map[string]bool{"home-retry-b": true},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(0, 0, 2)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)

	_, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
	if !errors.Is(errExecute, upstreamErr) {
		t.Fatalf("Execute() error = %v, want earlier model upstream error", errExecute)
	}
	if errors.Is(errExecute, internalErr) {
		t.Fatalf("Execute() error = %v, later internal executor failure replaced upstream error", errExecute)
	}
}

func TestHomeSelectionFailureIsNotHiddenByEarlierUpstreamAttempt(t *testing.T) {
	tests := []struct {
		name             string
		exhaustedPayload []byte
		exhaustedErr     error
		wantCode         string
	}{
		{name: "Home unavailable", exhaustedErr: errors.New("Home transport failed"), wantCode: "home_unavailable"},
		{name: "invalid auth payload", exhaustedPayload: []byte(`{}`), wantCode: "invalid_auth"},
		{
			name:             "malformed concurrency payload",
			exhaustedPayload: []byte(`{"concurrency":{"accounted":true,"credential_id":"home-retry-a","model":"gpt"},"error":"busy","auth":{"id":"home-retry-a","provider":"home-retry-contract"}}`),
			wantCode:         "invalid_home_concurrency",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstreamErr := &Error{Message: "model upstream failed", HTTPStatus: http.StatusBadGateway}
			dispatcher := &retryContractHomeDispatcher{
				authIDs:          []string{"home-retry-a"},
				exhaustedPayload: test.exhaustedPayload,
				exhaustedErr:     test.exhaustedErr,
			}
			executor := &retryContractHomeExecutor{failAll: true, failure: upstreamErr}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)

			retryLimit := -1
			_, errExecute := manager.executeHomeOnce(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{}, false, 2, &retryLimit)
			var authErr *Error
			if !errors.As(errExecute, &authErr) || authErr == nil || authErr.Code != test.wantCode {
				t.Fatalf("executeHomeOnce() error = %#v, want selection error %q", errExecute, test.wantCode)
			}
			if errors.Is(errExecute, upstreamErr) {
				t.Fatalf("executeHomeOnce() error = %v, earlier upstream error hid selection failure", errExecute)
			}
		})
	}
}

func TestHomeNextRoundSelectionFailureSupersedesEarlierUpstreamAttempt(t *testing.T) {
	selectionFailures := []struct {
		name    string
		payload []byte
		err     error
		code    string
	}{
		{name: "Home unavailable", err: errors.New("Home transport failed"), code: "home_unavailable"},
		{name: "invalid auth payload", payload: []byte(`{}`), code: "invalid_auth"},
	}
	for _, selectionFailure := range selectionFailures {
		for _, stream := range []bool{false, true} {
			name := selectionFailure.name + "/" + map[bool]string{false: "nonstream", true: "stream"}[stream]
			t.Run(name, func(t *testing.T) {
				upstreamErr := &Error{Message: "model upstream failed", HTTPStatus: http.StatusBadGateway}
				dispatcher := &retryRoundSelectionFailureDispatcher{
					selectionPayload: selectionFailure.payload,
					selectionErr:     selectionFailure.err,
				}
				executor := &retryContractHomeExecutor{failAll: true, failure: upstreamErr}
				manager := NewManager(nil, nil, nil)
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
				manager.SetRetryConfig(1, time.Second, 1)
				manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
				manager.RegisterExecutor(executor)

				var errExecute error
				if stream {
					var result *cliproxyexecutor.StreamResult
					result, errExecute = manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
					if result != nil {
						t.Fatalf("ExecuteStream() result = %#v, want nil", result)
					}
				} else {
					_, errExecute = manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
				}
				var authErr *Error
				if !errors.As(errExecute, &authErr) || authErr == nil || authErr.Code != selectionFailure.code {
					t.Fatalf("terminal error = %#v, want current selection error %q", errExecute, selectionFailure.code)
				}
				if errors.Is(errExecute, upstreamErr) {
					t.Fatalf("terminal error = %v, earlier upstream error hid current selection failure", errExecute)
				}
			})
		}
	}
}

func TestHomeStreamBootstrapErrorPreservesAggregatedRetryAfter(t *testing.T) {
	dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a", "home-retry-b"}}
	executor := &retryContractHomeExecutor{
		failAll:         true,
		streamBootstrap: true,
		streamHeaders:   http.Header{"Retry-After": {"30"}},
		failures: map[string]error{
			"home-retry-a": retryContractRateLimitError{retryAfter: 1500 * time.Millisecond},
			"home-retry-b": retryContractRateLimitError{retryAfter: 5 * time.Second},
		},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(0, time.Second, 2)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)

	result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	if result == nil {
		t.Fatal("ExecuteStream() result = nil")
	}
	chunk, ok := <-result.Chunks
	if !ok || chunk.Err == nil {
		t.Fatalf("stream bootstrap chunk = %#v, %t; want terminal error", chunk, ok)
	}
	if !isHomeRetryRoundExhausted(chunk.Err) {
		t.Fatalf("stream bootstrap error = %v, want exhausted retry round", chunk.Err)
	}
	if hasUpstreamExecutionAttempt(chunk.Err) {
		t.Fatalf("stream bootstrap error leaked internal upstream-attempt marker: %T/%v", chunk.Err, chunk.Err)
	}
	if got := SafeResponseHeaders(chunk.Err).Get("Retry-After"); got != "2" {
		t.Fatalf("safe Retry-After header = %q, want aggregated delay rounded to 2 seconds", got)
	}
}

func TestHomeRetryRoundUsesAuthoritativeRemoteCooldown(t *testing.T) {
	tests := []struct {
		name    string
		execute func(*Manager, *int) error
	}{
		{
			name: "nonstream",
			execute: func(manager *Manager, retryLimit *int) error {
				_, errExecute := manager.executeHomeOnce(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{}, false, 2, retryLimit)
				return errExecute
			},
		},
		{
			name: "stream",
			execute: func(manager *Manager, retryLimit *int) error {
				_, errExecute := manager.executeStreamMixedOnce(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true}, 2, retryLimit, 0, 0)
				return errExecute
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher := &retryContractHomeDispatcher{
				authIDs:          []string{"home-retry-a"},
				exhaustedPayload: []byte(`{"error":{"type":"model_cooldown","message":"remaining Home credentials are cooling down","retryable":true,"retry_after_ms":5000}}`),
			}
			executor := &retryContractHomeExecutor{
				failAll: true,
				failure: retryContractRateLimitError{retryAfter: 1500 * time.Millisecond},
			}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(1, time.Second, 2)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)

			retryLimit := -1
			errExecute := tc.execute(manager, &retryLimit)
			if !isHomeRetryRoundExhausted(errExecute) {
				t.Fatalf("execution error = %v, want exhausted retry round", errExecute)
			}
			retryAfter := retryAfterFromError(errExecute)
			if retryAfter == nil || *retryAfter != 5*time.Second {
				t.Fatalf("retry after = %v, want Home next-round cooldown 5s", retryAfter)
			}
			if got := SafeResponseHeaders(errExecute).Get("Retry-After"); got != "5" {
				t.Fatalf("safe Retry-After header = %q, want Home next-round delay 5 seconds", got)
			}
		})
	}
}

func TestHomeCooldownClassificationPreservesNonRetryableRoundStatus(t *testing.T) {
	tests := []struct {
		name    string
		execute func(*Manager, *int) error
	}{
		{
			name: "nonstream",
			execute: func(manager *Manager, retryLimit *int) error {
				_, errExecute := manager.executeHomeOnce(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{}, false, 2, retryLimit)
				return errExecute
			},
		},
		{
			name: "stream",
			execute: func(manager *Manager, retryLimit *int) error {
				_, errExecute := manager.executeStreamMixedOnce(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true}, 2, retryLimit, 0, 0)
				return errExecute
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &retryContractHomeDispatcher{
				authIDs:          []string{"home-retry-a"},
				exhaustedPayload: []byte(`{"error":{"type":"model_cooldown","message":"another credential is cooling down","retryable":true,"retry_after_ms":5,"request_retry":2}}`),
			}
			executor := &retryContractHomeExecutor{
				failAll: true,
				failure: &Error{HTTPStatus: http.StatusUnauthorized, Message: "invalid credential"},
			}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(3, time.Second, 2)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)

			retryLimit := -1
			errExecute := test.execute(manager, &retryLimit)
			if !isHomeRetryRoundExhausted(errExecute) || statusCodeFromError(errExecute) != http.StatusUnauthorized {
				t.Fatalf("execution error = %T %v, want exhausted 401 round", errExecute, errExecute)
			}
			if retryLimit != 2 {
				t.Fatalf("observed retry limit = %d, want authoritative Home limit 2", retryLimit)
			}
			if wait, shouldRetry := manager.shouldRetryAfterErrorWithHomeRetryLimit(context.Background(), cliproxyexecutor.Options{}, errExecute, 0, []string{"home-retry-contract"}, "gpt", time.Second, retryLimit, 0); shouldRetry || wait != 0 {
				t.Fatalf("401 round retry = (%v, %t), want (0, false)", wait, shouldRetry)
			}
		})
	}
}

func TestHomeRetryRoundStartsImmediatelyWhenHomeReportsAvailableNextRound(t *testing.T) {
	tests := []struct {
		name    string
		execute func(*Manager, *int) error
	}{
		{
			name: "nonstream",
			execute: func(manager *Manager, retryLimit *int) error {
				_, errExecute := manager.executeHomeOnce(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{}, false, 0, retryLimit)
				return errExecute
			},
		},
		{
			name: "stream",
			execute: func(manager *Manager, retryLimit *int) error {
				_, errExecute := manager.executeStreamMixedOnce(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true}, 0, retryLimit, 0, 0)
				return errExecute
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &retryContractHomeDispatcher{
				authIDs:          []string{"home-retry-a", "home-retry-b"},
				exhaustedPayload: []byte(`{"error":{"type":"auth_unavailable","message":"a credential is immediately available next round"}}`),
			}
			executor := &retryContractHomeExecutor{
				failAll: true,
				failures: map[string]error{
					"home-retry-a": retryContractRateLimitError{retryAfter: 5 * time.Second},
					"home-retry-b": &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream unavailable"},
				},
			}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(1, 10*time.Second, 0)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)

			retryLimit := -1
			errExecute := test.execute(manager, &retryLimit)
			if !isHomeRetryRoundExhausted(errExecute) {
				t.Fatalf("execution error = %v, want exhausted retry round", errExecute)
			}
			wait, shouldRetry := manager.shouldRetryAfterErrorWithHomeRetryLimit(context.Background(), cliproxyexecutor.Options{}, errExecute, 0, []string{"home-retry-contract"}, "gpt", 10*time.Second, retryLimit, 0)
			if !shouldRetry || wait != 0 {
				t.Fatalf("next-round retry = (%v, %t), want immediate", wait, shouldRetry)
			}
		})
	}
}

func TestHomeRetryRoundUsesRemoteCooldownWhenAttemptedErrorHasNoTiming(t *testing.T) {
	tests := []struct {
		name    string
		execute func(*Manager, *int) error
	}{
		{
			name: "nonstream",
			execute: func(manager *Manager, retryLimit *int) error {
				_, errExecute := manager.executeHomeOnce(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{}, false, 2, retryLimit)
				return errExecute
			},
		},
		{
			name: "stream",
			execute: func(manager *Manager, retryLimit *int) error {
				_, errExecute := manager.executeStreamMixedOnce(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true}, 2, retryLimit, 0, 0)
				return errExecute
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher := &retryContractHomeDispatcher{
				authIDs:          []string{"home-retry-a"},
				exhaustedPayload: []byte(`{"error":{"type":"model_cooldown","message":"remaining Home credentials are cooling down","retryable":true,"retry_after_ms":1500}}`),
			}
			executor := &retryContractHomeExecutor{
				failAll: true,
				failure: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream unavailable"},
			}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(1, 2*time.Second, 2)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)

			retryLimit := -1
			errExecute := tc.execute(manager, &retryLimit)
			if !isHomeRetryRoundExhausted(errExecute) {
				t.Fatalf("execution error = %v, want exhausted retry round", errExecute)
			}
			retryAfter := retryAfterFromError(errExecute)
			if retryAfter == nil || *retryAfter != 1500*time.Millisecond {
				t.Fatalf("retry after = %v, want remote cooldown delay 1500ms", retryAfter)
			}
			if got := SafeResponseHeaders(errExecute).Get("Retry-After"); got != "2" {
				t.Fatalf("safe Retry-After header = %q, want 2", got)
			}
		})
	}
}

func TestHomeStreamOAuthUnauthorizedRotatesWithoutRefreshRetry(t *testing.T) {
	dispatcher := &retryContractHomeDispatcher{
		authIDs: []string{"home-retry-a", "home-retry-b"},
		metadata: map[string]any{
			"auth_kind": "oauth",
		},
	}
	executor := &retryContractHomeExecutor{
		failures: map[string]error{
			"home-retry-a": &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired"},
		},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(0, time.Second, 2)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)

	result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for range result.Chunks {
	}
	if got := executor.Calls(); len(got) != 2 || got[0] != "home-retry-a" || got[1] != "home-retry-b" {
		t.Fatalf("executor calls = %v, want [home-retry-a home-retry-b]", got)
	}
	excluded := dispatcher.Excluded()
	if len(excluded) != 2 || len(excluded[0]) != 0 || len(excluded[1]) != 1 || excluded[1][0] != "home-retry-a" {
		t.Fatalf("Home excluded auth IDs = %v, want [[], [home-retry-a]]", excluded)
	}
}

func TestHomeStreamLifecycleRecoveryFailureRotatesWithoutExtraDispatch(t *testing.T) {
	dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a", "home-retry-b"}}
	executor := &retryContractHomeExecutor{
		failures: map[string]error{
			"home-retry-a": errors.New("unexpected EOF"),
		},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(0, time.Second, 2)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)

	result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for range result.Chunks {
	}
	if got := executor.Calls(); len(got) != 3 || got[0] != "home-retry-a" || got[1] != "home-retry-a" || got[2] != "home-retry-b" {
		t.Fatalf("executor calls = %v, want [home-retry-a home-retry-a home-retry-b]", got)
	}
	excluded := dispatcher.Excluded()
	if len(excluded) != 3 || len(excluded[0]) != 0 || len(excluded[1]) != 0 || len(excluded[2]) != 1 || excluded[2][0] != "home-retry-a" {
		t.Fatalf("Home excluded auth IDs = %v, want [[], [], [home-retry-a]]", excluded)
	}
}

func TestRetryRoundAvailabilityRejectsStaleQuotaForNonRetryableStatus(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name      string
		lastError *Error
		want      bool
	}{
		{name: "implicit quota", want: true},
		{name: "rate limit", lastError: &Error{HTTPStatus: http.StatusTooManyRequests}, want: true},
		{name: "payment required", lastError: &Error{HTTPStatus: http.StatusPaymentRequired}, want: false},
		{name: "not found", lastError: &Error{HTTPStatus: http.StatusNotFound}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			nextRetry := now.Add(time.Minute)
			auth := &Auth{
				ID:       "retry-round-stale-quota",
				Provider: "codex",
				Status:   StatusActive,
				ModelStates: map[string]*ModelState{
					"gpt": {
						Status:         StatusError,
						Unavailable:    true,
						NextRetryAfter: nextRetry,
						LastError:      test.lastError,
						Quota:          QuotaState{Exceeded: true, NextRecoverAt: nextRetry},
					},
				},
			}
			got, next := retryRoundAvailabilityForAuth(auth, "gpt", now)
			if got != test.want {
				t.Fatalf("retryRoundAvailabilityForAuth() eligible = %t, want %t", got, test.want)
			}
			if got && !next.Equal(nextRetry) {
				t.Fatalf("retryRoundAvailabilityForAuth() next = %v, want %v", next, nextRetry)
			}
		})
	}
}

func TestHomeStreamAPIKeyUnauthorizedRotatesImmediately(t *testing.T) {
	dispatcher := &retryContractHomeDispatcher{
		authIDs: []string{"home-retry-a", "home-retry-b"},
		metadata: map[string]any{
			"auth_kind": "apikey",
		},
	}
	executor := &retryContractHomeExecutor{
		failures: map[string]error{
			"home-retry-a": &Error{HTTPStatus: http.StatusUnauthorized, Message: "invalid api key"},
		},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(0, time.Second, 2)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)

	result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for range result.Chunks {
	}
	if got := executor.Calls(); len(got) != 2 || got[0] != "home-retry-a" || got[1] != "home-retry-b" {
		t.Fatalf("executor calls = %v, want [home-retry-a home-retry-b]", got)
	}
	excluded := dispatcher.Excluded()
	if len(excluded) != 2 || len(excluded[0]) != 0 || len(excluded[1]) != 1 || excluded[1][0] != "home-retry-a" {
		t.Fatalf("Home excluded auth IDs = %v, want [[], [home-retry-a]]", excluded)
	}
}

func TestHomeModelCooldownErrorPreservesRetryContract(t *testing.T) {
	errDecoded := decodeHomeDispatchError([]byte(`{"error":{"type":"model_cooldown","message":"all credentials are cooling down","retryable":true,"retry_after_ms":1500,"request_retry":2}}`))
	var retryErr *homeDispatchRetryAfterError
	if !errors.As(errDecoded, &retryErr) || retryErr == nil {
		t.Fatalf("decodeHomeDispatchError() = %#v, want retry-after error", errDecoded)
	}
	if retryErr.StatusCode() != http.StatusTooManyRequests || retryErr.RetryAfter() == nil || *retryErr.RetryAfter() != 1500*time.Millisecond {
		t.Fatalf("decoded Home cooldown = status %d retry-after %v, want 429/1500ms", retryErr.StatusCode(), retryErr.RetryAfter())
	}
	if retryLimit, ok := retryErr.RequestRetryLimit(); !ok || retryLimit != 2 {
		t.Fatalf("decoded Home request retry limit = (%d, %t), want (2, true)", retryLimit, ok)
	}
	var cause *Error
	if !errors.As(errDecoded, &cause) || cause == nil || cause.Code != "model_cooldown" || !cause.Retryable {
		t.Fatalf("decoded Home cooldown cause = %#v, want retryable model_cooldown", cause)
	}
	if got := SafeResponseHeaders(errDecoded).Get("Retry-After"); got != "2" {
		t.Fatalf("safe Retry-After header = %q, want 2", got)
	}
}

func TestHomeRequestRetryCountsAdditionalCredentialRounds(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a", "home-retry-b"}}
			executor := &retryContractHomeExecutor{failAll: true}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(1, time.Second, 2)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)

			if stream {
				result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
				if errExecute == nil || result != nil {
					t.Fatalf("ExecuteStream() = result %#v, error %v; want terminal rate-limit error", result, errExecute)
				}
			} else {
				_, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
				if errExecute == nil {
					t.Fatal("Execute() error = nil, want rate-limit error")
				}
			}
			if got := executor.Calls(); len(got) != 4 {
				t.Fatalf("executor calls = %v, want four calls across two rounds", got)
			}
			excluded := dispatcher.Excluded()
			if len(excluded) != 4 || len(excluded[0]) != 0 || len(excluded[1]) != 1 || excluded[1][0] != "home-retry-a" || len(excluded[2]) != 0 || len(excluded[3]) != 1 || excluded[3][0] != "home-retry-a" {
				t.Fatalf("Home excluded auth IDs = %v, want [[], [home-retry-a], [], [home-retry-a]]", excluded)
			}
		})
	}
}

func TestHomeRequestRetryRoundDoesNotRequireRetryAfter(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a", "home-retry-b"}}
			executor := &retryContractHomeExecutor{
				failAll: true,
				failure: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream unavailable"},
			}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(1, time.Second, 2)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)

			if stream {
				result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
				if errExecute == nil || result != nil {
					t.Fatalf("ExecuteStream() = result %#v, error %v; want terminal upstream error", result, errExecute)
				}
			} else {
				_, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
				if errExecute == nil {
					t.Fatal("Execute() error = nil, want upstream error")
				}
			}
			if got := executor.Calls(); len(got) != 4 {
				t.Fatalf("executor calls = %v, want four calls across two rounds", got)
			}
		})
	}
}

func TestHomeStreamLegacyDispatcherDoesNotSpinOnIgnoredExclusions(t *testing.T) {
	dispatcher := &legacyRepeatedStreamDispatcher{}
	executor := &retryContractHomeExecutor{
		failAll: true,
		failure: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream unavailable"},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(1, time.Second, 2)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)

	result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
	if result != nil || errExecute == nil {
		t.Fatalf("ExecuteStream() = result %#v, error %v; want terminal upstream error", result, errExecute)
	}
	if got := len(executor.Calls()); got != 2 {
		t.Fatalf("executor calls = %d, want one attempt in each of two rounds", got)
	}
	if got := dispatcher.calls.Load(); got != 4 {
		t.Fatalf("legacy Home dispatch calls = %d, want two dispatches in each of two rounds", got)
	}
}

func TestHomeNonStreamLegacyDispatcherCompletesAdditionalRetryRound(t *testing.T) {
	dispatcher := &legacyRepeatedStreamDispatcher{}
	executor := &retryContractHomeExecutor{
		failAll: true,
		failure: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream unavailable"},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(1, time.Second, 0)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)

	_, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want terminal upstream error")
	}
	if got := len(executor.Calls()); got != 2 {
		t.Fatalf("executor calls = %d, want one attempt in each of two rounds", got)
	}
	if got := dispatcher.calls.Load(); got != 4 {
		t.Fatalf("legacy Home dispatch calls = %d, want two dispatches in each of two rounds", got)
	}
}

func TestHomeLocalSelectionRejectionWaitsForReleaseAcknowledgement(t *testing.T) {
	tests := []struct {
		name                string
		dispatcher          homeAuthDispatcher
		executor            *retryContractHomeExecutor
		maxRetryCredentials int
		blockedGroup        executionregistry.ReleaseGroup
		blockedSequence     int64
		execute             func(*Manager, int, *int) error
	}{
		{
			name: "nonstream repeated auth",
			dispatcher: &accountedHomeExecutionDispatcher{auths: []Auth{
				{ID: "home-retry-a", Provider: "home-retry-contract", Status: StatusActive},
				{ID: "home-retry-a", Provider: "home-retry-contract", Status: StatusActive},
			}},
			executor:            &retryContractHomeExecutor{failure: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream unavailable"}},
			maxRetryCredentials: 0,
			blockedGroup:        executionregistry.ReleaseGroup{CredentialID: "home-retry-a", Model: "gpt"},
			blockedSequence:     2,
			execute: func(manager *Manager, maxRetryCredentials int, retryLimit *int) error {
				_, errExecute := manager.executeHomeOnce(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{}, false, maxRetryCredentials, retryLimit)
				return errExecute
			},
		},
		{
			name: "stream repeated excluded auth",
			dispatcher: &accountedHomeExecutionDispatcher{auths: []Auth{
				{ID: "home-retry-a", Provider: "home-retry-contract", Status: StatusActive},
				{ID: "home-retry-a", Provider: "home-retry-contract", Status: StatusActive},
			}},
			executor:            &retryContractHomeExecutor{failure: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream unavailable"}},
			maxRetryCredentials: 0,
			blockedGroup:        executionregistry.ReleaseGroup{CredentialID: "home-retry-a", Model: "gpt"},
			blockedSequence:     2,
			execute: func(manager *Manager, maxRetryCredentials int, retryLimit *int) error {
				_, errExecute := manager.executeStreamMixedOnce(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true}, maxRetryCredentials, retryLimit, 0, 0)
				return errExecute
			},
		},
		{
			name: "stream max retry credentials",
			dispatcher: &accountedHomeExecutionDispatcher{auths: []Auth{
				{ID: "home-retry-a", Provider: "home-retry-contract", Status: StatusActive},
				{ID: "home-retry-b", Provider: "home-retry-contract", Status: StatusActive},
			}},
			executor:            &retryContractHomeExecutor{failure: errors.New("unexpected EOF")},
			maxRetryCredentials: 1,
			blockedGroup:        executionregistry.ReleaseGroup{CredentialID: "home-retry-b", Model: "gpt"},
			blockedSequence:     1,
			execute: func(manager *Manager, maxRetryCredentials int, retryLimit *int) error {
				_, errExecute := manager.executeStreamMixedOnce(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true}, maxRetryCredentials, retryLimit, 0, 0)
				return errExecute
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := executionregistry.New()
			acknowledged := make(chan struct{})
			close(acknowledged)
			unacknowledged := make(chan struct{})
			var blockedReleaseSeen atomic.Bool
			registry.SetReleaseSink(func(group executionregistry.ReleaseGroup, sequence int64) *executionregistry.ReleaseTicket {
				done := (<-chan struct{})(acknowledged)
				if group == test.blockedGroup && sequence == test.blockedSequence {
					blockedReleaseSeen.Store(true)
					done = unacknowledged
				}
				return executionregistry.NewReleaseTicket(group, sequence, done)
			})

			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{
				Home:                  internalconfig.HomeConfig{Enabled: true},
				CredentialConcurrency: internalconfig.CredentialConcurrencyConfig{CPACancelBound: 10 * time.Millisecond},
			})
			manager.PublishHomeDispatch(test.dispatcher, registry, 1)
			manager.RegisterExecutor(test.executor)

			retryLimit := -1
			errExecute := test.execute(manager, test.maxRetryCredentials, &retryLimit)
			if !blockedReleaseSeen.Load() {
				t.Fatal("target release was not attempted")
			}
			var homeErr *Error
			if !errors.As(errExecute, &homeErr) || homeErr == nil || homeErr.Code != "home_unavailable" {
				t.Fatalf("execution error = %T %v, want Home release acknowledgement timeout", errExecute, errExecute)
			}
		})
	}
}

func TestHomeRetryRoundHonorsCredentialRequestRetryOverride(t *testing.T) {
	tests := []struct {
		name          string
		globalRetry   int
		override      int
		wantCallCount int
	}{
		{name: "override disables global rounds", globalRetry: 3, override: 0, wantCallCount: 2},
		{name: "override enables rounds over global", globalRetry: 0, override: 1, wantCallCount: 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher := &retryContractHomeDispatcher{
				authIDs:  []string{"home-retry-a", "home-retry-b"},
				metadata: map[string]any{"request_retry": tc.override},
			}
			executor := &retryContractHomeExecutor{failAll: true}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(tc.globalRetry, time.Second, 2)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)

			_, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
			if errExecute == nil {
				t.Fatal("Execute() error = nil, want terminal rate-limit error")
			}
			if got := len(executor.Calls()); got != tc.wantCallCount {
				t.Fatalf("executor call count = %d, want %d", got, tc.wantCallCount)
			}
		})
	}
}

func TestHomeRetryRoundUsesSuccessfulDispatchAggregate(t *testing.T) {
	tests := []struct {
		name    string
		execute func(*Manager) error
	}{
		{
			name: "nonstream",
			execute: func(manager *Manager) error {
				_, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "count tokens",
			execute: func(manager *Manager) error {
				_, errExecute := manager.ExecuteCount(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "stream",
			execute: func(manager *Manager) error {
				result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
				if errExecute != nil {
					return errExecute
				}
				for range result.Chunks {
				}
				return nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &aggregateRetryHomeDispatcher{}
			executor := &retryContractHomeExecutor{}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(0, time.Second, 1)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)

			if errExecute := test.execute(manager); errExecute != nil {
				t.Fatalf("execution error = %v", errExecute)
			}
			if got := executor.Calls(); len(got) != 2 || got[0] != "home-retry-a" || got[1] != "home-retry-b" {
				t.Fatalf("executor calls = %v, want [home-retry-a home-retry-b]", got)
			}
			if got := dispatcher.calls.Load(); got != 2 {
				t.Fatalf("Home dispatch calls = %d, want 2", got)
			}
		})
	}
}

func TestHomeRetryRoundUsesAuthoritativeZeroAggregate(t *testing.T) {
	tests := []struct {
		name    string
		execute func(*Manager) error
	}{
		{
			name: "nonstream",
			execute: func(manager *Manager) error {
				_, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "count tokens",
			execute: func(manager *Manager) error {
				_, errExecute := manager.ExecuteCount(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "stream",
			execute: func(manager *Manager) error {
				_, errExecute := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
				return errExecute
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remoteRetry := 0
			dispatcher := &retryContractHomeDispatcher{
				authIDs:      []string{"home-retry-a", "home-retry-b"},
				metadata:     map[string]any{"request_retry": 3},
				requestRetry: &remoteRetry,
			}
			executor := &retryContractHomeExecutor{
				failAll: true,
				failure: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream unavailable"},
			}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(3, time.Second, 2)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)

			if errExecute := test.execute(manager); errExecute == nil {
				t.Fatal("execution error = nil, want terminal first-round error")
			}
			if got := executor.Calls(); len(got) != 2 {
				t.Fatalf("executor calls = %v, want only the two first-round credentials", got)
			}
		})
	}
}
