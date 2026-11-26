package executor

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestAntigravityConvertStreamChunksToNonStream_PreservesGroundingMetadataWhenTrailingChunkEmpty(t *testing.T) {
	for _, tc := range []struct {
		name     string
		trailing string
	}{
		{
			name:     "compact empty object",
			trailing: `{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP","groundingMetadata":{}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5,"totalTokenCount":17}}}`,
		},
		{
			name:     "whitespace empty object",
			trailing: `{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP","groundingMetadata":{ }}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5,"totalTokenCount":17}}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := &AntigravityExecutor{}
			chunks := [][]byte{
				[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Search result answer."}]},"groundingMetadata":{"webSearchQueries":["cli proxy api"],"groundingChunks":[{"web":{"uri":"https://example.com","title":"Example"}}],"groundingSupports":[{"segment":{"startIndex":0,"endIndex":6},"groundingChunkIndices":[0]}]}}],"modelVersion":"gemini-3-pro","responseId":"resp-ground-empty-trailing"}}`),
				[]byte(tc.trailing),
			}

			out := exec.convertStreamChunksToNonStream(chunks)
			grounding := ojson.GetBytes(out, "response.candidates.0.groundingMetadata")
			if got := grounding.Get("webSearchQueries.0").String(); got != "cli proxy api" {
				t.Fatalf("expected webSearchQueries.0 = %q, got %q (raw=%s)", "cli proxy api", got, grounding.Raw)
			}
			if got := grounding.Get("groundingChunks.0.web.uri").String(); got != "https://example.com" {
				t.Fatalf("expected groundingChunks.0.web.uri = %q, got %q (raw=%s)", "https://example.com", got, grounding.Raw)
			}
			if got := grounding.Get("groundingSupports.0.groundingChunkIndices.0").Raw; got != "0" {
				t.Fatalf("expected groundingSupports.0.groundingChunkIndices.0 = 0, got %q (raw=%s)", got, grounding.Raw)
			}
		})
	}
}

func TestAntigravityConvertStreamChunksToNonStream_PreservesEarlierEmptyGroundingMetadataWhenTrailingChunkOmitted(t *testing.T) {
	exec := &AntigravityExecutor{}
	chunks := [][]byte{
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Plain answer."}]},"groundingMetadata":{}}],"modelVersion":"gemini-3-pro","responseId":"resp-ground-early-empty"}}`),
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"totalTokenCount":13}}}`),
	}

	out := exec.convertStreamChunksToNonStream(chunks)
	grounding := ojson.GetBytes(out, "response.candidates.0.groundingMetadata")
	if !grounding.Exists() || grounding.Raw != "{}" {
		t.Fatalf("expected groundingMetadata = {}, got exists=%v raw=%q", grounding.Exists(), grounding.Raw)
	}
}

func TestAntigravityConvertStreamChunksToNonStream_UsesLatestNonEmptyGroundingMetadata(t *testing.T) {
	exec := &AntigravityExecutor{}
	chunks := [][]byte{
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"First "}]},"groundingMetadata":{"webSearchQueries":["initial query"],"groundingChunks":[{"web":{"uri":"https://first.example","title":"First"}}]}}],"modelVersion":"gemini-3-pro","responseId":"resp-ground-latest"}}`),
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Second"}]},"groundingMetadata":{"webSearchQueries":["cli proxy api"],"groundingChunks":[{"web":{"uri":"https://latest.example","title":"Latest"}}]}}],"modelVersion":"gemini-3-pro","responseId":"resp-ground-latest"}}`),
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP","groundingMetadata":{}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5,"totalTokenCount":17}}}`),
	}

	out := exec.convertStreamChunksToNonStream(chunks)
	grounding := ojson.GetBytes(out, "response.candidates.0.groundingMetadata")
	if got := grounding.Get("webSearchQueries.0").String(); got != "cli proxy api" {
		t.Fatalf("expected webSearchQueries.0 = %q, got %q (raw=%s)", "cli proxy api", got, grounding.Raw)
	}
	if got := grounding.Get("groundingChunks.0.web.uri").String(); got != "https://latest.example" {
		t.Fatalf("expected groundingChunks.0.web.uri = %q, got %q (raw=%s)", "https://latest.example", got, grounding.Raw)
	}
}

func TestAntigravityConvertStreamChunksToNonStream_PreservesEmptyGroundingMetadataWhenNoNonEmptySeen(t *testing.T) {
	exec := &AntigravityExecutor{}
	chunks := [][]byte{
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Plain answer."}]}}],"modelVersion":"gemini-3-pro","responseId":"resp-ground-only-empty"}}`),
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP","groundingMetadata":{}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"totalTokenCount":13}}}`),
	}

	out := exec.convertStreamChunksToNonStream(chunks)
	grounding := ojson.GetBytes(out, "response.candidates.0.groundingMetadata")
	if !grounding.Exists() || grounding.Raw != "{}" {
		t.Fatalf("expected groundingMetadata = {}, got exists=%v raw=%q", grounding.Exists(), grounding.Raw)
	}
}
