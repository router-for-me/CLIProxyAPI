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
	FormatChatCompletions OpenAIRequestFormat = iota // /v1/chat/completions - uses "messages"
	FormatResponsesAPI                               // /v1/responses - uses "input"
)

// ToOpenAIRequest converts unified request to OpenAI Chat Completions API JSON (default format).
func ToOpenAIRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	return ToOpenAIRequestFmt(req, FormatChatCompletions)
}

// ToOpenAIRequestFmt converts unified request to specified OpenAI API format.
// Use FormatChatCompletions for traditional /v1/chat/completions endpoint.
// Use FormatResponsesAPI for new /v1/responses endpoint (Codex CLI, etc.).
func ToOpenAIRequestFmt(req *ir.UnifiedChatRequest, format OpenAIRequestFormat) ([]byte, error) {
	if format == FormatResponsesAPI {
		return convertToResponsesAPIRequest(req)
	}
	return convertToChatCompletionsRequest(req)
}

// convertToChatCompletionsRequest builds JSON for /v1/chat/completions endpoint.
// This is the traditional OpenAI format used by most clients (Cursor, Cline, etc.).
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
		tools := make([]interface{}, len(req.Tools))
		for i, t := range req.Tools {
			params := t.Parameters
			if params == nil {
				params = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
			}
			tools[i] = map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name": t.Name, "description": t.Description, "parameters": params,
				},
			}
		}
		m["tools"] = tools
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
// This is the new OpenAI format used by Codex CLI and newer clients.
// Key differences: uses "input" instead of "messages", "max_output_tokens" instead of "max_tokens".
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

	if req.Thinking != nil && (req.Thinking.IncludeThoughts || req.Thinking.Effort != "" || req.Thinking.Summary != "") {
		reasoning := map[string]interface{}{}
		if req.Thinking.Effort != "" {
			reasoning["effort"] = req.Thinking.Effort
		} else if req.Thinking.IncludeThoughts {
			reasoning["effort"] = ir.MapBudgetToEffort(req.Thinking.Budget, "low")
		}
		if req.Thinking.Summary != "" {
			reasoning["summary"] = req.Thinking.Summary
		}
		if len(reasoning) > 0 {
			m["reasoning"] = reasoning
		}
	}

	if len(req.Tools) > 0 {
		tools := make([]interface{}, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = map[string]interface{}{
				"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Parameters,
			}
		}
		m["tools"] = tools
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
		prompt := map[string]interface{}{"id": req.PromptID}
		if req.PromptVersion != "" {
			prompt["version"] = req.PromptVersion
		}
		if len(req.PromptVariables) > 0 {
			prompt["variables"] = req.PromptVariables
		}
		m["prompt"] = prompt
	}
	if req.PromptCacheKey != "" {
		m["prompt_cache_key"] = req.PromptCacheKey
	}
	if req.Store != nil {
		m["store"] = *req.Store
	}

	return json.Marshal(m)
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
		thoughtsTokens := 0
		if meta != nil && meta.ThoughtsTokenCount > 0 {
			thoughtsTokens = meta.ThoughtsTokenCount
		} else if usage != nil && usage.ThoughtsTokenCount > 0 {
			thoughtsTokens = usage.ThoughtsTokenCount
		}
		if thoughtsTokens > 0 {
			usageMap["completion_tokens_details"] = map[string]interface{}{"reasoning_tokens": thoughtsTokens}
		}
		response["usage"] = usageMap
	}

	return json.Marshal(response)
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
			choice["delta"] = map[string]interface{}{
				"role": "assistant",
				"tool_calls": []interface{}{
					map[string]interface{}{
						"index": chunkIndex, "id": event.ToolCall.ID, "type": "function",
						"function": map[string]interface{}{"name": event.ToolCall.Name, "arguments": event.ToolCall.Args},
					},
				},
			}
		}
	case ir.EventTypeImage:
		if event.Image != nil {
			choice["delta"] = map[string]interface{}{
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
			usageMap := map[string]interface{}{
				"prompt_tokens": event.Usage.PromptTokens, "completion_tokens": event.Usage.CompletionTokens, "total_tokens": event.Usage.TotalTokens,
			}
			
			promptDetails := map[string]interface{}{}
			if event.Usage.CachedTokens > 0 {
				promptDetails["cached_tokens"] = event.Usage.CachedTokens
			}
			if event.Usage.AudioTokens > 0 {
				promptDetails["audio_tokens"] = event.Usage.AudioTokens
			}
			if len(promptDetails) > 0 {
				usageMap["prompt_tokens_details"] = promptDetails
			}

			completionDetails := map[string]interface{}{}
			thoughtsTokens := 0
			if meta != nil && meta.ThoughtsTokenCount > 0 {
				thoughtsTokens = meta.ThoughtsTokenCount
			} else if event.Usage.ThoughtsTokenCount > 0 {
				thoughtsTokens = event.Usage.ThoughtsTokenCount
			}
			if thoughtsTokens > 0 {
				completionDetails["reasoning_tokens"] = thoughtsTokens
			}
			if event.Usage.AcceptedPredictionTokens > 0 {
				completionDetails["accepted_prediction_tokens"] = event.Usage.AcceptedPredictionTokens
			}
			if event.Usage.RejectedPredictionTokens > 0 {
				completionDetails["rejected_prediction_tokens"] = event.Usage.RejectedPredictionTokens
			}
			if len(completionDetails) > 0 {
				usageMap["completion_tokens_details"] = completionDetails
			}
			
			chunk["usage"] = usageMap
		}
	case ir.EventTypeError:
		return nil, fmt.Errorf("stream error: %v", event.Error)
	default:
		return nil, nil
	}
	
	// Add logprobs to non-finish events if present (though usually only on finish or token)
	if event.Logprobs != nil && event.Type != ir.EventTypeFinish {
		choice["logprobs"] = event.Logprobs
	}

	chunk["choices"] = []interface{}{choice}
	jsonBytes, err := json.Marshal(chunk)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("data: %s\n\n", string(jsonBytes))), nil
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

	return json.Marshal(response)
}

// ResponsesStreamState holds state for Responses API streaming conversion.
// Responses API streaming uses semantic events (response.output_text.delta, etc.)
// and requires tracking state across multiple events to build proper "done" events.
type ResponsesStreamState struct {
	Seq             int            // Sequence number for events (required by Responses API)
	ResponseID      string         // Response ID (generated once, reused for all events)
	Created         int64          // Creation timestamp
	Started         bool           // Whether initial events (response.created, response.in_progress) were sent
	ReasoningID     string         // ID for reasoning output item (if any)
	MsgID           string         // ID for message output item (if any)
	TextBuffer      string         // Accumulated text content (needed for "done" event)
	ReasoningBuffer string         // Accumulated reasoning content
	FuncCallIDs     map[int]string // Tool call IDs by index
	FuncNames       map[int]string // Tool call names by index
	FuncArgsBuffer  map[int]string // Accumulated tool call arguments by index
}

// NewResponsesStreamState creates a new streaming state for Responses API.
func NewResponsesStreamState() *ResponsesStreamState {
	return &ResponsesStreamState{
		FuncCallIDs:    make(map[int]string),
		FuncNames:      make(map[int]string),
		FuncArgsBuffer: make(map[int]string),
	}
}

// ToResponsesAPIChunk converts event to Responses API SSE streaming chunks.
// Returns multiple SSE strings because Responses API requires semantic events
// (e.g., first token requires output_item.added + content_part.added + delta events).
func ToResponsesAPIChunk(event ir.UnifiedEvent, model string, state *ResponsesStreamState) ([]string, error) {
	if state.ResponseID == "" {
		state.ResponseID = fmt.Sprintf("resp_%d", time.Now().UnixNano())
		state.Created = time.Now().Unix()
	}

	nextSeq := func() int { state.Seq++; return state.Seq }
	var out []string

	if !state.Started {
		for _, t := range []string{"response.created", "response.in_progress"} {
			b, _ := json.Marshal(map[string]interface{}{
				"type": t, "sequence_number": nextSeq(),
				"response": map[string]interface{}{
					"id": state.ResponseID, "object": "response", "created_at": state.Created, "status": "in_progress",
				},
			})
			out = append(out, fmt.Sprintf("event: %s\ndata: %s\n\n", t, string(b)))
		}
		state.Started = true
	}

	switch event.Type {
	case ir.EventTypeToken:
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

	case ir.EventTypeReasoning, ir.EventTypeReasoningSummary:
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

	case ir.EventTypeToolCall:
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
		// For complete tool call, we might not get deltas, so we can just emit done if needed,
		// but usually we get deltas or the full args. If we get full args here:
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

	case ir.EventTypeToolCallDelta:
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

	case ir.EventTypeFinish:
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

		usageMap := map[string]interface{}{}
		if event.Usage != nil {
			usageMap = map[string]interface{}{
				"input_tokens": event.Usage.PromptTokens, "output_tokens": event.Usage.CompletionTokens, "total_tokens": event.Usage.TotalTokens,
			}
			if event.Usage.CachedTokens > 0 {
				usageMap["input_tokens_details"] = map[string]interface{}{"cached_tokens": event.Usage.CachedTokens}
			}
			if event.Usage.ThoughtsTokenCount > 0 {
				usageMap["output_tokens_details"] = map[string]interface{}{"reasoning_tokens": event.Usage.ThoughtsTokenCount}
			}
		}

		b, _ := json.Marshal(map[string]interface{}{
			"type": "response.done", "sequence_number": nextSeq(),
			"response": map[string]interface{}{
				"id": state.ResponseID, "object": "response", "created_at": state.Created, "status": "completed",
				"usage": usageMap,
			},
		})
		out = append(out, fmt.Sprintf("event: response.done\ndata: %s\n\n", string(b)))
	}

	return out, nil
}
