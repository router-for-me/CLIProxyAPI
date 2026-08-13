package executor

import (
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestOpenAICompatResponseStatusErrorPreservesRetryAfterAndHeaders(t *testing.T) {
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:               "wafer",
		PassthroughHeaders: true,
	}}}
	executor := NewOpenAICompatExecutor("openai-compatible-wafer", cfg)
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatible-wafer",
		Attributes: map[string]string{
			"compat_name": "wafer",
		},
	}
	response := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Retry-After":  {"2"},
			"X-Request-Id": {"wafer-request"},
		},
	}

	err := executor.responseStatusError(auth, response, []byte(`{"error":"capacity"}`))
	if retryAfter := err.RetryAfter(); retryAfter == nil || *retryAfter != 2*time.Second {
		t.Fatalf("RetryAfter() = %v, want 2s", retryAfter)
	}
	if got := err.Headers().Get("X-Request-Id"); got != "wafer-request" {
		t.Fatalf("X-Request-Id = %q, want wafer-request", got)
	}
	if !err.PassthroughHeaders() {
		t.Fatal("expected provider-scoped passthrough")
	}
}
