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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	applypatch "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/apply-patch"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	dataTag = []byte("data:")
)

type toolCallStreamState struct {
	Index            int
	ArgumentsEmitted bool
	Patch            bool
	InputStarted     bool
	InputClosed      bool
	Done             bool
}

// ConvertCliToOpenAIParams holds parameters for response conversion.
type ConvertCliToOpenAIParams struct {
	ServiceTier           string
	ResponseID            string
	CreatedAt             int64
	Model                 string
	FunctionCallIndex     int
	toolCallStates        map[string]*toolCallStreamState
	currentToolCall       *toolCallStreamState
	citationKeys          map[string]struct{}
	emittedTextRunes      int64
	LastImageHashByItemID map[string][32]byte
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
			Model:                 modelName,
			CreatedAt:             0,
			ResponseID:            "",
			FunctionCallIndex:     -1,
			toolCallStates:        make(map[string]*toolCallStreamState),
			citationKeys:          make(map[string]struct{}),
			LastImageHashByItemID: make(map[string][32]byte),
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	// Initialize the OpenAI SSE template.
	template := []byte(`{"id":"","object":"chat.completion.chunk","created":12345,"model":"model","choices":[{"index":0,"delta":{},"finish_reason":null,"native_finish_reason":null}]}`)

	rootResult := gjson.ParseBytes(rawJSON)

	p := (*param).(*ConvertCliToOpenAIParams)
	if tier := codexResponseServiceTier(rootResult.Get("response")); tier != "" {
		p.ServiceTier = tier
	} else if tier := codexResponseServiceTier(rootResult); tier != "" {
		p.ServiceTier = tier
	}
	if p.ServiceTier != "" {
		template, _ = sjson.SetBytes(template, "service_tier", p.ServiceTier)
	}

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
		template = setCodexCacheWriteTokens(template, usageResult)
		if reasoningTokensResult := usageResult.Get("output_tokens_details.reasoning_tokens"); reasoningTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.completion_tokens_details.reasoning_tokens", reasoningTokensResult.Int())
		}
	}

	if dataType == "response.reasoning_summary_text.delta" || dataType == "response.reasoning_text.delta" {
		if deltaResult := rootResult.Get("delta"); deltaResult.Exists() {
			template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
			template, _ = sjson.SetBytes(template, "choices.0.delta.reasoning_content", deltaResult.String())
		}
	} else if dataType == "response.reasoning_summary_text.done" || dataType == "response.reasoning_text.done" {
		template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
		template, _ = sjson.SetBytes(template, "choices.0.delta.reasoning_content", "\n\n")
	} else if dataType == "response.output_text.delta" {
		if deltaResult := rootResult.Get("delta"); deltaResult.Exists() {
			delta := deltaResult.String()
			template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
			template, _ = sjson.SetBytes(template, "choices.0.delta.content", delta)
			p.emittedTextRunes += int64(utf8.RuneCountInString(delta))
		}
	} else if dataType == "response.output_text.annotation.added" || dataType == "response.output_text.done" || dataType == "response.content_part.done" {
		citations := buildCodexURLCitations(codexAnnotationsFromEvent(rootResult), p.emittedTextRunes, p.citationKeys)
		if len(citations) == 0 {
			return [][]byte{}
		}
		template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.annotations", translatorcommon.JoinRawArray(citations))
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
	} else if dataType == "response.completed" || dataType == "response.incomplete" {
		finishReason := "stop"
		nativeFinishReason := finishReason
		if dataType == "response.incomplete" {
			nativeFinishReason = rootResult.Get("response.incomplete_details.reason").String()
			switch nativeFinishReason {
			case "max_tokens", "max_output_tokens":
				finishReason = "length"
			case "content_filter":
				finishReason = "content_filter"
			}
		} else if (*param).(*ConvertCliToOpenAIParams).FunctionCallIndex != -1 {
			finishReason = "tool_calls"
			nativeFinishReason = finishReason
		}
		template, _ = sjson.SetBytes(template, "choices.0.finish_reason", finishReason)
		template, _ = sjson.SetBytes(template, "choices.0.native_finish_reason", nativeFinishReason)
	} else if dataType == "response.output_item.added" {
		itemResult := rootResult.Get("item")
		if !itemResult.Exists() || !isCodexToolCallType(itemResult.Get("type").String()) {
			return [][]byte{}
		}

		// Increment index for this new tool call item.
		p := (*param).(*ConvertCliToOpenAIParams)
		p.FunctionCallIndex++
		state := &toolCallStreamState{Index: p.FunctionCallIndex, Patch: isOriginalCustomPatch(originalRequestRawJSON, itemResult)}
		registerToolCallState(p, rootResult, itemResult, state)

		functionCallItemTemplate := []byte(`{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}`)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "index", state.Index)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "id", itemResult.Get("call_id").String())

		// Restore original tool name if it was shortened.
		name := itemResult.Get("name").String()
		rev := buildReverseMapFromOriginalOpenAI(originalRequestRawJSON)
		if orig, ok := rev[name]; ok {
			name = orig
		}
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.name", name)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.arguments", "")

		template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte(`[]`))
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls.-1", functionCallItemTemplate)

	} else if dataType == "response.function_call_arguments.delta" || dataType == "response.custom_tool_call_input.delta" {
		p := (*param).(*ConvertCliToOpenAIParams)
		state := findToolCallState(p, rootResult, gjson.Result{})
		deltaValue := rootResult.Get("delta").String()
		if state == nil || state.Done || deltaValue == "" {
			return [][]byte{}
		}
		state.ArgumentsEmitted = true
		if state.Patch {
			deltaValue = applypatch.EscapeInputFragment(deltaValue)
			if !state.InputStarted {
				deltaValue = `{"input":"` + deltaValue
				state.InputStarted = true
			}
		}

		functionCallItemTemplate := []byte(`{"index":0,"function":{"arguments":""}}`)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "index", state.Index)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.arguments", deltaValue)

		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte(`[]`))
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls.-1", functionCallItemTemplate)

	} else if dataType == "response.function_call_arguments.done" || dataType == "response.custom_tool_call_input.done" {
		p := (*param).(*ConvertCliToOpenAIParams)
		state := findToolCallState(p, rootResult, gjson.Result{})
		if state == nil || state.Done || state.InputClosed || (state.ArgumentsEmitted && !state.Patch) {
			return [][]byte{}
		}

		// Fallback: no delta events were received, emit the full arguments as a single chunk.
		fullArgsField := "arguments"
		if dataType == "response.custom_tool_call_input.done" {
			fullArgsField = "input"
		}
		state.ArgumentsEmitted = true
		fullArgs := rootResult.Get(fullArgsField).String()
		if state.Patch {
			fullArgs = finishPatchChatArguments(state, fullArgs)
		}
		if fullArgs == "" {
			return [][]byte{}
		}
		functionCallItemTemplate := []byte(`{"index":0,"function":{"arguments":""}}`)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "index", state.Index)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.arguments", fullArgs)

		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte(`[]`))
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls.-1", functionCallItemTemplate)

	} else if dataType == "response.output_item.done" {
		itemResult := rootResult.Get("item")
		if !itemResult.Exists() {
			return [][]byte{}
		}
		if itemResult.Get("type").String() == "message" {
			citations := buildCodexURLCitations(codexAnnotationsFromEvent(rootResult), p.emittedTextRunes, p.citationKeys)
			if len(citations) == 0 {
				return [][]byte{}
			}
			template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
			template, _ = sjson.SetRawBytes(template, "choices.0.delta.annotations", translatorcommon.JoinRawArray(citations))
			return [][]byte{template}
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
		if !isCodexToolCallType(itemType) {
			return [][]byte{}
		}

		p := (*param).(*ConvertCliToOpenAIParams)
		state := findToolCallState(p, rootResult, itemResult)
		if state != nil {
			if state.Done {
				return [][]byte{}
			}
			state.Done = true
			if state.ArgumentsEmitted && (!state.Patch || state.InputClosed) {
				return [][]byte{}
			}

			// The tool was announced, but no argument event arrived. Emit only the
			// completed arguments so the id and name are not duplicated.
			state.ArgumentsEmitted = true
			fullArgs := codexToolCallArguments(itemResult)
			if state.Patch {
				fullArgs = finishPatchChatArguments(state, fullArgs)
			}
			if fullArgs == "" {
				return [][]byte{}
			}
			functionCallItemTemplate := []byte(`{"index":0,"function":{"arguments":""}}`)
			functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "index", state.Index)
			functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.arguments", fullArgs)
			template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte(`[]`))
			template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls.-1", functionCallItemTemplate)
			return [][]byte{template}
		}

		// Fallback path: model skipped output_item.added, so emit the complete tool call now.
		p.FunctionCallIndex++
		state = &toolCallStreamState{Index: p.FunctionCallIndex, ArgumentsEmitted: true, Done: true, Patch: isOriginalCustomPatch(originalRequestRawJSON, itemResult)}
		registerToolCallState(p, rootResult, itemResult, state)

		functionCallItemTemplate := []byte(`{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}`)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "index", state.Index)

		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte(`[]`))
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "id", itemResult.Get("call_id").String())

		// Restore original tool name if it was shortened.
		name := itemResult.Get("name").String()
		rev := buildReverseMapFromOriginalOpenAI(originalRequestRawJSON)
		if orig, ok := rev[name]; ok {
			name = orig
		}
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.name", name)

		fullArgs := codexToolCallArguments(itemResult)
		if state.Patch {
			fullArgs = finishPatchChatArguments(state, fullArgs)
		}
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.arguments", fullArgs)
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
	// Verify this is a terminal response event.
	responseType := rootResult.Get("type").String()
	if responseType != "response.completed" && responseType != "response.incomplete" {
		return []byte{}
	}

	unixTimestamp := time.Now().Unix()

	responseResult := rootResult.Get("response")

	template := []byte(`{"id":"","object":"chat.completion","created":123456,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`)

	if tier := codexResponseServiceTier(responseResult); tier != "" {
		template, _ = sjson.SetBytes(template, "service_tier", tier)
	} else if tier := codexResponseServiceTier(rootResult); tier != "" {
		template, _ = sjson.SetBytes(template, "service_tier", tier)
	}

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
		template = setCodexCacheWriteTokens(template, usageResult)
		if reasoningTokensResult := usageResult.Get("output_tokens_details.reasoning_tokens"); reasoningTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.completion_tokens_details.reasoning_tokens", reasoningTokensResult.Int())
		}
	}

	// Process the output array for content and function calls
	var toolCalls [][]byte
	var images [][]byte
	outputResult := responseResult.Get("output")
	if outputResult.IsArray() {
		outputArray := outputResult.Array()
		var contentText string
		var reasoningText string
		var messageAnnotations [][]byte
		annotationKeys := make(map[string]struct{})
		var contentRuneOffset int64

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
				// Extract reasoning content from content
				if contentResult := outputItem.Get("content"); contentResult.IsArray() {
					contentArray := contentResult.Array()
					for _, contentItem := range contentArray {
						if contentItem.Get("type").String() == "reasoning_text" {
							if text := contentItem.Get("text").String(); text != "" {
								reasoningText += text
							}
						}
					}
				}
			case "message":
				// Extract message content and URL citations.
				if contentResult := outputItem.Get("content"); contentResult.IsArray() {
					for _, contentItem := range contentResult.Array() {
						if contentItem.Get("type").String() != "output_text" {
							continue
						}
						text := contentItem.Get("text").String()
						contentText += text
						messageAnnotations = append(messageAnnotations, buildCodexURLCitations(
							codexAnnotationResults(contentItem.Get("annotations")),
							contentRuneOffset,
							annotationKeys,
						)...)
						contentRuneOffset += int64(utf8.RuneCountInString(text))
					}
				}
			case "function_call", "custom_tool_call":
				// Handle function and custom tool call content.
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

				fullArgs := codexToolCallArguments(outputItem)
				if isOriginalCustomPatch(originalRequestRawJSON, outputItem) {
					fullArgs = applypatch.WrapInput(fullArgs)
				}
				functionCallTemplate, _ = sjson.SetBytes(functionCallTemplate, "function.arguments", fullArgs)

				toolCalls = append(toolCalls, functionCallTemplate)
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
		}

		if reasoningText != "" {
			template, _ = sjson.SetBytes(template, "choices.0.message.reasoning_content", reasoningText)
		}

		if len(messageAnnotations) > 0 {
			template, _ = sjson.SetRawBytes(template, "choices.0.message.annotations", translatorcommon.JoinRawArray(messageAnnotations))
		}

		// Add tool calls if any
		if len(toolCalls) > 0 {
			template, _ = sjson.SetRawBytes(template, "choices.0.message.tool_calls", translatorcommon.JoinRawArray(toolCalls))
		}

		// Add images if any
		if len(images) > 0 {
			template, _ = sjson.SetRawBytes(template, "choices.0.message.images", translatorcommon.JoinRawArray(images))
		}
	}

	// Extract and set the finish reason based on status.
	if statusResult := responseResult.Get("status"); statusResult.Exists() {
		status := statusResult.String()
		finishReason := ""
		nativeFinishReason := ""
		switch status {
		case "completed":
			finishReason = "stop"
			nativeFinishReason = finishReason
			if len(toolCalls) > 0 {
				finishReason = "tool_calls"
				nativeFinishReason = finishReason
			}
		case "incomplete":
			nativeFinishReason = responseResult.Get("incomplete_details.reason").String()
			switch nativeFinishReason {
			case "max_tokens", "max_output_tokens":
				finishReason = "length"
			case "content_filter":
				finishReason = "content_filter"
			default:
				finishReason = "stop"
			}
		}
		if finishReason != "" {
			template, _ = sjson.SetBytes(template, "choices.0.finish_reason", finishReason)
			template, _ = sjson.SetBytes(template, "choices.0.native_finish_reason", nativeFinishReason)
		}
	}

	return template
}

func codexAnnotationResults(value gjson.Result) []gjson.Result {
	if !value.Exists() {
		return nil
	}
	if value.IsArray() {
		results := make([]gjson.Result, 0, len(value.Array()))
		value.ForEach(func(_, item gjson.Result) bool {
			results = append(results, item)
			return true
		})
		return results
	}
	return []gjson.Result{value}
}

func codexAnnotationsFromEvent(event gjson.Result) []gjson.Result {
	annotations := make([]gjson.Result, 0)
	for _, path := range []string{"annotation", "annotations", "part.annotations"} {
		annotations = append(annotations, codexAnnotationResults(event.Get(path))...)
	}
	item := event.Get("item")
	if item.Exists() {
		annotations = append(annotations, codexAnnotationResults(item.Get("annotations"))...)
		if content := item.Get("content"); content.IsArray() {
			for _, contentItem := range content.Array() {
				annotations = append(annotations, codexAnnotationResults(contentItem.Get("annotations"))...)
			}
		}
	}
	return annotations
}

func buildCodexURLCitations(annotations []gjson.Result, runeOffset int64, seen map[string]struct{}) [][]byte {
	citations := make([][]byte, 0, len(annotations))
	for _, annotation := range annotations {
		if annotation.Get("type").String() != "url_citation" {
			continue
		}

		rawStartIndex := annotation.Get("start_index").Int()
		rawEndIndex := annotation.Get("end_index").Int()
		startIndex := rawStartIndex + runeOffset
		endIndex := rawEndIndex + runeOffset
		if startIndex < 0 || endIndex < startIndex {
			continue
		}

		url := annotation.Get("url").String()
		key := url + "\x00" + strconv.FormatInt(rawStartIndex, 10)
		if url == "" {
			key = annotation.Get("id").String() + "\x00" + strconv.FormatInt(rawStartIndex, 10)
		}
		keys := []string{key}
		if id := annotation.Get("id").String(); id != "" {
			keys = append(keys, "id\x00"+id)
		}
		if seen != nil {
			duplicate := false
			for _, key := range keys {
				if _, exists := seen[key]; exists {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			for _, key := range keys {
				seen[key] = struct{}{}
			}
		}

		citation := []byte(`{"type":"url_citation","url":"","title":"","start_index":0,"end_index":0}`)
		citation, _ = sjson.SetBytes(citation, "url", url)
		citation, _ = sjson.SetBytes(citation, "title", annotation.Get("title").String())
		citation, _ = sjson.SetBytes(citation, "start_index", startIndex)
		citation, _ = sjson.SetBytes(citation, "end_index", endIndex)
		citations = append(citations, citation)
	}
	return citations
}

func registerToolCallState(p *ConvertCliToOpenAIParams, eventResult, itemResult gjson.Result, state *toolCallStreamState) {
	if p.toolCallStates == nil {
		p.toolCallStates = make(map[string]*toolCallStreamState)
	}
	if itemID := eventResult.Get("item_id").String(); itemID != "" {
		p.toolCallStates["item:"+itemID] = state
	}
	if itemID := itemResult.Get("id").String(); itemID != "" {
		p.toolCallStates["item:"+itemID] = state
	}
	if outputIndex := eventResult.Get("output_index"); outputIndex.Exists() {
		p.toolCallStates["output:"+outputIndex.Raw] = state
	}
	p.currentToolCall = state
}

func findToolCallState(p *ConvertCliToOpenAIParams, eventResult, itemResult gjson.Result) *toolCallStreamState {
	if itemID := eventResult.Get("item_id").String(); itemID != "" {
		if state := p.toolCallStates["item:"+itemID]; state != nil {
			return state
		}
	}
	if itemID := itemResult.Get("id").String(); itemID != "" {
		if state := p.toolCallStates["item:"+itemID]; state != nil {
			return state
		}
	}
	if outputIndex := eventResult.Get("output_index"); outputIndex.Exists() {
		if state := p.toolCallStates["output:"+outputIndex.Raw]; state != nil {
			return state
		}
	}
	return p.currentToolCall
}

func isCodexToolCallType(itemType string) bool {
	return itemType == "function_call" || itemType == "custom_tool_call"
}

func codexToolCallArguments(itemResult gjson.Result) string {
	if itemResult.Get("type").String() == "custom_tool_call" {
		return itemResult.Get("input").String()
	}
	return itemResult.Get("arguments").String()
}

// buildReverseMapFromOriginalOpenAI builds a map of shortened tool name -> original tool name
// from the original OpenAI-style request JSON using the same shortening logic.
func buildReverseMapFromOriginalOpenAI(original []byte) map[string]string {
	rev := map[string]string{}
	names := collectRequestToolNames(original)
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

// codexResponseServiceTier returns only an actual nonempty upstream tier.
func codexResponseServiceTier(response gjson.Result) string {
	tier := response.Get("service_tier")
	if tier.Type != gjson.String || strings.TrimSpace(tier.Str) == "" {
		return ""
	}
	return strings.TrimSpace(tier.Str)
}

// setCodexCacheWriteTokens preserves the upstream integer without float conversion.
func setCodexCacheWriteTokens(template []byte, usage gjson.Result) []byte {
	value := usage.Get("input_tokens_details.cache_write_tokens")
	if !value.Exists() || value.Type == gjson.Null {
		return template
	}
	valid := value.Type == gjson.Number && value.Raw != ""
	for _, digit := range value.Raw {
		if digit < '0' || digit > '9' {
			valid = false
			break
		}
	}
	if !valid {
		log.WithField("field", "usage.input_tokens_details.cache_write_tokens").Warn("Ignoring invalid Codex cache write token count")
		return template
	}
	template, _ = sjson.SetRawBytes(template, "usage.prompt_tokens_details.cache_write_tokens", []byte(value.Raw))
	template, _ = sjson.SetRawBytes(template, "usage.prompt_tokens_details.cached_creation_tokens", []byte(value.Raw))
	return template
}

// isOriginalCustomPatch never promotes an ordinary same-name function to custom.
func isOriginalCustomPatch(original []byte, item gjson.Result) bool {
	if item.Get("type").String() != "custom_tool_call" {
		return false
	}
	name := util.QualifyResponsesNamespaceToolName(item.Get("namespace").String(), item.Get("name").String())
	// Chat Completions prefers ordinary functions for ambiguous names, regardless of order.
	for _, tool := range gjson.GetBytes(original, "tools").Array() {
		if tool.Get("type").String() == "function" && tool.Get("function.name").String() == name {
			return false
		}
	}
	winner, ok := util.CollectResponsesToolWinners(gjson.ParseBytes(original))[name]
	return ok && applypatch.IsCustomTool(winner.Tool)
}

func finishPatchChatArguments(state *toolCallStreamState, input string) string {
	if state.InputClosed {
		return ""
	}
	state.InputClosed = true
	if state.InputStarted {
		return `"}`
	}
	return applypatch.WrapInput(input)
}
