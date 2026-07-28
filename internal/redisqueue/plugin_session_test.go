package redisqueue

import (
	"context"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageQueuePluginPayloadIncludesSessionFields(t *testing.T) {
	withEnabledQueue(t, func() {
		plugin := &usageQueuePlugin{}
		plugin.HandleUsage(context.Background(), coreusage.Record{
			Provider: "openai",
			Model:    "gpt-5.4",
			Session: coreusage.SessionInfo{
				ID:             "codex:019f7e3b",
				Source:         "client_native_header",
				Confidence:     "high",
				Scope:          "session",
				ClientType:     "codex",
				ThreadID:       "thread-1",
				ParentThreadID: "thread-0",
				RequestKind:    "compact",
				ThreadSource:   "subagent",
				TurnID:         "019fa90b-3234-73d2-a061-0622e5f8c57c",
			},
		})

		payload := popSinglePayload(t)
		requireStringField(t, payload, "session_id", "codex:019f7e3b")
		requireStringField(t, payload, "session_source", "client_native_header")
		requireStringField(t, payload, "session_confidence", "high")
		requireStringField(t, payload, "session_scope", "session")
		requireStringField(t, payload, "client_type", "codex")
		requireStringField(t, payload, "thread_id", "thread-1")
		requireStringField(t, payload, "parent_thread_id", "thread-0")
		// Home folds housekeeping requests out of a session timeline using these.
		requireStringField(t, payload, "request_kind", "compact")
		requireStringField(t, payload, "thread_source", "subagent")
		requireStringField(t, payload, "turn_id", "019fa90b-3234-73d2-a061-0622e5f8c57c")
	})
}

// Executors that build their reporter before the identity is known still get the
// session from the request context.
func TestUsageQueuePluginFallsBackToContextSession(t *testing.T) {
	withEnabledQueue(t, func() {
		ctx := coreusage.WithSessionInfo(context.Background(), coreusage.SessionInfo{
			ID:         "claude:cc-1",
			Source:     "client_native_header",
			Confidence: "high",
			Scope:      "session",
			ClientType: "claude-code",
		})

		plugin := &usageQueuePlugin{}
		plugin.HandleUsage(ctx, coreusage.Record{Provider: "claude", Model: "claude-test"})

		payload := popSinglePayload(t)
		requireStringField(t, payload, "session_id", "claude:cc-1")
		requireStringField(t, payload, "client_type", "claude-code")
	})
}

func TestUsageQueuePluginOmitsSessionFieldsWhenUnresolved(t *testing.T) {
	withEnabledQueue(t, func() {
		plugin := &usageQueuePlugin{}
		plugin.HandleUsage(context.Background(), coreusage.Record{Provider: "openai", Model: "gpt-5.4"})

		payload := popSinglePayload(t)
		for _, field := range []string{
			"session_id", "session_source", "session_confidence",
			"session_scope", "client_type", "thread_id", "parent_thread_id",
			"request_kind", "thread_source", "turn_id",
		} {
			requireMissingField(t, payload, field)
		}
	})
}
