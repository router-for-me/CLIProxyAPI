package claude

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestConvertAntigravityResponseToClaudeNonStream_MalformedFunctionCallWithoutToolUseReturnsAPIError(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`)
	rawJSON := []byte(`{
		"response": {
			"candidates": [{
				"content": {"role": "model", "parts": []},
				"finishReason": "MALFORMED_FUNCTION_CALL"
			}],
			"usageMetadata": {
				"promptTokenCount": 120,
				"candidatesTokenCount": 10,
				"thoughtsTokenCount": 313,
				"totalTokenCount": 443
			},
			"modelVersion": "claude-opus-5-5-high",
			"responseId": "req_antigravity_malformed"
		}
	}`)

	output := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, rawJSON, nil)
	if got := ojson.GetBytesNoCopy(output, "stop_reason").String(); got == "refusal" {
		t.Fatalf("unexpected stop_reason = %q, output: %s", got, output)
	}
	if got := ojson.GetBytesNoCopy(output, "type").String(); got != "error" {
		t.Fatalf("type = %q, want error; output: %s", got, output)
	}
	if got := ojson.GetBytesNoCopy(output, "error.type").String(); got != "api_error" {
		t.Fatalf("error.type = %q, want api_error; output: %s", got, output)
	}
	if !strings.Contains(ojson.GetBytesNoCopy(output, "error.message").String(), "MALFORMED_FUNCTION_CALL") {
		t.Fatalf("error.message = %q, want mention of MALFORMED_FUNCTION_CALL; output: %s", ojson.GetBytesNoCopy(output, "error.message").String(), output)
	}
}

func TestConvertAntigravityResponseToClaudeStream_MalformedFunctionCallEmitsAPIError(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`)

	t.Run("partless_with_usage", func(t *testing.T) {
		chunk := []byte(`{
			"response": {
				"candidates": [{
					"content": {"role": "model", "parts": []},
					"finishReason": "MALFORMED_FUNCTION_CALL"
				}],
				"usageMetadata": {
					"promptTokenCount": 120,
					"candidatesTokenCount": 10,
					"thoughtsTokenCount": 313,
					"totalTokenCount": 443
				},
				"modelVersion": "claude-opus-5-5-high",
				"responseId": "req_ag_stream_1"
			}
		}`)

		var param any
		output := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, chunk, &param), nil)
		output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
		outputText := string(output)

		if strings.Contains(outputText, `"stop_reason":"refusal"`) {
			t.Fatalf("unexpected stop_reason refusal in output:\n%s", outputText)
		}
		if strings.Contains(outputText, `"type":"message_delta"`) || strings.Contains(outputText, `"type":"message_stop"`) {
			t.Fatalf("unexpected normal completion events in output:\n%s", outputText)
		}
		if !strings.Contains(outputText, "event: error\n") || !strings.Contains(outputText, `"type":"api_error"`) {
			t.Fatalf("expected SSE api_error event in output:\n%s", outputText)
		}
	})

	t.Run("after_thinking_closes_block_before_error", func(t *testing.T) {
		chunk1 := []byte(`{
			"response": {
				"candidates": [{
					"content": {"role": "model", "parts": [{"thought": true, "text": "planning tool call"}]}
				}],
				"modelVersion": "claude-opus-5-5-high",
				"responseId": "req_ag_stream_2"
			}
		}`)
		chunk2 := []byte(`{
			"response": {
				"candidates": [{
					"content": {"role": "model", "parts": []},
					"finishReason": "MALFORMED_FUNCTION_CALL"
				}],
				"usageMetadata": {
					"promptTokenCount": 120,
					"candidatesTokenCount": 10,
					"thoughtsTokenCount": 313,
					"totalTokenCount": 443
				},
				"modelVersion": "claude-opus-5-5-high",
				"responseId": "req_ag_stream_2"
			}
		}`)

		var param any
		output := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, chunk1, &param), nil)
		output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, chunk2, &param), nil)...)
		output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
		outputText := string(output)

		stopIdx := strings.Index(outputText, "event: content_block_stop\n")
		errIdx := strings.Index(outputText, "event: error\n")
		if stopIdx < 0 || errIdx < 0 || stopIdx >= errIdx {
			t.Fatalf("expected content_block_stop before error event in output:\n%s", outputText)
		}
		if strings.Contains(outputText, `"type":"message_delta"`) || strings.Contains(outputText, `"type":"message_stop"`) {
			t.Fatalf("unexpected normal completion events after MALFORMED_FUNCTION_CALL:\n%s", outputText)
		}
	})

	t.Run("without_usage_and_with_duplicate_terminal", func(t *testing.T) {
		chunkWithoutUsage := []byte(`{
			"response": {
				"candidates": [{
					"content": {"role": "model", "parts": []},
					"finishReason": "MALFORMED_FUNCTION_CALL"
				}],
				"modelVersion": "claude-opus-5-5-high",
				"responseId": "req_ag_stream_3"
			}
		}`)
		duplicateChunk := []byte(`{
			"response": {
				"candidates": [{
					"content": {"role": "model", "parts": [{"text": "late text"}]},
					"finishReason": "MALFORMED_FUNCTION_CALL"
				}],
				"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 1, "totalTokenCount": 11},
				"modelVersion": "claude-opus-5-5-high",
				"responseId": "req_ag_stream_3"
			}
		}`)

		var param any
		out1 := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, chunkWithoutUsage, &param), nil)
		lateOut := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, duplicateChunk, &param), nil)
		doneOut := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)
		if len(lateOut) != 0 || len(doneOut) != 0 {
			t.Fatalf("expected no chunks after error event, got late=%q done=%q", lateOut, doneOut)
		}
		if got := strings.Count(string(out1), "event: error\n"); got != 1 {
			t.Fatalf("error event count = %d, want 1; output:\n%s", got, out1)
		}
	})

	t.Run("with_valid_tool_use_preserves_tool_use", func(t *testing.T) {
		toolReq := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`)
		rawJSON := []byte(`{
			"response": {
				"candidates": [{
					"content": {
						"role": "model",
						"parts": [{"functionCall": {"name": "Bash", "args": {"command": "ls"}}}]
					},
					"finishReason": "MALFORMED_FUNCTION_CALL"
				}],
				"usageMetadata": {"promptTokenCount": 50, "candidatesTokenCount": 10, "totalTokenCount": 60},
				"modelVersion": "claude-opus-5-5-high",
				"responseId": "req_ag_tool"
			}
		}`)

		nonStream := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "claude-opus-5-5-high", toolReq, toolReq, rawJSON, nil)
		if got := ojson.GetBytesNoCopy(nonStream, "stop_reason").String(); got != "tool_use" {
			t.Fatalf("non-stream stop_reason = %q, want tool_use; output: %s", got, nonStream)
		}

		var param any
		streamOut := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-opus-5-5-high", toolReq, toolReq, rawJSON, &param), nil)
		streamOut = append(streamOut, bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-opus-5-5-high", toolReq, toolReq, []byte("[DONE]"), &param), nil)...)
		streamText := string(streamOut)
		if strings.Contains(streamText, "event: error\n") {
			t.Fatalf("unexpected error event when valid functionCall is present:\n%s", streamText)
		}
		if !strings.Contains(streamText, `"stop_reason":"tool_use"`) {
			t.Fatalf("expected stop_reason tool_use in stream output:\n%s", streamText)
		}
	})

	t.Run("with_partial_visible_text_preserves_text", func(t *testing.T) {
		nonStreamRaw := []byte(`{
			"response": {
				"candidates": [{
					"content": {
						"role": "model",
						"parts": [{"text": "partial answer"}]
					},
					"finishReason": "MALFORMED_FUNCTION_CALL"
				}],
				"usageMetadata": {"promptTokenCount": 50, "candidatesTokenCount": 3, "totalTokenCount": 53},
				"modelVersion": "claude-opus-5-5-high",
				"responseId": "req_ag_partial"
			}
		}`)

		nonStream := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, nonStreamRaw, nil)
		if got := ojson.GetBytesNoCopy(nonStream, "type").String(); got != "message" {
			t.Fatalf("non-stream type = %q, want message preserving partial text; output: %s", got, nonStream)
		}
		if got := ojson.GetBytesNoCopy(nonStream, "content.0.text").String(); got != "partial answer" {
			t.Fatalf("non-stream text = %q, want partial answer; output: %s", got, nonStream)
		}
		if got := ojson.GetBytesNoCopy(nonStream, "stop_reason").String(); got != "end_turn" {
			t.Fatalf("non-stream stop_reason = %q, want end_turn; output: %s", got, nonStream)
		}

		chunk1 := []byte(`{
			"response": {
				"candidates": [{
					"content": {"role": "model", "parts": [{"text": "partial answer"}]}
				}],
				"modelVersion": "claude-opus-5-5-high",
				"responseId": "req_ag_partial"
			}
		}`)
		chunk2 := []byte(`{
			"response": {
				"candidates": [{
					"content": {"role": "model", "parts": []},
					"finishReason": "MALFORMED_FUNCTION_CALL"
				}],
				"usageMetadata": {"promptTokenCount": 50, "candidatesTokenCount": 3, "totalTokenCount": 53},
				"modelVersion": "claude-opus-5-5-high",
				"responseId": "req_ag_partial"
			}
		}`)

		var param any
		streamOut := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, chunk1, &param), nil)
		streamOut = append(streamOut, bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, chunk2, &param), nil)...)
		streamOut = append(streamOut, bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
		streamText := string(streamOut)
		if strings.Contains(streamText, "event: error\n") {
			t.Fatalf("unexpected error event when partial visible text was already emitted:\n%s", streamText)
		}
		if !strings.Contains(streamText, "partial answer") || !strings.Contains(streamText, `"type":"message_stop"`) {
			t.Fatalf("expected preserved partial text and message_stop in stream output:\n%s", streamText)
		}
	})

	t.Run("with_empty_functionCall_placeholder_still_returns_error", func(t *testing.T) {
		rawJSON := []byte(`{
			"response": {
				"candidates": [{
					"content": {"role": "model", "parts": [{"functionCall": {}}]},
					"finishReason": "MALFORMED_FUNCTION_CALL"
				}],
				"usageMetadata": {"promptTokenCount": 50, "candidatesTokenCount": 1, "totalTokenCount": 51},
				"modelVersion": "claude-opus-5-5-high",
				"responseId": "req_ag_empty_fc"
			}
		}`)

		nonStream := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, rawJSON, nil)
		if got := ojson.GetBytesNoCopy(nonStream, "type").String(); got != "error" {
			t.Fatalf("non-stream type = %q, want error for empty functionCall placeholder; output: %s", got, nonStream)
		}

		var param any
		streamOut := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, rawJSON, &param), nil)
		streamOut = append(streamOut, bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-opus-5-5-high", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
		if !strings.Contains(string(streamOut), "event: error\n") {
			t.Fatalf("expected error event for empty functionCall placeholder, got:\n%s", streamOut)
		}
	})
}
