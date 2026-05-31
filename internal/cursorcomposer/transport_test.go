package cursorcomposer

import (
	"strings"
	"testing"
)

func TestResolveBackendBase(t *testing.T) {
	t.Setenv("CURSOR_BACKEND_BASE_URL", "")
	if got := ResolveBackendBase("https://custom.example/"); got != "https://custom.example" {
		t.Fatalf("configured backend = %q, want https://custom.example", got)
	}
	t.Setenv("CURSOR_BACKEND_BASE_URL", "https://env.example")
	if got := ResolveBackendBase(""); got != "https://env.example" {
		t.Fatalf("env backend = %q, want https://env.example", got)
	}
	t.Setenv("CURSOR_BACKEND_BASE_URL", "")
	if got := ResolveBackendBase(""); got != DefaultBackendBase {
		t.Fatalf("default backend = %q, want %s", got, DefaultBackendBase)
	}
}

func TestResolveChatEndpoint(t *testing.T) {
	t.Setenv("CURSOR_CHAT_ENDPOINT", "")
	if got := ResolveChatEndpoint("/custom.Chat/Run"); got != "custom.Chat/Run" {
		t.Fatalf("configured endpoint = %q, want custom.Chat/Run", got)
	}
	t.Setenv("CURSOR_CHAT_ENDPOINT", "/env.Chat/Run")
	if got := ResolveChatEndpoint(""); got != "env.Chat/Run" {
		t.Fatalf("env endpoint = %q, want env.Chat/Run", got)
	}
	t.Setenv("CURSOR_CHAT_ENDPOINT", "")
	if got := ResolveChatEndpoint(""); got != strings.TrimLeft(DefaultChatEndpoint, "/") {
		t.Fatalf("default endpoint = %q", got)
	}
}

func TestResolveClientVersion(t *testing.T) {
	t.Setenv("CURSOR_CLIENT_VERSION", "")
	if got := ResolveClientVersion("9.9.9"); got != "9.9.9" {
		t.Fatalf("configured version = %q", got)
	}
	t.Setenv("CURSOR_CLIENT_VERSION", "1.2.3")
	if got := ResolveClientVersion(""); got != "1.2.3" {
		t.Fatalf("env version = %q", got)
	}
	t.Setenv("CURSOR_CLIENT_VERSION", "")
	if got := ResolveClientVersion(""); got != DefaultClientVersion {
		t.Fatalf("default version = %q", got)
	}
}
