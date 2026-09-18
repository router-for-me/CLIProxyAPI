package auth

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestIsRetryableTransientStatus(t *testing.T) {
	for _, code := range []int{500, 502, 503, 504, 408, 429} {
		if !IsRetryableStatus(code) {
			t.Fatalf("status %d should be retryable", code)
		}
	}
}

func TestIsRetryableNonTransientStatus(t *testing.T) {
	for _, code := range []int{200, 201, 204, 400, 401, 403, 404, 422} {
		if IsRetryableStatus(code) {
			t.Fatalf("status %d should NOT be retryable", code)
		}
	}
}

func TestIsRetryableNetworkErrors(t *testing.T) {
	cases := []error{
		io.EOF,
		io.ErrUnexpectedEOF,
		&net.OpError{Op: "dial", Err: errors.New("connection reset")},
		&url.Error{Op: "Post", URL: "https://x.example", Err: errors.New("EOF")},
	}
	for _, e := range cases {
		if !IsRetryableError(e) {
			t.Fatalf("expected retryable: %v", e)
		}
	}
}

func TestIsRetryableApplicationErrors(t *testing.T) {
	if IsRetryableError(errors.New("invalid_request_error: model not supported")) {
		t.Fatalf("application errors should not be retryable")
	}
	if IsRetryableError(nil) {
		t.Fatalf("nil error should not be retryable")
	}
}

func TestIsRetryableUnifiedPredicate(t *testing.T) {
	r := AdaptHTTPResponse(&http.Response{StatusCode: 503})
	if !IsRetryable(r, nil) {
		t.Fatalf("503 should be retryable")
	}
	if IsRetryable(nil, errors.New("invalid_request_error")) {
		t.Fatalf("nil response + application error should NOT be retryable")
	}
	if !IsRetryable(nil, io.EOF) {
		t.Fatalf("nil response + EOF SHOULD be retryable (transient network error)")
	}
	// Sanity: nil response + nil error = no event = not retryable.
	if IsRetryable(nil, nil) {
		t.Fatalf("nil response + nil error should NOT be retryable")
	}
}

func TestClassifyRetryHonorsRetryAfterDeltaSeconds(t *testing.T) {
	r := AdaptHTTPResponse(&http.Response{Header: http.Header{"Retry-After": []string{"2"}}})
	d := RetryAfterDuration(r, time.Now().Add(10*time.Second))
	if d < time.Second || d > 3*time.Second {
		t.Fatalf("Retry-After=2 parse wrong: %v", d)
	}
}

func TestClassifyRetryHonorsRetryAfterHTTPDate(t *testing.T) {
	future := time.Now().UTC().Add(2 * time.Second).Format(http.TimeFormat)
	r := AdaptHTTPResponse(&http.Response{Header: http.Header{"Retry-After": []string{future}}})
	d := RetryAfterDuration(r, time.Now().Add(10*time.Second))
	if d < time.Second || d > 3*time.Second {
		t.Fatalf("Retry-After HTTP-date parse wrong: %v", d)
	}
}

func TestClassifyRetryAfterClampsToDeadline(t *testing.T) {
	r := AdaptHTTPResponse(&http.Response{Header: http.Header{"Retry-After": []string{"30"}}})
	d := RetryAfterDuration(r, time.Now().Add(2*time.Second))
	if d > 2*time.Second+50*time.Millisecond {
		t.Fatalf("Retry-After=30 should clamp to deadline ~2s, got %v", d)
	}
}

func TestClassifyRetryAfterMissingReturnsZero(t *testing.T) {
	r := AdaptHTTPResponse(&http.Response{Header: http.Header{}})
	if d := RetryAfterDuration(r, time.Now().Add(time.Minute)); d != 0 {
		t.Fatalf("missing Retry-After should return 0, got %v", d)
	}
	if d := RetryAfterDuration(nil, time.Now()); d != 0 {
		t.Fatalf("nil response should return 0, got %v", d)
	}
}

func TestAdaptHTTPResponseNilSafe(t *testing.T) {
	if AdaptHTTPResponse(nil) != nil {
		t.Fatalf("AdaptHTTPResponse(nil) should be nil")
	}
}
