package common

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// ClaudeMessagesJSONToSSE adapts a native Messages response for the existing SSE
// aggregators and returns its model. SSE and other payloads pass through unchanged.
func ClaudeMessagesJSONToSSE(rawJSON []byte) ([]byte, string) {
	if !ojson.ValidBytes(rawJSON) {
		return rawJSON, ""
	}
	root := util.ParseGJSONBytesNoCopy(rawJSON)
	if root.Get("type").String() != "message" || !root.Get("content").IsArray() {
		return rawJSON, ""
	}

	var out bytes.Buffer
	emit := func(event map[string]any) {
		// All values are JSON primitives or validated raw JSON from the response.
		payload, _ := json.Marshal(event)
		out.WriteString("data: ")
		out.Write(payload)
		out.WriteString("\n\n")
	}
	messageDoc := ojson.NewDoc(rawJSON)
	_ = messageDoc.SetRawBytes("content", []byte(`[]`))
	_ = messageDoc.Set("stop_reason", nil)
	_ = messageDoc.Set("stop_sequence", nil)
	emit(map[string]any{"type": "message_start", "message": json.RawMessage(messageDoc.Bytes())})

	for index, block := range root.Get("content").Array() {
		start := []byte(block.Raw)
		var delta map[string]any
		switch block.Get("type").String() {
		case "text":
			start, _ = ojson.SetBytes(start, "text", "")
			delta = map[string]any{"type": "text_delta", "text": block.Get("text").String()}
		case "tool_use":
			start, _ = ojson.SetRawBytes(start, "input", []byte(`{}`))
			input := block.Get("input").Raw
			if input == "" {
				input = "{}"
			}
			delta = map[string]any{"type": "input_json_delta", "partial_json": input}
		case "thinking":
			startDoc := ojson.NewDoc(start)
			_ = startDoc.Set("thinking", "")
			_ = startDoc.Set("signature", "")
			start = startDoc.Bytes()
			delta = map[string]any{"type": "thinking_delta", "thinking": block.Get("thinking").String()}
		}
		emit(map[string]any{"type": "content_block_start", "index": index, "content_block": json.RawMessage(start)})
		if delta != nil {
			emit(map[string]any{"type": "content_block_delta", "index": index, "delta": delta})
		}
		if block.Get("type").String() == "text" {
			for _, citation := range block.Get("citations").Array() {
				emit(map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{
					"type": "citations_delta", "citation": json.RawMessage(citation.Raw),
				}})
			}
		}
		if block.Get("type").String() == "thinking" && block.Get("signature").Exists() {
			emit(map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{
				"type": "signature_delta", "signature": block.Get("signature").String(),
			}})
		}
		emit(map[string]any{"type": "content_block_stop", "index": index})
	}
	usage := json.RawMessage(root.Get("usage").Raw)
	if len(usage) == 0 {
		usage = json.RawMessage(`{}`)
	}
	emit(map[string]any{"type": "message_delta", "delta": map[string]any{
		"stop_reason": root.Get("stop_reason").Value(), "stop_sequence": root.Get("stop_sequence").Value(),
	}, "usage": usage})
	emit(map[string]any{"type": "message_stop"})
	return out.Bytes(), strings.Clone(root.Get("model").String())
}
