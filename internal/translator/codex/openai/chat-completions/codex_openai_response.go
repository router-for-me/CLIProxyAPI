// Package openai provides response translation functionality for Codex to OpenAI API compatibility.
// This package handles the conversion of Codex API responses into OpenAI Chat Completions-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by OpenAI API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, reasoning content, and usage metadata appropriately.
package chat_completions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	dataTag = []byte("data:")
)

// ConvertCliToOpenAIParams holds parameters for response conversion.
type ConvertCliToOpenAIParams struct {
	ResponseID                string
	CreatedAt                 int64
	Model                     string
	FunctionCallIndex         int
	HasReceivedArgumentsDelta bool
	HasToolCallAnnounced      bool
	UseLegacyFunctionCall     bool
	LastImageHashByItemID     map[string][32]byte
}

// ConvertCodexResponseToOpenAI translates a single chunk of a streaming response from the
// Codex API format to the OpenAI Chat Completions streaming format.
// It processes various Codex event types and transforms them into OpenAI-compatible JSON responses.
// The function handles text content, tool calls, reasoning content, and usage metadata, outputting
// responses that match the OpenAI API format. It supports incremental updates for streaming responses.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Codex API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - [][]byte: A slice of OpenAI-compatible JSON responses
func ConvertCodexResponseToOpenAI(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &ConvertCliToOpenAIParams{
			Model:                     modelName,
			CreatedAt:                 0,
			ResponseID:                "",
			FunctionCallIndex:         -1,
			HasReceivedArgumentsDelta: false,
			HasToolCallAnnounced:      false,
			UseLegacyFunctionCall:     useLegacyOpenAIFunctionCall(originalRequestRawJSON),
			LastImageHashByItemID:     make(map[string][32]byte),
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	// Initialize the OpenAI SSE template.
	template := []byte(`{"id":"","object":"chat.completion.chunk","created":12345,"model":"model","choices":[{"index":0,"delta":{},"finish_reason":null,"native_finish_reason":null}]}`)

	rootResult := gjson.ParseBytes(rawJSON)

	typeResult := rootResult.Get("type")
	dataType := typeResult.String()
	if dataType == "response.created" {
		(*param).(*ConvertCliToOpenAIParams).ResponseID = rootResult.Get("response.id").String()
		(*param).(*ConvertCliToOpenAIParams).CreatedAt = rootResult.Get("response.created_at").Int()
		(*param).(*ConvertCliToOpenAIParams).Model = rootResult.Get("response.model").String()
		if (*param).(*ConvertCliToOpenAIParams).LastImageHashByItemID == nil {
			(*param).(*ConvertCliToOpenAIParams).LastImageHashByItemID = make(map[string][32]byte)
		}
		return [][]byte{}
	}

	// Extract and set the model version.
	cachedModel := (*param).(*ConvertCliToOpenAIParams).Model
	if modelResult := gjson.GetBytes(rawJSON, "model"); modelResult.Exists() {
		template, _ = sjson.SetBytes(template, "model", modelResult.String())
	} else if cachedModel != "" {
		template, _ = sjson.SetBytes(template, "model", cachedModel)
	} else if modelName != "" {
		template, _ = sjson.SetBytes(template, "model", modelName)
	}

	template, _ = sjson.SetBytes(template, "created", (*param).(*ConvertCliToOpenAIParams).CreatedAt)

	// Extract and set the response ID.
	template, _ = sjson.SetBytes(template, "id", (*param).(*ConvertCliToOpenAIParams).ResponseID)

	// Extract and set usage metadata (token counts).
	if usageResult := gjson.GetBytes(rawJSON, "response.usage"); usageResult.Exists() {
		if outputTokensResult := usageResult.Get("output_tokens"); outputTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.completion_tokens", outputTokensResult.Int())
		}
		if totalTokensResult := usageResult.Get("total_tokens"); totalTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.total_tokens", totalTokensResult.Int())
		}
		if inputTokensResult := usageResult.Get("input_tokens"); inputTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens", inputTokensResult.Int())
		}
		if cachedTokensResult := usageResult.Get("input_tokens_details.cached_tokens"); cachedTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens_details.cached_tokens", cachedTokensResult.Int())
		}
		if reasoningTokensResult := usageResult.Get("output_tokens_details.reasoning_tokens"); reasoningTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.completion_tokens_details.reasoning_tokens", reasoningTokensResult.Int())
		}
	}

	defer func() {
		if *param != nil {
			traceOpenAIChatResponse(dataType, (*param).(*ConvertCliToOpenAIParams).UseLegacyFunctionCall, rawJSON, nil)
		}
	}()

	if dataType == "response.reasoning_summary_text.delta" {
		if deltaResult := rootResult.Get("delta"); deltaResult.Exists() {
			template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
			template, _ = sjson.SetBytes(template, "choices.0.delta.reasoning_content", deltaResult.String())
		}
	} else if dataType == "response.reasoning_summary_text.done" {
		template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
		template, _ = sjson.SetBytes(template, "choices.0.delta.reasoning_content", "\n\n")
	} else if dataType == "response.output_text.delta" {
		if deltaResult := rootResult.Get("delta"); deltaResult.Exists() {
			template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
			template, _ = sjson.SetBytes(template, "choices.0.delta.content", deltaResult.String())
		}
	} else if dataType == "response.image_generation_call.partial_image" {
		itemID := rootResult.Get("item_id").String()
		b64 := rootResult.Get("partial_image_b64").String()
		if b64 == "" {
			return [][]byte{}
		}
		if itemID != "" {
			p := (*param).(*ConvertCliToOpenAIParams)
			if p.LastImageHashByItemID == nil {
				p.LastImageHashByItemID = make(map[string][32]byte)
			}
			hash := sha256.Sum256([]byte(b64))
			if last, ok := p.LastImageHashByItemID[itemID]; ok && last == hash {
				return [][]byte{}
			}
			p.LastImageHashByItemID[itemID] = hash
		}

		outputFormat := rootResult.Get("output_format").String()
		mimeType := mimeTypeFromCodexOutputFormat(outputFormat)
		imageURL := "data:" + mimeType + ";base64," + b64

		imagesResult := gjson.GetBytes(template, "choices.0.delta.images")
		if !imagesResult.Exists() || !imagesResult.IsArray() {
			template, _ = sjson.SetRawBytes(template, "choices.0.delta.images", []byte(`[]`))
		}
		imageIndex := len(gjson.GetBytes(template, "choices.0.delta.images").Array())
		imagePayload := []byte(`{"type":"image_url","image_url":{"url":""}}`)
		imagePayload, _ = sjson.SetBytes(imagePayload, "index", imageIndex)
		imagePayload, _ = sjson.SetBytes(imagePayload, "image_url.url", imageURL)

		template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.images.-1", imagePayload)
	} else if dataType == "response.completed" {
		finishReason := "stop"
		if (*param).(*ConvertCliToOpenAIParams).FunctionCallIndex != -1 {
			finishReason = "tool_calls"
			if (*param).(*ConvertCliToOpenAIParams).UseLegacyFunctionCall {
				finishReason = "function_call"
			}
		}
		template, _ = sjson.SetBytes(template, "choices.0.finish_reason", finishReason)
		template, _ = sjson.SetBytes(template, "choices.0.native_finish_reason", finishReason)
	} else if dataType == "response.output_item.added" {
		itemResult := rootResult.Get("item")
		if !itemResult.Exists() || !isCodexOpenAIToolCall(itemResult) {
			return [][]byte{}
		}

		// Increment index for this new function call item.
		(*param).(*ConvertCliToOpenAIParams).FunctionCallIndex++
		(*param).(*ConvertCliToOpenAIParams).HasReceivedArgumentsDelta = false
		(*param).(*ConvertCliToOpenAIParams).HasToolCallAnnounced = true

		functionCallItemTemplate := []byte(`{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}`)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "index", (*param).(*ConvertCliToOpenAIParams).FunctionCallIndex)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "id", itemResult.Get("call_id").String())

		// Restore original tool name if it was shortened.
		name := itemResult.Get("name").String()
		rev := buildReverseMapFromOriginalOpenAI(originalRequestRawJSON)
		if orig, ok := rev[name]; ok {
			name = orig
		}
		if (*param).(*ConvertCliToOpenAIParams).UseLegacyFunctionCall {
			template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
			template, _ = sjson.SetBytes(template, "choices.0.delta.function_call.name", name)
			template, _ = sjson.SetBytes(template, "choices.0.delta.function_call.arguments", "")
			return [][]byte{template}
		}

		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.name", name)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.arguments", "")
		template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte(`[]`))
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls.-1", functionCallItemTemplate)

	} else if dataType == "response.function_call_arguments.delta" || dataType == "response.custom_tool_call_input.delta" {
		(*param).(*ConvertCliToOpenAIParams).HasReceivedArgumentsDelta = true

		deltaValue := rootResult.Get("delta").String()
		if (*param).(*ConvertCliToOpenAIParams).UseLegacyFunctionCall {
			template, _ = sjson.SetBytes(template, "choices.0.delta.function_call.arguments", deltaValue)
			return [][]byte{template}
		}

		functionCallItemTemplate := []byte(`{"index":0,"function":{"arguments":""}}`)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "index", (*param).(*ConvertCliToOpenAIParams).FunctionCallIndex)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.arguments", deltaValue)

		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte(`[]`))
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls.-1", functionCallItemTemplate)

	} else if dataType == "response.function_call_arguments.done" || dataType == "response.custom_tool_call_input.done" {
		if (*param).(*ConvertCliToOpenAIParams).HasReceivedArgumentsDelta {
			// Arguments were already streamed via delta events; nothing to emit.
			return [][]byte{}
		}

		// Fallback: no delta events were received, emit the full arguments as a single chunk.
		fullArgs := rootResult.Get("arguments").String()
		if dataType == "response.custom_tool_call_input.done" {
			fullArgs = rootResult.Get("input").String()
		}
		if (*param).(*ConvertCliToOpenAIParams).UseLegacyFunctionCall {
			template, _ = sjson.SetBytes(template, "choices.0.delta.function_call.arguments", fullArgs)
			return [][]byte{template}
		}

		functionCallItemTemplate := []byte(`{"index":0,"function":{"arguments":""}}`)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "index", (*param).(*ConvertCliToOpenAIParams).FunctionCallIndex)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.arguments", fullArgs)

		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte(`[]`))
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls.-1", functionCallItemTemplate)

	} else if dataType == "response.output_item.done" {
		itemResult := rootResult.Get("item")
		if !itemResult.Exists() {
			return [][]byte{}
		}
		itemType := itemResult.Get("type").String()
		if itemType == "image_generation_call" {
			itemID := itemResult.Get("id").String()
			b64 := itemResult.Get("result").String()
			if b64 == "" {
				return [][]byte{}
			}
			if itemID != "" {
				p := (*param).(*ConvertCliToOpenAIParams)
				if p.LastImageHashByItemID == nil {
					p.LastImageHashByItemID = make(map[string][32]byte)
				}
				hash := sha256.Sum256([]byte(b64))
				if last, ok := p.LastImageHashByItemID[itemID]; ok && last == hash {
					return [][]byte{}
				}
				p.LastImageHashByItemID[itemID] = hash
			}

			outputFormat := itemResult.Get("output_format").String()
			mimeType := mimeTypeFromCodexOutputFormat(outputFormat)
			imageURL := "data:" + mimeType + ";base64," + b64

			imagesResult := gjson.GetBytes(template, "choices.0.delta.images")
			if !imagesResult.Exists() || !imagesResult.IsArray() {
				template, _ = sjson.SetRawBytes(template, "choices.0.delta.images", []byte(`[]`))
			}
			imageIndex := len(gjson.GetBytes(template, "choices.0.delta.images").Array())
			imagePayload := []byte(`{"type":"image_url","image_url":{"url":""}}`)
			imagePayload, _ = sjson.SetBytes(imagePayload, "index", imageIndex)
			imagePayload, _ = sjson.SetBytes(imagePayload, "image_url.url", imageURL)

			template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
			template, _ = sjson.SetRawBytes(template, "choices.0.delta.images.-1", imagePayload)
			return [][]byte{template}
		}
		if !isCodexOpenAIToolCall(itemResult) {
			return [][]byte{}
		}

		if (*param).(*ConvertCliToOpenAIParams).HasToolCallAnnounced {
			// Custom tool calls may only include their raw input on output_item.done.
			// Emit it as the Chat Completions arguments delta before suppressing the
			// duplicate tool-call announcement.
			if itemType == "custom_tool_call" && !(*param).(*ConvertCliToOpenAIParams).HasReceivedArgumentsDelta {
				(*param).(*ConvertCliToOpenAIParams).HasToolCallAnnounced = false
				input := itemResult.Get("input").String()
				if input == "" {
					return [][]byte{}
				}
				if (*param).(*ConvertCliToOpenAIParams).UseLegacyFunctionCall {
					template, _ = sjson.SetBytes(template, "choices.0.delta.function_call.arguments", input)
					return [][]byte{template}
				}

				functionCallItemTemplate := []byte(`{"index":0,"function":{"arguments":""}}`)
				functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "index", (*param).(*ConvertCliToOpenAIParams).FunctionCallIndex)
				functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.arguments", input)

				template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte(`[]`))
				template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls.-1", functionCallItemTemplate)
				return [][]byte{template}
			}
			// Tool call was already announced via output_item.added; skip emission.
			(*param).(*ConvertCliToOpenAIParams).HasToolCallAnnounced = false
			return [][]byte{}
		}

		// Fallback path: model skipped output_item.added, so emit complete tool call now.
		(*param).(*ConvertCliToOpenAIParams).FunctionCallIndex++

		functionCallItemTemplate := []byte(`{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}`)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "index", (*param).(*ConvertCliToOpenAIParams).FunctionCallIndex)

		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte(`[]`))
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "id", itemResult.Get("call_id").String())

		// Restore original tool name if it was shortened.
		name := itemResult.Get("name").String()
		rev := buildReverseMapFromOriginalOpenAI(originalRequestRawJSON)
		if orig, ok := rev[name]; ok {
			name = orig
		}
		if (*param).(*ConvertCliToOpenAIParams).UseLegacyFunctionCall {
			template, _ = sjson.SetBytes(template, "choices.0.delta.function_call.name", name)
			template, _ = sjson.SetBytes(template, "choices.0.delta.function_call.arguments", codexOpenAIToolCallArguments(itemResult))
			template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
			return [][]byte{template}
		}
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.name", name)

		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.arguments", codexOpenAIToolCallArguments(itemResult))
		template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls.-1", functionCallItemTemplate)

	} else {
		return [][]byte{}
	}

	return [][]byte{template}
}

// ConvertCodexResponseToOpenAINonStream converts a non-streaming Codex response to a non-streaming OpenAI response.
// This function processes the complete Codex response and transforms it into a single OpenAI-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the OpenAI API format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Codex API
//   - param: A pointer to a parameter object for the conversion (unused in current implementation)
//
// Returns:
//   - []byte: An OpenAI-compatible JSON response containing all message content and metadata
func ConvertCodexResponseToOpenAINonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	rootResult := gjson.ParseBytes(rawJSON)
	// Verify this is a response.completed event
	if rootResult.Get("type").String() != "response.completed" {
		return []byte{}
	}

	unixTimestamp := time.Now().Unix()

	responseResult := rootResult.Get("response")

	template := []byte(`{"id":"","object":"chat.completion","created":123456,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`)

	// Extract and set the model version.
	if modelResult := responseResult.Get("model"); modelResult.Exists() {
		template, _ = sjson.SetBytes(template, "model", modelResult.String())
	}

	// Extract and set the creation timestamp.
	if createdAtResult := responseResult.Get("created_at"); createdAtResult.Exists() {
		template, _ = sjson.SetBytes(template, "created", createdAtResult.Int())
	} else {
		template, _ = sjson.SetBytes(template, "created", unixTimestamp)
	}

	// Extract and set the response ID.
	if idResult := responseResult.Get("id"); idResult.Exists() {
		template, _ = sjson.SetBytes(template, "id", idResult.String())
	}

	// Extract and set usage metadata (token counts).
	if usageResult := responseResult.Get("usage"); usageResult.Exists() {
		if outputTokensResult := usageResult.Get("output_tokens"); outputTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.completion_tokens", outputTokensResult.Int())
		}
		if totalTokensResult := usageResult.Get("total_tokens"); totalTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.total_tokens", totalTokensResult.Int())
		}
		if inputTokensResult := usageResult.Get("input_tokens"); inputTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens", inputTokensResult.Int())
		}
		if cachedTokensResult := usageResult.Get("input_tokens_details.cached_tokens"); cachedTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens_details.cached_tokens", cachedTokensResult.Int())
		}
		if reasoningTokensResult := usageResult.Get("output_tokens_details.reasoning_tokens"); reasoningTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.completion_tokens_details.reasoning_tokens", reasoningTokensResult.Int())
		}
	}

	// Process the output array for content and function calls
	var toolCalls [][]byte
	var legacyFunctionCall []byte
	var images [][]byte
	useLegacyFunctionCall := useLegacyOpenAIFunctionCall(originalRequestRawJSON)
	outputResult := responseResult.Get("output")
	if outputResult.IsArray() {
		outputArray := outputResult.Array()
		var contentText string
		var reasoningText string

		for _, outputItem := range outputArray {
			outputType := outputItem.Get("type").String()

			switch outputType {
			case "reasoning":
				// Extract reasoning content from summary
				if summaryResult := outputItem.Get("summary"); summaryResult.IsArray() {
					summaryArray := summaryResult.Array()
					for _, summaryItem := range summaryArray {
						if summaryItem.Get("type").String() == "summary_text" {
							if text := summaryItem.Get("text").String(); text != "" {
								reasoningText += text
							}
							break
						}
					}
				}
			case "message":
				// Extract message content
				if contentResult := outputItem.Get("content"); contentResult.IsArray() {
					contentArray := contentResult.Array()
					for _, contentItem := range contentArray {
						if contentItem.Get("type").String() == "output_text" {
							if text := contentItem.Get("text").String(); text != "" {
								contentText += text
							}
							break
						}
					}
				}
			case "function_call", "custom_tool_call":
				// Handle function call content
				functionCallTemplate := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)

				if callIdResult := outputItem.Get("call_id"); callIdResult.Exists() {
					functionCallTemplate, _ = sjson.SetBytes(functionCallTemplate, "id", callIdResult.String())
				}

				if nameResult := outputItem.Get("name"); nameResult.Exists() {
					n := nameResult.String()
					rev := buildReverseMapFromOriginalOpenAI(originalRequestRawJSON)
					if orig, ok := rev[n]; ok {
						n = orig
					}
					functionCallTemplate, _ = sjson.SetBytes(functionCallTemplate, "function.name", n)
				}

				if args := codexOpenAIToolCallArguments(outputItem); args != "" {
					functionCallTemplate, _ = sjson.SetBytes(functionCallTemplate, "function.arguments", args)
				}

				toolCalls = append(toolCalls, functionCallTemplate)
				if useLegacyFunctionCall && len(legacyFunctionCall) == 0 {
					legacyFunctionCall = functionCallTemplate
				}
			case "image_generation_call":
				b64 := outputItem.Get("result").String()
				if b64 == "" {
					break
				}
				outputFormat := outputItem.Get("output_format").String()
				mimeType := mimeTypeFromCodexOutputFormat(outputFormat)
				imageURL := "data:" + mimeType + ";base64," + b64

				imagePayload := []byte(`{"type":"image_url","image_url":{"url":""}}`)
				imagePayload, _ = sjson.SetBytes(imagePayload, "index", len(images))
				imagePayload, _ = sjson.SetBytes(imagePayload, "image_url.url", imageURL)
				images = append(images, imagePayload)
			}
		}

		// Set content and reasoning content if found
		if contentText != "" {
			template, _ = sjson.SetBytes(template, "choices.0.message.content", contentText)
			template, _ = sjson.SetBytes(template, "choices.0.message.role", "assistant")
		}

		if reasoningText != "" {
			template, _ = sjson.SetBytes(template, "choices.0.message.reasoning_content", reasoningText)
			template, _ = sjson.SetBytes(template, "choices.0.message.role", "assistant")
		}

		// Add tool calls if any
		if len(toolCalls) > 0 {
			if useLegacyFunctionCall && len(legacyFunctionCall) > 0 {
				template, _ = sjson.SetBytes(template, "choices.0.message.function_call.name", gjson.GetBytes(legacyFunctionCall, "function.name").String())
				template, _ = sjson.SetBytes(template, "choices.0.message.function_call.arguments", gjson.GetBytes(legacyFunctionCall, "function.arguments").String())
			} else {
				template, _ = sjson.SetRawBytes(template, "choices.0.message.tool_calls", []byte(`[]`))
				for _, toolCall := range toolCalls {
					template, _ = sjson.SetRawBytes(template, "choices.0.message.tool_calls.-1", toolCall)
				}
			}
			template, _ = sjson.SetBytes(template, "choices.0.message.role", "assistant")
		}

		// Add images if any
		if len(images) > 0 {
			template, _ = sjson.SetRawBytes(template, "choices.0.message.images", []byte(`[]`))
			for _, image := range images {
				template, _ = sjson.SetRawBytes(template, "choices.0.message.images.-1", image)
			}
			template, _ = sjson.SetBytes(template, "choices.0.message.role", "assistant")
		}
	}

	// Extract and set the finish reason based on status
	if statusResult := responseResult.Get("status"); statusResult.Exists() {
		status := statusResult.String()
		if status == "completed" {
			finishReason := "stop"
			if len(toolCalls) > 0 {
				finishReason = "tool_calls"
				if useLegacyFunctionCall {
					finishReason = "function_call"
				}
			}
			template, _ = sjson.SetBytes(template, "choices.0.finish_reason", finishReason)
			template, _ = sjson.SetBytes(template, "choices.0.native_finish_reason", finishReason)
		}
	}

	return template
}

func useLegacyOpenAIFunctionCall(original []byte) bool {
	functions := gjson.GetBytes(original, "functions")
	if !functions.IsArray() || len(functions.Array()) == 0 {
		return false
	}
	tools := gjson.GetBytes(original, "tools")
	return !tools.IsArray() || len(tools.Array()) == 0
}

func isCodexOpenAIToolCall(item gjson.Result) bool {
	switch item.Get("type").String() {
	case "function_call", "custom_tool_call":
		return true
	default:
		return false
	}
}

func codexOpenAIToolCallArguments(item gjson.Result) string {
	if item.Get("type").String() == "custom_tool_call" {
		return item.Get("input").String()
	}
	return item.Get("arguments").String()
}

// buildReverseMapFromOriginalOpenAI builds a map of shortened tool name -> original tool name
// from the original OpenAI-style request JSON using the same shortening logic.
func buildReverseMapFromOriginalOpenAI(original []byte) map[string]string {
	rev := map[string]string{}
	var names []string
	tools := gjson.GetBytes(original, "tools")
	if tools.IsArray() && len(tools.Array()) > 0 {
		arr := tools.Array()
		for i := 0; i < len(arr); i++ {
			t := arr[i]
			if t.Get("type").String() != "function" {
				continue
			}
			fn := t.Get("function")
			if !fn.Exists() {
				continue
			}
			if v := fn.Get("name"); v.Exists() {
				names = append(names, v.String())
			}
		}
	} else if functions := gjson.GetBytes(original, "functions"); functions.IsArray() {
		for _, fn := range functions.Array() {
			if v := fn.Get("name"); v.Exists() {
				names = append(names, v.String())
			}
		}
	}
	if len(names) > 0 {
		m := buildShortNameMap(names)
		for orig, short := range m {
			rev[short] = orig
		}
	}
	return rev
}

func mimeTypeFromCodexOutputFormat(outputFormat string) string {
	if outputFormat == "" {
		return "image/png"
	}
	if strings.Contains(outputFormat, "/") {
		return outputFormat
	}
	switch strings.ToLower(outputFormat) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return "image/png"
	}
}
