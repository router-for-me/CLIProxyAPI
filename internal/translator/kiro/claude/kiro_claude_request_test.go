package claude

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildKiroPayloadUsesClientThinkingBudget(t *testing.T) {
	tests := []struct {
		name       string
		budget     int
		wantLength string
	}{
		{
			name:       "custom budget",
			budget:     8192,
			wantLength: "<max_thinking_length>8192</max_thinking_length>",
		},
		{
			name:       "explicit placeholder-sized budget",
			budget:     24000,
			wantLength: "<max_thinking_length>24000</max_thinking_length>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{
				"model":"claude-opus-4-1",
				"max_tokens":32000,
				"thinking":{"type":"enabled","budget_tokens":%d},
				"messages":[{"role":"user","content":"hi"}]
			}`, tt.budget))

			out, thinkingEnabled := BuildKiroPayload(body, "claude-opus-4-1", "", "CLI", false, false, nil, nil)
			if !thinkingEnabled {
				t.Fatalf("thinkingEnabled = false, want true")
			}

			content := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String()
			if !gjson.ValidBytes(out) {
				t.Fatalf("invalid JSON: %s", string(out))
			}
			if !containsAll(content, "<thinking_mode>enabled</thinking_mode>", tt.wantLength) {
				t.Fatalf("content missing client thinking budget, content=%s", content)
			}
		})
	}
}

func TestBuildKiroPayloadDefaultsPlaceholderThinkingBudget(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-1",
		"max_tokens":32000,
		"thinking":{"type":"enabled"},
		"messages":[{"role":"user","content":"hi"}]
	}`)

	out, thinkingEnabled := BuildKiroPayload(body, "claude-opus-4-1", "", "CLI", false, false, nil, nil)
	if !thinkingEnabled {
		t.Fatalf("thinkingEnabled = false, want true")
	}

	content := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String()
	if !containsAll(content, "<thinking_mode>enabled</thinking_mode>", "<max_thinking_length>16000</max_thinking_length>") {
		t.Fatalf("content missing default thinking budget, content=%s", content)
	}
}

func containsAll(s string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(s, needle) {
			return false
		}
	}
	return true
}
