package helps

import "context"

// NewDetachableContext returns a context that carries parent's values and
// follows parent's cancellation until detach is called.
//
// It is meant for upstream requests whose response body should outlive the
// downstream request once the response is semantically complete. Before
// detach, when parent is cancelled, the returned context is cancelled with
// parent's cause, so errors surfaced through it keep parent's identity.
// After a successful detach, parent's cancellation no longer reaches it; it
// only ends with release.
//
// A parent with a deadline is returned unchanged and never detaches: a
// cancelled context cannot report context.DeadlineExceeded from Err(), and
// some transports (for example HTTP/2) report Err() rather than the cause, so
// forwarding the deadline as a cancellation would change the error callers
// see. Such requests keep their existing semantics and simply skip whatever
// the caller would do after a successful detach.
//
// detach reports whether the returned context was still live when it was
// detached; it returns false if parent was already done, if parent has a
// deadline, or if detach was called before. release stops propagation and
// cancels the returned context; it must always be called once the context is
// no longer needed.
func NewDetachableContext(parent context.Context) (ctx context.Context, detach func() bool, release func()) {
	if parent == nil {
		parent = context.Background()
	}
	if _, hasDeadline := parent.Deadline(); hasDeadline {
		return parent, func() bool { return false }, func() {}
	}
	ctx, cancel := context.WithCancelCause(context.WithoutCancel(parent))
	forward := func() { cancel(context.Cause(parent)) }
	stop := context.AfterFunc(parent, forward)
	if parent.Err() != nil {
		// AfterFunc runs forward in its own goroutine when parent is already
		// done; run it synchronously so callers never get a live context.
		forward()
	}
	detach = func() bool {
		if !stop() {
			return false
		}
		if parent.Err() != nil {
			// For parents the context package cannot watch directly,
			// AfterFunc waits in a goroutine that may not have run yet.
			forward()
			return false
		}
		return ctx.Err() == nil
	}
	release = func() {
		stop()
		cancel(nil)
	}
	return ctx, detach, release
}
