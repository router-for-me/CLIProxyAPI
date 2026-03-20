package upstreamerrors

import (
	"net/http"
	"testing"
)

func TestNormalize_RequestBodyTruncatedContextCanceled(t *testing.T) {
	normalized := Normalize(http.StatusBadRequest, `{"kind":"request_error:request_body_truncated","message":"Post \"https://cpa.zhangxike.me/v1/responses\": context canceled","platform":"openai","account_id":481,"account_name":"CPA号池","upstream_request_body":"{\"model\":\"gpt-5.4\"}"}`)
	if normalized.Status != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", normalized.Status, http.StatusBadGateway)
	}
	if normalized.Message != "upstream request was interrupted before completion" {
		t.Fatalf("message = %q", normalized.Message)
	}
	if normalized.Type != "server_error" {
		t.Fatalf("type = %q, want %q", normalized.Type, "server_error")
	}
	if normalized.Code != "upstream_request_interrupted" {
		t.Fatalf("code = %q, want %q", normalized.Code, "upstream_request_interrupted")
	}
}

func TestNormalize_NestedOpenAIErrorPayload(t *testing.T) {
	normalized := Normalize(http.StatusTooManyRequests, `{"error":{"message":"Rate limit reached","type":"rate_limit_error","code":"rate_limit_exceeded"},"x_extra":"ignored"}`)
	if normalized.Status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", normalized.Status, http.StatusTooManyRequests)
	}
	if normalized.Message != "Rate limit reached" {
		t.Fatalf("message = %q", normalized.Message)
	}
	if normalized.Type != "rate_limit_error" {
		t.Fatalf("type = %q", normalized.Type)
	}
	if normalized.Code != "rate_limit_exceeded" {
		t.Fatalf("code = %q", normalized.Code)
	}
}
