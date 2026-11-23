package executor

import (
	"context"
	"errors"
	"fmt"
)

// ConfiguredTLSHandshakeTimeoutError identifies a TLS handshake failure
// caused by an internally configured establishment bound rather than by
// the caller's request context. Transports construct it only after they
// have established that the parent context was still live and their own
// handshake child context expired.
//
// The concrete type is deliberately shared through this neutral executor
// package so both transport helpers and auth retry classification can
// recognize it without importing each other or trusting error text.
type ConfiguredTLSHandshakeTimeoutError struct {
	cause error
}

// NewConfiguredTLSHandshakeTimeoutError wraps the handshake failure that
// resulted from an internally configured TLS handshake deadline.
func NewConfiguredTLSHandshakeTimeoutError(cause error) error {
	if cause == nil {
		return nil
	}
	return &ConfiguredTLSHandshakeTimeoutError{cause: cause}
}

func (e *ConfiguredTLSHandshakeTimeoutError) Error() string {
	if e == nil || e.cause == nil {
		return "tls handshake timeout"
	}
	return fmt.Sprintf("tls handshake timeout: %v", e.cause)
}

// Unwrap preserves the original handshake error chain, including
// context.DeadlineExceeded, for diagnostics and errors.Is checks.
func (e *ConfiguredTLSHandshakeTimeoutError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// IsConfiguredTLSHandshakeTimeoutError reports whether err is exclusively
// explained by configured TLS handshake deadlines.
//
// Every context.Canceled or context.DeadlineExceeded occurrence in the
// error tree must be contained in a ConfiguredTLSHandshakeTimeoutError
// subtree, and at least one configured timeout subtree must itself contain
// a deadline. A caller-owned cancellation or deadline anywhere outside
// such a subtree therefore wins, including when it is combined with a
// configured timeout through errors.Join. Matching error text is never
// sufficient on its own.
func IsConfiguredTLSHandshakeTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	foundConfiguredDeadline, hasUnownedContextError := inspectConfiguredTLSHandshakeTimeout(err)
	return foundConfiguredDeadline && !hasUnownedContextError
}

func inspectConfiguredTLSHandshakeTimeout(err error) (foundConfiguredDeadline bool, hasUnownedContextError bool) {
	if err == nil {
		return false, false
	}
	if configured, ok := err.(*ConfiguredTLSHandshakeTimeoutError); ok && configured != nil {
		// The transport has already proven that this subtree's deadline is
		// internally owned and that its parent context was still live.
		return errors.Is(configured.cause, context.DeadlineExceeded), false
	}
	// A node can assert caller-context identity through its own Is method
	// while Unwrap exposes unrelated children. Inspect that own identity
	// at every non-configured node, before descending, so such a caller
	// error cannot hide behind a joined configured timeout.
	hasUnownedContextError = hasOwnContextIdentity(err)
	for _, child := range unwrapChildErrors(err) {
		childFound, childUnowned := inspectConfiguredTLSHandshakeTimeout(child)
		foundConfiguredDeadline = foundConfiguredDeadline || childFound
		hasUnownedContextError = hasUnownedContextError || childUnowned
	}
	return foundConfiguredDeadline, hasUnownedContextError
}

func hasOwnContextIdentity(err error) bool {
	if err == context.Canceled || err == context.DeadlineExceeded {
		return true
	}
	if checker, ok := err.(interface{ Is(error) bool }); ok {
		return checker.Is(context.Canceled) || checker.Is(context.DeadlineExceeded)
	}
	return false
}

func unwrapChildErrors(err error) []error {
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		if children := multi.Unwrap(); len(children) > 0 {
			return children
		}
	}
	if single, ok := err.(interface{ Unwrap() error }); ok {
		if child := single.Unwrap(); child != nil {
			return []error{child}
		}
	}
	return nil
}
