package metrics

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestMetricsStore(t *testing.T) {
	// Temporary SQLite database file used exclusively for this test
	dbPath := "./test_metrics.db"
	// Ensure the database file is deleted after the test finishes (cleanup)
	defer os.Remove(dbPath)

	// Initialize a new MetricsStore instance with the temporary database
	store, err := NewMetricsStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create metrics store: %v", err)
	}
	// Close the database connection when the test completes
	defer store.Close()

	// Define a single sample usage metric for testing basic record/retrieve functionality
	metric := UsageMetric{
		Timestamp:        time.Now(),           // Current time as the event timestamp
		Model:            "gpt-4",              // Model used for the request
		PromptTokens:     100,                  // Number of tokens in the prompt
		CompletionTokens: 50,                   // Number of tokens in the generated completion
		TotalTokens:      150,                  // Sum of prompt + completion tokens
		Status:           "success",            // Request status (e.g., HTTP code or custom label)
	}

	// Record the metric into the store
	if err := store.RecordUsage(context.Background(), metric); err != nil {
		t.Fatalf("failed to record usage metric: %v", err)
	}

	// Retrieve overall totals with no filters (empty query)
	totals, err := store.GetTotals(context.Background(), MetricsQuery{})
	if err != nil {
		t.Fatalf("failed to retrieve totals: %v", err)
	}

	// Verify that the total token count matches the single recorded metric
	if totals.TotalTokens != 150 {
		t.Errorf("expected TotalTokens to be 150, got %d", totals.TotalTokens)
	}

	// Verify that exactly one request was counted
	if totals.TotalRequests != 1 {
		t.Errorf("expected TotalRequests to be 1, got %d", totals.TotalRequests)
	}

	// Optional: Helpful log output if the test runs with -v flag
	t.Logf("✅ Successfully recorded and verified 1 metric: %d tokens, %d request(s)",
		totals.TotalTokens, totals.TotalRequests)
}