// Package openai provides response translation functionality for Antigravity to OpenAI API compatibility.
// This package handles the conversion of Antigravity API responses into OpenAI Chat Completions-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by OpenAI API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, reasoning content, and usage metadata appropriately.
package chat_completions

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"

	"github.com/MoeForge/moego/ojson"
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/chat-completions"
)

// convertCliResponseToOpenAIChatParams holds parameters for response conversion.
type convertCliResponseToOpenAIChatParams struct {
	UnixTimestamp        int64
	FunctionIndex        int
	SawResponse          bool   // Tracks whether any upstream response chunk was seen
	SawToolCall          bool   // Tracks if any tool call was seen in the entire stream
	SawFinishReason      bool   // Tracks whether finish_reason has been emitted
	UpstreamFinishReason string // Caches the upstream finish reason for final chunk
	ModelVersion         string
	ResponseID           string
	PendingUsageMetadata []byte
	SanitizedNameMap     map[string]string
	ActiveToolCall       *antigravityOpenAIToolCall
	ToolCallsByID        map[string]*antigravityOpenAIToolCall
	ToolCalls            []*antigravityOpenAIToolCall
}

type antigravityOpenAIToolCall struct {
	Index             int
	UpstreamID        string
	ArgumentsStarted  bool
	ArgumentsHasItems bool
	ArgumentsClosed   bool
}

// functionCallIDCounter provides a process-wide unique counter for function call identifiers.
var functionCallIDCounter uint64

// ConvertAntigravityResponseToOpenAI translates a single chunk of a streaming response from the
// Antigravity API format to the OpenAI Chat Completions streaming format.
// It processes various Antigravity event types and transforms them into OpenAI-compatible JSON responses.
// The function handles text content, tool calls, reasoning content, and usage metadata, outputting
// responses that match the OpenAI API format. It supports incremental updates for streaming responses.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Antigravity API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - [][]byte: A slice of OpenAI-compatible JSON responses
func ConvertAntigravityResponseToOpenAI(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	params, ok := (*param).(*convertCliResponseToOpenAIChatParams)
	if !ok {
		params = &convertCliResponseToOpenAIChatParams{
			UnixTimestamp: 0,
			FunctionIndex: 0,
		}
		*param = params
	}
	if params.SanitizedNameMap == nil {
		params.SanitizedNameMap = util.DisambiguatedToolNameMap(originalRequestRawJSON)
	}

	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		// Never finalize a stream that produced no response at all: an empty
		// upstream body must stay detectable as a failure rather than be reported
		// as a successful empty completion.
		if params.SawResponse && !params.SawFinishReason {
			params.SawFinishReason = true
			finishReason, nativeFinishReason := resolveOpenAIFinishReason(params)
			templateDoc := ojson.NewDoc([]byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"model","choices":[{"index":0,"delta":{},"finish_reason":"stop","native_finish_reason":"stop"}]}`))
			appendAntigravityOpenAIToolCallCloses(templateDoc, params)
			_ = templateDoc.Set("created", params.UnixTimestamp)
			if params.ModelVersion != "" {
				_ = templateDoc.Set("model", params.ModelVersion)
			}
			if params.ResponseID != "" {
				_ = templateDoc.Set("id", params.ResponseID)
			}
			if len(params.PendingUsageMetadata) > 0 {
				setOpenAIUsageMetadataDoc(templateDoc, util.ParseGJSONBytesNoCopy(params.PendingUsageMetadata))
			}
			_ = templateDoc.Set("choices.0.finish_reason", finishReason)
			_ = templateDoc.Set("choices.0.native_finish_reason", nativeFinishReason)
			return [][]byte{templateDoc.Bytes()}
		}
		return [][]byte{}
	}

	if !params.SawResponse {
		params.SawResponse = translatorcommon.HasAntigravityResponsePayload(rawJSON)
	}

	// Initialize the OpenAI SSE template.
	templateDoc := ojson.NewDoc([]byte(`{"id":"","object":"chat.completion.chunk","created":12345,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null},"finish_reason":null,"native_finish_reason":null}]}`))

	// Extract and set the model version.
	if modelVersionResult := ojson.GetBytesNoCopy(rawJSON, "response.modelVersion"); modelVersionResult.Exists() {
		params.ModelVersion = strings.Clone(modelVersionResult.String())
		_ = templateDoc.Set("model", params.ModelVersion)
	}

	// Extract and set the creation timestamp.
	if createTimeResult := ojson.GetBytesNoCopy(rawJSON, "response.createTime"); createTimeResult.Exists() {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		if err == nil {
			params.UnixTimestamp = t.Unix()
		}
		_ = templateDoc.Set("created", params.UnixTimestamp)
	} else {
		_ = templateDoc.Set("created", params.UnixTimestamp)
	}

	// Extract and set the response ID.
	if responseIDResult := ojson.GetBytesNoCopy(rawJSON, "response.responseId"); responseIDResult.Exists() {
		params.ResponseID = strings.Clone(responseIDResult.String())
		_ = templateDoc.Set("id", params.ResponseID)
	}

	// Cache the finish reason - do NOT set it in output yet (will be set on final chunk)
	if finishReasonResult := ojson.GetBytesNoCopy(rawJSON, "response.candidates.0.finishReason"); finishReasonResult.Exists() {
		params.UpstreamFinishReason = strings.Clone(strings.ToUpper(finishReasonResult.String()))
	}

	// Extract and set usage metadata (token counts). FilterSSEUsageMetadata renames
	// non-terminal usage to cpaUsageMetadata, so retain the latest copy for [DONE]
	// instead of treating an intermediate chunk as terminal.
	usageResult := ojson.GetBytesNoCopy(rawJSON, "response.usageMetadata")
	if !usageResult.Exists() {
		if pendingUsage := ojson.GetBytesNoCopy(rawJSON, "response.cpaUsageMetadata"); pendingUsage.Exists() {
			params.PendingUsageMetadata = append(params.PendingUsageMetadata[:0], pendingUsage.Raw...)
		}
	} else {
		setOpenAIUsageMetadataDoc(templateDoc, usageResult)
	}

	// Process the main content part of the response.
	hasDeltaContent := false
	partsResult := ojson.GetBytesNoCopy(rawJSON, "response.candidates.0.content.parts")
	if partsResult.IsArray() {
		partResults := partsResult.Array()
		hasToolCallsArray := false
		imageCount := 0
		var textContent strings.Builder
		var reasoningContent strings.Builder
		hasTextContent := false
		hasReasoningContent := false
		for i := 0; i < len(partResults); i++ {
			partResult := partResults[i]
			partTextResult := partResult.Get("text")
			functionCallResult := partResult.Get("functionCall")
			thoughtSignatureResult := partResult.Get("thoughtSignature")
			if !thoughtSignatureResult.Exists() {
				thoughtSignatureResult = partResult.Get("thought_signature")
			}
			inlineDataResult := partResult.Get("inlineData")
			if !inlineDataResult.Exists() {
				inlineDataResult = partResult.Get("inline_data")
			}

			hasThoughtSignature := thoughtSignatureResult.Exists() && thoughtSignatureResult.String() != ""
			hasTextPayload := partTextResult.Exists() && partTextResult.String() != ""
			hasContentPayload := hasTextPayload || functionCallResult.Exists() || inlineDataResult.Exists()

			// Ignore encrypted thoughtSignature but keep any actual content in the same part.
			if hasThoughtSignature && !hasContentPayload {
				continue
			}

			if partTextResult.Exists() {
				text := partTextResult.String()
				if text != "" && hasOpenAntigravityOpenAIToolArguments(params) {
					if !hasToolCallsArray {
						_ = templateDoc.SetRaw("choices.0.delta.tool_calls", `[]`)
						hasToolCallsArray = true
					}
					appendAntigravityOpenAIToolCallCloses(templateDoc, params)
					params.ActiveToolCall = nil
				}

				// Handle text content, distinguishing between regular content and reasoning/thoughts.
				if partResult.Get("thought").Bool() {
					hasReasoningContent = true
					reasoningContent.WriteString(text)
				} else {
					hasTextContent = true
					textContent.WriteString(text)
				}
				_ = templateDoc.Set("choices.0.delta.role", "assistant")
			} else if functionCallResult.Exists() {
				// Handle function call content.
				params.SawToolCall = true // Persist across chunks
				if !hasToolCallsArray {
					_ = templateDoc.SetRaw("choices.0.delta.tool_calls", `[]`)
					hasToolCallsArray = true
				}

				rawName := strings.TrimSpace(functionCallResult.Get("name").String())
				upstreamID := strings.TrimSpace(functionCallResult.Get("id").String())
				if rawName == "" {
					active := params.ActiveToolCall
					if upstreamID != "" && params.ToolCallsByID != nil {
						active = params.ToolCallsByID[upstreamID]
					}
					if active != nil {
						if argumentDelta := antigravityOpenAIContinuationArguments(active, functionCallResult.Get("args")); argumentDelta != "" {
							appendAntigravityOpenAIToolCallDelta(templateDoc, active.Index, argumentDelta)
						}
						params.ActiveToolCall = active
						_ = templateDoc.Set("choices.0.delta.role", "assistant")
						continue
					}
				}

				functionCallIndex := params.FunctionIndex
				params.FunctionIndex++
				fcDoc := ojson.NewDocString(`{"id":"","index":0,"type":"function","function":{"name":"","arguments":""}}`)
				fcName := util.RestoreSanitizedToolName(params.SanitizedNameMap, rawName)
				fcID := upstreamID
				if fcID == "" {
					fcID = fmt.Sprintf("%s-%d-%d", fcName, time.Now().UnixNano(), atomic.AddUint64(&functionCallIDCounter, 1))
				}
				active := &antigravityOpenAIToolCall{Index: functionCallIndex, UpstreamID: upstreamID}
				params.ActiveToolCall = active
				params.ToolCalls = append(params.ToolCalls, active)
				if upstreamID != "" {
					if params.ToolCallsByID == nil {
						params.ToolCallsByID = make(map[string]*antigravityOpenAIToolCall)
					}
					params.ToolCallsByID[upstreamID] = active
				}
				_ = fcDoc.Set("id", fcID)
				_ = fcDoc.Set("index", functionCallIndex)
				_ = fcDoc.Set("function.name", fcName)
				if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
					_ = fcDoc.Set("function.arguments", antigravityOpenAIStartArguments(active, fcArgsResult))
				}
				_ = templateDoc.Set("choices.0.delta.role", "assistant")
				_ = templateDoc.SetRawBytes("choices.0.delta.tool_calls.-1", fcDoc.Bytes())
			} else if inlineDataResult.Exists() {
				if partResult.Get("thought").Bool() {
					continue
				}
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
				if imageCount == 0 {
					_ = templateDoc.SetRaw("choices.0.delta.images", `[]`)
				}
				imageIndex := imageCount
				imageCount++
				imageDoc := ojson.NewDoc([]byte(`{"type":"image_url","image_url":{"url":""}}`))
				_ = imageDoc.Set("index", imageIndex)
				_ = imageDoc.Set("image_url.url", imageURL)
				_ = templateDoc.Set("choices.0.delta.role", "assistant")
				_ = templateDoc.SetRawBytes("choices.0.delta.images.-1", imageDoc.Bytes())
			}
		}
		if hasTextContent {
			_ = templateDoc.Set("choices.0.delta.content", textContent.String())
		}
		if hasReasoningContent {
			_ = templateDoc.Set("choices.0.delta.reasoning_content", reasoningContent.String())
		}
		hasDeltaContent = hasTextContent || hasReasoningContent || hasToolCallsArray || imageCount > 0
	}

	if params.UpstreamFinishReason != "" && usageResult.Exists() && appendAntigravityOpenAIToolCallCloses(templateDoc, params) {
		hasDeltaContent = true
		params.ActiveToolCall = nil
	}

	// Determine finish_reason only on the final chunk, which upstream marks with
	// both finishReason and usage metadata. A usage-carrying chunk without
	// finishReason must not be treated as terminal: upstream can split the two
	// across chunks and keep streaming afterwards, and finalizing early would make
	// strict clients drop the remaining content. When upstream never sends
	// finishReason at all, the [DONE] branch synthesizes the terminal chunk.
	isFinalChunk := params.UpstreamFinishReason != "" && usageResult.Exists()

	if isFinalChunk {
		finishReason, nativeFinishReason := resolveOpenAIFinishReason(params)
		_ = templateDoc.Set("choices.0.finish_reason", finishReason)
		_ = templateDoc.Set("choices.0.native_finish_reason", nativeFinishReason)
		params.SawFinishReason = true
	}

	if !hasDeltaContent && !isFinalChunk && !usageResult.Exists() {
		return [][]byte{}
	}

	return [][]byte{templateDoc.Bytes()}
}

func antigravityOpenAIStartArguments(call *antigravityOpenAIToolCall, args ojson.Result) string {
	if call == nil || !args.Exists() || !args.IsObject() {
		return ""
	}
	raw := strings.TrimSpace(args.Raw)
	if len(raw) < 2 || raw[0] != '{' || raw[len(raw)-1] != '}' {
		return raw
	}
	call.ArgumentsStarted = true
	members := strings.TrimSpace(raw[1 : len(raw)-1])
	if members == "" {
		call.ArgumentsHasItems = false
		return "{"
	}
	call.ArgumentsHasItems = true
	return "{" + members
}

func antigravityOpenAIContinuationArguments(call *antigravityOpenAIToolCall, args ojson.Result) string {
	if call == nil || call.ArgumentsClosed || !args.Exists() || !args.IsObject() {
		return ""
	}
	raw := strings.TrimSpace(args.Raw)
	if len(raw) < 2 || raw[0] != '{' || raw[len(raw)-1] != '}' {
		return ""
	}
	members := strings.TrimSpace(raw[1 : len(raw)-1])
	if !call.ArgumentsStarted {
		call.ArgumentsStarted = true
		if members == "" {
			return "{"
		}
		call.ArgumentsHasItems = true
		return "{" + members
	}
	if members == "" {
		return ""
	}
	prefix := ""
	if call.ArgumentsHasItems {
		prefix = ","
	}
	call.ArgumentsHasItems = true
	return prefix + members
}

func hasOpenAntigravityOpenAIToolArguments(params *convertCliResponseToOpenAIChatParams) bool {
	if params == nil {
		return false
	}
	for _, call := range params.ToolCalls {
		if call != nil && call.ArgumentsStarted && !call.ArgumentsClosed {
			return true
		}
	}
	return false
}

func appendAntigravityOpenAIToolCallCloses(doc *ojson.Doc, params *convertCliResponseToOpenAIChatParams) bool {
	if doc == nil || params == nil {
		return false
	}
	appended := false
	for _, call := range params.ToolCalls {
		if call == nil || call.ArgumentsClosed || !call.ArgumentsStarted {
			continue
		}
		appendAntigravityOpenAIToolCallDelta(doc, call.Index, "}")
		call.ArgumentsClosed = true
		appended = true
	}
	return appended
}

func appendAntigravityOpenAIToolCallDelta(doc *ojson.Doc, index int, arguments string) {
	fcDoc := ojson.NewDocString(`{"index":0,"function":{"arguments":""}}`)
	_ = fcDoc.Set("index", index)
	_ = fcDoc.Set("function.arguments", arguments)
	_ = doc.SetRawBytes("choices.0.delta.tool_calls.-1", fcDoc.Bytes())
}

// resolveOpenAIFinishReason maps the cached upstream state to the OpenAI
// finish_reason and native_finish_reason pair. Both the terminal upstream chunk
// and the synthesized [DONE] chunk must agree on this mapping.
func resolveOpenAIFinishReason(params *convertCliResponseToOpenAIChatParams) (finishReason, nativeFinishReason string) {
	switch {
	case params.SawToolCall:
		finishReason = "tool_calls"
	case params.UpstreamFinishReason == "MAX_TOKENS":
		finishReason = "length"
	case isAntigravityContentFilterFinishReason(params.UpstreamFinishReason):
		finishReason = "content_filter"
	default:
		finishReason = "stop"
	}
	nativeFinishReason = "stop"
	if params.UpstreamFinishReason != "" {
		nativeFinishReason = strings.ToLower(params.UpstreamFinishReason)
	}
	return finishReason, nativeFinishReason
}

func isAntigravityContentFilterFinishReason(reason string) bool {
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "MALFORMED_RESPONSE", "MALFORMED_FUNCTION_CALL":
		return true
	default:
		return false
	}
}

func setOpenAIUsageMetadataDoc(templateDoc *ojson.Doc, usageResult ojson.Result) {
	cachedTokenCount := usageResult.Get("cachedContentTokenCount").Int()
	_ = templateDoc.Set("usage.completion_tokens", usageResult.Get("candidatesTokenCount").Int()+usageResult.Get("thoughtsTokenCount").Int())
	if totalTokenCountResult := usageResult.Get("totalTokenCount"); totalTokenCountResult.Exists() {
		_ = templateDoc.Set("usage.total_tokens", totalTokenCountResult.Int())
	}
	promptTokenCount := usageResult.Get("promptTokenCount").Int()
	thoughtsTokenCount := usageResult.Get("thoughtsTokenCount").Int()
	_ = templateDoc.Set("usage.prompt_tokens", promptTokenCount)
	if thoughtsTokenCount > 0 {
		_ = templateDoc.Set("usage.completion_tokens_details.reasoning_tokens", thoughtsTokenCount)
	}
	if cachedTokenCount > 0 {
		if errSet := templateDoc.Set("usage.prompt_tokens_details.cached_tokens", cachedTokenCount); errSet != nil {
			log.Warnf("antigravity openai response: failed to set cached_tokens: %v", errSet)
		}
	}
}

// ConvertAntigravityResponseToOpenAINonStream converts a non-streaming Antigravity response to a non-streaming OpenAI response.
// This function processes the complete Antigravity response and transforms it into a single OpenAI-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the OpenAI API format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Antigravity API
//   - param: A pointer to a parameter object for the conversion
//
// Returns:
//   - []byte: An OpenAI-compatible JSON response containing all message content and metadata
func ConvertAntigravityResponseToOpenAINonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	responseResult := ojson.GetBytesNoCopy(rawJSON, "response")
	if responseResult.Exists() {
		responseJSON := restoreAntigravityOpenAIFunctionNames([]byte(responseResult.Raw), originalRequestRawJSON)
		return ConvertGeminiResponseToOpenAINonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, responseJSON, param)
	}
	return []byte{}
}

func restoreAntigravityOpenAIFunctionNames(rawJSON, originalRequestRawJSON []byte) []byte {
	nameMap := util.DisambiguatedToolNameMap(originalRequestRawJSON)
	if len(nameMap) == 0 {
		return rawJSON
	}
	var rawDoc *ojson.Doc
	candidates := ojson.GetBytesNoCopy(rawJSON, "candidates")
	for candidateIndex, candidate := range candidates.Array() {
		for partIndex, part := range candidate.Get("content.parts").Array() {
			for _, field := range []string{"functionCall", "functionResponse"} {
				nameResult := part.Get(field + ".name")
				name := nameResult.String()
				if name == "" {
					continue
				}
				restoredName := util.RestoreSanitizedToolName(nameMap, name)
				if nameResult.Type == ojson.String && restoredName == name {
					continue
				}
				if rawDoc == nil {
					rawDoc = ojson.NewDoc(rawJSON)
				}
				path := fmt.Sprintf("candidates.%d.content.parts.%d.%s.name", candidateIndex, partIndex, field)
				_ = rawDoc.Set(path, restoredName)
			}
		}
	}
	if rawDoc != nil {
		return rawDoc.Bytes()
	}
	return rawJSON
}
