package executor

import (
	"testing"
)

// item builds a minimal image_generation_call item JSON.
func imageGenItem(result, format string) []byte {
	return []byte(`{"type":"image_generation_call","result":"` + result + `","output_format":"` + format + `"}`)
}

func TestCodexExtractImageResults_FromCompletedOutput(t *testing.T) {
	completed := []byte(`{"type":"response.completed","response":{"created_at":111,"output":[` +
		string(imageGenItem("AAA", "png")) + `]}}`)

	results, createdAt, _, firstMeta, err := codexExtractImageResults(completed, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if createdAt != 111 {
		t.Fatalf("createdAt = %d, want 111", createdAt)
	}
	if len(results) != 1 || results[0].Result != "AAA" {
		t.Fatalf("unexpected results: %+v", results)
	}
	if firstMeta.OutputFormat != "png" {
		t.Fatalf("firstMeta.OutputFormat = %q, want png", firstMeta.OutputFormat)
	}
}

func TestCodexExtractImageResults_FallbackToCollectedItemsOrdered(t *testing.T) {
	// Completed event has an empty output; images arrived via output_item.done.
	completed := []byte(`{"type":"response.completed","response":{"created_at":222,"output":[]}}`)
	itemsByIndex := map[int64][]byte{
		2: imageGenItem("SECOND", "png"),
		0: imageGenItem("FIRST", "jpg"),
	}

	results, createdAt, _, _, err := codexExtractImageResults(completed, itemsByIndex, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if createdAt != 222 {
		t.Fatalf("createdAt = %d, want 222", createdAt)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(results), results)
	}
	// Ordering must follow output_index (0 before 2).
	if results[0].Result != "FIRST" || results[1].Result != "SECOND" {
		t.Fatalf("results out of order: %+v", results)
	}
}

func TestCodexExtractImageResults_PrefersCompletedOutputOverItems(t *testing.T) {
	// When the completed output is non-empty, collected items must be ignored
	// (matches the original patchCodexCompletedOutput behaviour).
	completed := []byte(`{"type":"response.completed","response":{"created_at":333,"output":[` +
		string(imageGenItem("FROM_OUTPUT", "png")) + `]}}`)
	itemsByIndex := map[int64][]byte{0: imageGenItem("FROM_ITEMS", "png")}

	results, _, _, _, err := codexExtractImageResults(completed, itemsByIndex, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Result != "FROM_OUTPUT" {
		t.Fatalf("expected to prefer completed output, got %+v", results)
	}
}

func TestCodexExtractImageResults_WrongEventType(t *testing.T) {
	if _, _, _, _, err := codexExtractImageResults([]byte(`{"type":"response.in_progress"}`), nil, nil); err == nil {
		t.Fatalf("expected error for non-completed event type")
	}
}

func TestCodexExtractImageResults_FallbackList(t *testing.T) {
	// Items collected without an output_index land in the fallback slice.
	completed := []byte(`{"type":"response.completed","response":{"created_at":444}}`)
	fallback := [][]byte{imageGenItem("FB", "webp")}

	results, _, _, firstMeta, err := codexExtractImageResults(completed, nil, fallback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Result != "FB" {
		t.Fatalf("unexpected fallback results: %+v", results)
	}
	if firstMeta.OutputFormat != "webp" {
		t.Fatalf("firstMeta.OutputFormat = %q, want webp", firstMeta.OutputFormat)
	}
}
