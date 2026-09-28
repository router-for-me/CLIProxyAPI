package autorouter

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/tidwall/gjson"
)

// JevStateInput is the subset of an extracted request the Jev classifier gate
// sends as state. It is exported so the request path can build the classifier
// state without widening the scorer's internal extractedRequest type, which is
// tuned for lexing rather than for these shape signals.
type JevStateInput struct {
	LatestUserText      string
	MessageCount        int
	HistoryWordEstimate int
	HasTools            bool
	HasCodeFence        bool
	HasImages           bool
}

// jevImagePartTypes are the content-part type values that carry an image in the
// OpenAI, Claude, and Responses formats. Gemini carries images as inline data
// instead, so it is detected structurally (see hasImageParts).
var jevImagePartTypes = map[string]bool{
	"image":       true, // Claude
	"image_url":   true, // OpenAI chat completions
	"input_image": true, // OpenAI Responses
}

// ExtractJevStateInput extracts the classifier's state inputs from a raw
// request body. It shares the scorer's per-format text extraction so the gate
// supports exactly the same request shapes, and derives the shape signals with
// gjson the same way the scorer derives its own dimensions.
//
// MessageCount and HistoryWordEstimate are best-effort shape hints, not exact
// transcript sizes: formats without a role split (Gemini) have no per-turn
// history, so their whole body counts as one message.
func ExtractJevStateInput(rawJSON []byte, format string) JevStateInput {
	ext := extractRequest(rawJSON, format)
	return JevStateInput{
		LatestUserText:      ext.LatestUserText,
		MessageCount:        countRequestMessages(rawJSON, format),
		HistoryWordEstimate: ext.HistoryTokens,
		HasTools:            hasToolDeclarations(rawJSON),
		HasCodeFence:        ext.CodeFenceTokens > 0,
		HasImages:           hasImageParts(rawJSON),
	}
}

// countRequestMessages counts the top-level message-bearing arrays. The three
// shapes are mutually exclusive across formats (messages for OpenAI/Claude,
// input for Responses, contents for Gemini), so the sum is the message count.
func countRequestMessages(rawJSON []byte, format string) int {
	root := gjson.ParseBytes(rawJSON)
	switch strings.ToLower(strings.TrimSpace(format)) {
	case constant.Gemini, constant.GeminiInteractions, constant.Interactions:
		return len(root.Get("contents").Array())
	default:
		return len(root.Get("messages").Array()) + len(root.Get("input").Array())
	}
}

// hasToolDeclarations reports whether the request declares tools/functions,
// which is a strong signal of an agentic multi-step turn.
func hasToolDeclarations(rawJSON []byte) bool {
	root := gjson.ParseBytes(rawJSON)
	for _, path := range []string{"tools", "functions", "functions_declarations"} {
		if root.Get(path).Exists() {
			return true
		}
	}
	return false
}

// hasImageParts reports whether the request carries an image. Content-part
// type matching covers OpenAI, Claude, and Responses; Gemini's inline_data
// (and the camelCase inlineData spelling) is matched structurally instead.
func hasImageParts(rawJSON []byte) bool {
	root := gjson.ParseBytes(rawJSON)
	for _, path := range []string{"messages", "input"} {
		for _, msg := range root.Get(path).Array() {
			for _, part := range msg.Get("content").Array() {
				if jevImagePartTypes[part.Get("type").String()] {
					return true
				}
			}
		}
	}
	for _, content := range root.Get("contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if part.Get("inline_data").Exists() || part.Get("inlineData").Exists() {
				return true
			}
		}
	}
	return false
}
