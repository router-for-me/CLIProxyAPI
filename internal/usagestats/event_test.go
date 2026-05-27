package usagestats

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestRecordToEventBasicFields(t *testing.T) {
	record := coreusage.Record{
		Provider:        "openai",
		Model:           "gpt-4.1-mini",
		Alias:           "gpt-4.1-mini",
		AuthID:          "auth-001",
		AuthIndex:       "abc12345",
		AuthType:        "oauth",
		ReasoningEffort: "high",
		RequestedAt:     time.Date(2026, 5, 27, 4, 0, 0, 0, time.UTC),
		Latency:         1500 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  1000,
			OutputTokens: 500,
			TotalTokens:  1500,
		},
	}

	event := RecordToEvent(context.Background(), record, nil)

	if event.Provider != "openai" {
		t.Errorf("provider = %q, want openai", event.Provider)
	}
	if event.Model != "gpt-4.1-mini" {
		t.Errorf("model = %q, want gpt-4.1-mini", event.Model)
	}
	if event.AuthIndex != "abc12345" {
		t.Errorf("auth_index = %q, want abc12345", event.AuthIndex)
	}
	if event.LatencyMs != 1500 {
		t.Errorf("latency_ms = %d, want 1500", event.LatencyMs)
	}
	if event.InputTokens != 1000 {
		t.Errorf("input_tokens = %d, want 1000", event.InputTokens)
	}
	if event.OutputTokens != 500 {
		t.Errorf("output_tokens = %d, want 500", event.OutputTokens)
	}
	if event.TotalTokens != 1500 {
		t.Errorf("total_tokens = %d, want 1500", event.TotalTokens)
	}
	if event.RequestedAt != time.Date(2026, 5, 27, 4, 0, 0, 0, time.UTC) {
		t.Errorf("requested_at = %v, want 2026-05-27T04:00:00Z", event.RequestedAt)
	}
	if event.Failed {
		t.Error("failed = true, want false")
	}
}

func TestRecordToEventWithCost(t *testing.T) {
	record := coreusage.Record{
		Provider: "openai",
		Model:    "gpt-4.1-mini",
		Detail: coreusage.Detail{
			InputTokens:  1000,
			OutputTokens: 500,
		},
	}
	pm := NewPriceMatcher([]ModelPrice{
		{Provider: "openai", Model: "gpt-4.1-mini", InputCostPerToken: 0.0000004, OutputCostPerToken: 0.0000016},
	})

	event := RecordToEvent(context.Background(), record, pm)

	if !event.CostKnown {
		t.Fatal("cost_known = false, want true")
	}
	if event.InputCostMicros != 400 {
		t.Errorf("input_cost_micros = %d, want 400", event.InputCostMicros)
	}
	if event.OutputCostMicros != 800 {
		t.Errorf("output_cost_micros = %d, want 800", event.OutputCostMicros)
	}
	if event.TotalCostMicros != 1200 {
		t.Errorf("total_cost_micros = %d, want 1200", event.TotalCostMicros)
	}
}

func TestRecordToEventUnknownCost(t *testing.T) {
	record := coreusage.Record{
		Provider: "anthropic",
		Model:    "claude-opus-4",
		Detail: coreusage.Detail{
			InputTokens:  2000,
			OutputTokens: 1000,
		},
	}
	pm := NewPriceMatcher([]ModelPrice{
		{Provider: "openai", Model: "gpt-4.1-mini", InputCostPerToken: 0.0000004, OutputCostPerToken: 0.0000016},
	})

	event := RecordToEvent(context.Background(), record, pm)

	if event.CostKnown {
		t.Fatal("cost_known = true for unmatched model, want false")
	}
	if event.InputCostMicros != 0 || event.OutputCostMicros != 0 || event.TotalCostMicros != 0 {
		t.Errorf("cost micros should all be 0 for unknown price, got input=%d output=%d total=%d",
			event.InputCostMicros, event.OutputCostMicros, event.TotalCostMicros)
	}
}

func TestRecordToEventNoMatcher(t *testing.T) {
	record := coreusage.Record{
		Provider: "openai",
		Model:    "gpt-4.1-mini",
		Detail: coreusage.Detail{
			InputTokens:  1000,
			OutputTokens: 500,
		},
	}

	event := RecordToEvent(context.Background(), record, nil)

	if event.CostKnown {
		t.Fatal("cost_known = true with nil matcher, want false")
	}
}

func TestRecordToEventAPIKeyHashing(t *testing.T) {
	rawKey := "sk-proj-abcdef1234567890"
	record := coreusage.Record{
		Provider: "openai",
		Model:    "gpt-4.1-mini",
		APIKey:   rawKey,
	}

	event := RecordToEvent(context.Background(), record, nil)

	// The raw API key must NOT appear in the event.
	if strings.Contains(event.APIKeyID, rawKey) {
		t.Fatal("raw API key leaked into APIKeyID")
	}
	// APIKeyID should start with "key_" prefix.
	if !strings.HasPrefix(event.APIKeyID, "key_") {
		t.Errorf("api_key_id = %q, want prefix 'key_'", event.APIKeyID)
	}
	// APIKeyID should have "key_" + 16 hex chars.
	if len(event.APIKeyID) != 4+16 {
		t.Errorf("api_key_id length = %d, want 20", len(event.APIKeyID))
	}
}

func TestRecordToEventEmptyAPIKey(t *testing.T) {
	record := coreusage.Record{
		Provider: "openai",
		Model:    "gpt-4.1-mini",
		APIKey:   "",
	}

	event := RecordToEvent(context.Background(), record, nil)
	if event.APIKeyID != "" {
		t.Errorf("api_key_id = %q, want empty for empty key", event.APIKeyID)
	}
}

func TestRecordToEventSafeSourceLabel(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{"email", "user@example.com", "user@example.com"},
		{"project ID", "my-project-123", "my-project-123"},
		{"file path", "/auths/openai.json", "/auths/openai.json"},
		{"empty", "", ""},
		{"raw API key", "sk-proj-abcdef1234567890ABCDEFGHIJKLMNOPQRSTUVWXYZ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := coreusage.Record{
				Provider: "openai",
				Model:    "gpt-4.1-mini",
				Source:   tt.source,
			}
			event := RecordToEvent(context.Background(), record, nil)
			if event.SourceLabel != tt.want {
				t.Errorf("source_label = %q, want %q", event.SourceLabel, tt.want)
			}
		})
	}
}

func TestRecordToEventFailureCoarseErrorType(t *testing.T) {
	tests := []struct {
		name       string
		failed     bool
		statusCode int
		want       string
	}{
		{"success", false, 200, ""},
		{"429", true, 429, "http_429"},
		{"500", true, 500, "http_500"},
		{"403", true, 403, "http_403"},
		{"no status", true, 0, "unknown_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := coreusage.Record{
				Provider: "openai",
				Model:    "gpt-4.1-mini",
				Failed:   tt.failed,
				Fail:     coreusage.Failure{StatusCode: tt.statusCode, Body: "sensitive error body"},
			}
			event := RecordToEvent(context.Background(), record, nil)
			if event.ErrorType != tt.want {
				t.Errorf("error_type = %q, want %q", event.ErrorType, tt.want)
			}
			// The raw error body must NEVER appear in the event.
			if strings.Contains(event.ErrorType, "sensitive error body") {
				t.Fatal("raw error body leaked into error_type")
			}
		})
	}
}

func TestRecordToEventNoSensitiveFieldsInJSON(t *testing.T) {
	record := coreusage.Record{
		Provider:        "openai",
		Model:           "gpt-4.1-mini",
		APIKey:          "sk-secret-key-12345",
		Source:          "sk-secret-key-12345",
		ResponseHeaders: http.Header{"Authorization": []string{"Bearer secret-token"}},
		Fail:            coreusage.Failure{StatusCode: 500, Body: "internal server error with token abc123"},
		Failed:          true,
	}

	event := RecordToEvent(context.Background(), record, nil)

	// Serialize to JSON and check for sensitive values.
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	jsonStr := string(data)

	forbidden := []string{
		"sk-secret-key-12345",
		"Bearer secret-token",
		"secret-token",
		"internal server error with token abc123",
		"abc123",
	}
	for _, secret := range forbidden {
		if strings.Contains(jsonStr, secret) {
			t.Errorf("JSON output contains forbidden secret %q", secret)
		}
	}
}

func TestRecordToEventDefaultAlias(t *testing.T) {
	record := coreusage.Record{
		Provider: "openai",
		Model:    "gpt-4.1-mini",
		Alias:    "",
	}

	event := RecordToEvent(context.Background(), record, nil)
	if event.Alias != "gpt-4.1-mini" {
		t.Errorf("alias = %q, want model name when alias is empty", event.Alias)
	}
}

func TestRecordToEventTotalTokensFallback(t *testing.T) {
	record := coreusage.Record{
		Provider: "openai",
		Model:    "gpt-4.1-mini",
		Detail: coreusage.Detail{
			InputTokens:  100,
			OutputTokens: 50,
			// TotalTokens intentionally 0 to test fallback.
		},
	}

	event := RecordToEvent(context.Background(), record, nil)
	if event.TotalTokens != 150 {
		t.Errorf("total_tokens = %d, want 150 (fallback from input+output)", event.TotalTokens)
	}
}

func TestSafeAPIKeyIDDeterministic(t *testing.T) {
	key := "sk-test-key"
	id1 := SafeAPIKeyID(key)
	id2 := SafeAPIKeyID(key)
	if id1 != id2 {
		t.Errorf("same key produced different IDs: %q vs %q", id1, id2)
	}
}

func TestSafeAPIKeyIDDifferentKeys(t *testing.T) {
	id1 := SafeAPIKeyID("key-one")
	id2 := SafeAPIKeyID("key-two")
	if id1 == id2 {
		t.Errorf("different keys produced same ID: %q", id1)
	}
}

func TestClassifyCallType(t *testing.T) {
	tests := []struct {
		endpoint string
		want     string
	}{
		{"POST /v1/chat/completions", "chat"},
		{"POST /v1/completions", "completions"},
		{"POST /v1/messages", "chat"},
		{"POST /v1/messages/count_tokens", "count_tokens"},
		{"POST /v1/responses", "responses"},
		{"POST /v1/images/generations", "images"},
		{"POST /v1/images/edits", "images"},
		{"POST /v1/videos", "videos"},
		{"POST /v1/videos/generations", "videos"},
		{"POST /v1/embeddings", "embeddings"},
		{"POST /v1/audio/transcriptions", "audio"},
		{"GET /v1/models", "models"},
		{"POST /v1/unknown/endpoint", "other"},
		{"", ""},
		{"/v1/chat/completions", "chat"},
	}
	for _, tt := range tests {
		got := classifyCallType(tt.endpoint)
		if got != tt.want {
			t.Errorf("classifyCallType(%q) = %q, want %q", tt.endpoint, got, tt.want)
		}
	}
}

func TestRecordToEventCallType(t *testing.T) {
	ctx := context.Background()
	ctx = internallogging.WithEndpoint(ctx, "POST /v1/chat/completions")
	record := coreusage.Record{
		Provider: "openai",
		Model:    "gpt-4o",
	}
	evt := RecordToEvent(ctx, record, nil)
	if evt.CallType != "chat" {
		t.Errorf("CallType = %q, want %q", evt.CallType, "chat")
	}
	if evt.Endpoint != "POST /v1/chat/completions" {
		t.Errorf("Endpoint = %q, want %q", evt.Endpoint, "POST /v1/chat/completions")
	}
}
