// Package cursorcomposer resolves Cursor Composer upstream transport settings.
package cursorcomposer

import (
	"os"
	"strings"
)

const (
	// DefaultBackendBase is the public Cursor API host used for Composer connect-proto chat.
	// See https://cursor.com/docs/enterprise/network-configuration and community protocol notes.
	DefaultBackendBase = "https://api2.cursor.sh"

	// DefaultChatEndpoint is the Connect-RPC path for unified Composer chat streaming.
	DefaultChatEndpoint = "/aiserver.v1.ChatService/StreamUnifiedChatWithTools"

	// DefaultClientVersion matches the IDE profile used by composer-api reference clients.
	DefaultClientVersion = "2.6.22"
)

// ResolveBackendBase returns the configured backend base URL, then CURSOR_BACKEND_BASE_URL,
// then DefaultBackendBase.
func ResolveBackendBase(configured string) string {
	if v := strings.TrimSpace(configured); v != "" {
		return strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(os.Getenv("CURSOR_BACKEND_BASE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	if v := macCursorAPIBackendBase(); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultBackendBase
}

// ResolveChatEndpoint returns the configured chat endpoint path, then CURSOR_CHAT_ENDPOINT,
// then DefaultChatEndpoint.
func ResolveChatEndpoint(configured string) string {
	if v := strings.TrimSpace(configured); v != "" {
		return strings.TrimLeft(v, "/")
	}
	if v := strings.TrimSpace(os.Getenv("CURSOR_CHAT_ENDPOINT")); v != "" {
		return strings.TrimLeft(v, "/")
	}
	if v := macCursorAPIChatEndpoint(); v != "" {
		return strings.TrimLeft(v, "/")
	}
	return strings.TrimLeft(DefaultChatEndpoint, "/")
}

// ResolveClientVersion returns the configured client version, then CURSOR_CLIENT_VERSION,
// then DefaultClientVersion.
func ResolveClientVersion(configured string) string {
	if v := strings.TrimSpace(configured); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("CURSOR_CLIENT_VERSION")); v != "" {
		return v
	}
	if v := macCursorAPIClientVersion(); v != "" {
		return v
	}
	return DefaultClientVersion
}
