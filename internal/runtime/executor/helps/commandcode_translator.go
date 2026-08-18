package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	sdktr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	// FormatOpenAIChat represents the OpenAI chat completions schema.
	FormatOpenAIChat = sdktr.Format("openai.chat")

	// FormatCommandCode represents the Command Code /alpha/generate schema.
	FormatCommandCode = sdktr.Format("commandcode")
)

// CommandCodeStaticConfig represents the required schema-strict config envelope for /alpha/generate.
type CommandCodeStaticConfig struct {
	WorkingDir    string   `json:"workingDir"`
	Date          string   `json:"date"`
	Environment   string   `json:"environment"`
	Structure     []string `json:"structure"`
	IsGitRepo     bool     `json:"isGitRepo"`
	CurrentBranch string   `json:"currentBranch"`
	MainBranch    string   `json:"mainBranch"`
	GitStatus     string   `json:"gitStatus"`
	RecentCommits []string `json:"recentCommits"`
}

// BuildDefaultConfig returns the strict static config expected by Command Code /alpha/generate.
func BuildDefaultConfig() CommandCodeStaticConfig {
	return CommandCodeStaticConfig{
		WorkingDir:    "/",
		Date:          time.Now().UTC().Format("2006-01-02"),
		Environment:   "production",
		Structure:     []string{},
		IsGitRepo:     false,
		CurrentBranch: "",
		MainBranch:    "",
		GitStatus:     "",
		RecentCommits: []string{},
	}
}

// CommandCodeUserContent represents a content block for role: user.
type CommandCodeUserContent struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Image     string `json:"image,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
}

// CommandCodeAssistantContent represents a content block for role: assistant.
type CommandCodeAssistantContent struct {
	Type       string         `json:"type"`
	Text       string         `json:"text,omitempty"`
	ToolCallID string         `json:"toolCallId,omitempty"`
	ToolName   string         `json:"toolName,omitempty"`
	Input      map[string]any `json:"input,omitempty"`
}

// CommandCodeToolOutput represents the nested output object in tool-result.
type CommandCodeToolOutput struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// CommandCodeToolContent represents a content block for role: tool.
type CommandCodeToolContent struct {
	Type       string                `json:"type"`
	ToolCallID string                `json:"toolCallId"`
	ToolName   string                `json:"toolName"`
	Output     CommandCodeToolOutput `json:"output"`
}

// CommandCodeMessage represents a Vercel AI SDK ModelMessage.
type CommandCodeMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// CommandCodeTool represents a tool declaration sent to Command Code.
type CommandCodeTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema"`
}

// CommandCodeEnvelope represents the top-level payload sent to POST /alpha/generate.
type CommandCodeEnvelope struct {
	Config         CommandCodeStaticConfig `json:"config"`
	Memory         string                  `json:"memory"`
	Taste          any                     `json:"taste"`
	Skills         any                     `json:"skills"`
	PermissionMode string                  `json:"permissionMode"`
	Params         CommandCodeParams       `json:"params"`
}

// CommandCodeParams holds execution parameters under the envelope.
type CommandCodeParams struct {
	Model       string               `json:"model"`
	System      string               `json:"system,omitempty"`
	Messages    []CommandCodeMessage `json:"messages"`
	Tools       []CommandCodeTool    `json:"tools,omitempty"`
	MaxTokens   int64                `json:"max_tokens,omitempty"`
	Temperature *float64             `json:"temperature,omitempty"`
	Stream      bool                 `json:"stream"`
}

// ConvertOpenAIToCommandCodeRequest transforms an OpenAI Chat Completions JSON request
// into the Command Code /alpha/generate request envelope.
//
// Key contract: upstream params.stream is ALWAYS forced to true because /alpha/generate
// rejects stream: false with 400.
func ConvertOpenAIToCommandCodeRequest(modelName string, rawJSON []byte, _ bool) []byte {
	root := gjson.ParseBytes(rawJSON)

	model := modelName
	if model == "" {
		model = root.Get("model").String()
	}

	var systemPrompts []string
	var messages []CommandCodeMessage
	toolNameByID := make(map[string]string)

	// First pass: collect tool call id -> function name mappings across all assistant messages
	if rawMessages := root.Get("messages"); rawMessages.IsArray() {
		rawMessages.ForEach(func(_, msg gjson.Result) bool {
			if strings.EqualFold(msg.Get("role").String(), "assistant") {
				if toolCalls := msg.Get("tool_calls"); toolCalls.IsArray() {
					toolCalls.ForEach(func(_, tc gjson.Result) bool {
						callID := tc.Get("id").String()
						name := tc.Get("function.name").String()
						if callID != "" && name != "" {
							toolNameByID[callID] = name
						}
						return true
					})
				}
			}
			return true
		})
	}

	// Second pass: construct Vercel AI SDK ModelMessage[] structures
	if rawMessages := root.Get("messages"); rawMessages.IsArray() {
		rawMessages.ForEach(func(_, msg gjson.Result) bool {
			role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
			contentRes := msg.Get("content")

			switch role {
			case "system", "developer":
				if contentRes.Type == gjson.String && contentRes.String() != "" {
					systemPrompts = append(systemPrompts, contentRes.String())
				} else if contentRes.IsArray() {
					contentRes.ForEach(func(_, part gjson.Result) bool {
						if part.Get("type").String() == "text" && part.Get("text").String() != "" {
							systemPrompts = append(systemPrompts, part.Get("text").String())
						}
						return true
					})
				}

			case "user":
				var userParts []CommandCodeUserContent
				if contentRes.Type == gjson.String {
					if contentRes.String() != "" {
						userParts = append(userParts, CommandCodeUserContent{
							Type: "text",
							Text: contentRes.String(),
						})
					}
				} else if contentRes.IsArray() {
					contentRes.ForEach(func(_, part gjson.Result) bool {
						partType := part.Get("type").String()
						switch partType {
						case "text":
							text := part.Get("text").String()
							if text != "" {
								userParts = append(userParts, CommandCodeUserContent{
									Type: "text",
									Text: text,
								})
							}
						case "image_url":
							url := part.Get("image_url.url").String()
							mediaType := "image/jpeg"
							if strings.HasPrefix(url, "data:") {
								if idx := strings.Index(url, ";"); idx > 5 {
									mediaType = url[5:idx]
								}
							}
							userParts = append(userParts, CommandCodeUserContent{
								Type:      "image",
								Image:     url,
								MediaType: mediaType,
							})
						}
						return true
					})
				}
				if len(userParts) > 0 {
					messages = append(messages, CommandCodeMessage{
						Role:    "user",
						Content: userParts,
					})
				}

			case "assistant":
				var assistantParts []CommandCodeAssistantContent
				if contentRes.Type == gjson.String && contentRes.String() != "" {
					assistantParts = append(assistantParts, CommandCodeAssistantContent{
						Type: "text",
						Text: contentRes.String(),
					})
				}

				if toolCalls := msg.Get("tool_calls"); toolCalls.IsArray() {
					toolCalls.ForEach(func(_, tc gjson.Result) bool {
						callID := tc.Get("id").String()
						name := tc.Get("function.name").String()
						rawArgs := tc.Get("function.arguments").String()

						if callID != "" && name != "" {
							toolNameByID[callID] = name
						}

						var inputMap map[string]any
						if rawArgs != "" {
							_ = json.Unmarshal([]byte(rawArgs), &inputMap)
						}
						if inputMap == nil {
							inputMap = make(map[string]any)
						}

						assistantParts = append(assistantParts, CommandCodeAssistantContent{
							Type:       "tool-call",
							ToolCallID: callID,
							ToolName:   name,
							Input:      inputMap,
						})
						return true
					})
				}

				if len(assistantParts) > 0 {
					messages = append(messages, CommandCodeMessage{
						Role:    "assistant",
						Content: assistantParts,
					})
				}

			case "tool":
				callID := msg.Get("tool_call_id").String()
				name := toolNameByID[callID]
				if name == "" {
					name = msg.Get("name").String()
				}
				if name == "" {
					name = "unknown"
				}
				val := contentRes.String()

				block := CommandCodeToolContent{
					Type:       "tool-result",
					ToolCallID: callID,
					ToolName:   name,
					Output: CommandCodeToolOutput{
						Type:  "text",
						Value: val,
					},
				}

				// Merge consecutive tool results into one role: "tool" message
				if len(messages) > 0 && messages[len(messages)-1].Role == "tool" {
					if existing, ok := messages[len(messages)-1].Content.([]CommandCodeToolContent); ok {
						messages[len(messages)-1].Content = append(existing, block)
					} else {
						messages = append(messages, CommandCodeMessage{
							Role:    "tool",
							Content: []CommandCodeToolContent{block},
						})
					}
				} else {
					messages = append(messages, CommandCodeMessage{
						Role:    "tool",
						Content: []CommandCodeToolContent{block},
					})
				}
			}
			return true
		})
	}

	// Parse tools array
	var tools []CommandCodeTool
	if rawTools := root.Get("tools"); rawTools.IsArray() {
		rawTools.ForEach(func(_, toolRes gjson.Result) bool {
			fn := toolRes.Get("function")
			if !fn.Exists() {
				fn = toolRes
			}
			name := fn.Get("name").String()
			if name == "" {
				return true
			}
			desc := fn.Get("description").String()
			var paramsObj any
			if p := fn.Get("parameters"); p.Exists() {
				_ = json.Unmarshal([]byte(p.Raw), &paramsObj)
			}
			if paramsObj == nil {
				paramsObj = map[string]any{"type": "object", "properties": map[string]any{}}
			}

			tools = append(tools, CommandCodeTool{
				Name:        name,
				Description: desc,
				InputSchema: paramsObj,
			})
			return true
		})
	}

	systemStr := strings.Join(systemPrompts, "\n\n")

	envelope := CommandCodeEnvelope{
		Config:         BuildDefaultConfig(),
		Memory:         "",
		Taste:          nil,
		Skills:         nil,
		PermissionMode: "standard",
		Params: CommandCodeParams{
			Model:    model,
			System:   systemStr,
			Messages: messages,
			Tools:    tools,
			Stream:   true, // Upstream is ALWAYS stream: true
		},
	}

	if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
		envelope.Params.MaxTokens = maxTokens.Int()
	} else if maxCompletionTokens := root.Get("max_completion_tokens"); maxCompletionTokens.Exists() {
		envelope.Params.MaxTokens = maxCompletionTokens.Int()
	}
	if temp := root.Get("temperature"); temp.Exists() {
		val := temp.Float()
		envelope.Params.Temperature = &val
	}

	payload, err := json.Marshal(envelope)
	if err != nil {
		return rawJSON
	}
	return payload
}

// StreamTransformState maintains state across streaming chunks.
type StreamTransformState struct {
	ResponseID       string
	CreatedAt        int64
	Model            string
	RoleSent         bool
	SawTerminal      bool
	ToolCallIndices  map[string]int
	ToolCallNames    map[string]string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CachedTokens     int64
	HasToolCalls     bool
}

// ConvertCommandCodeStreamToOpenAI converts a single NDJSON line into OpenAI SSE chunks.
func ConvertCommandCodeStreamToOpenAI(ctx context.Context, modelName string, originalReq, translatedReq, rawJSON []byte, param *any) [][]byte {
	if param == nil {
		return nil
	}

	var state *StreamTransformState
	if *param == nil {
		respID := fmt.Sprintf("chatcmpl-%s", uuid.New().String()[:12])
		state = &StreamTransformState{
			ResponseID:      respID,
			CreatedAt:       time.Now().Unix(),
			Model:           modelName,
			ToolCallIndices: make(map[string]int),
			ToolCallNames:   make(map[string]string),
		}
		*param = state
	} else {
		state = (*param).(*StreamTransformState)
	}

	trimmed := bytes.TrimSpace(rawJSON)
	if len(trimmed) == 0 {
		return nil
	}

	event, err := ParseRawJSONLine(trimmed)
	if err != nil {
		return nil
	}

	var chunks [][]byte

	emitChunk := func(deltaMap map[string]any, finishReason any, usage any) {
		choice := map[string]any{
			"index":         0,
			"delta":         deltaMap,
			"finish_reason": finishReason,
		}
		chunkObj := map[string]any{
			"id":      state.ResponseID,
			"object":  "chat.completion.chunk",
			"created": state.CreatedAt,
			"model":   state.Model,
			"choices": []any{choice},
		}
		if usage != nil {
			chunkObj["usage"] = usage
		}
		b, _ := json.Marshal(chunkObj)
		chunks = append(chunks, b)
	}

	switch event.Type {
	case "reasoning-delta":
		delta := map[string]any{
			"reasoning_content": event.Text,
		}
		if !state.RoleSent {
			delta["role"] = "assistant"
			state.RoleSent = true
		}
		emitChunk(delta, nil, nil)

	case "text-delta":
		delta := map[string]any{
			"content": event.Text,
		}
		if !state.RoleSent {
			delta["role"] = "assistant"
			state.RoleSent = true
		}
		emitChunk(delta, nil, nil)

	case "tool-input-start":
		state.HasToolCalls = true
		callID := event.NormalizeToolEventID()
		name := event.NormalizeToolName()

		idx, exists := state.ToolCallIndices[callID]
		if !exists {
			idx = len(state.ToolCallIndices)
			state.ToolCallIndices[callID] = idx
		}
		if name != "" {
			state.ToolCallNames[callID] = name
		}

		toolCallDelta := map[string]any{
			"index": idx,
			"id":    callID,
			"type":  "function",
			"function": map[string]any{
				"name":      name,
				"arguments": "",
			},
		}

		delta := map[string]any{
			"tool_calls": []any{toolCallDelta},
		}
		if !state.RoleSent {
			delta["role"] = "assistant"
			state.RoleSent = true
		}
		emitChunk(delta, nil, nil)

	case "tool-input-delta":
		state.HasToolCalls = true
		callID := event.NormalizeToolEventID()
		idx, exists := state.ToolCallIndices[callID]
		if !exists {
			idx = len(state.ToolCallIndices)
			state.ToolCallIndices[callID] = idx
		}

		toolCallDelta := map[string]any{
			"index": idx,
			"function": map[string]any{
				"arguments": event.Delta,
			},
		}

		delta := map[string]any{
			"tool_calls": []any{toolCallDelta},
		}
		if !state.RoleSent {
			delta["role"] = "assistant"
			state.RoleSent = true
		}
		emitChunk(delta, nil, nil)

	case "tool-input-end", "tool-call":
		state.HasToolCalls = true
		callID := event.NormalizeToolEventID()
		name := event.NormalizeToolName()
		if name == "" {
			name = state.ToolCallNames[callID]
		}
		idx, exists := state.ToolCallIndices[callID]
		if !exists {
			idx = len(state.ToolCallIndices)
			state.ToolCallIndices[callID] = idx
		}

		rawInput := event.Input
		if len(rawInput) == 0 {
			rawInput = event.Args
		}

		if len(rawInput) > 0 {
			toolCallDelta := map[string]any{
				"index": idx,
				"id":    callID,
				"type":  "function",
				"function": map[string]any{
					"name":      name,
					"arguments": string(rawInput),
				},
			}
			delta := map[string]any{
				"tool_calls": []any{toolCallDelta},
			}
			if !state.RoleSent {
				delta["role"] = "assistant"
				state.RoleSent = true
			}
			emitChunk(delta, nil, nil)
		}

	case "finish-step", "finish":
		state.SawTerminal = true

		u := event.Usage
		if u == nil {
			u = event.TotalUsage
		}
		if u != nil {
			if u.InputTokens > 0 {
				state.PromptTokens = u.InputTokens
			} else if u.InputTokensAlt > 0 {
				state.PromptTokens = u.InputTokensAlt
			}
			if u.OutputTokens > 0 {
				state.CompletionTokens = u.OutputTokens
			} else if u.OutputTokensAlt > 0 {
				state.CompletionTokens = u.OutputTokensAlt
			}
			state.TotalTokens = u.TotalTokens
			if state.TotalTokens == 0 {
				state.TotalTokens = u.TotalTokensAlt
			}
			if state.TotalTokens == 0 {
				state.TotalTokens = state.PromptTokens + state.CompletionTokens
			}
			if u.CachedInputTokens > 0 {
				state.CachedTokens = u.CachedInputTokens
			} else if u.InputTokenDetails != nil {
				state.CachedTokens = u.InputTokenDetails.CacheReadTokens
			} else if u.Raw != nil {
				state.CachedTokens = u.Raw.PromptCacheHit
			}
		}

		rawReason := event.FinishReason
		if rawReason == "" {
			rawReason = event.RawFinishReason
		}
		finishReason := MapFinishReason(rawReason, state.HasToolCalls)

		emitChunk(map[string]any{}, finishReason, nil)

		usageObj := map[string]any{
			"prompt_tokens":     state.PromptTokens,
			"completion_tokens": state.CompletionTokens,
			"total_tokens":      state.TotalTokens,
		}
		if state.CachedTokens > 0 {
			usageObj["prompt_tokens_details"] = map[string]any{
				"cached_tokens": state.CachedTokens,
			}
		}

		finalChunk := map[string]any{
			"id":      state.ResponseID,
			"object":  "chat.completion.chunk",
			"created": state.CreatedAt,
			"model":   state.Model,
			"choices": []any{},
			"usage":   usageObj,
		}
		b, _ := json.Marshal(finalChunk)
		chunks = append(chunks, b)
	}

	return chunks
}

// ConvertCommandCodeNonStreamToOpenAI converts the aggregated full NDJSON lines into a single ChatCompletion JSON response.
func ConvertCommandCodeNonStreamToOpenAI(ctx context.Context, modelName string, originalReq, translatedReq, rawJSON []byte, param *any) []byte {
	resp, err := AccumulateNDJSON(ctx, modelName, rawJSON)
	if err != nil {
		return rawJSON
	}
	return resp
}

// AccumulateNDJSON parses full raw NDJSON bytes into standard OpenAI Chat Completion JSON.
func AccumulateNDJSON(ctx context.Context, model string, ndjsonBytes []byte) ([]byte, error) {
	respID := fmt.Sprintf("chatcmpl-%s", uuid.New().String()[:12])
	createdAt := time.Now().Unix()

	acc := NewStreamAccumulator(respID, model, createdAt)
	reader := NewUnboundedNDJSONReader(bytes.NewReader(ndjsonBytes))

	for {
		line, err := reader.ReadNextLine(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}

		event, errParse := ParseRawJSONLine(line)
		if errParse != nil {
			return nil, fmt.Errorf("malformed ndjson line: %w", errParse)
		}

		if errProc := acc.ProcessEvent(event); errProc != nil {
			return nil, errProc
		}
	}

	if !acc.SawTerminal {
		return nil, errors.New("commandcode: stream ended before finish-step or finish event")
	}

	messageObj := map[string]any{
		"role": "assistant",
	}

	contentStr := acc.Content.String()
	if contentStr != "" || len(acc.ToolCalls) == 0 {
		messageObj["content"] = contentStr
	} else {
		messageObj["content"] = nil
	}

	if acc.ReasoningContent.Len() > 0 {
		messageObj["reasoning_content"] = acc.ReasoningContent.String()
	}

	if len(acc.ToolCalls) > 0 {
		toolCalls := make([]any, 0, len(acc.ToolCalls))
		for _, tc := range acc.ToolCalls {
			toolCalls = append(toolCalls, map[string]any{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": tc.Arguments.String(),
				},
			})
		}
		messageObj["tool_calls"] = toolCalls
	}

	choice := map[string]any{
		"index":         0,
		"message":       messageObj,
		"finish_reason": acc.FinishReason,
	}

	usageObj := map[string]any{
		"prompt_tokens":     acc.PromptTokens,
		"completion_tokens": acc.CompletionTokens,
		"total_tokens":      acc.TotalTokens,
	}
	if acc.CachedTokens > 0 {
		usageObj["prompt_tokens_details"] = map[string]any{
			"cached_tokens": acc.CachedTokens,
		}
	}

	respObj := map[string]any{
		"id":      acc.ResponseID,
		"object":  "chat.completion",
		"created": acc.CreatedAt,
		"model":   acc.Model,
		"choices": []any{choice},
		"usage":   usageObj,
	}

	out, errMarshal := json.Marshal(respObj)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return out, nil
}
