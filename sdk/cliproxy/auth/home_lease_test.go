package auth

import (
	"context"
	"encoding/json"
	"errors"
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
	mu              sync.Mutex
	requestIDs      []string
	dispatchIDs     []string
	renewedIDs      []string
	releasedIDs     []string
	releaseReason   []string
	renewed         chan struct{}
	released        chan struct{}
	releaseStarted  chan struct{}
	releaseBlock    <-chan struct{}
	releaseErrors   int
	dispatchErrors  int
	dispatchError   error
	leaseTTLSeconds int64
	leaseExpiresAt  time.Time
	busy            bool
	busyResponses   int
	busyCode        string
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
	leaseTTLSeconds := d.leaseTTLSeconds
	leaseExpiresAt := d.leaseExpiresAt
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
	if leaseTTLSeconds <= 0 {
		leaseTTLSeconds = 60
	}
	leaseExpiresAtRaw := ""
	if !leaseExpiresAt.IsZero() {
		leaseExpiresAtRaw = leaseExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return json.Marshal(homeAuthDispatchResponse{
		Model:           requestedModel,
		Provider:        "test",
		AuthIndex:       "home-auth-1",
		LeaseID:         dispatchID,
		LeaseTTLSeconds: leaseTTLSeconds,
		LeaseExpiresAt:  leaseExpiresAtRaw,
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
	released := d.released
	releaseStarted := d.releaseStarted
	releaseBlock := d.releaseBlock
	releaseFails := d.releaseErrors > 0
	if releaseFails {
		d.releaseErrors--
	}
	d.mu.Unlock()
	for _, signal := range []chan struct{}{released, releaseStarted} {
		if signal != nil {
			select {
			case signal <- struct{}{}:
			default:
			}
		}
	}
	if releaseBlock != nil {
		<-releaseBlock
	}
	if releaseFails {
		return false, context.DeadlineExceeded
	}
	return true, nil
}

type selectiveBlockingReleaseDispatcher struct {
	firstStarted chan struct{}
	firstBlock   <-chan struct{}
	secondDone   chan struct{}
}

func (d *selectiveBlockingReleaseDispatcher) RenewInFlightLease(context.Context, string) (bool, error) {
	return true, nil
}

func (d *selectiveBlockingReleaseDispatcher) ReleaseInFlightLease(ctx context.Context, leaseID string, _ string) (bool, error) {
	switch leaseID {
	case "lease-blocked":
		select {
		case d.firstStarted <- struct{}{}:
		default:
		}
		select {
		case <-d.firstBlock:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	case "lease-fast":
		select {
		case d.secondDone <- struct{}{}:
		default:
		}
		return true, nil
	default:
		return true, nil
	}
}

func waitForLeaseReleaseCount(t *testing.T, dispatcher *leaseTestDispatcher, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		dispatcher.mu.Lock()
		got := len(dispatcher.releasedIDs)
		dispatcher.mu.Unlock()
		if got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d lease release(s); got %d", want, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
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
	waitForLeaseReleaseCount(t, dispatcher, 1)

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

func TestHomeSuccessfulExecutionDoesNotWaitForReleaseRPC(t *testing.T) {
	unblockRelease := make(chan struct{})
	defer close(unblockRelease)
	dispatcher := &leaseTestDispatcher{
		releaseStarted: make(chan struct{}, 1),
		releaseBlock:   unblockRelease,
	}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.RegisterExecutor(schedulerTestExecutor{})
	done := make(chan error, 1)
	go func() {
		_, errExecute := manager.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
		done <- errExecute
	}()

	select {
	case errExecute := <-done:
		if errExecute != nil {
			t.Fatalf("Execute() error = %v", errExecute)
		}
	case <-time.After(time.Second):
		t.Fatal("Execute() waited for the Home lease release RPC")
	}
	select {
	case <-dispatcher.releaseStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the asynchronous release RPC")
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
	waitForLeaseReleaseCount(t, dispatcher, 1)

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

func TestHomeDispatchUsesReportedExpiryForFirstRenewal(t *testing.T) {
	dispatcher := &leaseTestDispatcher{
		leaseTTLSeconds: int64((30 * time.Minute) / time.Second),
		leaseExpiresAt:  time.Now().Add(3 * time.Second),
	}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.RegisterExecutor(schedulerTestExecutor{})
	auth, _, _, errPick := manager.pickNextViaHome(logging.WithRequestID(context.Background(), "request-near-expiry"), "model-a", cliproxyexecutor.Options{}, nil)
	if errPick != nil || auth == nil {
		t.Fatalf("pickNextViaHome() = %#v, %v", auth, errPick)
	}
	lease := homeLeaseFromAuth(auth)
	if lease == nil {
		t.Fatal("home lease = nil")
	}
	if lease.firstRenewAfter <= 0 || lease.firstRenewAfter >= 2*time.Second || lease.firstRenewAfter >= lease.renewEvery {
		t.Fatalf("first renewal = %v, cadence = %v, want an earlier near-expiry renewal", lease.firstRenewAfter, lease.renewEvery)
	}
	manager.ReleaseHomeLease(auth, "test_completed")
	waitForLeaseReleaseCount(t, dispatcher, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if errShutdown := manager.ShutdownHomeLeaseReleases(ctx); errShutdown != nil {
		t.Fatalf("release queue shutdown error = %v", errShutdown)
	}
}

func TestHomeLeaseRenewsUntilRelease(t *testing.T) {
	dispatcher := &leaseTestDispatcher{renewed: make(chan struct{}, 1)}
	lease := newHomeLeaseHandle(dispatcher, "lease-renew", time.Minute, time.Time{}, nil)
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
	waitForLeaseReleaseCount(t, dispatcher, 1)

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.renewedIDs) == 0 || dispatcher.renewedIDs[0] != "lease-renew" {
		t.Fatalf("renewed IDs = %v", dispatcher.renewedIDs)
	}
	if len(dispatcher.releasedIDs) != 1 || dispatcher.releasedIDs[0] != "lease-renew" {
		t.Fatalf("released IDs = %v", dispatcher.releasedIDs)
	}
}

func TestHomeLeaseStartsWhenExecutionClaimsIt(t *testing.T) {
	dispatcher := &leaseTestDispatcher{renewed: make(chan struct{}, 1)}
	lease := newHomeLeaseHandle(dispatcher, "lease-claimed", time.Minute, time.Time{}, nil)
	if lease == nil {
		t.Fatal("newHomeLeaseHandle() = nil")
	}
	lease.renewEvery = 5 * time.Millisecond
	auth := &Auth{}
	setHomeLease(auth, lease)

	select {
	case <-dispatcher.renewed:
		t.Fatal("lease renewed before execution claimed it")
	case <-time.After(25 * time.Millisecond):
	}
	ctx := contextWithHomeLease(context.Background(), auth)
	if got := cliproxyusage.HomeLeaseIDFromContext(ctx); got != "lease-claimed" {
		t.Fatalf("HomeLeaseIDFromContext() = %q, want lease-claimed", got)
	}
	select {
	case <-dispatcher.renewed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for claimed lease renewal")
	}
	lease.release("completed")
	waitForLeaseReleaseCount(t, dispatcher, 1)
}

func TestHomeLeaseStartBindsCancellation(t *testing.T) {
	dispatcher := &leaseTestDispatcher{}
	releases := newHomeLeaseReleaseQueue()
	lease := newHomeLeaseHandle(dispatcher, "lease-context-cancel", time.Minute, time.Time{}, releases)
	if lease == nil {
		t.Fatal("newHomeLeaseHandle() = nil")
	}
	auth := &Auth{}
	setHomeLease(auth, lease)
	ctx, cancel := context.WithCancel(context.Background())
	startHomeLease(ctx, auth)
	cancel()
	waitForLeaseReleaseCount(t, dispatcher, 1)

	dispatcher.mu.Lock()
	if len(dispatcher.releaseReason) != 1 || dispatcher.releaseReason[0] != "request_canceled" {
		dispatcher.mu.Unlock()
		t.Fatalf("release reasons = %v, want request_canceled", dispatcher.releaseReason)
	}
	dispatcher.mu.Unlock()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if errShutdown := releases.shutdown(shutdownCtx); errShutdown != nil {
		t.Fatalf("release queue shutdown error = %v", errShutdown)
	}
}

func TestHomeLeaseRenewsBeforeReportedExpiry(t *testing.T) {
	dispatcher := &leaseTestDispatcher{renewed: make(chan struct{}, 1)}
	releases := newHomeLeaseReleaseQueue()
	lease := newHomeLeaseHandle(dispatcher, "lease-near-expiry", 30*time.Minute, time.Now().Add(60*time.Millisecond), releases)
	if lease == nil {
		t.Fatal("newHomeLeaseHandle() = nil")
	}
	lease.start()

	select {
	case <-dispatcher.renewed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for renewal before reported expiry")
	}
	lease.release("completed")
	waitForLeaseReleaseCount(t, dispatcher, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if errShutdown := releases.shutdown(ctx); errShutdown != nil {
		t.Fatalf("release queue shutdown error = %v", errShutdown)
	}
}

func TestHomeLeaseReleaseQueueShutdownDrains(t *testing.T) {
	unblockRelease := make(chan struct{})
	dispatcher := &leaseTestDispatcher{
		releaseStarted: make(chan struct{}, 1),
		releaseBlock:   unblockRelease,
	}
	releases := newHomeLeaseReleaseQueue()
	lease := newHomeLeaseHandle(dispatcher, "lease-shutdown-drain", time.Minute, time.Time{}, releases)
	if lease == nil {
		t.Fatal("newHomeLeaseHandle() = nil")
	}
	lease.release("completed")
	select {
	case <-dispatcher.releaseStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queued release to start")
	}

	shutdownDone := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		shutdownDone <- releases.shutdown(ctx)
	}()
	select {
	case errShutdown := <-shutdownDone:
		t.Fatalf("release queue shutdown returned before draining: %v", errShutdown)
	case <-time.After(25 * time.Millisecond):
	}
	close(unblockRelease)
	select {
	case errShutdown := <-shutdownDone:
		if errShutdown != nil {
			t.Fatalf("release queue shutdown error = %v", errShutdown)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for release queue shutdown")
	}
	if releases.enqueue(homeLeaseReleaseRequest{client: dispatcher, leaseID: "lease-after-shutdown"}) {
		t.Fatal("release queue accepted work after shutdown")
	}
}

func TestHomeLeaseReleaseQueueIsBounded(t *testing.T) {
	unblockRelease := make(chan struct{})
	dispatcher := &leaseTestDispatcher{
		releaseStarted: make(chan struct{}, 1),
		releaseBlock:   unblockRelease,
	}
	releases := newHomeLeaseReleaseQueue()
	releases.limit = 2
	releases.workers = 1
	if !releases.enqueue(homeLeaseReleaseRequest{client: dispatcher, leaseID: "lease-running"}) {
		t.Fatal("release queue rejected the running request")
	}
	if !releases.enqueue(homeLeaseReleaseRequest{client: dispatcher, leaseID: "lease-pending"}) {
		t.Fatal("release queue rejected the immediate pending request")
	}
	if releases.enqueue(homeLeaseReleaseRequest{client: dispatcher, leaseID: "lease-overflow"}) {
		t.Fatal("release queue accepted work beyond its bound")
	}
	select {
	case <-dispatcher.releaseStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the running release")
	}
	close(unblockRelease)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if errShutdown := releases.shutdown(ctx); errShutdown != nil {
		t.Fatalf("release queue shutdown error = %v", errShutdown)
	}
	waitForLeaseReleaseCount(t, dispatcher, 2)
}

func TestHomeLeaseReleaseWorkersAvoidHeadOfLineBlocking(t *testing.T) {
	unblockFirst := make(chan struct{})
	dispatcher := &selectiveBlockingReleaseDispatcher{
		firstStarted: make(chan struct{}, 1),
		firstBlock:   unblockFirst,
		secondDone:   make(chan struct{}, 1),
	}
	releases := newHomeLeaseReleaseQueue()
	releases.workers = 2
	if !releases.enqueue(homeLeaseReleaseRequest{client: dispatcher, leaseID: "lease-blocked"}) {
		t.Fatal("release queue rejected the blocked release")
	}
	select {
	case <-dispatcher.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the blocked release")
	}
	if !releases.enqueue(homeLeaseReleaseRequest{client: dispatcher, leaseID: "lease-fast"}) {
		t.Fatal("release queue rejected the fast release")
	}
	select {
	case <-dispatcher.secondDone:
	case <-time.After(time.Second):
		t.Fatal("fast release was blocked behind the failing release")
	}
	close(unblockFirst)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if errShutdown := releases.shutdown(ctx); errShutdown != nil {
		t.Fatalf("release queue shutdown error = %v", errShutdown)
	}
}

func TestHomeLeaseReleaseShutdownSkipsPendingWorkAfterTimeout(t *testing.T) {
	dispatcher := &selectiveBlockingReleaseDispatcher{
		firstStarted: make(chan struct{}, 1),
		firstBlock:   make(chan struct{}),
		secondDone:   make(chan struct{}, 1),
	}
	releases := newHomeLeaseReleaseQueue()
	releases.workers = 1
	if !releases.enqueue(homeLeaseReleaseRequest{client: dispatcher, leaseID: "lease-blocked"}) {
		t.Fatal("release queue rejected the blocked release")
	}
	select {
	case <-dispatcher.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the blocked release")
	}
	if !releases.enqueue(homeLeaseReleaseRequest{client: dispatcher, leaseID: "lease-fast"}) {
		t.Fatal("release queue rejected the pending release")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if errShutdown := releases.shutdown(ctx); !errors.Is(errShutdown, context.DeadlineExceeded) {
		t.Fatalf("release queue shutdown error = %v, want deadline exceeded", errShutdown)
	}
	select {
	case <-dispatcher.secondDone:
		t.Fatal("pending release ran after shutdown cancellation")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHomeLeaseRetriesFailedRelease(t *testing.T) {
	dispatcher := &leaseTestDispatcher{released: make(chan struct{}, 2), releaseErrors: 1}
	lease := newHomeLeaseHandle(dispatcher, "lease-release-retry", time.Minute, time.Time{}, nil)
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
	lease := newHomeLeaseHandle(dispatcher, "lease-runtime", time.Minute, time.Time{}, nil)
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
	waitForLeaseReleaseCount(t, dispatcher, 1)

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
	waitForLeaseReleaseCount(t, dispatcher, 2)

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
	waitForLeaseReleaseCount(t, dispatcher, 1)

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.releasedIDs) != 1 || len(dispatcher.releaseReason) != 1 || dispatcher.releaseReason[0] != "request_invalid" {
		t.Fatalf("released IDs = %v, reasons = %v", dispatcher.releasedIDs, dispatcher.releaseReason)
	}
	if identity.requestID == "" || identity.leaseID != dispatcher.releasedIDs[0] || identity.authIndex != "home-auth-1" || identity.model != "model-a" {
		t.Fatalf("failure identity = %+v", identity)
	}
}

type leasePreBootstrapStalledExecutor struct{ leaseIdentityExecutor }

func (e *leasePreBootstrapStalledExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.capture(ctx, auth, req)
	return &cliproxyexecutor.StreamResult{Chunks: make(chan cliproxyexecutor.StreamChunk)}, nil
}

type leaseStalledStreamExecutor struct {
	leaseIdentityExecutor
	unblock <-chan struct{}
}

func (e *leaseStalledStreamExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.capture(ctx, auth, req)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.output_text.delta","delta":"hello"}`)}
	go func() {
		<-e.unblock
		close(chunks)
	}()
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
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

func TestHomePreBootstrapCancellationReleasesLease(t *testing.T) {
	dispatcher := &leaseTestDispatcher{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	captured := make(chan leaseExecutionIdentity, 1)
	manager.RegisterExecutor(&leasePreBootstrapStalledExecutor{leaseIdentityExecutor: leaseIdentityExecutor{captured: captured}})
	ctx, cancel := context.WithCancel(logging.WithRequestID(context.Background(), "request-pre-bootstrap-cancel"))
	done := make(chan error, 1)
	go func() {
		_, errExecute := manager.ExecuteStream(ctx, []string{"test"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
		done <- errExecute
	}()
	identity := <-captured
	cancel()
	select {
	case errExecute := <-done:
		if !errors.Is(errExecute, context.Canceled) {
			t.Fatalf("ExecuteStream() error = %v, want context canceled", errExecute)
		}
	case <-time.After(time.Second):
		t.Fatal("pre-bootstrap stream did not return after cancellation")
	}
	waitForLeaseReleaseCount(t, dispatcher, 1)
	if identity.requestID != "request-pre-bootstrap-cancel" || identity.leaseID == "" {
		t.Fatalf("pre-bootstrap stream identity = %+v", identity)
	}

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.releaseReason) != 1 || dispatcher.releaseReason[0] != "request_canceled" {
		t.Fatalf("release reasons = %v, want request_canceled", dispatcher.releaseReason)
	}
}

func TestHomeStalledStreamCancellationReleasesLease(t *testing.T) {
	dispatcher := &leaseTestDispatcher{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	unblock := make(chan struct{})
	defer close(unblock)
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	captured := make(chan leaseExecutionIdentity, 1)
	manager.RegisterExecutor(&leaseStalledStreamExecutor{
		leaseIdentityExecutor: leaseIdentityExecutor{captured: captured},
		unblock:               unblock,
	})
	ctx, cancel := context.WithCancel(logging.WithRequestID(context.Background(), "request-stalled-stream-cancel"))
	result, errExecute := manager.ExecuteStream(ctx, []string{"test"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
	if errExecute != nil || result == nil {
		t.Fatalf("ExecuteStream() = %#v, %v", result, errExecute)
	}
	if _, ok := <-result.Chunks; !ok {
		t.Fatal("stream closed before the first chunk")
	}
	cancel()
	select {
	case _, ok := <-result.Chunks:
		if ok {
			t.Fatal("stream returned an unexpected chunk after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("stalled stream did not close after cancellation")
	}
	waitForLeaseReleaseCount(t, dispatcher, 1)
	identity := <-captured
	if identity.requestID != "request-stalled-stream-cancel" || identity.leaseID == "" {
		t.Fatalf("stalled stream identity = %+v", identity)
	}

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.releaseReason) != 1 || dispatcher.releaseReason[0] != "request_canceled" {
		t.Fatalf("release reasons = %v, want request_canceled", dispatcher.releaseReason)
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
	waitForLeaseReleaseCount(t, dispatcher, 1)

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
