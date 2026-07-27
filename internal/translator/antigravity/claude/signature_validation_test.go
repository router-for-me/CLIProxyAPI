package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestGeminiClaudeCarrierSignatureRoundTrip(t *testing.T) {
	validSignature := testGeminiEPrefixSignature(t)
	for _, testCase := range []struct {
		direction string
		kind      string
	}{
		{direction: geminiClaudeCarrierNext, kind: geminiClaudeCarrierText},
		{direction: geminiClaudeCarrierPrevious, kind: geminiClaudeCarrierFunction},
		{direction: geminiClaudeCarrierStandalone, kind: geminiClaudeCarrierAny},
	} {
		encoded := encodeGeminiClaudeCarrierSignature(validSignature, testCase.direction, testCase.kind)
		decoded, direction, kind, marked, ok := decodeGeminiClaudeCarrierSignature(encoded)
		if !marked || !ok || decoded != validSignature || direction != testCase.direction || kind != testCase.kind {
			t.Fatalf("carrier round trip = (%q,%q,%q,%v,%v)", decoded, direction, kind, marked, ok)
		}
	}
}

func TestStripInvalidGeminiSignatureThinkingBlocksPreservesMarkedNonEmptyThinking(t *testing.T) {
	validSignature := testGeminiEPrefixSignature(t)
	standalone := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierStandalone, geminiClaudeCarrierText)
	nextFunction := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierNext, geminiClaudeCarrierFunction)
	invalidPrevious := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierPrevious, geminiClaudeCarrierText)
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"signed thought","signature":"` + standalone + `"},{"type":"thinking","thinking":"tool preface","signature":"` + nextFunction + `"},{"type":"tool_use","id":"tool-1","name":"run","input":{}},{"type":"thinking","thinking":"invalid backward","signature":"` + invalidPrevious + `"}]}]}`)
	out := StripInvalidGeminiSignatureThinkingBlocks(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 3 || content[0].Get("signature").String() != standalone || content[1].Get("signature").String() != nextFunction || content[2].Get("type").String() != "tool_use" {
		t.Fatalf("marked non-empty thinking validation changed carriers: %s", out)
	}
}

func TestStripInvalidGeminiSignatureThinkingBlocksDropsMismatchedDirectionalThinking(t *testing.T) {
	validSignature := testGeminiEPrefixSignature(t)
	nextFunction := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierNext, geminiClaudeCarrierFunction)
	standaloneFunction := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierStandalone, geminiClaudeCarrierFunction)
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"wrong next target","signature":"` + nextFunction + `"},{"type":"text","text":"visible"},{"type":"thinking","thinking":"wrong standalone target","signature":"` + standaloneFunction + `"}]}]}`)
	out := StripInvalidGeminiSignatureThinkingBlocks(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 1 || content[0].Get("type").String() != "text" {
		t.Fatalf("mismatched directional thinking was preserved: %s", out)
	}
}

func TestStripInvalidGeminiSignatureThinkingBlocksDropsLegacyRawCarrierFromUserMessage(t *testing.T) {
	validSignature := testGeminiEPrefixSignature(t)
	input := []byte(`{"messages":[{"role":"user","content":[{"type":"thinking","thinking":"","signature":"` + validSignature + `"},{"type":"text","text":"user text"}]},{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"` + validSignature + `"},{"type":"text","text":"assistant text"}]}]}`)
	out := StripInvalidGeminiSignatureThinkingBlocks(input)
	userContent := gjson.GetBytes(out, "messages.0.content").Array()
	assistantContent := gjson.GetBytes(out, "messages.1.content").Array()
	if len(userContent) != 1 || userContent[0].Get("type").String() != "text" {
		t.Fatalf("legacy raw carrier survived user message: %s", out)
	}
	if len(assistantContent) != 2 || assistantContent[0].Get("signature").String() != validSignature {
		t.Fatalf("assistant legacy carrier was not preserved: %s", out)
	}
}

func TestStripInvalidGeminiSignatureThinkingBlocks(t *testing.T) {
	validSignature := testGeminiEPrefixSignature(t)
	validCarrier := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierPrevious, geminiClaudeCarrierText)
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":"first"},{"type":"thinking","thinking":"","signature":"` + validSignature + `"},{"type":"thinking","thinking":"","signature":"` + validCarrier + `"},{"type":"thinking","thinking":"","signature":"cpa-gemini-carrier-v1:previous:text:invalid"},{"type":"thinking","thinking":"","signature":"invalid"},{"type":"text","text":"last"}]}]}`)
	out := StripInvalidGeminiSignatureThinkingBlocks(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 4 {
		t.Fatalf("content count = %d, want 4; output=%s", len(content), out)
	}
	if got := content[1].Get("signature").String(); got != validSignature {
		t.Fatalf("preserved signature = %q, want Gemini signature", got)
	}
	if got := content[2].Get("signature").String(); got != validCarrier {
		t.Fatalf("preserved carrier = %q, want directional carrier", got)
	}
	if got := content[3].Get("text").String(); got != "last" {
		t.Fatalf("last text = %q, want last", got)
	}
}
