package metrics

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestMetricsIntegration(t *testing.T) {
	// Temporary database file for the integration test
	dbPath := "./test_integration.db"
	// Ensure the test database is removed after the test completes (success or failure)
	defer os.Remove(dbPath)

	// Create a new MetricsStore instance using the temporary SQLite database
	store, err := NewMetricsStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create metrics store: %v", err)
	}
	// Close the store connection when the test finishes
	defer store.Close()

	// Subtest: Verify basic functionality with multiple different models
	t.Run("MultipleModels", func(t *testing.T) {
		// Prepare three sample usage metrics with different models and timestamps
		metrics := []UsageMetric{
			{
				Timestamp:        time.Now().Add(-3 * time.Hour), // 3 hours ago
				Model:            "gpt-4",
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
				Status:           "200",
				LatencyMs:        300,
			},
			{
				Timestamp:        time.Now().Add(-2 * time.Hour), // 2 hours ago
				Model:            "gpt-3.5-turbo",
				PromptTokens:     50,
				CompletionTokens: 25,
				TotalTokens:      75,
				Status:           "200",
				LatencyMs:        150,
			},
			{
				Timestamp:        time.Now().Add(-1 * time.Hour), // 1 hour ago
				Model:            "gpt-4",
				PromptTokens:     200,
				CompletionTokens: 100,
				TotalTokens:      300,
				Status:           "200",
				LatencyMs:        500,
			},
		}

		// Record each metric into the store
		for i, m := range metrics {
			if err := store.RecordUsage(context.Background(), m); err != nil {
				t.Fatalf("failed to record metric %d: %v", i, err)
			}
		}

		t.Log("✅ Successfully recorded 3 metrics")

		// Verify overall totals across all recorded metrics
		totals, err := store.GetTotals(context.Background(), MetricsQuery{})
		if err != nil {
			t.Fatalf("failed to get totals: %v", err)
		}

		// Expected total tokens: 150 (gpt-4) + 75 (gpt-3.5) + 300 (gpt-4) = 525
		expectedTokens := int64(525)
		if totals.TotalTokens != expectedTokens {
			t.Errorf("expected total tokens %d, got %d", expectedTokens, totals.TotalTokens)
		}

		t.Logf("✅ Total tokens across all models: %d", totals.TotalTokens)
		t.Logf("✅ Total number of requests: %d", totals.TotalRequests)

		// Verify aggregation grouped by model
		models, err := store.GetByModel(context.Background(), MetricsQuery{})
		if err != nil {
			t.Fatalf("failed to get metrics by model: %v", err)
		}

		// We expect exactly two distinct models: gpt-4 and gpt-3.5-turbo
		if len(models) != 2 {
			t.Errorf("expected 2 distinct models, got %d", len(models))
		}

		// Log details for each model (tokens used, request count, average latency)
		for _, model := range models {
			t.Logf("Model: %s | Tokens: %d | Requests: %d | Avg Latency: %d ms",
				model.Model, model.Tokens, model.Requests, model.AvgLatencyMs)
		}

		// Verify time-series aggregation (hourly buckets in this case)
		timeSeries, err := store.GetTimeSeries(context.Background(), MetricsQuery{}, 1)
		if err != nil {
			t.Fatalf("failed to get time series: %v", err)
		}

		t.Logf("Time series returned %d bucket(s)", len(timeSeries))
		for _, bucket := range timeSeries {
			t.Logf("  Bucket start: %s | Requests: %d | Tokens: %d",
				bucket.BucketStart.Format("2006-01-02 15:04"),
				bucket.Requests, bucket.Tokens)
		}
	})

	// Subtest: Verify filtering by specific model works correctly
	t.Run("FilterByModel", func(t *testing.T) {
		model := "gpt-4"
		query := MetricsQuery{Model: &model}

		totals, err := store.GetTotals(context.Background(), query)
		if err != nil {
			t.Fatalf("failed to get filtered totals: %v", err)
		}

		// Only gpt-4 entries should be counted: 150 + 300 = 450 tokens
		expectedTokens := int64(450)
		if totals.TotalTokens != expectedTokens {
			t.Errorf("expected %d tokens for gpt-4 only, got %d", expectedTokens, totals.TotalTokens)
		}

		t.Logf("✅ Total tokens for gpt-4 model: %d", totals.TotalTokens)
	})

	// Subtest: Verify time-range filtering works as expected
	t.Run("FilterByTimeRange", func(t *testing.T) {
		now := time.Now()
		// Define a window covering the most recent two metrics (-2h and -1h ago)
		twoHoursAgo := now.Add(-2 * time.Hour)

		query := MetricsQuery{
			From: &twoHoursAgo,
			To:   &now,
		}

		totals, err := store.GetTotals(context.Background(), query)
		if err != nil {
			t.Fatalf("failed to get time-range filtered totals: %v", err)
		}

		t.Logf("✅ Requests in the last 2 hours: %d", totals.TotalRequests)
		t.Logf("✅ Tokens consumed in the last 2 hours: %d", totals.TotalTokens)

		// At least the two most recent metrics should fall into this range
		if totals.TotalRequests == 0 {
			t.Error("expected at least one request within the specified time range")
		}
	})
}