package redisqueue

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestUsageQueueCarriesHomeSelection(t *testing.T) {
	withEnabledQueue(t, func() {
		for _, selection := range []*coreusage.HomeSelection{
			nil,
			{Model: "route-model"},
			{Additional: true, Model: "route-model"},
			{Reused: true, Additional: true, Model: "route-model"},
			{Reused: true, Model: "route-model", CredentialPolicy: "codex-alpha-search-v1"},
		} {
			(&usageQueuePlugin{}).HandleUsage(t.Context(), coreusage.Record{
				RequestID: "response-unique-id", Provider: "codex", Model: "upstream-model", HomeSelection: selection,
			})
			items := PopOldest(1)
			if len(items) != 1 {
				t.Fatalf("queued=%d", len(items))
			}
			got := ojson.GetBytes(items[0], "home_selection")
			if selection == nil {
				if got.Exists() {
					t.Fatal("legacy usage acquired a Home marker")
				}
				continue
			}
			if got.Get("reused").Bool() != selection.Reused || got.Get("additional").Bool() != selection.Additional || got.Get("model").String() != selection.Model || got.Get("credential_policy").String() != selection.CredentialPolicy {
				t.Fatalf("lost selection metadata: %s", got.Raw)
			}
			if ojson.GetBytes(items[0], "execution_id").String() != "response-unique-id" {
				t.Fatal("usage lost the per-response replay identity")
			}
		}
	})
}
