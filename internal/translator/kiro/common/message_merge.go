// Package common provides shared utilities for Kiro translators.
package common

import (
	"encoding/json"

	"github.com/tidwall/gjson"
)

// MergeAdjacentMessages merges adjacent messages with the same role.
// This reduces API call complexity and improves compatibility.
// Based on AIClient-2-API implementation.
// NOTE: Tool messages are NOT merged because each has a unique tool_call_id that must be preserved.
func MergeAdjacentMessages(messages []gjson.Result) []gjson.Result {
	if len(messages) <= 1 {
		return messages
	}

	var merged []gjson.Result
	for _, msg := range messages {
		if len(merged) == 0 {
			merged = append(merged, msg)
			continue
		}

		lastMsg := merged[len(merged)-1]
		currentRole := msg.Get("role").String()
		lastRole := lastMsg.Get("role").String()

		// Don't merge tool messages - each has a unique tool_call_id
		if currentRole == "tool" || lastRole == "tool" {
			merged = append(merged, msg)
			continue
		}

		if currentRole == lastRole {
			// Merge content from current message into last message
			mergedContent := mergeMessageContent(lastMsg, msg)
			// Create a new merged message JSON
			mergedMsg := createMergedMessage(lastRole, mergedContent)
			merged[len(merged)-1] = gjson.Parse(mergedMsg)
		} else {
			merged = append(merged, msg)
		}
	}

	return merged
}

// mergeMessageContent merges the content of two messages with the same role.
// Handles both string content and array content (with text, tool_use, tool_result blocks).
func mergeMessageContent(msg1, msg2 gjson.Result) string {
	content1 := msg1.Get("content")
	content2 := msg2.Get("content")

	// Extract content blocks from both messages
	var blocks1, blocks2 []map[string]interface{}

	if content1.IsArray() {
		for _, block := range content1.Array() {
			blocks1 = append(blocks1, blockToMap(block))
		}
	} else if content1.Type == gjson.String {
		blocks1 = append(blocks1, map[string]interface{}{
			"type": "text",
			"text": content1.String(),
		})
	}

	if content2.IsArray() {
		for _, block := range content2.Array() {
			blocks2 = append(blocks2, blockToMap(block))
		}
	} else if content2.Type == gjson.String {
		blocks2 = append(blocks2, map[string]interface{}{
			"type": "text",
			"text": content2.String(),
		})
	}

	// Merge text blocks if both end/start with text
	if len(blocks1) > 0 && len(blocks2) > 0 {
		if blocks1[len(blocks1)-1]["type"] == "text" && blocks2[0]["type"] == "text" {
			// Merge the last text block of msg1 with the first text block of msg2
			text1 := blocks1[len(blocks1)-1]["text"].(string)
			text2 := blocks2[0]["text"].(string)
			blocks1[len(blocks1)-1]["text"] = text1 + "\n" + text2
			blocks2 = blocks2[1:] // Remove the merged block from blocks2
		}
	}

	// Combine all blocks
	allBlocks := append(blocks1, blocks2...)

	// Convert to JSON
	result, _ := json.Marshal(allBlocks)
	return string(result)
}

// blockToMap converts a gjson.Result block to a map[string]interface{}
func blockToMap(block gjson.Result) map[string]interface{} {
	result := make(map[string]interface{})
	block.ForEach(func(key, value gjson.Result) bool {
		if value.IsObject() {
			result[key.String()] = blockToMap(value)
		} else if value.IsArray() {
			var arr []interface{}
			for _, item := range value.Array() {
				if item.IsObject() {
					arr = append(arr, blockToMap(item))
				} else {
					arr = append(arr, item.Value())
				}
			}
			result[key.String()] = arr
		} else {
			result[key.String()] = value.Value()
		}
		return true
	})
	return result
}

// createMergedMessage creates a JSON string for a merged message
func createMergedMessage(role string, content string) string {
	msg := map[string]interface{}{
		"role":    role,
		"content": json.RawMessage(content),
	}
	result, _ := json.Marshal(msg)
	return string(result)
}