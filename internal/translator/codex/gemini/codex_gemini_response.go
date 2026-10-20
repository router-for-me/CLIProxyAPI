// Package gemini provides response translation functionality for Codex to Gemini API compatibility.
// This package handles the conversion of Codex API responses into Gemini-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Gemini API clients.
package gemini

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	dataTag = []byte("data:")
)

// ConvertCodexResponseToGeminiParams holds parameters for response conversion.
type ConvertCodexResponseToGeminiParams struct {
	Model      string
	CreatedAt  int64
	ResponseID string
	// StorageOutputs queues completed function-call chunks in stream order
	// until the next non-storage event flushes them. A queue (not a single
	// slot) so consecutive function calls can never overwrite each other.
	StorageOutputs     [][]byte
	HasOutputTextDelta bool
	LastImageHashByID  map[string][32]byte
	// PendingThoughtSignature holds a completed reasoning item's
	// encrypted_content until the next visible part can carry it as a
	// Gemini thoughtSignature, mirroring native Gemini part placement.
	PendingThoughtSignature string
	// SeenReasoningSignatures deduplicates repeated reasoning done events
	// by item ID + signature hash within one response stream.
	SeenReasoningSignatures map[[32]byte]bool
	// HasReasoningDelta and StreamedReasoningItems record whether reasoning
	// deltas were already emitted for the current or keyed reasoning item so
	// done-only reasoning summaries can fall back cleanly.
	HasReasoningDelta      bool
	StreamedReasoningItems map[string]bool
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

	// flushStorageOutputs drains queued function-call chunks ahead of any
	// extra chunks, preserving stream order on every return path.
	flushStorageOutputs := func(extra ...[]byte) [][]byte {
		outputs := make([][]byte, 0, len(params.StorageOutputs)+len(extra))
		for _, stored := range params.StorageOutputs {
			outputs = append(outputs, append([]byte(nil), stored...))
		}
		params.StorageOutputs = nil
		return append(outputs, extra...)
	}
	attachPendingThoughtSignature := func(part []byte) []byte {
		if params.PendingThoughtSignature == "" {
			return part
		}
		part = setGeminiThoughtSignature(part, params.PendingThoughtSignature)
		params.PendingThoughtSignature = ""
		return part
	}

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
		part = attachPendingThoughtSignature(part)
		template = translatorcommon.SetRawArrayItems(template, "candidates.0.content.parts", [][]byte{part})
		return flushStorageOutputs(template)
	}

	// Handle function call completion
	if typeStr == "response.output_item.done" {
		itemResult := rootResult.Get("item")
		itemType := itemResult.Get("type").String()
		if itemType == "reasoning" {
			signature := strings.TrimSpace(itemResult.Get("encrypted_content").String())
			itemID := strings.TrimSpace(itemResult.Get("id").String())
			hadStreamedReasoning := params.HasReasoningDelta
			if itemID != "" && len(params.StreamedReasoningItems) > 0 {
				hadStreamedReasoning = params.StreamedReasoningItems[itemID]
			}
			params.HasReasoningDelta = false
			var thoughtTexts []string
			if !hadStreamedReasoning {
				thoughtTexts = extractCodexGeminiReasoningTexts(itemResult)
			}
			if signature == "" && len(thoughtTexts) == 0 {
				return [][]byte{}
			}
			if signature != "" {
				seenKey := sha256.Sum256([]byte(itemID + "\x00" + signature))
				if params.SeenReasoningSignatures == nil {
					params.SeenReasoningSignatures = make(map[[32]byte]bool)
				}
				if params.SeenReasoningSignatures[seenKey] {
					return [][]byte{}
				}
				params.SeenReasoningSignatures[seenKey] = true
			}
			var emittedParts [][]byte
			if params.PendingThoughtSignature != "" && signature != "" {
				// A previous reasoning step never got a visible carrier part;
				// flush it as an empty thought part so no signature is lost.
				part := setGeminiThoughtSignature([]byte(`{"thought":true,"text":""}`), params.PendingThoughtSignature)
				params.PendingThoughtSignature = ""
				emittedParts = append(emittedParts, part)
			}
			for _, thoughtText := range thoughtTexts {
				part := []byte(`{"thought":true,"text":""}`)
				part, _ = sjson.SetBytes(part, "text", thoughtText)
				emittedParts = append(emittedParts, part)
			}
			if signature != "" {
				params.PendingThoughtSignature = signature
			}
			if len(emittedParts) > 0 {
				template = translatorcommon.SetRawArrayItems(template, "candidates.0.content.parts", emittedParts)
				return flushStorageOutputs(template)
			}
			return [][]byte{}
		}
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
			part = attachPendingThoughtSignature(part)
			template = translatorcommon.SetRawArrayItems(template, "candidates.0.content.parts", [][]byte{part})
			return flushStorageOutputs(template)
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
			functionCall = setGeminiFunctionCallID(functionCall, itemResult)
			functionCall = attachPendingThoughtSignature(functionCall)

			template = translatorcommon.SetRawArrayItems(template, "candidates.0.content.parts", [][]byte{functionCall})
			template, _ = sjson.SetBytes(template, "candidates.0.finishReason", "STOP")

			params.StorageOutputs = append(params.StorageOutputs, append([]byte(nil), template...))

			// Use this return to storage message
			return [][]byte{}
		}
	}

	if typeStr == "response.created" { // Handle response creation - set model and response ID
		// A new response must not inherit per-response streaming state if the
		// caller reuses params across responses.
		params.StorageOutputs = nil
		params.PendingThoughtSignature = ""
		params.SeenReasoningSignatures = nil
		params.HasReasoningDelta = false
		params.StreamedReasoningItems = nil
		params.HasOutputTextDelta = false
		template, _ = sjson.SetBytes(template, "modelVersion", rootResult.Get("response.model").String())
		template, _ = sjson.SetBytes(template, "responseId", rootResult.Get("response.id").String())
		params.ResponseID = rootResult.Get("response.id").String()
	} else if typeStr == "response.output_item.added" && rootResult.Get("item.type").String() == "reasoning" {
		params.HasReasoningDelta = false
		if params.PendingThoughtSignature != "" {
			part := setGeminiThoughtSignature([]byte(`{"thought":true,"text":""}`), params.PendingThoughtSignature)
			params.PendingThoughtSignature = ""
			template = translatorcommon.SetRawArrayItems(template, "candidates.0.content.parts", [][]byte{part})
			return flushStorageOutputs(template)
		}
		return [][]byte{}
	} else if typeStr == "response.reasoning_summary_text.delta" || typeStr == "response.reasoning_text.delta" { // Handle reasoning/thinking content delta
		params.HasReasoningDelta = true
		if itemID := strings.TrimSpace(rootResult.Get("item_id").String()); itemID != "" {
			if params.StreamedReasoningItems == nil {
				params.StreamedReasoningItems = make(map[string]bool)
			}
			params.StreamedReasoningItems[itemID] = true
		}
		var emittedParts [][]byte
		if params.PendingThoughtSignature != "" {
			carrier := setGeminiThoughtSignature([]byte(`{"thought":true,"text":""}`), params.PendingThoughtSignature)
			params.PendingThoughtSignature = ""
			emittedParts = append(emittedParts, carrier)
		}
		part := []byte(`{"thought":true,"text":""}`)
		part, _ = sjson.SetBytes(part, "text", rootResult.Get("delta").String())
		emittedParts = append(emittedParts, part)
		template = translatorcommon.SetRawArrayItems(template, "candidates.0.content.parts", emittedParts)
	} else if typeStr == "response.output_text.delta" { // Handle regular text content delta
		params.HasOutputTextDelta = true
		part := []byte(`{"text":""}`)
		part, _ = sjson.SetBytes(part, "text", rootResult.Get("delta").String())
		if rootResult.Get("delta").String() != "" {
			// An empty text part may be discarded by clients; never spend the
			// only signature copy on one.
			part = attachPendingThoughtSignature(part)
		}
		template = translatorcommon.SetRawArrayItems(template, "candidates.0.content.parts", [][]byte{part})
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
			part = attachPendingThoughtSignature(part)
			template, _ = sjson.SetRawBytes(template, "candidates.0.content.parts.-1", part)
			wroteText = true
			return true
		})
		if wroteText {
			params.HasOutputTextDelta = true
			return flushStorageOutputs(template)
		}
		return [][]byte{}
	} else if typeStr == "response.completed" || typeStr == "response.incomplete" { // Handle response completion with usage metadata
		if params.PendingThoughtSignature != "" {
			part := setGeminiThoughtSignature([]byte(`{"thought":true,"text":""}`), params.PendingThoughtSignature)
			params.PendingThoughtSignature = ""
			template = translatorcommon.SetRawArrayItems(template, "candidates.0.content.parts", [][]byte{part})
		}
		template, _ = sjson.SetBytes(template, "usageMetadata.promptTokenCount", rootResult.Get("response.usage.input_tokens").Int())
		template, _ = sjson.SetBytes(template, "usageMetadata.candidatesTokenCount", rootResult.Get("response.usage.output_tokens").Int())
		totalTokens := rootResult.Get("response.usage.input_tokens").Int() + rootResult.Get("response.usage.output_tokens").Int()
		template, _ = sjson.SetBytes(template, "usageMetadata.totalTokenCount", totalTokens)
		if typeStr == "response.incomplete" {
			template, _ = sjson.SetBytes(template, "candidates.0.finishReason", codexGeminiIncompleteFinishReason(rootResult.Get("response.incomplete_details.reason").String()))
		}
	} else {
		return [][]byte{}
	}

	return flushStorageOutputs(template)
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

	// Verify this is a terminal response event.
	responseType := rootResult.Get("type").String()
	if responseType != "response.completed" && responseType != "response.incomplete" {
		return []byte{}
	}

	// Base Gemini response template for non-streaming
	template := []byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"trafficType":"PROVISIONED_THROUGHPUT"},"modelVersion":"","createTime":"","responseId":""}`)

	// Set model version
	template, _ = sjson.SetBytes(template, "modelVersion", modelName)

	// Set response metadata from the completed response
	responseData := rootResult.Get("response")
	if responseData.Exists() {
		if responseType == "response.incomplete" {
			template, _ = sjson.SetBytes(template, "candidates.0.finishReason", codexGeminiIncompleteFinishReason(responseData.Get("incomplete_details.reason").String()))
		}
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
		var parts [][]byte
		var pendingFunctionCalls [][]byte

		flushPendingFunctionCalls := func() {
			if len(pendingFunctionCalls) == 0 {
				return
			}
			// Add all pending function calls as individual parts
			// This maintains the original Gemini API format while ensuring consecutive calls are grouped together
			parts = append(parts, pendingFunctionCalls...)
			pendingFunctionCalls = nil
		}

		if output := responseData.Get("output"); output.Exists() && output.IsArray() {
			output.ForEach(func(key, value gjson.Result) bool {
				itemType := value.Get("type").String()

				switch itemType {
				case "reasoning":
					// Flush any pending function calls before adding non-function content
					flushPendingFunctionCalls()

					// Surface the reasoning summary as thought parts and carry
					// the encrypted reasoning state as thoughtSignature on the
					// first of them, so a Gemini client can replay it next turn.
					// With no summary text, emit an empty thought carrier part;
					// dropping the signature here would silently break replay.
					signature := strings.TrimSpace(value.Get("encrypted_content").String())
					thoughtTexts := extractCodexGeminiReasoningTexts(value)
					for i, thoughtText := range thoughtTexts {
						part := []byte(`{"text":"","thought":true}`)
						part, _ = sjson.SetBytes(part, "text", thoughtText)
						if i == len(thoughtTexts)-1 && signature != "" {
							part = setGeminiThoughtSignature(part, signature)
						}
						parts = append(parts, part)
					}
					if len(thoughtTexts) == 0 && signature != "" {
						part := setGeminiThoughtSignature([]byte(`{"text":"","thought":true}`), signature)
						parts = append(parts, part)
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
									parts = append(parts, part)
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
					parts = append(parts, part)

				case "function_call":
					// Collect function call for potential merging with consecutive ones
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
					functionCall = setGeminiFunctionCallID(functionCall, value)

					pendingFunctionCalls = append(pendingFunctionCalls, functionCall)
				}
				return true
			})

			// Handle any remaining pending function calls at the end
			flushPendingFunctionCalls()

			if len(parts) > 0 {
				template, _ = sjson.SetRawBytes(template, "candidates.0.content.parts", translatorcommon.JoinRawArray(parts))
			}
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

// geminiCodexThoughtSignatureCarrierMagic marks a CPA carrier payload inside
// a Gemini thoughtSignature bytes field. The Gemini JSON wire form of a bytes
// field is standard padded base64, while Responses signatures are opaque
// strings that may use the base64url alphabet without padding; writing such a
// string verbatim makes official SDK []byte decoding fail. The carrier therefore
// stores magic+rawSignature as the byte payload and relies on standard base64
// JSON encoding, which every Gemini client decodes and echoes losslessly.
func extractCodexGeminiReasoningTexts(value gjson.Result) []string {
	var thoughtTexts []string
	if summary := value.Get("summary"); summary.IsArray() {
		for _, entry := range summary.Array() {
			if text := entry.Get("text").String(); text != "" {
				thoughtTexts = append(thoughtTexts, text)
			}
		}
	}
	if content := value.Get("content"); content.IsArray() {
		for _, entry := range content.Array() {
			if entry.Get("type").String() == "reasoning_text" {
				if text := entry.Get("text").String(); text != "" {
					thoughtTexts = append(thoughtTexts, text)
				}
			}
		}
	} else if content.Type == gjson.String && content.String() != "" {
		thoughtTexts = append(thoughtTexts, content.String())
	}
	return thoughtTexts
}

const geminiCodexThoughtSignatureCarrierMagic = "cpa-codex-gemini-carrier-v1:"

func encodeGeminiCodexThoughtSignature(rawSignature string) string {
	rawSignature = strings.TrimSpace(rawSignature)
	if rawSignature == "" {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(geminiCodexThoughtSignatureCarrierMagic + rawSignature))
}

func setGeminiThoughtSignature(part []byte, rawSignature string) []byte {
	encoded := encodeGeminiCodexThoughtSignature(rawSignature)
	if encoded == "" {
		return part
	}
	part, _ = sjson.SetBytes(part, "thoughtSignature", encoded)
	return part
}

func setGeminiFunctionCallID(functionCall []byte, item gjson.Result) []byte {
	if callID := strings.TrimSpace(item.Get("call_id").String()); callID != "" {
		functionCall, _ = sjson.SetBytes(functionCall, "functionCall.id", callID)
		return functionCall
	}
	if id := strings.TrimSpace(item.Get("id").String()); id != "" {
		functionCall, _ = sjson.SetBytes(functionCall, "functionCall.id", id)
	}
	return functionCall
}

func codexGeminiIncompleteFinishReason(reason string) string {
	switch reason {
	case "max_tokens", "max_output_tokens":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	default:
		return "OTHER"
	}
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
