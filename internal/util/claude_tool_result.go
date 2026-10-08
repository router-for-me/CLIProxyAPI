package util

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ClaudeToolResultInlineData represents base64 bytes, an image or a document, extracted from
// a Claude tool_result content block. Callers emit it as a provider-specific inline data
// part so that file bytes do not bloat the textual function response result.
type ClaudeToolResultInlineData struct {
	MimeType string
	Data     string
}

// ClaudeToolResult is the normalized form of a Claude tool_result `content` field,
// ready to be written into a Gemini-style functionResponse.
type ClaudeToolResult struct {
	// Result is the value for functionResponse.response.result.
	Result string
	// ResultIsRaw reports whether Result holds raw JSON (write with sjson.SetRaw*)
	// or a plain string (write with sjson.Set*). Writing raw JSON text through
	// sjson.Set as a string value would double-encode it, so callers must honor
	// this flag.
	ResultIsRaw bool
	// InlineData holds base64 image and document blocks separated out of the content.
	InlineData []ClaudeToolResultInlineData
}

// ConvertClaudeToolResultContent normalizes a Claude tool_result `content` field into
// a deterministic Gemini functionResponse result plus any extracted inline data.
//
// Claude tool_result content may be a plain string, an array of mixed text/image
// blocks, a single object, or absent. Some Claude->Gemini translators previously
// wrote content.Raw straight through sjson.SetBytes, which double-encoded string
// content and flattened structured arrays (including base64 image data) into one
// opaque escaped string. This helper mirrors the Antigravity Claude translator,
// which already handles structured content correctly:
//
//   - string             -> plain string result (no double-encoding)
//   - single non-image   -> raw JSON result (structure preserved)
//   - multiple non-image -> raw JSON array result
//   - base64 image or document block -> separated into InlineData (emitted as inline data parts)
//   - object             -> raw JSON result, or image -> InlineData with empty result
//   - absent/empty       -> empty string result
//
// Unlike Antigravity, image blocks without base64 data are dropped rather than
// emitted as empty inline data parts, matching the Gemini image part guards.
func ConvertClaudeToolResultContent(content gjson.Result) ClaudeToolResult {
	switch {
	case content.Type == gjson.String:
		return ClaudeToolResult{Result: content.String()}
	case content.IsArray():
		var inline []ClaudeToolResultInlineData
		nonImageCount := 0
		lastNonImageRaw := ""
		filtered := []byte(`[]`)
		content.ForEach(func(_, block gjson.Result) bool {
			if isClaudeBase64Media(block) {
				if img, ok := claudeInlineDataFromBlock(block); ok {
					inline = append(inline, img)
				}
				return true
			}
			nonImageCount++
			lastNonImageRaw = block.Raw
			filtered, _ = sjson.SetRawBytes(filtered, "-1", []byte(block.Raw))
			return true
		})
		switch {
		case nonImageCount == 1:
			return ClaudeToolResult{Result: lastNonImageRaw, ResultIsRaw: true, InlineData: inline}
		case nonImageCount > 1:
			return ClaudeToolResult{Result: string(filtered), ResultIsRaw: true, InlineData: inline}
		default:
			return ClaudeToolResult{InlineData: inline}
		}
	case content.IsObject():
		if isClaudeBase64Media(content) {
			if img, ok := claudeInlineDataFromBlock(content); ok {
				return ClaudeToolResult{InlineData: []ClaudeToolResultInlineData{img}}
			}
			return ClaudeToolResult{}
		}
		return ClaudeToolResult{Result: content.Raw, ResultIsRaw: true}
	case content.Raw != "":
		return ClaudeToolResult{Result: content.Raw, ResultIsRaw: true}
	default:
		return ClaudeToolResult{}
	}
}

// isClaudeBase64Media reports whether a content block is base64 bytes Gemini can take inline:
// an image, or a document that names its media type.
func isClaudeBase64Media(block gjson.Result) bool {
	if block.Get("source.type").String() != "base64" {
		return false
	}
	switch block.Get("type").String() {
	case "image":
		return true
	case "document":
		return block.Get("source.media_type").String() != "" && block.Get("source.data").String() != ""
	}
	return false
}

// claudeInlineDataFromBlock extracts the bytes of a base64 image or document block. It returns false
// when the block carries no base64 data, so empty inline data parts are not emitted.
func claudeInlineDataFromBlock(block gjson.Result) (ClaudeToolResultInlineData, bool) {
	data := block.Get("source.data").String()
	if data == "" {
		return ClaudeToolResultInlineData{}, false
	}
	return ClaudeToolResultInlineData{
		MimeType: block.Get("source.media_type").String(),
		Data:     data,
	}, true
}
