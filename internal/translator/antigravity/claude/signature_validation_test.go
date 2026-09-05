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

func TestStripInvalidGeminiSignatureThinkingBlocksPreservesUnsignedThoughtWithFollowingPreviousCarrier(t *testing.T) {
	validSignature := testGeminiEPrefixSignature(t)
	validCarrier := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierPrevious, geminiClaudeCarrierText)
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"let me think","signature":""},{"type":"text","text":"answer text"},{"type":"thinking","thinking":"","signature":"` + validCarrier + `"}]}]}`)
	out := StripInvalidGeminiSignatureThinkingBlocks(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 3 {
		t.Fatalf("content count = %d, want 3; output=%s", len(content), out)
	}
	if got := content[0].Get("thinking").String(); got != "let me think" {
		t.Fatalf("thinking text = %q, want 'let me think'", got)
	}
	if got := content[1].Get("text").String(); got != "answer text" {
		t.Fatalf("text = %q, want 'answer text'", got)
	}
	if got := content[2].Get("signature").String(); got != validCarrier {
		t.Fatalf("carrier = %q, want %q", got, validCarrier)
	}
}

func TestStripInvalidGeminiSignatureThinkingBlocksUnrelatedSegmentDoesNotPreserveEarlierUnsignedThought(t *testing.T) {
	validSignature := testGeminiEPrefixSignature(t)
	validCarrier := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierPrevious, geminiClaudeCarrierText)
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"unrelated early thought","signature":""},{"type":"text","text":"text A"},{"type":"thinking","thinking":"second thought","signature":""},{"type":"text","text":"text B"},{"type":"thinking","thinking":"","signature":"` + validCarrier + `"}]}]}`)
	out := StripInvalidGeminiSignatureThinkingBlocks(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 4 {
		t.Fatalf("content count = %d, want 4 (early thought dropped, second thought + text + carrier kept); output=%s", len(content), out)
	}
	if got := content[0].Get("text").String(); got != "text A" {
		t.Fatalf("content[0] text = %q, want 'text A'", got)
	}
	if got := content[1].Get("thinking").String(); got != "second thought" {
		t.Fatalf("content[1] thinking = %q, want 'second thought'", got)
	}
	if got := content[2].Get("text").String(); got != "text B" {
		t.Fatalf("content[2] text = %q, want 'text B'", got)
	}
	if got := content[3].Get("signature").String(); got != validCarrier {
		t.Fatalf("content[3] signature = %q, want %q", got, validCarrier)
	}
}

func TestStripInvalidGeminiSignatureThinkingBlocksDropsUnsignedThinkingWithoutCarriers(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"thought 1","signature":""},{"type":"thinking","thinking":"thought 2","signature":""},{"type":"thinking","thinking":"thought 3","signature":""},{"type":"text","text":"visible answer"}]}]}`)
	out := StripInvalidGeminiSignatureThinkingBlocks(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 1 || content[0].Get("text").String() != "visible answer" {
		t.Fatalf("unsigned thoughts without carrier were not stripped: %s", out)
	}
}

func TestStripInvalidGeminiSignatureThinkingBlocksDropsUnsignedThoughtWithMalformedOrWrongDirectionCarrier(t *testing.T) {
	validSignature := testGeminiEPrefixSignature(t)
	nextCarrier := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierNext, geminiClaudeCarrierText)
	malformedCarrier := "cpa-gemini-carrier-v1:previous:text:invalid-base64"

	// Trailing next carrier does not bind backward to previous text/thinking
	inputNext := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"thought text","signature":""},{"type":"text","text":"visible text"},{"type":"thinking","thinking":"","signature":"` + nextCarrier + `"}]}]}`)
	outNext := StripInvalidGeminiSignatureThinkingBlocks(inputNext)
	contentNext := gjson.GetBytes(outNext, "messages.0.content").Array()
	if len(contentNext) != 1 || contentNext[0].Get("text").String() != "visible text" {
		t.Fatalf("wrong direction trailing carrier preserved unsigned thought: %s", outNext)
	}

	// Trailing malformed carrier does not preserve unsigned thought
	inputMalformed := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"thought text","signature":""},{"type":"text","text":"visible text"},{"type":"thinking","thinking":"","signature":"` + malformedCarrier + `"}]}]}`)
	outMalformed := StripInvalidGeminiSignatureThinkingBlocks(inputMalformed)
	contentMalformed := gjson.GetBytes(outMalformed, "messages.0.content").Array()
	if len(contentMalformed) != 1 || contentMalformed[0].Get("text").String() != "visible text" {
		t.Fatalf("malformed trailing carrier preserved unsigned thought: %s", outMalformed)
	}
}

func TestStripInvalidGeminiSignatureThinkingBlocksDroppedNonEmptyThinkingInvalidatesPreviousCarrier(t *testing.T) {
	validSignature := testGeminiEPrefixSignature(t)
	functionCarrier := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierPrevious, geminiClaudeCarrierFunction)
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"run","input":{}},{"type":"thinking","thinking":"intervening unsigned thought","signature":""},{"type":"thinking","thinking":"","signature":"` + functionCarrier + `"}]}]}`)
	out := StripInvalidGeminiSignatureThinkingBlocks(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 1 || content[0].Get("type").String() != "tool_use" {
		t.Fatalf("dropped non-empty thinking did not invalidate following previous carrier: %s", out)
	}
}

func TestStripInvalidGeminiSignatureThinkingBlocksInvalidMiddleThinkingDoesNotPropagateCarrierToEarlyUnsignedThought(t *testing.T) {
	validSignature := testGeminiEPrefixSignature(t)
	validCarrier := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierPrevious, geminiClaudeCarrierText)
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"thought A","signature":""},{"type":"thinking","thinking":"invalid boundary","signature":"invalid"},{"type":"thinking","thinking":"","signature":"` + validCarrier + `"}]}]}`)
	out := StripInvalidGeminiSignatureThinkingBlocks(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 0 {
		t.Fatalf("invalid middle thinking propagated carrier; got: %s", out)
	}
}

func TestStripInvalidGeminiSignatureThinkingBlocksInvalidPlacementMiddleThinkingDoesNotPropagateCarrierToEarlyUnsignedThought(t *testing.T) {
	validSignature := testGeminiEPrefixSignature(t)
	invalidPlacementCarrier := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierPrevious, geminiClaudeCarrierText)
	validTrailingCarrier := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierPrevious, geminiClaudeCarrierText)
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"thought A","signature":""},{"type":"thinking","thinking":"invalid middle thinking","signature":"` + invalidPlacementCarrier + `"},{"type":"text","text":"text answer"},{"type":"thinking","thinking":"","signature":"` + validTrailingCarrier + `"}]}]}`)
	out := StripInvalidGeminiSignatureThinkingBlocks(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 || content[0].Get("text").String() != "text answer" || content[1].Get("signature").String() != validTrailingCarrier {
		t.Fatalf("invalid placement middle thinking propagated carrier; got: %s", out)
	}
}
