// Package gemini provides response translation functionality for Codex to Gemini API compatibility.
// This package handles the conversion of Codex API responses into Gemini-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Gemini API clients.
package gemini

import (
	"bytes"
	"context"
	"crypto/sha256"
	"github.com/MoeForge/moego/base64"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
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

	rootResult := util.ParseGJSONBytesNoCopy(rawJSON)
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
	attachPendingThoughtSignatureDoc := func(doc *ojson.Doc) {
		if params.PendingThoughtSignature == "" {
			return
		}
		setGeminiThoughtSignatureDoc(doc, params.PendingThoughtSignature)
		params.PendingThoughtSignature = ""
	}

	// Base Gemini response template
	templateDoc := ojson.NewDocString(`{"candidates":[{"content":{"role":"model","parts":[]}}],"usageMetadata":{"trafficType":"PROVISIONED_THROUGHPUT"},"modelVersion":"gemini-2.5-pro","createTime":"2025-08-15T02:52:03.884209Z","responseId":"06CeaPH7NaCU48APvNXDyA4"}`)
	{
		_ = templateDoc.Set("modelVersion", params.Model)
		createdAtResult := rootResult.Get("response.created_at")
		if createdAtResult.Exists() {
			params.CreatedAt = createdAtResult.Int()
			_ = templateDoc.Set("createTime", time.Unix(params.CreatedAt, 0).Format(time.RFC3339Nano))
		}
		_ = templateDoc.Set("responseId", params.ResponseID)
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
			params.LastImageHashByID[strings.Clone(itemID)] = hash
		}

		outputFormat := rootResult.Get("output_format").String()
		mimeType := mimeTypeFromCodexOutputFormat(outputFormat)

		partDoc := ojson.NewDocString(`{"inlineData":{"data":"","mimeType":""}}`)
		_ = partDoc.Set("inlineData.data", b64)
		_ = partDoc.Set("inlineData.mimeType", mimeType)
		attachPendingThoughtSignatureDoc(partDoc)
		translatorcommon.SetRawArrayItemsDoc(templateDoc, "candidates.0.content.parts", [][]byte{partDoc.Bytes()})
		return flushStorageOutputs(templateDoc.Bytes())
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
				part, _ = ojson.SetBytes(part, "text", thoughtText)
				emittedParts = append(emittedParts, part)
			}
			if signature != "" {
				params.PendingThoughtSignature = strings.Clone(signature)
			}
			if len(emittedParts) > 0 {
				translatorcommon.SetRawArrayItemsDoc(templateDoc, "candidates.0.content.parts", emittedParts)
				return flushStorageOutputs(templateDoc.Bytes())
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
				params.LastImageHashByID[strings.Clone(itemID)] = hash
			}

			outputFormat := itemResult.Get("output_format").String()
			mimeType := mimeTypeFromCodexOutputFormat(outputFormat)

			partDoc := ojson.NewDocString(`{"inlineData":{"data":"","mimeType":""}}`)
			_ = partDoc.Set("inlineData.data", b64)
			_ = partDoc.Set("inlineData.mimeType", mimeType)
			attachPendingThoughtSignatureDoc(partDoc)
			translatorcommon.SetRawArrayItemsDoc(templateDoc, "candidates.0.content.parts", [][]byte{partDoc.Bytes()})
			return flushStorageOutputs(templateDoc.Bytes())
		}
		if itemType == "function_call" {
			// Create function call part
			functionCallDoc := ojson.NewDocString(`{"functionCall":{"name":"","args":{}}}`)
			{
				// Restore original tool name if shortened
				n := itemResult.Get("name").String()
				rev := buildReverseMapFromGeminiOriginal(originalRequestRawJSON)
				if orig, ok := rev[n]; ok {
					n = orig
				}
				_ = functionCallDoc.Set("functionCall.name", n)
			}

			// Parse and set arguments
			argsStr := itemResult.Get("arguments").String()
			if argsStr != "" {
				argsResult := ojson.Parse(argsStr)
				if argsResult.IsObject() {
					_ = functionCallDoc.SetRawBytes("functionCall.args", []byte(argsStr))
				}
			}
			setGeminiFunctionCallIDDoc(functionCallDoc, itemResult)
			attachPendingThoughtSignatureDoc(functionCallDoc)

			translatorcommon.SetRawArrayItemsDoc(templateDoc, "candidates.0.content.parts", [][]byte{functionCallDoc.Bytes()})
			_ = templateDoc.Set("candidates.0.finishReason", "STOP")

			params.StorageOutputs = append(params.StorageOutputs, append([]byte(nil), templateDoc.Bytes()...))

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
		_ = templateDoc.Set("modelVersion", rootResult.Get("response.model").String())
		_ = templateDoc.Set("responseId", rootResult.Get("response.id").String())
		params.ResponseID = strings.Clone(rootResult.Get("response.id").String())
	} else if typeStr == "response.output_item.added" && rootResult.Get("item.type").String() == "reasoning" {
		params.HasReasoningDelta = false
		if params.PendingThoughtSignature != "" {
			part := setGeminiThoughtSignature([]byte(`{"thought":true,"text":""}`), params.PendingThoughtSignature)
			params.PendingThoughtSignature = ""
			translatorcommon.SetRawArrayItemsDoc(templateDoc, "candidates.0.content.parts", [][]byte{part})
			return flushStorageOutputs(templateDoc.Bytes())
		}
		return [][]byte{}
	} else if typeStr == "response.reasoning_summary_text.delta" || typeStr == "response.reasoning_text.delta" { // Handle reasoning/thinking content delta
		params.HasReasoningDelta = true
		if itemID := strings.TrimSpace(rootResult.Get("item_id").String()); itemID != "" {
			if params.StreamedReasoningItems == nil {
				params.StreamedReasoningItems = make(map[string]bool)
			}
			params.StreamedReasoningItems[strings.Clone(itemID)] = true
		}
		var emittedParts [][]byte
		if params.PendingThoughtSignature != "" {
			carrier := setGeminiThoughtSignature([]byte(`{"thought":true,"text":""}`), params.PendingThoughtSignature)
			params.PendingThoughtSignature = ""
			emittedParts = append(emittedParts, carrier)
		}
		part := []byte(`{"thought":true,"text":""}`)
		part, _ = ojson.SetBytes(part, "text", rootResult.Get("delta").String())
		emittedParts = append(emittedParts, part)
		translatorcommon.SetRawArrayItemsDoc(templateDoc, "candidates.0.content.parts", emittedParts)
	} else if typeStr == "response.output_text.delta" { // Handle regular text content delta
		params.HasOutputTextDelta = true
		partDoc := ojson.NewDocString(`{"text":""}`)
		_ = partDoc.Set("text", rootResult.Get("delta").String())
		if rootResult.Get("delta").String() != "" {
			// An empty text part may be discarded by clients; never spend the
			// only signature copy on one.
			attachPendingThoughtSignatureDoc(partDoc)
		}
		translatorcommon.SetRawArrayItemsDoc(templateDoc, "candidates.0.content.parts", [][]byte{partDoc.Bytes()})
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
		contentResult.ForEach(func(_, partResult ojson.Result) bool {
			if partResult.Get("type").String() != "output_text" {
				return true
			}
			text := partResult.Get("text").String()
			if text == "" {
				return true
			}
			partDoc := ojson.NewDocString(`{"text":""}`)
			_ = partDoc.Set("text", text)
			attachPendingThoughtSignatureDoc(partDoc)
			_ = templateDoc.SetRawBytes("candidates.0.content.parts.-1", partDoc.Bytes())
			wroteText = true
			return true
		})
		if wroteText {
			params.HasOutputTextDelta = true
			return flushStorageOutputs(templateDoc.Bytes())
		}
		return [][]byte{}
	} else if typeStr == "response.completed" || typeStr == "response.incomplete" { // Handle response completion with usage metadata
		if params.PendingThoughtSignature != "" {
			part := setGeminiThoughtSignature([]byte(`{"thought":true,"text":""}`), params.PendingThoughtSignature)
			params.PendingThoughtSignature = ""
			translatorcommon.SetRawArrayItemsDoc(templateDoc, "candidates.0.content.parts", [][]byte{part})
		}
		_ = templateDoc.Set("usageMetadata.promptTokenCount", rootResult.Get("response.usage.input_tokens").Int())
		_ = templateDoc.Set("usageMetadata.candidatesTokenCount", rootResult.Get("response.usage.output_tokens").Int())
		totalTokens := rootResult.Get("response.usage.input_tokens").Int() + rootResult.Get("response.usage.output_tokens").Int()
		_ = templateDoc.Set("usageMetadata.totalTokenCount", totalTokens)
		if typeStr == "response.incomplete" {
			_ = templateDoc.Set("candidates.0.finishReason", codexGeminiIncompleteFinishReason(rootResult.Get("response.incomplete_details.reason").String()))
		} else {
			_ = templateDoc.Set("candidates.0.finishReason", "STOP")
		}
	} else {
		return [][]byte{}
	}

	return flushStorageOutputs(templateDoc.Bytes())
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
	rootResult := util.ParseGJSONBytesNoCopy(rawJSON)

	// Verify this is a terminal response event.
	responseType := rootResult.Get("type").String()
	if responseType != "response.completed" && responseType != "response.incomplete" {
		return []byte{}
	}

	// Base Gemini response template for non-streaming
	templateDoc := ojson.NewDocString(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"trafficType":"PROVISIONED_THROUGHPUT"},"modelVersion":"","createTime":"","responseId":""}`)

	// Set model version
	_ = templateDoc.Set("modelVersion", modelName)

	// Set response metadata from the completed response
	responseData := rootResult.Get("response")
	if responseData.Exists() {
		if responseType == "response.incomplete" {
			_ = templateDoc.Set("candidates.0.finishReason", codexGeminiIncompleteFinishReason(responseData.Get("incomplete_details.reason").String()))
		}
		// Set response ID
		if responseId := responseData.Get("id"); responseId.Exists() {
			_ = templateDoc.Set("responseId", responseId.String())
		}

		// Set creation time
		if createdAt := responseData.Get("created_at"); createdAt.Exists() {
			_ = templateDoc.Set("createTime", time.Unix(createdAt.Int(), 0).Format(time.RFC3339Nano))
		}

		// Set usage metadata
		if usage := responseData.Get("usage"); usage.Exists() {
			inputTokens := usage.Get("input_tokens").Int()
			outputTokens := usage.Get("output_tokens").Int()
			totalTokens := inputTokens + outputTokens

			_ = templateDoc.Set("usageMetadata.promptTokenCount", inputTokens)
			_ = templateDoc.Set("usageMetadata.candidatesTokenCount", outputTokens)
			_ = templateDoc.Set("usageMetadata.totalTokenCount", totalTokens)
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
			output.ForEach(func(key, value ojson.Result) bool {
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
						partDoc := ojson.NewDocString(`{"text":"","thought":true}`)
						_ = partDoc.Set("text", thoughtText)
						if i == len(thoughtTexts)-1 && signature != "" {
							setGeminiThoughtSignatureDoc(partDoc, signature)
						}
						parts = append(parts, partDoc.Bytes())
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
						content.ForEach(func(_, contentItem ojson.Result) bool {
							if contentItem.Get("type").String() == "output_text" {
								if text := contentItem.Get("text"); text.Exists() {
									part := []byte(`{"text":""}`)
									part, _ = ojson.SetBytes(part, "text", text.String())
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

					partDoc := ojson.NewDocString(`{"inlineData":{"data":"","mimeType":""}}`)
					_ = partDoc.Set("inlineData.data", b64)
					_ = partDoc.Set("inlineData.mimeType", mimeType)
					parts = append(parts, partDoc.Bytes())

				case "function_call":
					// Collect function call for potential merging with consecutive ones
					functionCallDoc := ojson.NewDocString(`{"functionCall":{"args":{},"name":""}}`)
					{
						n := value.Get("name").String()
						rev := buildReverseMapFromGeminiOriginal(originalRequestRawJSON)
						if orig, ok := rev[n]; ok {
							n = orig
						}
						_ = functionCallDoc.Set("functionCall.name", n)
					}

					// Parse and set arguments
					if argsStr := value.Get("arguments").String(); argsStr != "" {
						argsResult := ojson.Parse(argsStr)
						if argsResult.IsObject() {
							_ = functionCallDoc.SetRawBytes("functionCall.args", []byte(argsStr))
						}
					}
					setGeminiFunctionCallIDDoc(functionCallDoc, value)

					pendingFunctionCalls = append(pendingFunctionCalls, functionCallDoc.Bytes())
				}
				return true
			})

			// Handle any remaining pending function calls at the end
			flushPendingFunctionCalls()

			if len(parts) > 0 {
				_ = templateDoc.SetRawBytes("candidates.0.content.parts", translatorcommon.JoinRawArray(parts))
			}
		}
	}
	return templateDoc.Bytes()
}

// buildReverseMapFromGeminiOriginal builds a map[short]original from original Gemini request tools.
func buildReverseMapFromGeminiOriginal(original []byte) map[string]string {
	tools := ojson.GetBytesNoCopy(original, "tools")
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
func extractCodexGeminiReasoningTexts(value ojson.Result) []string {
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
	} else if content.Type == ojson.String && content.String() != "" {
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
	part, _ = ojson.SetBytes(part, "thoughtSignature", encoded)
	return part
}

func setGeminiThoughtSignatureDoc(doc *ojson.Doc, rawSignature string) {
	encoded := encodeGeminiCodexThoughtSignature(rawSignature)
	if encoded == "" {
		return
	}
	_ = doc.Set("thoughtSignature", encoded)
}

func setGeminiFunctionCallIDDoc(doc *ojson.Doc, item ojson.Result) {
	if callID := strings.TrimSpace(item.Get("call_id").String()); callID != "" {
		_ = doc.Set("functionCall.id", callID)
		return
	}
	if id := strings.TrimSpace(item.Get("id").String()); id != "" {
		_ = doc.Set("functionCall.id", id)
	}
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
