package usage

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestRequestStatisticsSnapshotTracksFailovers(t *testing.T) {
	previous := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() {
		SetStatisticsEnabled(previous)
	})

	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		Provider:       "gemini",
		Model:          "gemini-2.5-pro",
		RequestedModel: "gemini-2.5-pro",
		ActualModel:    "gemini-2.0-flash",
		APIKey:         "test-key",
		RequestedAt:    time.Date(2026, time.March, 22, 12, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 5,
			TotalTokens:  15,
		},
	})

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("TotalRequests = %d, want 1", snapshot.TotalRequests)
	}
	if snapshot.TotalFailovers != 1 {
		t.Fatalf("TotalFailovers = %d, want 1", snapshot.TotalFailovers)
	}

	apiSnapshot, ok := snapshot.APIs["test-key"]
	if !ok {
		t.Fatal("expected API snapshot for test-key")
	}
	if apiSnapshot.TotalFailovers != 1 {
		t.Fatalf("APISnapshot.TotalFailovers = %d, want 1", apiSnapshot.TotalFailovers)
	}

	modelSnapshot, ok := apiSnapshot.Models["gemini-2.5-pro"]
	if !ok {
		t.Fatal("expected model snapshot for gemini-2.5-pro")
	}
	if len(modelSnapshot.Details) != 1 {
		t.Fatalf("detail count = %d, want 1", len(modelSnapshot.Details))
	}
	if got := modelSnapshot.Details[0].RequestedModel; got != "gemini-2.5-pro" {
		t.Fatalf("RequestedModel = %q, want %q", got, "gemini-2.5-pro")
	}
	if got := modelSnapshot.Details[0].ActualModel; got != "gemini-2.0-flash" {
		t.Fatalf("ActualModel = %q, want %q", got, "gemini-2.0-flash")
	}
}
