package redisqueue

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestUsageQueuePreservesPluginUnknownAndExplicitZero(t *testing.T) {
	for _, measured := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "measured-zero"}[measured], func(t *testing.T) {
			withEnabledQueue(t, func() {
				ctx := internallogging.WithResponseStatusHolder(context.Background())
				internallogging.SetResponseStatus(ctx, http.StatusOK)
				breakdown := coreusage.NewUnobservedTokenBreakdown()
				quality := coreusage.TokenAccountingQualityUnclassified
				if measured {
					breakdown = coreusage.WithTokenUsagePresence(coreusage.NewSubsetTokenBreakdown(0, 0, 0, 0, 0, 0), coreusage.TokenUsagePresence{InputTokens: true, OutputTokens: true, CacheReadTokens: true, CacheWriteTokens: true, ReasoningTokens: true, TotalTokens: true})
					quality = coreusage.TokenAccountingQualityComplete
				}
				plugin := &usageQueuePlugin{}
				plugin.HandleUsage(ctx, coreusage.Record{Provider: "plugin-presence", Detail: coreusage.Detail{TokenBreakdown: breakdown}})
				payload := popSinglePayload(t)
				requireTokenBreakdown(t, payload, quality, 0)
				requireTokensBoolField(t, payload, "cache_read_tokens_present", measured)
				var saved coreusage.TokenBreakdown
				if err := json.Unmarshal(payload["token_breakdown"], &saved); err != nil {
					t.Fatal(err)
				}
				if saved.Observed == nil || saved.Observed.InputTokens != measured || saved.Observed.ReasoningTokens != measured {
					t.Fatalf("measurement presence lost at queue: %+v", saved)
				}
			})
		})
	}
}
