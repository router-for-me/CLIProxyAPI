// Package to_ir converts provider-specific API formats into unified format.
// This file handles Ollama API requests and responses.
package to_ir

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/ir"
)

// =============================================================================
// Request Parsing (Client → Unified)
// =============================================================================

// ParseOllamaRequest parses incoming Ollama API request into unified format.
// Supports both /api/chat and /api/generate endpoints.
func ParseOllamaRequest(rawJSON []byte) (*ir.UnifiedChatRequest, error) {
	if !gjson.ValidBytes(rawJSON) {
		return nil, &json.UnmarshalTypeError{Value: "invalid json"}
	}

	root := gjson.ParseBytes(rawJSON)
	req := &ir.UnifiedChatRequest{
		Model:    root.Get("model").String(),
		Metadata: make(map[string]any),
	}

	// Parse options (temperature, top_p, etc.)
	parseOllamaOptions(root.Get("options"), req)

	// Determine endpoint type and parse messages
	if msgs := root.Get("messages"); msgs.Exists() && msgs.IsArray() {
		// /api/chat endpoint
		req.Messages = parseOllamaMessages(msgs.Array())
		req.Metadata["ollama_endpoint"] = "chat"
	} else if prompt := root.Get("prompt"); prompt.Exists() {
		// /api/generate endpoint
		req.Messages = []ir.Message{createOllamaUserMessage(prompt.String(), root.Get("images"))}
		req.Metadata["ollama_endpoint"] = "generate"
	}

	// System prompt (override or prepend)
	if sys := root.Get("system"); sys.Exists() && sys.String() != "" {
		req.Messages = append([]ir.Message{{
			Role:    ir.RoleSystem,
			Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: sys.String()}},
		}}, req.Messages...)
	}

	// Tools
	if tools := root.Get("tools"); tools.IsArray() {
		for _, t := range tools.Array() {
			if tool := parseOllamaTool(t); tool != nil {
				req.Tools = append(req.Tools, *tool)
			}
		}
	}

	// Metadata fields
	for _, key := range []string{"format", "keep_alive"} {
		if val := root.Get(key); val.Exists() {
			req.Metadata["ollama_"+key] = val.String()
		}
	}
	if stream := root.Get("stream"); stream.Exists() {
		req.Metadata["stream"] = stream.Bool()
	}

	return req, nil
}

// =============================================================================
// Response Parsing (Ollama API → Unified)
// =============================================================================

// ParseOllamaResponse parses non-streaming Ollama API response.
// Supports both /api/chat and /api/generate response formats.
func ParseOllamaResponse(rawJSON []byte) ([]ir.Message, *ir.Usage, error) {
	if !gjson.ValidBytes(rawJSON) {
		return nil, nil, &json.UnmarshalTypeError{Value: "invalid json"}
	}

	root := gjson.ParseBytes(rawJSON)
	usage := parseOllamaUsage(root)
	msg := ir.Message{Role: ir.RoleAssistant}

	// Extract content (supports both chat 'message' and generate 'response')
	if message := root.Get("message"); message.Exists() {
		// /api/chat
		parseOllamaContent(message, &msg)
		msg.ToolCalls = ir.ParseOpenAIStyleToolCalls(message.Get("tool_calls").Array())
	} else {
		// /api/generate (root level fields)
		parseOllamaContent(root, &msg)
	}

	if len(msg.Content) == 0 && len(msg.ToolCalls) == 0 {
		return nil, usage, nil
	}
	return []ir.Message{msg}, usage, nil
}

// ParseOllamaChunk parses streaming Ollama API chunk into events.
// Handles both /api/chat and /api/generate streaming formats.
func ParseOllamaChunk(rawJSON []byte) ([]ir.UnifiedEvent, error) {
	// Ollama uses newline-delimited JSON (not SSE)
	rawJSON = []byte(strings.TrimSpace(string(rawJSON)))
	if len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return nil, nil
	}

	root := gjson.ParseBytes(rawJSON)
	var events []ir.UnifiedEvent

	// Determine content source
	source := root
	if msg := root.Get("message"); msg.Exists() {
		source = msg // /api/chat
	}

	// 1. Reasoning/Thinking
	if thinking := source.Get("thinking"); thinking.Exists() && thinking.String() != "" {
		events = append(events, ir.UnifiedEvent{Type: ir.EventTypeReasoning, Reasoning: thinking.String()})
	}

	// 2. Content (Text)
	// /api/chat uses 'content', /api/generate uses 'response'
	content := source.Get("content")
	if !content.Exists() {
		content = root.Get("response")
	}
	if content.Exists() && content.String() != "" {
		events = append(events, ir.UnifiedEvent{Type: ir.EventTypeToken, Content: content.String()})
	}

	// 3. Tool Calls (only in /api/chat usually)
	if tcs := source.Get("tool_calls"); tcs.IsArray() {
		for _, tc := range tcs.Array() {
			if tc.Get("type").String() == "function" {
				events = append(events, ir.UnifiedEvent{
					Type: ir.EventTypeToolCall,
					ToolCall: &ir.ToolCall{
						ID:   tc.Get("id").String(),
						Name: tc.Get("function.name").String(),
						Args: tc.Get("function.arguments").String(),
					},
				})
			}
		}
	}

	// 4. Finish / Done
	if root.Get("done").Bool() {
		events = append(events, ir.UnifiedEvent{
			Type:         ir.EventTypeFinish,
			FinishReason: mapOllamaDoneReason(root.Get("done_reason").String()),
			Usage:        parseOllamaUsage(root),
		})
	}

	return events, nil
}

// =============================================================================
// Helper Functions
// =============================================================================

func parseOllamaUsage(root gjson.Result) *ir.Usage {
	p := root.Get("prompt_eval_count").Int()
	c := root.Get("eval_count").Int()
	if p == 0 && c == 0 {
		return nil
	}
	return &ir.Usage{
		PromptTokens:     int(p),
		CompletionTokens: int(c),
		TotalTokens:      int(p + c),
	}
}

func parseOllamaContent(source gjson.Result, msg *ir.Message) {
	if thinking := source.Get("thinking"); thinking.Exists() && thinking.String() != "" {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeReasoning, Reasoning: thinking.String()})
	}
	// 'content' for chat, 'response' for generate
	text := source.Get("content").String()
	if text == "" {
		text = source.Get("response").String()
	}
	if text != "" {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: text})
	}
}

func parseOllamaOptions(opts gjson.Result, req *ir.UnifiedChatRequest) {
	if !opts.Exists() {
		return
	}
	if v := opts.Get("temperature"); v.Exists() {
		f := v.Float()
		req.Temperature = &f
	}
	if v := opts.Get("top_p"); v.Exists() {
		f := v.Float()
		req.TopP = &f
	}
	if v := opts.Get("top_k"); v.Exists() {
		i := int(v.Int())
		req.TopK = &i
	}
	if v := opts.Get("num_predict"); v.Exists() {
		i := int(v.Int())
		req.MaxTokens = &i
	}
	if v := opts.Get("stop"); v.Exists() {
		if v.IsArray() {
			for _, s := range v.Array() {
				req.StopSequences = append(req.StopSequences, s.String())
			}
		} else {
			req.StopSequences = append(req.StopSequences, v.String())
		}
	}
	// Metadata options
	if v := opts.Get("seed"); v.Exists() {
		req.Metadata["ollama_seed"] = v.Int()
	}
	if v := opts.Get("num_ctx"); v.Exists() {
		req.Metadata["ollama_num_ctx"] = v.Int()
	}
}

func parseOllamaMessages(msgs []gjson.Result) []ir.Message {
	var res []ir.Message
	for _, m := range msgs {
		msg := ir.Message{Role: ir.MapStandardRole(m.Get("role").String())}

		// Handle content and images
		content := m.Get("content").String()
		images := m.Get("images")

		if images.IsArray() && len(images.Array()) > 0 {
			if content != "" {
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: content})
			}
			for _, img := range images.Array() {
				if part := parseOllamaImage(img.String()); part != nil {
					msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeImage, Image: part})
				}
			}
		} else if content != "" {
			msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: content})
		}

		// Tool calls
		if msg.Role == ir.RoleAssistant {
			msg.ToolCalls = ir.ParseOpenAIStyleToolCalls(m.Get("tool_calls").Array())
		}
		// Tool results
		if msg.Role == ir.RoleTool {
			id := m.Get("tool_call_id").String()
			if id == "" {
				id = m.Get("tool_name").String()
			} // Fallback
			if id != "" {
				msg.Content = append(msg.Content, ir.ContentPart{
					Type: ir.ContentTypeToolResult,
					ToolResult: &ir.ToolResultPart{ToolCallID: id, Result: ir.SanitizeText(content)},
				})
			}
		}

		if len(msg.Content) > 0 || len(msg.ToolCalls) > 0 {
			res = append(res, msg)
		}
	}
	return res
}

func createOllamaUserMessage(prompt string, images gjson.Result) ir.Message {
	msg := ir.Message{Role: ir.RoleUser, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: prompt}}}
	if images.IsArray() {
		for _, img := range images.Array() {
			if part := parseOllamaImage(img.String()); part != nil {
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeImage, Image: part})
			}
		}
	}
	return msg
}

func parseOllamaTool(t gjson.Result) *ir.ToolDefinition {
	if t.Get("type").String() != "function" {
		return nil
	}
	fn := t.Get("function")
	name := fn.Get("name").String()
	if name == "" {
		return nil
	}

	var params map[string]interface{}
	if p := fn.Get("parameters"); p.Exists() {
		json.Unmarshal([]byte(p.Raw), &params)
		params = ir.CleanJsonSchema(params)
	}
	if params == nil {
		params = make(map[string]interface{})
	}

	return &ir.ToolDefinition{
		Name:        name,
		Description: fn.Get("description").String(),
		Parameters:  params,
	}
}

func parseOllamaImage(data string) *ir.ImagePart {
	if data == "" {
		return nil
	}
	if !strings.HasPrefix(data, "data:") {
		data = "data:image/png;base64," + data
	}
	parts := strings.SplitN(data, ",", 2)
	if len(parts) != 2 {
		return nil
	}

	mime := "image/png"
	if idx := strings.Index(parts[0], ";"); idx > 5 {
		mime = parts[0][5:idx]
	}
	return &ir.ImagePart{MimeType: mime, Data: parts[1]}
}

func mapOllamaDoneReason(r string) ir.FinishReason {
	switch r {
	case "stop":
		return ir.FinishReasonStop
	case "length":
		return ir.FinishReasonLength
	case "tool_calls":
		return ir.FinishReasonToolCalls
	default:
		return ir.FinishReasonStop
	}
}
