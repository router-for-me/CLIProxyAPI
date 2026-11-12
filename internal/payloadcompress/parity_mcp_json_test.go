package payloadcompress

import (
	"encoding/json"
	"strings"
	"testing"
)

// Upstream 616dc5bebc: a truncated MCP tool result must stay valid JSON. Slicing
// the serialized string (the pre-fix behavior) hands the model unparseable JSON.

func TestMCPTruncationKeepsJSONParseable(t *testing.T) {
	inner := strings.Repeat("abcdefghij", 5415) // 54 150 chars of string content
	input := `{"found":true,"content":"` + inner + `"}`

	out, changed := mcpAccessibilityEngine{}.compress(input)
	if !changed {
		t.Fatal("expected an oversized JSON result to be truncated")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("truncated output is not valid JSON: %v\n%s", err, out)
	}
	if parsed[mcpJSONTruncatedField] != true {
		t.Fatalf("truncated mark missing: %v", parsed[mcpJSONTruncatedField])
	}
	if parsed["hint"] != mcpTruncationHint {
		t.Fatalf("hint = %v", parsed["hint"])
	}
	content, ok := parsed["content"].(string)
	if !ok {
		t.Fatalf("content is not a string: %T", parsed["content"])
	}
	if len(content) >= len(inner) {
		t.Fatalf("content was not bounded: %d", len(content))
	}
	if len(out) > mcpDefaultMaxTextChars {
		t.Fatalf("output exceeds maxTextChars: %d", len(out))
	}
}

func TestMCPTruncationShrinksTextBlocks(t *testing.T) {
	blocks := make([]any, 0, 2)
	for i := 0; i < 2; i++ {
		blocks = append(blocks, map[string]any{
			"type": "text",
			"text": strings.Repeat("0123456789", 3000), // 30 000 chars each
		})
	}
	raw, err := json.Marshal(map[string]any{"content": blocks})
	if err != nil {
		t.Fatal(err)
	}

	out, changed := mcpAccessibilityEngine{}.compress(string(raw))
	if !changed {
		t.Fatal("expected truncation")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	gotBlocks, ok := parsed["content"].([]any)
	if !ok || len(gotBlocks) != 2 {
		t.Fatalf("content blocks = %v", parsed["content"])
	}
	for i, raw := range gotBlocks {
		block, _ := raw.(map[string]any)
		if block[mcpJSONTruncatedField] != true {
			t.Fatalf("block %d missing the truncation mark: %v", i, block)
		}
		// Each block gets an even share of the 50 000-char budget, not a fixed
		// per-block cap, so assert it was cut rather than pin a size.
		if text, _ := block["text"].(string); len(text) >= 30000 {
			t.Fatalf("block %d text was not shrunk: %d", i, len(text))
		}
	}
}

// A result whose other fields alone exceed the limit cannot shrink its content
// under budget; it must still not exceed maxTextChars and must stay parseable.
func TestMCPTruncationEnvelopeForUnshrinkableResult(t *testing.T) {
	big := strings.Repeat("x", mcpDefaultMaxTextChars+1000)
	input := `{"meta":"` + big + `","content":"` + strings.Repeat("y", 60000) + `"}`

	out, changed := mcpAccessibilityEngine{}.compress(input)
	if !changed {
		t.Fatal("expected truncation")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("envelope is not valid JSON: %v", err)
	}
	if parsed[mcpJSONTruncatedField] != true {
		t.Fatalf("envelope missing the truncation mark")
	}
	if _, ok := parsed["preview"].(string); !ok {
		t.Fatalf("envelope missing a preview: %v", parsed["preview"])
	}
	if len(out) > mcpDefaultMaxTextChars {
		t.Fatalf("envelope exceeds maxTextChars: %d", len(out))
	}
}

// Plain accessibility trees take the line path unchanged: the JSON fast-path must
// not swallow text that merely happens to be large.
func TestMCPPlainTextStillUsesLinePath(t *testing.T) {
	input := strings.Repeat(`  - paragraph "unique line `, 3000) + "tail"
	out, changed := mcpAccessibilityEngine{}.compress(input)
	if !changed {
		t.Fatal("expected the line path to truncate")
	}
	if strings.Contains(out, `"`+mcpJSONTruncatedField+`":true`) {
		t.Fatalf("plain text took the JSON path: %q", out[:200])
	}
	if !strings.Contains(out, mcpTruncationHint) {
		t.Fatalf("plain truncation notice missing: %q", out[len(out)-400:])
	}
	// The hint has no closing bracket of its own; the notice must end with the
	// hint's own final period, not a stray `]` left over from the old wording.
	if !strings.HasSuffix(out, "with an offset.") {
		t.Fatalf("truncation notice has a stray suffix: %q", out[len(out)-120:])
	}
}
