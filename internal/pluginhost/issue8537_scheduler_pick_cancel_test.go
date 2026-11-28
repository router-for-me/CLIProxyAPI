package pluginhost

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type blockingSchedulerRPCClient struct {
	mu               sync.Mutex
	host             *Host
	pluginID         string
	instance         *hostCallbackInstance
	schemaVersion    uint32
	advertisesCancel bool

	pickCount   int
	cancelCh    chan pluginapi.SchedulerPickCancelRequest
	cancelCount int

	onPick func(ctx context.Context, raw []byte) ([]byte, error)
}

func (c *blockingSchedulerRPCClient) callbackInstance() *hostCallbackInstance {
	return c.instance
}

func (c *blockingSchedulerRPCClient) Shutdown() {}

func (c *blockingSchedulerRPCClient) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister:
		reg := map[string]any{
			"schema_version": c.schemaVersion,
			"metadata": map[string]any{
				"id":      c.pluginID,
				"name":    c.pluginID,
				"version": "1.0.0",
			},
			"capabilities": map[string]any{
				"scheduler":             true,
				"scheduler_pick_cancel": c.advertisesCancel,
			},
		}
		return marshalRPCResult(reg)
	case pluginabi.MethodSchedulerPick:
		c.mu.Lock()
		c.pickCount++
		c.mu.Unlock()
		if c.onPick != nil {
			return c.onPick(ctx, request)
		}
		return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-1"})
	case pluginabi.MethodSchedulerPickCancel:
		var payload pluginapi.SchedulerPickCancelRequest
		_ = json.Unmarshal(request, &payload)
		c.mu.Lock()
		c.cancelCount++
		c.mu.Unlock()
		if c.cancelCh != nil {
			select {
			case c.cancelCh <- payload:
			default:
			}
		}
		return marshalRPCResult(rpcEmptyResponse{})
	default:
		return marshalRPCError("unknown_method", "unknown method "+method), nil
	}
}

func registerBlockingSchedulerHost(t *testing.T, client *blockingSchedulerRPCClient) (*Host, *guardedPluginClient) {
	t.Helper()
	h := New()
	if client.instance == nil {
		client.instance = &hostCallbackInstance{}
	}
	client.host = h
	h.registerHostCallbackInstance(client.pluginID, client.instance)
	guarded := newGuardedPluginClient(client)
	plugin, errReg := registerRPCPlugin(context.Background(), h, client.pluginID, guarded, pluginabi.MethodPluginRegister, nil)
	if errReg != nil {
		t.Fatalf("registerRPCPlugin() error = %v", errReg)
	}
	setHostSnapshotForTest(h, true, capabilityRecord{
		id:       client.pluginID,
		priority: 100,
		meta:     plugin.Metadata,
		plugin:   plugin,
	})
	return h, guarded
}

func TestSchedulerPickReceivesHostCallbackIDAndContextDoneWait(t *testing.T) {
	pickEntered := make(chan string, 1)
	waitResultCh := make(chan pluginapi.HostContextWaitResponse, 1)
	waitErrCh := make(chan error, 1)
	doneBeforeCh := make(chan pluginapi.HostContextDoneResponse, 1)
	doneAfterCh := make(chan pluginapi.HostContextDoneResponse, 1)
	pickReturned := make(chan struct{})
	allowExit := make(chan struct{})

	client := &blockingSchedulerRPCClient{
		pluginID:      "hold-scheduler",
		instance:      &hostCallbackInstance{},
		schemaVersion: 10,
	}
	client.onPick = func(_ context.Context, raw []byte) ([]byte, error) {
		defer close(pickReturned)
		var decoded rpcSchedulerPickRequest
		if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		callbackID := strings.TrimSpace(decoded.HostCallbackID)
		pickEntered <- callbackID
		if callbackID == "" {
			<-allowExit
			return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-1"})
		}

		cbCtx := withHostCallbackIdentity(context.Background(), client.pluginID, client.instance)
		doneReq, _ := json.Marshal(pluginapi.HostContextDoneRequest{HostCallbackID: callbackID})
		rawDoneBefore, errDoneBefore := client.host.callFromPlugin(cbCtx, pluginabi.MethodHostContextDone, doneReq)
		if errDoneBefore != nil {
			waitErrCh <- errDoneBefore
			return nil, errDoneBefore
		}
		respBefore, _ := decodeRPCEnvelope[pluginapi.HostContextDoneResponse](rawDoneBefore)
		doneBeforeCh <- respBefore

		waitReq, _ := json.Marshal(pluginapi.HostContextWaitRequest{HostCallbackID: callbackID})
		rawWait, errWait := client.host.callFromPlugin(cbCtx, pluginabi.MethodHostContextWait, waitReq)
		if errWait != nil {
			waitErrCh <- errWait
			return nil, errWait
		}
		respWait, _ := decodeRPCEnvelope[pluginapi.HostContextWaitResponse](rawWait)
		waitResultCh <- respWait

		<-allowExit
		rawDoneAfter, errDoneAfter := client.host.callFromPlugin(cbCtx, pluginabi.MethodHostContextDone, doneReq)
		if errDoneAfter != nil {
			waitErrCh <- errDoneAfter
			return nil, errDoneAfter
		}
		respAfter, _ := decodeRPCEnvelope[pluginapi.HostContextDoneResponse](rawDoneAfter)
		doneAfterCh <- respAfter
		return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "canceled"})
	}

	h, guarded := registerBlockingSchedulerHost(t, client)

	ctx, cancel := context.WithCancel(context.Background())
	req := schedulerRequest("auth-1")
	req.Options.Metadata = map[string]any{pluginapi.SelectionIDMetadataKey: "sel-wait-1"}

	pickErrCh := make(chan error, 1)
	go func() {
		_, _, errPick := h.PickAuth(ctx, req)
		pickErrCh <- errPick
	}()

	var callbackID string
	select {
	case callbackID = <-pickEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scheduler.pick entry")
	}
	if callbackID == "" {
		close(allowExit)
		cancel()
		t.Fatal("scheduler.pick did not receive host_callback_id")
	}

	select {
	case errCb := <-waitErrCh:
		close(allowExit)
		cancel()
		t.Fatalf("host callback error: %v", errCb)
	case doneBefore := <-doneBeforeCh:
		if doneBefore.Done {
			close(allowExit)
			cancel()
			t.Fatalf("host.context.done before cancel = %#v, want done=false", doneBefore)
		}
	case <-time.After(2 * time.Second):
		close(allowExit)
		cancel()
		t.Fatal("timed out waiting for host.context.done before cancel")
	}

	cancel()

	select {
	case errPick := <-pickErrCh:
		if !errors.Is(errPick, context.Canceled) {
			close(allowExit)
			t.Fatalf("PickAuth() error = %v, want context.Canceled", errPick)
		}
	case <-time.After(2 * time.Second):
		close(allowExit)
		t.Fatal("timed out waiting for PickAuth to return after cancel")
	}

	select {
	case errCb := <-waitErrCh:
		close(allowExit)
		t.Fatalf("host.context.wait error: %v", errCb)
	case waitResp := <-waitResultCh:
		if !waitResp.Done || waitResp.Reason != "canceled" || waitResp.Error == "" {
			close(allowExit)
			t.Fatalf("host.context.wait = %#v, want done=true reason=canceled", waitResp)
		}
	case <-time.After(2 * time.Second):
		close(allowExit)
		t.Fatal("timed out waiting for host.context.wait to unblock on cancel")
	}

	// Verify host.context.done still observes canceled state AFTER PickAuth returned,
	// before the C-ABI call exits.
	close(allowExit)
	select {
	case errCb := <-waitErrCh:
		t.Fatalf("host.context.done after cancel error: %v", errCb)
	case doneAfter := <-doneAfterCh:
		if !doneAfter.Done || doneAfter.Reason != "canceled" || doneAfter.Error == "" {
			t.Fatalf("host.context.done after cancel = %#v, want done=true reason=canceled", doneAfter)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for host.context.done after cancel")
	}

	<-pickReturned
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	guarded.ShutdownContext(shutdownCtx)
	if shutdownCtx.Err() != nil {
		t.Fatalf("guarded.ShutdownContext timed out: %v", shutdownCtx.Err())
	}
}

func TestSchedulerPickCancelPushNotification(t *testing.T) {
	pickEntered := make(chan struct{})
	pickUnblocked := make(chan struct{})
	cancelCh := make(chan pluginapi.SchedulerPickCancelRequest, 1)

	client := &blockingSchedulerRPCClient{
		pluginID:         "push-cancel-scheduler",
		instance:         &hostCallbackInstance{},
		schemaVersion:    pluginabi.SchemaVersionSchedulerPickCancel,
		advertisesCancel: true,
		cancelCh:         cancelCh,
	}
	client.onPick = func(_ context.Context, _ []byte) ([]byte, error) {
		close(pickEntered)
		<-pickUnblocked
		return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "canceled"})
	}

	h, guarded := registerBlockingSchedulerHost(t, client)

	ctx, cancel := context.WithCancel(context.Background())
	req := schedulerRequest("auth-1")
	req.Options.Metadata = map[string]any{pluginapi.SelectionIDMetadataKey: "sel-push-123"}

	pickErrCh := make(chan error, 1)
	go func() {
		_, _, errPick := h.PickAuth(ctx, req)
		pickErrCh <- errPick
	}()

	select {
	case <-pickEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scheduler.pick entry")
	}

	cancel()

	var cancelPayload pluginapi.SchedulerPickCancelRequest
	select {
	case cancelPayload = <-cancelCh:
		close(pickUnblocked)
	case <-time.After(2 * time.Second):
		close(pickUnblocked)
		t.Fatal("scheduler.pick.cancel was not invoked when ctx was canceled")
	}
	if cancelPayload.SelectionID != "sel-push-123" {
		t.Fatalf("scheduler.pick.cancel selection_id = %q, want sel-push-123", cancelPayload.SelectionID)
	}
	if cancelPayload.Reason != "canceled" {
		t.Fatalf("scheduler.pick.cancel reason = %q, want canceled", cancelPayload.Reason)
	}

	select {
	case errPick := <-pickErrCh:
		if !errors.Is(errPick, context.Canceled) {
			t.Fatalf("PickAuth() error = %v, want context.Canceled", errPick)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for PickAuth return")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	guarded.ShutdownContext(shutdownCtx)
	if shutdownCtx.Err() != nil {
		t.Fatalf("guarded.ShutdownContext timed out: %v", shutdownCtx.Err())
	}
}

func TestSchedulerPickCancelPushNotificationEdgeCases(t *testing.T) {
	t.Run("legacy plugin without capability never receives scheduler.pick.cancel", func(t *testing.T) {
		for _, tc := range []struct {
			name             string
			schemaVersion    uint32
			advertisesCancel bool
		}{
			{name: "schema 10 with flag", schemaVersion: 10, advertisesCancel: true},
			{name: "schema 11 without flag", schemaVersion: pluginabi.SchemaVersionSchedulerPickCancel, advertisesCancel: false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				pickEntered := make(chan struct{})
				allowExit := make(chan struct{})
				client := &blockingSchedulerRPCClient{
					pluginID:         "legacy-scheduler",
					instance:         &hostCallbackInstance{},
					schemaVersion:    tc.schemaVersion,
					advertisesCancel: tc.advertisesCancel,
				}
				client.onPick = func(_ context.Context, _ []byte) ([]byte, error) {
					close(pickEntered)
					<-allowExit
					return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-1"})
				}
				h, guarded := registerBlockingSchedulerHost(t, client)
				ctx, cancel := context.WithCancel(context.Background())
				pickErrCh := make(chan error, 1)
				go func() {
					_, _, errPick := h.PickAuth(ctx, schedulerRequest("auth-1"))
					pickErrCh <- errPick
				}()
				<-pickEntered
				cancel()
				if errPick := <-pickErrCh; !errors.Is(errPick, context.Canceled) {
					close(allowExit)
					t.Fatalf("PickAuth() error = %v, want context.Canceled", errPick)
				}
				close(allowExit)
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer shutdownCancel()
				guarded.ShutdownContext(shutdownCtx)
				client.mu.Lock()
				gotCancels := client.cancelCount
				client.mu.Unlock()
				if gotCancels != 0 {
					t.Fatalf("cancelCount = %d, want 0", gotCancels)
				}
			})
		}
	})

	t.Run("completed pick does not trigger cancel and pre-canceled ctx skips pick", func(t *testing.T) {
		client := &blockingSchedulerRPCClient{
			pluginID:         "fast-scheduler",
			instance:         &hostCallbackInstance{},
			schemaVersion:    pluginabi.SchemaVersionSchedulerPickCancel,
			advertisesCancel: true,
		}
		h, guarded := registerBlockingSchedulerHost(t, client)
		defer guarded.Shutdown()

		ctx, cancel := context.WithCancel(context.Background())
		resp, handled, errPick := h.PickAuth(ctx, schedulerRequest("auth-1"))
		if errPick != nil || !handled || resp.AuthID != "auth-1" {
			cancel()
			t.Fatalf("PickAuth() = (%#v, %v, %v), want auth-1", resp, handled, errPick)
		}
		cancel()

		// Calling PickAuth with an already-canceled context must not invoke scheduler.pick or cancel.
		_, _, errPreCanceled := h.PickAuth(ctx, schedulerRequest("auth-1"))
		if !errors.Is(errPreCanceled, context.Canceled) {
			t.Fatalf("pre-canceled PickAuth() error = %v, want context.Canceled", errPreCanceled)
		}
		client.mu.Lock()
		gotPicks := client.pickCount
		gotCancels := client.cancelCount
		client.mu.Unlock()
		if gotPicks != 1 {
			t.Fatalf("pickCount = %d, want 1", gotPicks)
		}
		if gotCancels != 0 {
			t.Fatalf("cancelCount = %d, want 0", gotCancels)
		}
	})

	t.Run("missing selection_id is generated and matched on deadline_exceeded", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			pickSelCh := make(chan string, 1)
			pickUnblocked := make(chan struct{})
			cancelCh := make(chan pluginapi.SchedulerPickCancelRequest, 1)

			client := &blockingSchedulerRPCClient{
				pluginID:         "gen-sel-scheduler",
				instance:         &hostCallbackInstance{},
				schemaVersion:    pluginabi.SchemaVersionSchedulerPickCancel,
				advertisesCancel: true,
				cancelCh:         cancelCh,
			}
			client.onPick = func(_ context.Context, raw []byte) ([]byte, error) {
				var decoded rpcSchedulerPickRequest
				_ = json.Unmarshal(raw, &decoded)
				selID, _ := decoded.Options.Metadata[pluginapi.SelectionIDMetadataKey].(string)
				pickSelCh <- selID
				<-pickUnblocked
				return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-1"})
			}

			h, guarded := registerBlockingSchedulerHost(t, client)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			pickErrCh := make(chan error, 1)
			go func() {
				_, _, errPick := h.PickAuth(ctx, schedulerRequest("auth-1"))
				pickErrCh <- errPick
			}()

			gotSelID := <-pickSelCh
			if strings.TrimSpace(gotSelID) == "" {
				close(pickUnblocked)
				t.Fatal("expected generated selection_id on scheduler.pick")
			}

			time.Sleep(60 * time.Millisecond)
			synctest.Wait()

			cancelPayload := <-cancelCh
			close(pickUnblocked)
			if cancelPayload.SelectionID != gotSelID {
				t.Fatalf("cancel selection_id = %q, want %q", cancelPayload.SelectionID, gotSelID)
			}
			if cancelPayload.Reason != "deadline_exceeded" {
				t.Fatalf("cancel reason = %q, want deadline_exceeded", cancelPayload.Reason)
			}
			if errPick := <-pickErrCh; !errors.Is(errPick, context.DeadlineExceeded) {
				t.Fatalf("PickAuth() error = %v, want context.DeadlineExceeded", errPick)
			}
			guarded.Shutdown()
		})
	})
}

func TestHostContextCallbacksScopeAndTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := New()
		instA := &hostCallbackInstance{}
		instA2 := &hostCallbackInstance{}
		instB := &hostCallbackInstance{}

		reqCtx, reqCancel := context.WithCancel(context.Background())
		defer reqCancel()

		callbackID, closeCallback := h.openCallbackContextForPluginInstance(reqCtx, "plugin-a", instA)
		defer closeCallback()

		cbCtxA := withHostCallbackIdentity(context.Background(), "plugin-a", instA)
		cbCtxA2 := withHostCallbackIdentity(context.Background(), "plugin-a", instA2)
		cbCtxB := withHostCallbackIdentity(context.Background(), "plugin-b", instB)

		// Cross-plugin and cross-instance lookups must fail.
		idReq, _ := json.Marshal(pluginapi.HostContextDoneRequest{HostCallbackID: callbackID})
		if _, errCross := h.callFromPlugin(cbCtxB, pluginabi.MethodHostContextDone, idReq); errCross == nil {
			t.Fatal("expected error when foreign plugin calls host.context.done")
		}
		if _, errCrossInst := h.callFromPlugin(cbCtxA2, pluginabi.MethodHostContextWait, idReq); errCrossInst == nil {
			t.Fatal("expected error when foreign plugin instance calls host.context.wait")
		}

		// Empty callback ID and negative timeout must fail.
		emptyReq, _ := json.Marshal(pluginapi.HostContextDoneRequest{})
		if _, errEmpty := h.callFromPlugin(cbCtxA, pluginabi.MethodHostContextDone, emptyReq); errEmpty == nil {
			t.Fatal("expected error for empty host_callback_id")
		}
		negReq, _ := json.Marshal(pluginapi.HostContextWaitRequest{HostCallbackID: callbackID, TimeoutMS: -1})
		if _, errNeg := h.callFromPlugin(cbCtxA, pluginabi.MethodHostContextWait, negReq); errNeg == nil {
			t.Fatal("expected error for negative timeout_ms")
		}

		// Bounded timeout wait returns timed_out=true while context is still active.
		timeoutReq, _ := json.Marshal(pluginapi.HostContextWaitRequest{HostCallbackID: callbackID, TimeoutMS: 25})
		rawTimeoutResp, errTimeout := h.callFromPlugin(cbCtxA, pluginabi.MethodHostContextWait, timeoutReq)
		if errTimeout != nil {
			t.Fatalf("host.context.wait timeout error = %v", errTimeout)
		}
		timeoutResp, errDecode := decodeRPCEnvelope[pluginapi.HostContextWaitResponse](rawTimeoutResp)
		if errDecode != nil {
			t.Fatalf("decode host.context.wait response: %v", errDecode)
		}
		if timeoutResp.Done || !timeoutResp.TimedOut {
			t.Fatalf("host.context.wait timeout = %#v, want done=false timed_out=true", timeoutResp)
		}

		// Closing the plugin instance while host.context.wait is blocked unblocks it with done=true reason=closed.
		type waitOutcome struct {
			resp pluginapi.HostContextWaitResponse
			err  error
		}
		waitDone := make(chan waitOutcome, 1)
		go func() {
			rawResp, errWait := h.callFromPlugin(cbCtxA, pluginabi.MethodHostContextWait, idReq)
			if errWait != nil {
				waitDone <- waitOutcome{err: errWait}
				return
			}
			decoded, errDec := decodeRPCEnvelope[pluginapi.HostContextWaitResponse](rawResp)
			waitDone <- waitOutcome{resp: decoded, err: errDec}
		}()

		synctest.Wait()
		h.closeHostHTTPCallbackInstance("plugin-a", instA)
		synctest.Wait()

		outcome := <-waitDone
		if outcome.err != nil {
			t.Fatalf("host.context.wait after instance close error = %v", outcome.err)
		}
		if !outcome.resp.Done || outcome.resp.Reason != "closed" {
			t.Fatalf("host.context.wait after instance close = %#v, want done=true reason=closed", outcome.resp)
		}

		// Closed callback ID on a live instance returns done=true reason=closed.
		dlCtx, dlCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer dlCancel()
		cbID2, closeCb2 := h.openCallbackContextForPluginInstance(dlCtx, "plugin-a", instA2)
		time.Sleep(15 * time.Millisecond)
		synctest.Wait()
		dlReq, _ := json.Marshal(pluginapi.HostContextDoneRequest{HostCallbackID: cbID2})
		rawDL, errDL := h.callFromPlugin(cbCtxA2, pluginabi.MethodHostContextDone, dlReq)
		if errDL != nil {
			t.Fatalf("host.context.done on deadline_exceeded error = %v", errDL)
		}
		dlResp, _ := decodeRPCEnvelope[pluginapi.HostContextDoneResponse](rawDL)
		if !dlResp.Done || dlResp.Reason != "deadline_exceeded" {
			t.Fatalf("host.context.done on deadline_exceeded = %#v, want done=true reason=deadline_exceeded", dlResp)
		}

		closeCb2()
		rawClosed, errClosed := h.callFromPlugin(cbCtxA2, pluginabi.MethodHostContextDone, dlReq)
		if errClosed != nil {
			t.Fatalf("host.context.done on closed callback error = %v", errClosed)
		}
		closedResp, _ := decodeRPCEnvelope[pluginapi.HostContextDoneResponse](rawClosed)
		if !closedResp.Done || closedResp.Reason != "closed" {
			t.Fatalf("host.context.done on closed callback = %#v, want done=true reason=closed", closedResp)
		}
	})
}

func TestCallbackContextClosedInstanceRejectedAndShutdownUnblocksWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := New()
		closedInst := &hostCallbackInstance{}
		h.callbackContexts.closeInstance("plugin-closed", closedInst)
		if !closedInst.closed.Load() {
			t.Fatal("closeInstance() did not mark instance closed")
		}

		id, closeFn := h.openCallbackContextForPluginInstance(context.Background(), "plugin-closed", closedInst)
		defer closeFn()
		if id != "" {
			t.Fatalf("openCallbackContextForPluginInstance on closed instance = %q, want empty", id)
		}
		h.callbackContexts.mu.RLock()
		remainingAfterClosedOpen := len(h.callbackContexts.contexts)
		h.callbackContexts.mu.RUnlock()
		if remainingAfterClosedOpen != 0 {
			t.Fatalf("callbackContexts len = %d after closed-instance open, want 0", remainingAfterClosedOpen)
		}

		liveInst := &hostCallbackInstance{}
		cbID, closeLive := h.openCallbackContextForPluginInstance(context.Background(), "plugin-live", liveInst)
		defer closeLive()
		if cbID == "" {
			t.Fatal("expected non-empty callback ID for live instance")
		}

		type waitOutcome struct {
			resp pluginapi.HostContextWaitResponse
			err  error
		}
		waitDone := make(chan waitOutcome, 1)
		cbCtx := withHostCallbackIdentity(context.Background(), "plugin-live", liveInst)
		waitReq, _ := json.Marshal(pluginapi.HostContextWaitRequest{HostCallbackID: cbID})
		go func() {
			rawResp, errWait := h.callFromPlugin(cbCtx, pluginabi.MethodHostContextWait, waitReq)
			if errWait != nil {
				waitDone <- waitOutcome{err: errWait}
				return
			}
			decoded, errDec := decodeRPCEnvelope[pluginapi.HostContextWaitResponse](rawResp)
			waitDone <- waitOutcome{resp: decoded, err: errDec}
		}()

		synctest.Wait()
		h.ShutdownAllContext(context.Background())
		synctest.Wait()

		outcome := <-waitDone
		if outcome.err != nil {
			t.Fatalf("host.context.wait after ShutdownAllContext error = %v", outcome.err)
		}
		if !outcome.resp.Done || outcome.resp.Reason != "closed" {
			t.Fatalf("host.context.wait after ShutdownAllContext = %#v, want done=true reason=closed", outcome.resp)
		}
		if !liveInst.closed.Load() {
			t.Fatal("ShutdownAllContext did not mark active callback instance closed")
		}
		h.callbackContexts.mu.RLock()
		remainingAfterShutdown := len(h.callbackContexts.contexts)
		h.callbackContexts.mu.RUnlock()
		if remainingAfterShutdown != 0 {
			t.Fatalf("callbackContexts len = %d after ShutdownAllContext, want 0", remainingAfterShutdown)
		}
	})
}

func TestRPCCapabilitiesIncludeSchedulerPickCancel(t *testing.T) {
	plugin := pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{}, nil
			}),
			SchedulerPickCancel: true,
		},
	}
	caps := rpcCapabilitiesFromPlugin(plugin)
	if !caps.Scheduler || !caps.SchedulerPickCancel {
		t.Fatalf("rpcCapabilitiesFromPlugin() = %#v, want Scheduler=true SchedulerPickCancel=true", caps)
	}
	raw, errMarshal := json.Marshal(caps)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if decoded["scheduler_pick_cancel"] != true {
		t.Fatalf("scheduler_pick_cancel = %#v, want true", decoded["scheduler_pick_cancel"])
	}
}
