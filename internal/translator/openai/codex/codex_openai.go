// Package codex provides utilities to translate Codex Responses API request/response
// into OpenAI Chat Completions API format using gjson/sjson.
// It supports tools, multimodal inputs, and streaming/non-streaming responses.
// This enables Codex clients to communicate with providers that only support
// OpenAI Chat Completions API (e.g., Kimi Coding).
package codex

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var dataTag = []byte("data:")

// ConvertCodexRequestToOpenAI converts a Codex Responses API request JSON
// into an OpenAI Chat Completions API request JSON.
//
// Field mappings:
//   - input → messages
//   - input[].role → messages[].role
//   - input[].content → messages[].content
//   - tools → tools (with function name normalization: '.' → '_')
//   - max_tokens → max_tokens
//   - stream → stream
//   - temperature → temperature
//   - top_p → top_p
//   - model → model
func ConvertCodexRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON

	// Start with empty JSON object
	out := []byte(`{}`)

	// Set model
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Set stream
	out, _ = sjson.SetBytes(out, "stream", stream)

	// Map input → messages
	inputResult := gjson.GetBytes(rawJSON, "input")
	if inputResult.IsArray() {
		out, _ = sjson.SetRawBytes(out, "messages", []byte(`[]`))
		inputArray := inputResult.Array()
		for i := 0; i < len(inputArray); i++ {
			item := inputArray[i]
			itemType := item.Get("type").String()

			// Infer type if not explicitly set
			if itemType == "" {
				if item.Get("role").Exists() {
					itemType = "message"
				} else if item.Get("call_id").Exists() && item.Get("output").Exists() {
					itemType = "function_call_output"
				} else if item.Get("call_id").Exists() && item.Get("name").Exists() {
					itemType = "function_call"
				}
			}

			switch itemType {
			case "message":
				msg := []byte(`{}`)
				role := item.Get("role").String()
				if role == "developer" {
					role = "system"
				}
				msg, _ = sjson.SetBytes(msg, "role", role)

				// Handle content
				content := item.Get("content")
				if content.IsArray() {
					// Check if all items are simple text
					allText := true
					items := content.Array()
					for _, c := range items {
						if c.Get("type").String() != "input_text" && c.Get("type").String() != "output_text" {
							allText = false
							break
						}
					}

					if allText && len(items) > 0 {
						// Simple text content: concatenate
						var texts []string
						for _, c := range items {
							texts = append(texts, c.Get("text").String())
						}
						msg, _ = sjson.SetBytes(msg, "content", strings.Join(texts, ""))
					} else {
						// Complex content: map to Chat Completions format
						msg, _ = sjson.SetRawBytes(msg, "content", []byte(`[]`))
						for _, c := range items {
							cType := c.Get("type").String()
							switch cType {
							case "input_text", "output_text":
								part := []byte(`{"type":"text","text":""}`)
								part, _ = sjson.SetBytes(part, "text", c.Get("text").String())
								msg, _ = sjson.SetRawBytes(msg, "content.-1", part)
							case "input_image":
								part := []byte(`{"type":"image_url","image_url":{"url":""}}`)
								if url := c.Get("image_url").String(); url != "" {
									part, _ = sjson.SetBytes(part, "image_url.url", url)
								} else if b64 := c.Get("image_bytes").String(); b64 != "" {
									// Assume base64 encoded image
									part, _ = sjson.SetBytes(part, "image_url.url", "data:image/png;base64,"+b64)
								}
								msg, _ = sjson.SetRawBytes(msg, "content.-1", part)
							case "input_file":
								part := []byte(`{"type":"file","file":{"file_data":"","filename":""}}`)
								part, _ = sjson.SetBytes(part, "file.file_data", c.Get("file_data").String())
								part, _ = sjson.SetBytes(part, "file.filename", c.Get("filename").String())
								msg, _ = sjson.SetRawBytes(msg, "content.-1", part)
							}
						}
					}
				} else if content.Type == gjson.String {
					msg, _ = sjson.SetBytes(msg, "content", content.String())
				}

				out, _ = sjson.SetRawBytes(out, "messages.-1", msg)

			case "function_call":
				// Map function_call to assistant message with tool_calls
				msg := []byte(`{"role":"assistant","content":null}`)
				toolCall := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
				toolCall, _ = sjson.SetBytes(toolCall, "id", item.Get("call_id").String())
				name := item.Get("name").String()
				toolCall, _ = sjson.SetBytes(toolCall, "function.name", name)
				toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", item.Get("arguments").String())
				msg, _ = sjson.SetRawBytes(msg, "tool_calls", []byte(`[]`))
				msg, _ = sjson.SetRawBytes(msg, "tool_calls.-1", toolCall)
				out, _ = sjson.SetRawBytes(out, "messages.-1", msg)

			case "function_call_output":
				// Map function_call_output to tool message
				msg := []byte(`{"role":"tool","content":"","tool_call_id":""}`)
				msg, _ = sjson.SetBytes(msg, "tool_call_id", item.Get("call_id").String())
				msg, _ = sjson.SetBytes(msg, "content", item.Get("output").String())
				out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
			}
		}
	}

	// Map tools (normalize function names: replace '.' with '_')
	tools := gjson.GetBytes(rawJSON, "tools")
	if tools.IsArray() && len(tools.Array()) > 0 {
		out, _ = sjson.SetRawBytes(out, "tools", []byte(`[]`))
		arr := tools.Array()
		for i := 0; i < len(arr); i++ {
			t := arr[i]
			toolType := t.Get("type").String()

			if toolType != "" && toolType != "function" && t.IsObject() {
				// Built-in tools (e.g., web_search) pass through
				out, _ = sjson.SetRawBytes(out, "tools.-1", []byte(t.Raw))
				continue
			}

			if toolType == "function" || toolType == "" {
				item := []byte(`{"type":"function","function":{"name":"","description":"","parameters":{}}}`)
				name := t.Get("name").String()
				// Normalize: replace '.' with '_'
				name = normalizeFunctionName(name)
				item, _ = sjson.SetBytes(item, "function.name", name)
				if v := t.Get("description"); v.Exists() {
					item, _ = sjson.SetBytes(item, "function.description", v.String())
				}
				if v := t.Get("parameters"); v.Exists() {
					item, _ = sjson.SetRawBytes(item, "function.parameters", []byte(v.Raw))
				}
				if v := t.Get("strict"); v.Exists() {
					item, _ = sjson.SetBytes(item, "function.strict", v.Value())
				}
				out, _ = sjson.SetRawBytes(out, "tools.-1", item)
			}
		}
	}

	// Map tool_choice
	if tc := gjson.GetBytes(rawJSON, "tool_choice"); tc.Exists() {
		switch {
		case tc.Type == gjson.String:
			out, _ = sjson.SetBytes(out, "tool_choice", tc.String())
		case tc.IsObject():
			tcType := tc.Get("type").String()
			if tcType == "function" {
				name := tc.Get("name").String()
				name = normalizeFunctionName(name)
				choice := []byte(`{"type":"function","function":{"name":""}}`)
				choice, _ = sjson.SetBytes(choice, "function.name", name)
				out, _ = sjson.SetRawBytes(out, "tool_choice", choice)
			} else if tcType != "" {
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(tc.Raw))
			}
		}
	}

	// Map other parameters
	if v := gjson.GetBytes(rawJSON, "max_tokens"); v.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", v.Value())
	} else if v := gjson.GetBytes(rawJSON, "max_output_tokens"); v.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", v.Value())
	}

	if v := gjson.GetBytes(rawJSON, "temperature"); v.Exists() {
		out, _ = sjson.SetBytes(out, "temperature", v.Value())
	}

	if v := gjson.GetBytes(rawJSON, "top_p"); v.Exists() {
		out, _ = sjson.SetBytes(out, "top_p", v.Value())
	}

	if v := gjson.GetBytes(rawJSON, "presence_penalty"); v.Exists() {
		out, _ = sjson.SetBytes(out, "presence_penalty", v.Value())
	}

	if v := gjson.GetBytes(rawJSON, "frequency_penalty"); v.Exists() {
		out, _ = sjson.SetBytes(out, "frequency_penalty", v.Value())
	}

	if v := gjson.GetBytes(rawJSON, "stop"); v.Exists() {
		out, _ = sjson.SetBytes(out, "stop", v.Value())
	}

	if v := gjson.GetBytes(rawJSON, "seed"); v.Exists() {
		out, _ = sjson.SetBytes(out, "seed", v.Value())
	}

	if v := gjson.GetBytes(rawJSON, "response_format"); v.Exists() {
		out, _ = sjson.SetRawBytes(out, "response_format", []byte(v.Raw))
	}

	return out
}

// normalizeFunctionName replaces '.' with '_' in function names to ensure
// compatibility with providers that don't support dots in function names.
func normalizeFunctionName(name string) string {
	return strings.ReplaceAll(name, ".", "_")
}

// denormalizeFunctionName restores original function names by replacing '_'
// with '.' only when the original request contained dots.
func denormalizeFunctionName(name string, originalHasDots bool) string {
	if !originalHasDots {
		return name
	}
	return strings.ReplaceAll(name, "_", ".")
}

// originalToolsHaveDots checks if any original tool names contained dots.
func originalToolsHaveDots(originalRequest []byte) bool {
	tools := gjson.GetBytes(originalRequest, "tools")
	if !tools.IsArray() {
		return false
	}
	for _, t := range tools.Array() {
		name := t.Get("name").String()
		if strings.Contains(name, ".") {
			return true
		}
	}
	return false
}

// buildOriginalToolNameMap builds a map from normalized names back to original names.
func buildOriginalToolNameMap(originalRequest []byte) map[string]string {
	m := make(map[string]string)
	tools := gjson.GetBytes(originalRequest, "tools")
	if !tools.IsArray() {
		return m
	}
	for _, t := range tools.Array() {
		origName := t.Get("name").String()
		normName := normalizeFunctionName(origName)
		if origName != normName {
			m[normName] = origName
		}
	}
	return m
}

// ConvertOpenAIResponseToCodex translates a single OpenAI Chat Completions streaming
// chunk into Codex Responses API streaming format.
func ConvertOpenAIResponseToCodex(_ context.Context, modelName string, originalRequestRawJSON, _, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &map[string]any{
			"responseID":        "",
			"createdAt":         0,
			"model":             modelName,
			"functionCallIndex": -1,
			"toolCallsMap":      make(map[string]any),
		}
	}

	p := (*param).(map[string]any)

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	rootResult := gjson.ParseBytes(rawJSON)

	// Extract response ID and created time from first chunk
	if id := rootResult.Get("id"); id.Exists() && p["responseID"].(string) == "" {
		p["responseID"] = id.String()
		p["createdAt"] = rootResult.Get("created").Int()
		p["model"] = rootResult.Get("model").String()

		// Emit response.created event
		event := []byte(`{"type":"response.created","response":{"id":"","created_at":0,"model":""}}`)
		event, _ = sjson.SetBytes(event, "response.id", p["responseID"])
		event, _ = sjson.SetBytes(event, "response.created_at", p["createdAt"])
		event, _ = sjson.SetBytes(event, "response.model", p["model"])
		return [][]byte{event}
	}

	choice := rootResult.Get("choices.0")
	if !choice.Exists() {
		return [][]byte{}
	}

	var events [][]byte
	content := choice.Get("delta.content").String()
	finishReason := choice.Get("finish_reason").String()

	// Handle text content
	if content != "" {
		event := []byte(`{"type":"response.output_text.delta","delta":""}`)
		event, _ = sjson.SetBytes(event, "delta", content)
		events = append(events, event)
	}

	// Handle tool calls
	toolCalls := choice.Get("delta.tool_calls")
	if toolCalls.IsArray() {
		for _, tc := range toolCalls.Array() {
			index := tc.Get("index").Int()
			id := tc.Get("id").String()
			name := tc.Get("function.name").String()
			args := tc.Get("function.arguments").String()

			toolCallsMap := p["toolCallsMap"].(map[string]any)
			key := fmt.Sprintf("%d", index)

			if id != "" && name != "" {
				// New tool call
				p["functionCallIndex"] = index
				toolCallsMap[key] = map[string]string{
					"id":   id,
					"name": name,
				}

				// Restore original function name if it was normalized
				origMap := buildOriginalToolNameMap(originalRequestRawJSON)
				if orig, ok := origMap[name]; ok {
					name = orig
				}

				event := []byte(`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"","name":"","arguments":""}}`)
				event, _ = sjson.SetBytes(event, "item.call_id", id)
				event, _ = sjson.SetBytes(event, "item.name", name)
				event, _ = sjson.SetBytes(event, "item.arguments", "")
				events = append(events, event)
			}

			if args != "" {
				// Arguments delta
				event := []byte(`{"type":"response.function_call_arguments.delta","delta":""}`)
				event, _ = sjson.SetBytes(event, "delta", args)
				if id != "" {
					event, _ = sjson.SetBytes(event, "call_id", id)
				} else if tcMap, ok := toolCallsMap[key].(map[string]string); ok {
					event, _ = sjson.SetBytes(event, "call_id", tcMap["id"])
				}
				events = append(events, event)
			}
		}
	}

	// Handle finish reason
	if finishReason != "" {
		if finishReason == "tool_calls" {
			// Complete any pending tool calls
			toolCallsMap := p["toolCallsMap"].(map[string]any)
			for _, v := range toolCallsMap {
				if tcMap, ok := v.(map[string]string); ok {
					event := []byte(`{"type":"response.function_call_arguments.done","call_id":"","arguments":""}`)
					event, _ = sjson.SetBytes(event, "call_id", tcMap["id"])
					if args, ok := tcMap["args"]; ok {
						event, _ = sjson.SetBytes(event, "arguments", args)
					}
					events = append(events, event)
				}
			}
		}

		event := []byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`)
		events = append(events, event)
	}

	// Handle usage
	if usage := rootResult.Get("usage"); usage.Exists() {
		event := []byte(`{"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[]},"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`)
		if v := usage.Get("prompt_tokens"); v.Exists() {
			event, _ = sjson.SetBytes(event, "usage.input_tokens", v.Int())
		}
		if v := usage.Get("completion_tokens"); v.Exists() {
			event, _ = sjson.SetBytes(event, "usage.output_tokens", v.Int())
		}
		if v := usage.Get("total_tokens"); v.Exists() {
			event, _ = sjson.SetBytes(event, "usage.total_tokens", v.Int())
		}
		events = append(events, event)
	}

	return events
}

// ConvertOpenAIResponseToCodexNonStream builds a single Codex Responses JSON
// from a non-streaming OpenAI Chat Completions response.
func ConvertOpenAIResponseToCodexNonStream(_ context.Context, modelName string, originalRequestRawJSON, _, rawJSON []byte, _ *any) []byte {
	rootResult := gjson.ParseBytes(rawJSON)

	out := []byte(`{"id":"","object":"response","created_at":0,"model":"","status":"completed","output":[]}`)

	// Set basic fields
	out, _ = sjson.SetBytes(out, "id", "resp_"+rootResult.Get("id").String())
	out, _ = sjson.SetBytes(out, "created_at", rootResult.Get("created").Int())
	if m := rootResult.Get("model").String(); m != "" {
		out, _ = sjson.SetBytes(out, "model", m)
	} else {
		out, _ = sjson.SetBytes(out, "model", modelName)
	}

	// Map choices to output
	choices := rootResult.Get("choices")
	if choices.IsArray() {
		choice := choices.Array()[0]
		message := choice.Get("message")

		// Map content
		content := message.Get("content").String()
		if content != "" {
			msg := []byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":""}]}`)
			msg, _ = sjson.SetBytes(msg, "content.0.text", content)
			out, _ = sjson.SetRawBytes(out, "output.-1", msg)
		}

		// Map tool calls
		toolCalls := message.Get("tool_calls")
		if toolCalls.IsArray() {
			for _, tc := range toolCalls.Array() {
				name := tc.Get("function.name").String()

				// Restore original function name if it was normalized
				origMap := buildOriginalToolNameMap(originalRequestRawJSON)
				if orig, ok := origMap[name]; ok {
					name = orig
				}

				fc := []byte(`{"type":"function_call","call_id":"","name":"","arguments":""}`)
				fc, _ = sjson.SetBytes(fc, "call_id", tc.Get("id").String())
				fc, _ = sjson.SetBytes(fc, "name", name)
				fc, _ = sjson.SetBytes(fc, "arguments", tc.Get("function.arguments").String())
				out, _ = sjson.SetRawBytes(out, "output.-1", fc)
			}
		}
	}

	// Map usage
	usage := rootResult.Get("usage")
	if usage.Exists() {
		outUsage := []byte(`{"input_tokens":0,"output_tokens":0,"total_tokens":0}`)
		if v := usage.Get("prompt_tokens"); v.Exists() {
			outUsage, _ = sjson.SetBytes(outUsage, "input_tokens", v.Int())
		}
		if v := usage.Get("completion_tokens"); v.Exists() {
			outUsage, _ = sjson.SetBytes(outUsage, "output_tokens", v.Int())
		}
		if v := usage.Get("total_tokens"); v.Exists() {
			outUsage, _ = sjson.SetBytes(outUsage, "total_tokens", v.Int())
		}
		out, _ = sjson.SetRawBytes(out, "usage", outUsage)
	}

	return out
}
