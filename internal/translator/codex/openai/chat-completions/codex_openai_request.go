// Package openai provides utilities to translate OpenAI Chat Completions
// request JSON into OpenAI Responses API request JSON using gjson/sjson.
// It supports tools, multimodal text/image inputs, and Structured Outputs.
// The package handles the conversion of OpenAI API requests into the format
// expected by the OpenAI Responses API, including proper mapping of messages,
// tools, and generation parameters.
package chat_completions

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertOpenAIRequestToCodex converts an OpenAI Chat Completions request JSON
// into an OpenAI Responses API request JSON. The transformation follows the
// examples defined in docs/2.md exactly, including tools, multi-turn dialog,
// multimodal text/image handling, and Structured Outputs mapping.
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI Chat Completions API
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in OpenAI Responses API format
func ConvertOpenAIRequestToCodex(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	// Start with empty JSON object
	out := []byte(`{"instructions":""}`)

	// Stream must be set to true
	out, _ = sjson.SetBytes(out, "stream", stream)

	// Codex not support temperature, top_p, top_k, max_output_tokens, so comment them
	// if v := gjson.GetBytes(rawJSON, "temperature"); v.Exists() {
	// 	out, _ = sjson.SetBytes(out, "temperature", v.Value())
	// }
	// if v := gjson.GetBytes(rawJSON, "top_p"); v.Exists() {
	// 	out, _ = sjson.SetBytes(out, "top_p", v.Value())
	// }
	// if v := gjson.GetBytes(rawJSON, "top_k"); v.Exists() {
	// 	out, _ = sjson.SetBytes(out, "top_k", v.Value())
	// }

	// Map token limits
	// if v := gjson.GetBytes(rawJSON, "max_tokens"); v.Exists() {
	// 	out, _ = sjson.SetBytes(out, "max_output_tokens", v.Value())
	// }
	// if v := gjson.GetBytes(rawJSON, "max_completion_tokens"); v.Exists() {
	// 	out, _ = sjson.SetBytes(out, "max_output_tokens", v.Value())
	// }

	// Map reasoning effort
	if v := gjson.GetBytes(rawJSON, "reasoning_effort"); v.Exists() {
		out, _ = sjson.SetBytes(out, "reasoning.effort", v.Value())
	} else {
		out, _ = sjson.SetBytes(out, "reasoning.effort", "medium")
	}
	if v := gjson.GetBytes(rawJSON, "service_tier"); v.Exists() && v.String() == "priority" {
		out, _ = sjson.SetBytes(out, "service_tier", "priority")
	}
	out, _ = sjson.SetBytes(out, "parallel_tool_calls", true)
	out, _ = sjson.SetBytes(out, "reasoning.summary", "auto")
	out, _ = sjson.SetBytes(out, "include", []string{"reasoning.encrypted_content"})

	// Model
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Build tool name shortening map from original tools (if any)
	originalToolNameMap := map[string]string{}
	{
		tools := gjson.GetBytes(rawJSON, "tools")
		if tools.IsArray() && len(tools.Array()) > 0 {
			// Collect original tool names
			var names []string
			arr := tools.Array()
			for i := 0; i < len(arr); i++ {
				t := arr[i]
				if t.Get("type").String() == "function" {
					fn := t.Get("function")
					if fn.Exists() {
						if v := fn.Get("name"); v.Exists() {
							names = append(names, v.String())
						}
					}
				}
			}
			if len(names) > 0 {
				originalToolNameMap = buildShortNameMap(names)
			}
		}
	}

	// Extract system instructions from first system message (string or text object)
	messages := gjson.GetBytes(rawJSON, "messages")
	// if messages.IsArray() {
	// 	arr := messages.Array()
	// 	for i := 0; i < len(arr); i++ {
	// 		m := arr[i]
	// 		if m.Get("role").String() == "system" {
	// 			c := m.Get("content")
	// 			if c.Type == gjson.String {
	// 				out, _ = sjson.SetBytes(out, "instructions", c.String())
	// 			} else if c.IsObject() && c.Get("type").String() == "text" {
	// 				out, _ = sjson.SetBytes(out, "instructions", c.Get("text").String())
	// 			}
	// 			break
	// 		}
	// 	}
	// }

	// Build input from messages, handling all message types including tool calls
	out, _ = sjson.SetRawBytes(out, "input", []byte(`[]`))
	if messages.IsArray() {
		arr := messages.Array()
		for i := 0; i < len(arr); i++ {
			m := arr[i]
			role := m.Get("role").String()

			switch role {
			case "tool":
				// Handle tool response messages as top-level function_call_output objects
				toolCallID := m.Get("tool_call_id").String()
				content := m.Get("content").String()

				// Create function_call_output object
				funcOutput := []byte(`{}`)
				funcOutput, _ = sjson.SetBytes(funcOutput, "type", "function_call_output")
				funcOutput, _ = sjson.SetBytes(funcOutput, "call_id", toolCallID)
				funcOutput, _ = sjson.SetBytes(funcOutput, "output", content)
				out, _ = sjson.SetRawBytes(out, "input.-1", funcOutput)

			default:
				// Handle regular messages
				msg := []byte(`{}`)
				msg, _ = sjson.SetBytes(msg, "type", "message")
				if role == "system" {
					msg, _ = sjson.SetBytes(msg, "role", "developer")
				} else {
					msg, _ = sjson.SetBytes(msg, "role", role)
				}

				msg, _ = sjson.SetRawBytes(msg, "content", []byte(`[]`))

				// Handle regular content
				c := m.Get("content")
				if c.Exists() && c.Type == gjson.String && c.String() != "" {
					// Single string content
					partType := "input_text"
					if role == "assistant" {
						partType = "output_text"
					}
					part := []byte(`{}`)
					part, _ = sjson.SetBytes(part, "type", partType)
					part, _ = sjson.SetBytes(part, "text", c.String())
					msg, _ = sjson.SetRawBytes(msg, "content.-1", part)
				} else if c.Exists() && c.IsArray() {
					items := c.Array()
					for j := 0; j < len(items); j++ {
						it := items[j]
						t := it.Get("type").String()
						switch t {
						case "text":
							partType := "input_text"
							if role == "assistant" {
								partType = "output_text"
							}
							part := []byte(`{}`)
							part, _ = sjson.SetBytes(part, "type", partType)
							part, _ = sjson.SetBytes(part, "text", it.Get("text").String())
							msg, _ = sjson.SetRawBytes(msg, "content.-1", part)
						case "image_url":
							// Map image inputs to input_image for Responses API
							if role == "user" {
								part := []byte(`{}`)
								part, _ = sjson.SetBytes(part, "type", "input_image")
								if u := it.Get("image_url.url"); u.Exists() {
									part, _ = sjson.SetBytes(part, "image_url", u.String())
								}
								msg, _ = sjson.SetRawBytes(msg, "content.-1", part)
							}
						case "file":
							if role == "user" {
								fileData := it.Get("file.file_data").String()
								filename := it.Get("file.filename").String()
								if fileData != "" {
									part := []byte(`{}`)
									part, _ = sjson.SetBytes(part, "type", "input_file")
									part, _ = sjson.SetBytes(part, "file_data", fileData)
									if filename != "" {
										part, _ = sjson.SetBytes(part, "filename", filename)
									}
									msg, _ = sjson.SetRawBytes(msg, "content.-1", part)
								}
							}
						}
					}
				}

				// Don't emit empty assistant messages when only tool_calls
				// are present — Responses API needs function_call items
				// directly, otherwise call_id matching fails (#2132).
				if role != "assistant" || len(gjson.GetBytes(msg, "content").Array()) > 0 {
					out, _ = sjson.SetRawBytes(out, "input.-1", msg)
				}

				// Handle tool calls for assistant messages as separate top-level objects
				if role == "assistant" {
					toolCalls := m.Get("tool_calls")
					if toolCalls.Exists() && toolCalls.IsArray() {
						toolCallsArr := toolCalls.Array()
						for j := 0; j < len(toolCallsArr); j++ {
							tc := toolCallsArr[j]
							if tc.Get("type").String() == "function" {
								// Create function_call as top-level object
								funcCall := []byte(`{}`)
								funcCall, _ = sjson.SetBytes(funcCall, "type", "function_call")
								funcCall, _ = sjson.SetBytes(funcCall, "call_id", tc.Get("id").String())
								{
									name := tc.Get("function.name").String()
									if short, ok := originalToolNameMap[name]; ok {
										name = short
									} else {
										name = shortenNameIfNeeded(name)
									}
									funcCall, _ = sjson.SetBytes(funcCall, "name", name)
								}
								funcCall, _ = sjson.SetBytes(funcCall, "arguments", tc.Get("function.arguments").String())
								out, _ = sjson.SetRawBytes(out, "input.-1", funcCall)
							}
						}
					}
				}
			}
		}
	}

	// Map response_format and text settings to Responses API text.format
	rf := gjson.GetBytes(rawJSON, "response_format")
	text := gjson.GetBytes(rawJSON, "text")
	if rf.Exists() {
		// Always create text object when response_format provided
		if !gjson.GetBytes(out, "text").Exists() {
			out, _ = sjson.SetRawBytes(out, "text", []byte(`{}`))
		}

		rft := rf.Get("type").String()
		switch rft {
		case "text":
			out, _ = sjson.SetBytes(out, "text.format.type", "text")
		case "json_schema":
			js := rf.Get("json_schema")
			if js.Exists() {
				out, _ = sjson.SetBytes(out, "text.format.type", "json_schema")
				if v := js.Get("name"); v.Exists() {
					out, _ = sjson.SetBytes(out, "text.format.name", v.Value())
				}
				if v := js.Get("strict"); v.Exists() {
					out, _ = sjson.SetBytes(out, "text.format.strict", v.Value())
				}
				if v := js.Get("schema"); v.Exists() {
					out, _ = sjson.SetRawBytes(out, "text.format.schema", []byte(v.Raw))
				}
			}
		}

		// Map verbosity if provided
		if text.Exists() {
			if v := text.Get("verbosity"); v.Exists() {
				out, _ = sjson.SetBytes(out, "text.verbosity", v.Value())
			}
		}
	} else if text.Exists() {
		// If only text.verbosity present (no response_format), map verbosity
		if v := text.Get("verbosity"); v.Exists() {
			if !gjson.GetBytes(out, "text").Exists() {
				out, _ = sjson.SetRawBytes(out, "text", []byte(`{}`))
			}
			out, _ = sjson.SetBytes(out, "text.verbosity", v.Value())
		}
	}

	// Map tools (flatten function fields)
	tools := gjson.GetBytes(rawJSON, "tools")
	if tools.IsArray() && len(tools.Array()) > 0 {
		out, _ = sjson.SetRawBytes(out, "tools", []byte(`[]`))
		arr := tools.Array()
		for i := 0; i < len(arr); i++ {
			t := arr[i]
			toolType := t.Get("type").String()
			// Pass through built-in tools (e.g. {"type":"web_search"}) directly for the Responses API.
			// Only "function" needs structural conversion because Chat Completions nests details under "function".
			if toolType != "" && toolType != "function" && t.IsObject() {
				out, _ = sjson.SetRawBytes(out, "tools.-1", []byte(t.Raw))
				continue
			}

			if toolType == "function" {
				item := []byte(`{}`)
				item, _ = sjson.SetBytes(item, "type", "function")
				fn := t.Get("function")
				if fn.Exists() {
					if v := fn.Get("name"); v.Exists() {
						name := v.String()
						if short, ok := originalToolNameMap[name]; ok {
							name = short
						} else {
							name = shortenNameIfNeeded(name)
						}
						item, _ = sjson.SetBytes(item, "name", name)
					}
					if v := fn.Get("description"); v.Exists() {
						item, _ = sjson.SetBytes(item, "description", v.Value())
					}
					if v := fn.Get("parameters"); v.Exists() {
						item, _ = sjson.SetRawBytes(item, "parameters", []byte(v.Raw))
					}
					if v := fn.Get("strict"); v.Exists() {
						item, _ = sjson.SetBytes(item, "strict", v.Value())
					}
				}
				out, _ = sjson.SetRawBytes(out, "tools.-1", item)
			}
		}
	}

	// Map tool_choice when present.
	// Chat Completions: "tool_choice" can be a string ("auto"/"none") or an object (e.g. {"type":"function","function":{"name":"..."}}).
	// Responses API: keep built-in tool choices as-is; flatten function choice to {"type":"function","name":"..."}.
	if tc := gjson.GetBytes(rawJSON, "tool_choice"); tc.Exists() {
		switch {
		case tc.Type == gjson.String:
			out, _ = sjson.SetBytes(out, "tool_choice", tc.String())
		case tc.IsObject():
			tcType := tc.Get("type").String()
			if tcType == "function" {
				name := tc.Get("function.name").String()
				if name != "" {
					if short, ok := originalToolNameMap[name]; ok {
						name = short
					} else {
						name = shortenNameIfNeeded(name)
					}
				}
				choice := []byte(`{}`)
				choice, _ = sjson.SetBytes(choice, "type", "function")
				if name != "" {
					choice, _ = sjson.SetBytes(choice, "name", name)
				}
				out, _ = sjson.SetRawBytes(out, "tool_choice", choice)
			} else if tcType != "" {
				// Built-in tool choices (e.g. {"type":"web_search"}) are already Responses-compatible.
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(tc.Raw))
			}
		}
	}

	out, _ = sjson.SetBytes(out, "store", false)
	return out
}

// shortenNameIfNeeded applies the simple shortening rule for a single name.
// If the name length exceeds 64, it will try to preserve the "mcp__" prefix and last segment.
// Otherwise it truncates to 64 characters.
func shortenNameIfNeeded(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	if strings.HasPrefix(name, "mcp__") {
		// Keep prefix and last segment after '__'
		idx := strings.LastIndex(name, "__")
		if idx > 0 {
			candidate := "mcp__" + name[idx+2:]
			if len(candidate) > limit {
				return candidate[:limit]
			}
			return candidate
		}
	}
	return name[:limit]
}

// buildShortNameMap generates unique short names (<=64) for the given list of names.
// It preserves the "mcp__" prefix with the last segment when possible and ensures uniqueness
// by appending suffixes like "~1", "~2" if needed.
func buildShortNameMap(names []string) map[string]string {
	const limit = 64
	used := map[string]struct{}{}
	m := map[string]string{}

	baseCandidate := func(n string) string {
		if len(n) <= limit {
			return n
		}
		if strings.HasPrefix(n, "mcp__") {
			idx := strings.LastIndex(n, "__")
			if idx > 0 {
				cand := "mcp__" + n[idx+2:]
				if len(cand) > limit {
					cand = cand[:limit]
				}
				return cand
			}
		}
		return n[:limit]
	}

	makeUnique := func(cand string) string {
		if _, ok := used[cand]; !ok {
			return cand
		}
		base := cand
		for i := 1; ; i++ {
			suffix := "_" + strconv.Itoa(i)
			allowed := limit - len(suffix)
			if allowed < 0 {
				allowed = 0
			}
			tmp := base
			if len(tmp) > allowed {
				tmp = tmp[:allowed]
			}
			tmp = tmp + suffix
			if _, ok := used[tmp]; !ok {
				return tmp
			}
		}
	}

	for _, n := range names {
		cand := baseCandidate(n)
		uniq := makeUnique(cand)
		used[uniq] = struct{}{}
		m[n] = uniq
	}
	return m
}
