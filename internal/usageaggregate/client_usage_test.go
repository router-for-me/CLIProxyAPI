package usageaggregate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestClientUsageAttributionAndRestore(t *testing.T) {
	a := &aggregator{}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a.HandleUsage(context.Background(), coreusage.Record{APIKey: "test-client-secret", Model: "upstream", Alias: "requested", Failed: true, RequestedAt: now})
	snapshot := a.snapshot()
	if len(snapshot.ClientKeys) != 1 || snapshot.ClientKeys[0].Failures != 1 || snapshot.ClientKeys[0].Model != "requested" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "test-client-secret") {
		t.Fatal("raw key leaked")
	}
	restored := &aggregator{}
	restored.restore(snapshot)
	restored.HandleUsage(context.Background(), coreusage.Record{APIKey: "test-client-secret", Model: "requested", RequestedAt: now.Add(-time.Hour)})
	row := restored.snapshot().ClientKeys[0]
	if row.Requests != 2 || row.Failures != 1 || !row.LastUsed.Equal(now) {
		t.Fatalf("unexpected restored row: %+v", row)
	}
}
