package redisqueue

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
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

func TestResolvedFailureMarksUsageIncomplete(t *testing.T) {
	withEnabledQueue(t, func() {
		ctx := logging.WithResponseStatusHolder(context.Background())
		logging.SetResponseStatus(ctx, 500)
		(&usageQueuePlugin{}).HandleUsage(ctx, coreusage.Record{Provider: "claude", Model: "claude-opus-5", Detail: coreusage.Detail{InputTokens: 100, OutputTokens: 1, TotalTokens: 101, UsageObserved: true}})
		payload := popSinglePayload(t)
		if string(payload["failed"]) != "true" || string(payload["usage_complete"]) != "false" {
			t.Fatalf("inconsistent completeness: failed=%s complete=%s", payload["failed"], payload["usage_complete"])
		}
	})
}
