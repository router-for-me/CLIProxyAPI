package redisqueue

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestHomePluginUsageSurvivesDisabledLocalStatistics(t *testing.T) {
	withEnabledQueue(t, func() {
		SetUsageStatisticsEnabled(false)
		(&usageQueuePlugin{}).HandleUsage(t.Context(), coreusage.Record{SelectionID: "selection", RequestID: "execution", AuthID: "auth", BaseURL: "https://example.invalid", HomeSelection: &coreusage.HomeSelection{}})
		items := PopOldest(1)
		if len(items) != 1 || ojson.GetBytes(items[0], "selection_id").String() != "selection" || ojson.GetBytes(items[0], "execution_id").String() != "execution" || ojson.GetBytes(items[0], "auth_id").String() != "auth" || ojson.GetBytes(items[0], "base_url").String() != "https://example.invalid" {
			t.Fatalf("feedback=%s", items)
		}
		(&usageQueuePlugin{}).HandleUsage(t.Context(), coreusage.Record{SelectionID: "local-selection"})
		if items := PopOldest(1); len(items) != 0 {
			t.Fatalf("disabled standalone statistics unexpectedly enqueued: %s", items)
		}
	})
}

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
				SelectionID: "scheduler-selection-id",
			})
			items := PopOldest(1)
			if len(items) != 1 {
				t.Fatalf("queued=%d", len(items))
			}
			if got := ojson.GetBytes(items[0], "selection_id").String(); got != "scheduler-selection-id" {
				t.Fatalf("usage lost scheduler identity: %s", items[0])
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
