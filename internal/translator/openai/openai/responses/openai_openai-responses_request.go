package responses

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// ConvertOpenAIResponsesRequestToOpenAIChatCompletions converts OpenAI responses format to OpenAI chat completions format.
// It transforms the OpenAI responses API format (with instructions and input array) into the standard
// OpenAI chat completions format (with messages array and system content).
//
// The conversion handles:
// 1. Model name and streaming configuration
// 2. Instructions to system message conversion
// 3. Input array to messages array transformation
// 4. Tool definitions and tool choice conversion
// 5. Function calls and function results handling
// 6. Generation parameters mapping (max_tokens, reasoning, etc.)
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data in OpenAI responses format
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in OpenAI chat completions format
func ConvertOpenAIResponsesRequestToOpenAIChatCompletions(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertOpenAIResponsesRequestToOpenAIChatCompletions(modelName, inputRawJSON, stream)

}

// convertOpenAIResponsesRequestToOpenAIChatCompletions also reports a file or
// audio part Chat Completions cannot receive when it leaves a user turn with
// nothing to send.
func convertOpenAIResponsesRequestToOpenAIChatCompletions(modelName string, inputRawJSON []byte, stream bool, inlineToolMedia ...bool) ([]byte, error) {
	rawJSON := inputRawJSON
	toolMedia := relayToolFiles
	if len(inlineToolMedia) > 0 && inlineToolMedia[0] {
		toolMedia = inlineToolAttachments
	}
	var drops translatorcommon.UserTurnDrops
	var toolOutputErr error
	// Base OpenAI chat completions template with default values
	outDoc := ojson.NewDocString(`{"model":"","messages":[],"stream":false}`)

	root := util.ParseGJSONBytesNoCopy(rawJSON)
	toolIndex := newResponsesToolIndex(root)

	messages := make([][]byte, 0)
	appendMessage := func(message []byte) {
		messages = append(messages, message)
	}

	// Set model name
	_ = outDoc.Set("model", modelName)

	// Set stream configuration
	_ = outDoc.Set("stream", stream)

	// Map Responses text format to Chat Completions response format.
	if textFormat := root.Get("text.format"); textFormat.Exists() {
		if responseFormat := convertResponsesTextFormatToChatResponseFormat(textFormat); len(responseFormat) > 0 {
			_ = outDoc.SetRawBytes("response_format", responseFormat)
		}
	}

	// Map generation parameters from responses format to chat completions format
	if maxTokens := root.Get("max_output_tokens"); maxTokens.Exists() {
		if maxTokens.Raw != "" {
			_ = outDoc.SetRaw("max_tokens", maxTokens.Raw)
		} else {
			_ = outDoc.Set("max_tokens", maxTokens.Value())
		}
	}

	// Convert instructions to system message
	if instructions := root.Get("instructions"); instructions.Exists() {
		systemMessage := []byte(`{"role":"system","content":""}`)
		systemMessage, _ = ojson.SetBytes(systemMessage, "content", instructions.String())
		appendMessage(systemMessage)
	}

	duplicateOutputIDs := make(map[string]struct{})

	// Convert input array to messages
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		rawInputArray := toolIndex.toolSearchHistory(toolIndex.shellHistory(input.Array()))
		explicitOutputCounts := make(map[string]int)
		missingIDOutputsCount := 0
		for _, item := range rawInputArray {
			itemType := item.Get("type").String()
			if itemType == "function_call_output" || itemType == "custom_tool_call_output" {
				id := translatorcommon.ExtractResponsesCallID(item)
				if id != "" {
					explicitOutputCounts[id]++
				} else {
					missingIDOutputsCount++
				}
			}
		}

		unclaimedCalls := make(map[string]bool)
		for _, item := range rawInputArray {
			itemType := item.Get("type").String()
			if itemType == "function_call" || itemType == "custom_tool_call" {
				id := translatorcommon.ExtractResponsesCallID(item)
				if id != "" && explicitOutputCounts[id] == 0 {
					unclaimedCalls[id] = true
				}
			}
		}

		inputItems := translatorcommon.NormalizeResponsesToolCallOutputs(rawInputArray)
		if missingIDOutputsCount > 1 || (missingIDOutputsCount > 0 && len(unclaimedCalls) > 1) {
			for idx, item := range inputItems {
				itemType := item.Get("type").String()
				if itemType == "function_call_output" || itemType == "custom_tool_call_output" {
					if idx < len(rawInputArray) && translatorcommon.ExtractResponsesCallID(rawInputArray[idx]) == "" {
						rawDoc := ojson.NewDocString(item.Raw)
						_ = rawDoc.Delete("call_id")
						_ = rawDoc.Delete("tool_call_id")
						_ = rawDoc.Delete("callId")
						inputItems[idx] = util.ParseGJSONBytesNoCopy(rawDoc.Bytes())
					}
				}
			}
		}

		hasReasoningInSession := false
		if effortRes := root.Get("reasoning.effort"); effortRes.Exists() {
			effort := strings.ToLower(strings.TrimSpace(effortRes.String()))
			if effort != "" && effort != "none" && effort != "0" && effort != "false" {
				hasReasoningInSession = true
			}
		} else if effortRes := root.Get("reasoning_effort"); effortRes.Exists() {
			effort := strings.ToLower(strings.TrimSpace(effortRes.String()))
			if effort != "" && effort != "none" && effort != "0" && effort != "false" {
				hasReasoningInSession = true
			}
		} else if reasoningObj := root.Get("reasoning"); reasoningObj.Exists() {
			reasoningRaw := strings.ToLower(strings.TrimSpace(reasoningObj.String()))
			if reasoningRaw != "" && reasoningRaw != "none" && reasoningRaw != "false" && reasoningRaw != "{}" {
				hasReasoningInSession = true
			}
		}
		if !hasReasoningInSession {
			for _, item := range rawInputArray {
				itemType := item.Get("type").String()
				if itemType == "reasoning" || item.Get("reasoning_content").Exists() {
					hasReasoningInSession = true
					break
				}
			}
		}

		pendingToolCalls := make([]interface{}, 0)
		pendingToolCallIDs := make([]string, 0)
		pendingReasoningContent := ""
		latestReasoningContent := ""
		awaitingToolOutputs := make(map[string]struct{})
		outputCounts := make(map[string]int)
		mergeableAssistantIndex := -1
		var pendingToolFileParts [][]byte

		fallbackToolReasoning := func() string {
			if latestReasoningContent != "" {
				return latestReasoningContent
			}
			if hasReasoningInSession {
				return "[reasoning unavailable]"
			}
			return ""
		}

		takePendingReasoningContent := func() string {
			reasoningContent := pendingReasoningContent
			pendingReasoningContent = ""
			return reasoningContent
		}
		flushPendingToolCalls := func() {
			if len(pendingToolCalls) == 0 {
				return
			}

			reasoningContent := takePendingReasoningContent()
			mergedIntoAssistant := false
			if mergeableAssistantIndex >= 0 && mergeableAssistantIndex == len(messages)-1 {
				assistantMessage := util.ParseGJSONBytesNoCopy(messages[mergeableAssistantIndex])
				if assistantMessage.Get("role").String() == "assistant" && !assistantMessage.Get("tool_calls").Exists() {
					updatedMessageDoc := ojson.NewDoc(messages[mergeableAssistantIndex])
					_ = updatedMessageDoc.Set("tool_calls", pendingToolCalls)
					combinedReasoning := combineOpenAIResponsesReasoning(assistantMessage.Get("reasoning_content").String(), reasoningContent)
					if combinedReasoning != "" {
						_ = updatedMessageDoc.Set("reasoning_content", combinedReasoning)
						if isUsableResponsesReasoning(combinedReasoning) {
							latestReasoningContent = combinedReasoning
						}
					} else if fallback := fallbackToolReasoning(); fallback != "" {
						_ = updatedMessageDoc.Set("reasoning_content", fallback)
					}
					messages[mergeableAssistantIndex] = updatedMessageDoc.Bytes()
					mergedIntoAssistant = true
				}
			}
			if !mergedIntoAssistant {
				assistantMessageDoc := ojson.NewDocString(`{"role":"assistant","tool_calls":[]}`)
				_ = assistantMessageDoc.Set("tool_calls", pendingToolCalls)
				if reasoningContent != "" {
					_ = assistantMessageDoc.Set("reasoning_content", reasoningContent)
					if isUsableResponsesReasoning(reasoningContent) {
						latestReasoningContent = reasoningContent
					}
				} else if fallback := fallbackToolReasoning(); fallback != "" {
					_ = assistantMessageDoc.Set("reasoning_content", fallback)
				}
				appendMessage(assistantMessageDoc.Bytes())
			}
			for _, id := range pendingToolCallIDs {
				trimmed := strings.TrimSpace(id)
				if trimmed == "" {
					continue
				}
				awaitingToolOutputs[trimmed] = struct{}{}
			}
			pendingToolCalls = pendingToolCalls[:0]
			pendingToolCallIDs = pendingToolCallIDs[:0]
			mergeableAssistantIndex = -1
		}
		appendRegularMessage := func(message []byte) int {
			appendMessage(message)
			return len(messages) - 1
		}
		flushPendingToolFileParts := func() {
			if len(pendingToolFileParts) == 0 {
				return
			}
			userDoc := ojson.NewDocString(`{"role":"user","content":[]}`)
			translatorcommon.SetRawArrayItemsDoc(userDoc, "content", pendingToolFileParts)
			appendRegularMessage(userDoc.Bytes())
			pendingToolFileParts = nil
		}
		appendPendingReasoningMessage := func() {
			reasoningContent := takePendingReasoningContent()
			if reasoningContent == "" {
				return
			}
			if isUsableResponsesReasoning(reasoningContent) {
				latestReasoningContent = reasoningContent
			}
			message := []byte(`{"role":"assistant","content":"","reasoning_content":""}`)
			message, _ = ojson.SetBytes(message, "reasoning_content", reasoningContent)
			appendRegularMessage(message)
		}

		for _, item := range inputItems {
			itemType := item.Get("type").String()
			if itemType == "" && item.Get("role").String() != "" {
				itemType = "message"
			}
			if itemType != "function_call" && itemType != "custom_tool_call" {
				flushPendingToolCalls()
			}
			if itemType != "function_call_output" && itemType != "custom_tool_call_output" {
				flushPendingToolFileParts()
			}

			switch itemType {
			case "message", "":
				// Handle regular message conversion
				role := item.Get("role").String()
				// Chat Completions supports developer messages. Preserve their role and
				// position so mid-session instructions retain their original authority.
				// Only real user turns participate in unsupported-content refusal.
				isUserTurn := role == "user"
				mergeableAssistantIndex = -1
				if role != "assistant" {
					appendPendingReasoningMessage()
					latestReasoningContent = ""
				}
				messageDoc := ojson.NewDocString(`{"role":"","content":[]}`)
				_ = messageDoc.Set("role", role)

				if content := item.Get("content"); content.Exists() && content.IsArray() {
					var contentItems [][]byte
					// Counts the parts this turn really sends; an empty text part does not.
					turnSendable := 0
					content.ForEach(func(_, contentItem ojson.Result) bool {
						contentType := contentItem.Get("type").String()
						if contentType == "" {
							contentType = "input_text"
						}

						switch contentType {
						case "input_text", "output_text":
							text := contentItem.Get("text").String()
							contentPart := []byte(`{"type":"text","text":""}`)
							contentPart, _ = ojson.SetBytes(contentPart, "text", text)
							contentItems = append(contentItems, contentPart)
							if text != "" {
								turnSendable++
							}
						case "input_video", "video_url":
							contentPartDoc := ojson.NewDocString(`{"type":"video_url","video_url":{}}`)
							videoURL := contentItem.Get("video_url")
							if videoURL.IsObject() {
								_ = contentPartDoc.SetRaw("video_url", videoURL.Raw)
							} else if videoURL.Exists() {
								_ = contentPartDoc.SetRaw("video_url.url", videoURL.Raw)
							}
							if processing := contentItem.Get("processing"); processing.Exists() {
								_ = contentPartDoc.SetRaw("video_url.processing", processing.Raw)
							}
							// Preserve malformed video parts for upstream validation instead of
							// silently turning a video request into a text-only request.
							contentItems = append(contentItems, contentPartDoc.Bytes())
							turnSendable++
						case "input_image":
							imageURL := contentItem.Get("image_url").String()
							contentPartDoc := ojson.NewDocString(`{"type":"image_url","image_url":{"url":""}}`)
							_ = contentPartDoc.Set("image_url.url", imageURL)
							if detail, ok := normalizeChatImageDetail(contentItem.Get("detail")); ok && detail != "" {
								_ = contentPartDoc.Set("image_url.detail", detail)
							}
							contentItems = append(contentItems, contentPartDoc.Bytes())
							turnSendable++
						case "input_file":
							if contentPart, ok := responsesInputFileToChatPart(contentItem); ok {
								contentItems = append(contentItems, contentPart)
								turnSendable++
							} else if isUserTurn {
								// Chat Completions takes a file id or inline bytes only.
								drops.Drop(contentType)
							}
						case "input_audio":
							if contentPart, ok := responsesInputAudioToChatPart(contentItem); ok {
								contentItems = append(contentItems, contentPart)
								turnSendable++
							} else if isUserTurn {
								drops.Drop(contentType)
							}
						}
						return true
					})
					if isUserTurn {
						drops.EndTurn(turnSendable)
					}
					translatorcommon.SetRawArrayItemsDoc(messageDoc, "content", contentItems)
				} else if content.Type == ojson.String {
					_ = messageDoc.Set("content", content.String())
				}

				if role == "assistant" {
					reasoningContent := combineOpenAIResponsesReasoning(takePendingReasoningContent(), item.Get("reasoning_content").String())
					if reasoningContent != "" {
						_ = messageDoc.Set("reasoning_content", reasoningContent)
						if isUsableResponsesReasoning(reasoningContent) {
							latestReasoningContent = reasoningContent
						}
					}
				}

				messageIndex := appendRegularMessage(messageDoc.Bytes())
				if role == "assistant" {
					mergeableAssistantIndex = messageIndex
				}

			case "reasoning":
				reasoningContent := collectOpenAIResponsesReasoningContent(item)
				pendingReasoningContent = combineOpenAIResponsesReasoning(pendingReasoningContent, reasoningContent)
				if isUsableResponsesReasoning(reasoningContent) {
					latestReasoningContent = reasoningContent
				}

			case "function_call":
				rc := item.Get("reasoning_content").String()
				pendingReasoningContent = combineOpenAIResponsesReasoning(pendingReasoningContent, rc)
				if isUsableResponsesReasoning(rc) {
					latestReasoningContent = rc
				}
				// Buffer consecutive function calls and emit them as one assistant message.
				toolCallDoc := ojson.NewDocString(`{"id":"","type":"function","function":{"name":"","arguments":"{}"}}`)

				if callId := translatorcommon.ExtractResponsesCallID(item); callId != "" {
					_ = toolCallDoc.Set("id", callId)
				}

				if name := item.Get("name"); name.Exists() {
					functionName := name.String()
					if namespace := strings.TrimSpace(item.Get("namespace").String()); namespace != "" {
						functionName = toolIndex.namespaceName(namespace, functionName)
					} else {
						functionName = toolIndex.canonicalName(functionName)
					}
					_ = toolCallDoc.Set("function.name", functionName)
				}

				if arguments := item.Get("arguments"); arguments.Exists() && strings.TrimSpace(arguments.String()) != "" {
					_ = toolCallDoc.Set("function.arguments", arguments.String())
				}
				pendingToolCalls = append(pendingToolCalls, util.ParseGJSONBytesNoCopy(toolCallDoc.Bytes()).Value())
				if callID := translatorcommon.ExtractResponsesCallID(item); callID != "" {
					pendingToolCallIDs = append(pendingToolCallIDs, callID)
				}

			case "function_call_output":
				mergeableAssistantIndex = -1
				callID := translatorcommon.ExtractResponsesCallID(item)
				if callID != "" {
					outputCounts[callID]++
					if outputCounts[callID] > 1 {
						duplicateOutputIDs[callID] = struct{}{}
					}
				}
				if _, awaiting := awaitingToolOutputs[callID]; !awaiting {
					// Orphan outputs (empty call_id or no matching assistant
					// tool_calls, e.g. Codex send_message_to_thread cards) must
					// not become tool messages. Emit as user text instead.
					if errStandalone := appendStandaloneResponsesToolOutputAsUser(item.Get("output"), setFunctionCallOutputContentDoc, appendRegularMessage); errStandalone != nil && toolOutputErr == nil {
						toolOutputErr = errStandalone
					}
				} else {
					toolMessageDoc := ojson.NewDocString(`{"role":"tool","tool_call_id":"","content":""}`)
					_ = toolMessageDoc.Set("tool_call_id", callID)
					delete(awaitingToolOutputs, callID)
					if output := item.Get("output"); output.Exists() {
						fileParts, errToolOut := setFunctionCallOutputContentDoc(toolMessageDoc, output, toolMedia)
						if errToolOut != nil && toolOutputErr == nil {
							toolOutputErr = errToolOut
						}
						if len(fileParts) > 0 {
							pendingToolFileParts = append(pendingToolFileParts, fileParts...)
						}
					}
					appendMessage(toolMessageDoc.Bytes())
					if len(awaitingToolOutputs) == 0 {
						flushPendingToolFileParts()
					}
				}

			case "custom_tool_call":
				rc := item.Get("reasoning_content").String()
				pendingReasoningContent = combineOpenAIResponsesReasoning(pendingReasoningContent, rc)
				if isUsableResponsesReasoning(rc) {
					latestReasoningContent = rc
				}
				// Codex freeform tool call replay: wrap the raw input so it
				// matches the {"input": string} function shape used when
				// converting custom tool definitions.
				toolCallDoc := ojson.NewDocString(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
				_ = toolCallDoc.Set("id", translatorcommon.ExtractResponsesCallID(item))
				functionName := item.Get("name").String()
				if namespace := item.Get("namespace").String(); namespace != "" {
					functionName = toolIndex.namespaceName(namespace, functionName)
				} else {
					functionName = toolIndex.canonicalName(functionName)
				}
				_ = toolCallDoc.Set("function.name", functionName)
				wrappedArgs, _ := ojson.SetBytes([]byte(`{"input":""}`), "input", item.Get("input").String())
				_ = toolCallDoc.Set("function.arguments", string(wrappedArgs))
				pendingToolCalls = append(pendingToolCalls, util.ParseGJSONBytesNoCopy(toolCallDoc.Bytes()).Value())
				if callID := translatorcommon.ExtractResponsesCallID(item); callID != "" {
					pendingToolCallIDs = append(pendingToolCallIDs, callID)
				}

			case "custom_tool_call_output":
				mergeableAssistantIndex = -1
				callID := translatorcommon.ExtractResponsesCallID(item)
				if callID != "" {
					outputCounts[callID]++
					if outputCounts[callID] > 1 {
						duplicateOutputIDs[callID] = struct{}{}
					}
				}
				if _, awaiting := awaitingToolOutputs[callID]; !awaiting {
					if errStandalone := appendStandaloneResponsesToolOutputAsUser(item.Get("output"), setCustomToolCallOutputContentDoc, appendRegularMessage); errStandalone != nil && toolOutputErr == nil {
						toolOutputErr = errStandalone
					}
				} else {
					toolMessageDoc := ojson.NewDocString(`{"role":"tool","tool_call_id":"","content":""}`)
					_ = toolMessageDoc.Set("tool_call_id", callID)
					delete(awaitingToolOutputs, callID)
					if output := item.Get("output"); output.Exists() {
						fileParts, errToolOut := setCustomToolCallOutputContentDoc(toolMessageDoc, output, toolMedia)
						if errToolOut != nil && toolOutputErr == nil {
							toolOutputErr = errToolOut
						}
						if len(fileParts) > 0 {
							pendingToolFileParts = append(pendingToolFileParts, fileParts...)
						}
					}
					appendMessage(toolMessageDoc.Bytes())
					if len(awaitingToolOutputs) == 0 {
						flushPendingToolFileParts()
					}
				}

			default:
				mergeableAssistantIndex = -1
			}

		}
		flushPendingToolCalls()
		flushPendingToolFileParts()
		appendPendingReasoningMessage()
	} else if input.Type == ojson.String {
		msgDoc := ojson.NewDocString(`{}`)
		_ = msgDoc.Set("role", "user")
		_ = msgDoc.Set("content", input.String())
		appendMessage(msgDoc.Bytes())
	}

	if len(messages) > 0 {
		var extraAmbiguous []string
		for id := range duplicateOutputIDs {
			extraAmbiguous = append(extraAmbiguous, id)
		}
		messages = translatorcommon.AlignOpenAIToolCallMessages(messages, extraAmbiguous...)
		_ = outDoc.SetRawBytes("messages", translatorcommon.JoinRawArray(messages))
	}

	// Convert tools from responses format to chat completions format.
	// Codex Desktop (Responses Lite) delivers tool definitions through an
	// "additional_tools" input item instead of the top-level "tools" field,
	// so merge both sources.
	var chatCompletionsTools []interface{}
	for _, chatTool := range toolIndex.chatTools() {
		chatCompletionsTools = append(chatCompletionsTools, util.ParseGJSONBytesNoCopy(chatTool).Value())
	}
	if len(chatCompletionsTools) > 0 {
		_ = outDoc.Set("tools", chatCompletionsTools)
		if parallelToolCalls := root.Get("parallel_tool_calls"); parallelToolCalls.Exists() {
			_ = outDoc.Set("parallel_tool_calls", parallelToolCalls.Bool())
		}
		if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
			_ = outDoc.SetRawBytes("tool_choice", convertResponsesToolChoiceWithIndex(toolChoice, toolIndex))
		}
	}

	if reasoningEffort := root.Get("reasoning.effort"); reasoningEffort.Exists() {
		effort := strings.ToLower(strings.TrimSpace(reasoningEffort.String()))
		if effort != "" {
			_ = outDoc.Set("reasoning_effort", effort)
		}
	}

	if errDrops := drops.Err(); errDrops != nil {
		return outDoc.Bytes(), errDrops
	}
	return outDoc.Bytes(), toolOutputErr
}

// responsesInputFileToChatPart maps a Responses input_file part onto a Chat
// Completions file part. It reports false when the part carries neither a file
// id nor inline bytes, because Chat Completions has no field for a bare url.
func responsesInputFileToChatPart(contentItem ojson.Result) ([]byte, bool) {
	fileID := contentItem.Get("file_id").String()
	fileData := contentItem.Get("file_data").String()
	if fileID == "" && fileData == "" {
		return nil, false
	}
	partDoc := ojson.NewDocString(`{"type":"file","file":{}}`)
	if fileID != "" {
		_ = partDoc.Set("file.file_id", fileID)
	}
	if fileData != "" {
		_ = partDoc.Set("file.file_data", fileData)
	}
	if filename := contentItem.Get("filename").String(); filename != "" {
		_ = partDoc.Set("file.filename", filename)
	}
	return partDoc.Bytes(), true
}

// responsesInputAudioToChatPart maps a Responses input_audio part onto a Chat
// Completions input_audio part. It reports false when the part has no bytes.
func responsesInputAudioToChatPart(contentItem ojson.Result) ([]byte, bool) {
	audio := contentItem.Get("input_audio")
	data := audio.Get("data").String()
	if data == "" {
		data = contentItem.Get("data").String()
	}
	if data == "" {
		return nil, false
	}
	format := audio.Get("format").String()
	if format == "" {
		format = contentItem.Get("format").String()
	}
	partDoc := ojson.NewDocString(`{"type":"input_audio","input_audio":{"data":""}}`)
	_ = partDoc.Set("input_audio.data", data)
	if format != "" {
		_ = partDoc.Set("input_audio.format", format)
	}
	return partDoc.Bytes(), true
}

func convertResponsesToolChoiceToChatCompletions(toolChoice ojson.Result, inputRawJSON []byte) []byte {
	return convertResponsesToolChoiceWithIndex(toolChoice, newResponsesToolIndex(util.ParseGJSONBytesNoCopy(inputRawJSON)))
}

func convertResponsesToolChoiceWithIndex(toolChoice ojson.Result, toolIndex *responsesToolIndex) []byte {
	if !toolChoice.IsObject() {
		return []byte(toolChoice.Raw)
	}

	choiceType := toolChoice.Get("type").String()
	if choiceType == "shell" {
		if name := toolIndex.shellName(); name != "" {
			choiceDoc := ojson.NewDocString(`{"type":"function","function":{}}`)
			_ = choiceDoc.Set("function.name", name)
			return choiceDoc.Bytes()
		}
	}
	if choiceType == util.ResponsesToolSearchType {
		if name := toolIndex.toolSearchName(); name != "" {
			choiceDoc := ojson.NewDocString(`{"type":"function","function":{}}`)
			_ = choiceDoc.Set("function.name", name)
			return choiceDoc.Bytes()
		}
	}
	if choiceType != "function" && choiceType != "custom" {
		return []byte(toolChoice.Raw)
	}

	name := toolChoice.Get("function.name").String()
	if name == "" {
		name = toolChoice.Get("custom.name").String()
	}
	if name == "" {
		name = toolChoice.Get("name").String()
	}
	if name == "" {
		return []byte(toolChoice.Raw)
	}

	namespace := strings.TrimSpace(toolChoice.Get("namespace").String())
	if namespace == "" {
		namespace = strings.TrimSpace(toolChoice.Get("function.namespace").String())
	}
	if namespace == "" {
		namespace = strings.TrimSpace(toolChoice.Get("custom.namespace").String())
	}
	if namespace != "" {
		name = toolIndex.namespaceName(namespace, name)
	} else {
		name = toolIndex.canonicalName(name)
	}

	converted := []byte(`{"type":"function","function":{"name":""}}`)
	converted, _ = ojson.SetBytes(converted, "function.name", name)
	return converted
}

func convertResponsesTextFormatToChatResponseFormat(textFormat ojson.Result) []byte {
	formatType := textFormat.Get("type").String()
	switch formatType {
	case "text", "json_object":
		responseFormat := []byte(`{"type":""}`)
		responseFormat, _ = ojson.SetBytes(responseFormat, "type", formatType)
		return responseFormat
	case "json_schema":
		responseFormatDoc := ojson.NewDocString(`{"type":"json_schema","json_schema":{}}`)
		for _, field := range []string{"name", "description", "strict"} {
			if value := textFormat.Get(field); value.Exists() {
				_ = responseFormatDoc.Set("json_schema."+field, value.Value())
			}
		}
		if schema := textFormat.Get("schema"); schema.Exists() {
			_ = responseFormatDoc.SetRaw("json_schema.schema", schema.Raw)
		}
		return responseFormatDoc.Bytes()
	default:
		return nil
	}
}

type chatToolMediaMode uint8

const (
	relayToolFiles chatToolMediaMode = iota
	standaloneToolOutput
	inlineToolAttachments
)

func appendStandaloneResponsesToolOutputAsUser(output ojson.Result, setContentDoc func(*ojson.Doc, ojson.Result, chatToolMediaMode) ([][]byte, error), appendMessage func([]byte) int) error {
	userMessageDoc := ojson.NewDocString(`{"role":"user","content":""}`)
	if output.Exists() {
		if _, errSet := setContentDoc(userMessageDoc, output, standaloneToolOutput); errSet != nil {
			return errSet
		}
	}
	userMessage := userMessageDoc.Bytes()
	content := ojson.GetBytesNoCopy(userMessage, "content")
	if !content.Exists() {
		return nil
	}
	if content.Type == ojson.String && strings.TrimSpace(content.String()) == "" {
		return nil
	}
	if content.IsArray() && !content.Get("0").Exists() {
		return nil
	}
	appendMessage(userMessage)
	return nil
}

func setFunctionCallOutputContentDoc(toolMessageDoc *ojson.Doc, output ojson.Result, mode chatToolMediaMode) ([][]byte, error) {
	structuredContent := output
	if output.Type == ojson.String {
		if !ojson.Valid(output.String()) {
			_ = toolMessageDoc.Set("content", output.String())
			return nil, nil
		}
		structuredContent = ojson.Parse(output.String())
	}

	items, hasImage, hasFile, invalidTextOrImage, badImageType, unsupportedType := inspectChatToolOutputParts(structuredContent)
	if unsupportedType != "" {
		return nil, &translatorcommon.UnsupportedPartError{Type: unsupportedType}
	}
	if hasFile || (structuredContent.IsObject() && hasImage) {
		if badImageType != "" {
			return nil, &translatorcommon.UnsupportedPartError{Type: badImageType}
		}
		var toolItems [][]byte
		var userMediaItems [][]byte
		var textParts []string
		for _, item := range items {
			switch item.Get("type").String() {
			case "input_file", "file":
				if filePart, ok := chatToolOutputFilePart(item); ok {
					if mode == relayToolFiles {
						userMediaItems = append(userMediaItems, filePart)
					} else {
						toolItems = append(toolItems, filePart)
					}
				}
			case "image_url", "input_image":
				if _, _, ok := chatToolOutputImageFields(item); ok {
					imgPart := chatToolOutputContentPart(item, mode)
					if mode == relayToolFiles {
						userMediaItems = append(userMediaItems, imgPart)
					} else {
						toolItems = append(toolItems, imgPart)
					}
				}
			case "text", "input_text", "output_text":
				if item.Get("text").Type == ojson.String {
					txt := item.Get("text").String()
					textParts = append(textParts, txt)
					toolItems = append(toolItems, chatToolOutputContentPart(item, mode))
				} else {
					fallback := chatToolOutputFallbackPart(item)
					textParts = append(textParts, ojson.GetBytesNoCopy(fallback, "text").String())
					toolItems = append(toolItems, fallback)
				}
			default:
				fallback := chatToolOutputFallbackPart(item)
				textParts = append(textParts, ojson.GetBytesNoCopy(fallback, "text").String())
				toolItems = append(toolItems, fallback)
			}
		}
		if mode == relayToolFiles {
			if len(textParts) == 0 {
				_ = toolMessageDoc.Set("content", "Attached media from tool output follows in the user message.")
			} else {
				_ = toolMessageDoc.Set("content", strings.Join(textParts, "\n"))
			}
			return userMediaItems, nil
		}
		translatorcommon.SetRawArrayItemsDoc(toolMessageDoc, "content", toolItems)
		return nil, nil
	}
	if structuredContent.IsObject() && badImageType != "" {
		return nil, &translatorcommon.UnsupportedPartError{Type: badImageType}
	}
	if hasImage && !invalidTextOrImage {
		contentItems := make([][]byte, 0, len(items))
		for _, item := range items {
			contentItems = append(contentItems, chatToolOutputContentPart(item, mode))
		}
		translatorcommon.SetRawArrayItemsDoc(toolMessageDoc, "content", contentItems)
		return nil, nil
	}

	_ = toolMessageDoc.Set("content", output.String())
	return nil, nil
}

func setCustomToolCallOutputContentDoc(toolMessageDoc *ojson.Doc, output ojson.Result, mode chatToolMediaMode) ([][]byte, error) {
	structuredContent := output
	if output.Type == ojson.String && ojson.Valid(output.String()) {
		structuredContent = ojson.Parse(output.String())
	}
	_, hasImage, hasFile, invalidTextOrImage, badImageType, unsupportedType := inspectChatToolOutputParts(structuredContent)
	if unsupportedType != "" {
		return nil, &translatorcommon.UnsupportedPartError{Type: unsupportedType}
	}
	if hasFile || (structuredContent.IsObject() && (hasImage || badImageType != "")) || (hasImage && !invalidTextOrImage) {
		return setFunctionCallOutputContentDoc(toolMessageDoc, output, mode)
	}

	_ = toolMessageDoc.Set("content", responsesToolOutputText(output))
	return nil, nil
}

func chatToolOutputContentPart(item ojson.Result, mode chatToolMediaMode) []byte {
	itemType := item.Get("type").String()
	switch itemType {
	case "text", "input_text", "output_text":
		part := []byte(`{"type":"text","text":""}`)
		part, _ = ojson.SetBytes(part, "text", item.Get("text").String())
		return part
	case "image_url", "input_image":
		imageURL, detail, ok := chatToolOutputImageFields(item)
		if !ok {
			return chatToolOutputFallbackPart(item)
		}
		partDoc := ojson.NewDocString(`{"type":"image_url","image_url":{"url":""}}`)
		_ = partDoc.Set("image_url.url", imageURL)
		if desc := item.Get("description"); mode == inlineToolAttachments && desc.Type == ojson.String {
			_ = partDoc.Set("description", desc.String())
		}
		if detail != "" {
			_ = partDoc.Set("image_url.detail", detail)
		}
		return partDoc.Bytes()
	case "input_file", "file":
		if filePart, ok := chatToolOutputFilePart(item); ok {
			return filePart
		}
		return chatToolOutputFallbackPart(item)
	default:
		return chatToolOutputFallbackPart(item)
	}
}

func chatToolOutputFilePart(item ojson.Result) ([]byte, bool) {
	if item.Get("type").String() == "file" && item.Get("file").IsObject() {
		return responsesInputFileToChatPart(item.Get("file"))
	}
	return responsesInputFileToChatPart(item)
}

func isChatToolOutputPartObject(content ojson.Result) bool {
	if !content.IsObject() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(content.Get("type").String())) {
	case "input_text", "output_text", "image_url", "input_image", "input_file", "file", "input_audio", "audio", "input_video", "video":
		return true
	case "text":
		return content.Get("text").Type == ojson.String
	default:
		return false
	}
}

func inspectChatToolOutputParts(content ojson.Result) (items []ojson.Result, hasImage bool, hasFile bool, invalidTextOrImage bool, badImageType string, unsupportedType string) {
	switch {
	case content.IsArray():
		items = content.Array()
	case isChatToolOutputPartObject(content):
		items = []ojson.Result{content}
	default:
		return nil, false, false, false, "", ""
	}

	for _, item := range items {
		itemType := item.Get("type")
		if itemType.Type != ojson.String {
			continue
		}
		typeName := itemType.String()
		switch typeName {
		case "text", "input_text", "output_text":
			if item.Get("text").Type != ojson.String {
				invalidTextOrImage = true
			}
		case "image_url", "input_image":
			if _, _, ok := chatToolOutputImageFields(item); !ok {
				invalidTextOrImage = true
				if badImageType == "" {
					badImageType = typeName
				}
			} else {
				hasImage = true
			}
		case "input_file", "file":
			if _, ok := chatToolOutputFilePart(item); ok {
				hasFile = true
			} else if unsupportedType == "" {
				unsupportedType = typeName
			}
		case "input_audio", "audio", "input_video", "video":
			if unsupportedType == "" {
				unsupportedType = typeName
			}
		}
	}
	return items, hasImage, hasFile, invalidTextOrImage, badImageType, unsupportedType
}

func chatToolOutputImageFields(item ojson.Result) (imageURL, detail string, ok bool) {
	var imageURLValue ojson.Result
	var detailValue ojson.Result
	switch item.Get("type").String() {
	case "image_url":
		imageURLValue = item.Get("image_url.url")
		detailValue = item.Get("image_url.detail")
	case "input_image":
		imageURLValue = item.Get("image_url")
		detailValue = item.Get("detail")
	default:
		return "", "", false
	}

	if imageURLValue.Type != ojson.String {
		return "", "", false
	}
	imageURL = strings.TrimSpace(imageURLValue.String())
	if imageURL == "" {
		return "", "", false
	}

	detail, ok = normalizeChatImageDetail(detailValue)
	if !ok {
		return "", "", false
	}
	return imageURL, detail, true
}

func normalizeChatImageDetail(detailValue ojson.Result) (string, bool) {
	if !detailValue.Exists() {
		return "", true
	}
	if detailValue.Type != ojson.String {
		return "", false
	}

	normalizedDetail := strings.ToLower(strings.TrimSpace(detailValue.String()))
	switch normalizedDetail {
	case "auto", "low", "high":
		return normalizedDetail, true
	case "original":
		// Chat Completions does not support Codex's original detail value.
		return "high", true
	default:
		return "", true
	}
}

func chatToolOutputFallbackPart(item ojson.Result) []byte {
	text := item.Raw
	if item.Type == ojson.String || text == "" {
		text = item.String()
	}
	part := []byte(`{"type":"text","text":""}`)
	part, _ = ojson.SetBytes(part, "text", text)
	return part
}

func collectOpenAIResponsesReasoningContent(item ojson.Result) string {
	var reasoningText strings.Builder
	if summary := item.Get("summary"); summary.Exists() && summary.IsArray() {
		summary.ForEach(func(_, summaryItem ojson.Result) bool {
			if summaryItem.Get("type").String() != "summary_text" {
				return true
			}
			reasoningText.WriteString(summaryItem.Get("text").String())
			return true
		})
	}
	if reasoningText.Len() == 0 {
		return "[reasoning unavailable]"
	}
	return reasoningText.String()
}

func combineOpenAIResponsesReasoning(existing, incoming string) string {
	existingTrimmed := strings.TrimSpace(existing)
	incomingTrimmed := strings.TrimSpace(incoming)

	switch {
	case existingTrimmed == "":
		return incoming
	case incomingTrimmed == "":
		return existing
	case existingTrimmed == "[reasoning unavailable]":
		return incoming
	case incomingTrimmed == "[reasoning unavailable]", existingTrimmed == incomingTrimmed:
		return existing
	default:
		return existing + "\n\n" + incoming
	}
}

func isUsableResponsesReasoning(reasoning string) bool {
	trimmed := strings.TrimSpace(reasoning)
	return trimmed != "" && trimmed != "[reasoning unavailable]"
}
