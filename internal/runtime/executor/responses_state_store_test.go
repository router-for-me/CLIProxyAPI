package executor

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestMemoryResponsesStateStore_PutAndGet(t *testing.T) {
	t.Parallel()

	store := NewMemoryResponsesStateStore(30*time.Minute, 1024)

	snap := ResponsesSnapshot{
		Model:        "gpt-4",
		Instructions: "You are a helpful assistant.",
		Input:        json.RawMessage(`[{"role":"user","content":"hello"}]`),
		Output:       json.RawMessage(`[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]`),
		CreatedAt:    1234567890,
	}

	store.Put("resp_001", snap)

	got, ok := store.Get("resp_001")
	if !ok {
		t.Fatal("expected snapshot to exist")
	}
	if got.Model != snap.Model {
		t.Errorf("Model = %q, want %q", got.Model, snap.Model)
	}
	if got.Instructions != snap.Instructions {
		t.Errorf("Instructions = %q, want %q", got.Instructions, snap.Instructions)
	}
	if string(got.Input) != string(snap.Input) {
		t.Errorf("Input = %s, want %s", got.Input, snap.Input)
	}
	if string(got.Output) != string(snap.Output) {
		t.Errorf("Output = %s, want %s", got.Output, snap.Output)
	}
	if got.CreatedAt != snap.CreatedAt {
		t.Errorf("CreatedAt = %d, want %d", got.CreatedAt, snap.CreatedAt)
	}
	if got.StoredAt.IsZero() {
		t.Error("StoredAt should be set automatically")
	}
}

func TestMemoryResponsesStateStore_GetMissing(t *testing.T) {
	t.Parallel()

	store := NewMemoryResponsesStateStore(30*time.Minute, 1024)
	_, ok := store.Get("nonexistent")
	if ok {
		t.Fatal("expected snapshot not to exist")
	}
}

func TestMemoryResponsesStateStore_Delete(t *testing.T) {
	t.Parallel()

	store := NewMemoryResponsesStateStore(30*time.Minute, 1024)
	snap := ResponsesSnapshot{Model: "gpt-4"}
	store.Put("resp_002", snap)

	store.Delete("resp_002")

	_, ok := store.Get("resp_002")
	if ok {
		t.Fatal("expected snapshot to be deleted")
	}
}

func TestMemoryResponsesStateStore_TTLExpiry(t *testing.T) {
	// Cannot use t.Parallel() because we manipulate time-sensitive state.

	// Create a store with a very short TTL.
	store := NewMemoryResponsesStateStore(100*time.Millisecond, 1024)

	snap := ResponsesSnapshot{Model: "gpt-4"}
	store.Put("resp_ttl", snap)

	// Should exist immediately.
	_, ok := store.Get("resp_ttl")
	if !ok {
		t.Fatal("expected snapshot to exist immediately after Put")
	}

	// Wait for TTL to expire.
	time.Sleep(200 * time.Millisecond)

	_, ok = store.Get("resp_ttl")
	if ok {
		t.Fatal("expected snapshot to be expired after TTL")
	}
}

func TestMemoryResponsesStateStore_LRUEviction(t *testing.T) {
	t.Parallel()

	// Create a store with max 3 entries.
	store := NewMemoryResponsesStateStore(30*time.Minute, 3)

	snap := ResponsesSnapshot{Model: "gpt-4"}
	store.Put("resp_1", snap)
	// Small sleep to ensure ordering by createdAt is deterministic.
	time.Sleep(10 * time.Millisecond)
	store.Put("resp_2", snap)
	time.Sleep(10 * time.Millisecond)
	store.Put("resp_3", snap)

	// All three should exist.
	if _, ok := store.Get("resp_1"); !ok {
		t.Fatal("resp_1 should exist")
	}
	if _, ok := store.Get("resp_2"); !ok {
		t.Fatal("resp_2 should exist")
	}
	if _, ok := store.Get("resp_3"); !ok {
		t.Fatal("resp_3 should exist")
	}

	// Adding a 4th entry should evict the oldest (resp_1).
	store.Put("resp_4", snap)

	if _, ok := store.Get("resp_1"); ok {
		t.Fatal("resp_1 should have been evicted")
	}
	if _, ok := store.Get("resp_4"); !ok {
		t.Fatal("resp_4 should exist")
	}
}

func TestMemoryResponsesStateStore_OverwritePut(t *testing.T) {
	t.Parallel()

	store := NewMemoryResponsesStateStore(30*time.Minute, 1024)

	store.Put("resp_overwrite", ResponsesSnapshot{Model: "gpt-3.5"})
	store.Put("resp_overwrite", ResponsesSnapshot{Model: "gpt-4"})

	got, ok := store.Get("resp_overwrite")
	if !ok {
		t.Fatal("expected snapshot to exist")
	}
	if got.Model != "gpt-4" {
		t.Errorf("Model = %q, want %q after overwrite", got.Model, "gpt-4")
	}
}

func TestMemoryResponsesStateStore_DefaultTTL(t *testing.T) {
	t.Parallel()

	// Passing 0 TTL should default to 30 minutes.
	store := NewMemoryResponsesStateStore(0, 10)
	snap := ResponsesSnapshot{Model: "gpt-4"}
	store.Put("resp_default_ttl", snap)

	// Should still exist shortly after.
	_, ok := store.Get("resp_default_ttl")
	if !ok {
		t.Fatal("expected snapshot to exist with default TTL")
	}
}

func TestMemoryResponsesStateStore_DefaultMaxEntries(t *testing.T) {
	t.Parallel()

	// Passing 0 maxEntries should default to 1024.
	store := NewMemoryResponsesStateStore(30*time.Minute, 0)
	snap := ResponsesSnapshot{Model: "gpt-4"}
	store.Put("resp_default_max", snap)

	_, ok := store.Get("resp_default_max")
	if !ok {
		t.Fatal("expected snapshot to exist with default max entries")
	}
}

// ---------------------------------------------------------------------------
// MergeResponsesTranscript
// ---------------------------------------------------------------------------

func TestMergeResponsesTranscript_BasicMerge(t *testing.T) {
	t.Parallel()

	snapshot := ResponsesSnapshot{
		Model:        "gpt-4",
		Instructions: "Be helpful.",
		Input:        json.RawMessage(`[{"role":"user","content":"hello"}]`),
		Output:       json.RawMessage(`[{"type":"message","content":[{"type":"output_text","text":"hi there"}]}]`),
	}

	currentRequest := []byte(`{
		"model": "gpt-4",
		"previous_response_id": "resp_prev",
		"input": [{"role":"user","content":"how are you?"}],
		"stream": false
	}`)

	merged, err := MergeResponsesTranscript(currentRequest, snapshot)
	if err != nil {
		t.Fatalf("MergeResponsesTranscript error: %v", err)
	}

	// Verify previous_response_id is removed.
	if g := gjson.GetBytes(merged, "previous_response_id"); g.Exists() {
		t.Fatal("previous_response_id should be removed")
	}

	// Verify model is preserved.
	if g := gjson.GetBytes(merged, "model"); g.String() != "gpt-4" {
		t.Fatalf("model = %q, want %q", g.String(), "gpt-4")
	}

	// Verify input is merged: prevInput + prevOutput + currentInput = 3 items.
	inputArr := gjson.GetBytes(merged, "input")
	if !inputArr.IsArray() {
		t.Fatal("input should be an array")
	}
	count := 0
	inputArr.ForEach(func(_, _ gjson.Result) bool {
		count++
		return true
	})
	if count != 3 {
		t.Fatalf("merged input has %d items, want 3", count)
	}
}

func TestMergeResponsesTranscript_EmptyCurrentInput(t *testing.T) {
	t.Parallel()

	snapshot := ResponsesSnapshot{
		Model:  "gpt-4",
		Input:  json.RawMessage(`[{"role":"user","content":"hello"}]`),
		Output: json.RawMessage(`[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]`),
	}

	currentRequest := []byte(`{
		"model": "gpt-4",
		"previous_response_id": "resp_prev",
		"stream": false
	}`)

	merged, err := MergeResponsesTranscript(currentRequest, snapshot)
	if err != nil {
		t.Fatalf("MergeResponsesTranscript error: %v", err)
	}

	// Without current input, merged = prevInput + prevOutput = 2 items.
	inputArr := gjson.GetBytes(merged, "input")
	count := 0
	inputArr.ForEach(func(_, _ gjson.Result) bool {
		count++
		return true
	})
	if count != 2 {
		t.Fatalf("merged input has %d items, want 2", count)
	}
}

func TestMergeResponsesTranscript_BackfillModel(t *testing.T) {
	t.Parallel()

	snapshot := ResponsesSnapshot{
		Model:  "gpt-4-turbo",
		Input:  json.RawMessage(`[]`),
		Output: json.RawMessage(`[]`),
	}

	// currentRequest has no model field.
	currentRequest := []byte(`{"previous_response_id":"resp_prev","input":[],"stream":false}`)

	merged, err := MergeResponsesTranscript(currentRequest, snapshot)
	if err != nil {
		t.Fatalf("MergeResponsesTranscript error: %v", err)
	}

	if g := gjson.GetBytes(merged, "model"); g.String() != "gpt-4-turbo" {
		t.Fatalf("backfilled model = %q, want %q", g.String(), "gpt-4-turbo")
	}
}

func TestMergeResponsesTranscript_BackfillInstructions(t *testing.T) {
	t.Parallel()

	snapshot := ResponsesSnapshot{
		Model:        "gpt-4",
		Instructions: "Follow the rules.",
		Input:        json.RawMessage(`[]`),
		Output:       json.RawMessage(`[]`),
	}

	// currentRequest has no instructions field.
	currentRequest := []byte(`{"model":"gpt-4","previous_response_id":"resp_prev","input":[],"stream":false}`)

	merged, err := MergeResponsesTranscript(currentRequest, snapshot)
	if err != nil {
		t.Fatalf("MergeResponsesTranscript error: %v", err)
	}

	if g := gjson.GetBytes(merged, "instructions"); g.String() != "Follow the rules." {
		t.Fatalf("backfilled instructions = %q, want %q", g.String(), "Follow the rules.")
	}
}

func TestMergeResponsesTranscript_EmptyCurrentRequest(t *testing.T) {
	t.Parallel()

	snapshot := ResponsesSnapshot{Model: "gpt-4"}

	_, err := MergeResponsesTranscript([]byte{}, snapshot)
	if err == nil {
		t.Fatal("expected error for empty current request")
	}
}
