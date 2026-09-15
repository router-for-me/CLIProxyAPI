package configsnapshot

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func TestMapResourcesRejectsNilDependencies(t *testing.T) {
	_, err := MapResources(context.Background(), nil, nil, nil, &config.Config{})
	if err == nil {
		t.Fatal("expected nil dependency error")
	}
}

func TestImportReportCounts(t *testing.T) {
	var report ImportReport
	report.Add("upstream_providers", "created")
	report.Add("upstream_providers", "updated")
	if report.Created("upstream_providers") != 1 || report.Updated("upstream_providers") != 1 {
		t.Fatalf("unexpected report counts: %#v", report.Counts)
	}
}

// Keep the store import in the test contract so the intended public API remains explicit.
var _ store.UpstreamProviderStore
