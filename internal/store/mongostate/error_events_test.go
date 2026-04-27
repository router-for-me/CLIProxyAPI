package mongostate

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestBuildErrorEventDedupeKey_WithRequestID(t *testing.T) {
	got := BuildErrorEventDedupeKey(" Gemini ", "auth-1", " GPT-5 ", "req-1", "request_execution", "UPSTREAM_TIMEOUT", 504, time.UnixMilli(1000), "hash", 1)
	want := "gemini|auth-1|gpt-5|req-1|request_execution|upstream_timeout|504"
	if got != want {
		t.Fatalf("BuildErrorEventDedupeKey() = %q, want %q", got, want)
	}
}

func TestBuildErrorEventDedupeKey_WithoutRequestIDIncludesFallbackFields(t *testing.T) {
	got := BuildErrorEventDedupeKey("gemini", "auth-1", "gpt-5", "", "request_execution", "", 500, time.UnixMilli(123456), "abc", 2)
	want := "gemini|auth-1|gpt-5||request_execution||500|123456|abc|2"
	if got != want {
		t.Fatalf("BuildErrorEventDedupeKey() = %q, want %q", got, want)
	}
}

func TestErrorEventItemFromRecord_MapsFields(t *testing.T) {
	now := time.Now().UTC().Round(time.Second)
	record := ErrorEventRecord{
		ID:                 primitive.NewObjectID(),
		DedupeKey:          "key",
		CreatedAt:          now,
		OccurredAt:         now,
		Provider:           "gemini",
		Model:              "gpt-5",
		NormalizedModel:    "gpt-5",
		Source:             "api-key",
		AuthID:             "auth-1",
		AuthIndex:          "idx-1",
		RequestID:          "req-1",
		RequestLogRef:      "req-1",
		AttemptCount:       2,
		UpstreamRequestIDs: []string{"u1", "u2"},
		Failed:             true,
		FailureStage:       "request_execution",
		ErrorCode:          "upstream_timeout",
		ErrorMessageMasked: "masked",
		ErrorMessageHash:   "hash",
		StatusCode:         504,
		CircuitCountable:   true,
	}

	item := ErrorEventItemFromRecord(record)
	if item.ID != record.ID.Hex() {
		t.Fatalf("item.ID = %q, want %q", item.ID, record.ID.Hex())
	}
	if item.ErrorMessageMasked != "masked" {
		t.Fatalf("item.ErrorMessageMasked = %q, want %q", item.ErrorMessageMasked, "masked")
	}
	if item.ErrorMessageHash != "hash" {
		t.Fatalf("item.ErrorMessageHash = %q, want %q", item.ErrorMessageHash, "hash")
	}
	if len(item.UpstreamRequestIDs) != 2 {
		t.Fatalf("item.UpstreamRequestIDs len = %d, want %d", len(item.UpstreamRequestIDs), 2)
	}
}

func TestNormalizeErrorEventSummaryGroupBy_DefaultAndDedup(t *testing.T) {
	got := normalizeErrorEventSummaryGroupBy(nil)
	want := []string{"provider", "model", "auth_id", "error_code", "failure_stage", "status_code"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("group_by[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	got = normalizeErrorEventSummaryGroupBy([]string{" provider ", "unknown", "provider", "status_code"})
	if len(got) != 2 || got[0] != "provider" || got[1] != "status_code" {
		t.Fatalf("normalized group_by = %#v, want [provider status_code]", got)
	}

	got = normalizeErrorEventSummaryGroupBy([]string{"auth_id", "normalized_model"})
	if len(got) != 2 || got[0] != "auth_id" || got[1] != "normalized_model" {
		t.Fatalf("normalized group_by = %#v, want [auth_id normalized_model]", got)
	}
}

func TestBuildErrorEventFilter_MapsQueryFields(t *testing.T) {
	statusCode := 503
	filter := buildErrorEventFilter(ErrorEventQuery{
		Provider:     "Gemini",
		AuthID:       "a1",
		Model:        "gpt-5",
		FailureStage: "request_execution",
		ErrorCode:    "upstream_timeout",
		StatusCode:   &statusCode,
		RequestID:    "req-1",
		Start:        time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC),
		End:          time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC),
	})

	if filter["provider"] != "gemini" {
		t.Fatalf("provider filter = %v, want gemini", filter["provider"])
	}
	if filter["auth_id"] != "a1" {
		t.Fatalf("auth_id filter = %v, want a1", filter["auth_id"])
	}
	if filter["failure_stage"] != "request_execution" {
		t.Fatalf("failure_stage filter = %v, want request_execution", filter["failure_stage"])
	}
	if filter["error_code"] != "upstream_timeout" {
		t.Fatalf("error_code filter = %v, want upstream_timeout", filter["error_code"])
	}
	if filter["status_code"] != 503 {
		t.Fatalf("status_code filter = %v, want 503", filter["status_code"])
	}
	if filter["request_id"] != "req-1" {
		t.Fatalf("request_id filter = %v, want req-1", filter["request_id"])
	}
	if _, ok := filter["$or"]; !ok {
		t.Fatalf("missing model $or filter: %#v", filter)
	}
	if _, ok := filter["occurred_at"]; !ok {
		t.Fatalf("missing occurred_at filter: %#v", filter)
	}
}
