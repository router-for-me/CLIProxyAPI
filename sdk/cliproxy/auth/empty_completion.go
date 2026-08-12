package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// tokenCount is a tolerant usage count that accepts any valid JSON number
// (integer, decimal, or exponent) and treats every other JSON value (null,
// string, object, array, or malformed) as unset, absorbing it without failing
// the enclosing frame. positive reports whether the count is a finite number
// greater than zero, the only property the empty-completion logic needs.
type tokenCount json.Number

func (t *tokenCount) UnmarshalJSON(b []byte) error {
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		*t = ""
		return nil
	}
	*t = tokenCount(n)
	return nil
}

// positive reports whether c is a finite JSON number greater than zero.
func (c tokenCount) positive() bool {
	n := json.Number(c)
	if n == "" {
		return false
	}
	f, err := n.Float64()
	if err != nil {
		return false
	}
	return !math.IsNaN(f) && !math.IsInf(f, 0) && f > 0
}

// addUsage folds a positive usage count into the accumulator's token total.
// Exact integer counts are summed; fractional, huge, or otherwise non-integer
// positive values still count as output evidence so the >0 check holds.
func (a *emptyCompletionAccum) addUsage(c tokenCount) {
	if !c.positive() {
		return
	}
	if n, err := json.Number(c).Int64(); err == nil && n > 0 {
		a.completionTokens += int(n)
	} else {
		a.completionTokens = max(a.completionTokens, 1)
	}
}

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

// maxStreamBootstrapBytes bounds how much metadata a stream can accumulate
// before the conductor conservatively forwards it. Empty-completion detection
// must never create an unbounded pre-output buffer.
const maxStreamBootstrapBytes = 1 << 20

// openAIChunk is the minimal OpenAI-style SSE/JSON shape used to detect empty
// completions.
type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content          string            `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			Refusal          *string           `json:"refusal"`
			ToolCalls        []json.RawMessage `json:"tool_calls"`
			FunctionCall     json.RawMessage   `json:"function_call"`
			Audio            json.RawMessage   `json:"audio"`
		} `json:"delta"`
		Message struct {
			Content          string            `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			Refusal          *string           `json:"refusal"`
			ToolCalls        []json.RawMessage `json:"tool_calls"`
			FunctionCall     json.RawMessage   `json:"function_call"`
			Audio            json.RawMessage   `json:"audio"`
		} `json:"message"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		CompletionTokens *tokenCount `json:"completion_tokens"`
	} `json:"usage"`
}

// nonEmptyJSONPayload reports whether raw holds a payload beyond an empty
// null, empty string, empty object, or empty array.
func nonEmptyJSONPayload(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == `""` || s == "{}" || s == "[]" {
		return false
	}
	return true
}

func nonEmptyAudioPayload(raw json.RawMessage) bool {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return false
	}
	return nonEmptyAudioValue(value)
}

func nonEmptyAudioValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case bool:
		return typed
	case json.Number:
		number, err := typed.Float64()
		return err == nil && !math.IsNaN(number) && !math.IsInf(number, 0) && number != 0
	case []any:
		for _, item := range typed {
			if nonEmptyAudioValue(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if nonEmptyAudioValue(item) {
				return true
			}
		}
	}
	return false
}

// nonEmptyFunctionCall reports whether a legacy OpenAI function_call object
// carries a non-empty name and/or non-empty arguments.
func nonEmptyFunctionCall(raw json.RawMessage) bool {
	var fc struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		return false
	}
	return strings.TrimSpace(fc.Name) != "" || strings.TrimSpace(fc.Arguments) != ""
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
		OutputTokens *tokenCount `json:"output_tokens"`
	} `json:"usage"`
	Message *struct {
		Type       string               `json:"type"`
		StopReason *string              `json:"stop_reason"`
		Content    []claudeContentBlock `json:"content"`
		Usage      *struct {
			OutputTokens *tokenCount `json:"output_tokens"`
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
	Text                string          `json:"text"`
	FunctionCall        json.RawMessage `json:"functionCall"`
	InlineData          json.RawMessage `json:"inlineData"`
	FileData            json.RawMessage `json:"fileData"`
	FunctionResponse    json.RawMessage `json:"functionResponse"`
	ExecutableCode      json.RawMessage `json:"executableCode"`
	CodeExecutionResult json.RawMessage `json:"codeExecutionResult"`
	Thought             json.RawMessage `json:"thought"`
}

type geminiCandidate struct {
	Content *struct {
		Parts []geminiPart `json:"parts"`
	} `json:"content"`
	FinishReason *string `json:"finishReason"`
}

type geminiUsageMetadata struct {
	CandidatesTokenCount *tokenCount `json:"candidatesTokenCount"`
}

type geminiPromptFeedback struct {
	BlockReason string `json:"blockReason"`
}

type geminiChunk struct {
	Candidates     []geminiCandidate     `json:"candidates"`
	UsageMetadata  *geminiUsageMetadata  `json:"usageMetadata"`
	PromptFeedback *geminiPromptFeedback `json:"promptFeedback"`
	Response       *struct {
		Candidates     []geminiCandidate     `json:"candidates"`
		UsageMetadata  *geminiUsageMetadata  `json:"usageMetadata"`
		PromptFeedback *geminiPromptFeedback `json:"promptFeedback"`
	} `json:"response"`
}

// openAIResponseUsage is the usage block of the OpenAI Responses-API shape
// (used by codex/xai executors).
type openAIResponseUsage struct {
	OutputTokens *tokenCount `json:"output_tokens"`
}

type openAIResponseContentPart struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type openAIResponseOutputItem struct {
	Type      string                      `json:"type"`
	Text      string                      `json:"text"`
	Arguments string                      `json:"arguments"`
	Content   []openAIResponseContentPart `json:"content"`
}

type openAIResponseObject struct {
	Status string               `json:"status"`
	Output json.RawMessage      `json:"output"`
	Usage  *openAIResponseUsage `json:"usage"`
}

type openAIResponseChunk struct {
	Type      string                `json:"type"`
	Object    string                `json:"object"`
	Status    string                `json:"status"`
	Output    json.RawMessage       `json:"output"`
	Item      json.RawMessage       `json:"item"`
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
	"response.created":                       true,
	"response.in_progress":                   true,
	"response.completed":                     true,
	"response.incomplete":                    true,
	"response.failed":                        true,
	"response.output_item.added":             true,
	"response.output_item.done":              true,
	"response.output_text.delta":             true,
	"response.output_text.done":              true,
	"response.function_call_arguments.delta": true,
	"response.function_call_arguments.done":  true,
}

// emptyCompletionAccum accumulates the properties relevant to deciding whether
// an OpenAI-, Claude-, or Gemini-style completion is empty.
type emptyCompletionAccum struct {
	recognized       bool
	sawUnknownData   bool
	terminal         bool
	hasContent       bool
	hasToolCalls     bool
	completionTokens int
	sawUsage         bool
	blocked          bool
}

func (a *emptyCompletionAccum) evalJSON(data []byte) bool {
	values, err := decodeJSONValues(data)
	if err != nil {
		return false
	}
	recognized := false
	for _, v := range values {
		if a.evalOpenAI(v) || a.evalClaude(v) || a.evalOpenAIResponse(v) || a.evalGemini(v) {
			recognized = true
		}
	}
	return recognized
}

// decodeJSONValues decodes every top-level JSON value in payload with the
// stdlib decoder until io.EOF, supporting pretty JSON, NDJSON, whitespace
// separated, and directly concatenated values. It requires at least one value
// and a clean EOF; malformed or trailing garbage returns an error.
func decodeJSONValues(payload []byte) ([]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	var values []json.RawMessage
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		values = append(values, raw)
	}
	if len(values) == 0 {
		return nil, io.EOF
	}
	return values, nil
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
		a.addUsage(*chunk.Usage.CompletionTokens)
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
		if ch.Delta.Refusal != nil || ch.Message.Refusal != nil {
			a.hasContent = true
		}
		if len(ch.Delta.ToolCalls) > 0 || len(ch.Message.ToolCalls) > 0 {
			a.hasToolCalls = true
		}
		if nonEmptyFunctionCall(ch.Delta.FunctionCall) || nonEmptyFunctionCall(ch.Message.FunctionCall) {
			a.hasToolCalls = true
		}
		if nonEmptyAudioPayload(ch.Delta.Audio) || nonEmptyAudioPayload(ch.Message.Audio) {
			a.hasContent = true
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

	a.evalClaudeStopReason(chunk.StopReason)
	if chunk.Message != nil {
		a.evalClaudeStopReason(chunk.Message.StopReason)
	}
	if chunk.Delta != nil {
		a.evalClaudeStopReason(chunk.Delta.StopReason)
	}

	if chunk.Usage != nil && chunk.Usage.OutputTokens != nil {
		a.sawUsage = true
		a.addUsage(*chunk.Usage.OutputTokens)
	}
	if chunk.Message != nil && chunk.Message.Usage != nil && chunk.Message.Usage.OutputTokens != nil {
		a.sawUsage = true
		a.addUsage(*chunk.Message.Usage.OutputTokens)
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

func (a *emptyCompletionAccum) evalClaudeStopReason(stopReason *string) {
	if stopReason == nil {
		return
	}
	reason := strings.TrimSpace(*stopReason)
	if strings.EqualFold(reason, "end_turn") {
		a.terminal = true
	} else if reason != "" {
		// Request/output limits, refusals, and control stop reasons must reach the
		// client instead of being converted into a credential failure.
		a.blocked = true
	}
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
	case "response.completed":
		a.terminal = true
	case "response.incomplete", "response.failed":
		a.terminal = true
		a.blocked = true
	}
	a.evalOpenAIResponseStatus(chunk.Status)
	if chunk.Response != nil {
		a.evalOpenAIResponseStatus(chunk.Response.Status)
	}

	if chunk.Usage != nil && chunk.Usage.OutputTokens != nil {
		a.sawUsage = true
		a.addUsage(*chunk.Usage.OutputTokens)
	}
	if chunk.Response != nil && chunk.Response.Usage != nil && chunk.Response.Usage.OutputTokens != nil {
		a.sawUsage = true
		a.addUsage(*chunk.Response.Usage.OutputTokens)
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
	a.evalOpenAIResponseRawOutput(chunk.Item)
	if chunk.Response != nil {
		a.evalOpenAIResponseRawOutput(chunk.Response.Output)
	}

	return true
}

func (a *emptyCompletionAccum) evalOpenAIResponseStatus(status string) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		a.terminal = true
	case "incomplete", "failed":
		a.terminal = true
		a.blocked = true
	}
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
		itemType := strings.ToLower(strings.TrimSpace(item.Type))
		switch {
		case itemType == "image_generation_call":
			a.hasContent = true
		case strings.HasSuffix(itemType, "_call"):
			a.hasToolCalls = true
		case itemType != "" && itemType != "message":
			// Responses may add output item types over time. A complete, typed
			// non-message item is output unless the protocol proves otherwise.
			a.hasContent = true
		}
		if strings.TrimSpace(item.Text) != "" {
			a.hasContent = true
		}
		for _, part := range item.Content {
			partType := strings.ToLower(strings.TrimSpace(part.Type))
			if strings.TrimSpace(part.Text) != "" || strings.TrimSpace(part.Refusal) != "" ||
				partType == "refusal" || (partType != "" && partType != "output_text" && partType != "text") {
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
	promptFeedback := chunk.PromptFeedback

	if chunk.Response != nil {
		if len(candidates) == 0 {
			candidates = chunk.Response.Candidates
		}
		if usage == nil {
			usage = chunk.Response.UsageMetadata
		}
		if promptFeedback == nil {
			promptFeedback = chunk.Response.PromptFeedback
		}
	}
	promptBlocked := promptFeedback != nil && strings.TrimSpace(promptFeedback.BlockReason) != ""

	if len(candidates) == 0 {
		// Only treat an empty candidates array as a recognized empty completion
		// when the candidates key is actually present (e.g. a Gemini
		// safety/empty response with zero candidate tokens, or a stream
		// aggregate with nothing else). An absent candidates key is not a
		// Gemini shape at all.
		if hasJSONKey(data, "candidates") || hasNestedResponseCandidates(data) {
			a.recognized = true
			a.terminal = true
			a.blocked = promptBlocked
			if usage != nil && usage.CandidatesTokenCount != nil {
				a.sawUsage = true
				a.addUsage(*usage.CandidatesTokenCount)
			}
			return true
		}
		return false
	}

	a.recognized = true
	if promptBlocked {
		a.blocked = true
	}

	if usage != nil {
		if usage.CandidatesTokenCount != nil {
			a.sawUsage = true
			a.addUsage(*usage.CandidatesTokenCount)
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
				if nonEmptyJSONPayload(part.ExecutableCode) || nonEmptyJSONPayload(part.CodeExecutionResult) {
					a.hasContent = true
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
	if !a.recognized || a.sawUnknownData || !a.terminal {
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

func isEmptyCompletionError(err error) bool {
	var authErr *Error
	return errors.As(err, &authErr) && authErr != nil && authErr.Code == errEmptyCompletion.Code
}

// streamBootstrapState incrementally evaluates chunks so a metadata-heavy
// prefix is processed once instead of reparsing the entire prefix per chunk.
type streamBootstrapState struct {
	acc     emptyCompletionAccum
	bytes   int
	pending []byte
	forward bool
	sawSSE  bool
}

func (s *streamBootstrapState) observe(fragment []byte) bool {
	if s.forward {
		return true
	}
	s.bytes += len(fragment)
	if s.bytes > maxStreamBootstrapBytes {
		s.forward = true
		return true
	}
	s.pending = append(s.pending, fragment...)
	for {
		if newline := bytes.IndexByte(s.pending, '\n'); newline >= 0 {
			line := bytes.TrimSpace(s.pending[:newline])
			s.pending = s.pending[newline+1:]
			if len(line) > 0 {
				switch {
				case bytes.HasPrefix(line, []byte("event:")), bytes.HasPrefix(line, []byte("data:")), bytes.HasPrefix(line, []byte(":")):
					s.sawSSE = true
					s.acc.evalSSE(line)
				default:
					s.acc.sawUnknownData = true
				}
			}
			if s.shouldForward() {
				s.forward = true
				return true
			}
			continue
		}
		break
	}

	trimmed := bytes.TrimSpace(s.pending)
	if len(trimmed) == 0 || couldBeSSEPrefix(trimmed) {
		return false
	}
	switch classifyJSONBuffer(trimmed) {
	case jsonBufComplete:
		if !s.acc.evalJSON(trimmed) {
			s.acc.sawUnknownData = true
		}
		s.pending = s.pending[:0]
	case jsonBufEmpty, jsonBufIncomplete:
		return false
	case jsonBufInvalid:
		s.acc.sawUnknownData = true
	}
	s.forward = s.shouldForward()
	return s.forward
}

func (s *streamBootstrapState) shouldForward() bool {
	return s.acc.hasContent || s.acc.hasToolCalls || s.acc.sawUnknownData || (!s.acc.recognized && !s.sawSSE)
}

type jsonBufferStatus int

const (
	jsonBufEmpty jsonBufferStatus = iota
	jsonBufComplete
	jsonBufIncomplete
	jsonBufInvalid
)

// classifyJSONBuffer classifies an accumulated raw-JSON stream tail as holding
// one or more complete values (jsonBufComplete), a truncated prefix of a value
// (jsonBufIncomplete), malformed or trailing garbage (jsonBufInvalid), or no
// value (jsonBufEmpty). It inspects only the given buffer, so it can be called
// again on each growing chunk without keeping a persistent decoder.
func classifyJSONBuffer(buf []byte) jsonBufferStatus {
	if hasTruncatedUTF8Suffix(buf) {
		return jsonBufIncomplete
	}
	dec := json.NewDecoder(bytes.NewReader(buf))
	count := 0
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				if count == 0 {
					return jsonBufEmpty
				}
				return jsonBufComplete
			}
			if isTruncatedJSON(err) {
				return jsonBufIncomplete
			}
			return jsonBufInvalid
		}
		count++
	}
}

// isTruncatedJSON reports whether a json decoding error is caused by the input
// ending mid-value (a truncated prefix) rather than by malformed contents.
func isTruncatedJSON(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var syn *json.SyntaxError
	if errors.As(err, &syn) {
		return strings.Contains(syn.Error(), "unexpected end of JSON input")
	}
	return false
}

// hasTruncatedUTF8Suffix reports whether buf ends in the middle of a multi-byte
// UTF-8 sequence, which happens when a raw JSON value is split at a chunk
// boundary inside a string literal.
func hasTruncatedUTF8Suffix(buf []byte) bool {
	n := len(buf)
	if n == 0 {
		return false
	}
	i := n - 1
	for i >= 0 && buf[i]&0xC0 == 0x80 {
		i--
	}
	if i < 0 {
		return false
	}
	lead := buf[i]
	var need int
	switch {
	case lead&0xE0 == 0xC0:
		need = 1
	case lead&0xF0 == 0xE0:
		need = 2
	case lead&0xF8 == 0xF0:
		need = 3
	default:
		return false
	}
	return n-i-1 < need
}

func couldBeSSEPrefix(payload []byte) bool {
	const dataPrefix = "data:"
	const eventPrefix = "event:"
	value := string(payload)
	return strings.HasPrefix(value, ":") || strings.HasPrefix(dataPrefix, value) || strings.HasPrefix(eventPrefix, value) ||
		strings.HasPrefix(value, dataPrefix) || strings.HasPrefix(value, eventPrefix)
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
		if !a.evalJSON(data) {
			a.sawUnknownData = true
		}
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
