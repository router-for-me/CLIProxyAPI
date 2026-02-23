package watcher

import (
	"strings"
	"testing"
)

func TestRedactedConfigChangeLogLines(t *testing.T) {
	lines := redactedConfigChangeLogLines([]string{
		"api-key: sk-live-abc123",
		"oauth-token: bearer secret",
	})
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	for _, line := range lines {
		if strings.Contains(line, "sk-live-abc123") || strings.Contains(line, "secret") {
			t.Fatalf("sensitive content leaked in redacted line: %q", line)
		}
		if !strings.Contains(line, "redacted") {
			t.Fatalf("expected redacted marker in line: %q", line)
		}
	}
}

func TestClientReloadSummary(t *testing.T) {
	got := clientReloadSummary(9, 4, 5)
	if !strings.Contains(got, "9 clients") {
		t.Fatalf("expected total client count, got %q", got)
	}
	if !strings.Contains(got, "4 auth files") {
		t.Fatalf("expected auth file count, got %q", got)
	}
	if !strings.Contains(got, "5 static credential clients") {
		t.Fatalf("expected static credential count, got %q", got)
	}
	if strings.Contains(strings.ToLower(got), "api key") {
		t.Fatalf("summary should not mention api keys directly: %q", got)
	}
}
