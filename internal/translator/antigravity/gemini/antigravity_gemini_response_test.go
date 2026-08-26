package gemini

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
)

func TestRestoreUsageMetadata(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "cpaUsageMetadata renamed to usageMetadata",
			input:    []byte(`{"modelVersion":"gemini-3-pro","cpaUsageMetadata":{"promptTokenCount":100,"candidatesTokenCount":200}}`),
			expected: `{"modelVersion":"gemini-3-pro","usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":200}}`,
		},
		{
			name:     "no cpaUsageMetadata unchanged",
			input:    []byte(`{"modelVersion":"gemini-3-pro","usageMetadata":{"promptTokenCount":100}}`),
			expected: `{"modelVersion":"gemini-3-pro","usageMetadata":{"promptTokenCount":100}}`,
		},
		{
			name:     "empty input",
			input:    []byte(`{}`),
			expected: `{}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := restoreUsageMetadata(tt.input)
			if string(result) != tt.expected {
				t.Errorf("restoreUsageMetadata() = %s, want %s", string(result), tt.expected)
			}
		})
	}
}

func TestConvertAntigravityResponseToGeminiNonStream(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "cpaUsageMetadata restored in response",
			input:    []byte(`{"response":{"modelVersion":"gemini-3-pro","cpaUsageMetadata":{"promptTokenCount":100}}}`),
			expected: `{"modelVersion":"gemini-3-pro","usageMetadata":{"promptTokenCount":100}}`,
		},
		{
			name:     "usageMetadata preserved",
			input:    []byte(`{"response":{"modelVersion":"gemini-3-pro","usageMetadata":{"promptTokenCount":100}}}`),
			expected: `{"modelVersion":"gemini-3-pro","usageMetadata":{"promptTokenCount":100}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertAntigravityResponseToGeminiNonStream(context.Background(), "", nil, nil, tt.input, nil)
			if string(result) != tt.expected {
				t.Errorf("ConvertAntigravityResponseToGeminiNonStream() = %s, want %s", string(result), tt.expected)
			}
		})
	}
}

func TestConvertAntigravityResponseToGeminiNonStreamRestoresDisambiguatedName(t *testing.T) {
	first := "mcp__plugin_cloudflare_cloudflare-builds__workers_builds_get_build"
	second := "mcp__plugin_cloudflare_cloudflare-builds__workers_builds_get_build_logs"
	original := []byte(`{"tools":[{"functionDeclarations":[{"name":"` + first + `"},{"name":"` + second + `"}]}]}`)
	mapped := util.SanitizedFunctionNameMap(original)[second]
	raw := []byte(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"` + mapped + `","args":{}}}]}}]}}`)

	out := ConvertAntigravityResponseToGeminiNonStream(context.Background(), "", original, nil, raw, nil)
	if got := gjson.GetBytes(out, "candidates.0.content.parts.0.functionCall.name").String(); got != second {
		t.Fatalf("functionCall.name = %q, want %q. Output: %s", got, second, out)
	}
}

func TestConvertAntigravityResponseToGeminiStream_SynthesizesFinishReasonOnDoneWhenOmitted(t *testing.T) {
	ctx := context.WithValue(context.Background(), "alt", "")
	var param any

	// Pure thinking chunk with 0 output tokens and NO finishReason (reproducing real upstream Antigravity logs)
	chunk1 := []byte(`data: {"response":{"candidates":[{"content":{"parts":[{"thought":true,"text":"Thinking..."}],"role":"model"}}],"usageMetadata":{"promptTokenCount":100,"totalTokenCount":150,"thoughtsTokenCount":50},"modelVersion":"gemini-3.7-flash","responseId":"resp-123"}}`)
	results1 := ConvertAntigravityResponseToGemini(ctx, "gemini-3.7-flash", nil, nil, chunk1, &param)
	if len(results1) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(results1))
	}
	if fr := gjson.GetBytes(results1[0], "candidates.0.finishReason").String(); fr != "" {
		t.Fatalf("chunk1 should not have finishReason yet, got %q", fr)
	}

	// Upstream finishes cleanly with [DONE]
	doneResults := ConvertAntigravityResponseToGemini(ctx, "gemini-3.7-flash", nil, nil, []byte("[DONE]"), &param)
	if len(doneResults) != 1 {
		t.Fatalf("expected synthetic terminal chunk on [DONE], got %d results", len(doneResults))
	}
	synthetic := doneResults[0]
	if fr := gjson.GetBytes(synthetic, "candidates.0.finishReason").String(); fr != "STOP" {
		t.Fatalf("synthetic chunk finishReason = %q, want STOP", fr)
	}
	// Upstream terminal chunks always carry a model-role candidate with an empty
	// text part, so the synthetic chunk mirrors that shape for client compatibility.
	if role := gjson.GetBytes(synthetic, "candidates.0.content.role").String(); role != "model" {
		t.Fatalf("synthetic chunk content role = %q, want model", role)
	}
	if parts := gjson.GetBytes(synthetic, "candidates.0.content.parts"); parts.String() != `[{"text":""}]` {
		t.Fatalf("synthetic chunk parts = %s, want [{\"text\":\"\"}]", parts.Raw)
	}
	// The last real chunk carries usage, so the synthetic terminal chunk must keep it.
	if got := gjson.GetBytes(synthetic, "usageMetadata.promptTokenCount").Int(); got != 100 {
		t.Fatalf("synthetic chunk promptTokenCount = %d, want 100", got)
	}
	if got := gjson.GetBytes(synthetic, "usageMetadata.totalTokenCount").Int(); got != 150 {
		t.Fatalf("synthetic chunk totalTokenCount = %d, want 150", got)
	}
	if mv := gjson.GetBytes(synthetic, "modelVersion").String(); mv != "gemini-3.7-flash" {
		t.Fatalf("synthetic chunk modelVersion = %q, want gemini-3.7-flash", mv)
	}
	if rid := gjson.GetBytes(synthetic, "responseId").String(); rid != "resp-123" {
		t.Fatalf("synthetic chunk responseId = %q, want resp-123", rid)
	}

	// Repeated [DONE] should not emit another chunk
	dupResults := ConvertAntigravityResponseToGemini(ctx, "gemini-3.7-flash", nil, nil, []byte("[DONE]"), &param)
	if len(dupResults) != 0 {
		t.Fatalf("duplicate [DONE] should yield 0 results, got %d", len(dupResults))
	}
}

func TestConvertAntigravityResponseToGeminiStream_DoesNotDuplicateFinishReasonWhenPresent(t *testing.T) {
	ctx := context.WithValue(context.Background(), "alt", "")
	var param any

	// Chunk with explicit STOP finishReason
	chunk1 := []byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":"STOP"}],"modelVersion":"gemini-3.7-flash"}}`)
	results1 := ConvertAntigravityResponseToGemini(ctx, "gemini-3.7-flash", nil, nil, chunk1, &param)
	if len(results1) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(results1))
	}
	if fr := gjson.GetBytes(results1[0], "candidates.0.finishReason").String(); fr != "STOP" {
		t.Fatalf("chunk1 finishReason = %q, want STOP", fr)
	}

	// [DONE] should not emit a second chunk
	doneResults := ConvertAntigravityResponseToGemini(ctx, "gemini-3.7-flash", nil, nil, []byte("[DONE]"), &param)
	if len(doneResults) != 0 {
		t.Fatalf("expected 0 results on [DONE] when finishReason already observed, got %d", len(doneResults))
	}
}

func TestConvertAntigravityResponseToGeminiStream_CarriesFilteredUsageIntoSyntheticTerminal(t *testing.T) {
	ctx := context.WithValue(context.Background(), "alt", "")
	var param any

	// Real streams reach the translator after FilterSSEUsageMetadata, which renames
	// non-terminal usage to cpaUsageMetadata because finishReason is missing. The
	// filtered payload is inlined here so this package keeps no dependency on the
	// executor helpers; the executor tests cover the real filter end to end.
	filtered := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"}}],"modelVersion":"gemini-3.7-flash","responseId":"resp-filtered","cpaUsageMetadata":{"promptTokenCount":42,"candidatesTokenCount":7,"totalTokenCount":49}}}`)
	results := ConvertAntigravityResponseToGemini(ctx, "gemini-3.7-flash", nil, nil, filtered, &param)
	if len(results) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(results))
	}
	if got := gjson.GetBytes(results[0], "usageMetadata.totalTokenCount").Int(); got != 49 {
		t.Fatalf("restored chunk totalTokenCount = %d, want 49", got)
	}

	doneResults := ConvertAntigravityResponseToGemini(ctx, "gemini-3.7-flash", nil, nil, []byte("[DONE]"), &param)
	if len(doneResults) != 1 {
		t.Fatalf("expected synthetic terminal chunk, got %d results", len(doneResults))
	}
	if fr := gjson.GetBytes(doneResults[0], "candidates.0.finishReason").String(); fr != "STOP" {
		t.Fatalf("synthetic finishReason = %q, want STOP", fr)
	}
	if got := gjson.GetBytes(doneResults[0], "usageMetadata.totalTokenCount").Int(); got != 49 {
		t.Fatalf("synthetic totalTokenCount = %d, want 49", got)
	}
	if got := gjson.GetBytes(doneResults[0], "usageMetadata.candidatesTokenCount").Int(); got != 7 {
		t.Fatalf("synthetic candidatesTokenCount = %d, want 7", got)
	}
}

func TestConvertAntigravityResponseToGeminiStream_DoesNotFinalizeEmptyUpstreamStream(t *testing.T) {
	ctx := context.WithValue(context.Background(), "alt", "")
	var param any

	// A 200 response whose body carried no response chunk must not be reported as
	// a successful empty completion.
	doneResults := ConvertAntigravityResponseToGemini(ctx, "gemini-3.7-flash", nil, nil, []byte("[DONE]"), &param)
	if len(doneResults) != 0 {
		t.Fatalf("empty stream must not synthesize a terminal chunk, got %d results: %s", len(doneResults), doneResults[0])
	}
}

func TestConvertAntigravityResponseToGeminiStream_ResponseFreeEventDoesNotStartStream(t *testing.T) {
	ctx := context.WithValue(context.Background(), "alt", "")
	var param any

	// A JSON keepalive or malformed envelope carries no candidates and no usage,
	// so it must not make an otherwise empty stream look complete.
	for _, chunk := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"response":{}}`),
		[]byte(`{"response":{"candidates":[]}}`),
		[]byte(`{"response":{"candidates":[],"usageMetadata":{}}}`),
	} {
		ConvertAntigravityResponseToGemini(ctx, "gemini-3.7-flash", nil, nil, chunk, &param)
	}
	doneResults := ConvertAntigravityResponseToGemini(ctx, "gemini-3.7-flash", nil, nil, []byte("[DONE]"), &param)
	if len(doneResults) != 0 {
		t.Fatalf("response-free events must not synthesize a terminal chunk, got %d results: %s", len(doneResults), doneResults[0])
	}
}

func TestConvertAntigravityResponseToGeminiNonStream_DefaultsEveryCandidate(t *testing.T) {
	input := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"a"}],"role":"model"}},{"content":{"parts":[{"text":"b"}],"role":"model"},"finishReason":"MAX_TOKENS"},{"content":{"parts":[{"text":"c"}],"role":"model"}}]}}`)
	out := ConvertAntigravityResponseToGeminiNonStream(context.Background(), "", nil, nil, input, nil)
	want := []string{"STOP", "MAX_TOKENS", "STOP"}
	for i, expected := range want {
		got := gjson.GetBytes(out, fmt.Sprintf("candidates.%d.finishReason", i)).String()
		if got != expected {
			t.Fatalf("candidates.%d.finishReason = %q, want %q", i, got, expected)
		}
	}
}

func TestConvertAntigravityResponseToGeminiNonStream_DefaultsMissingFinishReason(t *testing.T) {
	input := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"Hi"}],"role":"model"}}]}}`)
	out := ConvertAntigravityResponseToGeminiNonStream(context.Background(), "", nil, nil, input, nil)
	if fr := gjson.GetBytes(out, "candidates.0.finishReason").String(); fr != "STOP" {
		t.Fatalf("ConvertAntigravityResponseToGeminiNonStream finishReason = %q, want STOP", fr)
	}
}
