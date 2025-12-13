package metrics

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestMetricsIntegration(t *testing.T) {
	dbPath := "./test_integration.db"
	defer os.Remove(dbPath)

	store, err := NewMetricsStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	t.Run("MultipleModels", func(t *testing.T) {
		metrics := []UsageMetric{
			{
				Timestamp:        time.Now().Add(-3 * time.Hour),
				Model:            "gpt-4",
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
				Status:           "200",
				LatencyMs:        300,
			},
			{
				Timestamp:        time.Now().Add(-2 * time.Hour),
				Model:            "gpt-3.5-turbo",
				PromptTokens:     50,
				CompletionTokens: 25,
				TotalTokens:      75,
				Status:           "200",
				LatencyMs:        150,
			},
			{
				Timestamp:        time.Now().Add(-1 * time.Hour),
				Model:            "gpt-4",
				PromptTokens:     200,
				CompletionTokens: 100,
				TotalTokens:      300,
				Status:           "200",
				LatencyMs:        500,
			},
		}

		for i, m := range metrics {
			if err := store.RecordUsage(context.Background(), m); err != nil {
				t.Fatalf("failed to record metric %d: %v", i, err)
			}
		}

		t.Log("✅ Recorded 3 metrics")

		// Проверка общих итогов
		totals, err := store.GetTotals(context.Background(), MetricsQuery{})
		if err != nil {
			t.Fatalf("failed to get totals: %v", err)
		}

		expectedTokens := int64(525) // 150 + 75 + 300
		if totals.TotalTokens != expectedTokens {
			t.Errorf("expected %d tokens, got %d", expectedTokens, totals.TotalTokens)
		}

		t.Logf("✅ Total tokens: %d", totals.TotalTokens)
		t.Logf("✅ Total requests: %d", totals.TotalRequests)

		// Проверка по моделям
		models, err := store.GetByModel(context.Background(), MetricsQuery{})
		if err != nil {
			t.Fatalf("failed to get by model: %v", err)
		}

		if len(models) != 2 {
			t.Errorf("expected 2 models, got %d", len(models))
		}

		for _, model := range models {
			t.Logf("Model: %s, Tokens: %d, Requests: %d, Avg Latency: %dms",
				model.Model, model.Tokens, model.Requests, model.AvgLatencyMs)
		}

		// Проверка временных рядов
		timeSeries, err := store.GetTimeSeries(context.Background(), MetricsQuery{}, 1)
		if err != nil {
			t.Fatalf("failed to get time series: %v", err)
		}

		t.Logf("Time series buckets: %d", len(timeSeries))
		for _, bucket := range timeSeries {
			t.Logf("  Bucket: %s, Requests: %d, Tokens: %d",
				bucket.BucketStart.Format("2006-01-02 15:04"),
				bucket.Requests, bucket.Tokens)
		}
	})

	t.Run("FilterByModel", func(t *testing.T) {
		model := "gpt-4"
		query := MetricsQuery{Model: &model}

		totals, err := store.GetTotals(context.Background(), query)
		if err != nil {
			t.Fatalf("failed to get filtered totals: %v", err)
		}

		expectedTokens := int64(450) // 150 + 300 (только gpt-4)
		if totals.TotalTokens != expectedTokens {
			t.Errorf("expected %d tokens for gpt-4, got %d", expectedTokens, totals.TotalTokens)
		}

		t.Logf("✅ GPT-4 total tokens: %d", totals.TotalTokens)
	})

	t.Run("FilterByTimeRange", func(t *testing.T) {
		now := time.Now()
		twoHoursAgo := now.Add(-2 * time.Hour)

		query := MetricsQuery{
			From: &twoHoursAgo,
			To:   &now,
		}

		totals, err := store.GetTotals(context.Background(), query)
		if err != nil {
			t.Fatalf("failed to get time-filtered totals: %v", err)
		}

		t.Logf("✅ Requests in last 2 hours: %d", totals.TotalRequests)
		t.Logf("✅ Tokens in last 2 hours: %d", totals.TotalTokens)

		if totals.TotalRequests == 0 {
			t.Error("expected at least some requests in time range")
		}
	})
}