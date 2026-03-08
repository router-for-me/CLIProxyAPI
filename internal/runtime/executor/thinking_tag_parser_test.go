package executor

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	antigravityclaude "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/antigravity/claude"
	"github.com/tidwall/gjson"
)

func makePayload(text string) []byte {
	return makePayloadWithParts(fmt.Sprintf(`{"text":%s}`, strconv.Quote(text)))
}

func makePayloadWithParts(parts ...string) []byte {
	return []byte(fmt.Sprintf(`{"response":{"candidates":[{"content":{"role":"model","parts":[%s]}}],"modelVersion":"claude-opus-4-6-thinking"}}`, strings.Join(parts, ",")))
}

func makeFunctionCallPayload(name string, args string) []byte {
	return []byte(fmt.Sprintf(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"%s","args":%s}}]}}],"modelVersion":"claude-opus-4-6-thinking"}}`, name, args))
}

func TestThinkingTagParser_NonClaudeModel_Passthrough(t *testing.T) {
	parser := NewThinkingTagParser("gemini-2.5-pro")
	input := makePayload("Hello world")
	result := parser.Process(input)
	if !bytes.Equal(result, input) {
		t.Errorf("Expected unchanged output for non-Claude model")
	}
}

func TestThinkingTagParser_NoThinkingTags_Passthrough(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")
	input := makePayload("Hello world")
	result := parser.Process(input)
	if !bytes.Equal(result, input) {
		t.Errorf("Expected unchanged output when no thinking tags present")
	}
}

func TestThinkingTagParser_SimpleThinkingBlock(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")
	input := makePayload("<thinking>content</thinking>")
	result := parser.Process(input)

	parts := gjson.GetBytes(result, "response.candidates.0.content.parts")
	if !parts.IsArray() {
		t.Fatalf("Expected parts to be array")
	}

	partsArray := parts.Array()
	if len(partsArray) != 1 {
		t.Fatalf("Expected 1 part, got %d", len(partsArray))
	}

	part := partsArray[0]
	text := part.Get("text").String()
	thought := part.Get("thought").Bool()

	if text != "content" {
		t.Errorf("Expected text='content', got '%s'", text)
	}
	if !thought {
		t.Errorf("Expected thought=true, got false")
	}
}

func TestThinkingTagParser_ThinkingTagsSplitAcrossChunks(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")

	result1 := parser.Process(makePayload("<thinking"))
	parts1 := gjson.GetBytes(result1, "response.candidates.0.content.parts")
	partsArray1 := parts1.Array()
	if len(partsArray1) > 0 {
		text1 := partsArray1[0].Get("text").String()
		if text1 != "" {
			t.Errorf("Expected empty text after buffering opening tag, got '%s'", text1)
		}
	}

	result2 := parser.Process(makePayload(">thinking text"))
	parts2 := gjson.GetBytes(result2, "response.candidates.0.content.parts")
	partsArray2 := parts2.Array()
	if len(partsArray2) < 1 {
		t.Fatalf("Expected at least 1 part after completing opening tag, got %d", len(partsArray2))
	}
	part2 := partsArray2[0]
	text2 := part2.Get("text").String()
	thought2 := part2.Get("thought").Bool()

	if text2 != "thinking text" {
		t.Errorf("Expected text='thinking text', got '%s'", text2)
	}
	if !thought2 {
		t.Errorf("Expected thought=true, got false")
	}

	result3 := parser.Process(makePayload("</thinking>"))
	parts3 := gjson.GetBytes(result3, "response.candidates.0.content.parts")
	partsArray3 := parts3.Array()
	for _, p := range partsArray3 {
		if p.Get("thought").Exists() && p.Get("thought").Bool() {
			t.Errorf("Expected thought=false after closing tag")
		}
	}
}

func TestThinkingTagParser_OpenTagSplitAtAngle(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")

	parser.Process(makePayload("start<"))

	result2 := parser.Process(makePayload("thinking>content"))
	parts2 := gjson.GetBytes(result2, "response.candidates.0.content.parts")
	partsArray2 := parts2.Array()

	if len(partsArray2) < 1 {
		t.Fatalf("Expected at least 1 part, got %d", len(partsArray2))
	}

	found := false
	for _, p := range partsArray2 {
		if p.Get("text").String() == "content" && p.Get("thought").Bool() {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected to find thinking part with text='content'")
	}
}

func TestThinkingTagParser_CloseTagSplitAcrossChunks(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")

	parser.Process(makePayload("<thinking>"))

	result2 := parser.Process(makePayload("thinking text</"))
	parts2 := gjson.GetBytes(result2, "response.candidates.0.content.parts")
	partsArray2 := parts2.Array()

	if len(partsArray2) < 1 {
		t.Fatalf("Expected parts after thinking text with partial close tag")
	}

	part := partsArray2[0]
	if part.Get("text").String() != "thinking text" || !part.Get("thought").Bool() {
		t.Errorf("Expected thinking text part, got text='%s' thought=%v", part.Get("text").String(), part.Get("thought").Bool())
	}

	parser.Process(makePayload("thinking>"))
}

func TestThinkingTagParser_ThinkingThenRegularText(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")
	input := makePayload("<thinking>thought</thinking>regular text")
	result := parser.Process(input)

	parts := gjson.GetBytes(result, "response.candidates.0.content.parts")
	partsArray := parts.Array()

	if len(partsArray) != 2 {
		t.Fatalf("Expected 2 parts, got %d", len(partsArray))
	}

	part1 := partsArray[0]
	if part1.Get("text").String() != "thought" {
		t.Errorf("Expected first part text='thought', got '%s'", part1.Get("text").String())
	}
	if !part1.Get("thought").Bool() {
		t.Errorf("Expected first part thought=true, got false")
	}

	part2 := partsArray[1]
	if part2.Get("text").String() != "regular text" {
		t.Errorf("Expected second part text='regular text', got '%s'", part2.Get("text").String())
	}
	if part2.Get("thought").Exists() && part2.Get("thought").Bool() {
		t.Errorf("Expected second part thought to be absent or false")
	}
}

func TestThinkingTagParser_SplitTaggedPartPreservesThoughtSignature(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")
	signature := "sig_1234567890123456789012345678901234567890"
	input := makePayloadWithParts(
		fmt.Sprintf(`{"text":%s,"thoughtSignature":"%s","custom":"value"}`, strconv.Quote("<thinking>thought</thinking>visible"), signature),
	)
	result := parser.Process(input)

	parts := gjson.GetBytes(result, "response.candidates.0.content.parts").Array()
	if len(parts) != 3 {
		t.Fatalf("Expected 3 parts, got %d", len(parts))
	}

	thoughtPart := parts[0]
	if thoughtPart.Get("text").String() != "thought" || !thoughtPart.Get("thought").Bool() {
		t.Fatalf("Expected first part to be thought text, got text=%q thought=%v", thoughtPart.Get("text").String(), thoughtPart.Get("thought").Bool())
	}
	if thoughtPart.Get("custom").String() != "value" {
		t.Fatalf("Expected first part to preserve custom metadata")
	}
	if thoughtPart.Get("thoughtSignature").Exists() || thoughtPart.Get("thought_signature").Exists() {
		t.Fatalf("Expected first part to move signature off the text-bearing thought segment")
	}

	signaturePart := parts[1]
	if signaturePart.Get("text").String() != "" || !signaturePart.Get("thought").Bool() {
		t.Fatalf("Expected second part to be an empty thought signature part")
	}
	if signaturePart.Get("thoughtSignature").String() != signature {
		t.Fatalf("Expected signature %q, got %q", signature, signaturePart.Get("thoughtSignature").String())
	}

	visiblePart := parts[2]
	if visiblePart.Get("text").String() != "visible" {
		t.Fatalf("Expected trailing visible text, got %q", visiblePart.Get("text").String())
	}
	if visiblePart.Get("thought").Exists() {
		t.Fatalf("Expected visible segment to drop thought flag")
	}
	if visiblePart.Get("thoughtSignature").Exists() || visiblePart.Get("thought_signature").Exists() {
		t.Fatalf("Expected visible segment to drop signature metadata")
	}
	if visiblePart.Get("custom").String() != "value" {
		t.Fatalf("Expected visible segment to preserve custom metadata")
	}
}

func TestThinkingTagParser_SplitTaggedPartPreservesRawMetadata(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")
	signature := "sig_1234567890123456789012345678901234567899"
	rawObject := `{"nested":[1,{"flag":true}],"label":"stable"}`
	rawInteger := "9007199254740993"
	input := makePayloadWithParts(
		fmt.Sprintf(
			`{"text":%s,"thoughtSignature":"%s","custom":%s,"sequence":%s}`,
			strconv.Quote("<thinking>thought</thinking>visible"),
			signature,
			rawObject,
			rawInteger,
		),
	)

	result := parser.Process(input)
	parts := gjson.GetBytes(result, "response.candidates.0.content.parts").Array()
	if len(parts) != 3 {
		t.Fatalf("Expected 3 parts, got %d", len(parts))
	}

	thoughtPart := parts[0]
	if thoughtPart.Get("custom").Raw != rawObject {
		t.Fatalf("Expected thought segment to preserve raw object metadata, got %s", thoughtPart.Get("custom").Raw)
	}
	if thoughtPart.Get("sequence").Raw != rawInteger {
		t.Fatalf("Expected thought segment to preserve raw integer metadata, got %s", thoughtPart.Get("sequence").Raw)
	}

	visiblePart := parts[2]
	if visiblePart.Get("custom").Raw != rawObject {
		t.Fatalf("Expected visible segment to preserve raw object metadata, got %s", visiblePart.Get("custom").Raw)
	}
	if visiblePart.Get("sequence").Raw != rawInteger {
		t.Fatalf("Expected visible segment to preserve raw integer metadata, got %s", visiblePart.Get("sequence").Raw)
	}
}

func TestThinkingTagParser_SingleThoughtSegmentPreservesThoughtSignature(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")
	signature := "sig_1234567890123456789012345678901234567891"
	input := makePayloadWithParts(
		fmt.Sprintf(`{"text":%s,"thoughtSignature":"%s","custom":"value"}`, strconv.Quote("<thinking>thought</thinking>"), signature),
	)
	result := parser.Process(input)

	parts := gjson.GetBytes(result, "response.candidates.0.content.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("Expected 2 parts, got %d", len(parts))
	}

	thoughtPart := parts[0]
	if thoughtPart.Get("text").String() != "thought" || !thoughtPart.Get("thought").Bool() {
		t.Fatalf("Expected first part to be thought text, got text=%q thought=%v", thoughtPart.Get("text").String(), thoughtPart.Get("thought").Bool())
	}
	if thoughtPart.Get("custom").String() != "value" {
		t.Fatalf("Expected first part to preserve custom metadata")
	}
	if thoughtPart.Get("thoughtSignature").Exists() || thoughtPart.Get("thought_signature").Exists() {
		t.Fatalf("Expected first part to move signature off the text-bearing thought segment")
	}

	signaturePart := parts[1]
	if signaturePart.Get("text").String() != "" || !signaturePart.Get("thought").Bool() {
		t.Fatalf("Expected second part to be an empty thought signature part")
	}
	if signaturePart.Get("thoughtSignature").String() != signature {
		t.Fatalf("Expected signature %q, got %q", signature, signaturePart.Get("thoughtSignature").String())
	}
}

func TestThinkingTagParser_FunctionCallAfterThinking(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")

	parser.Process(makePayload("<thinking>thought</thinking>"))

	input := makeFunctionCallPayload("read", `{"filePath": "/test.js"}`)
	result := parser.Process(input)

	if !bytes.Equal(result, input) {
		t.Errorf("Expected function call to remain unchanged")
	}
}

func TestThinkingTagParser_EmptyThinkingBlock(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")
	input := makePayload("<thinking></thinking>")
	result := parser.Process(input)

	parts := gjson.GetBytes(result, "response.candidates.0.content.parts")
	partsArray := parts.Array()

	if len(partsArray) > 0 {
		for _, p := range partsArray {
			if p.Get("text").String() != "" {
				t.Errorf("Expected empty text in thinking block, got '%s'", p.Get("text").String())
			}
		}
	}
}

func TestThinkingTagParser_PartialTagFlushed(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")

	parser.Process(makePayload("text<t"))

	result := parser.Process(makePayload("ext"))

	parts := gjson.GetBytes(result, "response.candidates.0.content.parts")
	partsArray := parts.Array()

	if len(partsArray) < 1 {
		t.Fatalf("Expected parts to be processed")
	}

	found := false
	for _, p := range partsArray {
		text := p.Get("text").String()
		if text == "<text" || text == "text<text" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected partial tag to be flushed")
	}
}

func TestThinkingTagParser_UnicodeEscapedTags(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")

	input := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"\u003cthinking\u003econtent\u003c/thinking\u003e"}]}}],"modelVersion":"claude-opus-4-6-thinking"}}`)
	result := parser.Process(input)

	parts := gjson.GetBytes(result, "response.candidates.0.content.parts")
	partsArray := parts.Array()

	if len(partsArray) < 1 {
		t.Fatalf("Expected parts to be processed")
	}

	part := partsArray[0]
	text := part.Get("text").String()
	thought := part.Get("thought").Bool()

	if text != "content" {
		t.Errorf("Expected text='content' from unicode-escaped tags, got '%s'", text)
	}
	if !thought {
		t.Errorf("Expected thought=true for unicode-escaped thinking tag")
	}
}

func TestThinkingTagParser_MultipleThinkingBlocks(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")
	input := makePayload("<thinking>first</thinking>text<thinking>second</thinking>")
	result := parser.Process(input)

	parts := gjson.GetBytes(result, "response.candidates.0.content.parts")
	partsArray := parts.Array()

	if len(partsArray) != 3 {
		t.Fatalf("Expected 3 parts, got %d", len(partsArray))
	}

	if partsArray[0].Get("text").String() != "first" || !partsArray[0].Get("thought").Bool() {
		t.Errorf("Expected first part to be thinking 'first'")
	}

	if partsArray[1].Get("text").String() != "text" {
		t.Errorf("Expected second part to be regular 'text'")
	}

	if partsArray[2].Get("text").String() != "second" || !partsArray[2].Get("thought").Bool() {
		t.Errorf("Expected third part to be thinking 'second'")
	}
}

func TestThinkingTagParser_DonePayload_Passthrough(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")
	input := []byte("[DONE]")
	result := parser.Process(input)

	if !bytes.Equal(result, input) {
		t.Errorf("Expected [DONE] payload to be unchanged")
	}
}

func TestThinkingTagParser_NoPartsInPayload(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")
	input := []byte(`{"response":{"candidates":[{"content":{"role":"model"}}]}}`)
	result := parser.Process(input)

	if !bytes.Equal(result, input) {
		t.Errorf("Expected payload without parts to be unchanged")
	}
}

func TestThinkingTagParser_RealisticLogReplay(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")

	chunk1 := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"\u003cthinking"}]}}],"modelVersion":"claude-opus-4-6-thinking"}}`)
	result1 := parser.Process(chunk1)
	parts1 := gjson.GetBytes(result1, "response.candidates.0.content.parts")
	if len(parts1.Array()) > 0 {
		if parts1.Array()[0].Get("text").String() != "" {
			t.Errorf("Expected empty text for buffered tag")
		}
	}

	chunk2 := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"\u003e\nNow I can see the chain"}]}}],"modelVersion":"claude-opus-4-6-thinking"}}`)
	result2 := parser.Process(chunk2)
	parts2 := gjson.GetBytes(result2, "response.candidates.0.content.parts")
	arr2 := parts2.Array()
	if len(arr2) < 1 {
		t.Fatalf("Expected parts after opening tag completion")
	}
	part2 := arr2[0]
	if !part2.Get("thought").Bool() {
		t.Errorf("Expected thought=true for thinking content")
	}
	text2 := part2.Get("text").String()
	if text2 != "\nNow I can see the chain" {
		t.Errorf("Expected thinking content to be preserved, got '%s'", text2)
	}

	chunk3 := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":". The PointHistoryChartDrawer"}]}}],"modelVersion":"claude-opus-4-6-thinking"}}`)
	result3 := parser.Process(chunk3)
	parts3 := gjson.GetBytes(result3, "response.candidates.0.content.parts")
	arr3 := parts3.Array()
	if len(arr3) < 1 {
		t.Fatalf("Expected parts for continuation")
	}
	part3 := arr3[0]
	if !part3.Get("thought").Bool() {
		t.Errorf("Expected thought=true for continuation of thinking")
	}

	chunkClose := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"\n\u003c/thinking\u003e"}]}}],"modelVersion":"claude-opus-4-6-thinking"}}`)
	resultClose := parser.Process(chunkClose)
	partsClose := gjson.GetBytes(resultClose, "response.candidates.0.content.parts")
	_ = partsClose.Array()

	chunkFunc := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read","args":{"filePath":"/test.js"},"id":"toolu_test"}}]}}],"modelVersion":"claude-opus-4-6-thinking"}}`)
	resultFunc := parser.Process(chunkFunc)

	if !bytes.Equal(resultFunc, chunkFunc) {
		t.Errorf("Expected function call to remain unchanged after thinking")
	}
}

func TestThinkingTagParser_CloseTagChunkPreservesThoughtSignature(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")
	signature := "sig_1234567890123456789012345678901234567892"

	parser.Process(makePayload("<thinking>thought"))

	result := parser.Process(makePayloadWithParts(
		fmt.Sprintf(`{"text":%s,"thoughtSignature":"%s","custom":"value"}`, strconv.Quote("</thinking>visible"), signature),
	))

	parts := gjson.GetBytes(result, "response.candidates.0.content.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("Expected 2 parts, got %d", len(parts))
	}

	signaturePart := parts[0]
	if signaturePart.Get("text").String() != "" || !signaturePart.Get("thought").Bool() {
		t.Fatalf("Expected first part to be an empty thought signature part")
	}
	if signaturePart.Get("thoughtSignature").String() != signature {
		t.Fatalf("Expected signature %q, got %q", signature, signaturePart.Get("thoughtSignature").String())
	}

	visiblePart := parts[1]
	if visiblePart.Get("text").String() != "visible" {
		t.Fatalf("Expected trailing visible text, got %q", visiblePart.Get("text").String())
	}
	if visiblePart.Get("thought").Exists() {
		t.Fatalf("Expected visible segment to drop thought flag")
	}
	if visiblePart.Get("thoughtSignature").Exists() || visiblePart.Get("thought_signature").Exists() {
		t.Fatalf("Expected visible segment to drop signature metadata")
	}
	if visiblePart.Get("custom").String() != "value" {
		t.Fatalf("Expected visible segment to preserve custom metadata")
	}
}

func TestThinkingTagParser_NormalizedSignedThinkingShapeCachesSignature(t *testing.T) {
	cache.ClearSignatureCache("")

	parser := NewThinkingTagParser("claude-opus-4-6-thinking")
	validSignature := "splitSig_1234567890123456789012345678901234567890123456"
	parsed := parser.Process(makePayloadWithParts(
		fmt.Sprintf(`{"text":%s,"thoughtSignature":"%s"}`, strconv.Quote("<thinking>My tagged thinking</thinking>Visible text"), validSignature),
	))

	requestJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Cache split test"}]}]
	}`)

	var param any
	output := antigravityclaude.ConvertAntigravityResponseToClaude(context.Background(), "claude-sonnet-4-5-thinking", requestJSON, requestJSON, parsed, &param)
	outputText := strings.Join(output, "")

	if !strings.Contains(outputText, `"type":"signature_delta"`) {
		t.Fatal("Expected output to contain a signature delta")
	}
	if !strings.Contains(outputText, "Visible text") {
		t.Fatal("Expected output to include the visible text segment")
	}

	cachedSig := cache.GetCachedSignature("claude-sonnet-4-5-thinking", "My tagged thinking")
	if cachedSig != validSignature {
		t.Fatalf("Expected cached signature %q, got %q", validSignature, cachedSig)
	}

	params := param.(*antigravityclaude.Params)
	if params.CurrentThinkingText.Len() != 0 {
		t.Fatal("Expected thinking text to be reset after caching the signature")
	}
}

func TestThinkingTagParser_BufferManagement(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")

	parser.Process(makePayload("some text</t"))

	parser.Process(makePayload("hinking>"))

	if parser.tagBuffer != "" {
		t.Errorf("Expected buffer to be cleared after completing tag, got '%s'", parser.tagBuffer)
	}
}

func TestThinkingTagParser_ConsecutiveThinkingBlocks(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")
	input := makePayload("<thinking>first</thinking><thinking>second</thinking>")
	result := parser.Process(input)

	parts := gjson.GetBytes(result, "response.candidates.0.content.parts")
	partsArray := parts.Array()

	if len(partsArray) != 2 {
		t.Fatalf("Expected 2 parts, got %d", len(partsArray))
	}

	for i, part := range partsArray {
		if !part.Get("thought").Bool() {
			t.Errorf("Expected part %d to have thought=true", i)
		}
	}
}

func TestThinkingTagParser_NestedLookingText(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")
	input := makePayload("<thinking attr>text</thinking end>")
	result := parser.Process(input)

	if !bytes.Equal(result, input) {
		t.Errorf("Expected text with tag-like content to be unchanged")
	}
}

func TestThinkingTagParser_VeryLongThinkingContent(t *testing.T) {
	parser := NewThinkingTagParser("claude-opus-4-6-thinking")

	longContent := ""
	for i := 0; i < 1000; i++ {
		longContent += "This is a very long thinking block content. "
	}

	input := makePayload("<thinking>" + longContent + "</thinking>")
	result := parser.Process(input)

	parts := gjson.GetBytes(result, "response.candidates.0.content.parts")
	partsArray := parts.Array()

	if len(partsArray) < 1 {
		t.Fatalf("Expected parts for long content")
	}

	part := partsArray[0]
	if !part.Get("thought").Bool() {
		t.Errorf("Expected thought=true for long content")
	}

	text := part.Get("text").String()
	if text != longContent {
		t.Errorf("Expected full long content to be preserved")
	}
}

func TestThinkingTagParser_MixedCaseModelName(t *testing.T) {
	parser := NewThinkingTagParser("Claude-Opus-4-6-Thinking")
	input := makePayload("<thinking>content</thinking>")
	result := parser.Process(input)

	parts := gjson.GetBytes(result, "response.candidates.0.content.parts")
	partsArray := parts.Array()

	if len(partsArray) < 1 {
		t.Fatalf("Expected parser to be active for mixed-case Claude model")
	}

	part := partsArray[0]
	if !part.Get("thought").Bool() {
		t.Errorf("Expected mixed-case model name to work")
	}
}
