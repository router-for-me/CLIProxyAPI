package util

import (
	"github.com/MoeForge/moego/ojson"
)

// ClaudeToolResultImage represents a base64-encoded media block (image or document)
// extracted from a Claude tool_result content block. Callers emit it as a
// provider-specific inline data part so that media bytes do not bloat the textual
// function response result.
type ClaudeToolResultImage struct {
	MimeType string
	Data     string
}

// ClaudeToolResult is the normalized form of a Claude tool_result block,
// ready to be written into a Gemini-style functionResponse.
type ClaudeToolResult struct {
	// Result is the value for functionResponse.response.result or .error.
	Result string
	// ResultIsRaw reports whether Result holds raw JSON (write with ojson.SetRaw*)
	// or a plain string (write with ojson.Set*). Writing raw JSON text through
	// ojson.Set as a string value would double-encode it, so callers must honor
	// this flag.
	ResultIsRaw bool
	// IsError preserves an explicit tool_result.is_error failure flag.
	IsError bool
	// Images holds base64 image blocks separated out of the content.
	Images []ClaudeToolResultImage
}

// ConvertClaudeToolResult normalizes a complete Claude tool_result block while
// preserving its success/failure semantics.
func ConvertClaudeToolResult(block ojson.Result) ClaudeToolResult {
	result := ConvertClaudeToolResultContent(block.Get("content"))
	result.IsError = block.Get("is_error").Type == ojson.True
	return result
}

// ConvertClaudeToolResultContent normalizes a Claude tool_result `content` field into
// a deterministic Gemini functionResponse result plus any extracted images.
//
// Claude tool_result content may be a plain string, an array of mixed text/image
// blocks, a single object, or absent. Some Claude->Gemini translators previously
// wrote content.Raw straight through ojson.SetBytes, which double-encoded string
// content and flattened structured arrays (including base64 image data) into one
// opaque escaped string. This helper mirrors the Antigravity Claude translator,
// which already handles structured content correctly:
//
//   - string             -> plain string result (no double-encoding)
//   - single non-image   -> raw JSON result (structure preserved)
//   - multiple non-image -> raw JSON array result
//   - base64 media block -> separated into Images (image or document emitted as inline data parts)
//   - object             -> raw JSON result, or media -> Images with empty result
//   - absent/empty       -> empty string result
//
// Unlike Antigravity, image/document blocks without base64 data are dropped rather than
// emitted as empty inline data parts, matching the Gemini inline data part guards.
func ConvertClaudeToolResultContent(content ojson.Result) ClaudeToolResult {
	switch {
	case content.Type == ojson.String:
		return ClaudeToolResult{Result: content.String()}
	case content.IsArray():
		var images []ClaudeToolResultImage
		nonImageCount := 0
		lastNonImageRaw := ""
		filteredDoc := ojson.NewDoc([]byte(`[]`))
		content.ForEach(func(_, block ojson.Result) bool {
			if isClaudeBase64Media(block) {
				if img, ok := claudeImageFromBlock(block); ok {
					images = append(images, img)
				}
				return true
			}
			nonImageCount++
			lastNonImageRaw = block.Raw
			_ = filteredDoc.SetRaw("-1", block.Raw)
			return true
		})
		switch {
		case nonImageCount == 1:
			return ClaudeToolResult{Result: lastNonImageRaw, ResultIsRaw: true, Images: images}
		case nonImageCount > 1:
			return ClaudeToolResult{Result: filteredDoc.String(), ResultIsRaw: true, Images: images}
		default:
			return ClaudeToolResult{Images: images}
		}
	case content.IsObject():
		if isClaudeBase64Media(content) {
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

// isClaudeBase64Media reports whether a content block is a base64-encoded image or document block.
func isClaudeBase64Media(block ojson.Result) bool {
	blockType := block.Get("type").String()
	return (blockType == "image" || blockType == "document") && block.Get("source.type").String() == "base64"
}

// claudeImageFromBlock extracts inline media data from a base64 image or document block.
// It returns false when the block carries no base64 data (or no media_type for documents),
// so empty inline data parts are not emitted.
func claudeImageFromBlock(block ojson.Result) (ClaudeToolResultImage, bool) {
	data := block.Get("source.data").String()
	if data == "" {
		return ClaudeToolResultImage{}, false
	}
	mimeType := block.Get("source.media_type").String()
	if block.Get("type").String() == "document" && mimeType == "" {
		return ClaudeToolResultImage{}, false
	}
	return ClaudeToolResultImage{
		MimeType: mimeType,
		Data:     data,
	}, true
}
