package auth

import (
	"errors"
	"net/http"
	"testing"
)

type statusOnlyError struct {
	status int
	msg    string
}

func (e statusOnlyError) Error() string   { return e.msg }
func (e statusOnlyError) StatusCode() int { return e.status }

func TestResultErrorFromExec_NormalizesKnownUpstreamFailure(t *testing.T) {
	err := statusOnlyError{
		status: http.StatusBadRequest,
		msg:    `{"kind":"request_error:request_body_truncated","message":"Post \"https://cpa.zhangxike.me/v1/responses\": context canceled","upstream_request_body":"{\"model\":\"gpt-5.4\"}"}`,
	}
	result := resultErrorFromExec(err)
	if result == nil {
		t.Fatal("expected result error, got nil")
	}
	if result.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", result.HTTPStatus, http.StatusBadGateway)
	}
	if result.Message != "upstream request was interrupted before completion" {
		t.Fatalf("message = %q", result.Message)
	}
	if result.Code != "upstream_request_interrupted" {
		t.Fatalf("code = %q", result.Code)
	}
}

func TestIsRequestInvalidError_UsesNormalizedPayload(t *testing.T) {
	if !isRequestInvalidError(statusOnlyError{status: http.StatusBadRequest, msg: `{"error":{"type":"invalid_request_error","message":"bad request"}}`}) {
		t.Fatal("expected invalid_request_error payload to be treated as request invalid")
	}
	if isRequestInvalidError(statusOnlyError{status: http.StatusBadRequest, msg: `{"kind":"request_error:request_body_truncated","message":"context canceled"}`}) {
		t.Fatal("expected truncated upstream request payload to be treated as upstream failure")
	}
	if isRequestInvalidError(errors.New("context canceled")) {
		t.Fatal("expected status-less transport cancellation to remain non-request-invalid")
	}
}
