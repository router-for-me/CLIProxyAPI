package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestUsageReporterHomeAdditionalModelAccounting(t *testing.T) {
	for _, mode := range []string{"local", "initial", "reused"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			if mode != "local" {
				ctx = usage.WithHomeSelection(ctx, usage.HomeSelection{
					Reused: mode == "reused", Model: "route-model", CredentialPolicy: "codex-alpha-search-v1",
				})
			}
			reporter := NewUsageReporter(ctx, "codex", "text-model", nil)
			primary := reporter.buildRecord(usage.Detail{TotalTokens: 12}, false)
			image, ok := reporter.buildAdditionalModelRecord("image-model", usage.Detail{TotalTokens: 5})
			if !ok {
				t.Fatal("missing image usage")
			}
			if image.RequestID == "" || image.RequestID == primary.RequestID {
				t.Fatal("primary and image usage must retain independent execution IDs")
			}
			if image.Model != "image-model" || image.Detail.TotalTokens != 5 || primary.Detail.TotalTokens != 12 {
				t.Fatal("scheduling metadata changed billable usage")
			}
			if mode == "local" {
				if primary.HomeSelection != nil || image.HomeSelection != nil {
					t.Fatal("local execution acquired Home accounting metadata")
				}
				return
			}
			if image.HomeSelection == nil || !image.HomeSelection.Additional || image.HomeSelection.Reused != (mode == "reused") {
				t.Fatalf("image accounting metadata=%+v", image.HomeSelection)
			}
			if image.HomeSelection.Model != "route-model" || image.HomeSelection.CredentialPolicy != "codex-alpha-search-v1" {
				t.Fatalf("image lost dispatch scope: %+v", image.HomeSelection)
			}
			if primary.HomeSelection == nil || primary.HomeSelection.Additional || primary.HomeSelection.Reused != (mode == "reused") {
				t.Fatalf("image changed primary accounting: %+v", primary.HomeSelection)
			}
			if usage.HomeSelectionFromContext(ctx).Additional || reporter.buildRecord(usage.Detail{}, false).HomeSelection.Additional {
				t.Fatal("image mutated context or reporter metadata")
			}
		})
	}
}
