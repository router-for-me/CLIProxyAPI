package util

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ClaudeToolResultImage represents a base64-encoded image extracted from a Claude
// tool_result content block. Callers emit it as a provider-specific inline data
// part so that image bytes do not bloat the textual function response result.
type ClaudeToolResultImage struct {
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
	// Images holds base64 image blocks separated out of the content.
	Images []ClaudeToolResultImage
}

// ConvertClaudeToolResultContent normalizes a Claude tool_result `content` field into
// a deterministic Gemini functionResponse result plus any extracted images.
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
//   - base64 image block -> separated into Images (emitted as inline data parts)
//   - object             -> raw JSON result, or image -> Images with empty result
//   - absent/empty       -> empty string result
//
// Unlike Antigravity, image blocks without base64 data are dropped rather than
// emitted as empty inline data parts, matching the Gemini image part guards.
func ConvertClaudeToolResultContent(content gjson.Result) ClaudeToolResult {
	switch {
	case content.Type == gjson.String:
		return ClaudeToolResult{Result: content.String()}
	case content.IsArray():
		var images []ClaudeToolResultImage
		nonImageCount := 0
		lastNonImageRaw := ""
		filtered := []byte(`[]`)
		content.ForEach(func(_, block gjson.Result) bool {
			if isClaudeBase64Image(block) {
				if img, ok := claudeImageFromBlock(block); ok {
					images = append(images, img)
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
			return ClaudeToolResult{Result: lastNonImageRaw, ResultIsRaw: true, Images: images}
		case nonImageCount > 1:
			return ClaudeToolResult{Result: string(filtered), ResultIsRaw: true, Images: images}
		default:
			return ClaudeToolResult{Images: images}
		}
	case content.IsObject():
		if isClaudeBase64Image(content) {
			if img, ok := claudeImageFromBlock(content); ok {
				return ClaudeToolResult{Images: []ClaudeToolResultImage{img}}
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

// isClaudeBase64Image reports whether a content block is a base64-encoded image block.
func isClaudeBase64Image(block gjson.Result) bool {
	return block.Get("type").String() == "image" && block.Get("source.type").String() == "base64"
}

// claudeImageFromBlock extracts image data from a base64 image block. It returns false
// when the block carries no base64 data, so empty inline data parts are not emitted.
func claudeImageFromBlock(block gjson.Result) (ClaudeToolResultImage, bool) {
	data := block.Get("source.data").String()
	if data == "" {
		return ClaudeToolResultImage{}, false
	}
	return ClaudeToolResultImage{
		MimeType: block.Get("source.media_type").String(),
		Data:     data,
	}, true
}
