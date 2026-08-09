package clienterror

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

type statusError struct {
	status int
	body   string
}

func (e statusError) Error() string   { return e.body }
func (e statusError) StatusCode() int { return e.status }

func TestHTTPStatusFromError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "nil", err: nil, want: 0},
		{name: "plain error", err: errors.New("boom"), want: 0},
		{name: "context canceled", err: context.Canceled, want: StatusClientClosedRequest},
		{name: "context deadline exceeded", err: context.DeadlineExceeded, want: http.StatusGatewayTimeout},
		{
			name: "url error wraps canceled",
			err:  &url.Error{Op: "Post", URL: "https://example.com", Err: context.Canceled},
			want: StatusClientClosedRequest,
		},
		{
			name: "url error wraps deadline",
			err:  &url.Error{Op: "Post", URL: "https://example.com", Err: context.DeadlineExceeded},
			want: http.StatusGatewayTimeout,
		},
		{
			name: "fmt wrap canceled",
			err:  fmt.Errorf("upstream: %w", context.Canceled),
			want: StatusClientClosedRequest,
		},
		{
			name: "explicit status code wins",
			err:  statusError{status: http.StatusTooManyRequests, body: "rate limited"},
			want: http.StatusTooManyRequests,
		},
		{
			name: "explicit status wins over canceled unwrap",
			err: statusAndUnwrapError{
				status: http.StatusTooManyRequests,
				body:   "rate limited",
				cause:  context.Canceled,
			},
			want: http.StatusTooManyRequests,
		},
		{
			name: "zero status code falls through to canceled unwrap",
			err: statusAndUnwrapError{
				status: 0,
				body:   "canceled",
				cause:  context.Canceled,
			},
			want: StatusClientClosedRequest,
		},
		{
			name: "zero status code without unwrap stays unknown",
			err:  statusError{status: 0, body: context.Canceled.Error()},
			want: 0,
		},
		{
			name: "wrapped status code via errors.As",
			err:  fmt.Errorf("execute failed: %w", statusError{status: http.StatusUnauthorized, body: "unauthorized"}),
			want: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HTTPStatusFromError(tc.err); got != tc.want {
				t.Fatalf("HTTPStatusFromError() = %d, want %d", got, tc.want)
			}
		})
	}

	if got := HTTPStatusFromErrorOr(errors.New("boom"), http.StatusBadGateway); got != http.StatusBadGateway {
		t.Fatalf("HTTPStatusFromErrorOr(plain) = %d, want %d", got, http.StatusBadGateway)
	}
	if got := HTTPStatusFromErrorOr(context.Canceled, http.StatusBadGateway); got != StatusClientClosedRequest {
		t.Fatalf("HTTPStatusFromErrorOr(canceled) = %d, want %d", got, StatusClientClosedRequest)
	}
}

type statusAndUnwrapError struct {
	status int
	body   string
	cause  error
}

func (e statusAndUnwrapError) Error() string { return e.body }
func (e statusAndUnwrapError) StatusCode() int {
	return e.status
}
func (e statusAndUnwrapError) Unwrap() error { return e.cause }

func TestIsRequestFaultStructuredIdentifiers(t *testing.T) {
	for _, code := range []string{
		"cyber_policy",
		"context_length_exceeded",
		"message_too_big",
		"string_above_max_length",
		"invalid_prompt",
		"invalid_value",
		"unsupported_value",
		"invalid_request_error",
		"previous_response_not_found",
	} {
		t.Run("code/"+code, func(t *testing.T) {
			err := errors.New(`{"error":{"code":"` + code + `"}}`)
			if !IsRequestFault(http.StatusBadGateway, err) {
				t.Fatalf("code %q was not classified as a request fault", code)
			}
		})
	}

	for _, errType := range []string{
		"invalid_request",
		"invalid_request_error",
		"bad_request_error",
		"invalid_prompt",
	} {
		t.Run("type/"+errType, func(t *testing.T) {
			err := errors.New(`{"error":{"type":"` + errType + `"}}`)
			if !IsRequestFault(http.StatusBadGateway, err) {
				t.Fatalf("type %q was not classified as a request fault", errType)
			}
		})
	}
}

func TestIsRequestFault(t *testing.T) {
	tests := []struct {
		name   string
		status int
		err    error
		want   bool
	}{
		{name: "bad request status", status: http.StatusBadRequest, err: errors.New("bad request"), want: true},
		{name: "conflict status", status: http.StatusConflict, err: errors.New("conflict"), want: true},
		{name: "entity too large status", status: http.StatusRequestEntityTooLarge, err: errors.New("too large"), want: true},
		{name: "unprocessable status", status: http.StatusUnprocessableEntity, err: errors.New("unprocessable"), want: true},
		{
			name:   "cyber policy behind bad gateway",
			status: http.StatusBadGateway,
			err:    errors.New(`{"error":{"type":"invalid_request","code":"cyber_policy","message":"blocked"}}`),
			want:   true,
		},
		{
			name:   "context length behind internal error",
			status: http.StatusInternalServerError,
			err:    errors.New(`{"response":{"error":{"type":"server_error","code":"context_length_exceeded"}}}`),
			want:   true,
		},
		{
			name:   "invalid request type behind bad gateway",
			status: http.StatusBadGateway,
			err:    errors.New(`{"body":{"error":{"type":"invalid_request","message":"invalid"}}}`),
			want:   true,
		},
		{
			name: "status from error",
			err:  statusError{status: http.StatusConflict, body: "conflict"},
			want: true,
		},
		{
			// Verbatim upstream text: plain text, not JSON, so it can only be matched
			// by message.
			name:   "item not persisted with store=false",
			status: http.StatusNotFound,
			err:    errors.New("Item with id 'rs_0b5f3eb6f51f175c0169ca74e4a85881998539920821603a74' not found. Items are not persisted when `store` is set to false. Try again with `store` set to true, or remove this item from your input."),
			want:   true,
		},
		{
			// An upstream internal error is not a request fault: it must stay eligible
			// for credential rotation and (credential, model) cooldown.
			name:   "upstream unknown internal error",
			status: http.StatusInternalServerError,
			err:    errors.New(`{"error":{"code":500,"message":"Internal error encountered.","status":"UNKNOWN"}}`),
		},
		{name: "plain not found", status: http.StatusNotFound, err: errors.New("model not found")},
		{name: "unauthorized", status: http.StatusUnauthorized, err: errors.New("invalid token")},
		{name: "quota", status: http.StatusTooManyRequests, err: errors.New("quota")},
		{name: "transport", status: http.StatusBadGateway, err: errors.New("unexpected EOF")},
		{name: "invalid JSON body", status: http.StatusBadGateway, err: errors.New(`{"error":`)},
		{name: "nil", status: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRequestFault(tc.status, tc.err); got != tc.want {
				t.Fatalf("IsRequestFault(%d, %v) = %t, want %t", tc.status, tc.err, got, tc.want)
			}
		})
	}
}
