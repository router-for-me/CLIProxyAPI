package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
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

// errEmptyCount indicates the upstream returned an empty count response. It is
// retriable so the conductor marks the auth as failed, cools it down, and
// rotates to the next auth/model.
var errEmptyCount = &Error{
	Code:       "empty_count",
	Message:    "upstream returned an empty count response",
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

func hasMeaningfulClaudePartialJSON(partial string) bool {
	return hasMeaningfulJSONArguments(partial)
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

// hasMeaningfulGeminiMediaPayload reports whether a Gemini media part contains usable content.
// Matching the translator semantics, inlineData counts only when data is non-blank and fileData
// only when fileUri is non-blank; a scaffold object with only a mimeType carries no media.
func hasMeaningfulGeminiMediaPayload(inlineData, fileData json.RawMessage) bool {
	if len(bytes.TrimSpace(inlineData)) > 0 {
		var v struct {
			Data string `json:"data"`
		}
		if json.Unmarshal(inlineData, &v) == nil && strings.TrimSpace(v.Data) != "" {
			return true
		}
	}
	if len(bytes.TrimSpace(fileData)) > 0 {
		var v struct {
			FileURI string `json:"fileUri"`
		}
		if json.Unmarshal(fileData, &v) == nil && strings.TrimSpace(v.FileURI) != "" {
			return true
		}
	}
	return false
}

func isMeaningfulGeminiFunctionCall(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var call struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(trimmed, &call); err != nil {
		return false
	}
	if strings.TrimSpace(call.Name) != "" {
		return true
	}
	return nonEmptyJSONPayload(call.Args)
}

type geminiGroundingChunk struct {
	Web *struct {
		URI   string `json:"uri"`
		Title string `json:"title"`
	} `json:"web"`
	RetrievedContext *struct {
		URI   string `json:"uri"`
		Title string `json:"title"`
		Text  string `json:"text"`
	} `json:"retrievedContext"`
}

type geminiGroundingMetadata struct {
	WebSearchQueries []string               `json:"webSearchQueries"`
	GroundingChunks  []geminiGroundingChunk `json:"groundingChunks"`
	SearchEntryPoint *struct {
		RenderedContent string `json:"renderedContent"`
	} `json:"searchEntryPoint"`
	RetrievalQueries []string `json:"retrievalQueries"`
}

func hasMeaningfulGroundingMetadata(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) || bytes.Equal(trimmed, []byte("[]")) {
		return false
	}
	var meta geminiGroundingMetadata
	if err := json.Unmarshal(trimmed, &meta); err == nil {
		for _, q := range meta.WebSearchQueries {
			if strings.TrimSpace(q) != "" {
				return true
			}
		}
		for _, q := range meta.RetrievalQueries {
			if strings.TrimSpace(q) != "" {
				return true
			}
		}
		for _, chunk := range meta.GroundingChunks {
			if chunk.Web != nil {
				if strings.TrimSpace(chunk.Web.URI) != "" || strings.TrimSpace(chunk.Web.Title) != "" {
					return true
				}
			}
			if chunk.RetrievedContext != nil {
				if strings.TrimSpace(chunk.RetrievedContext.URI) != "" ||
					strings.TrimSpace(chunk.RetrievedContext.Title) != "" ||
					strings.TrimSpace(chunk.RetrievedContext.Text) != "" {
					return true
				}
			}
		}
		if meta.SearchEntryPoint != nil && strings.TrimSpace(meta.SearchEntryPoint.RenderedContent) != "" {
			return true
		}
	}
	var generic map[string]any
	if err := json.Unmarshal(trimmed, &generic); err == nil {
		for k, v := range generic {
			if k == "groundingChunks" || k == "webSearchQueries" || k == "retrievalQueries" || k == "searchEntryPoint" {
				continue
			}
			switch val := v.(type) {
			case nil:
			case string:
				if strings.TrimSpace(val) != "" {
					return true
				}
			case map[string]any:
				if len(val) > 0 {
					for _, mv := range val {
						if s, ok := mv.(string); ok && strings.TrimSpace(s) != "" {
							return true
						}
					}
				}
			case []any:
				if len(val) > 0 {
					for _, ev := range val {
						if s, ok := ev.(string); ok && strings.TrimSpace(s) != "" {
							return true
						}
					}
				}
			default:
				return true
			}
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

type claudeContentBlock struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	Data      string          `json:"data"`
	Input     json.RawMessage `json:"input"`
	Citation  json.RawMessage `json:"citation"`
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
		Type        string          `json:"type"`
		Text        string          `json:"text"`
		Thinking    string          `json:"thinking"`
		Signature   string          `json:"signature"`
		Citation    json.RawMessage `json:"citation"`
		PartialJSON string          `json:"partial_json"`
		StopReason  *string         `json:"stop_reason"`
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
	ThoughtSignature    string          `json:"thoughtSignature"`
	Thought_Signature   string          `json:"thought_signature"`
}

type geminiCandidate struct {
	Index   *int `json:"index"`
	Content *struct {
		Parts []geminiPart `json:"parts"`
	} `json:"content"`
	FinishReason      *string         `json:"finishReason"`
	GroundingMetadata json.RawMessage `json:"groundingMetadata"`
}

type geminiUsageMetadata struct {
	CandidatesTokenCount *tokenCount `json:"candidatesTokenCount"`
}

type geminiPromptFeedback struct {
	BlockReason string `json:"blockReason"`
}

type geminiChunk struct {
	Candidates        []geminiCandidate     `json:"candidates"`
	UsageMetadata     *geminiUsageMetadata  `json:"usageMetadata"`
	PromptFeedback    *geminiPromptFeedback `json:"promptFeedback"`
	GroundingMetadata json.RawMessage       `json:"groundingMetadata"`
	Response          *struct {
		Candidates        []geminiCandidate     `json:"candidates"`
		UsageMetadata     *geminiUsageMetadata  `json:"usageMetadata"`
		PromptFeedback    *geminiPromptFeedback `json:"promptFeedback"`
		GroundingMetadata json.RawMessage       `json:"groundingMetadata"`
	} `json:"response"`
}

// openAIResponseUsage is the usage block of the OpenAI Responses-API shape
// (used by codex/xai executors).
type openAIResponseUsage struct {
	OutputTokens *tokenCount `json:"output_tokens"`
}

type openAIResponseContentPart struct {
	Type        string            `json:"type"`
	Text        string            `json:"text"`
	Refusal     string            `json:"refusal"`
	Annotations []json.RawMessage `json:"annotations"`
}

type openAIResponseOutputItem struct {
	ID               string                      `json:"id"`
	CallID           string                      `json:"call_id"`
	Name             string                      `json:"name"`
	Input            string                      `json:"input"`
	Type             string                      `json:"type"`
	Text             string                      `json:"text"`
	Arguments        string                      `json:"arguments"`
	Result           string                      `json:"result"`
	Action           json.RawMessage             `json:"action"`
	Results          json.RawMessage             `json:"results"`
	Content          []openAIResponseContentPart `json:"content"`
	EncryptedContent string                      `json:"encrypted_content"`
	Summary          json.RawMessage             `json:"summary"`
}

type openAIResponseObject struct {
	Status string               `json:"status"`
	Output json.RawMessage      `json:"output"`
	Usage  *openAIResponseUsage `json:"usage"`
}

type openAIResponsePart struct {
	Type             string            `json:"type"`
	Text             string            `json:"text"`
	EncryptedContent string            `json:"encrypted_content"`
	Annotations      []json.RawMessage `json:"annotations"`
}

type openAIResponseChunk struct {
	Type      string                `json:"type"`
	Object    string                `json:"object"`
	Status    string                `json:"status"`
	Output    json.RawMessage       `json:"output"`
	Item      json.RawMessage       `json:"item"`
	Part      json.RawMessage       `json:"part"`
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
	"response.content_part.added":            true,
	"response.content_part.done":             true,
	"response.output_text.delta":             true,
	"response.output_text.done":              true,
	"response.reasoning_summary_part.added":  true,
	"response.reasoning_summary_part.done":   true,
	"response.reasoning_summary_text.delta":  true,
	"response.reasoning_summary_text.done":   true,
	"response.reasoning_text.delta":          true,
	"response.reasoning_text.done":           true,
	"response.function_call_arguments.delta": true,
	"response.function_call_arguments.done":  true,
	"response.web_search_call.in_progress":   true,
	"response.web_search_call.searching":     true,
	"response.web_search_call.completed":     true,
	"error":                                  true,
	"codex.rate_limits":                      true,
	"codex.response.metadata":                true,
}

var interactionsEventTypes = map[string]bool{
	"interaction.created":       true,
	"interaction.status_update": true,
	"interaction.completed":     true,
	"interaction.failed":        true,
	"finish":                    true,
	"step.start":                true,
	"step.delta":                true,
	"step.stop":                 true,
}

type interactionsChunk struct {
	Object        string             `json:"object"`
	EventType     string             `json:"event_type"`
	Type          string             `json:"type"`
	Status        string             `json:"status"`
	InteractionID string             `json:"interaction_id"`
	Steps         []interactionsStep `json:"steps"`
	Step          *interactionsStep  `json:"step"`
	Delta         *interactionsDelta `json:"delta"`
	Usage         *interactionsUsage `json:"usage"`
	Metadata      *interactionsMeta  `json:"metadata"`
	Interaction   *struct {
		ID     string             `json:"id"`
		Status string             `json:"status"`
		Object string             `json:"object"`
		Steps  []interactionsStep `json:"steps"`
		Usage  *interactionsUsage `json:"usage"`
	} `json:"interaction"`
}

type interactionsMeta struct {
	TotalUsage *interactionsUsage `json:"total_usage"`
	Usage      *interactionsUsage `json:"usage"`
}

type interactionsUsage struct {
	OutputTokens      *tokenCount `json:"output_tokens"`
	TotalOutputTokens *tokenCount `json:"total_output_tokens"`
	CompletionTokens  *tokenCount `json:"completion_tokens"`
}

type interactionsStep struct {
	ID                    string                    `json:"id"`
	CallID                string                    `json:"call_id"`
	Type                  string                    `json:"type"`
	Name                  string                    `json:"name"`
	Arguments             json.RawMessage           `json:"arguments"`
	Content               []interactionsContent     `json:"content"`
	Result                json.RawMessage           `json:"result"`
	Signature             string                    `json:"signature"`
	ThoughtSignature      string                    `json:"thought_signature"`
	ThoughtSignatureCamel string                    `json:"thoughtSignature"`
	EncryptedContent      string                    `json:"encrypted_content"`
	ExtraContent          *interactionsExtraContent `json:"extra_content"`
}

// interactionsExtraContent carries the vendor-specific envelope Gemini uses to
// ship a thought signature alongside a step.
type interactionsExtraContent struct {
	Google *struct {
		ThoughtSignature string `json:"thought_signature"`
	} `json:"google"`
}

// hasSignature reports whether the step carries a reasoning signature.
// A signature without text, a tool-call, or positive completion tokens is not
// content: treating it as live would keep a dead Gemini/Claude credential in
// the pool.
func (s *interactionsStep) hasSignature() bool {
	if s == nil {
		return false
	}
	if strings.TrimSpace(s.Signature) != "" ||
		strings.TrimSpace(s.ThoughtSignature) != "" ||
		strings.TrimSpace(s.ThoughtSignatureCamel) != "" ||
		strings.TrimSpace(s.EncryptedContent) != "" {
		return true
	}
	if s.ExtraContent != nil && s.ExtraContent.Google != nil {
		return strings.TrimSpace(s.ExtraContent.Google.ThoughtSignature) != ""
	}
	return false
}

type interactionsContent struct {
	Type                  string `json:"type"`
	Text                  string `json:"text"`
	Data                  string `json:"data"`
	FileURI               string `json:"file_uri"`
	FileUri               string `json:"fileUri"`
	URL                   string `json:"url"`
	MimeType              string `json:"mime_type"`
	Mime_Type             string `json:"mimeType"`
	Signature             string `json:"signature"`
	ThoughtSignature      string `json:"thought_signature"`
	ThoughtSignatureCamel string `json:"thoughtSignature"`
}

func (c *interactionsContent) hasMeaningfulContent() bool {
	if c == nil {
		return false
	}
	return strings.TrimSpace(c.Text) != "" ||
		strings.TrimSpace(c.Data) != "" ||
		strings.TrimSpace(c.FileURI) != "" ||
		strings.TrimSpace(c.FileUri) != "" ||
		strings.TrimSpace(c.URL) != ""
}

type interactionsDelta struct {
	Type                  string               `json:"type"`
	Text                  string               `json:"text"`
	Data                  string               `json:"data"`
	FileURI               string               `json:"file_uri"`
	FileUri               string               `json:"fileUri"`
	URL                   string               `json:"url"`
	Arguments             json.RawMessage      `json:"arguments"`
	Signature             string               `json:"signature"`
	ThoughtSignature      string               `json:"thought_signature"`
	ThoughtSignatureCamel string               `json:"thoughtSignature"`
	Name                  string               `json:"name"`
	Content               *interactionsContent `json:"content"`
	Result                json.RawMessage      `json:"result"`
}

func hasMeaningfulInteractionsArguments(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var str string
	if err := json.Unmarshal(trimmed, &str); err == nil {
		return hasMeaningfulJSONArguments(str)
	}
	return nonEmptyJSONPayload(raw)
}

// emptyCompletionAccum accumulates the properties relevant to deciding whether
// an OpenAI-, Claude-, or Gemini-style completion is empty.
type emptyCompletionAccum struct {
	expectedChoices          int
	recognized               bool
	sawUnknownData           bool
	terminal                 bool
	hasContent               bool
	hasToolCalls             bool
	completionTokens         int
	sawUsage                 bool
	blocked                  bool
	sawMetadataOnly          bool
	sawMessageData           bool
	geminiTerminal           bool
	claudeTerminal           bool
	openAITerminal           bool
	interactionsTerminal     bool
	openAIChoicesSeen        map[int]bool
	openAIChoicesFinished    map[int]bool
	geminiCandidatesSeen     map[int]bool
	geminiCandidatesFinished map[int]bool
}

func (a *emptyCompletionAccum) evalJSON(data []byte) bool {
	values, err := decodeJSONValues(data)
	if err != nil {
		return false
	}
	recognized := false
	for _, v := range values {
		if evalProviderError(v, "") != nil {
			recognized = true
			a.blocked = true
			a.terminal = true
		} else if a.evalOpenAI(v) || a.evalClaude(v) || a.evalOpenAIResponse(v) || a.evalGemini(v) || a.evalInteractions(v) {
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

func (a *emptyCompletionAccum) evalClaude(data []byte) bool {
	var chunk claudeChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return false
	}

	isClaude := false
	switch chunk.Type {
	case "message", "message_start", "content_block_start", "content_block_delta", "message_delta", "message_stop", "ping":
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
	if chunk.Type == "ping" {
		a.sawMetadataOnly = true
	} else {
		a.sawMessageData = true
	}
	if chunk.Type == "message_stop" {
		a.terminal = true
		a.claudeTerminal = true
	}

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
		case "thinking_delta":
			if strings.TrimSpace(chunk.Delta.Thinking) != "" {
				a.hasContent = true
			}
		case "signature_delta":
			// A lonely signature is not visible content and does not spend
			// completion tokens; do not treat it as a live completion.
		case "citations_delta":
			if nonEmptyJSONPayload(chunk.Delta.Citation) {
				a.hasContent = true
			}
		case "input_json_delta":
			if hasMeaningfulClaudePartialJSON(chunk.Delta.PartialJSON) {
				a.hasToolCalls = true
			}
		default:
			if strings.TrimSpace(chunk.Delta.Text) != "" || strings.TrimSpace(chunk.Delta.Thinking) != "" || nonEmptyJSONPayload(chunk.Delta.Citation) {
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
	if strings.EqualFold(reason, "end_turn") || strings.EqualFold(reason, "tool_use") {
		a.terminal = true
		a.claudeTerminal = true
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
	a.sawMessageData = true

	var chunk openAIResponseChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return true
	}

	switch evType {
	case "response.completed":
		// Terminal Responses-API frames are valid completions even with empty
		// output (see codex responses tests); never judge them empty.
		a.terminal = true
		a.blocked = true
	case "response.incomplete", "response.failed", "error":
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
	case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if strings.TrimSpace(chunk.Delta) != "" {
			a.hasContent = true
		}
	case "response.output_text.done", "response.reasoning_summary_text.done", "response.reasoning_text.done":
		if strings.TrimSpace(chunk.Text) != "" {
			a.hasContent = true
		}
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.content_part.added", "response.content_part.done":
		var part openAIResponsePart
		if err := json.Unmarshal(chunk.Part, &part); err == nil {
			if strings.TrimSpace(part.Text) != "" || strings.TrimSpace(part.EncryptedContent) != "" || hasMeaningfulAnnotations(part.Annotations) {
				a.hasContent = true
			}
		}
	case "response.output_item.done":
		var item openAIResponseOutputItem
		if err := json.Unmarshal(chunk.Item, &item); err == nil {
			itemType := strings.ToLower(strings.TrimSpace(item.Type))
			if strings.HasSuffix(itemType, "_call") && itemType != "image_generation_call" {
				if hasMeaningfulResponsesCallItem(item) {
					a.hasToolCalls = true
				}
			}
		}
		if err := json.Unmarshal(chunk.Output, &item); err == nil {
			itemType := strings.ToLower(strings.TrimSpace(item.Type))
			if strings.HasSuffix(itemType, "_call") && itemType != "image_generation_call" {
				if hasMeaningfulResponsesCallItem(item) {
					a.hasToolCalls = true
				}
			}
		}
	case "response.function_call_arguments.delta", "response.function_call_arguments.done":
		if a.hasToolCalls || hasMeaningfulJSONArguments(chunk.Delta) || hasMeaningfulJSONArguments(chunk.Arguments) {
			a.hasToolCalls = true
		}
	case "response.web_search_call.in_progress", "response.web_search_call.searching", "response.web_search_call.completed",
		"codex.rate_limits", "codex.response.metadata":
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
		a.blocked = true
	case "incomplete", "failed", "error":
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

func hasMeaningfulResponsesCallItem(item openAIResponseOutputItem) bool {
	return strings.TrimSpace(item.Name) != "" ||
		hasMeaningfulJSONArguments(item.Arguments) ||
		strings.TrimSpace(item.Input) != "" ||
		strings.TrimSpace(item.Result) != "" ||
		nonEmptyJSONPayload(item.Action) ||
		nonEmptyJSONPayload(item.Results)
}

func hasMeaningfulResponsesImageGenerationCallItem(item openAIResponseOutputItem) bool {
	return strings.TrimSpace(item.Result) != "" || strings.TrimSpace(item.Text) != ""
}

func hasMeaningfulResponsesReasoningSummary(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var parts []openAIResponsePart
	if err := json.Unmarshal(trimmed, &parts); err == nil {
		for _, part := range parts {
			if strings.TrimSpace(part.Text) != "" || strings.TrimSpace(part.EncryptedContent) != "" {
				return true
			}
		}
		return false
	}
	var single openAIResponsePart
	if err := json.Unmarshal(trimmed, &single); err == nil {
		return strings.TrimSpace(single.Text) != "" || strings.TrimSpace(single.EncryptedContent) != ""
	}
	var strSlice []string
	if err := json.Unmarshal(trimmed, &strSlice); err == nil {
		for _, s := range strSlice {
			if strings.TrimSpace(s) != "" {
				return true
			}
		}
		return false
	}
	var str string
	if err := json.Unmarshal(trimmed, &str); err == nil {
		return strings.TrimSpace(str) != ""
	}
	return false
}

func hasMeaningfulAnnotations(annotations []json.RawMessage) bool {
	if len(annotations) == 0 {
		return false
	}
	for _, raw := range annotations {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) || bytes.Equal(trimmed, []byte("[]")) {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(trimmed, &obj); err == nil {
			hasField := false
			for _, v := range obj {
				switch val := v.(type) {
				case nil:
				case string:
					if strings.TrimSpace(val) != "" {
						hasField = true
					}
				default:
					hasField = true
				}
				if hasField {
					break
				}
			}
			if hasField {
				return true
			}
			continue
		}
		var str string
		if err := json.Unmarshal(trimmed, &str); err == nil {
			if strings.TrimSpace(str) != "" {
				return true
			}
			continue
		}
		return true
	}
	return false
}

func (a *emptyCompletionAccum) evalOpenAIResponseOutput(items []openAIResponseOutputItem) {
	for _, item := range items {
		itemType := strings.ToLower(strings.TrimSpace(item.Type))
		switch {
		case itemType == "image_generation_call":
			if hasMeaningfulResponsesImageGenerationCallItem(item) {
				a.hasContent = true
			}
		case strings.HasSuffix(itemType, "_call"):
			if hasMeaningfulResponsesCallItem(item) {
				a.hasToolCalls = true
			}
		case itemType == "reasoning":
			if strings.TrimSpace(item.EncryptedContent) != "" || hasMeaningfulResponsesReasoningSummary(item.Summary) {
				a.hasContent = true
			}
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
				hasMeaningfulAnnotations(part.Annotations) ||
				partType == "refusal" || (partType != "" && partType != "output_text" && partType != "text") {
				a.hasContent = true
			}
		}
	}
}

func (a *emptyCompletionAccum) evalClaudeBlocks(blocks []claudeContentBlock) {
	for _, b := range blocks {
		if b.Type == "tool_use" || b.Type == "server_tool_use" || b.Type == "mcp_tool_use" {
			if (strings.TrimSpace(b.ID) != "" && strings.TrimSpace(b.Name) != "") || nonEmptyJSONPayload(b.Input) {
				a.hasToolCalls = true
			}
			continue
		}
		if nonEmptyJSONPayload(b.Input) {
			a.hasToolCalls = true
			continue
		}
		if b.Type == "thinking" || b.Type == "redacted_thinking" || b.Type == "reasoning" || strings.TrimSpace(b.Thinking) != "" || strings.TrimSpace(b.Data) != "" {
			if strings.TrimSpace(b.Thinking) != "" || strings.TrimSpace(b.Data) != "" {
				a.hasContent = true
			}
			continue
		}
		if b.Type == "text" || strings.TrimSpace(b.Text) != "" {
			if strings.TrimSpace(b.Text) != "" {
				a.hasContent = true
			}
			continue
		}
		if nonEmptyJSONPayload(b.Citation) {
			a.hasContent = true
			continue
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
			a.sawMessageData = true
			a.terminal = true
			a.blocked = promptBlocked
			if !promptBlocked {
				a.geminiTerminal = true
			}
			if usage != nil && usage.CandidatesTokenCount != nil {
				a.sawUsage = true
				a.addUsage(*usage.CandidatesTokenCount)
			}
			return true
		}
		return false
	}

	a.recognized = true
	a.sawMessageData = true
	if promptBlocked {
		a.blocked = true
	}

	if usage != nil {
		if usage.CandidatesTokenCount != nil {
			a.sawUsage = true
			a.addUsage(*usage.CandidatesTokenCount)
		}
	}
	if hasMeaningfulGroundingMetadata(chunk.GroundingMetadata) {
		a.hasContent = true
	}
	if chunk.Response != nil && hasMeaningfulGroundingMetadata(chunk.Response.GroundingMetadata) {
		a.hasContent = true
	}

	if a.geminiCandidatesSeen == nil {
		a.geminiCandidatesSeen = make(map[int]bool)
		a.geminiCandidatesFinished = make(map[int]bool)
	}

	blocked := false
	for i, cand := range candidates {
		idx := i
		if cand.Index != nil {
			idx = *cand.Index
		}
		a.geminiCandidatesSeen[idx] = true
		if cand.FinishReason != nil {
			reason := strings.TrimSpace(*cand.FinishReason)
			if reason != "" {
				if strings.EqualFold(reason, "STOP") {
					a.geminiCandidatesFinished[idx] = true
					a.terminal = true
				} else {
					// A blocking or other terminal reason (SAFETY, RECITATION,
					// MAX_TOKENS, BLOCKLIST, PROHIBITED_CONTENT, OTHER) is not an
					// empty completion: the client must see the stop/block reason
					// rather than a silent auth rotation.
					blocked = true
					a.terminal = true
				}
			}
		}
		if hasMeaningfulGroundingMetadata(cand.GroundingMetadata) {
			a.hasContent = true
		}
		if cand.Content != nil {
			for _, part := range cand.Content.Parts {
				if isMeaningfulGeminiFunctionCall(part.FunctionCall) {
					a.hasToolCalls = true
				}
				if hasMeaningfulGeminiMediaPayload(part.InlineData, part.FileData) ||
					nonEmptyJSONPayload(part.FunctionResponse) {
					a.hasContent = true
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

	if blocked {
		a.blocked = true
	}

	expected := a.expectedChoices
	if expected <= 0 {
		expected = 1
	}
	targetCandidates := expected
	if len(a.geminiCandidatesSeen) > targetCandidates {
		targetCandidates = len(a.geminiCandidatesSeen)
	}
	if len(a.geminiCandidatesFinished) >= targetCandidates && len(a.geminiCandidatesFinished) >= len(a.geminiCandidatesSeen) && !a.blocked {
		a.geminiTerminal = true
	} else {
		a.geminiTerminal = false
	}

	return true
}

func (a *emptyCompletionAccum) evalInteractions(data []byte) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}

	var evType string
	if raw := probe["event_type"]; raw != nil {
		_ = json.Unmarshal(raw, &evType)
	}
	if evType == "" {
		if raw := probe["type"]; raw != nil {
			_ = json.Unmarshal(raw, &evType)
		}
	}

	var objName string
	if raw := probe["object"]; raw != nil {
		_ = json.Unmarshal(raw, &objName)
	}

	isInteractions := objName == "interaction" ||
		interactionsEventTypes[evType] ||
		hasJSONKey(data, "interaction") ||
		(hasJSONKey(data, "steps") && (hasJSONKey(data, "status") || hasJSONKey(data, "interaction_id")))

	if !isInteractions {
		return false
	}

	a.recognized = true
	a.sawMessageData = true

	var chunk interactionsChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return true
	}

	status := chunk.Status
	if chunk.Interaction != nil && chunk.Interaction.Status != "" {
		status = chunk.Interaction.Status
	}

	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		a.terminal = true
		a.interactionsTerminal = true
	case "failed", "cancelled", "error", "blocked", "incomplete":
		a.terminal = true
		a.blocked = true
	case "requires_action":
		a.terminal = true
		a.blocked = true
	}

	if evType == "interaction.completed" {
		a.terminal = true
		if !a.blocked {
			a.interactionsTerminal = true
		}
	} else if evType == "finish" {
		// The Interactions protocol ends a turn with a bare "finish" event whose
		// usage lives under metadata.total_usage instead of the top-level usage
		// field the other terminal events carry.
		a.terminal = true
		if !a.blocked {
			a.interactionsTerminal = true
		}
		if chunk.Metadata != nil {
			if chunk.Metadata.TotalUsage != nil {
				a.evalInteractionsUsage(chunk.Metadata.TotalUsage)
			} else {
				a.evalInteractionsUsage(chunk.Metadata.Usage)
			}
		}
	} else if evType == "interaction.failed" {
		a.terminal = true
		a.blocked = true
	}

	a.evalInteractionsUsage(chunk.Usage)
	if chunk.Interaction != nil {
		a.evalInteractionsUsage(chunk.Interaction.Usage)
	}

	if len(chunk.Steps) == 0 && chunk.Interaction != nil {
		a.evalInteractionsSteps(chunk.Interaction.Steps)
	} else {
		a.evalInteractionsSteps(chunk.Steps)
	}
	if chunk.Step != nil {
		a.evalInteractionsSteps([]interactionsStep{*chunk.Step})
	}

	if chunk.Delta != nil {
		if strings.TrimSpace(chunk.Delta.Text) != "" ||
			strings.TrimSpace(chunk.Delta.Data) != "" ||
			strings.TrimSpace(chunk.Delta.FileURI) != "" ||
			strings.TrimSpace(chunk.Delta.FileUri) != "" ||
			strings.TrimSpace(chunk.Delta.URL) != "" {
			a.hasContent = true
		}
		if chunk.Delta.Content != nil && chunk.Delta.Content.hasMeaningfulContent() {
			a.hasContent = true
		}
		if strings.TrimSpace(chunk.Delta.Name) != "" || hasMeaningfulInteractionsArguments(chunk.Delta.Arguments) {
			a.hasToolCalls = true
		}
		if nonEmptyJSONPayload(chunk.Delta.Result) {
			a.hasContent = true
		}
	}

	return true
}

func (a *emptyCompletionAccum) evalInteractionsUsage(usage *interactionsUsage) {
	if usage == nil {
		return
	}
	if usage.OutputTokens != nil {
		a.sawUsage = true
		a.addUsage(*usage.OutputTokens)
	}
	if usage.TotalOutputTokens != nil {
		a.sawUsage = true
		a.addUsage(*usage.TotalOutputTokens)
	}
	if usage.CompletionTokens != nil {
		a.sawUsage = true
		a.addUsage(*usage.CompletionTokens)
	}
}

func (a *emptyCompletionAccum) evalInteractionsSteps(steps []interactionsStep) {
	for _, step := range steps {
		stepType := strings.ToLower(strings.TrimSpace(step.Type))
		switch stepType {
		case "function_call":
			if strings.TrimSpace(step.Name) != "" || hasMeaningfulInteractionsArguments(step.Arguments) {
				a.hasToolCalls = true
			}
		case "function_result":
			if strings.TrimSpace(step.Name) != "" || nonEmptyJSONPayload(step.Result) {
				a.hasContent = true
			}
		default:
			if strings.TrimSpace(step.Name) != "" || hasMeaningfulInteractionsArguments(step.Arguments) {
				a.hasToolCalls = true
			}
			if nonEmptyJSONPayload(step.Result) {
				a.hasContent = true
			}
		}
		for _, content := range step.Content {
			if content.hasMeaningfulContent() {
				a.hasContent = true
			}
		}
	}
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

// isEmptyCompletion reports whether the buffered SSE stream chunks aggregate to
// an empty completion.
func isEmptyCompletion(chunks []cliproxyexecutor.StreamChunk) bool {
	if len(chunks) == 0 {
		return false
	}
	var detector StreamBootstrapDetector
	for _, c := range chunks {
		if detector.Observe(c.Payload) {
			return false
		}
	}
	return detector.Finish()
}

func isEmptyCompletionError(err error) bool {
	var authErr *Error
	return errors.As(err, &authErr) && authErr != nil && authErr.Code == errEmptyCompletion.Code
}

// streamBootstrapState incrementally evaluates chunks so a metadata-heavy
// prefix is processed once instead of reparsing the entire prefix per chunk.
type streamBootstrapState struct {
	acc          emptyCompletionAccum
	bytes        int
	pending      []byte
	dataLines    [][]byte
	forward      bool
	sawSSE       bool
	sawDone      bool
	currentEvent string
	streamErr    *Error
}

func (s *streamBootstrapState) streamError() error {
	if s == nil || s.streamErr == nil {
		return nil
	}
	return s.streamErr
}

func (s *streamBootstrapState) flushData() {
	if len(s.dataLines) == 0 {
		s.currentEvent = ""
		return
	}
	data := bytes.Join(s.dataLines, []byte("\n"))
	s.dataLines = s.dataLines[:0]
	currentEvent := s.currentEvent
	s.currentEvent = ""
	if bytes.Equal(data, []byte("[DONE]")) {
		s.acc.recognized = true
		s.acc.terminal = true
		s.acc.sawMessageData = true
		s.sawDone = true
		return
	}
	if len(data) == 0 {
		if currentEvent != "error" {
			s.acc.sawMetadataOnly = true
		}
		return
	}
	if err := evalProviderError(data, currentEvent); err != nil {
		s.streamErr = err
		return
	}
	if !s.acc.evalJSON(data) {
		s.acc.sawUnknownData = true
	}
}

func isSSEMetadataLine(b []byte) bool {
	return bytes.HasPrefix(b, []byte("event:")) ||
		bytes.HasPrefix(b, []byte("id:")) ||
		bytes.HasPrefix(b, []byte("retry:")) ||
		bytes.HasPrefix(b, []byte(":")) ||
		bytes.Equal(b, []byte("event")) ||
		bytes.Equal(b, []byte("id")) ||
		bytes.Equal(b, []byte("retry"))
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

func (s *streamBootstrapState) processLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		if len(s.dataLines) > 0 && classifyJSONBuffer(bytes.Join(s.dataLines, []byte("\n"))) == jsonBufIncomplete {
			// Pretty-printed raw JSON may contain blank lines; keep them in the
			// buffer and do not treat them as an SSE event boundary.
			s.dataLines = append(s.dataLines, []byte(""))
			return
		}
		s.flushData()
		return
	}
	s.processSingleLine(line)
}

func (s *streamBootstrapState) processSingleLine(line []byte) {
	switch {
	case bytes.HasPrefix(line, []byte("event:")):
		s.sawSSE = true
		event := strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
		s.currentEvent = event
		if event == "message_stop" {
			s.acc.recognized = true
			s.acc.terminal = true
			s.acc.sawMessageData = true
			s.sawDone = true
		} else if event == "error" {
			// Do not mark metadata only as success signal on error event
		} else {
			s.acc.sawMetadataOnly = true
		}
	case bytes.Equal(line, []byte("event")):
		s.sawSSE = true
		s.acc.sawMetadataOnly = true
	case bytes.HasPrefix(line, []byte("id:")), bytes.HasPrefix(line, []byte("retry:")), bytes.HasPrefix(line, []byte(":")):
		s.sawSSE = true
		s.acc.sawMetadataOnly = true
	case bytes.Equal(line, []byte("id")), bytes.Equal(line, []byte("retry")):
		s.sawSSE = true
		s.acc.sawMetadataOnly = true
	case bytes.HasPrefix(line, []byte("data:")):
		s.sawSSE = true
		s.dataLines = append(s.dataLines, parseSSEDataLine(line))
	case bytes.Equal(line, []byte("data")):
		s.sawSSE = true
		s.dataLines = append(s.dataLines, []byte(""))
	case bytes.HasPrefix(line, []byte("{")), bytes.HasPrefix(line, []byte("[")):
		s.sawSSE = true
		// Raw JSONL/NDJSON frames are newline-terminated and never followed by a
		// blank separator line, so buffering them would defer evaluation until the
		// bootstrap byte cap is hit. Evaluate a self-contained frame immediately;
		// keep buffering only when a multi-line JSON value is already in progress.
		if len(s.dataLines) == 0 && classifyJSONBuffer(line) == jsonBufComplete {
			if err := evalProviderError(line, ""); err != nil {
				s.streamErr = err
			} else if !s.acc.evalJSON(line) {
				s.acc.sawUnknownData = true
			}
			return
		}
		s.dataLines = append(s.dataLines, line)
	default:
		// A pretty-printed raw JSON frame arrives one line at a time: the opening
		// brace lands in dataLines and every continuation line looks like an
		// isolated, invalid JSON value on its own. Append the closing line first,
		// then classify the joined buffer; evaluate immediately when it becomes
		// complete instead of buffering until a blank line or EOF that may not come.
		if len(s.dataLines) > 0 {
			s.dataLines = append(s.dataLines, line)
			joined := bytes.Join(s.dataLines, []byte("\n"))
			s.dataLines = s.dataLines[:0]
			switch classifyJSONBuffer(joined) {
			case jsonBufComplete:
				if err := evalProviderError(joined, ""); err != nil {
					s.streamErr = err
				} else if !s.acc.evalJSON(joined) {
					s.acc.sawUnknownData = true
				}
			case jsonBufIncomplete:
				// The buffered value is still incomplete; restore the accumulated
				// lines and wait for the next continuation.
				s.dataLines = append([][]byte(nil), joined)
			default:
				s.acc.sawUnknownData = true
			}
			return
		}
		if classify := classifyJSONBuffer(line); classify == jsonBufComplete || classify == jsonBufIncomplete {
			if err := evalProviderError(line, ""); err != nil {
				s.streamErr = err
			} else if !s.acc.evalJSON(line) {
				s.acc.sawUnknownData = true
			}
		} else {
			s.acc.sawUnknownData = true
		}
	}
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
			s.processLine(line)
			if s.shouldForward() {
				s.forward = true
				return true
			}
			continue
		}
		break
	}

	trimmed := bytes.TrimSpace(s.pending)
	if len(trimmed) == 0 {
		return false
	}

	if bytes.HasPrefix(trimmed, []byte("data:")) {
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		if len(s.dataLines) == 0 && (bytes.Equal(payload, []byte("[DONE]")) || classifyJSONBuffer(payload) == jsonBufComplete) {
			s.sawSSE = true
			s.dataLines = append(s.dataLines, parseSSEDataLine(trimmed))
			s.flushData()
			s.pending = s.pending[:0]
			s.forward = s.shouldForward()
			return s.forward
		}
		return false
	}

	if couldBeSSEPrefix(trimmed) {
		return false
	}
	switch classifyJSONBuffer(trimmed) {
	case jsonBufComplete:
		if err := evalProviderError(trimmed, ""); err != nil {
			s.streamErr = err
		} else if !s.acc.evalJSON(trimmed) {
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

func (s *streamBootstrapState) finish() {
	if len(s.pending) > 0 {
		trimmed := bytes.TrimSpace(s.pending)
		s.pending = s.pending[:0]
		if len(trimmed) > 0 {
			s.processLine(trimmed)
		}
	}
	s.flushData()
}

func (s *streamBootstrapState) isEmptyCompletion() bool {
	return s.acc.empty()
}

func (s *streamBootstrapState) isTerminalEmpty() bool {
	if s.acc.openAITerminal && !s.acc.sawUsage {
		// OpenAI streams may emit finish_reason=stop before the final usage
		// frame; do not judge the stream empty until usage arrives.
		return false
	}
	return (s.sawDone || s.acc.geminiTerminal || s.acc.claudeTerminal || s.acc.openAITerminal || s.acc.interactionsTerminal) && s.acc.empty()
}

func (s *streamBootstrapState) setExpectedChoices(n int) {
	if n <= 0 {
		n = 1
	}
	s.acc.expectedChoices = n
}

func (s *streamBootstrapState) hasMeaningfulOutput() bool {
	if s.forward {
		return true
	}
	if s.acc.hasContent || s.acc.hasToolCalls || s.acc.blocked || (s.acc.sawUsage && s.acc.completionTokens > 0) || s.acc.sawUnknownData {
		return true
	}
	if s.streamErr != nil {
		return false
	}
	if !s.acc.recognized && !s.sawSSE && s.bytes > 0 {
		return true
	}
	return false
}

func (s *streamBootstrapState) shouldForward() bool {
	if s.streamErr != nil {
		return false
	}
	return s.acc.hasContent || s.acc.hasToolCalls || s.acc.blocked || (s.acc.sawUsage && s.acc.completionTokens > 0) || s.acc.sawUnknownData || (!s.acc.recognized && !s.sawSSE)
}

type streamErrorEnvelope struct {
	Type        string               `json:"type"`
	EventType   string               `json:"event_type"`
	Error       json.RawMessage      `json:"error"`
	Message     string               `json:"message"`
	Code        json.RawMessage      `json:"code"`
	Status      string               `json:"status"`
	Response    *streamErrorEnvelope `json:"response,omitempty"`
	Interaction *streamErrorEnvelope `json:"interaction,omitempty"`
}

func inferHTTPStatus(typeStr, codeStr, statusStr string) int {
	if statusStr != "" {
		switch strings.ToUpper(strings.TrimSpace(statusStr)) {
		case "RESOURCE_EXHAUSTED":
			return http.StatusTooManyRequests
		case "UNAUTHENTICATED":
			return http.StatusUnauthorized
		case "PERMISSION_DENIED":
			return http.StatusForbidden
		case "UNAVAILABLE":
			return http.StatusServiceUnavailable
		case "DEADLINE_EXCEEDED":
			return http.StatusGatewayTimeout
		case "INTERNAL":
			return http.StatusInternalServerError
		case "INVALID_ARGUMENT", "FAILED_PRECONDITION":
			return http.StatusBadRequest
		case "NOT_FOUND":
			return http.StatusNotFound
		case "ALREADY_EXISTS":
			return http.StatusConflict
		}
	}
	for _, s := range []string{typeStr, codeStr} {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "overloaded_error", "overloaded":
			return http.StatusServiceUnavailable
		case "rate_limit_error", "rate_limit_exceeded", "insufficient_quota", "quota_exceeded", "requests":
			return http.StatusTooManyRequests
		case "authentication_error", "invalid_api_key", "unauthorized":
			return http.StatusUnauthorized
		case "permission_error", "forbidden":
			return http.StatusForbidden
		case "not_found_error":
			return http.StatusNotFound
		case "invalid_request_error", "bad_request_error", "invalid_prompt", "cyber_policy", "context_length_exceeded":
			return http.StatusBadRequest
		case "api_error", "internal_server_error":
			return http.StatusInternalServerError
		}
	}
	return 0
}

// isInteractionsFailureEnvelope reports whether an Interactions event carries a
// provider failure. The failure detail is nested under "interaction", so without
// this the stream is only marked blocked and the request never fails over.
func isInteractionsFailureEnvelope(envelope streamErrorEnvelope) bool {
	if strings.EqualFold(envelope.EventType, "interaction.failed") || strings.EqualFold(envelope.Type, "interaction.failed") {
		return true
	}
	if envelope.Interaction == nil {
		return false
	}
	if len(envelope.Interaction.Error) > 0 && !bytes.Equal(envelope.Interaction.Error, []byte("null")) {
		return true
	}
	return strings.EqualFold(envelope.Interaction.Status, "failed")
}

func parseStreamErrorFromEnvelope(data []byte, envelope streamErrorEnvelope) *Error {
	if envelope.Interaction != nil {
		if (len(envelope.Error) == 0 || bytes.Equal(envelope.Error, []byte("null"))) && len(envelope.Interaction.Error) > 0 {
			envelope.Error = envelope.Interaction.Error
		}
		if envelope.Message == "" {
			envelope.Message = envelope.Interaction.Message
		}
		if len(envelope.Code) == 0 {
			envelope.Code = envelope.Interaction.Code
		}
	}

	if envelope.Response != nil {
		if (len(envelope.Error) == 0 || bytes.Equal(envelope.Error, []byte("null"))) && len(envelope.Response.Error) > 0 {
			envelope.Error = envelope.Response.Error
		}
		if envelope.Message == "" {
			envelope.Message = envelope.Response.Message
		}
		if len(envelope.Code) == 0 {
			envelope.Code = envelope.Response.Code
		}
		if envelope.Status == "" {
			envelope.Status = envelope.Response.Status
		}
		if (envelope.Type == "" || strings.EqualFold(envelope.Type, "response.failed")) && envelope.Response.Type != "" {
			envelope.Type = envelope.Response.Type
		}
	}

	var detail struct {
		Message string          `json:"message"`
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
		Status  string          `json:"status"`
	}

	var rawErrorString string
	if len(envelope.Error) > 0 {
		trimmedErr := bytes.TrimSpace(envelope.Error)
		if bytes.HasPrefix(trimmedErr, []byte("{")) {
			_ = json.Unmarshal(trimmedErr, &detail)
		} else if bytes.HasPrefix(trimmedErr, []byte("\"")) {
			_ = json.Unmarshal(trimmedErr, &rawErrorString)
		}
	}

	message := detail.Message
	if message == "" {
		message = envelope.Message
	}
	if message == "" {
		message = rawErrorString
	}
	if message == "" && detail.Type != "" {
		message = detail.Type
	}
	if message == "" && detail.Status != "" {
		message = detail.Status
	}
	if message == "" && len(data) > 0 && !bytes.HasPrefix(data, []byte("{")) {
		message = string(data)
	}
	if message == "" {
		message = "upstream stream error"
	}

	code := ""
	var rawCodeInt int

	extractCode := func(raw json.RawMessage) {
		if len(raw) == 0 {
			return
		}
		var strCode string
		if json.Unmarshal(raw, &strCode) == nil && strings.TrimSpace(strCode) != "" {
			code = strings.TrimSpace(strCode)
			if num, err := strconv.Atoi(code); err == nil && num > 0 {
				rawCodeInt = num
			}
			return
		}
		var num json.Number
		if json.Unmarshal(raw, &num) == nil {
			if n, err := num.Int64(); err == nil && n > 0 {
				rawCodeInt = int(n)
				code = strconv.Itoa(rawCodeInt)
			}
		}
	}

	extractCode(detail.Code)
	if code == "" {
		extractCode(envelope.Code)
	}
	if code == "" && detail.Type != "" {
		code = detail.Type
	}
	if code == "" && detail.Status != "" {
		code = detail.Status
	}
	if code == "" && envelope.Type != "" && !strings.EqualFold(envelope.Type, "error") {
		code = envelope.Type
	}

	status := 0
	if rawCodeInt >= 100 && rawCodeInt <= 599 {
		status = rawCodeInt
	}
	statusStr := strings.TrimSpace(detail.Status)
	if statusStr == "" {
		statusStr = strings.TrimSpace(envelope.Status)
	}
	typeStr := strings.TrimSpace(detail.Type)
	if typeStr == "" {
		typeStr = strings.TrimSpace(envelope.Type)
	}

	if status == 0 {
		status = inferHTTPStatus(typeStr, code, statusStr)
	}

	if status == 0 {
		lowerMsg := strings.ToLower(message)
		switch {
		case strings.Contains(lowerMsg, "rate limit") || strings.Contains(lowerMsg, "resource exhausted") || strings.Contains(lowerMsg, "too many requests") || strings.Contains(lowerMsg, "quota"):
			status = http.StatusTooManyRequests
		case strings.Contains(lowerMsg, "overloaded"):
			status = http.StatusServiceUnavailable
		case strings.Contains(lowerMsg, "unauthorized") || strings.Contains(lowerMsg, "invalid api key") || strings.Contains(lowerMsg, "invalid x-api-key") || strings.Contains(lowerMsg, "unauthenticated"):
			status = http.StatusUnauthorized
		case strings.Contains(lowerMsg, "permission denied") || strings.Contains(lowerMsg, "forbidden"):
			status = http.StatusForbidden
		default:
			status = http.StatusBadGateway
		}
	}

	err := &Error{
		Code:       code,
		Message:    message,
		HTTPStatus: status,
	}

	if isRequestInvalidError(err) || clienterror.IsRequestFault(status, errors.New(string(data))) {
		err.Retryable = false
	} else {
		err.Retryable = true
	}

	// Sanitize every text field before the parsed upstream error reaches
	// logging, result recording, or the output stream.
	return sanitizeErrorTextFields(err).(*Error)
}

func evalProviderError(data []byte, sseEvent string) *Error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil
	}

	var envelope streamErrorEnvelope
	isError := strings.EqualFold(sseEvent, "error")

	if bytes.HasPrefix(trimmed, []byte("{")) {
		if err := json.Unmarshal(trimmed, &envelope); err == nil {
			if len(envelope.Error) > 0 && !bytes.Equal(envelope.Error, []byte("null")) {
				isError = true
			} else if strings.EqualFold(envelope.Type, "error") || strings.EqualFold(envelope.Type, "response.failed") {
				isError = true
			} else if isInteractionsFailureEnvelope(envelope) {
				isError = true
			} else if envelope.Response != nil {
				if len(envelope.Response.Error) > 0 && !bytes.Equal(envelope.Response.Error, []byte("null")) {
					isError = true
				} else if strings.EqualFold(envelope.Response.Type, "error") || strings.EqualFold(envelope.Response.Status, "failed") {
					isError = true
				}
			}
		}
	}

	if !isError {
		return nil
	}

	return parseStreamErrorFromEnvelope(trimmed, envelope)
}

type streamPayloadErrorDetector struct {
	pending      []byte
	dataLines    [][]byte
	currentEvent string
	err          *Error
}

func (d *streamPayloadErrorDetector) Observe(chunk []byte) *Error {
	if d.err != nil {
		return d.err
	}
	if len(chunk) == 0 {
		return nil
	}
	d.pending = append(d.pending, chunk...)
	for {
		newline := bytes.IndexByte(d.pending, '\n')
		if newline < 0 {
			break
		}
		line := bytes.TrimSpace(d.pending[:newline])
		d.pending = d.pending[newline+1:]
		if len(line) == 0 {
			if len(d.dataLines) > 0 && classifyJSONBuffer(bytes.Join(d.dataLines, []byte("\n"))) == jsonBufIncomplete {
				d.dataLines = append(d.dataLines, []byte(""))
				continue
			}
			d.flushData()
			if d.err != nil {
				return d.err
			}
			continue
		}
		d.processLine(line)
		if d.err != nil {
			return d.err
		}
	}
	trimmed := bytes.TrimSpace(d.pending)
	if len(trimmed) > 0 && !isSSEPrefix(trimmed) && !couldBeSSEPrefix(trimmed) {
		if classifyJSONBuffer(trimmed) == jsonBufComplete {
			if values, err := decodeJSONValues(trimmed); err == nil {
				for _, v := range values {
					if streamErr := evalProviderError(v, ""); streamErr != nil {
						d.err = streamErr
						d.pending = d.pending[:0]
						return d.err
					}
				}
				d.pending = d.pending[:0]
			}
		}
	}
	return d.err
}

func (d *streamPayloadErrorDetector) processLine(line []byte) {
	switch {
	case bytes.HasPrefix(line, []byte("event:")):
		d.currentEvent = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
	case bytes.Equal(line, []byte("event")):
	case bytes.HasPrefix(line, []byte("id:")), bytes.HasPrefix(line, []byte("retry:")), bytes.HasPrefix(line, []byte(":")):
	case bytes.Equal(line, []byte("id")), bytes.Equal(line, []byte("retry")):
	case bytes.HasPrefix(line, []byte("data:")):
		d.dataLines = append(d.dataLines, parseSSEDataLine(line))
	case bytes.Equal(line, []byte("data")):
		d.dataLines = append(d.dataLines, []byte(""))
	case bytes.HasPrefix(line, []byte("{")), bytes.HasPrefix(line, []byte("[")):
		// Same JSONL/NDJSON framing as the bootstrap detector: a self-contained
		// frame is never followed by a blank line, so evaluate it now instead of
		// buffering it until a separator that will not arrive.
		if len(d.dataLines) == 0 && classifyJSONBuffer(line) == jsonBufComplete {
			d.evalCompleteJSONLine(line)
			return
		}
		d.dataLines = append(d.dataLines, line)
	default:
		// Mirror of the bootstrap detector: a pretty-printed raw JSON frame must
		// append the closing line first, then classify the joined buffer, and
		// evaluate immediately when it becomes complete.
		if len(d.dataLines) > 0 {
			d.dataLines = append(d.dataLines, line)
			joined := bytes.Join(d.dataLines, []byte("\n"))
			d.dataLines = nil
			switch classifyJSONBuffer(joined) {
			case jsonBufComplete:
				d.evalCompleteJSONLine(joined)
			case jsonBufIncomplete:
				// Still incomplete; restore and wait for the next line.
				d.dataLines = [][]byte{joined}
			}
			return
		}
		if classifyJSONBuffer(line) == jsonBufComplete {
			d.evalCompleteJSONLine(line)
		}
	}
}

func (d *streamPayloadErrorDetector) evalCompleteJSONLine(line []byte) {
	values, err := decodeJSONValues(line)
	if err != nil {
		if streamErr := evalProviderError(line, ""); streamErr != nil {
			d.err = streamErr
		}
		return
	}
	for _, v := range values {
		if streamErr := evalProviderError(v, ""); streamErr != nil {
			d.err = streamErr
			return
		}
	}
}

func (d *streamPayloadErrorDetector) flushData() {
	if len(d.dataLines) == 0 {
		return
	}
	data := bytes.Join(d.dataLines, []byte("\n"))
	currentEvent := d.currentEvent
	d.dataLines = nil
	d.currentEvent = ""
	if bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	if len(data) == 0 {
		return
	}
	if err := evalProviderError(data, currentEvent); err != nil {
		d.err = err
	}
}

func (d *streamPayloadErrorDetector) Finish() *Error {
	if d.err != nil {
		return d.err
	}
	if len(d.pending) > 0 {
		trimmed := bytes.TrimSpace(d.pending)
		d.pending = d.pending[:0]
		if len(trimmed) > 0 {
			if !isSSEPrefix(trimmed) && !couldBeSSEPrefix(trimmed) && classifyJSONBuffer(trimmed) == jsonBufComplete {
				if values, err := decodeJSONValues(trimmed); err == nil {
					for _, v := range values {
						if err := evalProviderError(v, ""); err != nil {
							d.err = err
							return d.err
						}
					}
				}
			} else {
				d.processLine(trimmed)
			}
		}
	}
	d.flushData()
	return d.err
}

func detectStreamPayloadError(payload []byte) *Error {
	var d streamPayloadErrorDetector
	if err := d.Observe(payload); err != nil {
		return err
	}
	return d.Finish()
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
	const idPrefix = "id:"
	const retryPrefix = "retry:"
	value := string(payload)
	return strings.HasPrefix(value, ":") ||
		strings.HasPrefix(dataPrefix, value) || strings.HasPrefix(eventPrefix, value) ||
		strings.HasPrefix(idPrefix, value) || strings.HasPrefix(retryPrefix, value) ||
		strings.HasPrefix(value, dataPrefix) || strings.HasPrefix(value, eventPrefix) ||
		strings.HasPrefix(value, idPrefix) || strings.HasPrefix(value, retryPrefix) ||
		value == "data" || value == "event" || value == "id" || value == "retry"
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

// markEmptyCount records a failed retriable empty count-tokens result and
// returns the error to propagate. It is the count analogue of markEmptyCompletion
// and uses errEmptyCount so count failures have a consistent code everywhere.
func (m *Manager) markEmptyCount(ctx context.Context, result *Result) error {
	result.Success = false
	result.Error = errEmptyCount
	m.MarkResult(ctx, *result)
	return errEmptyCount
}
