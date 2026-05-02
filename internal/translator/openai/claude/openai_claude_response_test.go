package claude

import (
	"bytes"
	"context"
	"testing"
)

func TestConvertOpenAIResponseToClaude_StreamSkipsEmptyToolName(t *testing.T) {
	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Skill"}]}`)
	var param any

	chunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"","arguments":"{\"skill\":\"superpowers:using-superpowers\",\"args\":\"\"}"}}]},"finish_reason":null}]}`),
		&param,
	)

	joined := bytes.Join(chunks, []byte("\n"))
	if bytes.Contains(joined, []byte(`"type":"tool_use"`)) {
		t.Fatalf("Did not expect tool_use block for empty tool name. Output: %s", string(joined))
	}

	doneChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: [DONE]`),
		&param,
	)
	doneJoined := bytes.Join(doneChunks, []byte("\n"))
	if bytes.Contains(doneJoined, []byte(`"type":"tool_use"`)) {
		t.Fatalf("Did not expect tool_use block on DONE for empty tool name. Output: %s", string(doneJoined))
	}
}

func TestConvertOpenAIResponseToClaude_StreamAllowsDelayedToolName(t *testing.T) {
	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Skill"}]}`)
	var param any

	firstChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"arguments":"{\"skill\":\"superpowers:using-superpowers\",\"args\":\"\"}"}}]},"finish_reason":null}]}`),
		&param,
	)

	if bytes.Contains(bytes.Join(firstChunks, []byte("\n")), []byte(`"type":"tool_use"`)) {
		t.Fatalf("Did not expect tool_use block before tool name arrives. Output: %s", string(bytes.Join(firstChunks, []byte("\n"))))
	}

	secondChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"Skill"}}]},"finish_reason":null}]}`),
		&param,
	)

	secondJoined := bytes.Join(secondChunks, []byte("\n"))
	if !bytes.Contains(secondJoined, []byte(`"type":"tool_use"`)) {
		t.Fatalf("Expected tool_use block after delayed tool name arrives. Output: %s", string(secondJoined))
	}
	if !bytes.Contains(secondJoined, []byte(`"name":"Skill"`)) {
		t.Fatalf("Expected delayed tool name to be preserved. Output: %s", string(secondJoined))
	}

	doneChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: [DONE]`),
		&param,
	)
	doneJoined := bytes.Join(doneChunks, []byte("\n"))
	if !bytes.Contains(doneJoined, []byte(`"type":"input_json_delta"`)) {
		t.Fatalf("Expected delayed tool arguments to be emitted as input_json_delta. Output: %s", string(doneJoined))
	}
	if !bytes.Contains(doneJoined, []byte(`superpowers:using-superpowers`)) {
		t.Fatalf("Expected delayed tool arguments to be emitted on stream completion. Output: %s", string(doneJoined))
	}
}

func TestConvertOpenAIResponseToClaude_StreamEmptyToolNameDoesNotForceToolUseStopReason(t *testing.T) {
	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Skill"}]}`)
	var param any

	_ = ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"","arguments":"{\"skill\":\"superpowers:using-superpowers\"}"}}]},"finish_reason":null}]}`),
		&param,
	)

	doneChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
		&param,
	)

	doneJoined := bytes.Join(doneChunks, []byte("\n"))
	if bytes.Contains(doneJoined, []byte(`"stop_reason":"tool_use"`)) {
		t.Fatalf("Did not expect tool_use stop_reason when no valid tool_use block was emitted. Output: %s", string(doneJoined))
	}
}

func TestConvertOpenAIResponseToClaude_StreamEmptyToolNameDoneDoesNotForceToolUseStopReason(t *testing.T) {
	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Skill"}]}`)
	var param any

	_ = ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"","arguments":"{\"skill\":\"superpowers:using-superpowers\"}"}}]},"finish_reason":null}]}`),
		&param,
	)

	_ = ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
		&param,
	)

	doneChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: [DONE]`),
		&param,
	)

	doneJoined := bytes.Join(doneChunks, []byte("\n"))
	if bytes.Contains(doneJoined, []byte(`"stop_reason":"tool_use"`)) {
		t.Fatalf("Did not expect tool_use stop_reason on DONE when no valid tool_use block was emitted. Output: %s", string(doneJoined))
	}
}

func TestConvertOpenAIResponseToClaude_StreamValidToolNameDonePreservesToolUseStopReason(t *testing.T) {
	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Skill"}]}`)
	var param any

	_ = ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Skill","arguments":"{\"skill\":\"superpowers:using-superpowers\"}"}}]},"finish_reason":null}]}`),
		&param,
	)

	_ = ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
		&param,
	)

	doneChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: [DONE]`),
		&param,
	)

	doneJoined := bytes.Join(doneChunks, []byte("\n"))
	if !bytes.Contains(doneJoined, []byte(`"stop_reason":"tool_use"`)) {
		t.Fatalf("Expected tool_use stop_reason on DONE when a valid tool_use block was emitted. Output: %s", string(doneJoined))
	}
}

func TestConvertOpenAIResponseToClaude_StreamStopFinishReasonPreservesToolUseWithUsage(t *testing.T) {
	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Skill"}]}`)
	var param any

	_ = ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Skill","arguments":"{\"a\":1}"}}]},"finish_reason":null}]}`),
		&param,
	)

	chunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`),
		&param,
	)

	joined := bytes.Join(chunks, []byte("\n"))
	if !bytes.Contains(joined, []byte(`"stop_reason":"tool_use"`)) {
		t.Fatalf("Expected tool_use stop_reason for finish_reason=stop after valid tool_use block. Output: %s", string(joined))
	}
}

func TestConvertOpenAIResponseToClaude_StreamStopFinishReasonPreservesToolUseOnDone(t *testing.T) {
	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Skill"}]}`)
	var param any

	_ = ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Skill","arguments":"{\"a\":1}"}}]},"finish_reason":null}]}`),
		&param,
	)

	_ = ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
		&param,
	)

	doneChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: [DONE]`),
		&param,
	)

	doneJoined := bytes.Join(doneChunks, []byte("\n"))
	if !bytes.Contains(doneJoined, []byte(`"stop_reason":"tool_use"`)) {
		t.Fatalf("Expected tool_use stop_reason on DONE for finish_reason=stop after valid tool_use block. Output: %s", string(doneJoined))
	}
}

func TestConvertOpenAIResponseToClaude_StreamFunctionCallFinishReasonPreservesToolUseWithUsage(t *testing.T) {
	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Skill"}]}`)
	var param any

	_ = ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Skill","arguments":"{\"a\":1}"}}]},"finish_reason":null}]}`),
		&param,
	)

	chunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{},"finish_reason":"function_call"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`),
		&param,
	)

	joined := bytes.Join(chunks, []byte("\n"))
	if !bytes.Contains(joined, []byte(`"stop_reason":"tool_use"`)) {
		t.Fatalf("Expected tool_use stop_reason for finish_reason=function_call after valid tool_use block. Output: %s", string(joined))
	}
}

func TestConvertOpenAIResponseToClaude_StreamFunctionCallFinishReasonPreservesToolUseOnDone(t *testing.T) {
	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Skill"}]}`)
	var param any

	_ = ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Skill","arguments":"{\"a\":1}"}}]},"finish_reason":null}]}`),
		&param,
	)

	_ = ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{},"finish_reason":"function_call"}]}`),
		&param,
	)

	doneChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: [DONE]`),
		&param,
	)

	doneJoined := bytes.Join(doneChunks, []byte("\n"))
	if !bytes.Contains(doneJoined, []byte(`"stop_reason":"tool_use"`)) {
		t.Fatalf("Expected tool_use stop_reason on DONE for finish_reason=function_call after valid tool_use block. Output: %s", string(doneJoined))
	}
}

func TestConvertOpenAIResponseToClaude_StreamLengthFinishReasonPreservesMaxTokensWithUsage(t *testing.T) {
	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Skill"}]}`)
	var param any

	_ = ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Skill","arguments":"{\"a\":1"}}]},"finish_reason":null}]}`),
		&param,
	)

	chunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`),
		&param,
	)

	joined := bytes.Join(chunks, []byte("\n"))
	if !bytes.Contains(joined, []byte(`"stop_reason":"max_tokens"`)) {
		t.Fatalf("Expected max_tokens stop_reason for finish_reason=length. Output: %s", string(joined))
	}
	if bytes.Contains(joined, []byte(`"stop_reason":"tool_use"`)) {
		t.Fatalf("Did not expect tool_use stop_reason for finish_reason=length. Output: %s", string(joined))
	}
}

func TestConvertOpenAIResponseToClaude_StreamLengthFinishReasonPreservesMaxTokensOnDone(t *testing.T) {
	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Skill"}]}`)
	var param any

	_ = ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Skill","arguments":"{\"a\":1"}}]},"finish_reason":null}]}`),
		&param,
	)

	_ = ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: {"id":"resp_1","model":"gpt-5.4","created":1,"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`),
		&param,
	)

	doneChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"claude-opus-4-6",
		originalRequest,
		nil,
		[]byte(`data: [DONE]`),
		&param,
	)

	doneJoined := bytes.Join(doneChunks, []byte("\n"))
	if !bytes.Contains(doneJoined, []byte(`"stop_reason":"max_tokens"`)) {
		t.Fatalf("Expected max_tokens stop_reason on DONE for finish_reason=length. Output: %s", string(doneJoined))
	}
	if bytes.Contains(doneJoined, []byte(`"stop_reason":"tool_use"`)) {
		t.Fatalf("Did not expect tool_use stop_reason on DONE for finish_reason=length. Output: %s", string(doneJoined))
	}
}

func TestConvertOpenAIResponseToClaudeNonStream_EmptyToolNameDoesNotPreserveToolUseStopReason(t *testing.T) {
	rawJSON := []byte(`{
		"id":"resp_1",
		"model":"gpt-5.4",
		"choices":[
			{
				"index":0,
				"message":{
					"role":"assistant",
					"content":"",
					"tool_calls":[
						{
							"id":"call_1",
							"type":"function",
							"function":{
								"name":"",
								"arguments":"{\"skill\":\"superpowers:using-superpowers\"}"
							}
						}
					]
				},
				"finish_reason":"tool_calls"
			}
		],
		"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
	}`)

	out := ConvertOpenAIResponseToClaudeNonStream(context.Background(), "claude-opus-4-6", []byte(`{"tools":[{"name":"Skill"}]}`), nil, rawJSON, nil)
	if bytes.Contains(out, []byte(`"type":"tool_use"`)) {
		t.Fatalf("Did not expect tool_use block for empty tool name. Output: %s", string(out))
	}
	if bytes.Contains(out, []byte(`"stop_reason":"tool_use"`)) {
		t.Fatalf("Did not expect tool_use stop_reason when no valid tool_use block exists. Output: %s", string(out))
	}
}

func TestConvertOpenAIResponseToClaudeNonStream_MapsToolNameFromOriginalRequest(t *testing.T) {
	rawJSON := []byte(`{
		"id":"resp_1",
		"model":"gpt-5.4",
		"choices":[
			{
				"index":0,
				"message":{
					"content":"done",
					"tool_calls":[
						{
							"id":"call_1",
							"type":"function",
							"function":{
								"name":"skill",
								"arguments":"{}"
							}
						}
					]
				},
				"finish_reason":"tool_calls"
			}
		],
		"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
	}`)

	out := ConvertOpenAIResponseToClaudeNonStream(
		context.Background(),
		"claude-opus-4-6",
		[]byte(`{"tools":[{"name":"Skill"}]}`),
		nil,
		rawJSON,
		nil,
	)
	if !bytes.Contains(out, []byte(`"name":"Skill"`)) {
		t.Fatalf("Expected tool name to be restored from original request casing. Output: %s", string(out))
	}
}
