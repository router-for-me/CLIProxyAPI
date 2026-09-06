package helps

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAntigravityCompactionSealAndUnseal(t *testing.T) {
	summary := "This is a test summary of the previous turns with ANSI: \x1b[31mred\x1b[0m and quotes \"hello\" and newlines \n line2."
	model := "gemini-3.7-flash"

	capsule, errSeal := SealAntigravityCompaction(summary, model)
	if errSeal != nil {
		t.Fatalf("SealAntigravityCompaction failed: %v", errSeal)
	}

	if !strings.HasPrefix(capsule, antigravityCompactionCapsulePrefix) {
		t.Fatalf("capsule missing expected prefix: %s", capsule)
	}

	gotSummary, errUnseal := UnsealAntigravityCompaction(capsule)
	if errUnseal != nil {
		t.Fatalf("UnsealAntigravityCompaction failed: %v", errUnseal)
	}

	if gotSummary != summary {
		t.Fatalf("summary mismatch: got %q, want %q", gotSummary, summary)
	}

	// Test tampered capsule
	tampered := capsule[:len(capsule)-4] + "AAAA"
	_, errTampered := UnsealAntigravityCompaction(tampered)
	if errTampered == nil {
		t.Fatal("expected error when decrypting tampered capsule, got nil")
	}

	// Test invalid prefix
	_, errPrefix := UnsealAntigravityCompaction("invalid-prefix:abc")
	if errPrefix == nil {
		t.Fatal("expected error for invalid prefix, got nil")
	}
}

func TestAntigravityCompactionPersistentSecretAcrossAccountSwitch(t *testing.T) {
	// 1. Account A seals a capsule using the fixed secret
	capsule, errSeal := SealAntigravityCompaction("State before account failover", "gemini-3.7-flash")
	if errSeal != nil {
		t.Fatalf("seal before failover: %v", errSeal)
	}

	// 2. Account failover: account switches, capsule must successfully unseal without being bound to old account ID
	summaryAfterSwitch, errUnseal := UnsealAntigravityCompaction(capsule)
	if errUnseal != nil {
		t.Fatalf("failed to unseal capsule after account switch: %v", errUnseal)
	}
	if summaryAfterSwitch != "State before account failover" {
		t.Fatalf("summary mismatch after failover: got %q, want 'State before account failover'", summaryAfterSwitch)
	}
}

func TestPrepareAntigravityCompactionSummaryPayload(t *testing.T) {
	payload := []byte(`{
		"model": "gemini-3.7-flash",
		"stream": true,
		"tools": [{"type": "function", "name": "bash"}],
		"tool_choice": "auto",
		"previous_response_id": "resp-old",
		"input": [
			{"type": "message", "role": "user", "content": "hi"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "hello"}]},
			{"type": "compaction_trigger"}
		]
	}`)

	prepared := PrepareAntigravityCompactionSummaryPayload(payload, "gemini-3.7-flash")

	if !json.Valid(prepared) {
		t.Fatalf("prepared payload is not valid JSON: %s", string(prepared))
	}
	if gjson.GetBytes(prepared, "stream").Bool() {
		t.Fatal("expected stream to be false")
	}
	if gjson.GetBytes(prepared, "tools").Exists() {
		t.Fatal("expected tools to be removed")
	}
	if gjson.GetBytes(prepared, "tool_choice").Exists() {
		t.Fatal("expected tool_choice to be removed")
	}
	if gjson.GetBytes(prepared, "previous_response_id").Exists() {
		t.Fatal("expected previous_response_id to be removed")
	}

	input := gjson.GetBytes(prepared, "input").Array()
	if len(input) != 3 {
		t.Fatalf("expected 3 items in input, got %d", len(input))
	}

	for _, item := range input {
		if item.Get("type").String() == "compaction_trigger" {
			t.Fatal("compaction_trigger must be removed from summary payload")
		}
	}

	lastItem := input[len(input)-1]
	if lastItem.Get("role").String() != "user" {
		t.Fatalf("expected last turn to be role 'user', got %s", lastItem.Get("role").String())
	}
}

func TestPrepareAntigravityCompactionSummaryPayload_StringInputWithControlChars(t *testing.T) {
	rawJSON := `{"model":"gemini-3.7-flash","input":"before\u001b[31mafter\nnewline and \"quotes\""}`
	if !json.Valid([]byte(rawJSON)) {
		t.Fatal("test rawJSON must be valid JSON")
	}

	prepared := PrepareAntigravityCompactionSummaryPayload([]byte(rawJSON), "gemini-3.7-flash")
	if !json.Valid(prepared) {
		t.Fatalf("prepared output with control chars must be valid JSON, got: %s", string(prepared))
	}

	input := gjson.GetBytes(prepared, "input").Array()
	if len(input) != 2 {
		t.Fatalf("expected 2 items in input for string input, got %d", len(input))
	}

	gotText := input[0].Get("content.0.text").String()
	wantText := "before\x1b[31mafter\nnewline and \"quotes\""
	if gotText != wantText {
		t.Fatalf("string input was truncated or corrupted: got %q, want %q", gotText, wantText)
	}
	if input[1].Get("role").String() != "user" {
		t.Fatalf("expected second item to be user summary prompt, got %s", input[1].Get("role").String())
	}
}

func TestExpandAntigravityCompactionCapsules(t *testing.T) {
	originalSummary := "Previous work done with \x1b[32mcontrol chars\x1b[0m and quotes: \"test\""
	capsule, errSeal := SealAntigravityCompaction(originalSummary, "gemini-3.7-flash")
	if errSeal != nil {
		t.Fatalf("seal: %v", errSeal)
	}

	payload := []byte(`{
		"model": "gemini-3.7-flash",
		"input": [
			{"type": "compaction", "id": "cmp-1", "encrypted_content": "` + capsule + `"},
			{"type": "message", "role": "user", "content": "next task"}
		]
	}`)

	expanded, errExpand := ExpandAntigravityCompactionCapsules(payload)
	if errExpand != nil {
		t.Fatalf("ExpandAntigravityCompactionCapsules failed: %v", errExpand)
	}
	if !json.Valid(expanded) {
		t.Fatalf("expanded payload is not valid JSON: %s", string(expanded))
	}

	input := gjson.GetBytes(expanded, "input").Array()
	if len(input) != 2 {
		t.Fatalf("expected 2 items, got %d", len(input))
	}

	firstItem := input[0]
	if firstItem.Get("type").String() != "message" {
		t.Fatalf("expected first item type 'message', got %s", firstItem.Get("type").String())
	}
	if firstItem.Get("role").String() != "developer" {
		t.Fatalf("expected first item role 'developer', got %s", firstItem.Get("role").String())
	}
	expandedText := firstItem.Get("content.0.text").String()
	if !strings.Contains(expandedText, originalSummary) {
		t.Fatalf("expected original summary preserved without corruption, got: %q", expandedText)
	}

	// Test invalid capsule returns error
	badPayload := []byte(`{
		"model": "gemini-3.7-flash",
		"input": [
			{"type": "compaction", "id": "cmp-1", "encrypted_content": "cpa-ag-compact-v1:corrupted"},
			{"type": "message", "role": "user", "content": "next task"}
		]
	}`)
	_, errBad := ExpandAntigravityCompactionCapsules(badPayload)
	if errBad == nil {
		t.Fatal("expected error for corrupted capsule, got nil")
	}
}

func TestExtractAntigravitySummaryText_ReasoningBeforeMessage(t *testing.T) {
	// Responses format where output.0 is reasoning and output.1 is the actual message
	resp := []byte(`{
		"output": [
			{
				"type": "reasoning",
				"summary": [{"type": "summary_text", "text": "thinking..."}]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [
					{"type": "output_text", "text": "This is the actual summary text."}
				]
			}
		]
	}`)

	text, err := ExtractAntigravitySummaryText(resp)
	if err != nil {
		t.Fatalf("ExtractAntigravitySummaryText failed: %v", err)
	}
	if text != "This is the actual summary text." {
		t.Fatalf("expected actual summary text, got %q", text)
	}

	// Empty output must return an error rather than placeholder
	emptyResp := []byte(`{"output": [{"type": "reasoning"}]}`)
	_, errEmpty := ExtractAntigravitySummaryText(emptyResp)
	if errEmpty == nil {
		t.Fatal("expected error when no summary text exists, got nil")
	}
}

func TestBuildAntigravityCompactionResponseAndChunks_ValidJSON(t *testing.T) {
	respJSON := BuildAntigravityCompactionResponse("gemini-3.7-flash", "cpa-ag-compact-v1:abc", 10, 20, 30)
	if !json.Valid(respJSON) {
		t.Fatalf("BuildAntigravityCompactionResponse produced invalid JSON: %s", string(respJSON))
	}

	chunks := BuildAntigravityCompactionStreamChunks("gemini-3.7-flash", "cpa-ag-compact-v1:abc", 10, 20, 30)
	if len(chunks) != 5 {
		t.Fatalf("expected 5 SSE frames, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		lines := strings.Split(string(chunk), "\n")
		var dataLine string
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				dataLine = strings.TrimPrefix(line, "data: ")
				break
			}
		}
		if dataLine == "" || !json.Valid([]byte(dataLine)) {
			t.Fatalf("chunk[%d] data payload is not valid JSON: %q", i, dataLine)
		}
	}
}
