package auth

import (
	"context"
	"fmt"
	"time"
)

// CircuitBreakerFailureState is the strong-consistency view used by routing.
type CircuitBreakerFailureState struct {
	Provider            string
	AuthID              string
	Model               string
	NormalizedModel     string
	ConsecutiveFailures int
	LastReason          string
	LastHTTPStatus      int
	LastFailedAt        time.Time
	UpdatedAt           time.Time
}

// CircuitBreakerFailureEvent records one upstream failure for audit.
type CircuitBreakerFailureEvent struct {
	RequestID           string
	Provider            string
	AuthID              string
	Model               string
	NormalizedModel     string
	Reason              string
	HTTPStatus          int
	ConsecutiveFailures int
	CircuitOpened       bool
	CreatedAt           time.Time
}

// CircuitBreakerFailureStore persists consecutive auth+model failures.
type CircuitBreakerFailureStore interface {
	GetFailureCounts(ctx context.Context, model string) (map[string]int, error)
	RecordFailure(ctx context.Context, event CircuitBreakerFailureEvent) (CircuitBreakerFailureState, error)
	ResetFailure(ctx context.Context, provider, authID, model string) error
}

func circuitBreakerFailureStoreError(action string, err error) *Error {
	if err == nil {
		return nil
	}
	return &Error{
		Code:       "circuit_breaker_failure_store_unavailable",
		Message:    fmt.Sprintf("circuit breaker failure store %s failed: %v", action, err),
		Retryable:  true,
		HTTPStatus: 503,
	}
}
