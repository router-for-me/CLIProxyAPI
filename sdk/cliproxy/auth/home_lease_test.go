package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxyusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type leaseExecutionIdentity struct {
	requestID string
	leaseID   string
	authIndex string
	model     string
	websocket bool
}

type leaseIdentityExecutor struct {
	schedulerTestExecutor
	captured chan leaseExecutionIdentity
}

func (e *leaseIdentityExecutor) capture(ctx context.Context, auth *Auth, req cliproxyexecutor.Request) {
	if e == nil || e.captured == nil {
		return
	}
	identity := leaseExecutionIdentity{
		requestID: logging.GetRequestID(ctx),
		leaseID:   cliproxyusage.HomeLeaseIDFromContext(ctx),
		model:     req.Model,
		websocket: cliproxyexecutor.DownstreamWebsocket(ctx),
	}
	if auth != nil {
		identity.authIndex = auth.Index
	}
	e.captured <- identity
}

func (e *leaseIdentityExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.capture(ctx, auth, req)
	return cliproxyexecutor.Response{}, nil
}

func (e *leaseIdentityExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.capture(ctx, auth, req)
	return cliproxyexecutor.Response{}, nil
}

func (e *leaseIdentityExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.capture(ctx, auth, req)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed"}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

type leaseTestDispatcher struct {
	mu             sync.Mutex
	requestIDs     []string
	dispatchIDs    []string
	renewedIDs     []string
	releasedIDs    []string
	releaseReason  []string
	renewed        chan struct{}
	released       chan struct{}
	releaseErrors  int
	dispatchErrors int
	dispatchError  error
	busy           bool
	busyResponses  int
	busyCode       string
}

func (d *leaseTestDispatcher) HeartbeatOK() bool { return true }

func (d *leaseTestDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return nil, nil
}

func (d *leaseTestDispatcher) RPopAuthWithIdentity(_ context.Context, requestedModel string, requestID string, dispatchID string, _ string, _ http.Header, _ int) ([]byte, error) {
	d.mu.Lock()
	d.requestIDs = append(d.requestIDs, requestID)
	d.dispatchIDs = append(d.dispatchIDs, dispatchID)
	busy := d.busy
	if d.busyResponses > 0 {
		d.busyResponses--
		busy = true
	}
	busyCode := d.busyCode
	dispatchError := d.dispatchError
	if d.dispatchErrors > 0 {
		d.dispatchErrors--
		d.mu.Unlock()
		if dispatchError == nil {
			dispatchError = context.DeadlineExceeded
		}
		return nil, dispatchError
	}
	d.mu.Unlock()
	if busy {
		if busyCode == "" {
			busyCode = "credential_concurrency_exceeded"
		}
		return []byte(`{"error":{"type":"` + busyCode + `","message":"busy","retryable":true,"retry_after_ms":250,"scope":"credential"}}`), nil
	}
	return json.Marshal(homeAuthDispatchResponse{
		Model:           requestedModel,
		Provider:        "test",
		AuthIndex:       "home-auth-1",
		LeaseID:         dispatchID,
		LeaseTTLSeconds: 60,
		Auth: Auth{
			ID:       "home-auth-1",
			Provider: "test",
			Status:   StatusActive,
		},
	})
}

func (d *leaseTestDispatcher) RenewInFlightLease(_ context.Context, leaseID string) (bool, error) {
	d.mu.Lock()
	d.renewedIDs = append(d.renewedIDs, leaseID)
	renewed := d.renewed
	d.mu.Unlock()
	if renewed != nil {
		select {
		case renewed <- struct{}{}:
		default:
		}
	}
	return true, nil
}

func (d *leaseTestDispatcher) ReleaseInFlightLease(_ context.Context, leaseID string, reason string) (bool, error) {
	d.mu.Lock()
	d.releasedIDs = append(d.releasedIDs, leaseID)
	d.releaseReason = append(d.releaseReason, reason)
	if d.released != nil {
		select {
		case d.released <- struct{}{}:
		default:
		}
	}
	if d.releaseErrors > 0 {
		d.releaseErrors--
		d.mu.Unlock()
		return false, context.DeadlineExceeded
	}
	d.mu.Unlock()
	return true, nil
}

func TestHomeDispatchIdentityAndLeaseRelease(t *testing.T) {
	dispatcher := &leaseTestDispatcher{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	captured := make(chan leaseExecutionIdentity, 1)
	manager.RegisterExecutor(&leaseIdentityExecutor{captured: captured})
	ctx := logging.WithRequestID(context.Background(), "request-lease-1")
	if _, errExecute := manager.Execute(ctx, []string{"test"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	identity := <-captured

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.requestIDs) != 1 || dispatcher.requestIDs[0] != "request-lease-1" {
		t.Fatalf("request IDs = %v", dispatcher.requestIDs)
	}
	if len(dispatcher.dispatchIDs) != 1 || dispatcher.dispatchIDs[0] == "" {
		t.Fatalf("dispatch IDs = %v", dispatcher.dispatchIDs)
	}
	if len(dispatcher.releasedIDs) != 1 || dispatcher.releasedIDs[0] != dispatcher.dispatchIDs[0] {
		t.Fatalf("released IDs = %v, dispatch IDs = %v", dispatcher.releasedIDs, dispatcher.dispatchIDs)
	}
	if len(dispatcher.releaseReason) != 1 || dispatcher.releaseReason[0] != "completed" {
		t.Fatalf("release reasons = %v", dispatcher.releaseReason)
	}
	if identity.requestID != "request-lease-1" || identity.leaseID != dispatcher.dispatchIDs[0] || identity.authIndex != "home-auth-1" || identity.model != "model-a" {
		t.Fatalf("execution identity = %+v", identity)
	}
}

func TestHomeDispatchGeneratesRequestID(t *testing.T) {
	dispatcher := &leaseTestDispatcher{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.RegisterExecutor(schedulerTestExecutor{})
	if _, errExecute := manager.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.requestIDs) != 1 || dispatcher.requestIDs[0] == "" {
		t.Fatalf("request IDs = %v, want generated request ID", dispatcher.requestIDs)
	}
}

func TestHomeDispatchTransportRetryReusesDispatchIdentity(t *testing.T) {
	dispatcher := &leaseTestDispatcher{dispatchErrors: 1, dispatchError: context.DeadlineExceeded}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.RegisterExecutor(schedulerTestExecutor{})
	ctx := logging.WithRequestID(context.Background(), "request-transport-retry")
	if _, errExecute := manager.Execute(ctx, []string{"test"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.requestIDs) != 2 || dispatcher.requestIDs[0] != dispatcher.requestIDs[1] || dispatcher.requestIDs[0] != "request-transport-retry" {
		t.Fatalf("request IDs = %v, want one stable request identity", dispatcher.requestIDs)
	}
	if len(dispatcher.dispatchIDs) != 2 || dispatcher.dispatchIDs[0] == "" || dispatcher.dispatchIDs[0] != dispatcher.dispatchIDs[1] {
		t.Fatalf("dispatch IDs = %v, want the same ID for transport recovery", dispatcher.dispatchIDs)
	}
	if len(dispatcher.releasedIDs) != 1 || dispatcher.releasedIDs[0] != dispatcher.dispatchIDs[0] {
		t.Fatalf("released IDs = %v, dispatch IDs = %v", dispatcher.releasedIDs, dispatcher.dispatchIDs)
	}
}

func TestHomeLeaseRenewsUntilRelease(t *testing.T) {
	dispatcher := &leaseTestDispatcher{renewed: make(chan struct{}, 1)}
	lease := newHomeLeaseHandle(dispatcher, "lease-renew", time.Minute)
	if lease == nil {
		t.Fatal("newHomeLeaseHandle() = nil")
	}
	lease.renewEvery = 5 * time.Millisecond
	lease.start()

	select {
	case <-dispatcher.renewed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for lease renewal")
	}
	lease.release("completed")

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.renewedIDs) == 0 || dispatcher.renewedIDs[0] != "lease-renew" {
		t.Fatalf("renewed IDs = %v", dispatcher.renewedIDs)
	}
	if len(dispatcher.releasedIDs) != 1 || dispatcher.releasedIDs[0] != "lease-renew" {
		t.Fatalf("released IDs = %v", dispatcher.releasedIDs)
	}
}

func TestHomeLeaseRetriesFailedRelease(t *testing.T) {
	dispatcher := &leaseTestDispatcher{released: make(chan struct{}, 2), releaseErrors: 1}
	lease := newHomeLeaseHandle(dispatcher, "lease-release-retry", time.Minute)
	if lease == nil {
		t.Fatal("newHomeLeaseHandle() = nil")
	}
	lease.release("completed")

	for attempt := 0; attempt < 2; attempt++ {
		select {
		case <-dispatcher.released:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for release attempt %d", attempt+1)
		}
	}
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.releasedIDs) != 2 {
		t.Fatalf("release attempts = %d, want 2", len(dispatcher.releasedIDs))
	}
}

type leaseInvalidExecutor struct{ leaseIdentityExecutor }

func (e *leaseInvalidExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.capture(ctx, auth, req)
	return cliproxyexecutor.Response{}, &Error{Code: "invalid_request", Message: "invalid_request_error: invalid request", HTTPStatus: http.StatusBadRequest}
}

func TestHomeLeaseDoesNotReplaceAuthRuntime(t *testing.T) {
	dispatcher := &leaseTestDispatcher{}
	runtimeState := &struct{ name string }{name: "provider-runtime"}
	auth := &Auth{Runtime: runtimeState}
	lease := newHomeLeaseHandle(dispatcher, "lease-runtime", time.Minute)
	setHomeLease(auth, lease)
	t.Cleanup(func() { releaseHomeLease(auth, "test_cleanup") })

	if auth.Runtime != runtimeState {
		t.Fatalf("auth runtime = %#v, want original provider runtime", auth.Runtime)
	}
	if got := homeLeaseFromAuth(auth); got != lease {
		t.Fatalf("home lease = %#v, want %#v", got, lease)
	}
	if clone := auth.Clone(); clone.Runtime != runtimeState || homeLeaseFromAuth(clone) != lease {
		t.Fatalf("cloned auth lost runtime or lease: runtime=%#v lease=%#v", clone.Runtime, homeLeaseFromAuth(clone))
	}
}

func TestHomeCountReleasesLeaseAfterCompletion(t *testing.T) {
	dispatcher := &leaseTestDispatcher{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	captured := make(chan leaseExecutionIdentity, 1)
	manager.RegisterExecutor(&leaseIdentityExecutor{captured: captured})
	if _, errExecute := manager.ExecuteCount(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("ExecuteCount() error = %v", errExecute)
	}
	identity := <-captured

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.releasedIDs) != 1 || len(dispatcher.releaseReason) != 1 || dispatcher.releaseReason[0] != "completed" {
		t.Fatalf("released IDs = %v, reasons = %v", dispatcher.releasedIDs, dispatcher.releaseReason)
	}
	if identity.requestID == "" || identity.leaseID != dispatcher.releasedIDs[0] || identity.authIndex != "home-auth-1" || identity.model != "model-a" {
		t.Fatalf("count identity = %+v", identity)
	}
}

type leaseCountEndpointMissingExecutor struct{ leaseIdentityExecutor }

func (e *leaseCountEndpointMissingExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.capture(ctx, auth, req)
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotFound, Message: "404 page not found"}
}

func TestHomeCountEndpointErrorReleasesEveryReservedLease(t *testing.T) {
	dispatcher := &leaseTestDispatcher{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.RegisterExecutor(&leaseCountEndpointMissingExecutor{})
	_, errExecute := manager.ExecuteCount(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
	if errExecute == nil || statusCodeFromError(errExecute) != http.StatusNotFound {
		t.Fatalf("ExecuteCount() error = %v, want endpoint 404", errExecute)
	}

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.dispatchIDs) != 2 || len(dispatcher.releasedIDs) != 2 {
		t.Fatalf("dispatches=%v releases=%v, want every reservation released", dispatcher.dispatchIDs, dispatcher.releasedIDs)
	}
	for index, dispatchID := range dispatcher.dispatchIDs {
		if dispatcher.releasedIDs[index] != dispatchID {
			t.Fatalf("release[%d]=%q, want dispatch %q", index, dispatcher.releasedIDs[index], dispatchID)
		}
	}
}

func TestHomeInvalidRequestReleasesLease(t *testing.T) {
	dispatcher := &leaseTestDispatcher{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	captured := make(chan leaseExecutionIdentity, 1)
	manager.RegisterExecutor(&leaseInvalidExecutor{leaseIdentityExecutor: leaseIdentityExecutor{captured: captured}})
	_, errExecute := manager.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
	if errExecute == nil || statusCodeFromError(errExecute) != http.StatusBadRequest {
		t.Fatalf("Execute() error = %v, want 400", errExecute)
	}
	identity := <-captured

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.releasedIDs) != 1 || len(dispatcher.releaseReason) != 1 || dispatcher.releaseReason[0] != "request_invalid" {
		t.Fatalf("released IDs = %v, reasons = %v", dispatcher.releasedIDs, dispatcher.releaseReason)
	}
	if identity.requestID == "" || identity.leaseID != dispatcher.releasedIDs[0] || identity.authIndex != "home-auth-1" || identity.model != "model-a" {
		t.Fatalf("failure identity = %+v", identity)
	}
}

type leaseCanceledStreamExecutor struct{ leaseIdentityExecutor }

func (e *leaseCanceledStreamExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.capture(ctx, auth, req)
	chunks := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(chunks)
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.output_text.delta","delta":"hello"}`)}
		<-ctx.Done()
	}()
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func TestHomeWebsocketStreamCarriesIdentityAndReleasesLease(t *testing.T) {
	dispatcher := &leaseTestDispatcher{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	captured := make(chan leaseExecutionIdentity, 1)
	manager.RegisterExecutor(&leaseIdentityExecutor{captured: captured})
	ctx := logging.WithRequestID(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), "request-websocket")
	result, errExecute := manager.ExecuteStream(ctx, []string{"test"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
	if errExecute != nil || result == nil {
		t.Fatalf("ExecuteStream() = %#v, %v", result, errExecute)
	}
	for range result.Chunks {
	}
	identity := <-captured

	deadline := time.Now().Add(time.Second)
	for {
		dispatcher.mu.Lock()
		released := len(dispatcher.releasedIDs)
		reason := ""
		if len(dispatcher.releaseReason) > 0 {
			reason = dispatcher.releaseReason[0]
		}
		dispatcher.mu.Unlock()
		if released == 1 {
			if reason != "stream_terminal" {
				t.Fatalf("release reason = %q, want stream_terminal", reason)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for stream lease release")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if identity.requestID != "request-websocket" || identity.leaseID == "" || identity.authIndex != "home-auth-1" || identity.model != "model-a" || !identity.websocket {
		t.Fatalf("websocket identity = %+v", identity)
	}
}

func TestHomeStreamCancellationReleasesLease(t *testing.T) {
	dispatcher := &leaseTestDispatcher{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	captured := make(chan leaseExecutionIdentity, 1)
	manager.RegisterExecutor(&leaseCanceledStreamExecutor{leaseIdentityExecutor: leaseIdentityExecutor{captured: captured}})
	ctx, cancel := context.WithCancel(logging.WithRequestID(context.Background(), "request-stream-cancel"))
	result, errExecute := manager.ExecuteStream(ctx, []string{"test"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
	if errExecute != nil || result == nil {
		t.Fatalf("ExecuteStream() = %#v, %v", result, errExecute)
	}
	if _, ok := <-result.Chunks; !ok {
		t.Fatal("stream closed before the first chunk")
	}
	cancel()
	for range result.Chunks {
	}
	identity := <-captured

	deadline := time.Now().Add(time.Second)
	for {
		dispatcher.mu.Lock()
		released := len(dispatcher.releasedIDs)
		reason := ""
		if len(dispatcher.releaseReason) > 0 {
			reason = dispatcher.releaseReason[0]
		}
		dispatcher.mu.Unlock()
		if released == 1 {
			if reason != "request_canceled" {
				t.Fatalf("release reason = %q, want request_canceled", reason)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for canceled stream lease release")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if identity.requestID != "request-stream-cancel" || identity.leaseID == "" || identity.authIndex != "home-auth-1" || identity.model != "model-a" {
		t.Fatalf("canceled stream identity = %+v", identity)
	}
}

func TestHomeConcurrencyErrorMapsToRetryable429(t *testing.T) {
	for _, errorCode := range []string{"credential_concurrency_exceeded", "credential_model_concurrency_exceeded"} {
		t.Run(errorCode, func(t *testing.T) {
			dispatcher := &leaseTestDispatcher{busy: true, busyCode: errorCode}
			oldCurrentHomeDispatcher := currentHomeDispatcher
			currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
			t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			_, _, _, errPick := manager.pickNextViaHome(logging.WithRequestID(context.Background(), "request-busy"), "model-a", cliproxyexecutor.Options{}, nil)
			if errPick == nil || statusCodeFromError(errPick) != http.StatusTooManyRequests {
				t.Fatalf("pickNextViaHome() error = %v, status = %d", errPick, statusCodeFromError(errPick))
			}
			retryAfter := retryAfterFromError(errPick)
			if retryAfter == nil || *retryAfter != 250*time.Millisecond {
				t.Fatalf("retry after = %v, want 250ms", retryAfter)
			}
		})
	}
}

func TestHomeConcurrencyBusyRetriesWithStableRequestIdentity(t *testing.T) {
	dispatcher := &leaseTestDispatcher{busyResponses: 1}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(1, time.Second, 0)
	manager.RegisterExecutor(schedulerTestExecutor{})
	ctx := logging.WithRequestID(context.Background(), "request-busy-retry")
	if _, errExecute := manager.Execute(ctx, []string{"test"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.requestIDs) != 2 || dispatcher.requestIDs[0] != "request-busy-retry" || dispatcher.requestIDs[1] != "request-busy-retry" {
		t.Fatalf("request IDs = %v, want stable request-busy-retry", dispatcher.requestIDs)
	}
	if len(dispatcher.dispatchIDs) != 2 || dispatcher.dispatchIDs[0] == "" || dispatcher.dispatchIDs[1] == "" || dispatcher.dispatchIDs[0] == dispatcher.dispatchIDs[1] {
		t.Fatalf("dispatch IDs = %v, want two unique IDs", dispatcher.dispatchIDs)
	}
	if len(dispatcher.releasedIDs) != 1 || dispatcher.releasedIDs[0] != dispatcher.dispatchIDs[1] {
		t.Fatalf("released IDs = %v, dispatch IDs = %v", dispatcher.releasedIDs, dispatcher.dispatchIDs)
	}
}

type leaseRateLimitedExecutor struct{ schedulerTestExecutor }

func (e *leaseRateLimitedExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &retryAfterStatusError{
		status:     http.StatusTooManyRequests,
		message:    "upstream rate limited",
		retryAfter: 250 * time.Millisecond,
	}
}

func TestHomeUpstreamRateLimitDoesNotGainOuterConcurrencyRetry(t *testing.T) {
	dispatcher := &leaseTestDispatcher{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(1, time.Second, 0)
	manager.RegisterExecutor(&leaseRateLimitedExecutor{})
	_, errExecute := manager.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
	if errExecute == nil || statusCodeFromError(errExecute) != http.StatusTooManyRequests {
		t.Fatalf("Execute() error = %v, want upstream 429", errExecute)
	}

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.dispatchIDs) != 2 {
		t.Fatalf("Home dispatches = %d, want one credential attempt plus repeated-auth stop", len(dispatcher.dispatchIDs))
	}
}
