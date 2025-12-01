// Package openai provides response translation functionality for Gemini to OpenAI API compatibility.
// This package handles the conversion of Gemini API responses into OpenAI Chat Completions-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by OpenAI API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, reasoning content, and usage metadata appropriately.
package chat_completions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// convertGeminiResponseToOpenAIChatParams holds parameters for response conversion.
type convertGeminiResponseToOpenAIChatParams struct {
	UnixTimestamp int64
	FunctionIndex int
}

// ConvertGeminiResponseToOpenAI translates a single chunk of a streaming response from the
// Gemini API format to the OpenAI Chat Completions streaming format.
// It processes various Gemini event types and transforms them into OpenAI-compatible JSON responses.
// The function handles text content, tool calls, reasoning content, and usage metadata, outputting
// responses that match the OpenAI API format. It supports incremental updates for streaming responses.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Gemini API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - []string: A slice of strings, each containing an OpenAI-compatible JSON response
func ConvertGeminiResponseToOpenAI(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	if *param == nil {
		*param = &convertGeminiResponseToOpenAIChatParams{
			UnixTimestamp: 0,
			FunctionIndex: 0,
		}
	}

	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}

	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		return []string{}
	}

	// Initialize the OpenAI SSE template.
	template := `{"id":"","object":"chat.completion.chunk","created":12345,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`

	// Extract and set the model version.
	if modelVersionResult := gjson.GetBytes(rawJSON, "modelVersion"); modelVersionResult.Exists() {
		template, _ = sjson.Set(template, "model", modelVersionResult.String())
	}

	// Extract and set the creation timestamp.
	if createTimeResult := gjson.GetBytes(rawJSON, "createTime"); createTimeResult.Exists() {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		if err == nil {
			(*param).(*convertGeminiResponseToOpenAIChatParams).UnixTimestamp = t.Unix()
		}
		template, _ = sjson.Set(template, "created", (*param).(*convertGeminiResponseToOpenAIChatParams).UnixTimestamp)
	} else {
		template, _ = sjson.Set(template, "created", (*param).(*convertGeminiResponseToOpenAIChatParams).UnixTimestamp)
	}

	// Extract and set the response ID.
	if responseIDResult := gjson.GetBytes(rawJSON, "responseId"); responseIDResult.Exists() {
		template, _ = sjson.Set(template, "id", responseIDResult.String())
	}

	// Extract and set the finish reason.
	if finishReasonResult := gjson.GetBytes(rawJSON, "candidates.0.finishReason"); finishReasonResult.Exists() {
		template, _ = sjson.Set(template, "choices.0.finish_reason", finishReasonResult.String())
		template, _ = sjson.Set(template, "choices.0.native_finish_reason", finishReasonResult.String())
	}

	// Extract and set usage metadata (token counts).
	if usageResult := gjson.GetBytes(rawJSON, "usageMetadata"); usageResult.Exists() {
		if candidatesTokenCountResult := usageResult.Get("candidatesTokenCount"); candidatesTokenCountResult.Exists() {
			template, _ = sjson.Set(template, "usage.completion_tokens", candidatesTokenCountResult.Int())
		}
		if totalTokenCountResult := usageResult.Get("totalTokenCount"); totalTokenCountResult.Exists() {
			template, _ = sjson.Set(template, "usage.total_tokens", totalTokenCountResult.Int())
		}
		promptTokenCount := usageResult.Get("promptTokenCount").Int()
		thoughtsTokenCount := usageResult.Get("thoughtsTokenCount").Int()
		template, _ = sjson.Set(template, "usage.prompt_tokens", promptTokenCount+thoughtsTokenCount)
		if thoughtsTokenCount > 0 {
			template, _ = sjson.Set(template, "usage.completion_tokens_details.reasoning_tokens", thoughtsTokenCount)
		}
	}

	// Process the main content part of the response.
	partsResult := gjson.GetBytes(rawJSON, "candidates.0.content.parts")
	hasFunctionCall := false
	if partsResult.IsArray() {
		partResults := partsResult.Array()
		for i := 0; i < len(partResults); i++ {
			partResult := partResults[i]
			partTextResult := partResult.Get("text")
			functionCallResult := partResult.Get("functionCall")
			inlineDataResult := partResult.Get("inlineData")
			if !inlineDataResult.Exists() {
				inlineDataResult = partResult.Get("inline_data")
			}
			thoughtSignatureResult := partResult.Get("thoughtSignature")
			if !thoughtSignatureResult.Exists() {
				thoughtSignatureResult = partResult.Get("thought_signature")
			}

			hasThoughtSignature := thoughtSignatureResult.Exists() && thoughtSignatureResult.String() != ""
			hasContentPayload := partTextResult.Exists() || functionCallResult.Exists() || inlineDataResult.Exists()

			// Skip pure thoughtSignature parts but keep any actual payload in the same part.
			if hasThoughtSignature && !hasContentPayload {
				continue
			}

			if partTextResult.Exists() {
				text := partTextResult.String()
				// Handle text content, distinguishing between regular content and reasoning/thoughts.
				if partResult.Get("thought").Bool() {
					template, _ = sjson.Set(template, "choices.0.delta.reasoning_content", text)
				} else {
					template, _ = sjson.Set(template, "choices.0.delta.content", text)
				}
				template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")
			} else if functionCallResult.Exists() {
				// Handle function call content.
				hasFunctionCall = true
				toolCallsResult := gjson.Get(template, "choices.0.delta.tool_calls")
				functionCallIndex := (*param).(*convertGeminiResponseToOpenAIChatParams).FunctionIndex
				(*param).(*convertGeminiResponseToOpenAIChatParams).FunctionIndex++
				if toolCallsResult.Exists() && toolCallsResult.IsArray() {
					functionCallIndex = len(toolCallsResult.Array())
				} else {
					template, _ = sjson.SetRaw(template, "choices.0.delta.tool_calls", `[]`)
				}

				functionCallTemplate := `{"id": "","index": 0,"type": "function","function": {"name": "","arguments": ""}}`
				fcName := functionCallResult.Get("name").String()
				functionCallTemplate, _ = sjson.Set(functionCallTemplate, "id", fmt.Sprintf("%s-%d", fcName, time.Now().UnixNano()))
				functionCallTemplate, _ = sjson.Set(functionCallTemplate, "index", functionCallIndex)
				functionCallTemplate, _ = sjson.Set(functionCallTemplate, "function.name", fcName)
				if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
					functionCallTemplate, _ = sjson.Set(functionCallTemplate, "function.arguments", fcArgsResult.Raw)
				}
				template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")
				template, _ = sjson.SetRaw(template, "choices.0.delta.tool_calls.-1", functionCallTemplate)
			} else if inlineDataResult.Exists() {
				data := inlineDataResult.Get("data").String()
				if data == "" {
					continue
				}
				mimeType := inlineDataResult.Get("mimeType").String()
				if mimeType == "" {
					mimeType = inlineDataResult.Get("mime_type").String()
				}
				if mimeType == "" {
					mimeType = "image/png"
				}
				imageURL := fmt.Sprintf("data:%s;base64,%s", mimeType, data)
				imagePayload, err := json.Marshal(map[string]any{
					"type": "image_url",
					"image_url": map[string]string{
						"url": imageURL,
					},
				})
				if err != nil {
					continue
				}
				imagesResult := gjson.Get(template, "choices.0.delta.images")
				if !imagesResult.Exists() || !imagesResult.IsArray() {
					template, _ = sjson.SetRaw(template, "choices.0.delta.images", `[]`)
				}
				template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")
				template, _ = sjson.SetRaw(template, "choices.0.delta.images.-1", string(imagePayload))
			}
		}
	}

	if hasFunctionCall {
		template, _ = sjson.Set(template, "choices.0.finish_reason", "tool_calls")
		template, _ = sjson.Set(template, "choices.0.native_finish_reason", "tool_calls")
	}

	return []string{template}
}

// ConvertGeminiResponseToOpenAINonStream converts a non-streaming Gemini response to a non-streaming OpenAI response.
// This function processes the complete Gemini response and transforms it into a single OpenAI-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the OpenAI API format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Gemini API
//   - param: A pointer to a parameter object for the conversion (unused in current implementation)
//
// Returns:
//   - string: An OpenAI-compatible JSON response containing all message content and metadata
func ConvertGeminiResponseToOpenAINonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) string {
	var unixTimestamp int64
	template := `{"id":"","object":"chat.completion","created":123456,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`
	if modelVersionResult := gjson.GetBytes(rawJSON, "modelVersion"); modelVersionResult.Exists() {
		template, _ = sjson.Set(template, "model", modelVersionResult.String())
	}

	if createTimeResult := gjson.GetBytes(rawJSON, "createTime"); createTimeResult.Exists() {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		if err == nil {
			unixTimestamp = t.Unix()
		}
		template, _ = sjson.Set(template, "created", unixTimestamp)
	} else {
		template, _ = sjson.Set(template, "created", unixTimestamp)
	}

	if responseIDResult := gjson.GetBytes(rawJSON, "responseId"); responseIDResult.Exists() {
		template, _ = sjson.Set(template, "id", responseIDResult.String())
	}

	if finishReasonResult := gjson.GetBytes(rawJSON, "candidates.0.finishReason"); finishReasonResult.Exists() {
		template, _ = sjson.Set(template, "choices.0.finish_reason", finishReasonResult.String())
		template, _ = sjson.Set(template, "choices.0.native_finish_reason", finishReasonResult.String())
	}

	if usageResult := gjson.GetBytes(rawJSON, "usageMetadata"); usageResult.Exists() {
		if candidatesTokenCountResult := usageResult.Get("candidatesTokenCount"); candidatesTokenCountResult.Exists() {
			template, _ = sjson.Set(template, "usage.completion_tokens", candidatesTokenCountResult.Int())
		}
		if totalTokenCountResult := usageResult.Get("totalTokenCount"); totalTokenCountResult.Exists() {
			template, _ = sjson.Set(template, "usage.total_tokens", totalTokenCountResult.Int())
		}
		promptTokenCount := usageResult.Get("promptTokenCount").Int()
		thoughtsTokenCount := usageResult.Get("thoughtsTokenCount").Int()
		template, _ = sjson.Set(template, "usage.prompt_tokens", promptTokenCount+thoughtsTokenCount)
		if thoughtsTokenCount > 0 {
			template, _ = sjson.Set(template, "usage.completion_tokens_details.reasoning_tokens", thoughtsTokenCount)
		}
	}

	// Process the main content part of the response.
	partsResult := gjson.GetBytes(rawJSON, "candidates.0.content.parts")
	hasFunctionCall := false
	if partsResult.IsArray() {
		partsResults := partsResult.Array()
		for i := 0; i < len(partsResults); i++ {
			partResult := partsResults[i]
			partTextResult := partResult.Get("text")
			functionCallResult := partResult.Get("functionCall")
			inlineDataResult := partResult.Get("inlineData")
			if !inlineDataResult.Exists() {
				inlineDataResult = partResult.Get("inline_data")
			}

			if partTextResult.Exists() {
				// Append text content, distinguishing between regular content and reasoning.
				if partResult.Get("thought").Bool() {
					template, _ = sjson.Set(template, "choices.0.message.reasoning_content", partTextResult.String())
				} else {
					template, _ = sjson.Set(template, "choices.0.message.content", partTextResult.String())
				}
				template, _ = sjson.Set(template, "choices.0.message.role", "assistant")
			} else if functionCallResult.Exists() {
				// Append function call content to the tool_calls array.
				hasFunctionCall = true
				toolCallsResult := gjson.Get(template, "choices.0.message.tool_calls")
				if !toolCallsResult.Exists() || !toolCallsResult.IsArray() {
					template, _ = sjson.SetRaw(template, "choices.0.message.tool_calls", `[]`)
				}
				functionCallItemTemplate := `{"id": "","type": "function","function": {"name": "","arguments": ""}}`
				fcName := functionCallResult.Get("name").String()
				functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "id", fmt.Sprintf("%s-%d", fcName, time.Now().UnixNano()))
				functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "function.name", fcName)
				if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
					functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "function.arguments", fcArgsResult.Raw)
				}
				template, _ = sjson.Set(template, "choices.0.message.role", "assistant")
				template, _ = sjson.SetRaw(template, "choices.0.message.tool_calls.-1", functionCallItemTemplate)
			} else if inlineDataResult.Exists() {
				data := inlineDataResult.Get("data").String()
				if data == "" {
					continue
				}
				mimeType := inlineDataResult.Get("mimeType").String()
				if mimeType == "" {
					mimeType = inlineDataResult.Get("mime_type").String()
				}
				if mimeType == "" {
					mimeType = "image/png"
				}
				imageURL := fmt.Sprintf("data:%s;base64,%s", mimeType, data)
				imagePayload, err := json.Marshal(map[string]any{
					"type": "image_url",
					"image_url": map[string]string{
						"url": imageURL,
					},
				})
				if err != nil {
					continue
				}
				imagesResult := gjson.Get(template, "choices.0.message.images")
				if !imagesResult.Exists() || !imagesResult.IsArray() {
					template, _ = sjson.SetRaw(template, "choices.0.message.images", `[]`)
				}
				template, _ = sjson.Set(template, "choices.0.message.role", "assistant")
				template, _ = sjson.SetRaw(template, "choices.0.message.images.-1", string(imagePayload))
			}
		}
	}

	if hasFunctionCall {
		template, _ = sjson.Set(template, "choices.0.finish_reason", "tool_calls")
		template, _ = sjson.Set(template, "choices.0.native_finish_reason", "tool_calls")
	}

	return template
}
