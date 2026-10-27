package redisqueue

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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

func TestUsageQueueSerializesIndependentRunAccounting(t *testing.T) {
	for _, test := range []string{"record", "detail", "absent", "invalid"} {
		t.Run(test, func(t *testing.T) {
			withEnabledQueue(t, func() {
				ctx := internallogging.WithResponseStatusHolder(context.Background())
				internallogging.SetResponseStatus(ctx, http.StatusOK)
				ref := &coreusage.UsageAccounting{Provider: "cursor-next", Scope: "run", RecordID: strings.Repeat("c", 64), State: "pending", Replay: true, Resumed: true}
				record := coreusage.Record{Provider: "cursor", Detail: coreusage.Detail{TokenBreakdown: coreusage.NewUnobservedTokenBreakdown()}}
				switch test {
				case "record":
					record.UsageAccounting = ref
				case "detail":
					record.Detail.UsageAccounting = ref
				case "invalid":
					ref.Scope = "request"
					record.UsageAccounting = ref
				}
				(&usageQueuePlugin{}).HandleUsage(ctx, record)
				payload := popSinglePayload(t)
				want := test == "record" || test == "detail"
				if _, ok := payload["usage_accounting"]; ok != want {
					t.Fatalf("queue reference presence=%v, want %v", ok, want)
				}
				if want {
					var saved coreusage.UsageAccounting
					if err := json.Unmarshal(payload["usage_accounting"], &saved); err != nil || saved != *ref {
						t.Fatalf("queue reference mismatch: %+v err=%v", saved, err)
					}
				}
				requireTokenBreakdown(t, payload, coreusage.TokenAccountingQualityUnclassified, 0)
				requireTokensBoolField(t, payload, "cache_read_tokens_present", false)
			})
		})
	}
}
