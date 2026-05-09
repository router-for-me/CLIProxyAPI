// Package gemini provides response translation functionality for Codex to Gemini API compatibility.
// This package handles the conversion of Codex API responses into Gemini-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Gemini API clients.
package gemini

import (
	"bytes"
	"context"
	"crypto/sha256"
	"strings"
	"time"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	dataTag = []byte("data:")
)

// ConvertCodexResponseToGeminiParams holds parameters for response conversion.
type ConvertCodexResponseToGeminiParams struct {
	Model              string
	CreatedAt          int64
	ResponseID         string
	LastStorageOutput  []byte
	HasOutputTextDelta bool
	LastImageHashByID  map[string][32]byte
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
//   - [][]byte: A slice of Gemini-compatible JSON responses
func ConvertCodexResponseToGemini(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &ConvertCodexResponseToGeminiParams{
			Model:              modelName,
			CreatedAt:          0,
			ResponseID:         "",
			LastStorageOutput:  nil,
			HasOutputTextDelta: false,
			LastImageHashByID:  make(map[string][32]byte),
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	rootResult := gjson.ParseBytes(rawJSON)
	typeResult := rootResult.Get("type")
	typeStr := typeResult.String()

	params := (*param).(*ConvertCodexResponseToGeminiParams)

	// Base Gemini response template
	template := []byte(`{"candidates":[{"content":{"role":"model","parts":[]}}],"usageMetadata":{"trafficType":"PROVISIONED_THROUGHPUT"},"modelVersion":"gemini-2.5-pro","createTime":"2025-08-15T02:52:03.884209Z","responseId":"06CeaPH7NaCU48APvNXDyA4"}`)
	{
		template, _ = sjson.SetBytes(template, "modelVersion", params.Model)
		createdAtResult := rootResult.Get("response.created_at")
		if createdAtResult.Exists() {
			params.CreatedAt = createdAtResult.Int()
			template, _ = sjson.SetBytes(template, "createTime", time.Unix(params.CreatedAt, 0).Format(time.RFC3339Nano))
		}
		template, _ = sjson.SetBytes(template, "responseId", params.ResponseID)
	}

	if typeStr == "response.image_generation_call.partial_image" {
		itemID := rootResult.Get("item_id").String()
		b64 := rootResult.Get("partial_image_b64").String()
		if b64 == "" {
			return [][]byte{}
		}
		if itemID != "" {
			if params.LastImageHashByID == nil {
				params.LastImageHashByID = make(map[string][32]byte)
			}
			hash := sha256.Sum256([]byte(b64))
			if last, ok := params.LastImageHashByID[itemID]; ok && last == hash {
				return [][]byte{}
			}
			params.LastImageHashByID[itemID] = hash
		}

		outputFormat := rootResult.Get("output_format").String()
		mimeType := mimeTypeFromCodexOutputFormat(outputFormat)

		part := []byte(`{"inlineData":{"data":"","mimeType":""}}`)
		part, _ = sjson.SetBytes(part, "inlineData.data", b64)
		part, _ = sjson.SetBytes(part, "inlineData.mimeType", mimeType)
		template, _ = sjson.SetRawBytes(template, "candidates.0.content.parts.-1", part)
		return [][]byte{template}
	}

	// Handle function call completion
	if typeStr == "response.output_item.done" {
		itemResult := rootResult.Get("item")
		itemType := itemResult.Get("type").String()
		if itemType == "image_generation_call" {
			itemID := itemResult.Get("id").String()
			b64 := itemResult.Get("result").String()
			if b64 == "" {
				return [][]byte{}
			}
			if itemID != "" {
				if params.LastImageHashByID == nil {
					params.LastImageHashByID = make(map[string][32]byte)
				}
				hash := sha256.Sum256([]byte(b64))
				if last, ok := params.LastImageHashByID[itemID]; ok && last == hash {
					return [][]byte{}
				}
				params.LastImageHashByID[itemID] = hash
			}

			outputFormat := itemResult.Get("output_format").String()
			mimeType := mimeTypeFromCodexOutputFormat(outputFormat)

			part := []byte(`{"inlineData":{"data":"","mimeType":""}}`)
			part, _ = sjson.SetBytes(part, "inlineData.data", b64)
			part, _ = sjson.SetBytes(part, "inlineData.mimeType", mimeType)
			template, _ = sjson.SetRawBytes(template, "candidates.0.content.parts.-1", part)
			return [][]byte{template}
		}
		if itemType == "function_call" {
			// Create function call part
			functionCall := []byte(`{"functionCall":{"name":"","args":{}}}`)
			{
				// Restore original tool name if shortened
				n := itemResult.Get("name").String()
				rev := buildReverseMapFromGeminiOriginal(originalRequestRawJSON)
				if orig, ok := rev[n]; ok {
					n = orig
				}
				functionCall, _ = sjson.SetBytes(functionCall, "functionCall.name", n)
			}

			// Parse and set arguments
			argsStr := itemResult.Get("arguments").String()
			if argsStr != "" {
				argsResult := gjson.Parse(argsStr)
				if argsResult.IsObject() {
					functionCall, _ = sjson.SetRawBytes(functionCall, "functionCall.args", []byte(argsStr))
				}
			}

			template, _ = sjson.SetRawBytes(template, "candidates.0.content.parts.-1", functionCall)
			template, _ = sjson.SetBytes(template, "candidates.0.finishReason", "STOP")

			params.LastStorageOutput = append([]byte(nil), template...)

			// Use this return to storage message
			return [][]byte{}
		}
	}

	if typeStr == "response.created" { // Handle response creation - set model and response ID
		template, _ = sjson.SetBytes(template, "modelVersion", rootResult.Get("response.model").String())
		template, _ = sjson.SetBytes(template, "responseId", rootResult.Get("response.id").String())
		params.ResponseID = rootResult.Get("response.id").String()
	} else if typeStr == "response.reasoning_summary_text.delta" { // Handle reasoning/thinking content delta
		part := []byte(`{"thought":true,"text":""}`)
		part, _ = sjson.SetBytes(part, "text", rootResult.Get("delta").String())
		template, _ = sjson.SetRawBytes(template, "candidates.0.content.parts.-1", part)
	} else if typeStr == "response.output_text.delta" { // Handle regular text content delta
		params.HasOutputTextDelta = true
		part := []byte(`{"text":""}`)
		part, _ = sjson.SetBytes(part, "text", rootResult.Get("delta").String())
		template, _ = sjson.SetRawBytes(template, "candidates.0.content.parts.-1", part)
	} else if typeStr == "response.output_item.done" { // Fallback: emit final message text when no delta chunks were received
		itemResult := rootResult.Get("item")
		if itemResult.Get("type").String() != "message" || params.HasOutputTextDelta {
			return [][]byte{}
		}
		contentResult := itemResult.Get("content")
		if !contentResult.Exists() || !contentResult.IsArray() {
			return [][]byte{}
		}
		wroteText := false
		contentResult.ForEach(func(_, partResult gjson.Result) bool {
			if partResult.Get("type").String() != "output_text" {
				return true
			}
			text := partResult.Get("text").String()
			if text == "" {
				return true
			}
			part := []byte(`{"text":""}`)
			part, _ = sjson.SetBytes(part, "text", text)
			template, _ = sjson.SetRawBytes(template, "candidates.0.content.parts.-1", part)
			wroteText = true
			return true
		})
		if wroteText {
			params.HasOutputTextDelta = true
			return [][]byte{template}
		}
		return [][]byte{}
	} else if typeStr == "response.completed" { // Handle response completion with usage metadata
		template, _ = sjson.SetBytes(template, "usageMetadata.promptTokenCount", rootResult.Get("response.usage.input_tokens").Int())
		template, _ = sjson.SetBytes(template, "usageMetadata.candidatesTokenCount", rootResult.Get("response.usage.output_tokens").Int())
		totalTokens := rootResult.Get("response.usage.input_tokens").Int() + rootResult.Get("response.usage.output_tokens").Int()
		template, _ = sjson.SetBytes(template, "usageMetadata.totalTokenCount", totalTokens)
	} else {
		return [][]byte{}
	}

	if len(params.LastStorageOutput) > 0 {
		stored := append([]byte(nil), params.LastStorageOutput...)
		params.LastStorageOutput = nil
		return [][]byte{stored, template}
	}
	return [][]byte{template}
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
//   - []byte: A Gemini-compatible JSON response containing all message content and metadata
func ConvertCodexResponseToGeminiNonStream(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	rootResult := gjson.ParseBytes(rawJSON)

	// Verify this is a response.completed event
	if rootResult.Get("type").String() != "response.completed" {
		return []byte{}
	}

	// Base Gemini response template for non-streaming
	template := []byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"trafficType":"PROVISIONED_THROUGHPUT"},"modelVersion":"","createTime":"","responseId":""}`)

	// Set model version
	template, _ = sjson.SetBytes(template, "modelVersion", modelName)

	// Set response metadata from the completed response
	responseData := rootResult.Get("response")
	if responseData.Exists() {
		// Set response ID
		if responseId := responseData.Get("id"); responseId.Exists() {
			template, _ = sjson.SetBytes(template, "responseId", responseId.String())
		}

		// Set creation time
		if createdAt := responseData.Get("created_at"); createdAt.Exists() {
			template, _ = sjson.SetBytes(template, "createTime", time.Unix(createdAt.Int(), 0).Format(time.RFC3339Nano))
		}

		// Set usage metadata
		if usage := responseData.Get("usage"); usage.Exists() {
			inputTokens := usage.Get("input_tokens").Int()
			outputTokens := usage.Get("output_tokens").Int()
			totalTokens := inputTokens + outputTokens

			template, _ = sjson.SetBytes(template, "usageMetadata.promptTokenCount", inputTokens)
			template, _ = sjson.SetBytes(template, "usageMetadata.candidatesTokenCount", outputTokens)
			template, _ = sjson.SetBytes(template, "usageMetadata.totalTokenCount", totalTokens)
		}

		// Process output content to build parts array
		hasToolCall := false
		var pendingFunctionCalls [][]byte

		flushPendingFunctionCalls := func() {
			if len(pendingFunctionCalls) == 0 {
				return
			}
			// Add all pending function calls as individual parts
			// This maintains the original Gemini API format while ensuring consecutive calls are grouped together
			for _, fc := range pendingFunctionCalls {
				template, _ = sjson.SetRawBytes(template, "candidates.0.content.parts.-1", fc)
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
						part := []byte(`{"text":"","thought":true}`)
						part, _ = sjson.SetBytes(part, "text", content.String())
						template, _ = sjson.SetRawBytes(template, "candidates.0.content.parts.-1", part)
					}

				case "message":
					// Flush any pending function calls before adding non-function content
					flushPendingFunctionCalls()

					// Add regular text content
					if content := value.Get("content"); content.Exists() && content.IsArray() {
						content.ForEach(func(_, contentItem gjson.Result) bool {
							if contentItem.Get("type").String() == "output_text" {
								if text := contentItem.Get("text"); text.Exists() {
									part := []byte(`{"text":""}`)
									part, _ = sjson.SetBytes(part, "text", text.String())
									template, _ = sjson.SetRawBytes(template, "candidates.0.content.parts.-1", part)
								}
							}
							return true
						})
					}

				case "image_generation_call":
					flushPendingFunctionCalls()
					b64 := value.Get("result").String()
					if b64 == "" {
						break
					}
					outputFormat := value.Get("output_format").String()
					mimeType := mimeTypeFromCodexOutputFormat(outputFormat)

					part := []byte(`{"inlineData":{"data":"","mimeType":""}}`)
					part, _ = sjson.SetBytes(part, "inlineData.data", b64)
					part, _ = sjson.SetBytes(part, "inlineData.mimeType", mimeType)
					template, _ = sjson.SetRawBytes(template, "candidates.0.content.parts.-1", part)

				case "function_call":
					// Collect function call for potential merging with consecutive ones
					hasToolCall = true
					functionCall := []byte(`{"functionCall":{"args":{},"name":""}}`)
					{
						n := value.Get("name").String()
						rev := buildReverseMapFromGeminiOriginal(originalRequestRawJSON)
						if orig, ok := rev[n]; ok {
							n = orig
						}
						functionCall, _ = sjson.SetBytes(functionCall, "functionCall.name", n)
					}

					// Parse and set arguments
					if argsStr := value.Get("arguments").String(); argsStr != "" {
						argsResult := gjson.Parse(argsStr)
						if argsResult.IsObject() {
							functionCall, _ = sjson.SetRawBytes(functionCall, "functionCall.args", []byte(argsStr))
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
			template, _ = sjson.SetBytes(template, "candidates.0.finishReason", "STOP")
		} else {
			template, _ = sjson.SetBytes(template, "candidates.0.finishReason", "STOP")
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

func GeminiTokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.GeminiTokenCountJSON(count)
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
