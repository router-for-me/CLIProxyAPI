package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
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

// openAIChunk is the minimal OpenAI-style SSE/JSON shape used to detect empty
// completions.
type openAIChunk struct {
	Choices []struct {
		Index *int   `json:"index"`
		Text  string `json:"text"`
		Delta struct {
			Content          string            `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			Reasoning        string            `json:"reasoning"`
			Refusal          *string           `json:"refusal"`
			ToolCalls        []json.RawMessage `json:"tool_calls"`
			FunctionCall     json.RawMessage   `json:"function_call"`
			Audio            json.RawMessage   `json:"audio"`
			Images           []json.RawMessage `json:"images"`
		} `json:"delta"`
		Message struct {
			Content          string            `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			Reasoning        string            `json:"reasoning"`
			Refusal          *string           `json:"refusal"`
			ToolCalls        []json.RawMessage `json:"tool_calls"`
			FunctionCall     json.RawMessage   `json:"function_call"`
			Audio            json.RawMessage   `json:"audio"`
			Images           []json.RawMessage `json:"images"`
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
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var val any
	if err := json.Unmarshal(trimmed, &val); err != nil {
		return false
	}
	switch v := val.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(v) != ""
	case map[string]any:
		return len(v) > 0
	case []any:
		return len(v) > 0
	default:
		return true
	}
}

func hasMeaningfulJSONArguments(args string) bool {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" || trimmed == "null" {
		return false
	}
	var val any
	if err := json.Unmarshal([]byte(trimmed), &val); err == nil {
		switch v := val.(type) {
		case nil:
			return false
		case string:
			return strings.TrimSpace(v) != ""
		case map[string]any:
			return len(v) > 0
		case []any:
			return len(v) > 0
		default:
			return true
		}
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
	return strings.TrimSpace(fc.Name) != "" || hasMeaningfulJSONArguments(fc.Arguments)
}

func hasMeaningfulImages(rawImages []json.RawMessage) bool {
	for _, raw := range rawImages {
		if nonEmptyJSONPayload(raw) {
			return true
		}
	}
	return false
}

func hasMeaningfulToolCalls(rawCalls []json.RawMessage) bool {
	for _, raw := range rawCalls {
		if isMeaningfulToolCall(raw) {
			return true
		}
	}
	return false
}

func isMeaningfulToolCall(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var call struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Custom    json.RawMessage `json:"custom"`
	}
	if err := json.Unmarshal(trimmed, &call); err != nil {
		var m map[string]any
		if err := json.Unmarshal(trimmed, &m); err == nil && len(m) > 0 {
			for _, v := range m {
				if v != nil && v != "" {
					return true
				}
			}
		}
		return false
	}
	if strings.TrimSpace(call.Function.Name) != "" || hasMeaningfulJSONArguments(call.Function.Arguments) {
		return true
	}
	if strings.TrimSpace(call.Name) != "" || hasMeaningfulJSONArguments(call.Arguments) {
		return true
	}
	if nonEmptyJSONPayload(call.Custom) {
		return true
	}
	return false
}

// emptyCompletionAccum accumulates the properties relevant to deciding whether
// an OpenAI-style completion is empty. Later slices add Claude/Gemini/Responses
// evaluators onto the same accum; unused format flags are omitted here.
type emptyCompletionAccum struct {
	expectedChoices       int
	recognized            bool
	sawUnknownData        bool
	terminal              bool
	hasContent            bool
	hasToolCalls          bool
	completionTokens      int
	sawUsage              bool
	blocked               bool
	sawMetadataOnly       bool
	sawMessageData        bool
	openAITerminal        bool
	openAIChoicesSeen     map[int]bool
	openAIChoicesFinished map[int]bool
}

func (a *emptyCompletionAccum) evalJSON(data []byte) bool {
	values, err := decodeJSONValues(data)
	if err != nil {
		return false
	}
	recognized := false
	for _, v := range values {
		if a.evalOpenAI(v) {
			recognized = true
		} else {
			a.sawUnknownData = true
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
	if !hasJSONKey(data, "choices") {
		return false
	}
	a.recognized = true
	a.sawMessageData = true
	var chunk openAIChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		// A recognized choices-bearing payload whose shape does not decode
		// (for example message.content as an array of content parts) carries
		// forward-compatible output we cannot inspect. Treat it as unknown
		// data so it passes through instead of being misjudged as an empty
		// completion.
		a.sawUnknownData = true
		return true
	}
	if chunk.Usage != nil && chunk.Usage.CompletionTokens != nil {
		a.sawUsage = true
		a.addUsage(*chunk.Usage.CompletionTokens)
	}
	if a.openAIChoicesSeen == nil {
		a.openAIChoicesSeen = make(map[int]bool)
		a.openAIChoicesFinished = make(map[int]bool)
	}
	for i, ch := range chunk.Choices {
		idx := i
		if ch.Index != nil {
			idx = *ch.Index
		}
		a.openAIChoicesSeen[idx] = true
		if ch.FinishReason != nil {
			reason := strings.TrimSpace(*ch.FinishReason)
			if strings.EqualFold(reason, "stop") || strings.EqualFold(reason, "tool_calls") || strings.EqualFold(reason, "function_call") {
				a.openAIChoicesFinished[idx] = true
				a.terminal = true
			} else if reason != "" {
				// content_filter, length, and other non-stop terminal reasons
				// are not empty completions: the client must see the reason
				// rather than a silent auth rotation.
				a.blocked = true
				a.terminal = true
			}
		}
		content := ch.Text + ch.Delta.Content + ch.Message.Content + ch.Delta.ReasoningContent + ch.Message.ReasoningContent + ch.Delta.Reasoning + ch.Message.Reasoning
		if strings.TrimSpace(content) != "" {
			a.hasContent = true
		}
		if (ch.Delta.Refusal != nil && strings.TrimSpace(*ch.Delta.Refusal) != "") ||
			(ch.Message.Refusal != nil && strings.TrimSpace(*ch.Message.Refusal) != "") {
			a.hasContent = true
		}
		if hasMeaningfulToolCalls(ch.Delta.ToolCalls) || hasMeaningfulToolCalls(ch.Message.ToolCalls) {
			a.hasToolCalls = true
		}
		if nonEmptyFunctionCall(ch.Delta.FunctionCall) || nonEmptyFunctionCall(ch.Message.FunctionCall) {
			a.hasToolCalls = true
		}
		if nonEmptyAudioPayload(ch.Delta.Audio) || nonEmptyAudioPayload(ch.Message.Audio) {
			a.hasContent = true
		}
		if hasMeaningfulImages(ch.Delta.Images) || hasMeaningfulImages(ch.Message.Images) {
			a.hasContent = true
		}
	}
	expected := a.expectedChoices
	if expected <= 0 {
		expected = 1
	}
	targetChoices := expected
	if len(a.openAIChoicesSeen) > targetChoices {
		targetChoices = len(a.openAIChoicesSeen)
	}
	if len(a.openAIChoicesFinished) >= targetChoices && len(a.openAIChoicesFinished) >= len(a.openAIChoicesSeen) && !a.blocked {
		a.openAITerminal = true
	} else {
		a.openAITerminal = false
	}
	if len(chunk.Choices) == 0 && chunk.Usage != nil {
		// A completed non-streaming payload with zero choices
		// ({"choices":[], "usage":...}) never enters the loop above, so
		// terminal would never be set and the payload would be accepted as a
		// successful response. With usage present the response is complete, so
		// the empty judgment can run. (Streamed zero-choices chunks without
		// usage are mid-stream signals and must not mark terminal here.)
		a.terminal = true
	}
	return true
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

// empty reports whether the accumulated stream is an empty completion.
func (a *emptyCompletionAccum) empty() bool {
	if a.sawUnknownData || a.blocked || a.hasContent || a.hasToolCalls || (a.sawUsage && a.completionTokens > 0) {
		return false
	}
	if a.recognized && a.terminal {
		return true
	}
	if a.recognized {
		return true
	}
	if a.sawMetadataOnly && !a.sawMessageData {
		return true
	}
	return false
}

// isEmptyCompletionPayload reports whether a payload (aggregated SSE chunks or
// a single non-stream JSON response) represents an empty completion.
func isEmptyCompletionPayload(payload []byte) bool {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		// A zero-length or whitespace-only body on an HTTP success is the
		// canonical empty completion: without this, Execute and plugin
		// executors returned it as a successful response and never rotated
		// credentials. A literal JSON null is equally empty.
		return true
	}

	var jsonAcc emptyCompletionAccum
	if jsonAcc.evalJSON(trimmed) {
		var probe struct {
			Choices json.RawMessage `json:"choices"`
		}
		if json.Unmarshal(trimmed, &probe) == nil && probe.Choices != nil {
			jsonAcc.terminal = true
		}
		return jsonAcc.empty()
	}

	var acc emptyCompletionAccum

	if isSSEPayload(trimmed) {
		acc.evalSSE(trimmed)
		return acc.empty()
	}

	acc.evalJSON(trimmed)
	// A complete non-SSE OpenAI chat completion body is terminal by
	// construction: zero-choice payloads such as {"choices":[]} or
	// {"choices":[],"usage":null} never enter the per-choice terminal paths,
	// so without this they would be accepted as successful responses instead
	// of being judged as empty completions. Other recognized shapes (for
	// example Claude messages) keep their per-shape terminal rules.
	var probe struct {
		Choices json.RawMessage `json:"choices"`
	}
	if json.Unmarshal(trimmed, &probe) == nil && probe.Choices != nil {
		acc.terminal = true
	}
	return acc.empty()
}

func isSSEPayload(trimmed []byte) bool {
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if isSSEPrefix(line) {
			return true
		}
	}
	return false
}

func parseSSEDataLine(line []byte) []byte {
	data := bytes.TrimPrefix(line, []byte("data:"))
	if len(data) > 0 && data[0] == ' ' {
		data = data[1:]
	}
	return data
}

func isSSEPrefix(b []byte) bool {
	return bytes.HasPrefix(b, []byte("data:")) ||
		bytes.HasPrefix(b, []byte("event:")) ||
		bytes.HasPrefix(b, []byte("id:")) ||
		bytes.HasPrefix(b, []byte("retry:")) ||
		bytes.HasPrefix(b, []byte(":")) ||
		bytes.Equal(b, []byte("data")) ||
		bytes.Equal(b, []byte("event")) ||
		bytes.Equal(b, []byte("id")) ||
		bytes.Equal(b, []byte("retry"))
}

func (a *emptyCompletionAccum) evalSSE(payload []byte) {
	var dataLines [][]byte
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		data := bytes.Join(dataLines, []byte("\n"))
		dataLines = dataLines[:0]
		if bytes.Equal(data, []byte("[DONE]")) {
			a.recognized = true
			a.terminal = true
			a.sawMessageData = true
			return
		}
		if len(data) == 0 {
			a.sawMetadataOnly = true
			return
		}
		if !a.evalJSON(data) {
			a.sawUnknownData = true
		}
	}

	processSingle := func(line []byte) {
		if bytes.HasPrefix(line, []byte("event:")) {
			event := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("event:")))
			if bytes.Equal(event, []byte("message_stop")) {
				a.recognized = true
				a.terminal = true
				a.sawMessageData = true
			} else {
				a.sawMetadataOnly = true
			}
			return
		}
		if bytes.Equal(line, []byte("event")) {
			a.sawMetadataOnly = true
			return
		}
		if bytes.HasPrefix(line, []byte("id:")) || bytes.HasPrefix(line, []byte("retry:")) || bytes.HasPrefix(line, []byte(":")) {
			a.sawMetadataOnly = true
			return
		}
		if bytes.Equal(line, []byte("id")) || bytes.Equal(line, []byte("retry")) {
			a.sawMetadataOnly = true
			return
		}
		switch {
		case bytes.HasPrefix(line, []byte("data:")):
			dataLines = append(dataLines, parseSSEDataLine(line))
		case bytes.Equal(line, []byte("data")):
			dataLines = append(dataLines, []byte(""))
		case bytes.HasPrefix(line, []byte("{")), bytes.HasPrefix(line, []byte("[")):
			// Some executors translate upstream SSE into the client format and
			// emit raw JSON payloads without SSE framing (the HTTP handler adds
			// the data: prefix later). Treat bare JSON lines as chunk data.
			dataLines = append(dataLines, line)
		default:
			a.sawUnknownData = true
		}
	}

	processLine := func(line []byte) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			flush()
			return
		}
		processSingle(line)
	}

	for _, line := range bytes.Split(payload, []byte("\n")) {
		processLine(line)
	}
	flush()
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
