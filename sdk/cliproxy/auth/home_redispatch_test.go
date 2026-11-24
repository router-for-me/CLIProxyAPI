package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	redispatchTestAccountA  = "home-retry-a"
	redispatchTestAccountB  = "home-retry-b"
	redispatchTestRetryTime = 7 * time.Second
)

// redispatchTestDispatcher hands out accounted credentials in order, honours
// excluded_auth_ids like a current Home, and can fail chosen dispatch calls.
type redispatchTestDispatcher struct {
	mu       sync.Mutex
	authIDs  []string
	calls    int
	excluded [][]string
	fail     func(call int) error
}

func (*redispatchTestDispatcher) HeartbeatOK() bool { return true }

func (d *redispatchTestDispatcher) RPopAuth(ctx context.Context, model string, sessionID string, headers http.Header, count int) ([]byte, error) {
	return d.RPopAuthWithConstraints(ctx, model, sessionID, headers, count, nil, "")
}

func (d *redispatchTestDispatcher) RPopAuthWithConstraints(_ context.Context, model string, _ string, _ http.Header, _ int, excludedAuthIDs []string, _ string) ([]byte, error) {
	d.mu.Lock()
	d.calls++
	call := d.calls
	d.excluded = append(d.excluded, append([]string(nil), excludedAuthIDs...))
	d.mu.Unlock()
	if d.fail != nil {
		if errFail := d.fail(call); errFail != nil {
			return nil, errFail
		}
	}
	excluded := make(map[string]struct{}, len(excludedAuthIDs))
	for _, authID := range excludedAuthIDs {
		excluded[authID] = struct{}{}
	}
	for _, authID := range d.authIDs {
		if _, skip := excluded[authID]; skip {
			continue
		}
		return json.Marshal(struct {
			Concurrency homeConcurrencyTuple `json:"concurrency"`
			Model       string               `json:"model"`
			AuthIndex   string               `json:"auth_index"`
			Auth        Auth                 `json:"auth"`
		}{
			Concurrency: homeConcurrencyTuple{Accounted: true, CredentialID: authID, Model: model},
			Model:       model,
			AuthIndex:   authID,
			Auth:        Auth{ID: authID, Provider: "home-retry-contract", Status: StatusActive},
		})
	}
	return nil, home.ErrAuthNotFound
}

func (*redispatchTestDispatcher) AbortAmbiguousDispatch() {}

func (d *redispatchTestDispatcher) Calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *redispatchTestDispatcher) Excluded() [][]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := make([][]string, len(d.excluded))
	for index := range d.excluded {
		result[index] = append([]string(nil), d.excluded[index]...)
	}
	return result
}

// redispatchTestReleaseSink acknowledges every release at once except the ones for
// blockedCredential, which complete only when ack closes (nil: never).
func redispatchTestReleaseSink(blockedCredential string, ack <-chan struct{}, blocked *atomic.Int32) executionregistry.ReleaseSink {
	acknowledged := make(chan struct{})
	close(acknowledged)
	if ack == nil {
		// A ticket with a nil done channel counts as acknowledged.
		ack = make(chan struct{})
	}
	return func(group executionregistry.ReleaseGroup, sequence int64) *executionregistry.ReleaseTicket {
		done := (<-chan struct{})(acknowledged)
		if group.CredentialID == blockedCredential {
			blocked.Add(1)
			done = ack
		}
		return executionregistry.NewReleaseTicket(group, sequence, done)
	}
}

func newRedispatchTestManager(dispatcher homeAuthDispatcher, executor *retryContractHomeExecutor, sink executionregistry.ReleaseSink) (*Manager, *executionregistry.Registry) {
	registry := executionregistry.New()
	registry.SetReleaseSink(sink)
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		Home:                  internalconfig.HomeConfig{Enabled: true},
		CredentialConcurrency: internalconfig.CredentialConcurrencyConfig{CPACancelBound: 10 * time.Millisecond},
	})
	// No extra request rounds: everything below must resolve inside one round.
	manager.SetRetryConfig(0, time.Second, 0)
	manager.PublishHomeDispatch(dispatcher, registry, 1)
	manager.RegisterExecutor(executor)
	return manager, registry
}

func newRedispatchTestRateLimitedExecutor(streamBootstrap bool) *retryContractHomeExecutor {
	return &retryContractHomeExecutor{
		failures:        map[string]error{redispatchTestAccountA: retryContractRateLimitError{retryAfter: redispatchTestRetryTime}},
		streamBootstrap: streamBootstrap,
	}
}

// redispatchTestStreamOutcome drains a stream and returns its payloads and the first
// error, whether it came back from ExecuteStream or as an in-stream chunk.
func redispatchTestStreamOutcome(result *cliproxyexecutor.StreamResult, errStream error) ([]string, error) {
	if errStream != nil {
		return nil, errStream
	}
	if result == nil || result.Chunks == nil {
		return nil, errors.New("nil stream result")
	}
	var payloads []string
	var firstErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			if firstErr == nil {
				firstErr = chunk.Err
			}
			continue
		}
		payloads = append(payloads, string(chunk.Payload))
	}
	return payloads, firstErr
}

func redispatchTestIsHomeUnavailable(err error) bool {
	return errorCodeFromError(err) == "home_unavailable"
}

func redispatchTestRun(manager *Manager, stream bool) ([]string, error) {
	if stream {
		result, errStream := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
		return redispatchTestStreamOutcome(result, errStream)
	}
	response, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
	if errExecute != nil {
		return nil, errExecute
	}
	return []string{string(response.Payload)}, nil
}

// Account a answers 429 and Home never acknowledges a's release. a is
// excluded from the redispatch, so the call is served by b instead of 503.
func TestHomeRedispatchSkipsAckWaitForExcludedCredential(t *testing.T) {
	tests := []struct {
		name            string
		stream          bool
		streamBootstrap bool
	}{
		{name: "stream start error", stream: true},
		{name: "stream bootstrap error chunk", stream: true, streamBootstrap: true},
		{name: "nonstream", stream: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var blocked atomic.Int32
			dispatcher := &redispatchTestDispatcher{authIDs: []string{redispatchTestAccountA, redispatchTestAccountB}}
			executor := newRedispatchTestRateLimitedExecutor(test.streamBootstrap)
			manager, registry := newRedispatchTestManager(dispatcher, executor, redispatchTestReleaseSink(redispatchTestAccountA, nil, &blocked))

			payloads, errRun := redispatchTestRun(manager, test.stream)
			if errRun != nil {
				t.Fatalf("call error = %T %v, want b to serve it (home_unavailable=%v)", errRun, errRun, redispatchTestIsHomeUnavailable(errRun))
			}
			if !reflect.DeepEqual(payloads, []string{redispatchTestAccountB}) {
				t.Fatalf("payloads = %q, want only account b", payloads)
			}
			if blocked.Load() != 1 {
				t.Fatalf("unacknowledged releases of a = %d, want 1", blocked.Load())
			}
			if calls := executor.Calls(); !reflect.DeepEqual(calls, []string{redispatchTestAccountA, redispatchTestAccountB}) {
				t.Fatalf("executor calls = %v, want a then b", calls)
			}
			if excluded := dispatcher.Excluded(); len(excluded) != 2 || !reflect.DeepEqual(excluded[1], []string{redispatchTestAccountA}) {
				t.Fatalf("dispatch exclusions = %v, want the redispatch to exclude a", excluded)
			}
			if active := registry.FreezeInFlight(time.Time{}).Executions; len(active) != 0 {
				t.Fatalf("execution scopes leaked: %v", active)
			}
		})
	}
}

// A plugin refuses account a for this request, and Home never acknowledges a's
// release. a is excluded from the redispatch, so the call is served by b.
func TestHomeRedispatchSkipsAckWaitForRefusedCredential(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(map[bool]string{true: "stream", false: "nonstream"}[stream], func(t *testing.T) {
			var blocked atomic.Int32
			dispatcher := &redispatchTestDispatcher{authIDs: []string{redispatchTestAccountA, redispatchTestAccountB}}
			executor := newRedispatchTestRateLimitedExecutor(false)
			manager, registry := newRedispatchTestManager(dispatcher, executor, redispatchTestReleaseSink(redispatchTestAccountA, nil, &blocked))
			opts := cliproxyexecutor.Options{
				Stream: stream,
				RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
					if authID, _ := req.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string); authID == redispatchTestAccountA {
						return cliproxyexecutor.RequestAfterAuthInterceptResponse{Terminate: true, TerminateScope: "credential", StatusCode: http.StatusForbidden, ResponseBody: []byte("a refused")}
					}
					return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
				},
			}
			var payloads []string
			var errRun error
			if stream {
				result, errStream := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, opts)
				payloads, errRun = redispatchTestStreamOutcome(result, errStream)
			} else {
				var response cliproxyexecutor.Response
				response, errRun = manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, opts)
				payloads = []string{string(response.Payload)}
			}
			if errRun != nil {
				t.Fatalf("call error = %T %v, want b to serve it (home_unavailable=%v)", errRun, errRun, redispatchTestIsHomeUnavailable(errRun))
			}
			if !reflect.DeepEqual(payloads, []string{redispatchTestAccountB}) {
				t.Fatalf("payloads = %q, want only account b", payloads)
			}
			if blocked.Load() != 1 {
				t.Fatalf("unacknowledged releases of a = %d, want 1", blocked.Load())
			}
			if calls := executor.Calls(); !reflect.DeepEqual(calls, []string{redispatchTestAccountB}) {
				t.Fatalf("executor calls = %v, want only b", calls)
			}
			if active := registry.FreezeInFlight(time.Time{}).Executions; len(active) != 0 {
				t.Fatalf("execution scopes leaked: %v", active)
			}
		})
	}
}

// After a's 429, one dispatch fails with home_unavailable. One more
// dispatch inside the bound serves the call; if that also fails, the caller
// gets a's 429 with its retry time, and any other lastErr keeps the 503.
func TestHomeRedispatchAfterHomeUnavailable(t *testing.T) {
	errDispatch := errors.New("home connection reset")
	for _, stream := range []bool{true, false} {
		mode := map[bool]string{true: "stream", false: "nonstream"}[stream]

		t.Run(mode+"/second dispatch serves b", func(t *testing.T) {
			var blocked atomic.Int32
			dispatcher := &redispatchTestDispatcher{
				authIDs: []string{redispatchTestAccountA, redispatchTestAccountB},
				fail: func(call int) error {
					if call == 2 {
						return errDispatch
					}
					return nil
				},
			}
			executor := newRedispatchTestRateLimitedExecutor(false)
			manager, _ := newRedispatchTestManager(dispatcher, executor, redispatchTestReleaseSink("", nil, &blocked))

			payloads, errRun := redispatchTestRun(manager, stream)
			if errRun != nil {
				t.Fatalf("call error = %T %v, want b to serve it", errRun, errRun)
			}
			if !reflect.DeepEqual(payloads, []string{redispatchTestAccountB}) {
				t.Fatalf("payloads = %q, want only account b", payloads)
			}
			if got := dispatcher.Calls(); got != 3 {
				t.Fatalf("dispatch calls = %d, want a, the failed dispatch, and exactly one more", got)
			}
		})

		t.Run(mode+"/second dispatch fails returns a's 429", func(t *testing.T) {
			var blocked atomic.Int32
			dispatcher := &redispatchTestDispatcher{
				authIDs: []string{redispatchTestAccountA, redispatchTestAccountB},
				fail: func(call int) error {
					if call >= 2 {
						return errDispatch
					}
					return nil
				},
			}
			executor := newRedispatchTestRateLimitedExecutor(false)
			manager, _ := newRedispatchTestManager(dispatcher, executor, redispatchTestReleaseSink("", nil, &blocked))

			_, errRun := redispatchTestRun(manager, stream)
			if errRun == nil {
				t.Fatal("call error = nil, want a's 429")
			}
			if redispatchTestIsHomeUnavailable(errRun) {
				t.Fatalf("call error = %v, want a's 429 instead of home_unavailable", errRun)
			}
			if status := statusCodeFromError(errRun); status != http.StatusTooManyRequests {
				t.Fatalf("status = %d (%T %v), want 429", status, errRun, errRun)
			}
			// The stream path turns a's retry time into a local deadline, so it reports what is left of it.
			if retryAfter := retryAfterFromError(errRun); retryAfter == nil || *retryAfter > redispatchTestRetryTime || *retryAfter <= redispatchTestRetryTime-time.Second {
				t.Fatalf("retry after = %v, want what is left of a's %v", retryAfter, redispatchTestRetryTime)
			}
			if got := dispatcher.Calls(); got != 3 {
				t.Fatalf("dispatch calls = %d, want exactly one extra dispatch", got)
			}
			if calls := executor.Calls(); !reflect.DeepEqual(calls, []string{redispatchTestAccountA}) {
				t.Fatalf("executor calls = %v, want only a", calls)
			}
		})

		t.Run(mode+"/non-429 last error keeps home_unavailable", func(t *testing.T) {
			var blocked atomic.Int32
			dispatcher := &redispatchTestDispatcher{
				authIDs: []string{redispatchTestAccountA, redispatchTestAccountB},
				fail: func(call int) error {
					if call >= 2 {
						return errDispatch
					}
					return nil
				},
			}
			executor := &retryContractHomeExecutor{failures: map[string]error{
				redispatchTestAccountA: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream unavailable"},
			}}
			manager, _ := newRedispatchTestManager(dispatcher, executor, redispatchTestReleaseSink("", nil, &blocked))

			_, errRun := redispatchTestRun(manager, stream)
			if !redispatchTestIsHomeUnavailable(errRun) {
				t.Fatalf("call error = %T %v, want home_unavailable", errRun, errRun)
			}
			if got := dispatcher.Calls(); got != 3 {
				t.Fatalf("dispatch calls = %d, want exactly one extra dispatch", got)
			}
		})
	}
}

// 25 concurrent requests all land on a, which answers 429 to each;
// Home acknowledges a's releases only long after CPACancelBound. Every request
// is served by b and none fails.
func TestHomeRedispatchBurstServedByB(t *testing.T) {
	const requests = 25
	var blocked atomic.Int32
	lateAck := make(chan struct{})
	dispatcher := &redispatchTestDispatcher{authIDs: []string{redispatchTestAccountA, redispatchTestAccountB}}
	executor := newRedispatchTestRateLimitedExecutor(false)
	manager, registry := newRedispatchTestManager(dispatcher, executor, redispatchTestReleaseSink(redispatchTestAccountA, lateAck, &blocked))

	var wg sync.WaitGroup
	failures := make(chan error, requests)
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payloads, errRun := redispatchTestRun(manager, true)
			if errRun != nil {
				failures <- errRun
				return
			}
			if !reflect.DeepEqual(payloads, []string{redispatchTestAccountB}) {
				failures <- fmt.Errorf("payloads = %q, want only account b", payloads)
			}
		}()
	}
	wg.Wait()
	close(lateAck)
	close(failures)
	for errRun := range failures {
		t.Errorf("request failed: %T %v (home_unavailable=%v)", errRun, errRun, redispatchTestIsHomeUnavailable(errRun))
	}
	if got := blocked.Load(); got != requests {
		t.Fatalf("unacknowledged releases of a = %d, want %d", got, requests)
	}
	calls := executor.Calls()
	counts := map[string]int{}
	for _, authID := range calls {
		counts[authID]++
	}
	if counts[redispatchTestAccountA] != requests || counts[redispatchTestAccountB] != requests || len(calls) != 2*requests {
		t.Fatalf("executor calls = %v, want %d on a and %d on b", counts, requests, requests)
	}
	if got := dispatcher.Calls(); got != 2*requests {
		t.Fatalf("dispatch calls = %d, want %d", got, 2*requests)
	}
	if active := registry.FreezeInFlight(time.Time{}).Executions; len(active) != 0 {
		t.Fatalf("execution scopes leaked: %v", active)
	}
}

// A websocket session keeps a's selection after a's 429 ended it, and Home
// never acknowledges a's release. The next pick excludes a, so finding a's old
// selection must not wait on that release a second time and answer 503.
func TestHomeRedispatchWebsocketSessionDoesNotWaitTwiceForAnExcludedRelease(t *testing.T) {
	var blocked atomic.Int32
	dispatcher := &redispatchTestDispatcher{authIDs: []string{redispatchTestAccountA, redispatchTestAccountB}}
	manager, _ := newRedispatchTestManager(dispatcher, newRedispatchTestRateLimitedExecutor(false), redispatchTestReleaseSink(redispatchTestAccountA, nil, &blocked))
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Stream: true, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "redispatch-ws-session"}}

	selectionA, errPick := manager.pickHomeDispatchSelection(ctx, "gpt", opts)
	if errPick != nil || selectionA == nil || selectionA.CloneAuth().ID != redispatchTestAccountA {
		t.Fatalf("first pick = %v, %v; want account a", selectionA, errPick)
	}
	routeModel, ok := validCanonicalHomeConcurrencyModelKey("gpt")
	if !ok {
		t.Fatal("no route model key")
	}
	manager.mu.Lock()
	manager.homeSessionSelections = map[string]map[homeSessionSelectionKey]*HomeDispatchSelection{
		"redispatch-ws-session": {{credentialID: redispatchTestAccountA, routeModel: routeModel}: selectionA},
	}
	manager.mu.Unlock()

	excluded := map[string]struct{}{redispatchTestAccountA: {}}
	// The failure path after a's 429: the release is not acknowledged within the bound.
	if errEnd := manager.endHomeSelectionBeforeExcludedRedispatch(ctx, selectionA, "stream_start_failed", excluded); errEnd != nil {
		t.Fatalf("failure path end = %v, want the excluded release tolerated", errEnd)
	}
	selectionB, errRedispatch := manager.pickHomeDispatchSelection(ctx, "gpt", withHomeExcludedAuthIDs(opts, excluded))
	if errRedispatch != nil {
		t.Fatalf("redispatch = %T %v (home_unavailable=%v), want b", errRedispatch, errRedispatch, redispatchTestIsHomeUnavailable(errRedispatch))
	}
	if selectionB == nil || selectionB.CloneAuth().ID != redispatchTestAccountB {
		t.Fatalf("redispatch picked %v, want b", selectionB)
	}
	selectionB.End("test_done")
	if got := blocked.Load(); got != 1 {
		t.Fatalf("unacknowledged releases of a = %d, want the one release, not a second", got)
	}
}

// Only a second home_unavailable turns into the account's own 429. Any other
// failure of the extra pick, such as Home saying no account is free yet, keeps
// the existing handling and its retry rounds.
func TestHomeRedispatchFailureErrorOnlyForHomeUnavailable(t *testing.T) {
	limited := retryContractRateLimitError{retryAfter: redispatchTestRetryTime}
	var timing homeRetryRoundTiming
	timing.Observe(limited)
	unavailable := &Error{Code: "home_unavailable", Message: "Home dispatch failed", Retryable: true, HTTPStatus: http.StatusServiceUnavailable}
	if errAccount := homeRedispatchFailureError(context.Background(), unavailable, limited, nil, &timing); statusCodeFromError(errAccount) != http.StatusTooManyRequests {
		t.Fatalf("after a second home_unavailable = %v, want a's 429", errAccount)
	}
	for _, errPick := range []error{
		&Error{Code: "auth_unavailable", Message: "no credential is free", Retryable: true, HTTPStatus: http.StatusServiceUnavailable},
		&Error{Code: "auth_not_found", Message: "no auth available"},
		errors.New("plain failure"),
	} {
		if errAccount := homeRedispatchFailureError(context.Background(), errPick, limited, nil, &timing); errAccount != nil {
			t.Fatalf("after %v = %v, want the existing handling", errPick, errAccount)
		}
	}
	if errAccount := homeRedispatchFailureError(context.Background(), unavailable, errors.New("upstream unavailable"), nil, &timing); errAccount != nil {
		t.Fatalf("after a non-429 = %v, want the 503", errAccount)
	}
}
