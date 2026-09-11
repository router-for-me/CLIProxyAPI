package executor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

type traeSSEEvent struct {
	Name string
	Data []byte
}

type traeMetadataEvent struct {
	Model     string `json:"model"`
	SessionID string `json:"session_id"`
}

type traeOutputEvent struct {
	Response          string          `json:"response"`
	ReasoningContent  string          `json:"reasoning_content"`
	ToolCalls         []traeToolCall  `json:"tool_calls"`
	MultimodalContent json.RawMessage `json:"multimodal_contents"`
}

type traeToolCall struct {
	Index        int    `json:"index"`
	ID           string `json:"id"`
	Type         string `json:"type"`
	FunctionCall struct {
		Name             string          `json:"name"`
		Arguments        string          `json:"arguments"`
		PartialArguments json.RawMessage `json:"partial_arguments"`
		Namespace        string          `json:"namespace"`
	} `json:"function_call"`
}

type traeTokenUsage struct {
	PromptTokens                  int64 `json:"prompt_tokens"`
	CompletionTokens              int64 `json:"completion_tokens"`
	TotalTokens                   int64 `json:"total_tokens"`
	CacheCreationInputTokens      int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens          int64 `json:"cache_read_input_tokens"`
	ReasoningTokens               int64 `json:"reasoning_tokens"`
	PromptTokensTotal             int64 `json:"prompt_tokens_total"`
	CompletionTokensTotal         int64 `json:"completion_tokens_total"`
	TotalTokensTotal              int64 `json:"total_tokens_total"`
	CacheCreationInputTokensTotal int64 `json:"cache_creation_input_tokens_total"`
	CacheReadInputTokensTotal     int64 `json:"cache_read_input_tokens_total"`
	ReasoningTokensTotal          int64 `json:"reasoning_tokens_total"`
}

type traeDoneEvent struct {
	FinishReason string `json:"finish_reason"`
}

type traeStreamToolState struct {
	ID   string
	Type string
	Name string
}

type traeStreamState struct {
	ID           string
	Model        string
	Created      int64
	EmittedRole  bool
	Finished     bool
	FinishReason string
	Tools        map[int]traeStreamToolState
}

func newTraeStreamState(model string) *traeStreamState {
	return &traeStreamState{
		ID:      "chatcmpl-" + strings.ReplaceAll(newTraeUUID(), "-", ""),
		Model:   model,
		Created: time.Now().Unix(),
		Tools:   make(map[int]traeStreamToolState),
	}
}

func scanTraeSSE(reader io.Reader, consume func(traeSSEEvent) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	name := ""
	data := make([]byte, 0, 4096)
	emit := func() error {
		if name == "" && len(data) == 0 {
			return nil
		}
		event := traeSSEEvent{Name: name, Data: bytes.Clone(data)}
		name = ""
		data = data[:0]
		return consume(event)
	}
	for scanner.Scan() {
		line := bytes.TrimSuffix(scanner.Bytes(), []byte{'\r'})
		if len(line) == 0 {
			if errEmit := emit(); errEmit != nil {
				return errEmit
			}
			continue
		}
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			name = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
		case bytes.HasPrefix(line, []byte("data:")):
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))...)
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return errScan
	}
	return emit()
}

func (state *traeStreamState) convert(event traeSSEEvent) ([]byte, *traeTokenUsage, error) {
	switch event.Name {
	case "metadata":
		var metadata traeMetadataEvent
		if errUnmarshal := json.Unmarshal(event.Data, &metadata); errUnmarshal != nil {
			return nil, nil, fmt.Errorf("trae executor: parse metadata event: %w", errUnmarshal)
		}
		if metadata.SessionID != "" {
			state.ID = "chatcmpl-" + strings.ReplaceAll(metadata.SessionID, "-", "")
		}
		return nil, nil, nil
	case "output":
		var output traeOutputEvent
		if errUnmarshal := json.Unmarshal(event.Data, &output); errUnmarshal != nil {
			return nil, nil, fmt.Errorf("trae executor: parse output event: %w", errUnmarshal)
		}
		delta := make(map[string]any)
		meaningful := false
		if output.Response != "" {
			delta["content"] = output.Response
			meaningful = true
		}
		if output.ReasoningContent != "" {
			delta["reasoning_content"] = output.ReasoningContent
			meaningful = true
		}
		toolCalls := make([]map[string]any, 0, len(output.ToolCalls))
		for _, toolCall := range output.ToolCalls {
			previous := state.Tools[toolCall.Index]
			name := strings.TrimSpace(toolCall.FunctionCall.Name)
			id := strings.TrimSpace(toolCall.ID)
			callType := strings.TrimSpace(toolCall.Type)
			arguments := toolCall.FunctionCall.Arguments
			if arguments == "" && id != "" && name != "" && previous.ID == id && previous.Name == name {
				continue
			}
			converted := map[string]any{"index": toolCall.Index}
			if id != "" {
				converted["id"] = id
			}
			if callType != "" {
				converted["type"] = callType
			}
			function := make(map[string]any)
			if name != "" {
				function["name"] = name
			}
			if arguments != "" {
				function["arguments"] = arguments
			}
			if len(function) > 0 {
				converted["function"] = function
			}
			if len(converted) == 1 {
				continue
			}
			toolCalls = append(toolCalls, converted)
			if id != "" {
				previous.ID = id
			}
			if callType != "" {
				previous.Type = callType
			}
			if name != "" {
				previous.Name = name
			}
			state.Tools[toolCall.Index] = previous
		}
		if len(toolCalls) > 0 {
			delta["tool_calls"] = toolCalls
			meaningful = true
		}
		if len(output.MultimodalContent) > 0 && string(output.MultimodalContent) != "null" {
			var multimodal any
			if json.Unmarshal(output.MultimodalContent, &multimodal) == nil {
				delta["multimodal_contents"] = multimodal
				meaningful = true
			}
		}
		if !meaningful {
			return nil, nil, nil
		}
		if !state.EmittedRole {
			delta["role"] = "assistant"
			state.EmittedRole = true
		}
		return state.chatChunk(delta, nil, nil), nil, nil
	case "token_usage":
		var usage traeTokenUsage
		if errUnmarshal := json.Unmarshal(event.Data, &usage); errUnmarshal != nil {
			return nil, nil, fmt.Errorf("trae executor: parse token usage event: %w", errUnmarshal)
		}
		return state.chatChunk(nil, nil, openAIUsage(usage)), &usage, nil
	case "done":
		var done traeDoneEvent
		if errUnmarshal := json.Unmarshal(event.Data, &done); errUnmarshal != nil {
			return nil, nil, fmt.Errorf("trae executor: parse done event: %w", errUnmarshal)
		}
		if done.FinishReason == "" {
			done.FinishReason = "stop"
		}
		state.Finished = true
		state.FinishReason = done.FinishReason
		return state.chatChunk(map[string]any{}, &done.FinishReason, nil), nil, nil
	case "error":
		var payload map[string]any
		_ = json.Unmarshal(event.Data, &payload)
		message := strings.TrimSpace(anyString(payload["message"]))
		if message == "" {
			message = strings.TrimSpace(string(event.Data))
		}
		return nil, nil, fmt.Errorf("trae upstream error: %s", message)
	default:
		return nil, nil, nil
	}
}

func (state *traeStreamState) chatChunk(delta map[string]any, finishReason *string, usage map[string]any) []byte {
	choices := make([]any, 0, 1)
	if delta != nil || finishReason != nil {
		choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
		if finishReason != nil {
			choice["finish_reason"] = *finishReason
		}
		choices = append(choices, choice)
	}
	payload := map[string]any{
		"id":      state.ID,
		"object":  "chat.completion.chunk",
		"created": state.Created,
		"model":   state.Model,
		"choices": choices,
	}
	if usage != nil {
		payload["usage"] = usage
	}
	raw, _ := json.Marshal(payload)
	return append([]byte("data: "), raw...)
}

type traeResponseAccumulator struct {
	ID                string
	Model             string
	Created           int64
	Content           strings.Builder
	Reasoning         strings.Builder
	Tools             map[int]*traeAccumulatedToolCall
	MultimodalContent json.RawMessage
	Usage             *traeTokenUsage
	FinishReason      string
	Finished          bool
}

type traeAccumulatedToolCall struct {
	Index     int
	ID        string
	Type      string
	Name      string
	Arguments strings.Builder
}

func newTraeResponseAccumulator(model string) *traeResponseAccumulator {
	return &traeResponseAccumulator{
		ID:      "chatcmpl-" + strings.ReplaceAll(newTraeUUID(), "-", ""),
		Model:   model,
		Created: time.Now().Unix(),
		Tools:   make(map[int]*traeAccumulatedToolCall),
	}
}

func (accumulator *traeResponseAccumulator) consume(event traeSSEEvent) error {
	switch event.Name {
	case "metadata":
		var metadata traeMetadataEvent
		if errUnmarshal := json.Unmarshal(event.Data, &metadata); errUnmarshal != nil {
			return fmt.Errorf("trae executor: parse metadata event: %w", errUnmarshal)
		}
		if metadata.SessionID != "" {
			accumulator.ID = "chatcmpl-" + strings.ReplaceAll(metadata.SessionID, "-", "")
		}
	case "output":
		var output traeOutputEvent
		if errUnmarshal := json.Unmarshal(event.Data, &output); errUnmarshal != nil {
			return fmt.Errorf("trae executor: parse output event: %w", errUnmarshal)
		}
		accumulator.Content.WriteString(output.Response)
		accumulator.Reasoning.WriteString(output.ReasoningContent)
		if len(output.MultimodalContent) > 0 && string(output.MultimodalContent) != "null" {
			accumulator.MultimodalContent = bytes.Clone(output.MultimodalContent)
		}
		for _, toolCall := range output.ToolCalls {
			current := accumulator.Tools[toolCall.Index]
			if current == nil {
				current = &traeAccumulatedToolCall{Index: toolCall.Index}
				accumulator.Tools[toolCall.Index] = current
			}
			if toolCall.ID != "" {
				current.ID = toolCall.ID
			}
			if toolCall.Type != "" {
				current.Type = toolCall.Type
			}
			if toolCall.FunctionCall.Name != "" {
				current.Name = toolCall.FunctionCall.Name
			}
			current.Arguments.WriteString(toolCall.FunctionCall.Arguments)
		}
	case "token_usage":
		var usage traeTokenUsage
		if errUnmarshal := json.Unmarshal(event.Data, &usage); errUnmarshal != nil {
			return fmt.Errorf("trae executor: parse token usage event: %w", errUnmarshal)
		}
		accumulator.Usage = &usage
	case "done":
		var done traeDoneEvent
		if errUnmarshal := json.Unmarshal(event.Data, &done); errUnmarshal != nil {
			return fmt.Errorf("trae executor: parse done event: %w", errUnmarshal)
		}
		accumulator.FinishReason = done.FinishReason
		if accumulator.FinishReason == "" {
			accumulator.FinishReason = "stop"
		}
		accumulator.Finished = true
	case "error":
		var payload map[string]any
		_ = json.Unmarshal(event.Data, &payload)
		message := strings.TrimSpace(anyString(payload["message"]))
		if message == "" {
			message = strings.TrimSpace(string(event.Data))
		}
		return fmt.Errorf("trae upstream error: %s", message)
	}
	return nil
}

func (accumulator *traeResponseAccumulator) responseJSON() []byte {
	message := map[string]any{"role": "assistant", "content": accumulator.Content.String()}
	if accumulator.Reasoning.Len() > 0 {
		message["reasoning_content"] = accumulator.Reasoning.String()
	}
	if len(accumulator.MultimodalContent) > 0 {
		var multimodal any
		if json.Unmarshal(accumulator.MultimodalContent, &multimodal) == nil {
			message["multimodal_contents"] = multimodal
		}
	}
	indexes := make([]int, 0, len(accumulator.Tools))
	for index := range accumulator.Tools {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	if len(indexes) > 0 {
		toolCalls := make([]map[string]any, 0, len(indexes))
		for _, index := range indexes {
			toolCall := accumulator.Tools[index]
			callType := toolCall.Type
			if callType == "" {
				callType = "function"
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   toolCall.ID,
				"type": callType,
				"function": map[string]any{
					"name":      toolCall.Name,
					"arguments": toolCall.Arguments.String(),
				},
			})
		}
		message["tool_calls"] = toolCalls
	}
	finishReason := accumulator.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	payload := map[string]any{
		"id":      accumulator.ID,
		"object":  "chat.completion",
		"created": accumulator.Created,
		"model":   accumulator.Model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
	}
	if accumulator.Usage != nil {
		payload["usage"] = openAIUsage(*accumulator.Usage)
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func openAIUsage(usage traeTokenUsage) map[string]any {
	promptTokens := usage.PromptTokens
	completionTokens := usage.CompletionTokens
	totalTokens := usage.TotalTokens
	if promptTokens == 0 && usage.PromptTokensTotal > 0 {
		promptTokens = usage.PromptTokensTotal
	}
	if completionTokens == 0 && usage.CompletionTokensTotal > 0 {
		completionTokens = usage.CompletionTokensTotal
	}
	if totalTokens == 0 && usage.TotalTokensTotal > 0 {
		totalTokens = usage.TotalTokensTotal
	}
	return map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      totalTokens,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": usage.CacheReadInputTokens,
		},
		"completion_tokens_details": map[string]any{
			"reasoning_tokens": usage.ReasoningTokens,
		},
		"cache_creation_input_tokens": usage.CacheCreationInputTokens,
		"cache_read_input_tokens":     usage.CacheReadInputTokens,
	}
}

func anyString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}
