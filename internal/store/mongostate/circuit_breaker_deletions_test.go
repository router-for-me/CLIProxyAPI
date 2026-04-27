package mongostate

import (
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestBuildCircuitBreakerDeletionDedupeKey_NormalizesValues(t *testing.T) {
	got := BuildCircuitBreakerDeletionDedupeKey(" Gemini ", "auth-1", " GPT-5 ")
	if got != "gemini|auth-1|gpt-5" {
		t.Fatalf("BuildCircuitBreakerDeletionDedupeKey() = %q, want %q", got, "gemini|auth-1|gpt-5")
	}
}

func TestCircuitBreakerDeletionItemFromRecord_MapsActionFields(t *testing.T) {
	now := time.Now().UTC().Round(time.Second)
	record := CircuitBreakerDeletionRecord{
		ID:                  primitive.NewObjectID(),
		AuthID:              "auth-1",
		Provider:            "codex",
		Model:               "gpt-5",
		NormalizedModel:     "gpt-5",
		DedupeKey:           "codex|auth-1|gpt-5",
		Status:              CircuitBreakerDeletionStatusDeleted,
		OpenCycles:          4,
		FailureCount:        7,
		ConsecutiveFailures: 3,
		OpenedAt:            now,
		ActionAt:            &now,
		ActionBy:            "management_api",
		ActionError:         "boom",
		RequestID:           "req-1",
		RequestLogRef:       "req-log-1",
		FailureStage:        "request_execution",
		ErrorCode:           "upstream_timeout",
		ErrorMessageHash:    "hash-1",
		StatusCode:          504,
		LastErrorEventID:    "event-1",
		Persisted:           true,
		AlreadyRemoved:      false,
		RuntimeSuspended:    false,
		UpdatedAt:           now,
		CreatedAt:           now,
	}

	item := CircuitBreakerDeletionItemFromRecord(record)
	if item.ID != record.ID.Hex() {
		t.Fatalf("item.ID = %q, want %q", item.ID, record.ID.Hex())
	}
	if item.Status != CircuitBreakerDeletionStatusDeleted {
		t.Fatalf("item.Status = %q, want %q", item.Status, CircuitBreakerDeletionStatusDeleted)
	}
	if item.ActionBy != "management_api" {
		t.Fatalf("item.ActionBy = %q, want %q", item.ActionBy, "management_api")
	}
	if item.ActionError != "boom" {
		t.Fatalf("item.ActionError = %q, want %q", item.ActionError, "boom")
	}
	if item.ActionAt == nil || !item.ActionAt.Equal(now) {
		t.Fatalf("item.ActionAt = %v, want %v", item.ActionAt, now)
	}
	if item.RequestID != "req-1" || item.RequestLogRef != "req-log-1" || item.LastErrorEventID != "event-1" {
		t.Fatalf("evidence fields not mapped: %+v", item)
	}
	if item.FailureStage != "request_execution" || item.ErrorCode != "upstream_timeout" || item.StatusCode != 504 || item.ErrorMessageHash != "hash-1" {
		t.Fatalf("error evidence fields not mapped: %+v", item)
	}
}

func TestCircuitBreakerDeletionIndexModels_TTLOnlyAppliesToTerminalStatuses(t *testing.T) {
	models := circuitBreakerDeletionIndexModels(30)
	var ttlFilter any
	for _, model := range models {
		if model.Options != nil && model.Options.Name != nil && *model.Options.Name == "ttl_terminal_created_at" {
			ttlFilter = model.Options.PartialFilterExpression
			break
		}
	}
	if ttlFilter == nil {
		t.Fatal("missing ttl_terminal_created_at index")
	}
	want := bson.M{"status": bson.M{"$in": []string{
		CircuitBreakerDeletionStatusDeleted,
		CircuitBreakerDeletionStatusFailed,
		CircuitBreakerDeletionStatusDismissed,
	}}}
	if !reflect.DeepEqual(ttlFilter, want) {
		t.Fatalf("ttl partial filter = %#v, want %#v", ttlFilter, want)
	}
}

func TestCircuitBreakerDeletionActionFilter_RequiresPendingStatus(t *testing.T) {
	id := primitive.NewObjectID()
	filter := circuitBreakerDeletionActionFilter(id)
	if filter["_id"] != id {
		t.Fatalf("filter _id = %v, want %v", filter["_id"], id)
	}
	if filter["status"] != CircuitBreakerDeletionStatusPending {
		t.Fatalf("filter status = %v, want pending", filter["status"])
	}
}
