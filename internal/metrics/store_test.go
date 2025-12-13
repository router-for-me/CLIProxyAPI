package metrics

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestMetricsStore(t *testing.T) {
	dbPath := "./test_metrics.db"
	defer os.Remove(dbPath)

	store, err := NewMetricsStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	metric := UsageMetric{
		Timestamp:        time.Now(),
		Model:            "gpt-4",
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		Status:           "success",
	}

	if err := store.RecordUsage(context.Background(), metric); err != nil {
		t.Fatalf("failed to record usage: %v", err)
	}

	totals, err := store.GetTotals(context.Background(), MetricsQuery{})
	if err != nil {
		t.Fatalf("failed to get totals: %v", err)
	}

	if totals.TotalTokens != 150 {
		t.Errorf("expected 150 tokens, got %d", totals.TotalTokens)
	}

	if totals.TotalRequests != 1 {
		t.Errorf("expected 1 request, got %d", totals.TotalRequests)
	}
}