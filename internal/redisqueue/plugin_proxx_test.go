package redisqueue

import (
	"context"
	"net/http"
	"testing"

	"github.com/MoeForge/moego/ojson"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// proxx: the queue payload must carry the resolved cost (ADR 0004).
func TestUsageQueuePluginPayloadCarriesProxxCost(t *testing.T) {
	withEnabledQueue(t, func() {
		ctx := internallogging.WithResponseStatusHolder(context.Background())
		internallogging.SetResponseStatus(ctx, http.StatusOK)
		(&usageQueuePlugin{}).HandleUsage(ctx, coreusage.Record{
			Provider: "openai",
			Model:    "gpt-5",
			Detail: coreusage.Detail{
				InputTokens: 1,
				TotalTokens: 1,
				CostDetails: &coreusage.CostDetails{FinalCost: coreusage.Float64Ptr(0.25), CostSource: "catalog"},
			},
		})
		payload := popSinglePayload(t)
		raw := payload["cost_details"]
		if got := ojson.GetBytes(raw, "final_cost").Float(); got != 0.25 {
			t.Fatalf("cost_details.final_cost = %v (%s)", got, raw)
		}
		if got := ojson.GetBytes(raw, "cost_source").String(); got != "catalog" {
			t.Fatalf("cost_details.cost_source = %q (%s)", got, raw)
		}
	})
}
