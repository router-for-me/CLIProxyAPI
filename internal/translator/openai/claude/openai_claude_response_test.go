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
