package auth

import (
	"strings"
	"testing"
)

func TestSessionAffinityLogIDDistinguishesClaudeSessionsAndAgents(t *testing.T) {
	ids := []string{
		"claude:aaaaaaaa-1111-4111-8111-111111111111",
		"claude:aaaaaaaa-2222-4222-8222-222222222222",
		"claude:aaaaaaaa-1111-4111-8111-111111111111:agent:worker-a",
		"claude:aaaaaaaa-1111-4111-8111-111111111111:agent:worker-b",
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		got := sessionAffinityLogID(id)
		if _, exists := seen[got]; exists {
			t.Fatalf("different session identities share log identity %q", got)
		}
		seen[got] = struct{}{}
		if again := sessionAffinityLogID(id); again != got {
			t.Fatalf("same session identity changed log identity: %q != %q", again, got)
		}
	}
}

func TestSessionAffinityLogIDDoesNotExposeRawIdentity(t *testing.T) {
	for _, id := range []string{"short-secret", "claude:private-session\nforged-log-entry", "会话:private-agent"} {
		t.Run(id, func(t *testing.T) {
			got := sessionAffinityLogID(id)
			if strings.Contains(got, id) || strings.ContainsAny(got, "\r\n") {
				t.Fatalf("log identity exposes input or control characters: %q", got)
			}
		})
	}
}
