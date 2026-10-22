package executor

import (
	"errors"
	"io"
	"sync"
)

// ExecutionLifecycle owns resources associated with an execution attempt.
type ExecutionLifecycle interface {
	Bind(func() error) error
	End(string)
}

// UpstreamFailureHandler controls session termination while the manager can retry.
type UpstreamFailureHandler interface {
	// Defer saves the termination action when the error can be retried.
	Defer(error, func()) bool
	// Recovered discards the previous failure when a connection is bound.
	Recovered()
	// Commit ends recovery and runs any pending termination action.
	// Call it before emitting a stream payload or after the final attempt.
	Commit()
}

// BindExecutionResource binds a closer to the execution lifecycle.
func BindExecutionResource(opts Options, closer io.Closer) error {
	if opts.ExecutionLifecycle == nil || closer == nil {
		return nil
	}

	var closeOnce sync.Once
	var closeErr error
	closeResource := func() error {
		closeOnce.Do(func() {
			closeErr = closer.Close()
		})
		return closeErr
	}
	if errBind := opts.ExecutionLifecycle.Bind(closeResource); errBind != nil {
		return errors.Join(errBind, closeResource())
	}
	return nil
}
