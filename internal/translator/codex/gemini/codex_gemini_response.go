// Package gemini provides response translation functionality for Codex to Gemini API compatibility.
// This package handles the conversion of Codex API responses into Gemini-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Gemini API clients.
package gemini

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	dataTag = []byte("data:")
)

// ConvertCodexResponseToGeminiParams holds parameters for response conversion.
type ConvertCodexResponseToGeminiParams struct {
	Model             string
	CreatedAt         int64
	ResponseID        string
	LastStorageOutput string
}

// ConvertCodexResponseToGemini converts Codex streaming response format to Gemini format.
// This function processes various Codex event types and transforms them into Gemini-compatible JSON responses.
// It handles text content, tool calls, and usage metadata, outputting responses that match the Gemini API format.
// The function maintains state across multiple calls to ensure proper response sequencing.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Codex API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - []string: A slice of strings, each containing a Gemini-compatible JSON response
func ConvertCodexResponseToGemini(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	if *param == nil {
		*param = &ConvertCodexResponseToGeminiParams{
			Model:             modelName,
			CreatedAt:         0,
			ResponseID:        "",
			LastStorageOutput: "",
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return []string{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	rootResult := gjson.ParseBytes(rawJSON)
	typeResult := rootResult.Get("type")
	typeStr := typeResult.String()

	// Base Gemini response template
	template := `{"candidates":[{"content":{"role":"model","parts":[]}}],"usageMetadata":{"trafficType":"PROVISIONED_THROUGHPUT"},"modelVersion":"gemini-2.5-pro","createTime":"2025-08-15T02:52:03.884209Z","responseId":"06CeaPH7NaCU48APvNXDyA4"}`
	if (*param).(*ConvertCodexResponseToGeminiParams).LastStorageOutput != "" && typeStr == "response.output_item.done" {
		template = (*param).(*ConvertCodexResponseToGeminiParams).LastStorageOutput
	} else {
		template, _ = sjson.Set(template, "modelVersion", (*param).(*ConvertCodexResponseToGeminiParams).Model)
		createdAtResult := rootResult.Get("response.created_at")
		if createdAtResult.Exists() {
			(*param).(*ConvertCodexResponseToGeminiParams).CreatedAt = createdAtResult.Int()
			template, _ = sjson.Set(template, "createTime", time.Unix((*param).(*ConvertCodexResponseToGeminiParams).CreatedAt, 0).Format(time.RFC3339Nano))
		}
		template, _ = sjson.Set(template, "responseId", (*param).(*ConvertCodexResponseToGeminiParams).ResponseID)
	}

	// Handle function call completion
	if typeStr == "response.output_item.done" {
		itemResult := rootResult.Get("item")
		itemType := itemResult.Get("type").String()
		if itemType == "function_call" {
			// Create function call part
			functionCall := `{"functionCall":{"name":"","args":{}}}`
			{
				// Restore original tool name if shortened
				n := itemResult.Get("name").String()
				rev := buildReverseMapFromGeminiOriginal(originalRequestRawJSON)
				if orig, ok := rev[n]; ok {
					n = orig
				}
				functionCall, _ = sjson.Set(functionCall, "functionCall.name", n)
			}

			// Parse and set arguments
			argsStr := itemResult.Get("arguments").String()
			if argsStr != "" {
				argsResult := gjson.Parse(argsStr)
				if argsResult.IsObject() {
					functionCall, _ = sjson.SetRaw(functionCall, "functionCall.args", argsStr)
				}
			}

			template, _ = sjson.SetRaw(template, "candidates.0.content.parts.-1", functionCall)
			template, _ = sjson.Set(template, "candidates.0.finishReason", "STOP")

			(*param).(*ConvertCodexResponseToGeminiParams).LastStorageOutput = template

			// Use this return to storage message
			return []string{}
		}
	}

	if typeStr == "response.created" { // Handle response creation - set model and response ID
		template, _ = sjson.Set(template, "modelVersion", rootResult.Get("response.model").String())
		template, _ = sjson.Set(template, "responseId", rootResult.Get("response.id").String())
		(*param).(*ConvertCodexResponseToGeminiParams).ResponseID = rootResult.Get("response.id").String()
	} else if typeStr == "response.reasoning_summary_text.delta" { // Handle reasoning/thinking content delta
		part := `{"thought":true,"text":""}`
		part, _ = sjson.Set(part, "text", rootResult.Get("delta").String())
		template, _ = sjson.SetRaw(template, "candidates.0.content.parts.-1", part)
	} else if typeStr == "response.output_text.delta" { // Handle regular text content delta
		part := `{"text":""}`
		part, _ = sjson.Set(part, "text", rootResult.Get("delta").String())
		template, _ = sjson.SetRaw(template, "candidates.0.content.parts.-1", part)
	} else if typeStr == "response.completed" { // Handle response completion with usage metadata
		template, _ = sjson.Set(template, "usageMetadata.promptTokenCount", rootResult.Get("response.usage.input_tokens").Int())
		template, _ = sjson.Set(template, "usageMetadata.candidatesTokenCount", rootResult.Get("response.usage.output_tokens").Int())
		totalTokens := rootResult.Get("response.usage.input_tokens").Int() + rootResult.Get("response.usage.output_tokens").Int()
		template, _ = sjson.Set(template, "usageMetadata.totalTokenCount", totalTokens)
	} else {
		return []string{}
	}

	if (*param).(*ConvertCodexResponseToGeminiParams).LastStorageOutput != "" {
		return []string{(*param).(*ConvertCodexResponseToGeminiParams).LastStorageOutput, template}
	} else {
		return []string{template}
	}

}

// ConvertCodexResponseToGeminiNonStream converts a non-streaming Codex response to a non-streaming Gemini response.
// This function processes the complete Codex response and transforms it into a single Gemini-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the Gemini API format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Codex API
//   - param: A pointer to a parameter object for the conversion (unused in current implementation)
//
// Returns:
//   - string: A Gemini-compatible JSON response containing all message content and metadata
func ConvertCodexResponseToGeminiNonStream(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) string {
	rootResult := gjson.ParseBytes(rawJSON)

	// Verify this is a response.completed event
	if rootResult.Get("type").String() != "response.completed" {
		return ""
	}

	// Base Gemini response template for non-streaming
	template := `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"trafficType":"PROVISIONED_THROUGHPUT"},"modelVersion":"","createTime":"","responseId":""}`

	// Set model version
	template, _ = sjson.Set(template, "modelVersion", modelName)

	// Set response metadata from the completed response
	responseData := rootResult.Get("response")
	if responseData.Exists() {
		// Set response ID
		if responseId := responseData.Get("id"); responseId.Exists() {
			template, _ = sjson.Set(template, "responseId", responseId.String())
		}

		// Set creation time
		if createdAt := responseData.Get("created_at"); createdAt.Exists() {
			template, _ = sjson.Set(template, "createTime", time.Unix(createdAt.Int(), 0).Format(time.RFC3339Nano))
		}

		// Set usage metadata
		if usage := responseData.Get("usage"); usage.Exists() {
			inputTokens := usage.Get("input_tokens").Int()
			outputTokens := usage.Get("output_tokens").Int()
			totalTokens := inputTokens + outputTokens

			template, _ = sjson.Set(template, "usageMetadata.promptTokenCount", inputTokens)
			template, _ = sjson.Set(template, "usageMetadata.candidatesTokenCount", outputTokens)
			template, _ = sjson.Set(template, "usageMetadata.totalTokenCount", totalTokens)
		}

		// Process output content to build parts array
		hasToolCall := false
		var pendingFunctionCalls []string

		flushPendingFunctionCalls := func() {
			if len(pendingFunctionCalls) == 0 {
				return
			}
			// Add all pending function calls as individual parts
			// This maintains the original Gemini API format while ensuring consecutive calls are grouped together
			for _, fc := range pendingFunctionCalls {
				template, _ = sjson.SetRaw(template, "candidates.0.content.parts.-1", fc)
			}
			pendingFunctionCalls = nil
		}

		if output := responseData.Get("output"); output.Exists() && output.IsArray() {
			output.ForEach(func(key, value gjson.Result) bool {
				itemType := value.Get("type").String()

				switch itemType {
				case "reasoning":
					// Flush any pending function calls before adding non-function content
					flushPendingFunctionCalls()

					// Add thinking content
					if content := value.Get("content"); content.Exists() {
						part := `{"text":"","thought":true}`
						part, _ = sjson.Set(part, "text", content.String())
						template, _ = sjson.SetRaw(template, "candidates.0.content.parts.-1", part)
					}

				case "message":
					// Flush any pending function calls before adding non-function content
					flushPendingFunctionCalls()

					// Add regular text content
					if content := value.Get("content"); content.Exists() && content.IsArray() {
						content.ForEach(func(_, contentItem gjson.Result) bool {
							if contentItem.Get("type").String() == "output_text" {
								if text := contentItem.Get("text"); text.Exists() {
									part := `{"text":""}`
									part, _ = sjson.Set(part, "text", text.String())
									template, _ = sjson.SetRaw(template, "candidates.0.content.parts.-1", part)
								}
							}
							return true
						})
					}

				case "function_call":
					// Collect function call for potential merging with consecutive ones
					hasToolCall = true
					functionCall := `{"functionCall":{"args":{},"name":""}}`
					{
						n := value.Get("name").String()
						rev := buildReverseMapFromGeminiOriginal(originalRequestRawJSON)
						if orig, ok := rev[n]; ok {
							n = orig
						}
						functionCall, _ = sjson.Set(functionCall, "functionCall.name", n)
					}

					// Parse and set arguments
					if argsStr := value.Get("arguments").String(); argsStr != "" {
						argsResult := gjson.Parse(argsStr)
						if argsResult.IsObject() {
							functionCall, _ = sjson.SetRaw(functionCall, "functionCall.args", argsStr)
						}
					}

					pendingFunctionCalls = append(pendingFunctionCalls, functionCall)
				}
				return true
			})

			// Handle any remaining pending function calls at the end
			flushPendingFunctionCalls()
		}

		// Set finish reason based on whether there were tool calls
		if hasToolCall {
			template, _ = sjson.Set(template, "candidates.0.finishReason", "STOP")
		} else {
			template, _ = sjson.Set(template, "candidates.0.finishReason", "STOP")
		}
	}
	return template
}

// buildReverseMapFromGeminiOriginal builds a map[short]original from original Gemini request tools.
func buildReverseMapFromGeminiOriginal(original []byte) map[string]string {
	tools := gjson.GetBytes(original, "tools")
	rev := map[string]string{}
	if !tools.IsArray() {
		return rev
	}
	var names []string
	tarr := tools.Array()
	for i := 0; i < len(tarr); i++ {
		fns := tarr[i].Get("functionDeclarations")
		if !fns.IsArray() {
			continue
		}
		for _, fn := range fns.Array() {
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

func GeminiTokenCount(ctx context.Context, count int64) string {
	return fmt.Sprintf(`{"totalTokens":%d,"promptTokensDetails":[{"modality":"TEXT","tokenCount":%d}]}`, count, count)
}
