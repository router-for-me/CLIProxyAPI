package executor

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

func TestNativeClaudeProtocolHeadersBoundary(t *testing.T) {
	safe := http.Header{
		"User-Agent": {"claude-cli/2.1.270 (external, sdk-cli)"},
		"Accept":     {"application/json"}, "Accept-Encoding": {"gzip, deflate, br, zstd"},
		"Anthropic-Beta": {"native-beta-a", "native-beta-b"}, "Anthropic-Version": {"2023-06-01"},
		"X-Stainless-Os": {"MacOS"}, "X-Stainless-Arch": {"arm64"},
		"X-Stainless-Package-Version": {"0.112.1"}, "X-Stainless-Runtime-Version": {"v26.3.0"},
		"X-Stainless-Async": {"async"}, "X-Client-Request-Id": {"per-request-correlation"},
		"X-Claude-Code-Agent-Id": {"tool-agent"}, "X-Claude-Code-Parent-Agent-Id": {"parent-agent"},
	}
	src := safe.Clone()
	for _, name := range []string{"Authorization", "Cookie", "X-Api-Key", "Proxy-Authorization", "X-Organization-Uuid", "X-Account-Id", "X-Forwarded-For", "X-Claude-Code-Session-Id", "X-Claude-Remote-Container-Id", "X-Claude-Remote-Session-Id", "X-Anthropic-Additional-Protection", "X-Unknown", "X-Stainless-Future-Secret", "X-Trusted-Device-Token", "Content-Length"} {
		src.Set(name, "private-canary")
	}
	if got := NativeClaudeProtocolHeaders(src); !reflect.DeepEqual(got, safe) {
		t.Fatalf("protocol boundary mismatch: got %v want %v", got, safe)
	}
	ctx := WithNativeClaudeProtocolHeaders(context.Background(), src)
	src.Set("User-Agent", "changed")
	first, ok := NativeClaudeProtocolHeadersFromContext(ctx)
	if !ok || !reflect.DeepEqual(first, safe) {
		t.Fatal("context did not retain an independent sanitized snapshot")
	}
	first.Set("User-Agent", "changed-again")
	second, _ := NativeClaudeProtocolHeadersFromContext(ctx)
	if !reflect.DeepEqual(second, safe) {
		t.Fatal("caller mutated the shared snapshot")
	}
	if _, ok := NativeClaudeProtocolHeadersFromContext(context.Background()); ok {
		t.Fatal("ordinary API callers must not opt in")
	}
}

func TestNativeClaudeHeadersExcludeConnectionTokensCaseInsensitively(t *testing.T) {
	header := http.Header{
		"connection": {" x-app, X-STAINLESS-LANG "}, "x-app": {"secret"},
		"X-Stainless-Lang": {"secret"}, "anthropic-beta": {"a", "b"},
	}
	if got := NativeClaudeProtocolHeaders(header); !reflect.DeepEqual(got, http.Header{"Anthropic-Beta": {"a", "b"}}) {
		t.Fatalf("connection-scoped headers survived: %v", got)
	}
}
