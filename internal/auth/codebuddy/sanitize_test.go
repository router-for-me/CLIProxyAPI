package codebuddy

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeForContentFilterStringContent(t *testing.T) {
	body := []byte(`{"model":"hy3","messages":[
		{"role":"system","content":"You are Claude Code, Anthropic's official CLI for Claude.\n\nCodex CLI is an open source project led by OpenAI. Use Codex CLI to build PRs."},
		{"role":"user","content":"hi"}
	]}`)

	sanitized, changed, err := SanitizeForContentFilter(body, nil)
	if err != nil {
		t.Fatalf("SanitizeForContentFilter returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected the system prompt to be rewritten")
	}
	system := gjson.GetBytes(sanitized, "messages.0.content").String()
	if strings.Contains(system, "You are Claude Code") {
		t.Errorf("vendor identity sentence still present: %q", system)
	}
	if strings.Contains(system, "Codex CLI") {
		t.Errorf("'Codex CLI' still present: %q", system)
	}
	if strings.Contains(system, "PRs") {
		t.Errorf("'PRs' still present: %q", system)
	}
	if !strings.Contains(system, "workbuddy to build PR") {
		t.Errorf("expected rewritten text, got %q", system)
	}
	user := gjson.GetBytes(sanitized, "messages.1.content").String()
	if user != "hi" {
		t.Errorf("user message must be untouched, got %q", user)
	}
}

func TestSanitizeForContentFilterArrayContent(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"system","content":[{"type":"text","text":"Codex CLI helps with PRs"},{"type":"text","text":"unchanged"}]}
	]}`)

	sanitized, changed, err := SanitizeForContentFilter(body, nil)
	if err != nil {
		t.Fatalf("SanitizeForContentFilter returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected the system prompt to be rewritten")
	}
	first := gjson.GetBytes(sanitized, "messages.0.content.0.text").String()
	if strings.Contains(first, "Codex CLI") || strings.Contains(first, "PRs") {
		t.Errorf("first text part not rewritten: %q", first)
	}
	second := gjson.GetBytes(sanitized, "messages.0.content.1.text").String()
	if second != "unchanged" {
		t.Errorf("second text part must be untouched, got %q", second)
	}
}

func TestSanitizeForContentFilterNoSystemMessages(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"You are Claude Code, Anthropic's official CLI for Claude."}]}`)
	sanitized, changed, err := SanitizeForContentFilter(body, nil)
	if err != nil {
		t.Fatalf("SanitizeForContentFilter returned error: %v", err)
	}
	if changed {
		t.Fatal("user messages must not be rewritten")
	}
	if string(sanitized) != string(body) {
		t.Fatal("body must stay unchanged when no system message matches")
	}
}

func TestSanitizeForContentFilterCustomRules(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"please drop SECRETWORD"}]}`)
	sanitized, changed, err := SanitizeForContentFilter(body, []SystemPromptBlacklistRule{{Find: "SECRETWORD", Replace: "safe"}})
	if err != nil {
		t.Fatalf("SanitizeForContentFilter returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected custom rule to apply")
	}
	if !strings.Contains(gjson.GetBytes(sanitized, "messages.0.content").String(), "please drop safe") {
		t.Errorf("custom rule not applied: %s", gjson.GetBytes(sanitized, "messages.0.content").String())
	}
}

func TestSanitizeForContentFilterInvalidRegex(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"x"}]}`)
	if _, _, err := SanitizeForContentFilter(body, []SystemPromptBlacklistRule{{Find: "([", IsRegex: true}}); err == nil {
		t.Fatal("expected an error for an invalid regex rule")
	}
}
