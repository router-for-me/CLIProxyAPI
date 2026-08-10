package auth

import (
	"bytes"
	"encoding/json"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// errEmptyCompletion indicates the upstream returned a terminal but empty
// completion (no content, no tool calls, zero completion tokens). It is
// retriable so the conductor marks the auth as failed, cools it down, and
// rotates to the next auth/model.
var errEmptyCompletion = &Error{
	Code:      "empty_completion",
	Message:   "upstream returned an empty completion",
	Retryable: true,
}

// openAIChunk is the minimal OpenAI-style SSE/JSON shape used to detect empty
// completions.
type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content          string            `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			ToolCalls        []json.RawMessage `json:"tool_calls"`
		} `json:"delta"`
		Message struct {
			Content          string            `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			ToolCalls        []json.RawMessage `json:"tool_calls"`
		} `json:"message"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		CompletionTokens *int `json:"completion_tokens"`
	} `json:"usage"`
}

type claudeContentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	Input    json.RawMessage `json:"input"`
}

type claudeChunk struct {
	Type       string               `json:"type"`
	StopReason *string              `json:"stop_reason"`
	Content    []claudeContentBlock `json:"content"`
	Usage      *struct {
		OutputTokens *int `json:"output_tokens"`
	} `json:"usage"`
	Message *struct {
		Type       string               `json:"type"`
		StopReason *string              `json:"stop_reason"`
		Content    []claudeContentBlock `json:"content"`
		Usage      *struct {
			OutputTokens *int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	ContentBlock *claudeContentBlock `json:"content_block"`
	Delta        *struct {
		Type       string  `json:"type"`
		Text       string  `json:"text"`
		Thinking   string  `json:"thinking"`
		StopReason *string `json:"stop_reason"`
	} `json:"delta"`
}

type geminiPart struct {
	Text             string          `json:"text"`
	FunctionCall     json.RawMessage `json:"functionCall"`
	FunctionCallAlt  json.RawMessage `json:"function_call"`
	InlineData       json.RawMessage `json:"inlineData"`
	InlineDataAlt    json.RawMessage `json:"inline_data"`
	FileData         json.RawMessage `json:"fileData"`
	FileDataAlt      json.RawMessage `json:"file_data"`
	FunctionResponse json.RawMessage `json:"functionResponse"`
	FunctionRespAlt  json.RawMessage `json:"function_response"`
	Thought          json.RawMessage `json:"thought"`
}

type geminiCandidate struct {
	Content *struct {
		Parts []geminiPart `json:"parts"`
	} `json:"content"`
	FinishReason *string `json:"finishReason"`
}

type geminiUsageMetadata struct {
	CandidatesTokenCount *int `json:"candidatesTokenCount"`
	CandidatesTokensAlt  *int `json:"candidates_token_count"`
}

type geminiChunk struct {
	Candidates       []geminiCandidate    `json:"candidates"`
	UsageMetadata    *geminiUsageMetadata `json:"usageMetadata"`
	UsageMetadataAlt *geminiUsageMetadata `json:"usage_metadata"`
	Response         *struct {
		Candidates       []geminiCandidate    `json:"candidates"`
		UsageMetadata    *geminiUsageMetadata `json:"usageMetadata"`
		UsageMetadataAlt *geminiUsageMetadata `json:"usage_metadata"`
	} `json:"response"`
}

// emptyCompletionAccum accumulates the properties relevant to deciding whether
// an OpenAI-, Claude-, or Gemini-style completion is empty.
type emptyCompletionAccum struct {
	recognized       bool
	terminal         bool
	hasContent       bool
	hasToolCalls     bool
	completionTokens int
	sawUsage         bool
}

func (a *emptyCompletionAccum) evalJSON(data []byte) {
	if a.evalOpenAI(data) {
		return
	}
	if a.evalClaude(data) {
		return
	}
	a.evalGemini(data)
}

func (a *emptyCompletionAccum) evalOpenAI(data []byte) bool {
	var chunk openAIChunk
	if err := json.Unmarshal(data, &chunk); err != nil || len(chunk.Choices) == 0 {
		return false
	}
	a.recognized = true
	if chunk.Usage != nil && chunk.Usage.CompletionTokens != nil {
		a.sawUsage = true
		a.completionTokens += *chunk.Usage.CompletionTokens
	}
	for _, ch := range chunk.Choices {
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			a.terminal = true
		}
		content := ch.Delta.Content + ch.Message.Content + ch.Delta.ReasoningContent + ch.Message.ReasoningContent
		if strings.TrimSpace(content) != "" {
			a.hasContent = true
		}
		if len(ch.Delta.ToolCalls) > 0 || len(ch.Message.ToolCalls) > 0 {
			a.hasToolCalls = true
		}
	}
	return true
}

func (a *emptyCompletionAccum) evalClaude(data []byte) bool {
	var chunk claudeChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return false
	}

	isClaude := false
	switch chunk.Type {
	case "message", "message_start", "content_block_start", "content_block_delta", "message_delta", "message_stop":
		isClaude = true
	default:
		if chunk.StopReason != nil || (chunk.Message != nil && (chunk.Message.Type == "message" || chunk.Message.StopReason != nil)) {
			isClaude = true
		}
	}

	if !isClaude {
		return false
	}

	a.recognized = true

	if chunk.Type == "message_stop" {
		a.terminal = true
	}
	if chunk.StopReason != nil && strings.TrimSpace(*chunk.StopReason) != "" {
		a.terminal = true
	}
	if chunk.Message != nil && chunk.Message.StopReason != nil && strings.TrimSpace(*chunk.Message.StopReason) != "" {
		a.terminal = true
	}
	if chunk.Delta != nil && chunk.Delta.StopReason != nil && strings.TrimSpace(*chunk.Delta.StopReason) != "" {
		a.terminal = true
	}

	if chunk.Usage != nil && chunk.Usage.OutputTokens != nil {
		a.sawUsage = true
		a.completionTokens += *chunk.Usage.OutputTokens
	}
	if chunk.Message != nil && chunk.Message.Usage != nil && chunk.Message.Usage.OutputTokens != nil {
		a.sawUsage = true
		a.completionTokens += *chunk.Message.Usage.OutputTokens
	}

	a.evalClaudeBlocks(chunk.Content)
	if chunk.Message != nil {
		a.evalClaudeBlocks(chunk.Message.Content)
	}
	if chunk.ContentBlock != nil {
		a.evalClaudeBlocks([]claudeContentBlock{*chunk.ContentBlock})
	}
	if chunk.Delta != nil {
		switch chunk.Delta.Type {
		case "text_delta":
			if strings.TrimSpace(chunk.Delta.Text) != "" {
				a.hasContent = true
			}
		case "thinking_delta", "signature_delta":
			if strings.TrimSpace(chunk.Delta.Thinking) != "" {
				a.hasContent = true
			}
		case "input_json_delta":
			a.hasToolCalls = true
		default:
			if strings.TrimSpace(chunk.Delta.Text) != "" || strings.TrimSpace(chunk.Delta.Thinking) != "" {
				a.hasContent = true
			}
		}
	}

	return true
}

func (a *emptyCompletionAccum) evalClaudeBlocks(blocks []claudeContentBlock) {
	for _, b := range blocks {
		if b.Type == "tool_use" || len(b.Input) > 0 {
			a.hasToolCalls = true
			continue
		}
		if b.Type == "thinking" || b.Type == "redacted_thinking" || strings.TrimSpace(b.Thinking) != "" {
			a.hasContent = true
			continue
		}
		if strings.TrimSpace(b.Text) != "" {
			a.hasContent = true
			continue
		}
		if b.Type != "" && b.Type != "text" {
			a.hasContent = true
		}
	}
}

func (a *emptyCompletionAccum) evalGemini(data []byte) bool {
	var chunk geminiChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return false
	}

	candidates := chunk.Candidates
	usage := chunk.UsageMetadata
	if usage == nil {
		usage = chunk.UsageMetadataAlt
	}

	if chunk.Response != nil {
		if len(candidates) == 0 {
			candidates = chunk.Response.Candidates
		}
		if usage == nil {
			usage = chunk.Response.UsageMetadata
		}
		if usage == nil {
			usage = chunk.Response.UsageMetadataAlt
		}
	}

	if len(candidates) == 0 {
		return false
	}

	a.recognized = true

	if usage != nil {
		if usage.CandidatesTokenCount != nil {
			a.sawUsage = true
			a.completionTokens += *usage.CandidatesTokenCount
		} else if usage.CandidatesTokensAlt != nil {
			a.sawUsage = true
			a.completionTokens += *usage.CandidatesTokensAlt
		}
	}

	allTerminal := true
	for _, cand := range candidates {
		if cand.FinishReason == nil || strings.TrimSpace(*cand.FinishReason) == "" {
			allTerminal = false
		}
		if cand.Content != nil {
			for _, part := range cand.Content.Parts {
				if len(part.FunctionCall) > 0 || len(part.FunctionCallAlt) > 0 {
					a.hasToolCalls = true
				}
				if len(part.InlineData) > 0 || len(part.InlineDataAlt) > 0 ||
					len(part.FileData) > 0 || len(part.FileDataAlt) > 0 ||
					len(part.FunctionResponse) > 0 || len(part.FunctionRespAlt) > 0 {
					a.hasContent = true
				}
				if len(part.Thought) > 0 {
					thoughtStr := strings.TrimSpace(string(part.Thought))
					if thoughtStr != "" && thoughtStr != "false" && thoughtStr != "null" {
						a.hasContent = true
					}
				}
				if strings.TrimSpace(part.Text) != "" {
					a.hasContent = true
				}
			}
		}
	}

	if allTerminal {
		a.terminal = true
	}

	return true
}

// empty reports whether the accumulated stream is an empty completion.
func (a *emptyCompletionAccum) empty() bool {
	if !a.recognized || !a.terminal {
		return false
	}
	if a.hasContent || a.hasToolCalls {
		return false
	}
	if a.sawUsage && a.completionTokens > 0 {
		return false
	}
	return true
}

// isEmptyCompletion reports whether the buffered SSE stream chunks aggregate to
// an empty completion.
func isEmptyCompletion(chunks []cliproxyexecutor.StreamChunk) bool {
	var buf bytes.Buffer
	for _, c := range chunks {
		buf.Write(c.Payload)
	}
	return isEmptyCompletionPayload(buf.Bytes())
}

// streamBootstrapShouldForward reports whether the stream bootstrap should stop
// buffering and forward the buffered chunks to the client. It returns true when
// the aggregate already contains real output (content or tool calls), or when
// the payload format is not recognized (conservative: no empty completion
// detection for unrecognized streams). It returns false only while the
// aggregate is a recognized completion with no real output yet, so
// the bootstrap keeps reading to detect a complete empty completion.
func streamBootstrapShouldForward(chunks []cliproxyexecutor.StreamChunk) bool {
	var acc emptyCompletionAccum
	var buf bytes.Buffer
	for _, c := range chunks {
		buf.Write(c.Payload)
	}
	acc.evalSSE(buf.Bytes())
	return acc.hasContent || acc.hasToolCalls || !acc.recognized
}

// isEmptyCompletionPayload reports whether a payload (aggregated SSE chunks or
// a single non-stream JSON response) represents an empty completion.
func isEmptyCompletionPayload(payload []byte) bool {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return false
	}

	var acc emptyCompletionAccum

	if bytes.Contains(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) {
		acc.evalSSE(trimmed)
		return acc.empty()
	}

	acc.evalJSON(trimmed)
	return acc.empty()
}

func (a *emptyCompletionAccum) evalSSE(payload []byte) {
	for _, line := range bytes.Split(payload, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("event:")) {
			event := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("event:")))
			if bytes.Equal(event, []byte("message_stop")) {
				a.recognized = true
				a.terminal = true
			}
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if bytes.Equal(data, []byte("[DONE]")) {
			a.recognized = true
			a.terminal = true
			continue
		}
		if len(data) == 0 {
			continue
		}
		a.evalJSON(data)
	}
}