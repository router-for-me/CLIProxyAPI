package executor

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestAntigravityConvertStreamChunksToNonStream_PreservesGroundingMetadataWhenTrailingChunkEmpty(t *testing.T) {
	exec := &AntigravityExecutor{}
	chunks := [][]byte{
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Answer with citations."}]},"groundingMetadata":{"webSearchQueries":["cli proxy api"],"groundingChunks":[{"web":{"uri":"https://example.com","title":"Example"}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-1"},"traceId":"trace-1"}`),
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":" Done."}]},"finishReason":"STOP","groundingMetadata":{}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}},"traceId":"trace-1"}`),
	}

	got := exec.convertStreamChunksToNonStream(chunks)
	gm := ojson.GetBytes(got, "response.candidates.0.groundingMetadata")
	if !gm.Exists() || !gm.IsObject() {
		t.Fatalf("expected groundingMetadata object in merged response, got: %s", string(got))
	}
	if q := gm.Get("webSearchQueries.0").String(); q != "cli proxy api" {
		t.Fatalf("expected webSearchQueries.0 = %q, got %q (raw=%s)", "cli proxy api", q, gm.Raw)
	}
	if n := gm.Get("groundingChunks.#").Int(); n != 1 {
		t.Fatalf("expected 1 groundingChunk, got %d (raw=%s)", n, gm.Raw)
	}
}

func TestAntigravityConvertStreamChunksToNonStream_PreservesGroundingMetadataWhenTrailingChunkOmitted(t *testing.T) {
	exec := &AntigravityExecutor{}
	chunks := [][]byte{
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"groundingMetadata":{"webSearchQueries":["golang"]}}]}}`),
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":" world"}]},"finishReason":"STOP"}]}}`),
	}

	got := exec.convertStreamChunksToNonStream(chunks)
	if q := ojson.GetBytes(got, "response.candidates.0.groundingMetadata.webSearchQueries.0").String(); q != "golang" {
		t.Fatalf("expected preserved webSearchQueries.0 = %q, got %q (raw=%s)", "golang", q, string(got))
	}
}

func TestAntigravityConvertStreamChunksToNonStream_UsesLatestNonEmptyGroundingMetadata(t *testing.T) {
	exec := &AntigravityExecutor{}
	chunks := [][]byte{
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"A"}]},"groundingMetadata":{"webSearchQueries":["first"]}}]}}`),
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"B"}]},"groundingMetadata":{"webSearchQueries":["second"]}}]}}`),
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"C"}]},"finishReason":"STOP","groundingMetadata":{}}]}}`),
	}

	got := exec.convertStreamChunksToNonStream(chunks)
	if q := ojson.GetBytes(got, "response.candidates.0.groundingMetadata.webSearchQueries.0").String(); q != "second" {
		t.Fatalf("expected latest non-empty webSearchQueries.0 = %q, got %q (raw=%s)", "second", q, string(got))
	}
}
