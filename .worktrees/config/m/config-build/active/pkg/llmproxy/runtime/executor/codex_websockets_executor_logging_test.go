package executor

import (
	"strings"
	"testing"
)

func TestSanitizeCodexWebsocketLogURLMasksQueryAndUserInfo(t *testing.T) {
	raw := "wss://user:secret@example.com/v1/realtime?api_key=verysecret&token=abc123&foo=bar#frag"
	got := sanitizeCodexWebsocketLogURL(raw)

	if strings.Contains(got, "secret") || strings.Contains(got, "abc123") || strings.Contains(got, "verysecret") {
		t.Fatalf("expected sensitive values to be masked, got %q", got)
	}
	if strings.Contains(got, "user:") {
		t.Fatalf("expected userinfo to be removed, got %q", got)
	}
	if strings.Contains(got, "#frag") {
		t.Fatalf("expected fragment to be removed, got %q", got)
	}
}

func TestSanitizeCodexWebsocketLogFieldMasksTokenLikeValue(t *testing.T) {
	got := sanitizeCodexWebsocketLogField("  sk-super-secret-token  ")
	if got == "sk-super-secret-token" {
		t.Fatalf("expected auth field to be masked, got %q", got)
	}
}
