package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestErrorEnvelopeCarriesHTTPStatus(t *testing.T) {
	raw := errorEnvelope("forbidden", "permission denied", http.StatusForbidden)
	var env envelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		t.Fatalf("unmarshal envelope: %v", errUnmarshal)
	}
	if env.OK {
		t.Fatalf("env.OK = true, want false")
	}
	if env.Error == nil {
		t.Fatal("env.Error = nil, want non-nil")
	}
	if env.Error.HTTPStatus != http.StatusForbidden {
		t.Fatalf("env.Error.HTTPStatus = %d, want %d", env.Error.HTTPStatus, http.StatusForbidden)
	}
}

type mockStatusError struct {
	status int
	msg    string
}

func (m mockStatusError) Error() string   { return m.msg }
func (m mockStatusError) StatusCode() int { return m.status }

func TestErrorEnvelopeFromStatusError(t *testing.T) {
	inner := mockStatusError{status: http.StatusTooManyRequests, msg: "rate limit exceeded"}
	wrappedErr := fmt.Errorf("call upstream: %w", inner)
	status := 0
	type statusCoder interface{ StatusCode() int }
	var sc statusCoder
	if errors.As(wrappedErr, &sc) && sc != nil {
		status = sc.StatusCode()
	}
	raw := errorEnvelope("rate_limit_error", wrappedErr.Error(), status)
	var env envelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		t.Fatalf("unmarshal envelope: %v", errUnmarshal)
	}
	if env.Error == nil || env.Error.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("expected HTTPStatus %d, got %+v", http.StatusTooManyRequests, env.Error)
	}
}
