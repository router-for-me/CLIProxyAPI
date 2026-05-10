package responses

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertOpenAIResponsesRequestToOpenAIChatCompletions converts OpenAI responses format to OpenAI chat completions format.
// It transforms the OpenAI responses API format (with instructions and input array) into the standard
// OpenAI chat completions format (with messages array and system content).
//
// The conversion handles:
// 1. Model name and streaming configuration
// 2. Instructions to system message conversion
// 3. Input array to messages array transformation
// 4. Tool definitions and tool choice conversion
// 5. Function calls and function results handling
// 6. Generation parameters mapping (max_tokens, reasoning, etc.)
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data in OpenAI responses format
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in OpenAI chat completions format
func ConvertOpenAIResponsesRequestToOpenAIChatCompletions(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	// Base OpenAI chat completions template with default values
	out := []byte(`{"model":"","messages":[],"stream":false}`)

	root := gjson.ParseBytes(rawJSON)

	// Set model name
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Set stream configuration
	out, _ = sjson.SetBytes(out, "stream", stream)

	// Map generation parameters from responses format to chat completions format
	if maxTokens := root.Get("max_output_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
	}

	if parallelToolCalls := root.Get("parallel_tool_calls"); parallelToolCalls.Exists() {
		out, _ = sjson.SetBytes(out, "parallel_tool_calls", parallelToolCalls.Bool())
	}

	// Convert instructions to system message
	if instructions := root.Get("instructions"); instructions.Exists() {
		systemMessage := []byte(`{"role":"system","content":""}`)
		systemMessage, _ = sjson.SetBytes(systemMessage, "content", instructions.String())
		out, _ = sjson.SetRawBytes(out, "messages.-1", systemMessage)
	}

	// Convert input array to messages
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		// Group-buffering approach for tool calls.
		//
		// In Responses API format, function_call and function_call_output items
		// can be interleaved with messages (e.g. developer approval messages
		// between a call and its result). Chat Completions is stricter:
		// an assistant message with tool_calls MUST be immediately followed by
		// the corresponding tool messages.
		//
		// We buffer the entire tool group and flush it in the correct order:
		//   1. One assistant message with all tool_calls
		//   2. All tool messages (one per function_call_output)
		//   3. Any messages that were interleaved between calls and results
		var pendingFunctionCalls []gjson.Result
		var bufferedMessages []gjson.Result
		var pendingToolOutputs []gjson.Result
		var pendingReasoningContent string

		flushToolGroup := func() {
			if len(pendingFunctionCalls) == 0 && len(pendingToolOutputs) == 0 {
				return
			}
			// 1. Emit one assistant message with all accumulated tool_calls (only if there are function calls to emit)
			assistantMessage := []byte(`{"role":"assistant","tool_calls":[]}`)
			for i, fc := range pendingFunctionCalls {
				toolCall := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
				if callId := fc.Get("call_id"); callId.Exists() {
					toolCall, _ = sjson.SetBytes(toolCall, "id", callId.String())
				}
				if name := fc.Get("name"); name.Exists() {
					toolCall, _ = sjson.SetBytes(toolCall, "function.name", name.String())
				}
				if arguments := fc.Get("arguments"); arguments.Exists() {
					toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", arguments.String())
				}
				assistantMessage, _ = sjson.SetRawBytes(assistantMessage, fmt.Sprintf("tool_calls.%d", i), toolCall)
			}
			out, _ = sjson.SetRawBytes(out, "messages.-1", assistantMessage)

			// 2. Emit tool messages for all collected function_call_output items (in order)
			for _, output := range pendingToolOutputs {
				toolMessage := []byte(`{"role":"tool","tool_call_id":"","content":""}`)
				if callId := output.Get("call_id"); callId.Exists() {
					toolMessage, _ = sjson.SetBytes(toolMessage, "tool_call_id", callId.String())
				}
				if outputVal := output.Get("output"); outputVal.Exists() {
					toolMessage, _ = sjson.SetBytes(toolMessage, "content", outputVal.String())
				}
				out, _ = sjson.SetRawBytes(out, "messages.-1", toolMessage)
			}

			// 3. Emit any messages that were interleaved between function_call
			//    and function_call_output (e.g. developer approval messages).
			for _, msg := range bufferedMessages {
				role := msg.Get("role").String()
				if role == "developer" {
					role = "user"
				}
				message := []byte(`{"role":"","content":[]}`)
				message, _ = sjson.SetBytes(message, "role", role)

				if role == "assistant" && pendingReasoningContent != "" {
					message, _ = sjson.SetBytes(message, "reasoning_content", pendingReasoningContent)
					pendingReasoningContent = ""
				}

				if content := msg.Get("content"); content.Exists() && content.IsArray() {
					content.ForEach(func(_, contentItem gjson.Result) bool {
						contentType := contentItem.Get("type").String()
						if contentType == "" {
							contentType = "input_text"
						}
						switch contentType {
						case "input_text", "output_text":
							text := contentItem.Get("text").String()
							contentPart := []byte(`{"type":"text","text":""}`)
							contentPart, _ = sjson.SetBytes(contentPart, "text", text)
							message, _ = sjson.SetRawBytes(message, "content.-1", contentPart)
						case "input_image":
							imageURL := contentItem.Get("image_url").String()
							contentPart := []byte(`{"type":"image_url","image_url":{"url":""}}`)
							contentPart, _ = sjson.SetBytes(contentPart, "image_url.url", imageURL)
							message, _ = sjson.SetRawBytes(message, "content.-1", contentPart)
						case "reasoning_text":
							message, _ = sjson.SetBytes(message, "reasoning_content", contentItem.Get("text").String())
						}
						return true
					})
				} else if content.Type == gjson.String {
					message, _ = sjson.SetBytes(message, "content", content.String())
				}

				out, _ = sjson.SetRawBytes(out, "messages.-1", message)
			}

			// Reset all buffers
			pendingFunctionCalls = nil
			pendingToolOutputs = nil
			bufferedMessages = nil
		}

		input.ForEach(func(_, item gjson.Result) bool {
			itemType := item.Get("type").String()
			if itemType == "" && item.Get("role").String() != "" {
				itemType = "message"
			}

			switch itemType {
			case "message", "":
				if len(pendingFunctionCalls) > 0 || len(pendingToolOutputs) > 0 {
					// We're inside an active tool group — buffer this message
					// so it gets emitted after the tool messages in the correct order.
					bufferedMessages = append(bufferedMessages, item)
				} else {
					// No tool group active, emit directly
					role := item.Get("role").String()
					if role == "developer" {
						role = "user"
					}
					message := []byte(`{"role":"","content":[]}`)
					message, _ = sjson.SetBytes(message, "role", role)

					if role == "assistant" && pendingReasoningContent != "" {
						message, _ = sjson.SetBytes(message, "reasoning_content", pendingReasoningContent)
						pendingReasoningContent = ""
					}

					if content := item.Get("content"); content.Exists() && content.IsArray() {
						content.ForEach(func(_, contentItem gjson.Result) bool {
							contentType := contentItem.Get("type").String()
							if contentType == "" {
								contentType = "input_text"
							}
							switch contentType {
							case "input_text", "output_text":
								text := contentItem.Get("text").String()
								contentPart := []byte(`{"type":"text","text":""}`)
								contentPart, _ = sjson.SetBytes(contentPart, "text", text)
								message, _ = sjson.SetRawBytes(message, "content.-1", contentPart)
							case "input_image":
								imageURL := contentItem.Get("image_url").String()
								contentPart := []byte(`{"type":"image_url","image_url":{"url":""}}`)
								contentPart, _ = sjson.SetBytes(contentPart, "image_url.url", imageURL)
								message, _ = sjson.SetRawBytes(message, "content.-1", contentPart)
							case "reasoning_text":
								message, _ = sjson.SetBytes(message, "reasoning_content", contentItem.Get("text").String())
							}
							return true
						})
					} else if content.Type == gjson.String {
						message, _ = sjson.SetBytes(message, "content", content.String())
					}

					out, _ = sjson.SetRawBytes(out, "messages.-1", message)
				}

			case "function_call":
				// If the previous tool group already has outputs collected,
				// this function_call starts a *new* group flush the old one first.
				if len(pendingToolOutputs) > 0 {
					flushToolGroup()
				}
				pendingFunctionCalls = append(pendingFunctionCalls, item)

			case "function_call_output":
				// Collect the output it will be emitted by flushToolGroup
				// in the correct position (after assistant+tool_calls,
				// before any buffered messages).
				pendingToolOutputs = append(pendingToolOutputs, item)

			case "reasoning":
				// Extract summary text from standalone reasoning input items.
				// This text will be injected as reasoning_content on the
				// subsequent assistant message for models that require echo-back.
				if summary := item.Get("summary"); summary.Exists() && summary.IsArray() {
					summary.ForEach(func(_, s gjson.Result) bool {
						if s.Get("type").String() == "summary_text" {
							if text := s.Get("text").String(); text != "" {
								pendingReasoningContent = text
							}
						}
						return true
					})
				}
			}

			return true
		})

		// Flush any remaining tool group at end of array
		flushToolGroup()
	} else if input.Type == gjson.String {
		msg := []byte(`{}`)
		msg, _ = sjson.SetBytes(msg, "role", "user")
		msg, _ = sjson.SetBytes(msg, "content", input.String())
		out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
	}

	// Convert tools from responses format to chat completions format
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		var chatCompletionsTools []interface{}

		tools.ForEach(func(_, tool gjson.Result) bool {
			// Built-in tools (e.g. {"type":"web_search"}) are already compatible with the Chat Completions schema.
			// Only function tools need structural conversion because Chat Completions nests details under "function".
			toolType := tool.Get("type").String()
			if toolType != "" && toolType != "function" && tool.IsObject() {
				// Almost all providers lack built-in tools, so we just ignore them.
				// chatCompletionsTools = append(chatCompletionsTools, tool.Value())
				return true
			}

			chatTool := []byte(`{"type":"function","function":{}}`)

			// Convert tool structure from responses format to chat completions format
			function := []byte(`{"name":"","description":"","parameters":{}}`)

			if name := tool.Get("name"); name.Exists() {
				function, _ = sjson.SetBytes(function, "name", name.String())
			}

			if description := tool.Get("description"); description.Exists() {
				function, _ = sjson.SetBytes(function, "description", description.String())
			}

			if parameters := tool.Get("parameters"); parameters.Exists() {
				function, _ = sjson.SetRawBytes(function, "parameters", []byte(parameters.Raw))
			}

			chatTool, _ = sjson.SetRawBytes(chatTool, "function", function)
			chatCompletionsTools = append(chatCompletionsTools, gjson.ParseBytes(chatTool).Value())

			return true
		})

		if len(chatCompletionsTools) > 0 {
			out, _ = sjson.SetBytes(out, "tools", chatCompletionsTools)
		}
	}

	// Handle reasoning configuration.
	//
	// When reasoning.effort is explicitly set (e.g. "low", "medium", "high"),
	// map it to the Chat Completions reasoning_effort field — this enables
	// thinking mode on models that support it.
	//
	// When reasoning is absent, disable thinking mode via the non-standard
	// "thinking" parameter. Without this, DeepSeek (and similar providers)
	// default to thinking mode and return reasoning_content, which they then
	// require echoed back on every subsequent request. Codex CLI does not
	// echo back reasoning_text, so disabling thinking by default is necessary
	// for reliable operation. Providers that don't support "thinking" (e.g.
	// OpenAI) will return a 400, which is caught and handled by the retry layer.
	if reasoning := root.Get("reasoning"); reasoning.Exists() {
		effort := reasoning.Get("effort").String()
		if effort != "" {
			out, _ = sjson.SetBytes(out, "reasoning_effort", strings.ToLower(strings.TrimSpace(effort)))
		} else {
			out, _ = sjson.SetBytes(out, "thinking", map[string]interface{}{"type": "disabled"})
		}
	} else {
		out, _ = sjson.SetBytes(out, "thinking", map[string]interface{}{"type": "disabled"})
	}

	// Convert tool_choice if present
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		out, _ = sjson.SetBytes(out, "tool_choice", toolChoice.String())
	}

	return out
}
