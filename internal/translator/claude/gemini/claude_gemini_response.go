// Package gemini provides response translation functionality for Claude Code to Gemini API compatibility.
// This package handles the conversion of Claude Code API responses into Gemini-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Gemini API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, and usage metadata appropriately.
package gemini

import (
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

var (
	dataTag = []byte("data:")
)

// ConvertAnthropicResponseToGeminiParams holds parameters for response conversion
// It also carries minimal streaming state across calls to assemble tool_use input_json_delta.
// This structure maintains state information needed for proper conversion of streaming responses
// from Claude Code format to Gemini format, particularly for handling tool calls that span
// multiple streaming events.
type ConvertAnthropicResponseToGeminiParams struct {
	Model             string
	CreatedAt         int64
	ResponseID        string
	LastStorageOutput []byte
	IsStreaming       bool

	// Streaming state for tool_use assembly
	// Keyed by content_block index from Claude SSE events
	ToolUseNames map[int]string           // function/tool name per block index
	ToolUseArgs  map[int]*strings.Builder // accumulates partial_json across deltas
	ToolUseIDs   map[int]string           // tool use ID per block index
}

// ConvertClaudeResponseToGemini converts Claude Code streaming response format to Gemini format.
// This function processes various Claude Code event types and transforms them into Gemini-compatible JSON responses.
// It handles text content, tool calls, reasoning content, and usage metadata, outputting responses that match
// the Gemini API format. The function supports incremental updates for streaming responses and maintains
// state information to properly assemble multi-part tool calls.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Claude Code API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - [][]byte: A slice of Gemini-compatible JSON responses
func ConvertClaudeResponseToGemini(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &ConvertAnthropicResponseToGeminiParams{
			Model:      modelName,
			CreatedAt:  0,
			ResponseID: "",
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	root := util.ParseGJSONBytesNoCopy(rawJSON)
	eventType := root.Get("type").String()

	// Base Gemini response template with default values
	templateDoc := ojson.NewDoc([]byte(`{"candidates":[{"content":{"role":"model","parts":[]}}],"usageMetadata":{"trafficType":"PROVISIONED_THROUGHPUT"},"modelVersion":"","createTime":"","responseId":""}`))

	// Set model version
	if (*param).(*ConvertAnthropicResponseToGeminiParams).Model != "" {
		// Map Claude model names back to Gemini model names
		_ = templateDoc.Set("modelVersion", (*param).(*ConvertAnthropicResponseToGeminiParams).Model)
	}

	// Set response ID and creation time
	if (*param).(*ConvertAnthropicResponseToGeminiParams).ResponseID != "" {
		_ = templateDoc.Set("responseId", (*param).(*ConvertAnthropicResponseToGeminiParams).ResponseID)
	}

	// Set creation time to current time if not provided
	if (*param).(*ConvertAnthropicResponseToGeminiParams).CreatedAt == 0 {
		(*param).(*ConvertAnthropicResponseToGeminiParams).CreatedAt = time.Now().Unix()
	}
	_ = templateDoc.Set("createTime", time.Unix((*param).(*ConvertAnthropicResponseToGeminiParams).CreatedAt, 0).Format(time.RFC3339Nano))

	switch eventType {
	case "message_start":
		// Initialize response with message metadata when a new message begins
		if message := root.Get("message"); message.Exists() {
			(*param).(*ConvertAnthropicResponseToGeminiParams).ResponseID = strings.Clone(message.Get("id").String())
			(*param).(*ConvertAnthropicResponseToGeminiParams).Model = strings.Clone(message.Get("model").String())
		}
		return [][]byte{}

	case "content_block_start":
		// Start of a content block - record tool_use name by index for functionCall assembly
		if cb := root.Get("content_block"); cb.Exists() {
			if cb.Get("type").String() == "tool_use" {
				idx := int(root.Get("index").Int())
				if (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseNames == nil {
					(*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseNames = map[int]string{}
				}
				if name := cb.Get("name"); name.Exists() {
					(*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseNames[idx] = strings.Clone(name.String())
				}
				if toolID := cb.Get("id").String(); toolID != "" {
					if (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseIDs == nil {
						(*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseIDs = map[int]string{}
					}
					(*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseIDs[idx] = strings.Clone(toolID)
				}
			} else if cb.Get("type").String() == "thinking" {
				if sig := cb.Get("signature"); sig.Exists() && sig.String() != "" {
					thinkingPart := []byte(`{"thought":true,"thoughtSignature":""}`)
					thinkingPart, _ = ojson.SetBytes(thinkingPart, "thoughtSignature", sigcompat.GeminiReplaySignatureOrBypass(sig.String(), sigcompat.SignatureBlockKindGeminiModelPart))
					_ = templateDoc.SetRawBytes("candidates.0.content.parts.-1", thinkingPart)
					return [][]byte{templateDoc.Bytes()}
				}
			}
		}
		return [][]byte{}

	case "content_block_delta":
		// Handle content delta (text, thinking, or tool use arguments)
		if delta := root.Get("delta"); delta.Exists() {
			deltaType := delta.Get("type").String()

			switch deltaType {
			case "text_delta":
				// Regular text content delta for normal response text
				if text := delta.Get("text"); text.Exists() && text.String() != "" {
					textPart := []byte(`{"text":""}`)
					textPart, _ = ojson.SetBytes(textPart, "text", text.String())
					_ = templateDoc.SetRawBytes("candidates.0.content.parts.-1", textPart)
				}
			case "thinking_delta":
				// Thinking/reasoning content delta for models with reasoning capabilities
				if text := delta.Get("thinking"); text.Exists() && text.String() != "" {
					thinkingPart := []byte(`{"thought":true,"text":""}`)
					thinkingPart, _ = ojson.SetBytes(thinkingPart, "text", text.String())
					_ = templateDoc.SetRawBytes("candidates.0.content.parts.-1", thinkingPart)
				}
			case "signature_delta":
				if sig := delta.Get("signature"); sig.Exists() && sig.String() != "" {
					thinkingPart := []byte(`{"thought":true,"thoughtSignature":""}`)
					thinkingPart, _ = ojson.SetBytes(thinkingPart, "thoughtSignature", sigcompat.GeminiReplaySignatureOrBypass(sig.String(), sigcompat.SignatureBlockKindGeminiModelPart))
					_ = templateDoc.SetRawBytes("candidates.0.content.parts.-1", thinkingPart)
				}
			case "input_json_delta":
				// Tool use input delta - accumulate partial_json by index for later assembly at content_block_stop
				idx := int(root.Get("index").Int())
				if (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs == nil {
					(*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs = map[int]*strings.Builder{}
				}
				b, ok := (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs[idx]
				if !ok || b == nil {
					bb := &strings.Builder{}
					(*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs[idx] = bb
					b = bb
				}
				if pj := delta.Get("partial_json"); pj.Exists() {
					b.WriteString(pj.String())
				}
				return [][]byte{}
			}
		}
		return [][]byte{templateDoc.Bytes()}

	case "content_block_stop":
		// End of content block - finalize tool calls if any
		idx := int(root.Get("index").Int())
		// Claude's content_block_stop often doesn't include content_block payload (see docs/response-claude.txt)
		// So we finalize using accumulated state captured during content_block_start and input_json_delta.
		name := ""
		if (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseNames != nil {
			name = (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseNames[idx]
		}
		var argsTrim string
		if (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs != nil {
			if b := (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs[idx]; b != nil {
				argsTrim = strings.TrimSpace(b.String())
			}
		}
		toolID := ""
		if (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseIDs != nil {
			toolID = (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseIDs[idx]
		}
		if name != "" || argsTrim != "" {
			fcDoc := ojson.NewDoc([]byte(`{"functionCall":{"name":"","args":{}}}`))
			if name != "" {
				_ = fcDoc.Set("functionCall.name", name)
			}
			if argsTrim != "" {
				_ = fcDoc.SetRaw("functionCall.args", argsTrim)
			}
			if toolID != "" {
				_ = fcDoc.Set("functionCall.id", toolID)
			}
			_ = templateDoc.SetRawBytes("candidates.0.content.parts.-1", fcDoc.Bytes())
			_ = templateDoc.Set("candidates.0.finishReason", "STOP")
			outBytes := templateDoc.Bytes()
			(*param).(*ConvertAnthropicResponseToGeminiParams).LastStorageOutput = append([]byte(nil), outBytes...)
			// cleanup used state for this index
			if (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs != nil {
				delete((*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs, idx)
			}
			if (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseNames != nil {
				delete((*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseNames, idx)
			}
			if (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseIDs != nil {
				delete((*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseIDs, idx)
			}
			return [][]byte{outBytes}
		}
		return [][]byte{}

	case "message_delta":
		// Handle message-level changes (like stop reason and usage information)
		if delta := root.Get("delta"); delta.Exists() {
			if stopReason := delta.Get("stop_reason"); stopReason.Exists() {
				switch stopReason.String() {
				case "end_turn":
					_ = templateDoc.Set("candidates.0.finishReason", "STOP")
				case "tool_use":
					_ = templateDoc.Set("candidates.0.finishReason", "STOP")
				case "max_tokens":
					_ = templateDoc.Set("candidates.0.finishReason", "MAX_TOKENS")
				case "stop_sequence":
					_ = templateDoc.Set("candidates.0.finishReason", "STOP")
				default:
					_ = templateDoc.Set("candidates.0.finishReason", "STOP")
				}
			}
		}

		if usage := root.Get("usage"); usage.Exists() {
			// Basic token counts for prompt and completion
			inputTokens := usage.Get("input_tokens").Int()
			outputTokens := usage.Get("output_tokens").Int()

			// Set basic usage metadata according to Gemini API specification
			_ = templateDoc.Set("usageMetadata.promptTokenCount", inputTokens)
			_ = templateDoc.Set("usageMetadata.candidatesTokenCount", outputTokens)
			_ = templateDoc.Set("usageMetadata.totalTokenCount", inputTokens+outputTokens)

			// Add cache-related token counts if present (Claude Code API cache fields)
			if cacheCreationTokens := usage.Get("cache_creation_input_tokens"); cacheCreationTokens.Exists() {
				_ = templateDoc.Set("usageMetadata.cachedContentTokenCount", cacheCreationTokens.Int())
			}
			if cacheReadTokens := usage.Get("cache_read_input_tokens"); cacheReadTokens.Exists() {
				// Add cache read tokens to cached content count
				existingCacheTokens := usage.Get("cache_creation_input_tokens").Int()
				totalCacheTokens := existingCacheTokens + cacheReadTokens.Int()
				_ = templateDoc.Set("usageMetadata.cachedContentTokenCount", totalCacheTokens)
			}

			// Add thinking tokens if present (for models with reasoning capabilities)
			if thinkingTokens := usage.Get("thinking_tokens"); thinkingTokens.Exists() {
				_ = templateDoc.Set("usageMetadata.thoughtsTokenCount", thinkingTokens.Int())
			}

			// Set traffic type (required by Gemini API)
			_ = templateDoc.Set("usageMetadata.trafficType", "PROVISIONED_THROUGHPUT")
		}
		_ = templateDoc.Set("candidates.0.finishReason", "STOP")

		return [][]byte{templateDoc.Bytes()}
	case "message_stop":
		// Final message with usage information - no additional output needed
		return [][]byte{}
	case "error":
		// Handle error responses and convert to Gemini error format
		errorMsg := root.Get("error.message").String()
		if errorMsg == "" {
			errorMsg = "Unknown error occurred"
		}

		// Create error response in Gemini format
		errorResponse := []byte(`{"error":{"code":400,"message":"","status":"INVALID_ARGUMENT"}}`)
		errorResponse, _ = ojson.SetBytes(errorResponse, "error.message", errorMsg)
		return [][]byte{errorResponse}

	default:
		// Unknown event type, return empty response
		return [][]byte{}
	}
}

// ConvertClaudeResponseToGeminiNonStream converts a non-streaming Claude Code response to a non-streaming Gemini response.
// This function processes the complete Claude Code response and transforms it into a single Gemini-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the Gemini API format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Claude Code API
//   - param: A pointer to a parameter object for the conversion (unused in current implementation)
//
// Returns:
//   - []byte: A Gemini-compatible JSON response containing all message content and metadata
func ConvertClaudeResponseToGeminiNonStream(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	// Base Gemini response template for non-streaming with default values
	templateDoc := ojson.NewDoc([]byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"trafficType":"PROVISIONED_THROUGHPUT"},"modelVersion":"","createTime":"","responseId":""}`))

	// Set model version
	_ = templateDoc.Set("modelVersion", modelName)

	streamingEvents := make([][]byte, 0)
	remaining := rawJSON
	for len(remaining) > 0 {
		var line []byte
		idx := bytes.IndexByte(remaining, '\n')
		if idx >= 0 {
			line = remaining[:idx]
			remaining = remaining[idx+1:]
		} else {
			line = remaining
			remaining = nil
		}
		line = bytes.TrimRight(line, "\r")
		if bytes.HasPrefix(line, dataTag) {
			jsonData := bytes.TrimSpace(line[5:])
			streamingEvents = append(streamingEvents, jsonData)
		}
	}
	// log.Debug("streamingEvents: ", streamingEvents)
	// log.Debug("rawJSON: ", string(rawJSON))

	// Initialize parameters for streaming conversion with proper state management
	newParam := &ConvertAnthropicResponseToGeminiParams{
		Model:             modelName,
		CreatedAt:         0,
		ResponseID:        "",
		LastStorageOutput: nil,
		IsStreaming:       false,
		ToolUseNames:      nil,
		ToolUseArgs:       nil,
		ToolUseIDs:        nil,
	}

	// Process each streaming event and collect parts
	var allParts [][]byte
	var finalUsageJSON []byte
	var responseID string
	var createdAt int64

	for _, eventData := range streamingEvents {
		if len(eventData) == 0 {
			continue
		}

		root := util.ParseGJSONBytesNoCopy(eventData)
		eventType := root.Get("type").String()

		switch eventType {
		case "message_start":
			// Extract response metadata including ID, model, and creation time
			if message := root.Get("message"); message.Exists() {
				responseID = message.Get("id").String()
				newParam.ResponseID = responseID
				newParam.Model = message.Get("model").String()

				// Set creation time to current time if not provided
				createdAt = time.Now().Unix()
				newParam.CreatedAt = createdAt
			}

		case "content_block_start":
			// Prepare for content block; record tool_use name by index for later functionCall assembly
			idx := int(root.Get("index").Int())
			if cb := root.Get("content_block"); cb.Exists() {
				if cb.Get("type").String() == "tool_use" {
					if newParam.ToolUseNames == nil {
						newParam.ToolUseNames = map[int]string{}
					}
					if name := cb.Get("name"); name.Exists() {
						newParam.ToolUseNames[idx] = name.String()
					}
					if toolID := cb.Get("id").String(); toolID != "" {
						if newParam.ToolUseIDs == nil {
							newParam.ToolUseIDs = map[int]string{}
						}
						newParam.ToolUseIDs[idx] = toolID
					}
				} else if cb.Get("type").String() == "thinking" {
					if sig := cb.Get("signature"); sig.Exists() && sig.String() != "" {
						partJSON := []byte(`{"thought":true,"thoughtSignature":""}`)
						partJSON, _ = ojson.SetBytes(partJSON, "thoughtSignature", sigcompat.GeminiReplaySignatureOrBypass(sig.String(), sigcompat.SignatureBlockKindGeminiModelPart))
						allParts = append(allParts, partJSON)
					}
				}
			}
			continue

		case "content_block_delta":
			// Handle content delta (text, thinking, or tool input)
			if delta := root.Get("delta"); delta.Exists() {
				deltaType := delta.Get("type").String()
				switch deltaType {
				case "text_delta":
					// Process regular text content
					if text := delta.Get("text"); text.Exists() && text.String() != "" {
						partJSON := []byte(`{"text":""}`)
						partJSON, _ = ojson.SetBytes(partJSON, "text", text.String())
						allParts = append(allParts, partJSON)
					}
				case "thinking_delta":
					// Process reasoning/thinking content
					if text := delta.Get("thinking"); text.Exists() && text.String() != "" {
						partJSON := []byte(`{"thought":true,"text":""}`)
						partJSON, _ = ojson.SetBytes(partJSON, "text", text.String())
						allParts = append(allParts, partJSON)
					}
				case "signature_delta":
					if sig := delta.Get("signature"); sig.Exists() && sig.String() != "" {
						partJSON := []byte(`{"thought":true,"thoughtSignature":""}`)
						partJSON, _ = ojson.SetBytes(partJSON, "thoughtSignature", sigcompat.GeminiReplaySignatureOrBypass(sig.String(), sigcompat.SignatureBlockKindGeminiModelPart))
						allParts = append(allParts, partJSON)
					}
				case "input_json_delta":
					// accumulate args partial_json for this index
					idx := int(root.Get("index").Int())
					if newParam.ToolUseArgs == nil {
						newParam.ToolUseArgs = map[int]*strings.Builder{}
					}
					if _, ok := newParam.ToolUseArgs[idx]; !ok || newParam.ToolUseArgs[idx] == nil {
						newParam.ToolUseArgs[idx] = &strings.Builder{}
					}
					if pj := delta.Get("partial_json"); pj.Exists() {
						newParam.ToolUseArgs[idx].WriteString(pj.String())
					}
				}
			}

		case "content_block_stop":
			// Handle tool use completion by assembling accumulated arguments
			idx := int(root.Get("index").Int())
			// Claude's content_block_stop often doesn't include content_block payload (see docs/response-claude.txt)
			// So we finalize using accumulated state captured during content_block_start and input_json_delta.
			name := ""
			if newParam.ToolUseNames != nil {
				name = newParam.ToolUseNames[idx]
			}
			var argsTrim string
			if newParam.ToolUseArgs != nil {
				if b := newParam.ToolUseArgs[idx]; b != nil {
					argsTrim = strings.TrimSpace(b.String())
				}
			}
			toolID := ""
			if newParam.ToolUseIDs != nil {
				toolID = newParam.ToolUseIDs[idx]
			}
			if name != "" || argsTrim != "" {
				fcDoc := ojson.NewDoc([]byte(`{"functionCall":{"name":"","args":{}}}`))
				if name != "" {
					_ = fcDoc.Set("functionCall.name", name)
				}
				if argsTrim != "" {
					_ = fcDoc.SetRaw("functionCall.args", argsTrim)
				}
				if toolID != "" {
					_ = fcDoc.Set("functionCall.id", toolID)
				}
				allParts = append(allParts, fcDoc.Bytes())
				// cleanup used state for this index
				if newParam.ToolUseArgs != nil {
					delete(newParam.ToolUseArgs, idx)
				}
				if newParam.ToolUseNames != nil {
					delete(newParam.ToolUseNames, idx)
				}
				if newParam.ToolUseIDs != nil {
					delete(newParam.ToolUseIDs, idx)
				}
			}

		case "message_delta":
			// Extract final usage information using sjson for token counts and metadata
			if usage := root.Get("usage"); usage.Exists() {
				usageDoc := ojson.NewDoc([]byte(`{}`))

				// Basic token counts for prompt and completion
				inputTokens := usage.Get("input_tokens").Int()
				outputTokens := usage.Get("output_tokens").Int()

				// Set basic usage metadata according to Gemini API specification
				_ = usageDoc.Set("promptTokenCount", inputTokens)
				_ = usageDoc.Set("candidatesTokenCount", outputTokens)
				_ = usageDoc.Set("totalTokenCount", inputTokens+outputTokens)

				// Add cache-related token counts if present (Claude Code API cache fields)
				if cacheCreationTokens := usage.Get("cache_creation_input_tokens"); cacheCreationTokens.Exists() {
					_ = usageDoc.Set("cachedContentTokenCount", cacheCreationTokens.Int())
				}
				if cacheReadTokens := usage.Get("cache_read_input_tokens"); cacheReadTokens.Exists() {
					// Add cache read tokens to cached content count
					existingCacheTokens := usage.Get("cache_creation_input_tokens").Int()
					totalCacheTokens := existingCacheTokens + cacheReadTokens.Int()
					_ = usageDoc.Set("cachedContentTokenCount", totalCacheTokens)
				}

				// Add thinking tokens if present (for models with reasoning capabilities)
				if thinkingTokens := usage.Get("thinking_tokens"); thinkingTokens.Exists() {
					_ = usageDoc.Set("thoughtsTokenCount", thinkingTokens.Int())
				}

				// Set traffic type (required by Gemini API)
				_ = usageDoc.Set("trafficType", "PROVISIONED_THROUGHPUT")

				finalUsageJSON = usageDoc.Bytes()
			}
		}
	}

	// Set response metadata
	if responseID != "" {
		_ = templateDoc.Set("responseId", responseID)
	}
	if createdAt > 0 {
		_ = templateDoc.Set("createTime", time.Unix(createdAt, 0).Format(time.RFC3339Nano))
	}

	// Consolidate consecutive text parts and thinking parts for cleaner output
	consolidatedParts := consolidateParts(allParts)

	// Set the consolidated parts array
	if len(consolidatedParts) > 0 {
		_ = templateDoc.SetRawBytes("candidates.0.content.parts", translatorcommon.JoinRawArray(consolidatedParts))
	}

	// Set usage metadata
	if len(finalUsageJSON) > 0 {
		_ = templateDoc.SetRawBytes("usageMetadata", finalUsageJSON)
	}

	return templateDoc.Bytes()
}

func GeminiTokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.GeminiTokenCountJSON(count)
}

// consolidateParts merges consecutive text parts and thinking parts to create a cleaner response.
// This function processes the parts array to combine adjacent text elements and thinking elements
// into single consolidated parts, which results in a more readable and efficient response structure.
// Tool calls and other non-text parts are preserved as separate elements.
func consolidateParts(parts [][]byte) [][]byte {
	if len(parts) == 0 {
		return parts
	}

	var consolidated [][]byte
	var currentTextPart strings.Builder
	var currentThoughtPart strings.Builder
	var currentThoughtSignature string
	var hasText, hasThought bool

	flushText := func() {
		// Flush accumulated text content to the consolidated parts array
		if hasText && currentTextPart.Len() > 0 {
			textPartJSON := []byte(`{"text":""}`)
			textPartJSON, _ = ojson.SetBytes(textPartJSON, "text", currentTextPart.String())
			consolidated = append(consolidated, textPartJSON)
			currentTextPart.Reset()
			hasText = false
		}
	}

	flushThought := func() {
		// Flush accumulated thinking content to the consolidated parts array
		if hasThought && (currentThoughtPart.Len() > 0 || currentThoughtSignature != "") {
			thoughtPartDoc := ojson.NewDoc([]byte(`{"thought":true,"text":""}`))
			_ = thoughtPartDoc.Set("text", currentThoughtPart.String())
			if currentThoughtSignature != "" {
				_ = thoughtPartDoc.Set("thoughtSignature", currentThoughtSignature)
			}
			consolidated = append(consolidated, thoughtPartDoc.Bytes())
			currentThoughtPart.Reset()
			currentThoughtSignature = ""
			hasThought = false
		}
	}

	for _, partJSON := range parts {
		part := util.ParseGJSONBytesNoCopy(partJSON)
		if !part.Exists() || !part.IsObject() {
			// Flush any pending parts and add this non-text part
			flushText()
			flushThought()
			consolidated = append(consolidated, partJSON)
			continue
		}

		thought := part.Get("thought")
		if thought.Exists() && thought.Type == ojson.True {
			// This is a thinking part - flush any pending text first
			flushText() // Flush any pending text first

			if text := part.Get("text"); text.Exists() && text.Type == ojson.String {
				currentThoughtPart.WriteString(text.String())
				hasThought = true
			}
			if sig := part.Get("thoughtSignature"); sig.Exists() && sig.Type == ojson.String && sig.String() != "" {
				currentThoughtSignature = sig.String()
				hasThought = true
			}
		} else if text := part.Get("text"); text.Exists() && text.Type == ojson.String {
			// This is a regular text part - flush any pending thought first
			flushThought() // Flush any pending thought first

			currentTextPart.WriteString(text.String())
			hasText = true
		} else {
			// This is some other type of part (like function call) - flush both text and thought
			flushText()
			flushThought()
			consolidated = append(consolidated, partJSON)
		}
	}

	// Flush any remaining parts
	flushThought() // Flush thought first to maintain order
	flushText()

	return consolidated
}
