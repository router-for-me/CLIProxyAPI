// Package claude provides response translation functionality for Kiro API to Claude format.
// This package handles the conversion of Kiro API responses into Claude-compatible format,
// including support for thinking blocks and tool use.
package claude

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"

	kirocommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/kiro/common"
)

// generateThinkingSignature generates a signature for thinking content.
// This is required by Claude API for thinking blocks in non-streaming responses.
// The signature is a base64-encoded hash of the thinking content.
func generateThinkingSignature(thinkingContent string) string {
	if thinkingContent == "" {
		return ""
	}
	// Generate a deterministic signature based on content hash
	hash := sha256.Sum256([]byte(thinkingContent))
	return base64.StdEncoding.EncodeToString(hash[:])
}

// Local references to kirocommon constants for thinking block parsing
var (
	thinkingStartTag = kirocommon.ThinkingStartTag
	thinkingEndTag   = kirocommon.ThinkingEndTag
)

// BuildClaudeResponse constructs a Claude-compatible response.
// Supports tool_use blocks when tools are present in the response.
// Supports thinking blocks - parses <thinking> tags and converts to Claude thinking content blocks.
// stopReason is passed from upstream; fallback logic applied if empty.
func BuildClaudeResponse(content string, toolUses []KiroToolUse, model string, usageInfo usage.Detail, stopReason string) []byte {
	var contentBlocks []map[string]interface{}

	// Extract thinking blocks and text from content
	if content != "" {
		blocks := ExtractThinkingFromContent(content)
		contentBlocks = append(contentBlocks, blocks...)

		// Log if thinking blocks were extracted
		for _, block := range blocks {
			if block["type"] == "thinking" {
				thinkingContent := block["thinking"].(string)
				log.Infof("kiro: buildClaudeResponse extracted thinking block (len: %d)", len(thinkingContent))
			}
		}
	}

	// Add tool_use blocks
	for _, toolUse := range toolUses {
		contentBlocks = append(contentBlocks, map[string]interface{}{
			"type":  "tool_use",
			"id":    toolUse.ToolUseID,
			"name":  toolUse.Name,
			"input": toolUse.Input,
		})
	}

	// Ensure at least one content block (Claude API requires non-empty content)
	if len(contentBlocks) == 0 {
		contentBlocks = append(contentBlocks, map[string]interface{}{
			"type": "text",
			"text": "",
		})
	}

	// Use upstream stopReason; apply fallback logic if not provided
	if stopReason == "" {
		stopReason = "end_turn"
		if len(toolUses) > 0 {
			stopReason = "tool_use"
		}
		log.Debugf("kiro: buildClaudeResponse using fallback stop_reason: %s", stopReason)
	}

	// Log warning if response was truncated due to max_tokens
	if stopReason == "max_tokens" {
		log.Warnf("kiro: response truncated due to max_tokens limit (buildClaudeResponse)")
	}

	response := map[string]interface{}{
		"id":          "msg_" + uuid.New().String()[:24],
		"type":        "message",
		"role":        "assistant",
		"model":       model,
		"content":     contentBlocks,
		"stop_reason": stopReason,
		"usage": map[string]interface{}{
			"input_tokens":  usageInfo.InputTokens,
			"output_tokens": usageInfo.OutputTokens,
		},
	}
	result, _ := json.Marshal(response)
	return result
}

// ExtractThinkingFromContent parses content to extract thinking blocks and text.
// Returns a list of content blocks in the order they appear in the content.
// Handles interleaved thinking and text blocks correctly.
func ExtractThinkingFromContent(content string) []map[string]interface{} {
	var blocks []map[string]interface{}

	if content == "" {
		return blocks
	}

	// Check if content contains thinking tags at all
	if !strings.Contains(content, thinkingStartTag) {
		// No thinking tags, return as plain text
		return []map[string]interface{}{
			{
				"type": "text",
				"text": content,
			},
		}
	}

	log.Debugf("kiro: extractThinkingFromContent - found thinking tags in content (len: %d)", len(content))

	remaining := content

	for len(remaining) > 0 {
		// Look for <thinking> tag
		startIdx := strings.Index(remaining, thinkingStartTag)

		if startIdx == -1 {
			// No more thinking tags, add remaining as text
			if strings.TrimSpace(remaining) != "" {
				blocks = append(blocks, map[string]interface{}{
					"type": "text",
					"text": remaining,
				})
			}
			break
		}

		// Add text before thinking tag (if any meaningful content)
		if startIdx > 0 {
			textBefore := remaining[:startIdx]
			if strings.TrimSpace(textBefore) != "" {
				blocks = append(blocks, map[string]interface{}{
					"type": "text",
					"text": textBefore,
				})
			}
		}

		// Move past the opening tag
		remaining = remaining[startIdx+len(thinkingStartTag):]

		// Find closing tag
		endIdx := strings.Index(remaining, thinkingEndTag)

		if endIdx == -1 {
			// No closing tag found, treat rest as thinking content (incomplete response)
			if strings.TrimSpace(remaining) != "" {
				// Generate signature for thinking content (required by Claude API)
				signature := generateThinkingSignature(remaining)
				blocks = append(blocks, map[string]interface{}{
					"type":      "thinking",
					"thinking":  remaining,
					"signature": signature,
				})
				log.Warnf("kiro: extractThinkingFromContent - missing closing </thinking> tag")
			}
			break
		}

		// Extract thinking content between tags
		thinkContent := remaining[:endIdx]
		if strings.TrimSpace(thinkContent) != "" {
			// Generate signature for thinking content (required by Claude API)
			signature := generateThinkingSignature(thinkContent)
			blocks = append(blocks, map[string]interface{}{
				"type":      "thinking",
				"thinking":  thinkContent,
				"signature": signature,
			})
			log.Debugf("kiro: extractThinkingFromContent - extracted thinking block (len: %d)", len(thinkContent))
		}

		// Move past the closing tag
		remaining = remaining[endIdx+len(thinkingEndTag):]
	}

	// If no blocks were created (all whitespace), return empty text block
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]interface{}{
			"type": "text",
			"text": "",
		})
	}

	return blocks
}