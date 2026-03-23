// Package claude provides response translation functionality for Claude Code API compatibility.
// This package handles the conversion of backend client responses into Claude Code-compatible
// Server-Sent Events (SSE) format, implementing a sophisticated state machine that manages
// different response types including text content, thinking processes, and function calls.
// The translation ensures proper sequencing of SSE events and maintains state across
// multiple response chunks to provide a seamless streaming experience.
package claude

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Params holds parameters for response conversion and maintains state across streaming chunks.
// This structure tracks the current state of the response translation process to ensure
// proper sequencing of SSE events and transitions between different content types.
type Params struct {
	HasFirstResponse     bool   // Indicates if the initial message_start event has been sent
	ResponseType         int    // Current response type: 0=none, 1=content, 2=thinking, 3=function
	ResponseIndex        int    // Index counter for content blocks in the streaming response
	HasFinishReason      bool   // Tracks whether a finish reason has been observed
	FinishReason         string // The finish reason string returned by the provider
	HasUsageMetadata     bool   // Tracks whether usage metadata has been observed
	PromptTokenCount     int64  // Cached prompt token count from usage metadata
	CandidatesTokenCount int64  // Cached candidate token count from usage metadata
	ThoughtsTokenCount   int64  // Cached thinking token count from usage metadata
	TotalTokenCount      int64  // Cached total token count from usage metadata
	CachedTokenCount     int64  // Cached content token count (indicates prompt caching)
	HasSentFinalEvents   bool   // Indicates if final content/message events have been sent
	HasToolUse           bool   // Indicates if tool use was observed in the stream
	HasContent           bool   // Tracks whether any content (text, thinking, or tool use) has been output

	// Signature caching support
	CurrentThinkingText strings.Builder // Accumulates thinking text for signature caching

	// Web search support
	WebSearchQuery   string
	WebSearchResults []map[string]any
	WebSearchEmitted bool

	// Reverse map: sanitized Gemini function name → original Claude tool name.
	// Populated lazily on the first response chunk from the original request JSON.
	ToolNameMap map[string]string
}

// toolUseIDCounter provides a process-wide unique counter for tool use identifiers.
var toolUseIDCounter uint64

// ConvertAntigravityResponseToClaude performs sophisticated streaming response format conversion.
// This function implements a complex state machine that translates backend client responses
// into Claude Code-compatible Server-Sent Events (SSE) format. It manages different response types
// and handles state transitions between content blocks, thinking processes, and function calls.
//
// Response type states: 0=none, 1=content, 2=thinking, 3=function
// The function maintains state across multiple calls to ensure proper SSE event sequencing.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Gemini CLI API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - [][]byte: A slice of bytes, each containing a Claude Code-compatible SSE payload.
func ConvertAntigravityResponseToClaude(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &Params{
			HasFirstResponse: false,
			ResponseType:     0,
			ResponseIndex:    0,
			ToolNameMap:      util.SanitizedToolNameMap(originalRequestRawJSON),
		}
	}
	modelName := gjson.GetBytes(requestRawJSON, "model").String()

	params := (*param).(*Params)

	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		output := make([]byte, 0, 256)
		// Only send final events if we have actually output content
		if params.HasContent {
			appendFinalEvents(params, &output, true)
			output = translatorcommon.AppendSSEEventString(output, "message_stop", `{"type":"message_stop"}`, 3)
			return [][]byte{output}
		}
		return [][]byte{}
	}

	output := make([]byte, 0, 1024)
	appendEvent := func(event, payload string) {
		output = translatorcommon.AppendSSEEventString(output, event, payload, 3)
	}

	// Initialize the streaming session with a message_start event
	// This is only sent for the very first response chunk to establish the streaming session
	if !params.HasFirstResponse {
		// Create the initial message structure with default values according to Claude Code API specification
		// This follows the Claude Code API specification for streaming message initialization
		messageStartTemplate := []byte(`{"type": "message_start", "message": {"id": "msg_1nZdL29xx5MUA1yADyHTEsnR8uuvGzszyY", "type": "message", "role": "assistant", "content": [], "model": "claude-3-5-sonnet-20241022", "stop_reason": null, "stop_sequence": null, "usage": {"input_tokens": 0, "output_tokens": 0}}}`)

		// Use cpaUsageMetadata within the message_start event for Claude.
		if promptTokenCount := gjson.GetBytes(rawJSON, "response.cpaUsageMetadata.promptTokenCount"); promptTokenCount.Exists() {
			messageStartTemplate, _ = sjson.SetBytes(messageStartTemplate, "message.usage.input_tokens", promptTokenCount.Int())
		}
		if candidatesTokenCount := gjson.GetBytes(rawJSON, "response.cpaUsageMetadata.candidatesTokenCount"); candidatesTokenCount.Exists() {
			messageStartTemplate, _ = sjson.SetBytes(messageStartTemplate, "message.usage.output_tokens", candidatesTokenCount.Int())
		}

		// Override default values with actual response metadata if available from the Gemini CLI response
		if modelVersionResult := gjson.GetBytes(rawJSON, "response.modelVersion"); modelVersionResult.Exists() {
			messageStartTemplate, _ = sjson.SetBytes(messageStartTemplate, "message.model", modelVersionResult.String())
		}
		if responseIDResult := gjson.GetBytes(rawJSON, "response.responseId"); responseIDResult.Exists() {
			messageStartTemplate, _ = sjson.SetBytes(messageStartTemplate, "message.id", responseIDResult.String())
		}
		appendEvent("message_start", string(messageStartTemplate))

		params.HasFirstResponse = true
	}

	// Process the response parts array from the backend client
	// Each part can contain text content, thinking content, or function calls
	partsResult := gjson.GetBytes(rawJSON, "response.candidates.0.content.parts")
	if partsResult.IsArray() {
		partResults := partsResult.Array()
		for i := 0; i < len(partResults); i++ {
			partResult := partResults[i]

			// Extract the different types of content from each part
			partTextResult := partResult.Get("text")
			functionCallResult := partResult.Get("functionCall")

			// Handle text content (both regular content and thinking)
			if partTextResult.Exists() {
				// Process thinking content (internal reasoning)
				if partResult.Get("thought").Bool() {
					if thoughtSignature := partResult.Get("thoughtSignature"); thoughtSignature.Exists() && thoughtSignature.String() != "" {
						// log.Debug("Branch: signature_delta")

						if params.CurrentThinkingText.Len() > 0 {
							cache.CacheSignature(modelName, params.CurrentThinkingText.String(), thoughtSignature.String())
							// log.Debugf("Cached signature for thinking block (textLen=%d)", params.CurrentThinkingText.Len())
							params.CurrentThinkingText.Reset()
						}

						data, _ := sjson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":""}}`, params.ResponseIndex)), "delta.signature", fmt.Sprintf("%s#%s", cache.GetModelGroup(modelName), thoughtSignature.String()))
						appendEvent("content_block_delta", string(data))
						params.HasContent = true
					} else if params.ResponseType == 2 { // Continue existing thinking block if already in thinking state
						params.CurrentThinkingText.WriteString(partTextResult.String())
						data, _ := sjson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":""}}`, params.ResponseIndex)), "delta.thinking", partTextResult.String())
						appendEvent("content_block_delta", string(data))
						params.HasContent = true
					} else {
						// Transition from another state to thinking
						// First, close any existing content block
						if params.ResponseType != 0 {
							if params.ResponseType == 2 {
								// output = output + "event: content_block_delta\n"
								// output = output + fmt.Sprintf(`data: {"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":null}}`, params.ResponseIndex)
								// output = output + "\n\n\n"
							}
							appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, params.ResponseIndex))
							params.ResponseIndex++
						}

						// Start a new thinking content block
						appendEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":""}}`, params.ResponseIndex))
						data, _ := sjson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":""}}`, params.ResponseIndex)), "delta.thinking", partTextResult.String())
						appendEvent("content_block_delta", string(data))
						params.ResponseType = 2 // Set state to thinking
						params.HasContent = true
						// Start accumulating thinking text for signature caching
						params.CurrentThinkingText.Reset()
						params.CurrentThinkingText.WriteString(partTextResult.String())
					}
				} else {
					finishReasonResult := gjson.GetBytes(rawJSON, "response.candidates.0.finishReason")
					if partTextResult.String() != "" || !finishReasonResult.Exists() {
						// Process regular text content (user-visible output)
						// Continue existing text block if already in content state
						if params.ResponseType == 1 {
							data, _ := sjson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":""}}`, params.ResponseIndex)), "delta.text", partTextResult.String())
							appendEvent("content_block_delta", string(data))
							params.HasContent = true
						} else {
							// Transition from another state to text content
							// First, close any existing content block
							if params.ResponseType != 0 {
								if params.ResponseType == 2 {
									// output = output + "event: content_block_delta\n"
									// output = output + fmt.Sprintf(`data: {"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":null}}`, params.ResponseIndex)
									// output = output + "\n\n\n"
								}
								appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, params.ResponseIndex))
								params.ResponseIndex++
							}
							if partTextResult.String() != "" {
								// Start a new text content block
								appendEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, params.ResponseIndex))
								data, _ := sjson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":""}}`, params.ResponseIndex)), "delta.text", partTextResult.String())
								appendEvent("content_block_delta", string(data))
								params.ResponseType = 1 // Set state to content
								params.HasContent = true
							}
						}
					}
				}
			} else if functionCallResult.Exists() {
				// Handle function/tool calls from the AI model
				// This processes tool usage requests and formats them for Claude Code API compatibility
				params.HasToolUse = true
				fcName := util.RestoreSanitizedToolName(params.ToolNameMap, functionCallResult.Get("name").String())

				// Handle state transitions when switching to function calls
				// Close any existing function call block first
				if params.ResponseType == 3 {
					appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, params.ResponseIndex))
					params.ResponseIndex++
					params.ResponseType = 0
				}

				// Special handling for thinking state transition
				if params.ResponseType == 2 {
					// output = output + "event: content_block_delta\n"
					// output = output + fmt.Sprintf(`data: {"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":null}}`, params.ResponseIndex)
					// output = output + "\n\n\n"
				}

				// Close any other existing content block
				if params.ResponseType != 0 {
					appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, params.ResponseIndex))
					params.ResponseIndex++
				}

				// Start a new tool use content block
				// This creates the structure for a function call in Claude Code format
				// Create the tool use block with unique ID and function details
				data := []byte(fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`, params.ResponseIndex))
				data, _ = sjson.SetBytes(data, "content_block.id", util.SanitizeClaudeToolID(fmt.Sprintf("%s-%d-%d", fcName, time.Now().UnixNano(), atomic.AddUint64(&toolUseIDCounter, 1))))
				data, _ = sjson.SetBytes(data, "content_block.name", fcName)
				appendEvent("content_block_start", string(data))

				if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
					data, _ = sjson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, params.ResponseIndex)), "delta.partial_json", fcArgsResult.Raw)
					appendEvent("content_block_delta", string(data))
				}
				params.ResponseType = 3
				params.HasContent = true
			}
		}
	}

	if q, results := extractWebSearchFromAntigravity(rawJSON); q != "" || len(results) > 0 {
		if q != "" {
			params.WebSearchQuery = q
		}
		if len(results) > 0 {
			params.WebSearchResults = results
		}
	}

	if finishReasonResult := gjson.GetBytes(rawJSON, "response.candidates.0.finishReason"); finishReasonResult.Exists() {
		params.HasFinishReason = true
		params.FinishReason = finishReasonResult.String()
	}

	if usageResult := gjson.GetBytes(rawJSON, "response.usageMetadata"); usageResult.Exists() {
		params.HasUsageMetadata = true
		params.CachedTokenCount = usageResult.Get("cachedContentTokenCount").Int()
		params.PromptTokenCount = usageResult.Get("promptTokenCount").Int() - params.CachedTokenCount
		params.CandidatesTokenCount = usageResult.Get("candidatesTokenCount").Int()
		params.ThoughtsTokenCount = usageResult.Get("thoughtsTokenCount").Int()
		params.TotalTokenCount = usageResult.Get("totalTokenCount").Int()
		if params.CandidatesTokenCount == 0 && params.TotalTokenCount > 0 {
			params.CandidatesTokenCount = params.TotalTokenCount - params.PromptTokenCount - params.ThoughtsTokenCount
			if params.CandidatesTokenCount < 0 {
				params.CandidatesTokenCount = 0
			}
		}
	}

	if params.HasUsageMetadata && params.HasFinishReason {
		appendWebSearchBlocks(params, &output)
		appendFinalEvents(params, &output, false)
	}

	return [][]byte{output}
}

func appendFinalEvents(params *Params, output *[]byte, force bool) {
	if params.HasSentFinalEvents {
		return
	}

	if !params.HasUsageMetadata && !force {
		return
	}

	// Only send final events if we have actually output content
	if !params.HasContent {
		return
	}

	if params.ResponseType != 0 {
		*output = translatorcommon.AppendSSEEventString(*output, "content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, params.ResponseIndex), 3)
		params.ResponseType = 0
	}

	stopReason := resolveStopReason(params)
	usageOutputTokens := params.CandidatesTokenCount + params.ThoughtsTokenCount
	if usageOutputTokens == 0 && params.TotalTokenCount > 0 {
		usageOutputTokens = params.TotalTokenCount - params.PromptTokenCount
		if usageOutputTokens < 0 {
			usageOutputTokens = 0
		}
	}

	delta := []byte(fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"%s","stop_sequence":null},"usage":{"input_tokens":%d,"output_tokens":%d}}`, stopReason, params.PromptTokenCount, usageOutputTokens))
	// Add cache_read_input_tokens if cached tokens are present (indicates prompt caching is working)
	if params.CachedTokenCount > 0 {
		var err error
		delta, err = sjson.SetBytes(delta, "usage.cache_read_input_tokens", params.CachedTokenCount)
		if err != nil {
			log.Warnf("antigravity claude response: failed to set cache_read_input_tokens: %v", err)
		}
	}
	*output = translatorcommon.AppendSSEEventString(*output, "message_delta", string(delta), 3)

	params.HasSentFinalEvents = true
}

func resolveStopReason(params *Params) string {
	if params.HasToolUse {
		return "tool_use"
	}

	switch params.FinishReason {
	case "MAX_TOKENS":
		return "max_tokens"
	case "STOP", "FINISH_REASON_UNSPECIFIED", "UNKNOWN":
		return "end_turn"
	}

	return "end_turn"
}

func buildEncryptedContent(url, title string) string {
	payload := map[string]string{"url": url, "title": title}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(encoded)
}

func extractWebSearchFromAntigravity(rawJSON []byte) (string, []map[string]any) {
	candidate := gjson.GetBytes(rawJSON, "response.candidates.0")
	if !candidate.Exists() {
		candidate = gjson.GetBytes(rawJSON, "candidates.0")
	}
	if !candidate.Exists() {
		return "", nil
	}

	query := candidate.Get("groundingMetadata.webSearchQueries.0").String()

	chunks := candidate.Get("groundingChunks")
	if !chunks.Exists() {
		chunks = candidate.Get("groundingMetadata.groundingChunks")
	}
	if !chunks.Exists() || !chunks.IsArray() {
		return query, nil
	}

	results := make([]map[string]any, 0, len(chunks.Array()))
	for _, chunk := range chunks.Array() {
		web := chunk.Get("web")
		if !web.Exists() {
			continue
		}
		url := web.Get("uri").String()
		if url == "" {
			url = web.Get("url").String()
		}
		title := web.Get("title").String()
		if title == "" {
			title = web.Get("domain").String()
		}
		if url == "" && title == "" {
			continue
		}
		item := map[string]any{
			"type":              "web_search_result",
			"title":             title,
			"url":               url,
			"encrypted_content": buildEncryptedContent(url, title),
			"page_age":          nil,
		}
		results = append(results, item)
	}

	if len(results) == 0 {
		return query, nil
	}
	return query, results
}

func appendWebSearchBlocks(params *Params, output *[]byte) {
	if params.WebSearchEmitted {
		return
	}
	if params.WebSearchQuery == "" && len(params.WebSearchResults) == 0 {
		return
	}

	if params.ResponseType != 0 {
		*output = translatorcommon.AppendSSEEventString(*output, "content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, params.ResponseIndex), 3)
		params.ResponseType = 0
		params.ResponseIndex++
	}

	toolUseID := fmt.Sprintf("srvtoolu_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&toolUseIDCounter, 1))
	serverToolBytes := []byte(fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":"","name":"web_search","input":{}}}`, params.ResponseIndex))
	serverToolBytes, _ = sjson.SetBytes(serverToolBytes, "content_block.id", toolUseID)
	if params.WebSearchQuery != "" {
		serverToolBytes, _ = sjson.SetBytes(serverToolBytes, "content_block.input.query", params.WebSearchQuery)
	}
	*output = translatorcommon.AppendSSEEventString(*output, "content_block_start", string(serverToolBytes), 3)
	*output = translatorcommon.AppendSSEEventString(*output, "content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, params.ResponseIndex), 3)
	params.ResponseIndex++

	resultBlockBytes := []byte(fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"web_search_tool_result","tool_use_id":"","content":[]}}`, params.ResponseIndex))
	resultBlockBytes, _ = sjson.SetBytes(resultBlockBytes, "content_block.tool_use_id", toolUseID)
	if len(params.WebSearchResults) > 0 {
		if raw, err := json.Marshal(params.WebSearchResults); err == nil {
			resultBlockBytes, _ = sjson.SetRawBytes(resultBlockBytes, "content_block.content", raw)
		}
	}
	*output = translatorcommon.AppendSSEEventString(*output, "content_block_start", string(resultBlockBytes), 3)
	*output = translatorcommon.AppendSSEEventString(*output, "content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, params.ResponseIndex), 3)
	params.ResponseIndex++

	params.HasContent = true
	params.WebSearchEmitted = true
}

// ConvertAntigravityResponseToClaudeNonStream converts a non-streaming Gemini CLI response to a non-streaming Claude response.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON response from the Gemini CLI API.
//   - param: A pointer to a parameter object for the conversion.
//
// Returns:
//   - []byte: A Claude-compatible JSON response.
func ConvertAntigravityResponseToClaudeNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	toolNameMap := util.SanitizedToolNameMap(originalRequestRawJSON)
	modelName := gjson.GetBytes(requestRawJSON, "model").String()

	root := gjson.ParseBytes(rawJSON)
	promptTokens := root.Get("response.usageMetadata.promptTokenCount").Int()
	candidateTokens := root.Get("response.usageMetadata.candidatesTokenCount").Int()
	thoughtTokens := root.Get("response.usageMetadata.thoughtsTokenCount").Int()
	totalTokens := root.Get("response.usageMetadata.totalTokenCount").Int()
	cachedTokens := root.Get("response.usageMetadata.cachedContentTokenCount").Int()
	outputTokens := candidateTokens + thoughtTokens
	if outputTokens == 0 && totalTokens > 0 {
		outputTokens = totalTokens - promptTokens
		if outputTokens < 0 {
			outputTokens = 0
		}
	}

	responseJSON := []byte(`{"id":"","type":"message","role":"assistant","model":"","content":null,"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`)
	responseJSON, _ = sjson.SetBytes(responseJSON, "id", root.Get("response.responseId").String())
	responseJSON, _ = sjson.SetBytes(responseJSON, "model", root.Get("response.modelVersion").String())
	responseJSON, _ = sjson.SetBytes(responseJSON, "usage.input_tokens", promptTokens)
	responseJSON, _ = sjson.SetBytes(responseJSON, "usage.output_tokens", outputTokens)
	// Add cache_read_input_tokens if cached tokens are present (indicates prompt caching is working)
	if cachedTokens > 0 {
		var err error
		responseJSON, err = sjson.SetBytes(responseJSON, "usage.cache_read_input_tokens", cachedTokens)
		if err != nil {
			log.Warnf("antigravity claude response: failed to set cache_read_input_tokens: %v", err)
		}
	}

	contentArrayInitialized := false
	ensureContentArray := func() {
		if contentArrayInitialized {
			return
		}
		responseJSON, _ = sjson.SetRawBytes(responseJSON, "content", []byte("[]"))
		contentArrayInitialized = true
	}

	parts := root.Get("response.candidates.0.content.parts")
	textBuilder := strings.Builder{}
	thinkingBuilder := strings.Builder{}
	thinkingSignature := ""
	toolIDCounter := 0
	hasToolCall := false

	flushText := func() {
		if textBuilder.Len() == 0 {
			return
		}
		ensureContentArray()
		block := []byte(`{"type":"text","text":""}`)
		block, _ = sjson.SetBytes(block, "text", textBuilder.String())
		responseJSON, _ = sjson.SetRawBytes(responseJSON, "content.-1", block)
		textBuilder.Reset()
	}

	flushThinking := func() {
		if thinkingBuilder.Len() == 0 && thinkingSignature == "" {
			return
		}
		ensureContentArray()
		block := []byte(`{"type":"thinking","thinking":""}`)
		block, _ = sjson.SetBytes(block, "thinking", thinkingBuilder.String())
		if thinkingSignature != "" {
			block, _ = sjson.SetBytes(block, "signature", fmt.Sprintf("%s#%s", cache.GetModelGroup(modelName), thinkingSignature))
		}
		responseJSON, _ = sjson.SetRawBytes(responseJSON, "content.-1", block)
		thinkingBuilder.Reset()
		thinkingSignature = ""
	}

	if parts.IsArray() {
		for _, part := range parts.Array() {
			isThought := part.Get("thought").Bool()
			if isThought {
				sig := part.Get("thoughtSignature")
				if !sig.Exists() {
					sig = part.Get("thought_signature")
				}
				if sig.Exists() && sig.String() != "" {
					thinkingSignature = sig.String()
				}
			}

			if text := part.Get("text"); text.Exists() && text.String() != "" {
				if isThought {
					flushText()
					thinkingBuilder.WriteString(text.String())
					continue
				}
				flushThinking()
				textBuilder.WriteString(text.String())
				continue
			}

			if functionCall := part.Get("functionCall"); functionCall.Exists() {
				flushThinking()
				flushText()
				hasToolCall = true

				name := util.RestoreSanitizedToolName(toolNameMap, functionCall.Get("name").String())
				toolIDCounter++
				toolBlock := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
				toolBlock, _ = sjson.SetBytes(toolBlock, "id", fmt.Sprintf("tool_%d", toolIDCounter))
				toolBlock, _ = sjson.SetBytes(toolBlock, "name", name)

				if args := functionCall.Get("args"); args.Exists() && args.Raw != "" && gjson.Valid(args.Raw) && args.IsObject() {
					toolBlock, _ = sjson.SetRawBytes(toolBlock, "input", []byte(args.Raw))
				}

				ensureContentArray()
				responseJSON, _ = sjson.SetRawBytes(responseJSON, "content.-1", toolBlock)
				continue
			}
		}
	}

	flushThinking()
	flushText()
	if query, results := extractWebSearchFromAntigravity(rawJSON); query != "" || len(results) > 0 {
		ensureContentArray()
		toolUseID := fmt.Sprintf("srvtoolu_%d", time.Now().UnixNano())
		serverToolBytes := []byte(`{"type":"server_tool_use","id":"","name":"web_search","input":{}}`)
		serverToolBytes, _ = sjson.SetBytes(serverToolBytes, "id", toolUseID)
		if query != "" {
			serverToolBytes, _ = sjson.SetBytes(serverToolBytes, "input.query", query)
		}
		responseJSON, _ = sjson.SetRawBytes(responseJSON, "content.-1", serverToolBytes)

		resultBlockBytes := []byte(`{"type":"web_search_tool_result","tool_use_id":"","content":[]}`)
		resultBlockBytes, _ = sjson.SetBytes(resultBlockBytes, "tool_use_id", toolUseID)
		if len(results) > 0 {
			if raw, err := json.Marshal(results); err == nil {
				resultBlockBytes, _ = sjson.SetRawBytes(resultBlockBytes, "content", raw)
			}
		}
		responseJSON, _ = sjson.SetRawBytes(responseJSON, "content.-1", resultBlockBytes)
	}

	stopReason := "end_turn"
	if hasToolCall {
		stopReason = "tool_use"
	} else {
		if finish := root.Get("response.candidates.0.finishReason"); finish.Exists() {
			switch finish.String() {
			case "MAX_TOKENS":
				stopReason = "max_tokens"
			case "STOP", "FINISH_REASON_UNSPECIFIED", "UNKNOWN":
				stopReason = "end_turn"
			default:
				stopReason = "end_turn"
			}
		}
	}
	responseJSON, _ = sjson.SetBytes(responseJSON, "stop_reason", stopReason)

	if promptTokens == 0 && outputTokens == 0 {
		if usageMeta := root.Get("response.usageMetadata"); !usageMeta.Exists() {
			responseJSON, _ = sjson.DeleteBytes(responseJSON, "usage")
		}
	}

	return responseJSON
}

func ClaudeTokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.ClaudeInputTokensJSON(count)
}
