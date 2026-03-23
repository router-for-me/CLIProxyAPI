package routing

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func boolPtr(b bool) *bool { return &b }

func makeRequest(messages []map[string]interface{}) []byte {
	body := map[string]interface{}{
		"model":    "claude-opus-4-6",
		"messages": messages,
	}
	data, _ := json.Marshal(body)
	return data
}

func TestAnalyze_BasicSignals(t *testing.T) {
	raw := makeRequest([]map[string]interface{}{
		{"role": "system", "content": "You are helpful"},
		{"role": "user", "content": "Hello world"},
	})
	signals := Analyze(raw, "claude-opus-4-6")

	if signals.MessageCount != 2 {
		t.Errorf("expected 2 messages, got %d", signals.MessageCount)
	}
	if signals.LastUserMessage != "Hello world" {
		t.Errorf("unexpected last user message: %s", signals.LastUserMessage)
	}
	if signals.SystemPrompt != "You are helpful" {
		t.Errorf("unexpected system prompt: %s", signals.SystemPrompt)
	}
	if signals.HasCodeBlocks {
		t.Error("should not detect code blocks")
	}
	if signals.HasToolBlocks {
		t.Error("should not detect tool blocks")
	}
}

func TestAnalyze_CodeBlocks(t *testing.T) {
	raw := makeRequest([]map[string]interface{}{
		{"role": "user", "content": "Fix this:\n```go\nfunc main() {}\n```"},
	})
	signals := Analyze(raw, "test")
	if !signals.HasCodeBlocks {
		t.Error("should detect code blocks")
	}
}

func TestAnalyze_ToolBlocks(t *testing.T) {
	raw := makeRequest([]map[string]interface{}{
		{"role": "assistant", "content": []map[string]interface{}{
			{"type": "text", "text": "Let me help"},
			{"type": "tool_use", "id": "t1", "name": "read_file"},
		}},
		{"role": "user", "content": []map[string]interface{}{
			{"type": "tool_result", "tool_use_id": "t1", "content": "file contents"},
		}},
	})
	signals := Analyze(raw, "test")
	if !signals.HasToolBlocks {
		t.Error("should detect tool blocks")
	}
}

func TestEngine_DisabledReturnsEmpty(t *testing.T) {
	e := NewEngine(config.ModelRoutingConfig{Enabled: false})
	result := e.Evaluate(makeRequest([]map[string]interface{}{
		{"role": "user", "content": "test"},
	}), "opus")
	if result != "" {
		t.Errorf("disabled engine should return empty, got %s", result)
	}
}

func TestEngine_FirstMatchWins(t *testing.T) {
	e := NewEngine(config.ModelRoutingConfig{
		Enabled: true,
		Rules: []config.ModelRoutingRule{
			{Name: "short", Priority: 1, TargetModel: "haiku",
				Conditions: []config.ModelRoutingCondition{
					{Type: "last-message-length", Operator: "less-than", Value: "100"},
				}},
			{Name: "always", Priority: 2, TargetModel: "sonnet",
				Conditions: []config.ModelRoutingCondition{
					{Type: "always"},
				}},
		},
	})
	result := e.Evaluate(makeRequest([]map[string]interface{}{
		{"role": "user", "content": "hi"},
	}), "opus")
	if result != "haiku" {
		t.Errorf("expected haiku, got %s", result)
	}
}

func TestEngine_PriorityOrdering(t *testing.T) {
	e := NewEngine(config.ModelRoutingConfig{
		Enabled: true,
		Rules: []config.ModelRoutingRule{
			{Name: "low-priority", Priority: 10, TargetModel: "haiku",
				Conditions: []config.ModelRoutingCondition{{Type: "always"}}},
			{Name: "high-priority", Priority: 1, TargetModel: "opus",
				Conditions: []config.ModelRoutingCondition{{Type: "always"}}},
		},
	})
	result := e.Evaluate(makeRequest([]map[string]interface{}{
		{"role": "user", "content": "test"},
	}), "original")
	if result != "opus" {
		t.Errorf("expected opus (priority 1), got %s", result)
	}
}

func TestEngine_DisabledRule(t *testing.T) {
	e := NewEngine(config.ModelRoutingConfig{
		Enabled: true,
		Rules: []config.ModelRoutingRule{
			{Name: "disabled", Priority: 1, TargetModel: "haiku", Enabled: boolPtr(false),
				Conditions: []config.ModelRoutingCondition{{Type: "always"}}},
			{Name: "enabled", Priority: 2, TargetModel: "sonnet",
				Conditions: []config.ModelRoutingCondition{{Type: "always"}}},
		},
	})
	result := e.Evaluate(makeRequest([]map[string]interface{}{
		{"role": "user", "content": "test"},
	}), "opus")
	if result != "sonnet" {
		t.Errorf("expected sonnet (disabled rule skipped), got %s", result)
	}
}

func TestEngine_DryRunMode(t *testing.T) {
	e := NewEngine(config.ModelRoutingConfig{
		Enabled: true,
		DryRun:  true,
		Rules: []config.ModelRoutingRule{
			{Name: "match", Priority: 1, TargetModel: "haiku",
				Conditions: []config.ModelRoutingCondition{{Type: "always"}}},
		},
	})
	result := e.Evaluate(makeRequest([]map[string]interface{}{
		{"role": "user", "content": "test"},
	}), "opus")
	if result != "" {
		t.Errorf("dry-run should return empty, got %s", result)
	}
}

func TestEngine_DefaultModel(t *testing.T) {
	e := NewEngine(config.ModelRoutingConfig{
		Enabled:      true,
		DefaultModel: "sonnet",
		Rules: []config.ModelRoutingRule{
			{Name: "no-match", Priority: 1, TargetModel: "haiku",
				Conditions: []config.ModelRoutingCondition{
					{Type: "last-message-length", Operator: "greater-than", Value: "999999"},
				}},
		},
	})
	result := e.Evaluate(makeRequest([]map[string]interface{}{
		{"role": "user", "content": "short"},
	}), "opus")
	if result != "sonnet" {
		t.Errorf("expected default model sonnet, got %s", result)
	}
}

func TestEngine_ModelFamily(t *testing.T) {
	e := NewEngine(config.ModelRoutingConfig{
		Enabled: true,
		Rules: []config.ModelRoutingRule{
			{Name: "claude-only", Priority: 1, TargetModel: "sonnet",
				Conditions: []config.ModelRoutingCondition{
					{Type: "requested-model-family", Operator: "equals", Value: "claude"},
				}},
		},
	})

	// Claude model should match
	result := e.Evaluate(makeRequest([]map[string]interface{}{
		{"role": "user", "content": "test"},
	}), "claude-opus-4-6")
	if result != "sonnet" {
		t.Errorf("expected sonnet for claude model, got %s", result)
	}

	// GPT model should not match
	result = e.Evaluate(makeRequest([]map[string]interface{}{
		{"role": "user", "content": "test"},
	}), "gpt-5")
	if result != "" {
		t.Errorf("expected empty for gpt model, got %s", result)
	}
}

func TestEngine_MultipleConditions(t *testing.T) {
	e := NewEngine(config.ModelRoutingConfig{
		Enabled: true,
		Rules: []config.ModelRoutingRule{
			{Name: "short-no-code", Priority: 1, TargetModel: "haiku",
				Conditions: []config.ModelRoutingCondition{
					{Type: "last-message-length", Operator: "less-than", Value: "100"},
					{Type: "has-code-blocks", Operator: "equals", Value: "false"},
				}},
		},
	})

	// Short message without code -> matches
	result := e.Evaluate(makeRequest([]map[string]interface{}{
		{"role": "user", "content": "hello"},
	}), "opus")
	if result != "haiku" {
		t.Errorf("expected haiku, got %s", result)
	}

	// Short message WITH code -> no match
	result = e.Evaluate(makeRequest([]map[string]interface{}{
		{"role": "user", "content": "fix:\n```\ncode\n```"},
	}), "opus")
	if result != "" {
		t.Errorf("expected empty (code blocks), got %s", result)
	}
}

func TestDetectModelFamily(t *testing.T) {
	tests := []struct{ model, expected string }{
		{"claude-opus-4-6", "claude"},
		{"claude-sonnet-4-5", "claude"},
		{"gemini-claude-opus-4-6-thinking", "claude"},
		{"gpt-5", "gpt"},
		{"o1-preview", "gpt"},
		{"o4-mini", "gpt"},
		{"gemini-2.5-flash", "gemini"},
		{"deepseek-v3", "compatible"},
	}
	for _, tt := range tests {
		got := detectModelFamily(tt.model)
		if got != tt.expected {
			t.Errorf("detectModelFamily(%q) = %q, want %q", tt.model, got, tt.expected)
		}
	}
}
