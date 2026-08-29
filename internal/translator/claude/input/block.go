package input

import "github.com/tidwall/sjson"

// ToolUseBlock builds a provider-independent Claude tool_use content block.
func ToolUseBlock(id, name string, input []byte) []byte {
	block := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
	block, _ = sjson.SetBytes(block, "id", id)
	block, _ = sjson.SetBytes(block, "name", name)
	if len(input) > 0 {
		block, _ = sjson.SetRawBytes(block, "input", input)
	}
	return block
}

// ToolResultBlock builds a provider-independent Claude tool_result block. The
// content argument must contain its final JSON representation.
func ToolResultBlock(toolUseID string, content []byte) []byte {
	block := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)
	block, _ = sjson.SetBytes(block, "tool_use_id", toolUseID)
	if len(content) > 0 {
		block, _ = sjson.SetRawBytes(block, "content", content)
	}
	return block
}

// ToolResultTextBlock builds a Claude tool_result with string content.
func ToolResultTextBlock(toolUseID, content string) []byte {
	block := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)
	block, _ = sjson.SetBytes(block, "tool_use_id", toolUseID)
	block, _ = sjson.SetBytes(block, "content", content)
	return block
}

// ThinkingBlock builds a Claude thinking block and includes a signature only
// when one is available.
func ThinkingBlock(text, signature string) []byte {
	block := []byte(`{"type":"thinking","thinking":""}`)
	block, _ = sjson.SetBytes(block, "thinking", text)
	if signature != "" {
		block, _ = sjson.SetBytes(block, "signature", signature)
	}
	return block
}

// RedactedThinkingBlock builds a Claude redacted_thinking content block.
func RedactedThinkingBlock(data string) []byte {
	block := []byte(`{"type":"redacted_thinking","data":""}`)
	block, _ = sjson.SetBytes(block, "data", data)
	return block
}
