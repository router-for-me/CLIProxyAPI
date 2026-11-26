// Package openai provides response translation functionality for Codex to OpenAI API compatibility.
// This package handles the conversion of Codex API responses into OpenAI Chat Completions-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by OpenAI API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, reasoning content, and usage metadata appropriately.
package chat_completions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
	applypatch "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/apply-patch"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

var (
	dataTag = []byte("data:")
)

type messageStreamState struct {
	itemID       string
	outputIdx    int64
	hasOutputIdx bool
	hasTextDelta bool
}

type toolCallStreamState struct {
	Index            int
	ArgumentsEmitted bool
	Patch            bool
	InputStarted     bool
	InputClosed      bool
	Done             bool
}

// codexChatToolMetadata carries request-derived tool facts shared by the
// streaming and non-streaming translators. It is resolved once, on first
// tool call, instead of re-scanning the original request per tool event;
// callers pass the same request bytes for a stream's lifetime.
type codexChatToolMetadata struct {
	resolved           bool
	reverseToolNames   map[string]string
	functionToolNames  map[string]struct{}
	customPatchWinners map[string]bool
}

func (m *codexChatToolMetadata) resolve(original []byte) {
	if m.resolved {
		return
	}
	m.resolved = true
	m.reverseToolNames = buildReverseMapFromOriginalOpenAI(original)
	m.functionToolNames = map[string]struct{}{}
	for _, tool := range ojson.GetBytesNoCopy(original, "tools").Array() {
		if tool.Get("type").String() == "function" {
			m.functionToolNames[strings.Clone(tool.Get("function.name").String())] = struct{}{}
		}
	}
	m.customPatchWinners = map[string]bool{}
	for name, winner := range collectChatToolWinners(original) {
		if applypatch.IsCustomTool(winner.Tool) {
			m.customPatchWinners[strings.Clone(name)] = true
		}
	}
}

// collectChatToolWinners normalizes nested Chat custom declarations for Responses metadata.
func collectChatToolWinners(original []byte) map[string]util.ResponsesToolDescriptor {
	root := util.ParseGJSONBytesNoCopy(original)
	var doc *ojson.Doc
	for i, tool := range root.Get("tools").Array() {
		if tool.Get("type").String() != "custom" {
			continue
		}
		definition := customToolDefinition(tool)
		if definition.Raw == tool.Raw {
			continue
		}
		if doc == nil {
			doc = ojson.NewDoc(original)
		}
		path := "tools." + strconv.Itoa(i)
		_ = doc.SetRaw(path, definition.Raw)
		_ = doc.Set(path+".type", "custom")
	}
	if doc != nil {
		return util.CollectResponsesToolWinnersDoc(doc)
	}
	return util.CollectResponsesToolWinners(root)
}

// restoreToolName maps a shortened upstream tool name back to the
// client-declared original, matching buildReverseMapFromOriginalOpenAI.
func (m *codexChatToolMetadata) restoreToolName(original []byte, name string) string {
	m.resolve(original)
	if orig, ok := m.reverseToolNames[name]; ok {
		return orig
	}
	return name
}

// isCustomPatch matches isOriginalCustomPatch using the cached metadata.
func (m *codexChatToolMetadata) isCustomPatch(original []byte, item ojson.Result) bool {
	if item.Get("type").String() != "custom_tool_call" {
		return false
	}
	m.resolve(original)
	name := util.QualifyResponsesNamespaceToolName(item.Get("namespace").String(), item.Get("name").String())
	if _, ok := m.functionToolNames[name]; ok {
		return false
	}
	return m.customPatchWinners[name]
}

// ConvertCliToOpenAIParams holds parameters for response conversion.
type ConvertCliToOpenAIParams struct {
	ServiceTier           string
	toolMetadata          codexChatToolMetadata
	ResponseID            string
	CreatedAt             int64
	Model                 string
	FunctionCallIndex     int
	toolCallStates        map[string]*toolCallStreamState
	currentToolCall       *toolCallStreamState
	citationKeys          map[string]struct{}
	emittedTextRunes      int64
	HasMessageTextDelta   bool
	messageStatesByID     map[string]*messageStreamState
	messageStatesByIndex  map[int64]*messageStreamState
	openMessageStates     map[*messageStreamState]struct{}
	LastImageHashByItemID map[string][32]byte
}

// ConvertCodexResponseToOpenAI translates a single chunk of a streaming response from the
// Codex API format to the OpenAI Chat Completions streaming format.
// It processes various Codex event types and transforms them into OpenAI-compatible JSON responses.
// The function handles text content, tool calls, reasoning content, and usage metadata, outputting
// responses that match the OpenAI API format. It supports incremental updates for streaming responses.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Codex API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - [][]byte: A slice of OpenAI-compatible JSON responses
func ConvertCodexResponseToOpenAI(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &ConvertCliToOpenAIParams{
			Model:                 modelName,
			CreatedAt:             0,
			ResponseID:            "",
			FunctionCallIndex:     -1,
			toolCallStates:        make(map[string]*toolCallStreamState),
			citationKeys:          make(map[string]struct{}),
			LastImageHashByItemID: make(map[string][32]byte),
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	// Initialize the OpenAI SSE template.
	templateDoc := ojson.NewDocString(`{"id":"","object":"chat.completion.chunk","created":12345,"model":"model","choices":[{"index":0,"delta":{},"finish_reason":null,"native_finish_reason":null}]}`)

	rootResult := util.ParseGJSONBytesNoCopy(rawJSON)

	p := (*param).(*ConvertCliToOpenAIParams)
	if tier := codexResponseServiceTier(rootResult.Get("response")); tier != "" {
		p.ServiceTier = strings.Clone(tier)
	} else if tier := codexResponseServiceTier(rootResult); tier != "" {
		p.ServiceTier = strings.Clone(tier)
	}
	if p.ServiceTier != "" {
		_ = templateDoc.Set("service_tier", p.ServiceTier)
	}

	typeResult := rootResult.Get("type")
	dataType := typeResult.String()
	if dataType == "response.created" {
		p.ResponseID = strings.Clone(rootResult.Get("response.id").String())
		p.CreatedAt = rootResult.Get("response.created_at").Int()
		p.Model = strings.Clone(rootResult.Get("response.model").String())
		p.resetMessageTextDeltaState()
		if p.LastImageHashByItemID == nil {
			p.LastImageHashByItemID = make(map[string][32]byte)
		}
		return [][]byte{}
	}

	// Extract and set the model version.
	cachedModel := (*param).(*ConvertCliToOpenAIParams).Model
	if modelResult := rootResult.Get("model"); modelResult.Exists() {
		_ = templateDoc.Set("model", modelResult.String())
	} else if cachedModel != "" {
		_ = templateDoc.Set("model", cachedModel)
	} else if modelName != "" {
		_ = templateDoc.Set("model", modelName)
	}

	_ = templateDoc.Set("created", (*param).(*ConvertCliToOpenAIParams).CreatedAt)

	// Extract and set the response ID.
	_ = templateDoc.Set("id", (*param).(*ConvertCliToOpenAIParams).ResponseID)

	// Extract and set usage metadata (token counts).
	if usageResult := rootResult.Get("response.usage"); usageResult.Exists() {
		if outputTokensResult := usageResult.Get("output_tokens"); outputTokensResult.Exists() {
			_ = templateDoc.Set("usage.completion_tokens", outputTokensResult.Int())
		}
		if totalTokensResult := usageResult.Get("total_tokens"); totalTokensResult.Exists() {
			_ = templateDoc.Set("usage.total_tokens", totalTokensResult.Int())
		}
		if inputTokensResult := usageResult.Get("input_tokens"); inputTokensResult.Exists() {
			_ = templateDoc.Set("usage.prompt_tokens", inputTokensResult.Int())
		}
		if cachedTokensResult := usageResult.Get("input_tokens_details.cached_tokens"); cachedTokensResult.Exists() {
			_ = templateDoc.Set("usage.prompt_tokens_details.cached_tokens", cachedTokensResult.Int())
		}
		setCodexCacheWriteTokensDoc(templateDoc, usageResult)
		if reasoningTokensResult := usageResult.Get("output_tokens_details.reasoning_tokens"); reasoningTokensResult.Exists() {
			_ = templateDoc.Set("usage.completion_tokens_details.reasoning_tokens", reasoningTokensResult.Int())
		}
	}

	if dataType == "response.reasoning_summary_text.delta" || dataType == "response.reasoning_text.delta" {
		if deltaResult := rootResult.Get("delta"); deltaResult.Exists() {
			_ = templateDoc.Set("choices.0.delta.role", "assistant")
			_ = templateDoc.Set("choices.0.delta.reasoning_content", deltaResult.String())
		}
	} else if dataType == "response.reasoning_summary_text.done" || dataType == "response.reasoning_text.done" {
		_ = templateDoc.Set("choices.0.delta.role", "assistant")
		_ = templateDoc.Set("choices.0.delta.reasoning_content", "\n\n")
	} else if dataType == "response.output_text.delta" || dataType == "response.refusal.delta" {
		if deltaResult := rootResult.Get("delta"); deltaResult.Exists() {
			delta := deltaResult.String()
			_ = templateDoc.Set("choices.0.delta.role", "assistant")
			_ = templateDoc.Set("choices.0.delta.content", delta)
			p.emittedTextRunes += int64(utf8.RuneCountInString(delta))
			if delta != "" {
				p.markMessageTextDelta(rootResult)
			}
		}
	} else if dataType == "response.output_text.annotation.added" || dataType == "response.output_text.done" || dataType == "response.content_part.done" {
		citations := buildCodexURLCitations(codexAnnotationsFromEvent(rootResult), p.emittedTextRunes, p.citationKeys)
		if len(citations) == 0 {
			return [][]byte{}
		}
		_ = templateDoc.Set("choices.0.delta.role", "assistant")
		_ = templateDoc.SetRawBytes("choices.0.delta.annotations", translatorcommon.JoinRawArray(citations))
	} else if dataType == "response.image_generation_call.partial_image" {
		itemID := rootResult.Get("item_id").String()
		b64 := rootResult.Get("partial_image_b64").String()
		if b64 == "" {
			return [][]byte{}
		}
		if itemID != "" {
			p := (*param).(*ConvertCliToOpenAIParams)
			if p.LastImageHashByItemID == nil {
				p.LastImageHashByItemID = make(map[string][32]byte)
			}
			hash := sha256.Sum256([]byte(b64))
			if last, ok := p.LastImageHashByItemID[itemID]; ok && last == hash {
				return [][]byte{}
			}
			p.LastImageHashByItemID[strings.Clone(itemID)] = hash
		}

		outputFormat := rootResult.Get("output_format").String()
		mimeType := mimeTypeFromCodexOutputFormat(outputFormat)
		imageURL := "data:" + mimeType + ";base64," + b64

		imagePayloadDoc := ojson.NewDocString(`{"type":"image_url","image_url":{"url":""}}`)
		_ = imagePayloadDoc.Set("index", 0)
		_ = imagePayloadDoc.Set("image_url.url", imageURL)

		_ = templateDoc.Set("choices.0.delta.role", "assistant")
		_ = templateDoc.SetRawBytes("choices.0.delta.images", translatorcommon.JoinRawArray([][]byte{imagePayloadDoc.Bytes()}))
	} else if dataType == "response.completed" || dataType == "response.incomplete" {
		finishReason := "stop"
		nativeFinishReason := finishReason
		if dataType == "response.incomplete" {
			nativeFinishReason = rootResult.Get("response.incomplete_details.reason").String()
			switch nativeFinishReason {
			case "max_tokens", "max_output_tokens":
				finishReason = "length"
			case "content_filter":
				finishReason = "content_filter"
			}
		} else if (*param).(*ConvertCliToOpenAIParams).FunctionCallIndex != -1 {
			finishReason = "tool_calls"
			nativeFinishReason = finishReason
		}
		_ = templateDoc.Set("choices.0.finish_reason", finishReason)
		_ = templateDoc.Set("choices.0.native_finish_reason", nativeFinishReason)
	} else if dataType == "response.failed" || dataType == "error" {
		return [][]byte{buildCodexFailedOpenAIError(rootResult)}
	} else if dataType == "response.output_item.added" {
		itemResult := rootResult.Get("item")
		if itemResult.Exists() && itemResult.Get("type").String() == "message" {
			p.startMessageState(rootResult, itemResult)
			return [][]byte{}
		}
		if !itemResult.Exists() || !isCodexToolCallType(itemResult.Get("type").String()) {
			return [][]byte{}
		}

		// Increment index for this new tool call item.
		p := (*param).(*ConvertCliToOpenAIParams)
		p.FunctionCallIndex++
		state := &toolCallStreamState{Index: p.FunctionCallIndex, Patch: p.toolMetadata.isCustomPatch(originalRequestRawJSON, itemResult)}
		registerToolCallState(p, rootResult, itemResult, state)

		functionCallItemDoc := ojson.NewDocString(`{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}`)
		_ = functionCallItemDoc.Set("index", state.Index)
		_ = functionCallItemDoc.Set("id", itemResult.Get("call_id").String())

		// Restore original tool name if it was shortened.
		name := p.toolMetadata.restoreToolName(originalRequestRawJSON, itemResult.Get("name").String())
		_ = functionCallItemDoc.Set("function.name", name)
		_ = functionCallItemDoc.Set("function.arguments", "")

		_ = templateDoc.Set("choices.0.delta.role", "assistant")
		_ = templateDoc.SetRawBytes("choices.0.delta.tool_calls", translatorcommon.JoinRawArray([][]byte{functionCallItemDoc.Bytes()}))

	} else if dataType == "response.function_call_arguments.delta" || dataType == "response.custom_tool_call_input.delta" {
		p := (*param).(*ConvertCliToOpenAIParams)
		state := findToolCallState(p, rootResult, ojson.Result{})
		deltaValue := rootResult.Get("delta").String()
		if state == nil || state.Done || deltaValue == "" {
			return [][]byte{}
		}
		state.ArgumentsEmitted = true
		if state.Patch {
			deltaValue = applypatch.EscapeInputFragment(deltaValue)
			if !state.InputStarted {
				deltaValue = `{"input":"` + deltaValue
				state.InputStarted = true
			}
		}

		functionCallItemDoc := ojson.NewDocString(`{"index":0,"function":{"arguments":""}}`)
		_ = functionCallItemDoc.Set("index", state.Index)
		_ = functionCallItemDoc.Set("function.arguments", deltaValue)

		_ = templateDoc.SetRawBytes("choices.0.delta.tool_calls", translatorcommon.JoinRawArray([][]byte{functionCallItemDoc.Bytes()}))

	} else if dataType == "response.function_call_arguments.done" || dataType == "response.custom_tool_call_input.done" {
		p := (*param).(*ConvertCliToOpenAIParams)
		state := findToolCallState(p, rootResult, ojson.Result{})
		if state == nil || state.Done || state.InputClosed || (state.ArgumentsEmitted && !state.Patch) {
			return [][]byte{}
		}

		// Fallback: no delta events were received, emit the full arguments as a single chunk.
		fullArgsField := "arguments"
		if dataType == "response.custom_tool_call_input.done" {
			fullArgsField = "input"
		}
		state.ArgumentsEmitted = true
		fullArgs := rootResult.Get(fullArgsField).String()
		if state.Patch {
			fullArgs = finishPatchChatArguments(state, fullArgs)
		}
		if fullArgs == "" {
			return [][]byte{}
		}
		functionCallItemDoc := ojson.NewDocString(`{"index":0,"function":{"arguments":""}}`)
		_ = functionCallItemDoc.Set("index", state.Index)
		_ = functionCallItemDoc.Set("function.arguments", fullArgs)

		_ = templateDoc.SetRawBytes("choices.0.delta.tool_calls", translatorcommon.JoinRawArray([][]byte{functionCallItemDoc.Bytes()}))

	} else if dataType == "response.output_item.done" {
		itemResult := rootResult.Get("item")
		if !itemResult.Exists() {
			return [][]byte{}
		}
		if itemResult.Get("type").String() == "message" {
			hadMessageTextDelta := p.consumeMessageTextDelta(rootResult, itemResult)
			var fallbackText string
			if !hadMessageTextDelta {
				var sb strings.Builder
				for _, part := range itemResult.Get("content").Array() {
					switch part.Get("type").String() {
					case "output_text":
						sb.WriteString(part.Get("text").String())
					case "refusal":
						if txt := part.Get("refusal").String(); txt != "" {
							sb.WriteString(txt)
						} else {
							sb.WriteString(part.Get("text").String())
						}
					}
				}
				fallbackText = sb.String()
			}
			citations := buildCodexURLCitations(codexAnnotationsFromEvent(rootResult), p.emittedTextRunes, p.citationKeys)
			if fallbackText == "" && len(citations) == 0 {
				return [][]byte{}
			}
			_ = templateDoc.Set("choices.0.delta.role", "assistant")
			if fallbackText != "" {
				_ = templateDoc.Set("choices.0.delta.content", fallbackText)
				p.emittedTextRunes += int64(utf8.RuneCountInString(fallbackText))
			}
			if len(citations) > 0 {
				_ = templateDoc.SetRawBytes("choices.0.delta.annotations", translatorcommon.JoinRawArray(citations))
			}
			return [][]byte{templateDoc.Bytes()}
		}
		itemType := itemResult.Get("type").String()
		if itemType == "image_generation_call" {
			itemID := itemResult.Get("id").String()
			b64 := itemResult.Get("result").String()
			if b64 == "" {
				return [][]byte{}
			}
			if itemID != "" {
				p := (*param).(*ConvertCliToOpenAIParams)
				if p.LastImageHashByItemID == nil {
					p.LastImageHashByItemID = make(map[string][32]byte)
				}
				hash := sha256.Sum256([]byte(b64))
				if last, ok := p.LastImageHashByItemID[itemID]; ok && last == hash {
					return [][]byte{}
				}
				p.LastImageHashByItemID[strings.Clone(itemID)] = hash
			}

			outputFormat := itemResult.Get("output_format").String()
			mimeType := mimeTypeFromCodexOutputFormat(outputFormat)
			imageURL := "data:" + mimeType + ";base64," + b64

			imagePayloadDoc := ojson.NewDocString(`{"type":"image_url","image_url":{"url":""}}`)
			_ = imagePayloadDoc.Set("index", 0)
			_ = imagePayloadDoc.Set("image_url.url", imageURL)

			_ = templateDoc.Set("choices.0.delta.role", "assistant")
			_ = templateDoc.SetRawBytes("choices.0.delta.images", translatorcommon.JoinRawArray([][]byte{imagePayloadDoc.Bytes()}))
			return [][]byte{templateDoc.Bytes()}
		}
		if !isCodexToolCallType(itemType) {
			return [][]byte{}
		}

		p := (*param).(*ConvertCliToOpenAIParams)
		state := findToolCallState(p, rootResult, itemResult)
		if state != nil {
			if state.Done {
				return [][]byte{}
			}
			state.Done = true
			if state.ArgumentsEmitted && (!state.Patch || state.InputClosed) {
				return [][]byte{}
			}

			// The tool was announced, but no argument event arrived. Emit only the
			// completed arguments so the id and name are not duplicated.
			state.ArgumentsEmitted = true
			fullArgs := codexToolCallArguments(itemResult)
			if state.Patch {
				fullArgs = finishPatchChatArguments(state, fullArgs)
			}
			if fullArgs == "" {
				return [][]byte{}
			}
			functionCallItemDoc := ojson.NewDocString(`{"index":0,"function":{"arguments":""}}`)
			_ = functionCallItemDoc.Set("index", state.Index)
			_ = functionCallItemDoc.Set("function.arguments", fullArgs)
			_ = templateDoc.SetRawBytes("choices.0.delta.tool_calls", translatorcommon.JoinRawArray([][]byte{functionCallItemDoc.Bytes()}))
			return [][]byte{templateDoc.Bytes()}
		}

		// Fallback path: model skipped output_item.added, so emit the complete tool call now.
		p.FunctionCallIndex++
		state = &toolCallStreamState{Index: p.FunctionCallIndex, ArgumentsEmitted: true, Done: true, Patch: p.toolMetadata.isCustomPatch(originalRequestRawJSON, itemResult)}
		registerToolCallState(p, rootResult, itemResult, state)

		functionCallItemDoc := ojson.NewDocString(`{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}`)
		_ = functionCallItemDoc.Set("index", state.Index)
		_ = functionCallItemDoc.Set("id", itemResult.Get("call_id").String())

		// Restore original tool name if it was shortened.
		name := p.toolMetadata.restoreToolName(originalRequestRawJSON, itemResult.Get("name").String())
		_ = functionCallItemDoc.Set("function.name", name)

		fullArgs := codexToolCallArguments(itemResult)
		if state.Patch {
			fullArgs = finishPatchChatArguments(state, fullArgs)
		}
		_ = functionCallItemDoc.Set("function.arguments", fullArgs)
		_ = templateDoc.Set("choices.0.delta.role", "assistant")
		_ = templateDoc.SetRawBytes("choices.0.delta.tool_calls", translatorcommon.JoinRawArray([][]byte{functionCallItemDoc.Bytes()}))

	} else {
		return [][]byte{}
	}

	return [][]byte{templateDoc.Bytes()}
}

// ConvertCodexResponseToOpenAINonStream converts a non-streaming Codex response to a non-streaming OpenAI response.
// This function processes the complete Codex response and transforms it into a single OpenAI-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the OpenAI API format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Codex API
//   - param: A pointer to a parameter object for the conversion (unused in current implementation)
//
// Returns:
//   - []byte: An OpenAI-compatible JSON response containing all message content and metadata
func ConvertCodexResponseToOpenAINonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	rootResult := util.ParseGJSONBytesNoCopy(rawJSON)
	// Verify this is a terminal response event.
	responseType := rootResult.Get("type").String()
	if responseType == "response.failed" || responseType == "error" {
		return buildCodexFailedOpenAIError(rootResult)
	}
	if responseType != "response.completed" && responseType != "response.incomplete" {
		return []byte{}
	}

	unixTimestamp := time.Now().Unix()

	responseResult := rootResult.Get("response")

	templateDoc := ojson.NewDocString(`{"id":"","object":"chat.completion","created":123456,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`)

	if tier := codexResponseServiceTier(responseResult); tier != "" {
		_ = templateDoc.Set("service_tier", tier)
	} else if tier := codexResponseServiceTier(rootResult); tier != "" {
		_ = templateDoc.Set("service_tier", tier)
	}

	// Extract and set the model version.
	if modelResult := responseResult.Get("model"); modelResult.Exists() {
		_ = templateDoc.Set("model", modelResult.String())
	}

	// Extract and set the creation timestamp.
	if createdAtResult := responseResult.Get("created_at"); createdAtResult.Exists() {
		_ = templateDoc.Set("created", createdAtResult.Int())
	} else {
		_ = templateDoc.Set("created", unixTimestamp)
	}

	// Extract and set the response ID.
	if idResult := responseResult.Get("id"); idResult.Exists() {
		_ = templateDoc.Set("id", idResult.String())
	}

	// Extract and set usage metadata (token counts).
	if usageResult := responseResult.Get("usage"); usageResult.Exists() {
		if outputTokensResult := usageResult.Get("output_tokens"); outputTokensResult.Exists() {
			_ = templateDoc.Set("usage.completion_tokens", outputTokensResult.Int())
		}
		if totalTokensResult := usageResult.Get("total_tokens"); totalTokensResult.Exists() {
			_ = templateDoc.Set("usage.total_tokens", totalTokensResult.Int())
		}
		if inputTokensResult := usageResult.Get("input_tokens"); inputTokensResult.Exists() {
			_ = templateDoc.Set("usage.prompt_tokens", inputTokensResult.Int())
		}
		if cachedTokensResult := usageResult.Get("input_tokens_details.cached_tokens"); cachedTokensResult.Exists() {
			_ = templateDoc.Set("usage.prompt_tokens_details.cached_tokens", cachedTokensResult.Int())
		}
		setCodexCacheWriteTokensDoc(templateDoc, usageResult)
		if reasoningTokensResult := usageResult.Get("output_tokens_details.reasoning_tokens"); reasoningTokensResult.Exists() {
			_ = templateDoc.Set("usage.completion_tokens_details.reasoning_tokens", reasoningTokensResult.Int())
		}
	}

	// Process the output array for content and function calls
	var toolMetadata codexChatToolMetadata
	var toolCalls [][]byte
	var images [][]byte
	outputResult := responseResult.Get("output")
	if outputResult.IsArray() {
		outputArray := outputResult.Array()
		var contentText string
		var reasoningText string
		var messageAnnotations [][]byte
		annotationKeys := make(map[string]struct{})
		var contentRuneOffset int64

		for _, outputItem := range outputArray {
			outputType := outputItem.Get("type").String()

			switch outputType {
			case "reasoning":
				// Extract reasoning content from summary
				if summaryResult := outputItem.Get("summary"); summaryResult.IsArray() {
					summaryArray := summaryResult.Array()
					for _, summaryItem := range summaryArray {
						if summaryItem.Get("type").String() == "summary_text" {
							if text := summaryItem.Get("text").String(); text != "" {
								reasoningText += text
							}
						}
					}
				}
				// Extract reasoning content from content
				if contentResult := outputItem.Get("content"); contentResult.IsArray() {
					contentArray := contentResult.Array()
					for _, contentItem := range contentArray {
						if contentItem.Get("type").String() == "reasoning_text" {
							if text := contentItem.Get("text").String(); text != "" {
								reasoningText += text
							}
						}
					}
				}
			case "message":
				// Extract message content and URL citations.
				if contentResult := outputItem.Get("content"); contentResult.IsArray() {
					for _, contentItem := range contentResult.Array() {
						partType := contentItem.Get("type").String()
						if partType != "output_text" && partType != "refusal" {
							continue
						}
						text := contentItem.Get("text").String()
						if partType == "refusal" {
							if refusal := contentItem.Get("refusal").String(); refusal != "" {
								text = refusal
							}
						}
						contentText += text
						messageAnnotations = append(messageAnnotations, buildCodexURLCitations(
							codexAnnotationResults(contentItem.Get("annotations")),
							contentRuneOffset,
							annotationKeys,
						)...)
						contentRuneOffset += int64(utf8.RuneCountInString(text))
					}
				}
			case "function_call", "custom_tool_call":
				// Handle function and custom tool call content.
				functionCallDoc := ojson.NewDocString(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)

				if callIdResult := outputItem.Get("call_id"); callIdResult.Exists() {
					_ = functionCallDoc.Set("id", callIdResult.String())
				}

				if nameResult := outputItem.Get("name"); nameResult.Exists() {
					n := toolMetadata.restoreToolName(originalRequestRawJSON, nameResult.String())
					_ = functionCallDoc.Set("function.name", n)
				}

				fullArgs := codexToolCallArguments(outputItem)
				if toolMetadata.isCustomPatch(originalRequestRawJSON, outputItem) {
					fullArgs = applypatch.WrapInput(fullArgs)
				}
				_ = functionCallDoc.Set("function.arguments", fullArgs)

				toolCalls = append(toolCalls, functionCallDoc.Bytes())
			case "image_generation_call":
				b64 := outputItem.Get("result").String()
				if b64 == "" {
					break
				}
				outputFormat := outputItem.Get("output_format").String()
				mimeType := mimeTypeFromCodexOutputFormat(outputFormat)
				imageURL := "data:" + mimeType + ";base64," + b64

				imagePayloadDoc := ojson.NewDocString(`{"type":"image_url","image_url":{"url":""}}`)
				_ = imagePayloadDoc.Set("index", len(images))
				_ = imagePayloadDoc.Set("image_url.url", imageURL)
				images = append(images, imagePayloadDoc.Bytes())
			}
		}

		// Set content and reasoning content if found
		if contentText != "" {
			_ = templateDoc.Set("choices.0.message.content", contentText)
		}

		if reasoningText != "" {
			_ = templateDoc.Set("choices.0.message.reasoning_content", reasoningText)
		}

		if len(messageAnnotations) > 0 {
			_ = templateDoc.SetRawBytes("choices.0.message.annotations", translatorcommon.JoinRawArray(messageAnnotations))
		}

		// Add tool calls if any
		if len(toolCalls) > 0 {
			_ = templateDoc.SetRawBytes("choices.0.message.tool_calls", translatorcommon.JoinRawArray(toolCalls))
		}

		// Add images if any
		if len(images) > 0 {
			_ = templateDoc.SetRawBytes("choices.0.message.images", translatorcommon.JoinRawArray(images))
		}
	}

	// Extract and set the finish reason based on status.
	if statusResult := responseResult.Get("status"); statusResult.Exists() {
		status := statusResult.String()
		finishReason := ""
		nativeFinishReason := ""
		switch status {
		case "completed":
			finishReason = "stop"
			nativeFinishReason = finishReason
			if len(toolCalls) > 0 {
				finishReason = "tool_calls"
				nativeFinishReason = finishReason
			}
		case "incomplete":
			nativeFinishReason = responseResult.Get("incomplete_details.reason").String()
			switch nativeFinishReason {
			case "max_tokens", "max_output_tokens":
				finishReason = "length"
			case "content_filter":
				finishReason = "content_filter"
			default:
				finishReason = "stop"
			}
		}
		if finishReason != "" {
			_ = templateDoc.Set("choices.0.finish_reason", finishReason)
			_ = templateDoc.Set("choices.0.native_finish_reason", nativeFinishReason)
		}
	}

	return templateDoc.Bytes()
}

func buildCodexFailedOpenAIError(rootResult ojson.Result) []byte {
	errNode := rootResult.Get("response.error")
	if !errNode.Exists() || errNode.Type == ojson.Null {
		errNode = rootResult.Get("error")
	}
	var msg string
	if errNode.Type == ojson.String {
		msg = strings.TrimSpace(errNode.String())
	} else {
		msg = strings.TrimSpace(errNode.Get("message").String())
	}
	if msg == "" {
		msg = strings.TrimSpace(rootResult.Get("message").String())
	}
	if msg == "" {
		msg = "upstream response failed"
	}
	errType := strings.TrimSpace(errNode.Get("type").String())
	if errType == "" {
		errType = "server_error"
	}
	errDoc := ojson.NewDocString(`{"error":{"message":"","type":""}}`)
	_ = errDoc.Set("error.message", msg)
	_ = errDoc.Set("error.type", errType)
	code := errNode.Get("code")
	if !code.Exists() || code.Type == ojson.Null {
		code = rootResult.Get("code")
	}
	if code.Exists() && code.Type != ojson.Null && strings.TrimSpace(code.String()) != "" {
		_ = errDoc.Set("error.code", strings.TrimSpace(code.String()))
	}
	return errDoc.Bytes()
}

func codexAnnotationResults(value ojson.Result) []ojson.Result {
	if !value.Exists() {
		return nil
	}
	if value.IsArray() {
		results := make([]ojson.Result, 0, len(value.Array()))
		value.ForEach(func(_, item ojson.Result) bool {
			results = append(results, item)
			return true
		})
		return results
	}
	return []ojson.Result{value}
}

func codexAnnotationsFromEvent(event ojson.Result) []ojson.Result {
	annotations := make([]ojson.Result, 0)
	for _, path := range []string{"annotation", "annotations", "part.annotations"} {
		annotations = append(annotations, codexAnnotationResults(event.Get(path))...)
	}
	item := event.Get("item")
	if item.Exists() {
		annotations = append(annotations, codexAnnotationResults(item.Get("annotations"))...)
		if content := item.Get("content"); content.IsArray() {
			for _, contentItem := range content.Array() {
				annotations = append(annotations, codexAnnotationResults(contentItem.Get("annotations"))...)
			}
		}
	}
	return annotations
}

func buildCodexURLCitations(annotations []ojson.Result, runeOffset int64, seen map[string]struct{}) [][]byte {
	citations := make([][]byte, 0, len(annotations))
	for _, annotation := range annotations {
		if annotation.Get("type").String() != "url_citation" {
			continue
		}

		rawStartIndex := annotation.Get("start_index").Int()
		rawEndIndex := annotation.Get("end_index").Int()
		startIndex := rawStartIndex + runeOffset
		endIndex := rawEndIndex + runeOffset
		if startIndex < 0 || endIndex < startIndex {
			continue
		}

		url := annotation.Get("url").String()
		key := url + "\x00" + strconv.FormatInt(rawStartIndex, 10)
		if url == "" {
			key = annotation.Get("id").String() + "\x00" + strconv.FormatInt(rawStartIndex, 10)
		}
		keys := []string{key}
		if id := annotation.Get("id").String(); id != "" {
			keys = append(keys, "id\x00"+id)
		}
		if seen != nil {
			duplicate := false
			for _, key := range keys {
				if _, exists := seen[key]; exists {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			for _, key := range keys {
				seen[strings.Clone(key)] = struct{}{}
			}
		}

		citationDoc := ojson.NewDocString(`{"type":"url_citation","url":"","title":"","start_index":0,"end_index":0}`)
		_ = citationDoc.Set("url", url)
		_ = citationDoc.Set("title", annotation.Get("title").String())
		_ = citationDoc.Set("start_index", startIndex)
		_ = citationDoc.Set("end_index", endIndex)
		citations = append(citations, citationDoc.Bytes())
	}
	return citations
}

func messageEventKeys(eventResult, itemResult ojson.Result) (itemID string, outputIdx int64, hasOutputIdx bool) {
	if itemResult.Exists() {
		itemID = strings.TrimSpace(itemResult.Get("id").String())
	}
	if itemID == "" {
		itemID = strings.TrimSpace(eventResult.Get("item_id").String())
	}
	if outIdx := eventResult.Get("output_index"); outIdx.Exists() && outIdx.Type == ojson.Number {
		outputIdx = outIdx.Int()
		hasOutputIdx = true
	}
	return itemID, outputIdx, hasOutputIdx
}

func (p *ConvertCliToOpenAIParams) resetMessageTextDeltaState() {
	p.HasMessageTextDelta = false
	p.messageStatesByID = nil
	p.messageStatesByIndex = nil
	p.openMessageStates = nil
}

func (p *ConvertCliToOpenAIParams) findMessageState(itemID string, outputIdx int64, hasOutputIdx bool) *messageStreamState {
	if itemID != "" {
		if state := p.messageStatesByID[itemID]; state != nil {
			return state
		}
		if hasOutputIdx {
			if state := p.messageStatesByIndex[outputIdx]; state != nil && state.itemID == "" {
				return state
			}
		}
		return nil
	}
	if hasOutputIdx {
		return p.messageStatesByIndex[outputIdx]
	}
	return nil
}

func (p *ConvertCliToOpenAIParams) registerMessageState(state *messageStreamState) {
	if state.itemID != "" {
		if p.messageStatesByID == nil {
			p.messageStatesByID = make(map[string]*messageStreamState)
		}
		p.messageStatesByID[state.itemID] = state
	}
	if state.hasOutputIdx {
		if p.messageStatesByIndex == nil {
			p.messageStatesByIndex = make(map[int64]*messageStreamState)
		}
		p.messageStatesByIndex[state.outputIdx] = state
	}
	if p.openMessageStates == nil {
		p.openMessageStates = make(map[*messageStreamState]struct{})
	}
	p.openMessageStates[state] = struct{}{}
}

func (p *ConvertCliToOpenAIParams) unlinkMessageState(state *messageStreamState) {
	if state == nil {
		return
	}
	if state.itemID != "" && p.messageStatesByID[state.itemID] == state {
		delete(p.messageStatesByID, state.itemID)
	}
	if state.hasOutputIdx && p.messageStatesByIndex[state.outputIdx] == state {
		delete(p.messageStatesByIndex, state.outputIdx)
	}
	delete(p.openMessageStates, state)
}

func (p *ConvertCliToOpenAIParams) startMessageState(eventResult, itemResult ojson.Result) {
	itemID, outputIdx, hasOutputIdx := messageEventKeys(eventResult, itemResult)
	if itemID == "" && !hasOutputIdx {
		p.HasMessageTextDelta = false
		return
	}
	if existing := p.findMessageState(itemID, outputIdx, hasOutputIdx); existing != nil {
		p.unlinkMessageState(existing)
	}
	state := &messageStreamState{
		itemID:       strings.Clone(itemID),
		outputIdx:    outputIdx,
		hasOutputIdx: hasOutputIdx,
	}
	p.registerMessageState(state)
}

func (p *ConvertCliToOpenAIParams) markMessageTextDelta(eventResult ojson.Result) {
	itemID, outputIdx, hasOutputIdx := messageEventKeys(eventResult, ojson.Result{})
	if itemID == "" && !hasOutputIdx {
		if len(p.openMessageStates) == 1 {
			for state := range p.openMessageStates {
				state.hasTextDelta = true
			}
			return
		}
		p.HasMessageTextDelta = true
		return
	}
	state := p.findMessageState(itemID, outputIdx, hasOutputIdx)
	if state == nil {
		state = &messageStreamState{
			itemID:       strings.Clone(itemID),
			outputIdx:    outputIdx,
			hasOutputIdx: hasOutputIdx,
		}
	} else {
		if itemID != "" && state.itemID == "" {
			state.itemID = strings.Clone(itemID)
		}
		if hasOutputIdx && !state.hasOutputIdx {
			state.outputIdx = outputIdx
			state.hasOutputIdx = true
		}
	}
	state.hasTextDelta = true
	p.registerMessageState(state)
}

func (p *ConvertCliToOpenAIParams) consumeMessageTextDelta(eventResult, itemResult ojson.Result) bool {
	itemID, outputIdx, hasOutputIdx := messageEventKeys(eventResult, itemResult)
	if itemID == "" && !hasOutputIdx {
		if len(p.openMessageStates) == 1 {
			for state := range p.openMessageStates {
				hadDelta := state.hasTextDelta
				p.unlinkMessageState(state)
				if !hadDelta && p.HasMessageTextDelta {
					hadDelta = true
					p.HasMessageTextDelta = false
				}
				return hadDelta
			}
		}
		hadDelta := p.HasMessageTextDelta
		p.HasMessageTextDelta = false
		return hadDelta
	}
	state := p.findMessageState(itemID, outputIdx, hasOutputIdx)
	if state != nil {
		hadDelta := state.hasTextDelta
		p.unlinkMessageState(state)
		if !hadDelta && p.HasMessageTextDelta {
			hadDelta = true
			p.HasMessageTextDelta = false
		}
		return hadDelta
	}
	if p.HasMessageTextDelta {
		p.HasMessageTextDelta = false
		return true
	}
	return false
}

func registerToolCallState(p *ConvertCliToOpenAIParams, eventResult, itemResult ojson.Result, state *toolCallStreamState) {
	if p.toolCallStates == nil {
		p.toolCallStates = make(map[string]*toolCallStreamState)
	}
	if itemID := eventResult.Get("item_id").String(); itemID != "" {
		p.toolCallStates[strings.Clone("item:"+itemID)] = state
	}
	if itemID := itemResult.Get("id").String(); itemID != "" {
		p.toolCallStates[strings.Clone("item:"+itemID)] = state
	}
	if outputIndex := eventResult.Get("output_index"); outputIndex.Exists() {
		p.toolCallStates[strings.Clone("output:"+outputIndex.Raw)] = state
	}
	p.currentToolCall = state
}

func findToolCallState(p *ConvertCliToOpenAIParams, eventResult, itemResult ojson.Result) *toolCallStreamState {
	if itemID := eventResult.Get("item_id").String(); itemID != "" {
		if state := p.toolCallStates["item:"+itemID]; state != nil {
			return state
		}
	}
	if itemID := itemResult.Get("id").String(); itemID != "" {
		if state := p.toolCallStates["item:"+itemID]; state != nil {
			return state
		}
	}
	if outputIndex := eventResult.Get("output_index"); outputIndex.Exists() {
		if state := p.toolCallStates["output:"+outputIndex.Raw]; state != nil {
			return state
		}
	}
	return p.currentToolCall
}

func isCodexToolCallType(itemType string) bool {
	return itemType == "function_call" || itemType == "custom_tool_call"
}

func codexToolCallArguments(itemResult ojson.Result) string {
	if itemResult.Get("type").String() == "custom_tool_call" {
		return itemResult.Get("input").String()
	}
	return itemResult.Get("arguments").String()
}

// buildReverseMapFromOriginalOpenAI builds a map of shortened tool name -> original tool name
// from the original OpenAI-style request JSON using the same shortening logic.
func buildReverseMapFromOriginalOpenAI(original []byte) map[string]string {
	rev := map[string]string{}
	names := collectRequestToolNames(original)
	if len(names) > 0 {
		m := buildShortNameMap(names)
		for orig, short := range m {
			rev[short] = orig
		}
	}
	return rev
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

// codexResponseServiceTier returns only an actual nonempty upstream tier.
func codexResponseServiceTier(response ojson.Result) string {
	tier := response.Get("service_tier")
	if tier.Type != ojson.String || strings.TrimSpace(tier.Str) == "" {
		return ""
	}
	return strings.TrimSpace(tier.Str)
}

// setCodexCacheWriteTokensDoc preserves the upstream integer without float conversion.
func setCodexCacheWriteTokensDoc(doc *ojson.Doc, usage ojson.Result) {
	value := usage.Get("input_tokens_details.cache_write_tokens")
	if !value.Exists() || value.Type == ojson.Null {
		return
	}
	valid := value.Type == ojson.Number && value.Raw != ""
	for _, digit := range value.Raw {
		if digit < '0' || digit > '9' {
			valid = false
			break
		}
	}
	if !valid {
		log.WithField("field", "usage.input_tokens_details.cache_write_tokens").Warn("Ignoring invalid Codex cache write token count")
		return
	}
	_ = doc.SetRawBytes("usage.prompt_tokens_details.cache_write_tokens", []byte(value.Raw))
	_ = doc.SetRawBytes("usage.prompt_tokens_details.cached_creation_tokens", []byte(value.Raw))
}

// isOriginalCustomPatch never promotes an ordinary same-name function to custom.
func isOriginalCustomPatch(original []byte, item ojson.Result) bool {
	if item.Get("type").String() != "custom_tool_call" {
		return false
	}
	name := util.QualifyResponsesNamespaceToolName(item.Get("namespace").String(), item.Get("name").String())
	// Chat Completions prefers ordinary functions for ambiguous names, regardless of order.
	for _, tool := range ojson.GetBytesNoCopy(original, "tools").Array() {
		if tool.Get("type").String() == "function" && tool.Get("function.name").String() == name {
			return false
		}
	}
	winner, ok := collectChatToolWinners(original)[name]
	return ok && applypatch.IsCustomTool(winner.Tool)
}

func finishPatchChatArguments(state *toolCallStreamState, input string) string {
	if state.InputClosed {
		return ""
	}
	state.InputClosed = true
	if state.InputStarted {
		return `"}`
	}
	return applypatch.WrapInput(input)
}
