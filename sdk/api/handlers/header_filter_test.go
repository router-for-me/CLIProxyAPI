package handlers

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestFilterUpstreamHeaders_RemovesConnectionScopedHeaders(t *testing.T) {
	src := http.Header{}
	src.Add("Connection", "keep-alive, x-hop-a, x-hop-b")
	src.Add("Connection", "x-hop-c")
	src.Set("Keep-Alive", "timeout=5")
	src.Set("X-Hop-A", "a")
	src.Set("X-Hop-B", "b")
	src.Set("X-Hop-C", "c")
	src.Set("X-Request-Id", "req-1")
	src.Set("Set-Cookie", "session=secret")
	src.Set("x-cpa-trace-id", "upstream-trace")
	src.Set("Access-Control-Expose-Headers", "upstream-header")

	filtered := FilterUpstreamHeaders(src)
	if filtered == nil {
		t.Fatalf("expected filtered headers, got nil")
	}

	requestID := filtered.Get("X-Request-Id")
	if requestID != "req-1" {
		t.Fatalf("expected X-Request-Id to be preserved, got %q", requestID)
	}

	blockedHeaderKeys := []string{
		"Connection",
		"Keep-Alive",
		"X-Hop-A",
		"X-Hop-B",
		"X-Hop-C",
		"Set-Cookie",
		"x-cpa-trace-id",
		"Access-Control-Expose-Headers",
	}
	for _, key := range blockedHeaderKeys {
		value := filtered.Get(key)
		if value != "" {
			t.Fatalf("expected %s to be removed, got %q", key, value)
		}
	}
}

func TestNativeClaudeDownstreamHeadersBypassGenericFilter(t *testing.T) {
	ctx := coreexecutor.WithNativeClaudeProtocolHeaders(context.Background(), http.Header{"Anthropic-Version": {"2023-06-01"}})
	src := http.Header{
		"Content-Encoding":          {"gzip"},
		"Content-Length":            {"123"},
		"Set-Cookie":                {"cookie-a", "cookie-b"},
		"Authorization":             {"upstream-response-value"},
		"X-Litellm-Future":          {"preserved"},
		"X-Anthropic-Future-Header": {"one", "two"},
		"Connection":                {"X-Hop"},
		"X-Hop":                     {"preserve-until-final-boundary"},
	}

	for name, got := range map[string]http.Header{
		"executor":    downstreamHeadersFromExecutor(ctx, src, false),
		"interceptor": downstreamHeadersAfterInterceptors(ctx, src, src, false),
	} {
		if !reflect.DeepEqual(got, src) {
			t.Errorf("%s headers = %v, want %v", name, got, src)
		}
		got.Set("X-Anthropic-Future-Header", "changed")
		if src.Values("X-Anthropic-Future-Header")[0] != "one" {
			t.Errorf("%s result aliases source", name)
		}
	}
}

func TestFilterUpstreamHeaders_ReturnsNilWhenAllHeadersBlocked(t *testing.T) {
	src := http.Header{}
	src.Add("Connection", "x-hop-a")
	src.Set("X-Hop-A", "a")
	src.Set("Set-Cookie", "session=secret")

	filtered := FilterUpstreamHeaders(src)
	if filtered != nil {
		t.Fatalf("expected nil when all headers are filtered, got %#v", filtered)
	}
}
