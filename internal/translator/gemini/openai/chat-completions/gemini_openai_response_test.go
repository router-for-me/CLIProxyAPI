package chat_completions

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiResponseToOpenAI_PreservesReasoningContent(t *testing.T) {
	// Simulate Gemini streaming chunks with thought parts
	// Gemini sends SSE data: chunks with candidates[].content.parts[]
	geminiChunks := []string{
		// First chunk: thought part with signature (empty text)
		`data: {"candidates":[{"content":{"role":"model","parts":[{"thought":true,"thoughtSignature":"skip_thought_signature_validator","text":""}]}}],"modelVersion":"gemini-2.5-flash","responseId":"resp_1"}`,
		// Second chunk: thought part with actual text
		`data: {"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"Let me think about this step by step."}]}}],"modelVersion":"gemini-2.5-flash","responseId":"resp_1"}`,
		// Third chunk: thought part with more text
		`data: {"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":" First, I need to understand the problem."}]}}],"modelVersion":"gemini-2.5-flash","responseId":"resp_1"}`,
		// Fourth chunk: regular (non-thought) text part
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello! I've analyzed the problem."}]}}],"modelVersion":"gemini-2.5-flash","responseId":"resp_1"}`,
		// Fifth chunk: finish
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15,"thoughtsTokenCount":3},"modelVersion":"gemini-2.5-flash","responseId":"resp_1"}`,
	}

	var param any
	var allOutputs [][]byte
	for _, chunk := range geminiChunks {
		outputs := ConvertGeminiResponseToOpenAI(context.Background(), "gemini-2.5-flash", nil, nil, []byte(chunk), &param)
		allOutputs = append(allOutputs, outputs...)
	}

	// Collect all reasoning_content and content values
	var reasoningTexts []string
	var contentTexts []string
	for _, output := range allOutputs {
		result := gjson.ParseBytes(output)
		choices := result.Get("choices")
		if !choices.IsArray() {
			continue
		}
		choices.ForEach(func(_, choice gjson.Result) bool {
			rc := choice.Get("delta.reasoning_content")
			if rc.Exists() && rc.String() != "" {
				reasoningTexts = append(reasoningTexts, rc.String())
			}
			c := choice.Get("delta.content")
			if c.Exists() && c.String() != "" {
				contentTexts = append(contentTexts, c.String())
			}
			return true
		})
	}

	t.Logf("All outputs count: %d", len(allOutputs))
	t.Logf("Reasoning texts: %v", reasoningTexts)
	t.Logf("Content texts: %v", contentTexts)

	// Verify reasoning content is present
	if len(reasoningTexts) == 0 {
		t.Fatal("FAIL: reasoning_content is missing! Gemini thought parts were not converted to reasoning_content.")
	}

	// Verify content text is also present
	if len(contentTexts) == 0 {
		t.Fatal("FAIL: content is missing! Regular text parts were not converted to content.")
	}

	// Check that accumulated reasoning text is correct
	expectedReasoning := "Let me think about this step by step. First, I need to understand the problem."
	actualReasoning := strings.Join(reasoningTexts, "")
	if actualReasoning != expectedReasoning {
		t.Fatalf("FAIL: accumulated reasoning text mismatch.\n  expected: %q\n  got:      %q", expectedReasoning, actualReasoning)
	}

	expectedContent := "Hello! I've analyzed the problem."
	actualContent := strings.Join(contentTexts, "")
	if actualContent != expectedContent {
		t.Fatalf("FAIL: accumulated content text mismatch.\n  expected: %q\n  got:      %q", expectedContent, actualContent)
	}

	t.Log("PASS: reasoning_content is properly preserved!")
}

func TestConvertGeminiResponseToOpenAINonStream_PreservesReasoningContent(t *testing.T) {
	// Simulate a complete (non-streaming) Gemini response with thought parts
	geminiResp := `{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"Let me think step by step."},{"thought":true,"text":" First, analyze the problem."},{"text":"The answer is 42."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15,"thoughtsTokenCount":3},"modelVersion":"gemini-2.5-flash","responseId":"resp_1"}`

	result := ConvertGeminiResponseToOpenAINonStream(context.Background(), "gemini-2.5-flash", nil, nil, []byte(geminiResp), nil)

	t.Logf("Full non-streaming result: %s", string(result))

	rc := gjson.GetBytes(result, "choices.0.message.reasoning_content")
	content := gjson.GetBytes(result, "choices.0.message.content")

	t.Logf("reasoning_content: %q", rc.String())
	t.Logf("content: %q", content.String())

	if rc.String() == "" {
		t.Fatal("FAIL: non-streaming reasoning_content is empty!")
	}

	expectedReasoning := "Let me think step by step. First, analyze the problem."
	if rc.String() != expectedReasoning {
		t.Fatalf("FAIL: reasoning_content mismatch.\n  expected: %q\n  got:      %q", expectedReasoning, rc.String())
	}

	expectedContent := "The answer is 42."
	if content.String() != expectedContent {
		t.Fatalf("FAIL: content mismatch.\n  expected: %q\n  got:      %q", expectedContent, content.String())
	}

	t.Log("PASS: non-streaming reasoning_content is properly preserved!")
}

func TestConvertGeminiResponseToOpenAI_SignatureOnlyThoughtPartIsSkipped(t *testing.T) {
	// Test that signature-only thought parts are skipped (not emitted as empty chunks)
	geminiChunk := `data: {"candidates":[{"content":{"role":"model","parts":[{"thought":true,"thoughtSignature":"skip_thought_signature_validator","text":""}]}}],"modelVersion":"gemini-2.5-flash","responseId":"resp_1"}`

	var param any
	outputs := ConvertGeminiResponseToOpenAI(context.Background(), "gemini-2.5-flash", nil, nil, []byte(geminiChunk), &param)

	// The signature-only part should be skipped entirely (no output for this chunk)
	// But the usage/base template might still produce output
	hasReasoningContent := false
	for _, output := range outputs {
		result := gjson.ParseBytes(output)
		rc := result.Get("choices.0.delta.reasoning_content")
		if rc.Exists() && rc.String() != "" {
			hasReasoningContent = true
		}
	}

	if hasReasoningContent {
		t.Fatal("FAIL: signature-only thought part should be skipped, but reasoning_content was emitted")
	}

	t.Log("PASS: signature-only thought parts are correctly skipped")
}