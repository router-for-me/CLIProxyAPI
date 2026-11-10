package executor

import (
	"strings"
	"testing"
)

func TestKiroExecutorBuildHeadersUsesSupportedIDEVersion(t *testing.T) {
	exec := NewKiroExecutor(nil)

	t.Run("oauth streaming headers advertise supported Kiro IDE version", func(t *testing.T) {
		headers := exec.BuildHeaders("https://q.us-east-1.amazonaws.com/generateAssistantResponse", "test-token", "idc")

		ua := headers.Get("User-Agent")
		amzUA := headers.Get("X-Amz-User-Agent")

		expectedTag := "KiroIDE-" + KiroDefaultIDEVersion + "-kiro-anonymous"
		if !strings.Contains(ua, expectedTag) {
			t.Fatalf("expected User-Agent to contain %q, got %q", expectedTag, ua)
		}
		if !strings.Contains(amzUA, expectedTag) {
			t.Fatalf("expected X-Amz-User-Agent to contain %q, got %q", expectedTag, amzUA)
		}
		if strings.Contains(ua, "KiroIDE-0.") || strings.Contains(amzUA, "KiroIDE-0.") {
			t.Fatalf("headers must not contain deprecated 0.x KiroIDE version: ua=%q amzUA=%q", ua, amzUA)
		}
		if got := headers.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("expected Authorization Bearer header, got %q", got)
		}
	})

	t.Run("api_key runtime headers advertise supported Kiro IDE version", func(t *testing.T) {
		headers := exec.BuildHeaders("https://runtime.us-east-1.kiro.dev/", "ksk_test", "api_key")

		ua := headers.Get("User-Agent")
		amzUA := headers.Get("X-Amz-User-Agent")

		expectedTag := "KiroIDE-" + KiroDefaultIDEVersion + "-kiro-anonymous"
		if !strings.Contains(ua, expectedTag) {
			t.Fatalf("expected User-Agent to contain %q, got %q", expectedTag, ua)
		}
		if !strings.Contains(amzUA, expectedTag) {
			t.Fatalf("expected X-Amz-User-Agent to contain %q, got %q", expectedTag, amzUA)
		}
		if got := headers.Get("tokentype"); got != "API_KEY" {
			t.Fatalf("expected tokentype API_KEY, got %q", got)
		}
	})
}

