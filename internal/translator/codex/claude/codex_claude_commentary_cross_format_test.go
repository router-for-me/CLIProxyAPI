package claude

import (
	"strings"
	"testing"

	antigravityclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/claude"
	geminiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/claude"
	interactionsclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/interactions/claude"
	openaiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/claude"
	"github.com/tidwall/gjson"
)

// A client may replay CPA-relayed commentary history to a non-Codex upstream.
// Every Claude request translator must restore the internal marker as plain
// assistant text before its own thinking handling, and the marker literal must
// never appear in the translated request.
const crossFormatCommentaryPayload = `{"model":"test-model","messages":[{"role":"user","content":"First turn"},{"role":"assistant","content":[{"type":"thinking","thinking":"COMMENTARY_SENTINEL","signature":"cpa:commentary:v1"},{"type":"text","text":"FINAL_SENTINEL"}]},{"role":"user","content":"Next turn"}]}`

func assertCommentaryRestoredAsText(t *testing.T, name string, out []byte, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: conversion error: %v", name, err)
	}
	if strings.Contains(string(out), "cpa:commentary:v1") {
		t.Fatalf("%s: internal marker leaked into translated request: %s", name, out)
	}
	if !strings.Contains(string(out), "COMMENTARY_SENTINEL") {
		t.Fatalf("%s: commentary text lost in translation: %s", name, out)
	}
	if !strings.Contains(string(out), "FINAL_SENTINEL") {
		t.Fatalf("%s: final text lost in translation: %s", name, out)
	}
}

func TestCommentaryMarkerRestoredByOpenAIChatTranslator(t *testing.T) {
	out, err := openaiclaude.ConvertClaudeRequestToOpenAI("test-model", []byte(crossFormatCommentaryPayload), false)
	assertCommentaryRestoredAsText(t, "openai default", out, err)
	if strings.Contains(string(out), "reasoning_content") && strings.Contains(string(out), "COMMENTARY_SENTINEL") && gjson.GetBytes(out, "messages.1.reasoning_content").String() == "COMMENTARY_SENTINEL" {
		t.Fatalf("openai default: commentary must not become reasoning_content: %s", out)
	}

	out, err = openaiclaude.ConvertClaudeRequestToOpenAIWithCompat("test-model", []byte(crossFormatCommentaryPayload), false)
	assertCommentaryRestoredAsText(t, "openai compat", out, err)
	if got := gjson.GetBytes(out, "messages.1.reasoning_content").String(); got == "COMMENTARY_SENTINEL" {
		t.Fatalf("openai compat: commentary must not become reasoning_content: %s", out)
	}
}

func TestCommentaryMarkerRestoredByGeminiTranslator(t *testing.T) {
	out, err := geminiclaude.ConvertClaudeRequestToGemini("test-model", []byte(crossFormatCommentaryPayload), false)
	assertCommentaryRestoredAsText(t, "gemini default", out, err)
	if strings.Contains(string(out), `"thought":true`) && strings.Contains(string(out), "COMMENTARY_SENTINEL") {
		for _, part := range gjson.GetBytes(out, "contents.1.parts").Array() {
			if part.Get("text").String() == "COMMENTARY_SENTINEL" && part.Get("thought").Bool() {
				t.Fatalf("gemini default: commentary must not become a thought part: %s", out)
			}
		}
	}

	out, err = geminiclaude.ConvertClaudeRequestToGeminiWithCompat("test-model", []byte(crossFormatCommentaryPayload), false)
	assertCommentaryRestoredAsText(t, "gemini compat", out, err)
	for _, content := range gjson.GetBytes(out, "contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if part.Get("text").String() == "COMMENTARY_SENTINEL" && part.Get("thought").Bool() {
				t.Fatalf("gemini compat: commentary must not become a thought part: %s", out)
			}
			if part.Get("thoughtSignature").String() == "cpa:commentary:v1" {
				t.Fatalf("gemini compat: internal marker became a thoughtSignature: %s", out)
			}
		}
	}
}

func TestCommentaryMarkerRestoredByInteractionsTranslator(t *testing.T) {
	out, err := interactionsclaude.ConvertClaudeRequestToInteractions("test-model", []byte(crossFormatCommentaryPayload), false)
	assertCommentaryRestoredAsText(t, "interactions default", out, err)

	out, err = interactionsclaude.ConvertClaudeRequestToInteractionsWithCompat("test-model", []byte(crossFormatCommentaryPayload), false)
	assertCommentaryRestoredAsText(t, "interactions compat", out, err)
	if strings.Contains(string(out), `"type":"thought"`) {
		t.Fatalf("interactions compat: commentary must not become a thought step: %s", out)
	}
}

func TestCommentaryMarkerRestoredByAntigravityTranslator(t *testing.T) {
	out, err := antigravityclaude.ConvertClaudeRequestToAntigravity("test-model", []byte(crossFormatCommentaryPayload), false)
	assertCommentaryRestoredAsText(t, "antigravity", out, err)
	for _, content := range gjson.GetBytes(out, "request.contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if part.Get("text").String() == "COMMENTARY_SENTINEL" && part.Get("thought").Bool() {
				t.Fatalf("antigravity: commentary must not become a thought part: %s", out)
			}
		}
	}
}
