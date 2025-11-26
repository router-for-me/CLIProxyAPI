package chat_completions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	. "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/openai/chat-completions"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// convertCliResponseToOpenAIChatParams holds parameters for response conversion.
type convertCliResponseToOpenAIChatParams struct {
	UnixTimestamp int64
	FunctionIndex map[int]int
}

// ConvertAntigravityResponseToOpenAI translates a single chunk of a streaming response from the
// Gemini CLI API format to the OpenAI Chat Completions streaming format.
// It processes various Gemini CLI event types and transforms them into OpenAI-compatible JSON responses.
// The function handles text content, tool calls, reasoning content, and usage metadata, outputting
// responses that match the OpenAI API format. It supports incremental updates for streaming responses
// and handles multiple candidates updates if present (although Antigravity/Gemini CLI usually stream single candidates).
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Gemini CLI API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - []string: A slice of strings, each containing an OpenAI-compatible JSON response
func ConvertAntigravityResponseToOpenAI(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	if *param == nil {
		*param = &convertCliResponseToOpenAIChatParams{
			UnixTimestamp: 0,
			FunctionIndex: make(map[int]int),
		}
	}

	// Safety check for param state structure updates
	p := (*param).(*convertCliResponseToOpenAIChatParams)
	if p.FunctionIndex == nil {
		p.FunctionIndex = make(map[int]int)
	}

	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		return []string{}
	}

	// Initialize the OpenAI SSE template.
	template := `{"id":"","object":"chat.completion.chunk","created":12345,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`

	// Extract and set the model version.
	if modelVersionResult := gjson.GetBytes(rawJSON, "response.modelVersion"); modelVersionResult.Exists() {
		template, _ = sjson.Set(template, "model", modelVersionResult.String())
	}

	// Extract and set the creation timestamp.
	if createTimeResult := gjson.GetBytes(rawJSON, "response.createTime"); createTimeResult.Exists() {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		if err == nil {
			p.UnixTimestamp = t.Unix()
		}
		template, _ = sjson.Set(template, "created", p.UnixTimestamp)
	} else {
		template, _ = sjson.Set(template, "created", p.UnixTimestamp)
	}

	// Extract and set the response ID.
	if responseIDResult := gjson.GetBytes(rawJSON, "response.responseId"); responseIDResult.Exists() {
		template, _ = sjson.Set(template, "id", responseIDResult.String())
	}

	var results []string

	// Process candidates
	candidates := gjson.GetBytes(rawJSON, "response.candidates")
	if candidates.IsArray() {
		candidates.ForEach(func(_, candidate gjson.Result) bool {
			idx := int(candidate.Get("index").Int())
			choiceTemplate := template
			choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.index", idx)

			// Extract and set the finish reason.
			if finishReasonResult := candidate.Get("finishReason"); finishReasonResult.Exists() {
				choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.finish_reason", finishReasonResult.String())
				choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.native_finish_reason", finishReasonResult.String())
			}

			// Process the content parts of this candidate
			partsResult := candidate.Get("content.parts")
			hasFunctionCall := false

			if partsResult.IsArray() {
				partResults := partsResult.Array()
				for i := 0; i < len(partResults); i++ {
					partResult := partResults[i]
					partTextResult := partResult.Get("text")
					functionCallResult := partResult.Get("functionCall")
					thoughtSignatureResult := partResult.Get("thoughtSignature")
					inlineDataResult := partResult.Get("inlineData")
					if !inlineDataResult.Exists() {
						inlineDataResult = partResult.Get("inline_data")
					}

					// Handle thoughtSignature - this is encrypted reasoning content that should not be exposed to the client
					if thoughtSignatureResult.Exists() && thoughtSignatureResult.String() != "" {
						// Skip thoughtSignature processing - it's internal encrypted data
						continue
					}

					if partTextResult.Exists() {
						textContent := partTextResult.String()

						// Handle text content, distinguishing between regular content and reasoning/thoughts.
						if partResult.Get("thought").Bool() {
							choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.delta.reasoning_content", textContent)
						} else {
							choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.delta.content", textContent)
						}
						choiceTemplate, _ = sjson.Set(choiceTemplate, "choices.0.delta.role", "assistant")
					} else if functionCallResult.Exists() {
						// Handle function call content.
						hasFunctionCall = true
						toolCallsResult := gjson.Get(choiceTemplate, "choices.0.delta.tool_calls")

						functionIndex := p.FunctionIndex[idx]
						p.FunctionIndex[idx]++

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
	if usageResult := gjson.GetBytes(rawJSON, "response.usageMetadata"); usageResult.Exists() {
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

// ConvertAntigravityResponseToOpenAINonStream converts a non-streaming Gemini CLI response to a non-streaming OpenAI response.
// This function processes the complete Gemini CLI response and transforms it into a single OpenAI-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the OpenAI API format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Gemini CLI API
//   - param: A pointer to a parameter object for the conversion
//
// Returns:
//   - string: An OpenAI-compatible JSON response containing all message content and metadata
func ConvertAntigravityResponseToOpenAINonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) string {
	responseResult := gjson.GetBytes(rawJSON, "response")
	if responseResult.Exists() {
		return ConvertGeminiResponseToOpenAINonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, []byte(responseResult.Raw), param)
	}
	return ""
}
