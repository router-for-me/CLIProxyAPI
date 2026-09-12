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
