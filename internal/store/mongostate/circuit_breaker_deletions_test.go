package mongostate

import (
	"testing"
	"time"

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
}
