package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestApplyRequestAfterAuthInterceptorRejectPropagates(t *testing.T) {
	opts := cliproxyexecutor.Options{
		RequestAfterAuthInterceptor: func(_ context.Context, _ cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{Reject: true, RejectReason: "policy denied"}
		},
	}
	req := cliproxyexecutor.Request{Model: "gpt", Payload: []byte("body")}

	outReq, _, errAfterAuth := applyRequestAfterAuthInterceptor(context.Background(), nil, "openai", req, opts, "gpt")

	if errAfterAuth == nil {
		t.Fatal("expected a rejection error to propagate from the after-auth interceptor")
	}
	if e, ok := errAfterAuth.(*Error); ok {
		if e.HTTPStatus != http.StatusForbidden {
			t.Fatalf("HTTPStatus = %d, want %d", e.HTTPStatus, http.StatusForbidden)
		}
		if e.Code != "request_rejected_by_plugin" {
			t.Fatalf("Code = %q, want %q", e.Code, "request_rejected_by_plugin")
		}
		if e.Message != "policy denied" {
			t.Fatalf("Message = %q, want %q", e.Message, "policy denied")
		}
		if e.Retryable {
			t.Fatal("Retryable = true, want false for plugin policy rejection")
		}
	} else {
		t.Fatalf("error type = %T, want *Error", errAfterAuth)
	}
	if string(outReq.Payload) != "body" {
		t.Fatalf("Payload = %q, want original body on reject", outReq.Payload)
	}
	if !isRequestInvalidError(errAfterAuth) {
		t.Fatal("isRequestInvalidError should treat plugin rejection as terminal")
	}
	if !isRequestRejectedByPluginError(errAfterAuth) {
		t.Fatal("isRequestRejectedByPluginError should match request_rejected_by_plugin")
	}
	if _, shouldRetry := (&Manager{}).shouldRetryAfterError(errAfterAuth, 0, []string{"openai"}, "gpt", time.Minute); shouldRetry {
		t.Fatal("shouldRetryAfterError should not retry plugin policy rejections")
	}
}
