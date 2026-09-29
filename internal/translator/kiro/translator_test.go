package kiro

import (
	"encoding/json"
	"testing"
)

func TestTranslateToKiroRequest(t *testing.T) {
	temperature := 0.2
	openAIReq := map[string]any{
		"model":       "kiro/claude-sonnet-4.5-thinking",
		"max_tokens":  123,
		"temperature": temperature,
		"messages": []map[string]any{
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Write a hello world in Go."},
		},
	}

	rawBody, _ := json.Marshal(openAIReq)
	translated, cleanModel, err := TranslateToKiroRequest(rawBody, "arn:aws:codewhisperer:us-east-1:123:profile")
	if err != nil {
		t.Fatalf("failed to translate request: %v", err)
	}

	if cleanModel != "claude-sonnet-4.5" {
		t.Errorf("expected cleanModel 'claude-sonnet-4.5', got '%s'", cleanModel)
	}

	var res KiroGenerateRequest
	if err := json.Unmarshal(translated, &res); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if res.ProfileArn != "arn:aws:codewhisperer:us-east-1:123:profile" {
		t.Errorf("profileArn mismatch")
	}

	if res.ConversationState.CurrentMessage.UserInputMessage.Content == "" {
		t.Errorf("expected non-empty user message content")
	}
	if res.InferenceConfig == nil || res.InferenceConfig.MaxTokens != 123 {
		t.Fatalf("expected inference config with max tokens")
	}
}

func TestTranslateToKiroRequestWithToolResult(t *testing.T) {
	openAIReq := map[string]any{
		"model": "kiro/claude-sonnet-4.6",
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "read_file",
				"description": "Read a file",
				"parameters": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []any{},
				},
			},
		}},
		"messages": []map[string]any{
			{"role": "user", "content": "Read a file."},
			{"role": "assistant", "content": "", "tool_calls": []map[string]any{{
				"id":   "call_1",
				"type": "function",
				"function": map[string]any{
					"name":      "read_file",
					"arguments": `{"path":"main.go"}`,
				},
			}}},
			{"role": "tool", "tool_call_id": "call_1", "content": "package main"},
		},
	}

	rawBody, _ := json.Marshal(openAIReq)
	translated, _, err := TranslateToKiroRequest(rawBody, "")
	if err != nil {
		t.Fatalf("failed to translate request: %v", err)
	}

	var res KiroGenerateRequest
	if err := json.Unmarshal(translated, &res); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}
	ctx := res.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx == nil || len(ctx.ToolResults) != 1 || ctx.ToolResults[0].ToolUseID != "call_1" {
		t.Fatalf("expected current tool result for call_1, got %#v", ctx)
	}
	if len(ctx.Tools) != 1 {
		t.Fatalf("expected one tool spec, got %#v", ctx.Tools)
	}
	if _, hasAdditional := ctx.Tools[0].ToolSpecification.InputSchema.JSON.(map[string]any)["additionalProperties"]; hasAdditional {
		t.Fatalf("expected unsupported additionalProperties to be stripped")
	}
	if len(res.ConversationState.History) != 2 || len(res.ConversationState.History[1].AssistantResponseMessage.ToolUses) != 1 {
		t.Fatalf("expected active assistant tool use in history, got %#v", res.ConversationState.History)
	}
}

func TestTranslateToKiroRequestWithImageBlock(t *testing.T) {
	req := map[string]any{
		"model": "claude-sonnet-4.6",
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": "look"},
				{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,aGVsbG8="}},
			},
		}},
	}
	rawBody, _ := json.Marshal(req)
	translated, _, err := TranslateToKiroRequest(rawBody, "")
	if err != nil {
		t.Fatalf("failed to translate request: %v", err)
	}
	var res KiroGenerateRequest
	if err := json.Unmarshal(translated, &res); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}
	current := res.ConversationState.CurrentMessage.UserInputMessage
	if current.Content != "look" {
		t.Fatalf("expected text content, got %q", current.Content)
	}
	if len(current.Images) != 1 || current.Images[0].Format != "png" || current.Images[0].Source.Bytes != "aGVsbG8=" {
		t.Fatalf("expected one png image, got %#v", current.Images)
	}
}

func TestTranslateToKiroRequestPreservesAutoModel(t *testing.T) {
	rawBody := []byte(`{"model":"kiro/auto","messages":[{"role":"user","content":"hello"}]}`)
	translated, cleanModel, err := TranslateToKiroRequest(rawBody, "")
	if err != nil {
		t.Fatalf("failed to translate request: %v", err)
	}
	if cleanModel != "auto" {
		t.Fatalf("expected clean model auto, got %q", cleanModel)
	}

	var res KiroGenerateRequest
	if err := json.Unmarshal(translated, &res); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}
	if modelID := res.ConversationState.CurrentMessage.UserInputMessage.ModelID; modelID != "auto" {
		t.Fatalf("expected upstream modelId auto, got %q", modelID)
	}
}

func TestCleanKiroModelIDAliases(t *testing.T) {
	tests := map[string]string{
		"kiro/auto":                       "auto",
		"kr/auto":                         "auto",
		"kiro-auto":                       "auto",
		"kiro":                            "auto",
		"":                                "auto",
		"kiro/claude-sonnet-4.5-thinking": "claude-sonnet-4.5",
		"claude-sonnet-4-20250514":        "claude-sonnet-4",
		"claude-3-5-sonnet":               "claude-3-5-sonnet",
		"claude-3.5-sonnet":               "claude-3.5-sonnet",
		"claude-3-7-sonnet":               "claude-3-7-sonnet",
		"claude-3.7-sonnet":               "claude-3.7-sonnet",
		"claude-sonnet-4-5":               "claude-sonnet-4.5",
		"claude-haiku-4-5":                "claude-haiku-4.5",
	}
	for input, want := range tests {
		if got := CleanKiroModelID(input); got != want {
			t.Fatalf("CleanKiroModelID(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestCleanKiroModelIDNormalizesUnknownDashedVersions covers models that are not
// in the alias switch, which is how newly released Kiro models arrive.
func TestCleanKiroModelIDNormalizesUnknownDashedVersions(t *testing.T) {
	tests := map[string]string{
		"claude-opus-5-5":          "claude-opus-5.5",
		"claude-opus-5.5":          "claude-opus-5.5",
		"kiro/claude-opus-5-5":     "claude-opus-5.5",
		"claude-opus-5-5-thinking": "claude-opus-5.5",
		// A future release should resolve without another hardcoded case.
		"claude-sonnet-6-2": "claude-sonnet-6.2",
		"gpt-5-7-sol":       "gpt-5.7-sol",
		// Mid-name versions and single-segment versions must stay untouched.
		"claude-3-5-sonnet": "claude-3-5-sonnet",
		"claude-opus-5":     "claude-opus-5",
		"glm-5":             "glm-5",
		"qwen3-coder-next":  "qwen3-coder-next",
	}
	for input, want := range tests {
		if got := CleanKiroModelID(input); got != want {
			t.Fatalf("CleanKiroModelID(%q) = %q, want %q", input, got, want)
		}
	}
}
