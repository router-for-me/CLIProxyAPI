package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestQwenExecutorParseSuffix(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantBase  string
		wantLevel string
	}{
		{"no suffix", "qwen-max", "qwen-max", ""},
		{"with level suffix", "qwen-max(high)", "qwen-max", "high"},
		{"with budget suffix", "qwen-max(16384)", "qwen-max", "16384"},
		{"complex model name", "qwen-plus-latest(medium)", "qwen-plus-latest", "medium"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := thinking.ParseSuffix(tt.model)
			if result.ModelName != tt.wantBase {
				t.Errorf("ParseSuffix(%q).ModelName = %q, want %q", tt.model, result.ModelName, tt.wantBase)
			}
		})
	}
}

func TestEnsureQwenSystemMessage_MergeStringSystem(t *testing.T) {
	payload := []byte(`{
		"model": "qwen3.6-plus",
		"stream": true,
		"messages": [
			{ "role": "system", "content": "ABCDEFG" },
			{ "role": "user", "content": [ { "type": "text", "text": "你好" } ] }
		]
	}`)

	out, err := ensureQwenSystemMessage(payload)
	if err != nil {
		t.Fatalf("ensureQwenSystemMessage() error = %v", err)
	}

	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 2 {
		t.Fatalf("messages length = %d, want 2", len(msgs))
	}
	if msgs[0].Get("role").String() != "system" {
		t.Fatalf("messages[0].role = %q, want %q", msgs[0].Get("role").String(), "system")
	}
	parts := msgs[0].Get("content").Array()
	if len(parts) != 2 {
		t.Fatalf("messages[0].content length = %d, want 2", len(parts))
	}
	if parts[0].Get("text").String() != "You are Qwen Code." || parts[0].Get("cache_control.type").String() != "ephemeral" {
		t.Fatalf("messages[0].content[0] = %s, want injected system part", parts[0].Raw)
	}
	if parts[1].Get("type").String() != "text" || parts[1].Get("text").String() != "ABCDEFG" {
		t.Fatalf("messages[0].content[1] = %s, want text part with ABCDEFG", parts[1].Raw)
	}
	if msgs[1].Get("role").String() != "user" {
		t.Fatalf("messages[1].role = %q, want %q", msgs[1].Get("role").String(), "user")
	}
}

func TestEnsureQwenSystemMessage_MergeObjectSystem(t *testing.T) {
	payload := []byte(`{
		"messages": [
			{ "role": "system", "content": { "type": "text", "text": "ABCDEFG" } },
			{ "role": "user", "content": [ { "type": "text", "text": "你好" } ] }
		]
	}`)

	out, err := ensureQwenSystemMessage(payload)
	if err != nil {
		t.Fatalf("ensureQwenSystemMessage() error = %v", err)
	}

	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 2 {
		t.Fatalf("messages length = %d, want 2", len(msgs))
	}
	parts := msgs[0].Get("content").Array()
	if len(parts) != 2 {
		t.Fatalf("messages[0].content length = %d, want 2", len(parts))
	}
	if parts[1].Get("text").String() != "ABCDEFG" {
		t.Fatalf("messages[0].content[1].text = %q, want %q", parts[1].Get("text").String(), "ABCDEFG")
	}
}

func TestEnsureQwenSystemMessage_PrependsWhenMissing(t *testing.T) {
	payload := []byte(`{
		"messages": [
			{ "role": "user", "content": [ { "type": "text", "text": "你好" } ] }
		]
	}`)

	out, err := ensureQwenSystemMessage(payload)
	if err != nil {
		t.Fatalf("ensureQwenSystemMessage() error = %v", err)
	}

	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 2 {
		t.Fatalf("messages length = %d, want 2", len(msgs))
	}
	if msgs[0].Get("role").String() != "system" {
		t.Fatalf("messages[0].role = %q, want %q", msgs[0].Get("role").String(), "system")
	}
	if !msgs[0].Get("content").IsArray() || len(msgs[0].Get("content").Array()) == 0 {
		t.Fatalf("messages[0].content = %s, want non-empty array", msgs[0].Get("content").Raw)
	}
	if msgs[1].Get("role").String() != "user" {
		t.Fatalf("messages[1].role = %q, want %q", msgs[1].Get("role").String(), "user")
	}
}

func TestEnsureQwenSystemMessage_MergesMultipleSystemMessages(t *testing.T) {
	payload := []byte(`{
		"messages": [
			{ "role": "system", "content": "A" },
			{ "role": "user", "content": [ { "type": "text", "text": "hi" } ] },
			{ "role": "system", "content": "B" }
		]
	}`)

	out, err := ensureQwenSystemMessage(payload)
	if err != nil {
		t.Fatalf("ensureQwenSystemMessage() error = %v", err)
	}

	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 2 {
		t.Fatalf("messages length = %d, want 2", len(msgs))
	}
	parts := msgs[0].Get("content").Array()
	if len(parts) != 3 {
		t.Fatalf("messages[0].content length = %d, want 3", len(parts))
	}
	if parts[1].Get("text").String() != "A" {
		t.Fatalf("messages[0].content[1].text = %q, want %q", parts[1].Get("text").String(), "A")
	}
	if parts[2].Get("text").String() != "B" {
		t.Fatalf("messages[0].content[2].text = %q, want %q", parts[2].Get("text").String(), "B")
	}
}
