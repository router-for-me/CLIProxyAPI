package helps

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/tiktoken-go/tokenizer"
)

// CountResponsesInputTokens estimates model-visible text and images in a Responses
// request. It excludes transport metadata and opaque encrypted state. It is not
// an authoritative provider count or a complete estimate of protocol framing.
func CountResponsesInputTokens(enc tokenizer.Codec, body []byte) (int64, error) {
	if enc == nil {
		return 0, fmt.Errorf("encoder is nil")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return 0, nil
	}
	if !ojson.ValidBytes(body) {
		return 0, fmt.Errorf("invalid Responses request JSON")
	}
	root := ojson.ParseBytes(body)
	segments := make([]string, 0, 32)
	model := root.Get("model").String()
	var imageTokens int64
	appendResponseTokenText(&segments, root.Get("instructions").String())
	input := root.Get("input")
	if input.Type == ojson.String {
		appendResponseTokenText(&segments, input.String())
	} else if input.IsArray() {
		input.ForEach(func(_, item ojson.Result) bool {
			switch item.Get("type").String() {
			case "message", "":
				if item.Get("role").Exists() || item.Get("content").Exists() {
					appendResponseTokenText(&segments, item.Get("role").String())
					imageTokens += collectResponseTokenContent(model, item.Get("content"), &segments)
				} else {
					appendResponseTokenText(&segments, item.Get("text").String())
				}
			case "function_call":
				appendResponseTokenText(&segments, item.Get("name").String())
				appendResponseTokenText(&segments, item.Get("arguments").String())
			case "custom_tool_call":
				appendResponseTokenText(&segments, item.Get("name").String())
				appendResponseTokenText(&segments, item.Get("input").String())
			case "function_call_output", "custom_tool_call_output":
				imageTokens += collectResponseTokenContent(model, item.Get("output"), &segments)
			case "reasoning":
				imageTokens += collectResponseTokenContent(model, item.Get("summary"), &segments)
			case "additional_tools":
				appendResponseTokenJSON(&segments, item.Get("tools"))
			default:
				appendResponseTokenText(&segments, item.Get("text").String())
			}
			return true
		})
	}
	root.Get("tools").ForEach(func(_, tool ojson.Result) bool {
		appendResponseTokenJSON(&segments, tool)
		return true
	})
	appendResponseTokenJSON(&segments, root.Get("text.format"))
	if len(segments) == 0 {
		return imageTokens, nil
	}
	count, err := enc.Count(strings.Join(segments, "\n"))
	if err != nil {
		return 0, err
	}
	return int64(count) + imageTokens, nil
}

func collectResponseTokenContent(model string, content ojson.Result, segments *[]string) int64 {
	if content.Type == ojson.String {
		appendResponseTokenText(segments, content.String())
		return 0
	}
	if content.IsArray() {
		var images int64
		content.ForEach(func(_, part ojson.Result) bool {
			images += collectResponseTokenContent(model, part, segments)
			return true
		})
		return images
	}
	switch content.Get("type").String() {
	case "input_text", "output_text", "text", "summary_text":
		appendResponseTokenText(segments, content.Get("text").String())
	case "refusal":
		appendResponseTokenText(segments, content.Get("refusal").String())
	case "input_image":
		return estimateCodexImageTokens(model, content)
	}
	return 0
}

func appendResponseTokenText(segments *[]string, text string) {
	// Indentation, leading newlines and whitespace-only messages are model input.
	if text != "" {
		*segments = append(*segments, text)
	}
}

func appendResponseTokenJSON(segments *[]string, value ojson.Result) {
	if !value.Exists() {
		return
	}
	if value.Type == ojson.String {
		appendResponseTokenText(segments, value.String())
		return
	}
	appendResponseTokenText(segments, value.Get("@ugly").Raw)
}
