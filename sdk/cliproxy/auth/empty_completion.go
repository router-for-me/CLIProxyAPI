package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// errEmptyCompletion indicates the upstream returned a terminal but empty
// completion (no content, no tool calls, zero completion tokens). It is
// retriable so the conductor marks the auth as failed, cools it down, and
// rotates to the next auth/model.
var errEmptyCompletion = &Error{
	Code:       "empty_completion",
	Message:    "upstream returned an empty completion",
	Retryable:  true,
	HTTPStatus: http.StatusServiceUnavailable,
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
	InlineData       json.RawMessage `json:"inlineData"`
	FileData         json.RawMessage `json:"fileData"`
	FunctionResponse json.RawMessage `json:"functionResponse"`
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
}

type geminiChunk struct {
	Candidates    []geminiCandidate    `json:"candidates"`
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata"`
	Response      *struct {
		Candidates    []geminiCandidate    `json:"candidates"`
		UsageMetadata *geminiUsageMetadata `json:"usageMetadata"`
	} `json:"response"`
}

// openAIResponseUsage is the usage block of the OpenAI Responses-API shape
// (used by codex/xai executors).
type openAIResponseUsage struct {
	OutputTokens *int `json:"output_tokens"`
}

type openAIResponseContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAIResponseOutputItem struct {
	Type      string                       `json:"type"`
	Text      string                       `json:"text"`
	Arguments string                       `json:"arguments"`
	Content   []openAIResponseContentPart  `json:"content"`
}

type openAIResponseObject struct {
	Status string                     `json:"status"`
	Output json.RawMessage            `json:"output"`
	Usage  *openAIResponseUsage       `json:"usage"`
}

type openAIResponseChunk struct {
	Type      string                `json:"type"`
	Object    string                `json:"object"`
	Status    string                `json:"status"`
	Output    json.RawMessage       `json:"output"`
	Usage     *openAIResponseUsage  `json:"usage"`
	Response  *openAIResponseObject `json:"response"`
	Delta     string                `json:"delta"`
	Text      string                `json:"text"`
	Arguments string                `json:"arguments"`
}

// openAIResponseEventTypes is the conservative set of Responses-API streamed
// event types we recognize. Unknown/partial sub-shapes are left unrecognized so
// the stream is forwarded rather than judged empty.
var openAIResponseEventTypes = map[string]bool{
	"response.created":                         true,
	"response.in_progress":                     true,
	"response.completed":                       true,
	"response.incomplete":                      true,
	"response.failed":                          true,
	"response.output_item.added":               true,
	"response.output_item.done":                true,
	"response.output_text.delta":               true,
	"response.output_text.done":                true,
	"response.function_call_arguments.delta":   true,
	"response.function_call_arguments.done":    true,
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
	blocked          bool
}

func (a *emptyCompletionAccum) evalJSON(data []byte) {
	if a.evalOpenAI(data) {
		return
	}
	if a.evalClaude(data) {
		return
	}
	if a.evalOpenAIResponse(data) {
		return
	}
	a.evalGemini(data)
}

func (a *emptyCompletionAccum) evalOpenAI(data []byte) bool {
	// Recognize the OpenAI shape by the presence of a "choices" key, even when
	// the array is empty (e.g. {"choices":[]}). Such prefixes must still be
	// judged at stream close instead of being forwarded immediately.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	if _, ok := probe["choices"]; !ok {
		return false
	}
	a.recognized = true
	var chunk openAIChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return true
	}
	if chunk.Usage != nil && chunk.Usage.CompletionTokens != nil {
		a.sawUsage = true
		a.completionTokens += *chunk.Usage.CompletionTokens
	}
	for _, ch := range chunk.Choices {
		if ch.FinishReason != nil {
			reason := strings.TrimSpace(*ch.FinishReason)
			if strings.EqualFold(reason, "stop") {
				a.terminal = true
			} else if reason != "" {
				// content_filter, length, and other non-stop terminal reasons
				// are not empty completions: the client must see the reason
				// rather than a silent auth rotation.
				a.blocked = true
			}
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

func (a *emptyCompletionAccum) evalOpenAIResponse(data []byte) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}

	var evType string
	if raw := probe["type"]; raw != nil {
		_ = json.Unmarshal(raw, &evType)
	}
	var objName string
	if raw := probe["object"]; raw != nil {
		_ = json.Unmarshal(raw, &objName)
	}

	if objName != "response" && !openAIResponseEventTypes[evType] {
		return false
	}
	a.recognized = true

	var chunk openAIResponseChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return true
	}

	switch evType {
	case "response.completed", "response.incomplete":
		a.terminal = true
	}
	if (chunk.Object == "response" && (chunk.Status == "completed" || chunk.Status == "incomplete")) ||
		(chunk.Response != nil && (chunk.Response.Status == "completed" || chunk.Response.Status == "incomplete")) {
		a.terminal = true
	}

	if chunk.Usage != nil && chunk.Usage.OutputTokens != nil {
		a.sawUsage = true
		a.completionTokens += *chunk.Usage.OutputTokens
	}
	if chunk.Response != nil && chunk.Response.Usage != nil && chunk.Response.Usage.OutputTokens != nil {
		a.sawUsage = true
		a.completionTokens += *chunk.Response.Usage.OutputTokens
	}

	switch evType {
	case "response.output_text.delta":
		if strings.TrimSpace(chunk.Delta) != "" {
			a.hasContent = true
		}
	case "response.output_text.done":
		if strings.TrimSpace(chunk.Text) != "" {
			a.hasContent = true
		}
	case "response.function_call_arguments.delta", "response.function_call_arguments.done":
		a.hasToolCalls = true
	}

	a.evalOpenAIResponseRawOutput(chunk.Output)
	if chunk.Response != nil {
		a.evalOpenAIResponseRawOutput(chunk.Response.Output)
	}

	return true
}

func (a *emptyCompletionAccum) evalOpenAIResponseRawOutput(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var items []openAIResponseOutputItem
	if err := json.Unmarshal(raw, &items); err == nil {
		a.evalOpenAIResponseOutput(items)
		return
	}
	var item openAIResponseOutputItem
	if err := json.Unmarshal(raw, &item); err == nil {
		a.evalOpenAIResponseOutput([]openAIResponseOutputItem{item})
	}
}

func (a *emptyCompletionAccum) evalOpenAIResponseOutput(items []openAIResponseOutputItem) {
	for _, item := range items {
		switch item.Type {
		case "function_call", "web_search_call", "file_search_call", "computer_call":
			a.hasToolCalls = true
		}
		if strings.TrimSpace(item.Text) != "" {
			a.hasContent = true
		}
		for _, part := range item.Content {
			if strings.TrimSpace(part.Text) != "" {
				a.hasContent = true
			}
		}
	}
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

// hasJSONKey reports whether the given JSON object contains name as a top-level
// key. It returns false for non-object or malformed input.
func hasJSONKey(data []byte, name string) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	_, ok := probe[name]
	return ok
}

// hasNestedResponseCandidates reports whether the payload's response object
// contains a candidates key (the Gemini streaming wrapper shape).
func hasNestedResponseCandidates(data []byte) bool {
	var probe struct {
		Response map[string]json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	_, ok := probe.Response["candidates"]
	return ok
}

func (a *emptyCompletionAccum) evalGemini(data []byte) bool {
	var chunk geminiChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return false
	}

	candidates := chunk.Candidates
	usage := chunk.UsageMetadata

	if chunk.Response != nil {
		if len(candidates) == 0 {
			candidates = chunk.Response.Candidates
		}
		if usage == nil {
			usage = chunk.Response.UsageMetadata
		}
	}

	if len(candidates) == 0 {
		// Only treat an empty candidates array as a recognized empty completion
		// when the candidates key is actually present (e.g. a Gemini
		// safety/empty response with zero candidate tokens, or a stream
		// aggregate with nothing else). An absent candidates key is not a
		// Gemini shape at all.
		if hasJSONKey(data, "candidates") || hasNestedResponseCandidates(data) {
			a.recognized = true
			a.terminal = true
			if usage != nil && usage.CandidatesTokenCount != nil {
				a.sawUsage = true
				a.completionTokens += *usage.CandidatesTokenCount
			}
			return true
		}
		return false
	}

	a.recognized = true

	if usage != nil {
		if usage.CandidatesTokenCount != nil {
			a.sawUsage = true
			a.completionTokens += *usage.CandidatesTokenCount
		}
	}

	allTerminal := true
	blocked := false
	for _, cand := range candidates {
		if cand.FinishReason == nil {
			allTerminal = false
		} else {
			reason := strings.TrimSpace(*cand.FinishReason)
			if reason == "" {
				allTerminal = false
			} else if !strings.EqualFold(reason, "STOP") {
				// A blocking or other terminal reason (SAFETY, RECITATION,
				// MAX_TOKENS, BLOCKLIST, PROHIBITED_CONTENT, OTHER) is not an
				// empty completion: the client must see the stop/block reason
				// rather than a silent auth rotation.
				allTerminal = false
				blocked = true
			}
		}
		if cand.Content != nil {
			for _, part := range cand.Content.Parts {
				if len(part.FunctionCall) > 0 {
					a.hasToolCalls = true
				}
				if len(part.InlineData) > 0 ||
					len(part.FileData) > 0 ||
					len(part.FunctionResponse) > 0 {
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
	if blocked {
		a.blocked = true
	}

	return true
}

// empty reports whether the accumulated stream is an empty completion.
func (a *emptyCompletionAccum) empty() bool {
	if !a.recognized || !a.terminal {
		return false
	}
	if a.blocked {
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

// markEmptyCompletion records a failed retriable empty-completion result and
// returns the error to propagate. The mixed duty execution path rotates on an
// empty completion; the home (credits) path reports it via reportHomeResult.
func (m *Manager) markEmptyCompletion(ctx context.Context, result *Result) error {
	result.Success = false
	result.Error = errEmptyCompletion
	m.MarkResult(ctx, *result)
	return errEmptyCompletion
}
