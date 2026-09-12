package redisqueue

import (
	"context"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestFailedPartialUsageIsNotPublishedAsComplete(t *testing.T) {
	withEnabledQueue(t, func() {
		(&usageQueuePlugin{}).HandleUsage(context.Background(), coreusage.Record{Provider: "claude", Model: "claude-opus-5", Failed: true, Detail: coreusage.Detail{InputTokens: 100, OutputTokens: 1, TotalTokens: 101, UsageObserved: true}})
		payload := popSinglePayload(t)
		if string(payload["usage_complete"]) != "false" {
			t.Fatalf("partial usage advertised as complete: %v", payload["usage_complete"])
		}
	})
}
