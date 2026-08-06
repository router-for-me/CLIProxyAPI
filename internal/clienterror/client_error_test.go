package clienterror

import (
	"errors"
	"net/http"
	"testing"
)

type statusError struct {
	status int
	body   string
}

func (e statusError) Error() string   { return e.body }
func (e statusError) StatusCode() int { return e.status }

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
