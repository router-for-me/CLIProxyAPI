package executor

import (
	"errors"
	"sync/atomic"
	"testing"
)

type lifecycleRecorder struct {
	closeFn func() error
}

func (r *lifecycleRecorder) Bind(closeFn func() error) error {
	r.closeFn = closeFn
	return nil
}

func (*lifecycleRecorder) End(string) {}

type lifecycleCloser struct {
	calls atomic.Int32
}

func (c *lifecycleCloser) Close() error {
	c.calls.Add(1)
	return nil
}

func TestBindExecutionResourceClosesResourceOnce(t *testing.T) {
	lifecycle := &lifecycleRecorder{}
	closer := &lifecycleCloser{}

	if errBind := BindExecutionResource(Options{ExecutionLifecycle: lifecycle}, closer); errBind != nil {
		t.Fatalf("BindExecutionResource() error = %v", errBind)
	}
	if lifecycle.closeFn == nil {
		t.Fatal("BindExecutionResource() did not bind a closer")
	}
	if errClose := lifecycle.closeFn(); errClose != nil {
		t.Fatalf("first close error = %v", errClose)
	}
	if errClose := lifecycle.closeFn(); errClose != nil {
		t.Fatalf("second close error = %v", errClose)
	}
	if got := closer.calls.Load(); got != 1 {
		t.Fatalf("closer calls = %d, want 1", got)
	}
}

func TestBindExecutionResourceClosesWhenBindFails(t *testing.T) {
	want := errors.New("selection ended")
	lifecycle := &failingLifecycle{err: want}
	closer := &lifecycleCloser{}

	errBind := BindExecutionResource(Options{ExecutionLifecycle: lifecycle}, closer)
	if !errors.Is(errBind, want) {
		t.Fatalf("BindExecutionResource() error = %v, want %v", errBind, want)
	}
	if got := closer.calls.Load(); got != 1 {
		t.Fatalf("closer calls = %d, want 1", got)
	}
}

type failingLifecycle struct {
	err error
}

func (l *failingLifecycle) Bind(func() error) error { return l.err }
func (*failingLifecycle) End(string)                {}
