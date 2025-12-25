// Package from_ir converts unified request format to provider-specific formats.
// This file handles conversion TO OpenAI API formats (both Chat Completions and Responses API).
package from_ir

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/ir"
)

// OpenAIRequestFormat specifies which OpenAI API format to generate.
type OpenAIRequestFormat int

const (
	FormatChatCompletions OpenAIRequestFormat = iota // /v1/chat/completions
	FormatResponsesAPI                               // /v1/responses
)

// ToOpenAIRequest converts unified request to OpenAI Chat Completions API JSON (default format).
func ToOpenAIRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	return ToOpenAIRequestFmt(req, FormatChatCompletions)
}

// ToOpenAIRequestFmt converts unified request to specified OpenAI API format.
func ToOpenAIRequestFmt(req *ir.UnifiedChatRequest, format OpenAIRequestFormat) ([]byte, error) {
	if format == FormatResponsesAPI {
		return convertToResponsesAPIRequest(req)
	}
	return convertToChatCompletionsRequest(req)
}

// convertToChatCompletionsRequest builds JSON for /v1/chat/completions endpoint.
func convertToChatCompletionsRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	m := map[string]interface{}{
		"model":    req.Model,
		"messages": []interface{}{},
	}
	if req.Temperature != nil {
		m["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		m["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		m["max_tokens"] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 {
		m["stop"] = req.StopSequences
	}
	if req.Thinking != nil && req.Thinking.IncludeThoughts {
		m["reasoning_effort"] = ir.MapBudgetToEffort(req.Thinking.Budget, "auto")
	}

	var messages []interface{}
	for _, msg := range req.Messages {
		if msgObj := convertMessageToOpenAI(msg); msgObj != nil {
			messages = append(messages, msgObj)
		}
	}
	m["messages"] = messages

	if len(req.Tools) > 0 {
		m["tools"] = buildOpenAITools(req.Tools)
	}

	if req.ToolChoice != "" {
		m["tool_choice"] = req.ToolChoice
	}
	if req.ParallelToolCalls != nil {
		m["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if len(req.ResponseModality) > 0 {
		m["modalities"] = req.ResponseModality
	}

	return json.Marshal(m)
}

// convertToResponsesAPIRequest builds JSON for /v1/responses endpoint.
func convertToResponsesAPIRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	m := map[string]interface{}{"model": req.Model}
	if req.Temperature != nil {
		m["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		m["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		m["max_output_tokens"] = *req.MaxTokens
	}
	if req.Instructions != "" {
		m["instructions"] = req.Instructions
	}

	var input []interface{}
	for _, msg := range req.Messages {
		if msg.Role == ir.RoleSystem && req.Instructions != "" {
			continue
		}
		if item := convertMessageToResponsesInput(msg); item != nil {
			input = append(input, item)
		}
	}
	if len(input) > 0 {
		m["input"] = input
	}

	if req.Thinking != nil {
		applyResponsesThinking(m, req.Thinking)
	}

	if len(req.Tools) > 0 {
		m["tools"] = buildResponsesTools(req.Tools)
	}

	if req.ToolChoice != "" {
		m["tool_choice"] = req.ToolChoice
	}
	if req.ParallelToolCalls != nil {
		m["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if req.PreviousResponseID != "" {
		m["previous_response_id"] = req.PreviousResponseID
	}
	if req.PromptID != "" {
		applyPromptConfig(m, req)
	}
	if req.PromptCacheKey != "" {
		m["prompt_cache_key"] = req.PromptCacheKey
	}
	if req.Store != nil {
		m["store"] = *req.Store
	}

	return json.Marshal(m)
}

func buildOpenAITools(tools []ir.ToolDefinition) []interface{} {
	res := make([]interface{}, len(tools))
	for i, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		res[i] = map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  params,
			},
		}
	}
	return res
}

func buildResponsesTools(tools []ir.ToolDefinition) []interface{} {
	res := make([]interface{}, len(tools))
	for i, t := range tools {
		res[i] = map[string]interface{}{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.Parameters,
		}
	}
	return res
}

func applyResponsesThinking(m map[string]interface{}, thinking *ir.ThinkingConfig) {
	if !thinking.IncludeThoughts && thinking.Effort == "" && thinking.Summary == "" {
		return
	}
	reasoning := map[string]interface{}{}
	if thinking.Effort != "" {
		reasoning["effort"] = thinking.Effort
	} else if thinking.IncludeThoughts {
		reasoning["effort"] = ir.MapBudgetToEffort(thinking.Budget, "low")
	}
	if thinking.Summary != "" {
		reasoning["summary"] = thinking.Summary
	}
	if len(reasoning) > 0 {
		m["reasoning"] = reasoning
	}
}

func applyPromptConfig(m map[string]interface{}, req *ir.UnifiedChatRequest) {
	prompt := map[string]interface{}{"id": req.PromptID}
	if req.PromptVersion != "" {
		prompt["version"] = req.PromptVersion
	}
	if len(req.PromptVariables) > 0 {
		prompt["variables"] = req.PromptVariables
	}
	m["prompt"] = prompt
}

func convertMessageToResponsesInput(msg ir.Message) interface{} {
	switch msg.Role {
	case ir.RoleSystem:
		if text := ir.CombineTextParts(msg); text != "" {
			return map[string]interface{}{
				"type": "message", "role": "system",
				"content": []interface{}{map[string]interface{}{"type": "input_text", "text": text}},
			}
		}
	case ir.RoleUser:
		return buildResponsesUserMessage(msg)
	case ir.RoleAssistant:
		if len(msg.ToolCalls) > 0 {
			tc := msg.ToolCalls[0]
			return map[string]interface{}{
				"type": "function_call", "call_id": tc.ID, "name": tc.Name, "arguments": tc.Args,
			}
		}
		if text := ir.CombineTextParts(msg); text != "" {
			return map[string]interface{}{
				"type": "message", "role": "assistant",
				"content": []interface{}{map[string]interface{}{"type": "output_text", "text": text}},
			}
		}
	case ir.RoleTool:
		for _, part := range msg.Content {
			if part.Type == ir.ContentTypeToolResult && part.ToolResult != nil {
				return map[string]interface{}{
					"type": "function_call_output", "call_id": part.ToolResult.ToolCallID, "output": part.ToolResult.Result,
				}
			}
		}
	}
	return nil
}

func buildResponsesUserMessage(msg ir.Message) interface{} {
	var content []interface{}
	for _, part := range msg.Content {
		switch part.Type {
		case ir.ContentTypeText:
			if part.Text != "" {
				content = append(content, map[string]interface{}{"type": "input_text", "text": part.Text})
			}
		case ir.ContentTypeImage:
			if part.Image != nil {
				if part.Image.URL != "" {
					content = append(content, map[string]interface{}{"type": "input_image", "image_url": part.Image.URL})
				} else if part.Image.Data != "" {
					content = append(content, map[string]interface{}{
						"type": "input_image", "image_url": fmt.Sprintf("data:%s;base64,%s", part.Image.MimeType, part.Image.Data),
					})
				}
			}
		case ir.ContentTypeFile:
			if part.File != nil {
				fileItem := map[string]interface{}{"type": "input_file"}
				if part.File.FileID != "" {
					fileItem["file_id"] = part.File.FileID
				}
				if part.File.FileURL != "" {
					fileItem["file_url"] = part.File.FileURL
				}
				if part.File.Filename != "" {
					fileItem["filename"] = part.File.Filename
				}
				if part.File.FileData != "" {
					fileItem["file_data"] = part.File.FileData
				}
				content = append(content, fileItem)
			}
		}
	}
	if len(content) == 0 {
		return nil
	}
	return map[string]interface{}{"type": "message", "role": "user", "content": content}
}

// ToOpenAIChatCompletion converts messages to OpenAI chat completion response.
func ToOpenAIChatCompletion(messages []ir.Message, usage *ir.Usage, model, messageID string) ([]byte, error) {
	return ToOpenAIChatCompletionMeta(messages, usage, model, messageID, nil)
}

func ToOpenAIChatCompletionMeta(messages []ir.Message, usage *ir.Usage, model, messageID string, meta *ir.OpenAIMeta) ([]byte, error) {
	builder := ir.NewResponseBuilder(messages, usage, model)
	responseID, created := messageID, time.Now().Unix()
	if meta != nil {
		if meta.ResponseID != "" {
			responseID = meta.ResponseID
		}
		if meta.CreateTime > 0 {
			created = meta.CreateTime
		}
	}

	response := map[string]interface{}{
		"id": responseID, "object": "chat.completion", "created": created, "model": model, "choices": []interface{}{},
	}

	if msg := builder.GetLastMessage(); msg != nil {
		msgContent := map[string]interface{}{"role": string(msg.Role)}
		if text := builder.GetTextContent(); text != "" {
			msgContent["content"] = text
		}
		if reasoning := builder.GetReasoningContent(); reasoning != "" {
			ir.AddReasoningToMessage(msgContent, reasoning, "")
		}
		if tcs := builder.BuildOpenAIToolCalls(); tcs != nil {
			msgContent["tool_calls"] = tcs
		}

		choiceObj := map[string]interface{}{
			"index": 0, "finish_reason": builder.DetermineFinishReason(), "message": msgContent,
		}
		if meta != nil && meta.NativeFinishReason != "" {
			choiceObj["native_finish_reason"] = meta.NativeFinishReason
		}
		response["choices"] = []interface{}{choiceObj}
	}

	if usageMap := builder.BuildUsageMap(); usageMap != nil {
		addUsageDetails(usageMap, usage, meta)
		response["usage"] = usageMap
	}

	return json.Marshal(response)
}

func addUsageDetails(usageMap map[string]interface{}, usage *ir.Usage, meta *ir.OpenAIMeta) {
	thoughtsTokens := 0
	if meta != nil && meta.ThoughtsTokenCount > 0 {
		thoughtsTokens = meta.ThoughtsTokenCount
	} else if usage != nil && usage.ThoughtsTokenCount > 0 {
		thoughtsTokens = usage.ThoughtsTokenCount
	}
	if thoughtsTokens > 0 {
		usageMap["completion_tokens_details"] = map[string]interface{}{"reasoning_tokens": thoughtsTokens}
	}
}

// ToOpenAIChunk converts event to OpenAI SSE streaming chunk.
func ToOpenAIChunk(event ir.UnifiedEvent, model, messageID string, chunkIndex int) ([]byte, error) {
	return ToOpenAIChunkMeta(event, model, messageID, chunkIndex, nil)
}

func ToOpenAIChunkMeta(event ir.UnifiedEvent, model, messageID string, chunkIndex int, meta *ir.OpenAIMeta) ([]byte, error) {
	responseID, created := messageID, time.Now().Unix()
	if meta != nil {
		if meta.ResponseID != "" {
			responseID = meta.ResponseID
		}
		if meta.CreateTime > 0 {
			created = meta.CreateTime
		}
	}

	chunk := map[string]interface{}{
		"id": responseID, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []interface{}{},
	}
	if event.SystemFingerprint != "" {
		chunk["system_fingerprint"] = event.SystemFingerprint
	}

	choice := map[string]interface{}{"index": 0, "delta": map[string]interface{}{}}

	switch event.Type {
	case ir.EventTypeToken:
		delta := map[string]interface{}{"role": "assistant"}
		if event.Content != "" {
			delta["content"] = event.Content
		}
		if event.Refusal != "" {
			delta["refusal"] = event.Refusal
		}
		choice["delta"] = delta
	case ir.EventTypeReasoning:
		choice["delta"] = ir.BuildReasoningDelta(event.Reasoning, event.ThoughtSignature)
	case ir.EventTypeToolCall:
		if event.ToolCall != nil {
			choice["delta"] = buildToolCallDelta(event)
		}
	case ir.EventTypeImage:
		if event.Image != nil {
			choice["delta"] = buildImageDelta(event)
		}
	case ir.EventTypeFinish:
		choice["finish_reason"] = ir.MapFinishReasonToOpenAI(event.FinishReason)
		if meta != nil && meta.NativeFinishReason != "" {
			choice["native_finish_reason"] = meta.NativeFinishReason
		}
		if event.Logprobs != nil {
			choice["logprobs"] = event.Logprobs
		}
		if event.ContentFilter != nil {
			choice["content_filter_results"] = event.ContentFilter
		}
		if event.Usage != nil {
			chunk["usage"] = buildChunkUsage(event.Usage, meta)
		}
	case ir.EventTypeError:
		return nil, fmt.Errorf("stream error: %v", event.Error)
	default:
		return nil, nil
	}

	if event.Logprobs != nil && event.Type != ir.EventTypeFinish {
		choice["logprobs"] = event.Logprobs
	}

	chunk["choices"] = []interface{}{choice}
	return json.Marshal(chunk)
}

func buildToolCallDelta(event ir.UnifiedEvent) map[string]interface{} {
	tcChunk := map[string]interface{}{"index": event.ToolCallIndex}
	if event.ToolCall.ID != "" {
		tcChunk["id"] = event.ToolCall.ID
		tcChunk["type"] = "function"
	}
	funcChunk := map[string]interface{}{}
	if event.ToolCall.Name != "" {
		funcChunk["name"] = event.ToolCall.Name
	}
	funcChunk["arguments"] = event.ToolCall.Args
	tcChunk["function"] = funcChunk
	return map[string]interface{}{"tool_calls": []interface{}{tcChunk}}
}

func buildImageDelta(event ir.UnifiedEvent) map[string]interface{} {
	return map[string]interface{}{
		"role": "assistant",
		"images": []interface{}{
			map[string]interface{}{
				"type": "image_url",
				"image_url": map[string]string{
					"url": fmt.Sprintf("data:%s;base64,%s", event.Image.MimeType, event.Image.Data),
				},
			},
		},
	}
}

func buildChunkUsage(usage *ir.Usage, meta *ir.OpenAIMeta) map[string]interface{} {
	usageMap := map[string]interface{}{
		"prompt_tokens": usage.PromptTokens, "completion_tokens": usage.CompletionTokens, "total_tokens": usage.TotalTokens,
	}

	promptDetails := map[string]interface{}{}
	if usage.CachedTokens > 0 {
		promptDetails["cached_tokens"] = usage.CachedTokens
	}
	if usage.AudioTokens > 0 {
		promptDetails["audio_tokens"] = usage.AudioTokens
	}
	if len(promptDetails) > 0 {
		usageMap["prompt_tokens_details"] = promptDetails
	}

	completionDetails := map[string]interface{}{}
	thoughtsTokens := 0
	if meta != nil && meta.ThoughtsTokenCount > 0 {
		thoughtsTokens = meta.ThoughtsTokenCount
	} else if usage.ThoughtsTokenCount > 0 {
		thoughtsTokens = usage.ThoughtsTokenCount
	}
	if thoughtsTokens > 0 {
		completionDetails["reasoning_tokens"] = thoughtsTokens
	}
	if usage.AcceptedPredictionTokens > 0 {
		completionDetails["accepted_prediction_tokens"] = usage.AcceptedPredictionTokens
	}
	if usage.RejectedPredictionTokens > 0 {
		completionDetails["rejected_prediction_tokens"] = usage.RejectedPredictionTokens
	}
	if len(completionDetails) > 0 {
		usageMap["completion_tokens_details"] = completionDetails
	}

	return usageMap
}

func convertMessageToOpenAI(msg ir.Message) map[string]interface{} {
	switch msg.Role {
	case ir.RoleSystem:
		if text := ir.CombineTextParts(msg); text != "" {
			return map[string]interface{}{"role": "system", "content": text}
		}
	case ir.RoleUser:
		return buildOpenAIUserMessage(msg)
	case ir.RoleAssistant:
		return buildOpenAIAssistantMessage(msg)
	case ir.RoleTool:
		return buildOpenAIToolMessage(msg)
	}
	return nil
}

func buildOpenAIUserMessage(msg ir.Message) map[string]interface{} {
	var parts []interface{}
	for _, part := range msg.Content {
		switch part.Type {
		case ir.ContentTypeText:
			if part.Text != "" {
				parts = append(parts, map[string]interface{}{"type": "text", "text": part.Text})
			}
		case ir.ContentTypeImage:
			if part.Image != nil {
				parts = append(parts, map[string]interface{}{
					"type":      "image_url",
					"image_url": map[string]string{"url": fmt.Sprintf("data:%s;base64,%s", part.Image.MimeType, part.Image.Data)},
				})
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 {
		if tp, ok := parts[0].(map[string]interface{}); ok && tp["type"] == "text" {
			return map[string]interface{}{"role": "user", "content": tp["text"]}
		}
	}
	return map[string]interface{}{"role": "user", "content": parts}
}

func buildOpenAIAssistantMessage(msg ir.Message) map[string]interface{} {
	result := map[string]interface{}{"role": "assistant"}
	if text := ir.CombineTextParts(msg); text != "" {
		result["content"] = text
	}
	if reasoning := ir.CombineReasoningParts(msg); reasoning != "" {
		ir.AddReasoningToMessage(result, reasoning, ir.GetFirstReasoningSignature(msg))
	}
	if len(msg.ToolCalls) > 0 {
		tcs := make([]interface{}, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			tcs[i] = map[string]interface{}{
				"id": tc.ID, "type": "function",
				"function": map[string]interface{}{"name": tc.Name, "arguments": tc.Args},
			}
		}
		result["tool_calls"] = tcs
	}
	return result
}

func buildOpenAIToolMessage(msg ir.Message) map[string]interface{} {
	for _, part := range msg.Content {
		if part.Type == ir.ContentTypeToolResult && part.ToolResult != nil {
			return map[string]interface{}{
				"role": "tool", "tool_call_id": part.ToolResult.ToolCallID, "content": part.ToolResult.Result,
			}
		}
	}
	return nil
}

// ToResponsesAPIResponse converts messages to Responses API non-streaming response.
func ToResponsesAPIResponse(messages []ir.Message, usage *ir.Usage, model string, meta *ir.OpenAIMeta) ([]byte, error) {
	responseID, created := fmt.Sprintf("resp_%d", time.Now().UnixNano()), time.Now().Unix()
	if meta != nil {
		if meta.ResponseID != "" {
			responseID = meta.ResponseID
		}
		if meta.CreateTime > 0 {
			created = meta.CreateTime
		}
	}

	response := map[string]interface{}{
		"id": responseID, "object": "response", "created_at": created, "status": "completed", "model": model,
	}

	var output []interface{}
	var outputText string
	builder := ir.NewResponseBuilder(messages, usage, model)

	for _, msg := range messages {
		if msg.Role != ir.RoleAssistant {
			continue
		}
		if reasoning := ir.CombineReasoningParts(msg); reasoning != "" {
			output = append(output, map[string]interface{}{
				"id": fmt.Sprintf("rs_%s", responseID), "type": "reasoning",
				"summary": []interface{}{map[string]interface{}{"type": "summary_text", "text": reasoning}},
			})
		}
		if text := ir.CombineTextParts(msg); text != "" {
			outputText = text
			output = append(output, map[string]interface{}{
				"id": fmt.Sprintf("msg_%s", responseID), "type": "message", "status": "completed", "role": "assistant",
				"content": []interface{}{map[string]interface{}{"type": "output_text", "text": text, "annotations": []interface{}{}}},
			})
		}
		for _, tc := range msg.ToolCalls {
			output = append(output, map[string]interface{}{
				"id": fmt.Sprintf("fc_%s", tc.ID), "type": "function_call", "status": "completed",
				"call_id": tc.ID, "name": tc.Name, "arguments": tc.Args,
			})
		}
	}

	if len(output) > 0 {
		response["output"] = output
	}
	if outputText != "" {
		response["output_text"] = outputText
	}

	if usageMap := builder.BuildUsageMap(); usageMap != nil {
		addResponsesUsage(response, usageMap, usage, meta)
	}

	return json.Marshal(response)
}

func addResponsesUsage(response map[string]interface{}, usageMap map[string]interface{}, usage *ir.Usage, meta *ir.OpenAIMeta) {
	responsesUsage := map[string]interface{}{
		"input_tokens": usageMap["prompt_tokens"], "output_tokens": usageMap["completion_tokens"], "total_tokens": usageMap["total_tokens"],
	}
	if usage != nil && usage.CachedTokens > 0 {
		responsesUsage["input_tokens_details"] = map[string]interface{}{"cached_tokens": usage.CachedTokens}
	}
	thoughtsTokens := 0
	if meta != nil && meta.ThoughtsTokenCount > 0 {
		thoughtsTokens = meta.ThoughtsTokenCount
	} else if usage != nil && usage.ThoughtsTokenCount > 0 {
		thoughtsTokens = usage.ThoughtsTokenCount
	}
	if thoughtsTokens > 0 {
		responsesUsage["output_tokens_details"] = map[string]interface{}{"reasoning_tokens": thoughtsTokens}
	}
	response["usage"] = responsesUsage
}

// ResponsesStreamState holds state for Responses API streaming conversion.
type ResponsesStreamState struct {
	Seq             int
	ResponseID      string
	Created         int64
	Started         bool
	ReasoningID     string
	MsgID           string
	TextBuffer      string
	ReasoningBuffer string
	FuncCallIDs     map[int]string
	FuncNames       map[int]string
	FuncArgsBuffer  map[int]string
}

func NewResponsesStreamState() *ResponsesStreamState {
	return &ResponsesStreamState{
		FuncCallIDs:    make(map[int]string),
		FuncNames:      make(map[int]string),
		FuncArgsBuffer: make(map[int]string),
	}
}

// ToResponsesAPIChunk converts event to Responses API SSE streaming chunks.
func ToResponsesAPIChunk(event ir.UnifiedEvent, model string, state *ResponsesStreamState) ([]string, error) {
	if state.ResponseID == "" {
		state.ResponseID = fmt.Sprintf("resp_%d", time.Now().UnixNano())
		state.Created = time.Now().Unix()
	}

	nextSeq := func() int { state.Seq++; return state.Seq }
	var out []string

	if !state.Started {
		out = append(out, buildResponsesStartEvents(state, nextSeq)...)
		state.Started = true
	}

	switch event.Type {
	case ir.EventTypeToken:
		out = append(out, handleTokenEvent(event, state, nextSeq)...)
	case ir.EventTypeReasoning, ir.EventTypeReasoningSummary:
		out = append(out, handleReasoningEvent(event, state, nextSeq)...)
	case ir.EventTypeToolCall:
		out = append(out, handleToolCallEvent(event, state, nextSeq)...)
	case ir.EventTypeToolCallDelta:
		out = append(out, handleToolCallDeltaEvent(event, state, nextSeq)...)
	case ir.EventTypeFinish:
		out = append(out, handleFinishEvent(event, state, nextSeq)...)
	}

	return out, nil
}

func buildResponsesStartEvents(state *ResponsesStreamState, nextSeq func() int) []string {
	var out []string
	for _, t := range []string{"response.created", "response.in_progress"} {
		b, _ := json.Marshal(map[string]interface{}{
			"type": t, "sequence_number": nextSeq(),
			"response": map[string]interface{}{
				"id": state.ResponseID, "object": "response", "created_at": state.Created, "status": "in_progress",
				"output": []interface{}{},
			},
		})
		out = append(out, fmt.Sprintf("event: %s\ndata: %s\n\n", t, string(b)))
	}
	return out
}

func handleTokenEvent(event ir.UnifiedEvent, state *ResponsesStreamState, nextSeq func() int) []string {
	var out []string
	if state.MsgID == "" {
		state.MsgID = fmt.Sprintf("msg_%s", state.ResponseID)
		b1, _ := json.Marshal(map[string]interface{}{
			"type": "response.output_item.added", "sequence_number": nextSeq(), "output_index": 0,
			"item": map[string]interface{}{"id": state.MsgID, "type": "message", "status": "in_progress", "role": "assistant", "content": []interface{}{}},
		})
		out = append(out, fmt.Sprintf("event: response.output_item.added\ndata: %s\n\n", string(b1)))
		b2, _ := json.Marshal(map[string]interface{}{
			"type": "response.content_part.added", "sequence_number": nextSeq(), "item_id": state.MsgID,
			"output_index": 0, "content_index": 0, "part": map[string]interface{}{"type": "output_text", "text": ""},
		})
		out = append(out, fmt.Sprintf("event: response.content_part.added\ndata: %s\n\n", string(b2)))
	}
	state.TextBuffer += event.Content
	b, _ := json.Marshal(map[string]interface{}{
		"type": "response.output_text.delta", "sequence_number": nextSeq(), "item_id": state.MsgID,
		"output_index": 0, "content_index": 0, "delta": event.Content,
	})
	out = append(out, fmt.Sprintf("event: response.output_text.delta\ndata: %s\n\n", string(b)))
	return out
}

func handleReasoningEvent(event ir.UnifiedEvent, state *ResponsesStreamState, nextSeq func() int) []string {
	var out []string
	text := event.Reasoning
	if event.Type == ir.EventTypeReasoningSummary {
		text = event.ReasoningSummary
	}
	if state.ReasoningID == "" {
		state.ReasoningID = fmt.Sprintf("rs_%s", state.ResponseID)
		b, _ := json.Marshal(map[string]interface{}{
			"type": "response.output_item.added", "sequence_number": nextSeq(), "output_index": 0,
			"item": map[string]interface{}{"id": state.ReasoningID, "type": "reasoning", "status": "in_progress", "summary": []interface{}{}},
		})
		out = append(out, fmt.Sprintf("event: response.output_item.added\ndata: %s\n\n", string(b)))
	}
	state.ReasoningBuffer += text
	b, _ := json.Marshal(map[string]interface{}{
		"type": "response.reasoning_summary_text.delta", "sequence_number": nextSeq(), "item_id": state.ReasoningID,
		"output_index": 0, "content_index": 0, "delta": text,
	})
	out = append(out, fmt.Sprintf("event: response.reasoning_summary_text.delta\ndata: %s\n\n", string(b)))
	return out
}

func handleToolCallEvent(event ir.UnifiedEvent, state *ResponsesStreamState, nextSeq func() int) []string {
	var out []string
	idx := event.ToolCallIndex
	if _, exists := state.FuncCallIDs[idx]; !exists {
		state.FuncCallIDs[idx] = fmt.Sprintf("fc_%s", event.ToolCall.ID)
		state.FuncNames[idx] = event.ToolCall.Name
		b, _ := json.Marshal(map[string]interface{}{
			"type": "response.output_item.added", "sequence_number": nextSeq(), "output_index": idx,
			"item": map[string]interface{}{
				"id": state.FuncCallIDs[idx], "type": "function_call", "status": "in_progress",
				"call_id": event.ToolCall.ID, "name": event.ToolCall.Name, "arguments": "",
			},
		})
		out = append(out, fmt.Sprintf("event: response.output_item.added\ndata: %s\n\n", string(b)))
	}
	if event.ToolCall.Args != "" {
		b, _ := json.Marshal(map[string]interface{}{
			"type": "response.function_call_arguments.delta", "sequence_number": nextSeq(), "item_id": state.FuncCallIDs[idx],
			"output_index": idx, "delta": event.ToolCall.Args,
		})
		out = append(out, fmt.Sprintf("event: response.function_call_arguments.delta\ndata: %s\n\n", string(b)))
	}
	b, _ := json.Marshal(map[string]interface{}{
		"type": "response.output_item.done", "sequence_number": nextSeq(), "item_id": state.FuncCallIDs[idx],
		"output_index": idx, "item": map[string]interface{}{
			"id": state.FuncCallIDs[idx], "type": "function_call", "status": "completed",
			"call_id": event.ToolCall.ID, "name": event.ToolCall.Name, "arguments": event.ToolCall.Args,
		},
	})
	out = append(out, fmt.Sprintf("event: response.output_item.done\ndata: %s\n\n", string(b)))
	return out
}

func handleToolCallDeltaEvent(event ir.UnifiedEvent, state *ResponsesStreamState, nextSeq func() int) []string {
	var out []string
	idx := event.ToolCallIndex
	if _, exists := state.FuncCallIDs[idx]; !exists {
		state.FuncCallIDs[idx] = fmt.Sprintf("fc_%s", event.ToolCall.ID)
		b, _ := json.Marshal(map[string]interface{}{
			"type": "response.output_item.added", "sequence_number": nextSeq(), "output_index": idx,
			"item": map[string]interface{}{
				"id": state.FuncCallIDs[idx], "type": "function_call", "status": "in_progress",
				"call_id": event.ToolCall.ID, "name": "", "arguments": "",
			},
		})
		out = append(out, fmt.Sprintf("event: response.output_item.added\ndata: %s\n\n", string(b)))
	}
	state.FuncArgsBuffer[idx] += event.ToolCall.Args
	b, _ := json.Marshal(map[string]interface{}{
		"type": "response.function_call_arguments.delta", "sequence_number": nextSeq(), "item_id": state.FuncCallIDs[idx],
		"output_index": idx, "delta": event.ToolCall.Args,
	})
	out = append(out, fmt.Sprintf("event: response.function_call_arguments.delta\ndata: %s\n\n", string(b)))
	return out
}

func handleFinishEvent(event ir.UnifiedEvent, state *ResponsesStreamState, nextSeq func() int) []string {
	var out []string
	if state.MsgID != "" {
		b1, _ := json.Marshal(map[string]interface{}{
			"type": "response.content_part.done", "sequence_number": nextSeq(), "item_id": state.MsgID,
			"output_index": 0, "content_index": 0, "part": map[string]interface{}{"type": "output_text", "text": state.TextBuffer},
		})
		out = append(out, fmt.Sprintf("event: response.content_part.done\ndata: %s\n\n", string(b1)))
		b2, _ := json.Marshal(map[string]interface{}{
			"type": "response.output_item.done", "sequence_number": nextSeq(), "output_index": 0,
			"item": map[string]interface{}{
				"id": state.MsgID, "type": "message", "status": "completed", "role": "assistant",
				"content": []interface{}{map[string]interface{}{"type": "output_text", "text": state.TextBuffer}},
			},
		})
		out = append(out, fmt.Sprintf("event: response.output_item.done\ndata: %s\n\n", string(b2)))
	}
	if state.ReasoningID != "" {
		b, _ := json.Marshal(map[string]interface{}{
			"type": "response.output_item.done", "sequence_number": nextSeq(), "output_index": 0,
			"item": map[string]interface{}{
				"id": state.ReasoningID, "type": "reasoning", "status": "completed",
				"summary": []interface{}{map[string]interface{}{"type": "summary_text", "text": state.ReasoningBuffer}},
			},
		})
		out = append(out, fmt.Sprintf("event: response.output_item.done\ndata: %s\n\n", string(b)))
	}

	usageMap := buildUsageMapForResponses(event.Usage)
	b, _ := json.Marshal(map[string]interface{}{
		"type": "response.done", "sequence_number": nextSeq(),
		"response": map[string]interface{}{
			"id": state.ResponseID, "object": "response", "created_at": state.Created, "status": "completed",
			"usage": usageMap,
		},
	})
	out = append(out, fmt.Sprintf("event: response.done\ndata: %s\n\n", string(b)))
	return out
}

func buildUsageMapForResponses(usage *ir.Usage) map[string]interface{} {
	usageMap := map[string]interface{}{}
	if usage != nil {
		usageMap = map[string]interface{}{
			"input_tokens": usage.PromptTokens, "output_tokens": usage.CompletionTokens, "total_tokens": usage.TotalTokens,
		}
		if usage.CachedTokens > 0 {
			usageMap["input_tokens_details"] = map[string]interface{}{"cached_tokens": usage.CachedTokens}
		}
		if usage.ThoughtsTokenCount > 0 {
			usageMap["output_tokens_details"] = map[string]interface{}{"reasoning_tokens": usage.ThoughtsTokenCount}
		}
	}
	return usageMap
}
