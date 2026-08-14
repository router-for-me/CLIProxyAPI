package executor

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestDevinEncodeMetadata(t *testing.T) {
	meta := devinMetadata{
		IdeName:          "devin-cli",
		ExtensionVersion: "3000.4.25",
		ApiKey:           "devin-session-token$test",
		Locale:           "en",
		OS:               "darwin",
		IdeVersion:       "3000.4.25",
		ExtensionName:    "chisel",
		IdeType:          "chisel",
		F:                "abc123",
	}
	b := encodeMetadata(meta)
	if len(b) == 0 {
		t.Fatal("encodeMetadata returned empty bytes")
	}

	// Verify field 1 (ide_name) contains "devin-cli".
	val := extractStringField(t, b, 1)
	if val != "devin-cli" {
		t.Errorf("field 1: got %q, want %q", val, "devin-cli")
	}

	// Verify field 12 (extension_name) contains "chisel".
	val = extractStringField(t, b, 12)
	if val != "chisel" {
		t.Errorf("field 12: got %q, want %q", val, "chisel")
	}

	// Verify field 31 (f) contains "abc123".
	val = extractStringField(t, b, 31)
	if val != "abc123" {
		t.Errorf("field 31: got %q, want %q", val, "abc123")
	}
}

func TestDevinEncodeCompletionConfig(t *testing.T) {
	cfg := devinCompletionConfig{
		NumCompletions: 1,
		MaxTokens:      128000,
		MaxNewlines:    400,
		Temperature:    1.0,
		TopK:           40,
		TopP:           0.95,
	}
	b := encodeCompletionConfig(cfg)
	if len(b) == 0 {
		t.Fatal("encodeCompletionConfig returned empty bytes")
	}

	// Verify field 1 (num_completions) = 1.
	val := extractVarintField(t, b, 1)
	if val != 1 {
		t.Errorf("field 1: got %d, want 1", val)
	}

	// Verify field 2 (max_tokens) = 128000.
	val = extractVarintField(t, b, 2)
	if val != 128000 {
		t.Errorf("field 2: got %d, want 128000", val)
	}
}

func TestDevinFrameConnectMessage(t *testing.T) {
	payload := []byte("hello world")
	frame := frameConnectMessage(payload)
	if len(frame) != 5+len(payload) {
		t.Fatalf("frame length: got %d, want %d", len(frame), 5+len(payload))
	}
	if frame[0] != connectFlagUncompressed {
		t.Errorf("flag: got %d, want %d", frame[0], connectFlagUncompressed)
	}
	// Check length in big-endian.
	length := uint32(frame[1])<<24 | uint32(frame[2])<<16 | uint32(frame[3])<<8 | uint32(frame[4])
	if int(length) != len(payload) {
		t.Errorf("length: got %d, want %d", length, len(payload))
	}
	// Check payload.
	if string(frame[5:]) != string(payload) {
		t.Errorf("payload mismatch")
	}
}

func TestDevinReadConnectFrame(t *testing.T) {
	payload := []byte("test payload")
	frame := frameConnectMessage(payload)

	parsed, remaining, err := readConnectFrame(frame)
	if err != nil {
		t.Fatalf("readConnectFrame error: %v", err)
	}
	if parsed.Flag != connectFlagUncompressed {
		t.Errorf("flag: got %d, want %d", parsed.Flag, connectFlagUncompressed)
	}
	if string(parsed.Payload) != string(payload) {
		t.Errorf("payload: got %q, want %q", string(parsed.Payload), string(payload))
	}
	if len(remaining) != 0 {
		t.Errorf("remaining: got %d bytes, want 0", len(remaining))
	}
}

func TestDevinReadConnectFrameMultipleFrames(t *testing.T) {
	frame1 := frameConnectMessage([]byte("first"))
	frame2 := frameConnectMessage([]byte("second"))
	combined := append(frame1, frame2...)

	parsed1, remaining, err := readConnectFrame(combined)
	if err != nil {
		t.Fatalf("first read error: %v", err)
	}
	if string(parsed1.Payload) != "first" {
		t.Errorf("first payload: got %q, want %q", string(parsed1.Payload), "first")
	}

	parsed2, remaining2, err := readConnectFrame(remaining)
	if err != nil {
		t.Fatalf("second read error: %v", err)
	}
	if string(parsed2.Payload) != "second" {
		t.Errorf("second payload: got %q, want %q", string(parsed2.Payload), "second")
	}
	if len(remaining2) != 0 {
		t.Errorf("remaining2: got %d bytes, want 0", len(remaining2))
	}
}

func TestDevinReadConnectFrameTooShort(t *testing.T) {
	_, _, err := readConnectFrame([]byte{0x00, 0x00, 0x00})
	if err == nil {
		t.Fatal("expected error for short buffer, got nil")
	}
}

func TestDevinParseGetChatMessageResponse(t *testing.T) {
	// Build a response with delta_text (field 3) and message_id (field 1).
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, "bot-test-message-id")
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendString(b, "pong")
	b = protowire.AppendTag(b, 5, protowire.VarintType)
	b = protowire.AppendVarint(b, devinStopReasonStopPattern)

	chunk, err := parseGetChatMessageResponse(b)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if chunk.MessageID != "bot-test-message-id" {
		t.Errorf("message_id: got %q, want %q", chunk.MessageID, "bot-test-message-id")
	}
	if chunk.DeltaText != "pong" {
		t.Errorf("delta_text: got %q, want %q", chunk.DeltaText, "pong")
	}
	if chunk.StopReason != devinStopReasonStopPattern {
		t.Errorf("stop_reason: got %d, want %d", chunk.StopReason, devinStopReasonStopPattern)
	}
}

func TestDevinParseUsageStats(t *testing.T) {
	var b []byte
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	b = protowire.AppendVarint(b, 42)
	b = protowire.AppendTag(b, 3, protowire.VarintType)
	b = protowire.AppendVarint(b, 3)
	b = protowire.AppendTag(b, 5, protowire.VarintType)
	b = protowire.AppendVarint(b, 18560)
	b = protowire.AppendTag(b, 9, protowire.BytesType)
	b = protowire.AppendString(b, "glm-5-2")

	usage, err := parseUsageStats(b)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if usage.InputTokens != 42 {
		t.Errorf("input_tokens: got %d, want 42", usage.InputTokens)
	}
	if usage.OutputTokens != 3 {
		t.Errorf("output_tokens: got %d, want 3", usage.OutputTokens)
	}
	if usage.CacheReadTokens != 18560 {
		t.Errorf("cache_read_tokens: got %d, want 18560", usage.CacheReadTokens)
	}
	if usage.ModelUID != "glm-5-2" {
		t.Errorf("model_uid: got %q, want %q", usage.ModelUID, "glm-5-2")
	}
}

func TestDevinStopReasonToString(t *testing.T) {
	tests := []struct {
		reason int
		want   string
	}{
		{devinStopReasonStopPattern, "stop"},
		{devinStopReasonMaxTokens, "length"},
		{devinStopReasonMaxNewlines, "length"},
		{devinStopReasonIncomplete, "length"},
		{devinStopReasonUnspecified, "stop"},
	}
	for _, tt := range tests {
		got := devinStopReasonToString(tt.reason)
		if got != tt.want {
			t.Errorf("devinStopReasonToString(%d): got %q, want %q", tt.reason, got, tt.want)
		}
	}
}

func TestDevinNormalizeSessionToken(t *testing.T) {
	// Token without prefix gets the prefix added.
	got := normalizeDevinSessionToken("mytoken")
	want := "devin-session-token$mytoken"
	if got != want {
		t.Errorf("normalize without prefix: got %q, want %q", got, want)
	}

	// Token with prefix stays unchanged.
	got = normalizeDevinSessionToken("devin-session-token$mytoken")
	if got != want {
		t.Errorf("normalize with prefix: got %q, want %q", got, want)
	}

	// Empty token stays empty.
	got = normalizeDevinSessionToken("")
	if got != "" {
		t.Errorf("normalize empty: got %q, want empty", got)
	}
}

func TestDevinGenerateAttestationF(t *testing.T) {
	f := generateAttestationF("test-install-id")
	if len(f) != 732 {
		t.Fatalf("attestation length: got %d, want 732", len(f))
	}
	// Should be hex characters.
	for _, c := range f {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("attestation contains non-hex character: %c", c)
		}
	}

	// Same input should produce same output (deterministic).
	f2 := generateAttestationF("test-install-id")
	if f != f2 {
		t.Error("attestation is not deterministic")
	}

	// Different input should produce different output.
	f3 := generateAttestationF("different-install-id")
	if f == f3 {
		t.Error("different inputs produced same attestation")
	}
}

func TestDevinParseOpenAIRequest(t *testing.T) {
	payload := []byte(`{
		"model": "glm-5-2",
		"messages": [
			{"role": "system", "content": "You are a test assistant."},
			{"role": "user", "content": "reply with just: pong"}
		],
		"stream": true,
		"temperature": 0.5,
		"max_tokens": 1000
	}`)

	parsed, err := devinParseOpenAIRequest(payload)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if parsed.Model != "glm-5-2" {
		t.Errorf("model: got %q, want %q", parsed.Model, "glm-5-2")
	}
	if len(parsed.Messages) != 2 {
		t.Fatalf("messages: got %d, want 2", len(parsed.Messages))
	}
	if parsed.Messages[0].Role != "system" {
		t.Errorf("message 0 role: got %q, want %q", parsed.Messages[0].Role, "system")
	}
	if parsed.Messages[1].Role != "user" {
		t.Errorf("message 1 role: got %q, want %q", parsed.Messages[1].Role, "user")
	}
	if parsed.SystemPrompt != "You are a test assistant." {
		t.Errorf("system prompt: got %q, want %q", parsed.SystemPrompt, "You are a test assistant.")
	}
	if !parsed.Stream {
		t.Error("stream: got false, want true")
	}
	if parsed.Temperature == nil || *parsed.Temperature != 0.5 {
		t.Errorf("temperature: got %v, want 0.5", parsed.Temperature)
	}
	if parsed.MaxTokens == nil || *parsed.MaxTokens != 1000 {
		t.Errorf("max_tokens: got %v, want 1000", parsed.MaxTokens)
	}
}

func TestDevinBuildDevinRequest(t *testing.T) {
	temp := 0.7
	maxTok := 500
	parsed := &devinParsedRequest{
		Model: "glm-5-2",
		Messages: []devinOpenAIMessage{
			{Role: "system", Content: json.RawMessage(`"You are a test."`)},
			{Role: "user", Content: json.RawMessage(`"hello"`)},
		},
		Temperature:  &temp,
		MaxTokens:    &maxTok,
		SystemPrompt: "You are a test.",
	}

	req := buildDevinRequest(parsed, "devin-session-token$test", "glm-5-2")

	if req.Metadata.IdeName != "devin-cli" {
		t.Errorf("ide_name: got %q, want %q", req.Metadata.IdeName, "devin-cli")
	}
	if req.Metadata.ExtensionName != "chisel" {
		t.Errorf("extension_name: got %q, want %q", req.Metadata.ExtensionName, "chisel")
	}
	if req.Metadata.ApiKey != "devin-session-token$test" {
		t.Errorf("api_key: got %q, want %q", req.Metadata.ApiKey, "devin-session-token$test")
	}
	if req.Prompt != "You are a test." {
		t.Errorf("prompt: got %q, want %q", req.Prompt, "You are a test.")
	}
	if req.RequestType != devinRequestTypeCascade {
		t.Errorf("request_type: got %d, want %d", req.RequestType, devinRequestTypeCascade)
	}
	if req.Configuration.MaxTokens != 500 {
		t.Errorf("max_tokens override: got %d, want 500", req.Configuration.MaxTokens)
	}
	if req.Configuration.Temperature != 0.7 {
		t.Errorf("temperature override: got %f, want 0.7", req.Configuration.Temperature)
	}
	if req.PlannerMode != devinPlannerModeDefault {
		t.Errorf("planner_mode: got %d, want %d", req.PlannerMode, devinPlannerModeDefault)
	}
	if req.ChatModelUID != "glm-5-2" {
		t.Errorf("chat_model_uid: got %q, want %q", req.ChatModelUID, "glm-5-2")
	}
	// System message should be excluded from chat_message_prompts.
	if len(req.ChatMessagePrompts) != 1 {
		t.Fatalf("chat_message_prompts: got %d, want 1 (system excluded)", len(req.ChatMessagePrompts))
	}
	if req.ChatMessagePrompts[0].Source != devinMsgSourceUser {
		t.Errorf("message source: got %d, want %d", req.ChatMessagePrompts[0].Source, devinMsgSourceUser)
	}
}

func TestDevinBuildOpenAIStreamChunk(t *testing.T) {
	chunk := &devinStreamChunk{
		MessageID: "bot-test-id",
		DeltaText: "hello",
	}
	sse := buildOpenAIStreamChunk(chunk, "glm-5-2")
	if sse == "" {
		t.Fatal("buildOpenAIStreamChunk returned empty string")
	}
	if !contains(sse, `"content":"hello"`) {
		t.Errorf("SSE missing content: %s", sse)
	}
	if !contains(sse, `"model":"glm-5-2"`) {
		t.Errorf("SSE missing model: %s", sse)
	}

	// Empty chunk should return empty string.
	empty := buildOpenAIStreamChunk(&devinStreamChunk{}, "glm-5-2")
	if empty != "" {
		t.Errorf("empty chunk should return empty string, got %q", empty)
	}
}

func TestDevinBuildOpenAIChatCompletion(t *testing.T) {
	usage := &devinUsageStats{
		InputTokens:  42,
		OutputTokens: 3,
	}
	resp := buildOpenAIChatCompletion("bot-test", "glm-5-2", "pong", "", "stop", nil, usage)

	var result map[string]any
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if result["model"] != "glm-5-2" {
		t.Errorf("model: got %v, want %v", result["model"], "glm-5-2")
	}
	choices := result["choices"].([]any)
	choice := choices[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	if msg["content"] != "pong" {
		t.Errorf("content: got %v, want %v", msg["content"], "pong")
	}
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish_reason: got %v, want %v", choice["finish_reason"], "stop")
	}
	usageObj := result["usage"].(map[string]any)
	if usageObj["prompt_tokens"] != float64(42) {
		t.Errorf("prompt_tokens: got %v, want 42", usageObj["prompt_tokens"])
	}
}

func TestDevinBuildOpenAIChatCompletionWithToolCalls(t *testing.T) {
	toolCalls := []devinToolCall{
		{ID: "call-1", Name: "run_command", ArgumentsJSON: `{"command": "ls"}`},
		{ID: "call-2", Name: "edit_file", ArgumentsJSON: `{"path": "hello.txt", "content": "Hello World"}`},
	}
	resp := buildOpenAIChatCompletion("bot-test", "glm-5-2", "", "", "stop", toolCalls, nil)

	var result map[string]any
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	choices := result["choices"].([]any)
	choice := choices[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	tcs, ok := msg["tool_calls"].([]any)
	if !ok {
		t.Fatalf("missing tool_calls in response")
	}
	if len(tcs) != 2 {
		t.Fatalf("tool_calls: got %d, want 2", len(tcs))
	}
	tc0 := tcs[0].(map[string]any)
	if tc0["id"] != "call-1" {
		t.Errorf("tool_call[0].id: got %v, want call-1", tc0["id"])
	}
	fn0 := tc0["function"].(map[string]any)
	if fn0["name"] != "run_command" {
		t.Errorf("tool_call[0].name: got %v, want run_command", fn0["name"])
	}
	if fn0["arguments"] != `{"command": "ls"}` {
		t.Errorf("tool_call[0].arguments: got %v, want {\"command\": \"ls\"}", fn0["arguments"])
	}
}

func TestDevinEncodeToolDefinition(t *testing.T) {
	tool := devinToolDefinition{
		Name:             "run_command",
		Description:      "Run a terminal command",
		JSONSchemaString: `{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`,
	}
	b := encodeToolDefinition(tool)
	if len(b) == 0 {
		t.Fatal("encodeToolDefinition returned empty bytes")
	}

	// Verify field 1 (name)
	name := extractStringField(t, b, 1)
	if name != "run_command" {
		t.Errorf("field 1 (name): got %q, want %q", name, "run_command")
	}
	// Verify field 2 (description)
	desc := extractStringField(t, b, 2)
	if desc != "Run a terminal command" {
		t.Errorf("field 2 (description): got %q, want %q", desc, "Run a terminal command")
	}
	// Verify field 3 (json_schema_string)
	schema := extractStringField(t, b, 3)
	if !contains(schema, "command") {
		t.Errorf("field 3 (json_schema): missing 'command' in %q", schema)
	}
}

func TestDevinEncodeToolCall(t *testing.T) {
	tc := devinToolCall{
		ID:            "call-1",
		Name:          "run_command",
		ArgumentsJSON: `{"command": "ls"}`,
	}
	b := encodeToolCall(tc)
	if len(b) == 0 {
		t.Fatal("encodeToolCall returned empty bytes")
	}

	// Verify field 1 (id)
	id := extractStringField(t, b, 1)
	if id != "call-1" {
		t.Errorf("field 1 (id): got %q, want %q", id, "call-1")
	}
	// Verify field 2 (name)
	name := extractStringField(t, b, 2)
	if name != "run_command" {
		t.Errorf("field 2 (name): got %q, want %q", name, "run_command")
	}
	// Verify field 3 (arguments_json)
	args := extractStringField(t, b, 3)
	if args != `{"command": "ls"}` {
		t.Errorf("field 3 (arguments): got %q, want %q", args, `{"command": "ls"}`)
	}
}

func TestDevinStreamChunkWithToolCalls(t *testing.T) {
	chunk := &devinStreamChunk{
		MessageID: "bot-test",
		DeltaToolCalls: []devinToolCall{
			{ID: "call-1", Name: "edit_file", ArgumentsJSON: `{"path":"hello.txt"}`},
		},
	}
	sse := buildOpenAIStreamChunk(chunk, "glm-5-2")
	if sse == "" {
		t.Fatal("buildOpenAIStreamChunk returned empty for tool call chunk")
	}
	if !contains(sse, `"tool_calls"`) {
		t.Errorf("SSE missing tool_calls: %s", sse)
	}
	if !contains(sse, `"edit_file"`) {
		t.Errorf("SSE missing tool name: %s", sse)
	}
	if !contains(sse, `"call-1"`) {
		t.Errorf("SSE missing tool id: %s", sse)
	}
}

// extractStringField extracts a string field from a protobuf message for testing.
func extractStringField(t *testing.T, data []byte, fieldNum protowire.Number) string {
	t.Helper()
	for len(data) > 0 {
		num, wireType, n := protowire.ConsumeTag(data)
		if n < 0 {
			t.Fatalf("ConsumeTag error for field %d", fieldNum)
		}
		data = data[n:]
		if num == fieldNum && wireType == protowire.BytesType {
			val, valLen := protowire.ConsumeString(data)
			if valLen < 0 {
				t.Fatalf("ConsumeString error for field %d", fieldNum)
			}
			return string(val)
		}
		skipLen := protowire.ConsumeFieldValue(num, wireType, data)
		if skipLen < 0 {
			t.Fatalf("ConsumeFieldValue error")
		}
		data = data[skipLen:]
	}
	return ""
}

// extractVarintField extracts a varint field from a protobuf message for testing.
func extractVarintField(t *testing.T, data []byte, fieldNum protowire.Number) uint64 {
	t.Helper()
	for len(data) > 0 {
		num, wireType, n := protowire.ConsumeTag(data)
		if n < 0 {
			t.Fatalf("ConsumeTag error for field %d", fieldNum)
		}
		data = data[n:]
		if num == fieldNum && wireType == protowire.VarintType {
			val, valLen := protowire.ConsumeVarint(data)
			if valLen < 0 {
				t.Fatalf("ConsumeVarint error for field %d", fieldNum)
			}
			return val
		}
		skipLen := protowire.ConsumeFieldValue(num, wireType, data)
		if skipLen < 0 {
			t.Fatalf("ConsumeFieldValue error")
		}
		data = data[skipLen:]
	}
	return 0
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
