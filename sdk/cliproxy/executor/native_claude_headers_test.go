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
		"X-Claude-Code-Session-Id": {"11111111-2222-4333-8444-555555555555"},
		"X-Claude-Code-Agent-Id":   {"tool-agent"}, "X-Claude-Code-Parent-Agent-Id": {"parent-agent"},
		"X-Claude-Code-Prompt-Id":     {"22222222-3333-4444-8555-666666666666"},
		"X-Claude-Code-Request-Class": {"user"}, "X-Claude-Code-Agent-Type": {"custom"},
		"X-Claude-Code-Future-Hint":         {"future-native-value"},
		"X-Claude-Remote-Container-Id":      {"remote-container"},
		"X-Claude-Remote-Session-Id":        {"remote-session"},
		"X-Anthropic-Additional-Protection": {"true"},
		"X-Custom-Identity":                 {"future-end-to-end-value"},
		"X-Future-Native-Protocol":          {"future-value-a", "future-value-b"},
		"X-Stainless-Future-Protocol":       {"future-sdk-value"},
	}
	src := safe.Clone()
	for _, name := range []string{
		"Authorization", "Anthropic-Organization-Id", "Anthropic-User-Profile-Id", "Anthropic-Workspace-Id",
		"Cookie", "Cookie2", "X-Api-Key", "Proxy-Authorization", "X-Claude-Code-Ide-Authorization",
		"Forwarded", "Via", "X-Forwarded-For", "X-Forwarded-Proto", "X-Real-Ip",
		"X-Organization-Uuid", "X-Account-Id", "X-Trusted-Device-Token",
		"Connection", "Content-Length", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
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
	if _, ok := NativeClaudeProtocolHeadersFromContext(nil); ok {
		t.Fatal("a missing context must not opt in")
	}
}

func TestNativeClaudeHeadersExcludeConnectionTokensCaseInsensitively(t *testing.T) {
	header := http.Header{
		"connection": {" x-app, X-STAINLESS-LANG ,, "}, "x-app": {"secret"},
		"X-Stainless-Lang": {"secret"}, "anthropic-beta": {"a", "b"},
	}
	if got := NativeClaudeProtocolHeaders(header); !reflect.DeepEqual(got, http.Header{"Anthropic-Beta": {"a", "b"}}) {
		t.Fatalf("connection-scoped headers survived: %v", got)
	}
}

func TestNativeClaudeUpstreamSuccessRequiresTrustedNativeContext(t *testing.T) {
	var calls int
	callback := func(ctx context.Context, authID string) {
		if ctx == nil || authID != "selected-account" {
			t.Fatal("success observer lost execution context or selected credential")
		}
		calls++
	}
	ordinary := WithNativeClaudeUpstreamSuccessCallback(context.Background(), callback)
	NotifyNativeClaudeUpstreamSuccess(nil, "selected-account")
	NotifyNativeClaudeUpstreamSuccess(ordinary, "selected-account")
	native := WithNativeClaudeProtocolHeaders(ordinary, http.Header{})
	NotifyNativeClaudeUpstreamSuccess(native, "")
	NotifyNativeClaudeUpstreamSuccess(WithNativeClaudeUpstreamSuccessCallback(native, nil), "selected-account")
	if calls != 0 {
		t.Fatal("ordinary or incomplete context established a native conversation origin")
	}
	NotifyNativeClaudeUpstreamSuccess(native, "selected-account")
	if calls != 1 {
		t.Fatal("trusted native success was not observed exactly once")
	}
}

func TestNativeClaudeResponseStatusHolderBoundary(t *testing.T) {
	ordinary := WithNativeClaudeResponseStatusHolder(context.Background())
	SetNativeClaudeResponseStatus(ordinary, http.StatusCreated)
	SetNativeClaudeResponseStatus(nil, http.StatusCreated)
	if NativeClaudeResponseStatusFromContext(ordinary) != 0 || NativeClaudeResponseStatusFromContext(nil) != 0 {
		t.Fatal("ordinary execution captured a native upstream status")
	}
	native := WithNativeClaudeResponseStatusHolder(WithNativeClaudeProtocolHeaders(context.Background(), http.Header{}))
	for _, status := range []int{http.StatusCreated, http.StatusAccepted, http.StatusNoContent} {
		SetNativeClaudeResponseStatus(native, status)
		if got := NativeClaudeResponseStatusFromContext(native); got != status {
			t.Fatalf("successful native status=%d want=%d", got, status)
		}
	}
	SetNativeClaudeResponseStatus(native, http.StatusTooManyRequests)
	if NativeClaudeResponseStatusFromContext(native) != http.StatusNoContent {
		t.Fatal("an error replaced the accepted native response status")
	}
	fresh := WithNativeClaudeResponseStatusHolder(native)
	if NativeClaudeResponseStatusFromContext(fresh) != 0 || NativeClaudeResponseStatusFromContext(native) != http.StatusNoContent {
		t.Fatal("a new request inherited or reset another request's native status")
	}
}
