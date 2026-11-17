package maintenance

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const owner = "0123456789abcdef0123456789abcdef"

func begin(t *testing.T, d *Controller) Status {
	t.Helper()
	s, ok := d.Begin(d.Status().InstanceID, owner)
	if !ok {
		t.Fatal("could not acquire drain")
	}
	return s
}

func TestLeaseOwnershipExpirationAndAtomicStop(t *testing.T) {
	d := New()
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	first := begin(t, d)
	if !first.Ready {
		t.Fatal(first)
	}
	if _, ok := d.Begin(first.InstanceID, strings.Repeat("x", 32)); ok {
		t.Fatal("stole lease")
	}
	now = now.Add(time.Minute)
	again := begin(t, d)
	if again.LeaseRemainingMS != 60000 {
		t.Fatal("retry extended lease", again)
	}
	if d.Cancel(first.InstanceID, "wrong") {
		t.Fatal("cancelled another owner")
	}
	now = now.Add(time.Minute)
	if d.CommitStop(first.InstanceID, owner, nil) {
		t.Fatal("committed expired lease")
	}
	if d.Status().Draining {
		t.Fatal("lease did not expire")
	}
	begin(t, d)
	if !d.CommitStop(first.InstanceID, owner, nil) {
		t.Fatal("could not commit safe stop")
	}
	now = now.Add(10 * time.Minute)
	if !d.Status().Stopping || !d.Status().Draining {
		t.Fatal("committed stop resumed admission")
	}
	if _, ok := d.admit(httptest.NewRequest("POST", "/v1/responses", nil)); ok {
		t.Fatal("admitted after committed stop")
	}
}

func TestRequestMustReturnAndFinishFlushing(t *testing.T) {
	for _, flushFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "flush first", false: "handler first"}[flushFirst], func(t *testing.T) {
			d := New()
			conn, other := net.Pipe()
			defer conn.Close()
			defer other.Close()
			req := httptest.NewRequest("POST", "/custom/plugin/route", nil)
			req = req.WithContext(d.ConnContext(context.Background(), conn))
			ticket, ok := d.admit(req)
			if !ok {
				t.Fatal("admission failed")
			}
			begin(t, d)
			if flushFirst {
				d.ConnState(conn, http.StateClosed)
			} else {
				ticket.finish()
			}
			if d.Status().Ready {
				t.Fatal("ready before handler and flush finished")
			}
			if flushFirst {
				ticket.finish()
			} else {
				d.ConnState(conn, http.StateIdle)
			}
			if !d.Status().Ready {
				t.Fatal(d.Status())
			}
		})
	}
}

func TestManagedSessionPendingToolsAndCleanupBlockStop(t *testing.T) {
	d := New()
	ticket, _ := d.admit(httptest.NewRequest("GET", "/v1/responses", nil))
	session := ManageWebsocket(context.WithValue(context.Background(), requestKey, ticket))
	if signal := session.Waiting(true); signal != nil {
		t.Fatal("pending tools can be disconnected")
	}
	begin(t, d)
	if d.Status().Ready || d.Status().ActiveRequests != 0 {
		t.Fatal(d.Status())
	}
	if !session.BeginTurn() {
		t.Fatal("blocked pending tool continuation")
	}
	signal := session.Waiting(false)
	select {
	case <-signal:
	default:
		t.Fatal("idle session did not see drain")
	}
	if session.BeginTurn() {
		t.Fatal("started new turn during drain")
	}
	if d.CommitStop(d.Status().InstanceID, owner, nil) {
		t.Fatal("ready before upstream cleanup")
	}
	session.Close()
	ticket.finish()
	if !d.Status().Ready {
		t.Fatal(d.Status())
	}
}

func TestCancelResumesIdleSessionAndHTTP(t *testing.T) {
	d := New()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := d.Wrap(next, func(r *http.Request) bool { return r.URL.Path == "/status" })
	state := begin(t, d)
	for path, want := range map[string]int{"/inference": 503, "/status": 200} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Fatal(path, w.Code)
		}
	}
	if !d.Cancel(state.InstanceID, owner) {
		t.Fatal("cancel failed")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("POST", "/inference", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestConcurrentAdmissionVersusBeginHasNoUntrackedWork(t *testing.T) {
	for range 100 {
		d := New()
		var wg sync.WaitGroup
		wg.Add(2)
		var ticket *request
		go func() { defer wg.Done(); ticket, _ = d.admit(httptest.NewRequest("POST", "/work", nil)) }()
		go func() { defer wg.Done(); d.Begin(d.instance, owner) }()
		wg.Wait()
		s := d.Status()
		if s.Ready && ticket != nil {
			t.Fatal("ready with admitted work")
		}
		if ticket != nil {
			ticket.finish()
		}
	}
}

func TestHTTP2IdleDoesNotProveFinalFrameFlushed(t *testing.T) {
	d := New()
	conn, other := net.Pipe()
	defer conn.Close()
	defer other.Close()
	req := httptest.NewRequest("POST", "/work", nil)
	req.ProtoMajor = 2
	req = req.WithContext(d.ConnContext(context.Background(), conn))
	ticket, _ := d.admit(req)
	ticket.finish()
	begin(t, d)
	d.ConnState(conn, http.StateIdle)
	if d.Status().Ready {
		t.Fatal("HTTP/2 Idle may precede frame flush")
	}
	d.ConnState(conn, http.StateClosed)
	if !d.Status().Ready {
		t.Fatal(d.Status())
	}
}

func TestMissingConnectionMetadataCannotClaimFlushComplete(t *testing.T) {
	d := New()
	ticket, _ := d.admit(httptest.NewRequest("POST", "/work", nil))
	ticket.finish()
	if status := begin(t, d); status.Ready {
		t.Fatal("missing ConnContext/ConnState must fail closed")
	}
}

func TestStopEvaluatesDetachedWorkAfterAdmittedRequestCompletes(t *testing.T) {
	d := New()
	conn, other := net.Pipe()
	defer conn.Close()
	defer other.Close()
	req := httptest.NewRequest("POST", "/v1/realtime/calls", nil)
	req = req.WithContext(d.ConnContext(context.Background(), conn))
	ticket, _ := d.admit(req)
	instance := begin(t, d).InstanceID
	called, detached := false, false
	canStop := func() bool { called = true; return !detached }
	if d.CommitStop(instance, owner, canStop) || called {
		t.Fatal("blockers read before admitted request was complete")
	}
	// The bootstrap publishes its detached session before returning/flushing.
	detached = true
	ticket.finish()
	d.ConnState(conn, http.StateIdle)
	if d.CommitStop(instance, owner, canStop) || !called {
		t.Fatal("missed detached session handoff")
	}
	detached = false
	if !d.CommitStop(instance, owner, canStop) {
		t.Fatal("blocked after detached work ended")
	}
}

func TestCompletedHTTP2AccountingIsBoundedByConnectionCount(t *testing.T) {
	d := New()
	conn, other := net.Pipe()
	defer conn.Close()
	defer other.Close()
	req := httptest.NewRequest("POST", "/work", nil)
	req.ProtoMajor = 2
	req = req.WithContext(d.ConnContext(context.Background(), conn))
	for range 10000 {
		ticket, _ := d.admit(req)
		ticket.finish()
		d.ConnState(conn, http.StateIdle)
	}
	if len(d.requests) != 0 || len(d.connections) != 1 {
		t.Fatal("retained historical request tickets", len(d.requests), len(d.connections))
	}
	if status := begin(t, d); status.Ready || status.ActiveRequests != 1 {
		t.Fatal(status)
	}
	d.ConnState(conn, http.StateClosed)
	if status := d.Status(); !status.Ready || len(d.connections) != 0 {
		t.Fatal(status)
	}
}

func TestUnknownConnectionCompletionBarrierIsBounded(t *testing.T) {
	d := New()
	for range 10000 {
		ticket, _ := d.admit(httptest.NewRequest("POST", "/work", nil))
		ticket.finish()
	}
	if len(d.requests) != 0 || !d.untrackedCompletion {
		t.Fatal("retained historical untracked tickets")
	}
	if status := begin(t, d); status.Ready || status.ActiveRequests != 1 {
		t.Fatal(status)
	}
}

func TestHijackedSocketCloseRemovesItsConnectionState(t *testing.T) {
	d := New()
	conn, other := net.Pipe()
	defer other.Close()
	req := httptest.NewRequest("GET", "/background-ws", nil)
	req = req.WithContext(d.ConnContext(context.Background(), conn))
	ticket, _ := d.admit(req)
	ticket.hijacked = true
	ticket.finish()
	socket := &hijackedConn{Conn: conn, ticket: ticket}
	begin(t, d)
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	if len(d.connections) != 0 || len(d.requests) != 0 || !d.Status().Ready {
		t.Fatal("retained closed socket state", d.Status())
	}
}

func TestHijackedSocketRawTransportCloseRemovesItsConnectionState(t *testing.T) {
	d := New()
	conn, other := net.Pipe()
	defer other.Close()
	req := httptest.NewRequest("GET", "/v1/tunnel", nil)
	req = req.WithContext(d.ConnContext(context.Background(), conn))
	ticket, _ := d.admit(req)
	ticket.hijacked = true
	ticket.finish()
	socket := &hijackedConn{Conn: tls.Server(conn, &tls.Config{}), ticket: ticket}
	raw := socket.NetConn()
	if _, wrapped := raw.(interface{ NetConn() net.Conn }); wrapped {
		t.Fatal("raw transport must not expose another wrapper")
	}
	begin(t, d)
	// Closing the raw transport skips TLS close_notify but must still release the drain.
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	// A later Close of the TLS wrapper must not account for the socket twice.
	_ = socket.Close()
	if len(d.connections) != 0 || len(d.requests) != 0 || !d.Status().Ready {
		t.Fatal("retained closed socket state", d.Status())
	}
}
