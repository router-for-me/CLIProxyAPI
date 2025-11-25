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
	// FunctionIndex maps a candidate index to its current function call index
	// needed because different candidates generate function calls independently in streams.
	FunctionIndex map[int]int
}

// ConvertGeminiResponseToOpenAI translates a single chunk of a streaming response from the
// Gemini API format to the OpenAI Chat Completions streaming format.
// It processes various Gemini event types and transforms them into OpenAI-compatible JSON responses.
// The function handles text content, tool calls, reasoning content, and usage metadata, outputting
// responses that match the OpenAI API format. It supports incremental updates for streaming responses
// and handles multiple candidates (n>1).
//
// Parameters:
//   - ctx: The context for the request
//   - modelName: The name of the model being used
//   - originalRequestRawJSON: The original request body
//   - requestRawJSON: The processed request body
//   - rawJSON: The raw JSON response from the Gemini API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - []string: A slice of strings, each containing an OpenAI-compatible JSON response chunk
func ConvertGeminiResponseToOpenAI(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	// Initialize param if nil
	if *param == nil {
		*param = &convertGeminiResponseToOpenAIChatParams{
			UnixTimestamp: 0,
			FunctionIndex: make(map[int]int),
		}
	}

	// Ensure param structure is valid (handle cases where param might exist but map is nil due to version changes)
	p := (*param).(*convertGeminiResponseToOpenAIChatParams)
	if p.FunctionIndex == nil {
		p.FunctionIndex = make(map[int]int)
	}

	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}

	// Gemini streaming end signal
	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		return []string{}
	}

	root := gjson.ParseBytes(rawJSON)

	// Base OpenAI SSE template. Note: We use a generic choices array structure here.
	// When streaming, we typically emit one choice per chunk to update that specific index.
	template := `{"id":"","object":"chat.completion.chunk","created":12345,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`

	// Extract and set the model version.
	if modelVersionResult := root.Get("modelVersion"); modelVersionResult.Exists() {
		template, _ = sjson.Set(template, "model", modelVersionResult.String())
	}

	// Extract and set the creation timestamp.
	if createTimeResult := root.Get("createTime"); createTimeResult.Exists() {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		if err == nil {
			p.UnixTimestamp = t.Unix()
		}
		template, _ = sjson.Set(template, "created", p.UnixTimestamp)
	} else {
		template, _ = sjson.Set(template, "created", p.UnixTimestamp)
	}

	// Extract and set the response ID.
	if responseIDResult := root.Get("responseId"); responseIDResult.Exists() {
		template, _ = sjson.Set(template, "id", responseIDResult.String())
	}

	var results []string

	// Process all candidates.
	// Gemini may return updates for multiple candidates in a single chunk (if n>1).
	// OpenAI expects these as separate `data: ...` chunks or within the choices array.
	// Generating separate chunks for each candidate update is the safest approach for SSE clients.
	candidates := root.Get("candidates")
	if candidates.IsArray() {
		candidates.ForEach(func(_, candidate gjson.Result) bool {
			// Determine the index of this candidate
			idx := int(candidate.Get("index").Int())
			
			// Create a clean template for this specific candidate update
			choiceTemplate := template
			choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.index", idx)

			// Extract and set the finish reason.
			if finishReasonResult := candidate.Get("finishReason"); finishReasonResult.Exists() {
				choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.finish_reason", mapGeminiFinishReasonToOpenAI(finishReasonResult.String()))
				choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.native_finish_reason", finishReasonResult.String())
			}

			// Process the content parts of this candidate.
			partsResult := candidate.Get("content.parts")
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

					if partTextResult.Exists() {
						// Handle text content, distinguishing between regular content and reasoning/thoughts.
						if partResult.Get("thought").Bool() {
							choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.delta.reasoning_content", partTextResult.String())
						} else {
							choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.delta.content", partTextResult.String())
						}
						// Always explicitly set role for delta in the first chunk or when switching context, 
						// but setting it every time is safe for OpenAI clients.
						choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.delta.role", "assistant")
					} else if functionCallResult.Exists() {
						// Handle function call content.
						hasFunctionCall = true
						toolCallsResult := gjson.Get(choiceTemplate, "choices.0.delta.tool_calls")

						// Get current function index for THIS candidate
						functionIndex := p.FunctionIndex[idx]
						p.FunctionIndex[idx]++

						// If tool_calls array already started in this chunk (unlikely in Gemini stream but possible), use length
						if toolCallsResult.Exists() && toolCallsResult.IsArray() {
							functionIndex = len(toolCallsResult.Array())
						} else {
							choiceTemplate, _ = sjson.SetRaw(choiceTemplate, "choices.0.delta.tool_calls", `[]`)
						}

						functionCallItemTemplate := `{"id": "","index": 0,"type": "function","function": {"name": "","arguments": ""}}`
						fcName := functionCallResult.Get("name").String()
						functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "id", fmt.Sprintf("%s-%d", fcName, time.Now().UnixNano()))
						functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "index", functionIndex)
						functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "function.name", fcName)
						if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
							functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "function.arguments", fcArgsResult.Raw)
						}
						choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.delta.role", "assistant")
						choiceTemplate, _ = sjson.SetRaw(choiceTemplate, "choices.0.delta.tool_calls.-1", functionCallItemTemplate)
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
						imagesResult := gjson.Get(choiceTemplate, "choices.0.delta.images")
						if !imagesResult.Exists() || !imagesResult.IsArray() {
							choiceTemplate, _ = sjson.SetRaw(choiceTemplate, "choices.0.delta.images", `[]`)
						}
						choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.delta.role", "assistant")
						choiceTemplate, _ = sjson.SetRaw(choiceTemplate, "choices.0.delta.images.-1", string(imagePayload))
					}
				}
			}

			if hasFunctionCall {
				choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.finish_reason", "tool_calls")
				choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.native_finish_reason", "tool_calls")
			}

			results = append(results, choiceTemplate)
			return true
		})
	}

	// Extract and set usage metadata (token counts).
	// In streaming, usage is typically sent as a separate chunk at the end with an empty choices array (if stream_options.include_usage is set).
	// We emit it if Gemini provides it.
	if usageResult := root.Get("usageMetadata"); usageResult.Exists() {
		usageTemplate := template
		// Ensure choices is empty for the usage chunk
		usageTemplate, _ = sjson.Set(usageTemplate, "choices", []interface{}{}) 

		if candidatesTokenCountResult := usageResult.Get("candidatesTokenCount"); candidatesTokenCountResult.Exists() {
			usageTemplate, _ = sjson.Set(usageTemplate, "usage.completion_tokens", candidatesTokenCountResult.Int())
		}
		if totalTokenCountResult := usageResult.Get("totalTokenCount"); totalTokenCountResult.Exists() {
			usageTemplate, _ = sjson.Set(usageTemplate, "usage.total_tokens", totalTokenCountResult.Int())
		}
		promptTokenCount := usageResult.Get("promptTokenCount").Int()
		thoughtsTokenCount := usageResult.Get("thoughtsTokenCount").Int()
		usageTemplate, _ = sjson.Set(usageTemplate, "usage.prompt_tokens", promptTokenCount+thoughtsTokenCount)
		if thoughtsTokenCount > 0 {
			usageTemplate, _ = sjson.Set(usageTemplate, "usage.completion_tokens_details.reasoning_tokens", thoughtsTokenCount)
		}

		results = append(results, usageTemplate)
	}

	return results
}

// ConvertGeminiResponseToOpenAINonStream converts a non-streaming Gemini response to a non-streaming OpenAI response.
// This function processes the complete Gemini response and transforms it into a single OpenAI-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the OpenAI API format.
//
// Parameters:
//   - ctx: The context for the request
//   - modelName: The name of the model being used
//   - originalRequestRawJSON: The original request body
//   - requestRawJSON: The processed request body
//   - rawJSON: The raw JSON response from the Gemini API
//   - param: A pointer to a parameter object
//
// Returns:
//   - string: An OpenAI-compatible JSON response containing all message content and metadata
func ConvertGeminiResponseToOpenAINonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) string {
	var unixTimestamp int64
	root := gjson.ParseBytes(rawJSON)

	// Base OpenAI response template with empty choices
	out := `{"id":"","object":"chat.completion","created":123456,"model":"model","choices":[]}`

	if modelVersionResult := root.Get("modelVersion"); modelVersionResult.Exists() {
		out, _ = sjson.Set(out, "model", modelVersionResult.String())
	}

	if createTimeResult := root.Get("createTime"); createTimeResult.Exists() {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		if err == nil {
			unixTimestamp = t.Unix()
		}
		out, _ = sjson.Set(out, "created", unixTimestamp)
	} else {
		out, _ = sjson.Set(out, "created", unixTimestamp)
	}

	if responseIDResult := root.Get("responseId"); responseIDResult.Exists() {
		out, _ = sjson.Set(out, "id", responseIDResult.String())
	}

	// Process all candidates
	candidates := root.Get("candidates")
	if candidates.IsArray() {
		candidates.ForEach(func(_, candidate gjson.Result) bool {
			choiceIdx := int(candidate.Get("index").Int())

			// Construct choice object structure
			// Default content is nil so it marshals to null if not set, 
			// though usually we want empty string if there's no content but there are tools.
			choice := map[string]interface{}{
				"index": choiceIdx,
				"message": map[string]interface{}{
					"role": "assistant",
				},
				"finish_reason": nil,
			}

			// Override role if explicitly provided by Gemini (rarely needed)
			if role := candidate.Get("content.role"); role.Exists() && role.String() != "model" {
				choice["message"].(map[string]interface{})["role"] = "assistant"
			}

			var toolCalls []interface{}
			var hasText bool

			// Process the main content parts of the candidate.
			partsResult := candidate.Get("content.parts")
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

					if partTextResult.Exists() {
						hasText = true
						// Append text content, distinguishing between regular content and reasoning.
						msgMap := choice["message"].(map[string]interface{})
						if partResult.Get("thought").Bool() {
							// Reasoning content
							currentReasoning, _ := msgMap["reasoning_content"].(string)
							msgMap["reasoning_content"] = currentReasoning + partTextResult.String()
						} else {
							// Standard content
							currentContent, _ := msgMap["content"].(string)
							msgMap["content"] = currentContent + partTextResult.String()
						}
					} else if functionCallResult.Exists() {
						// Append function call content to the tool_calls array.
						functionCallItem := map[string]interface{}{
							"id":   "",
							"type": "function",
							"function": map[string]interface{}{
								"name":      "",
								"arguments": "",
							},
						}
						fcName := functionCallResult.Get("name").String()
						functionCallItem["id"] = fmt.Sprintf("%s-%d", fcName, time.Now().UnixNano())
						functionCallItem["function"].(map[string]interface{})["name"] = fcName
						if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
							functionCallItem["function"].(map[string]interface{})["arguments"] = fcArgsResult.Raw
						}
						toolCalls = append(toolCalls, functionCallItem)
					} else if inlineDataResult.Exists() {
						// Handle inline images
						data := inlineDataResult.Get("data").String()
						if data != "" {
							mimeType := inlineDataResult.Get("mimeType").String()
							if mimeType == "" {
								mimeType = inlineDataResult.Get("mime_type").String()
							}
							if mimeType == "" {
								mimeType = "image/png"
							}
							imageURL := fmt.Sprintf("data:%s;base64,%s", mimeType, data)
							
							// Gemini images in response are rare but supported in protocol.
							// We need to check if 'images' array exists in message or if we should treat it as part of content array (multimodal).
							// Standard OpenAI text response usually implies 'content' is string. 
							// If we receive image output, we add it to a non-standard 'images' field for now, 
							// or we could reconstruct 'content' as array of objects if desired. 
							// Keeping consistent with streaming implementation:
							images, _ := choice["message"].(map[string]interface{})["images"].([]interface{})
							imageItem := map[string]interface{}{
								"type": "image_url",
								"image_url": map[string]string{
									"url": imageURL,
								},
							}
							choice["message"].(map[string]interface{})["images"] = append(images, imageItem)
						}
					}
				}
			}

			// Ensure content is at least empty string if it was touched, or nil if only tool calls
			if hasText && choice["message"].(map[string]interface{})["content"] == nil {
				choice["message"].(map[string]interface{})["content"] = ""
			}

			if len(toolCalls) > 0 {
				choice["message"].(map[string]interface{})["tool_calls"] = toolCalls
			}

			// Handle finish reason
			if finishReason := candidate.Get("finishReason"); finishReason.Exists() {
				choice["finish_reason"] = mapGeminiFinishReasonToOpenAI(finishReason.String())
				choice["native_finish_reason"] = finishReason.String()
			}

			if len(toolCalls) > 0 {
				choice["finish_reason"] = "tool_calls"
			}

			// Append constructed choice to the choices array in 'out'
			// We marshal the individual choice object and append it.
			choiceJson, err := json.Marshal(choice)
			if err == nil {
				out, _ = sjson.SetRaw(out, "choices.-1", string(choiceJson))
			}
			
			return true
		})
	}

	// Handle usage information
	if usage := root.Get("usageMetadata"); usage.Exists() {
		usageObj := map[string]interface{}{
			"prompt_tokens":     usage.Get("promptTokenCount").Int(),
			"completion_tokens": usage.Get("candidatesTokenCount").Int(),
			"total_tokens":      usage.Get("totalTokenCount").Int(),
		}
		
		thoughtsTokenCount := usage.Get("thoughtsTokenCount").Int()
		
		// If promptTokenCount doesn't implicitly include thoughts, you might want to sum them. 
		// Usually Gemini reports promptTokenCount separately.
		// However, in OpenAI reasoning models, reasoning tokens are output tokens.
		// Your original logic summed prompt + thoughts for prompt_tokens.
		promptTokenCount := usage.Get("promptTokenCount").Int()
		usageObj["prompt_tokens"] = promptTokenCount + thoughtsTokenCount

		if thoughtsTokenCount > 0 {
			usageObj["completion_tokens_details"] = map[string]interface{}{
				"reasoning_tokens": thoughtsTokenCount,
			}
		}
		out, _ = sjson.Set(out, "usage", usageObj)
	}

	return out
}

// mapGeminiFinishReasonToOpenAI maps Gemini finish reasons to OpenAI format.
func mapGeminiFinishReasonToOpenAI(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	case "RECITATION":
		return "content_filter"
	default:
		return "stop"
	}
}
