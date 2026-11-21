package helps

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type detachableContextKey struct{}

func TestNewDetachableContext_FollowsParentUntilDetached(t *testing.T) {
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), detachableContextKey{}, "v"))
	ctx, _, release := NewDetachableContext(parent)
	defer release()

	if got := ctx.Value(detachableContextKey{}); got != "v" {
		t.Fatalf("value = %v, want v", got)
	}
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("parent cancellation did not propagate before detach")
	}
}

func TestNewDetachableContext_DetachStopsPropagation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, detach, release := NewDetachableContext(parent)

	if !detach() {
		t.Fatal("detach() = false on a live context")
	}
	if detach() {
		t.Fatal("second detach() = true, want false")
	}
	cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("detached context cancelled by parent: %v", err)
	}
	release()
	if ctx.Err() == nil {
		t.Fatal("release() did not cancel the context")
	}
}

func TestNewDetachableContext_DetachAfterParentCancelReportsFalse(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, detach, release := NewDetachableContext(parent)
	defer release()

	cancel()
	<-ctx.Done()
	if detach() {
		t.Fatal("detach() = true after parent cancellation propagated")
	}
}

func TestNewDetachableContext_AlreadyCanceledParentCancelsImmediately(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	ctx, detach, release := NewDetachableContext(parent)
	defer release()

	if ctx.Err() == nil {
		t.Fatal("context still live for an already-cancelled parent")
	}
	if detach() {
		t.Fatal("detach() = true for an already-cancelled parent")
	}
}

// pendingPropagationContext is a valid context the context package cannot
// see through, so AfterFunc watches it from its own goroutine. The first Err
// call after cancellation (made by that goroutine) blocks until gate is
// closed, which holds propagation pending while the test calls detach.
type pendingPropagationContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
	gate    chan struct{}
}

func (c *pendingPropagationContext) Err() error {
	err := c.Context.Err()
	if err != nil {
		first := false
		c.once.Do(func() { first = true })
		if first {
			close(c.entered)
			<-c.gate
		}
	}
	return err
}

func (c *pendingPropagationContext) Value(any) any { return nil }

func TestNewDetachableContext_DetachAfterParentCancelBeforePropagationReportsFalse(t *testing.T) {
	inner, cancel := context.WithCancel(context.Background())
	parent := &pendingPropagationContext{Context: inner, entered: make(chan struct{}), gate: make(chan struct{})}
	ctx, detach, release := NewDetachableContext(parent)
	defer release()
	defer close(parent.gate)

	cancel()
	<-parent.entered
	if detach() {
		t.Fatal("detach() = true after parent cancellation")
	}
	if !errors.Is(context.Cause(ctx), context.Canceled) {
		t.Fatalf("Cause() = %v, want context.Canceled", context.Cause(ctx))
	}
}

var errDetachableCustomCause = errors.New("custom cause")

func TestNewDetachableContext_ForwardsParentCause(t *testing.T) {
	parent, cancel := context.WithCancelCause(context.Background())
	ctx, _, release := NewDetachableContext(parent)
	defer release()

	cancel(errDetachableCustomCause)
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("parent cancellation did not propagate")
	}
	if got := context.Cause(ctx); !errors.Is(got, errDetachableCustomCause) {
		t.Fatalf("Cause() = %v, want custom cause", got)
	}
}

func TestNewDetachableContext_AlreadyDoneParentKeepsCause(t *testing.T) {
	parent, cancel := context.WithCancelCause(context.Background())
	cancel(errDetachableCustomCause)
	ctx, _, release := NewDetachableContext(parent)
	defer release()

	if got := context.Cause(ctx); !errors.Is(got, errDetachableCustomCause) {
		t.Fatalf("Cause() = %v, want custom cause", got)
	}
}

// A parent with a deadline is used as-is and never detaches, so its deadline
// and error identity are untouched and the caller keeps its old behaviour.
func TestNewDetachableContext_DeadlineParentIsReturnedUnchanged(t *testing.T) {
	parent, cancel := context.WithTimeout(context.WithValue(context.Background(), detachableContextKey{}, "v"), time.Hour)
	defer cancel()
	ctx, detach, release := NewDetachableContext(parent)
	defer release()

	if ctx != parent {
		t.Fatal("deadline parent was wrapped, want it returned unchanged")
	}
	if detach() {
		t.Fatal("detach() = true for a deadline parent")
	}
	release()
	if err := parent.Err(); err != nil {
		t.Fatalf("release() cancelled the parent: %v", err)
	}
}

// Errors that net/http surfaces through the detachable context must keep the
// identity they have with the parent context itself, both while the request
// is sent and while the body is read.
func TestNewDetachableContext_HTTPErrorsMatchParent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: x\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()

	parents := map[string]func() (context.Context, func()){
		"cancel": func() (context.Context, func()) {
			return context.WithCancel(context.Background())
		},
		"cancel cause": func() (context.Context, func()) {
			ctx, cancel := context.WithCancelCause(context.Background())
			return ctx, func() { cancel(errDetachableCustomCause) }
		},
	}
	run := func(t *testing.T, newParent func() (context.Context, func()), detachable, inBody bool) error {
		t.Helper()
		parent, end := newParent()
		ctx := parent
		if detachable {
			var release func()
			ctx, _, release = NewDetachableContext(parent)
			defer release()
		}
		req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		if errReq != nil {
			t.Fatal(errReq)
		}
		if !inBody {
			end()
			resp, errRoundTrip := transport.RoundTrip(req)
			if errRoundTrip == nil {
				_ = resp.Body.Close()
			}
			return errRoundTrip
		}
		resp, errRoundTrip := transport.RoundTrip(req)
		if errRoundTrip != nil {
			t.Fatalf("RoundTrip() error = %v", errRoundTrip)
		}
		defer func() { _ = resp.Body.Close() }()
		buf := make([]byte, 64)
		_, _ = resp.Body.Read(buf)
		end()
		for {
			if _, errRead := resp.Body.Read(buf); errRead != nil {
				return errRead
			}
		}
	}
	identity := func(err error) [3]bool {
		return [3]bool{errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, errDetachableCustomCause)}
	}
	for name, newParent := range parents {
		for _, inBody := range []bool{false, true} {
			phase := "request"
			if inBody {
				phase = "body"
			}
			t.Run(name+"/"+phase, func(t *testing.T) {
				want := identity(run(t, newParent, false, inBody))
				got := identity(run(t, newParent, true, inBody))
				if want == [3]bool{} {
					t.Fatal("parent context produced no context error")
				}
				if got != want {
					t.Fatalf("error identity [canceled deadline custom] = %v, want %v", got, want)
				}
			})
		}
	}
}

// HTTP/2 reports ctx.Err() directly on some paths, so a deadline must not be
// turned into a cancellation by the detachable context.
func TestNewDetachableContext_HTTP2DeadlineErrorMatchesParent(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	client := server.Client()
	defer client.CloseIdleConnections()

	warm, errWarm := client.Get(server.URL)
	if errWarm != nil {
		t.Fatalf("warm-up request error = %v", errWarm)
	}
	_ = warm.Body.Close()
	if warm.ProtoMajor != 2 {
		t.Fatalf("warm-up protocol = %s, want HTTP/2", warm.Proto)
	}

	do := func(detachable bool) error {
		parent, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
		defer cancel()
		ctx := parent
		if detachable {
			var release func()
			ctx, _, release = NewDetachableContext(parent)
			defer release()
		}
		req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		if errReq != nil {
			t.Fatal(errReq)
		}
		resp, errDo := client.Do(req)
		if errDo == nil {
			_ = resp.Body.Close()
		}
		return errDo
	}
	want, got := do(false), do(true)
	if !errors.Is(want, context.DeadlineExceeded) {
		t.Fatalf("parent error = %v, want context.DeadlineExceeded", want)
	}
	if !errors.Is(got, context.DeadlineExceeded) || errors.Is(got, context.Canceled) {
		t.Fatalf("detachable error = %v, want context.DeadlineExceeded like parent (%v)", got, want)
	}
}
