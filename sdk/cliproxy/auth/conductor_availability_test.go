package auth

import (
	"net/http"
	"testing"
	"time"
)

type testStatusError struct {
	code int
	msg  string
}

func (e testStatusError) Error() string {
	return e.msg
}

func (e testStatusError) StatusCode() int {
	return e.code
}

func TestUpdateAggregatedAvailability_UnavailableWithoutNextRetryDoesNotBlockAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:      StatusError,
				Unavailable: true,
			},
		},
	}

	updateAggregatedAvailability(auth, now)

	if auth.Unavailable {
		t.Fatalf("auth.Unavailable = true, want false")
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = %v, want zero", auth.NextRetryAfter)
	}
}

func TestUpdateAggregatedAvailability_FutureNextRetryBlocksAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	next := now.Add(5 * time.Minute)
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
			},
		},
	}

	updateAggregatedAvailability(auth, now)

	if !auth.Unavailable {
		t.Fatalf("auth.Unavailable = false, want true")
	}
	if auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = zero, want %v", next)
	}
	if auth.NextRetryAfter.Sub(next) > time.Second || next.Sub(auth.NextRetryAfter) > time.Second {
		t.Fatalf("auth.NextRetryAfter = %v, want %v", auth.NextRetryAfter, next)
	}
}

func TestIsRequestInvalidError_ReturnsFalseForValidationRequired(t *testing.T) {
	t.Parallel()

	err := testStatusError{
		code: http.StatusBadRequest,
		msg:  `{"error":{"type":"invalid_request_error","code":"validation_required","message":"validation required: API key must be reset"}}`,
	}
	if isRequestInvalidError(err) {
		t.Fatal("expected validation_required errors to be retryable")
	}
}

func TestIsRequestInvalidError_ReturnsTrueForInvalidRequestError(t *testing.T) {
	t.Parallel()

	err := testStatusError{
		code: http.StatusBadRequest,
		msg:  `{"error":{"type":"invalid_request_error","message":"bad format"}}`,
	}
	if !isRequestInvalidError(err) {
		t.Fatal("expected invalid_request_error to stop rotation")
	}
}
