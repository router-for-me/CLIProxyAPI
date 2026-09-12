package helps

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/tidwall/gjson"
)

// MaxNDJSONLineSize defines the maximum permissible size for a single NDJSON line (16 MiB).
// This provides ample headroom for large tool payloads while preventing unbounded memory growth.
const MaxNDJSONLineSize = 16 * 1024 * 1024

// GatewayEvent represents a generic event from the Command Code /alpha/generate NDJSON stream.
type GatewayEvent struct {
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	ToolCallID       string          `json:"toolCallId,omitempty"`
	ToolName         string          `json:"toolName,omitempty"`
	Name             string          `json:"name,omitempty"`
	Text             string          `json:"text,omitempty"`
	Delta            string          `json:"delta,omitempty"`
	Input            json.RawMessage `json:"input,omitempty"`
	Args             json.RawMessage `json:"args,omitempty"`
	FinishReason     string          `json:"finishReason,omitempty"`
	RawFinishReason  string          `json:"rawFinishReason,omitempty"`
	Usage            *EventUsage     `json:"usage,omitempty"`
	TotalUsage       *EventUsage     `json:"totalUsage,omitempty"`
	Message          string          `json:"message,omitempty"`
	Error            any             `json:"error,omitempty"`
	ProviderMetadata json.RawMessage `json:"providerMetadata,omitempty"`
}

// EventUsage captures token counts reported by the gateway.
type EventUsage struct {
	InputTokens       int64            `json:"inputTokens,omitempty"`
	InputTokensAlt    int64            `json:"input_tokens,omitempty"`
	OutputTokens      int64            `json:"outputTokens,omitempty"`
	OutputTokensAlt   int64            `json:"output_tokens,omitempty"`
	TotalTokens       int64            `json:"totalTokens,omitempty"`
	TotalTokensAlt    int64            `json:"total_tokens,omitempty"`
	CachedInputTokens int64            `json:"cachedInputTokens,omitempty"`
	ReasoningTokens   int64            `json:"reasoningTokens,omitempty"`
	InputTokenDetails *TokenDetails    `json:"inputTokenDetails,omitempty"`
	Raw               *RawUsageDetails `json:"raw,omitempty"`
}

// TokenDetails contains sub-details of tokens.
type TokenDetails struct {
	NoCacheTokens   int64 `json:"noCacheTokens,omitempty"`
	CacheReadTokens int64 `json:"cacheReadTokens,omitempty"`
}

// RawUsageDetails holds provider-level raw token details.
type RawUsageDetails struct {
	PromptTokens      int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens  int64 `json:"completion_tokens,omitempty"`
	PromptCacheHit    int64 `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMiss   int64 `json:"prompt_cache_miss_tokens,omitempty"`
	TotalTokens       int64 `json:"total_tokens,omitempty"`
	CompletionDetails any   `json:"completion_tokens_details,omitempty"`
}

// NormalizeToolEventID extracts tool call ID from ID or ToolCallID fields.
func (e *GatewayEvent) NormalizeToolEventID() string {
	if e.ID != "" {
		return e.ID
	}
	return e.ToolCallID
}

// NormalizeToolName extracts tool name from ToolName or Name fields.
func (e *GatewayEvent) NormalizeToolName() string {
	if e.ToolName != "" {
		return e.ToolName
	}
	return e.Name
}

// UnboundedNDJSONReader reads newline-delimited JSON with a 16 MiB defensive upper bound.
type UnboundedNDJSONReader struct {
	reader *bufio.Reader
}

// NewUnboundedNDJSONReader creates an unbounded reader wrapping an io.Reader.
func NewUnboundedNDJSONReader(r io.Reader) *UnboundedNDJSONReader {
	return &UnboundedNDJSONReader{
		reader: bufio.NewReaderSize(r, 64*1024),
	}
}

// ReadNextLine reads the next non-empty newline-terminated or EOF-terminated line.
// Fails closed if any single line exceeds MaxNDJSONLineSize.
func (r *UnboundedNDJSONReader) ReadNextLine(ctx context.Context) ([]byte, error) {
	for {
		if ctx != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}

		var lineBuf bytes.Buffer
		for {
			chunk, isPrefix, err := r.reader.ReadLine()
			if err != nil {
				if errors.Is(err, io.EOF) {
					if lineBuf.Len() > 0 || len(chunk) > 0 {
						if lineBuf.Len()+len(chunk) > MaxNDJSONLineSize {
							return nil, fmt.Errorf("ndjson line exceeds maximum allowed size of %d bytes", MaxNDJSONLineSize)
						}
						lineBuf.Write(chunk)
						trimmed := bytes.TrimSpace(lineBuf.Bytes())
						if len(trimmed) > 0 {
							return trimmed, nil
						}
					}
					return nil, io.EOF
				}
				return nil, err
			}

			if lineBuf.Len()+len(chunk) > MaxNDJSONLineSize {
				return nil, fmt.Errorf("ndjson line exceeds maximum allowed size of %d bytes", MaxNDJSONLineSize)
			}
			lineBuf.Write(chunk)

			if !isPrefix {
				break
			}
		}

		trimmed := bytes.TrimSpace(lineBuf.Bytes())
		if len(trimmed) == 0 {
			continue
		}
		return trimmed, nil
	}
}

// ParsedToolCall represents an accumulated tool call.
type ParsedToolCall struct {
	Index     int
	ID        string
	Name      string
	Arguments strings.Builder
}

// StreamAccumulator accumulates the full turn from NDJSON events.
type StreamAccumulator struct {
	ResponseID       string
	CreatedAt        int64
	Model            string
	Content          strings.Builder
	ReasoningContent strings.Builder
	ToolCalls        []*ParsedToolCall
	toolCallByID     map[string]*ParsedToolCall
	FinishReason     string
	SawTerminal      bool
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CachedTokens     int64
}

// NewStreamAccumulator creates a new accumulator.
func NewStreamAccumulator(responseID, model string, createdAt int64) *StreamAccumulator {
	return &StreamAccumulator{
		ResponseID:   responseID,
		CreatedAt:    createdAt,
		Model:        model,
		toolCallByID: make(map[string]*ParsedToolCall),
		FinishReason: "stop",
	}
}

// ProcessEvent updates accumulator state with a single gateway event.
func (acc *StreamAccumulator) ProcessEvent(event *GatewayEvent) error {
	if event == nil {
		return nil
	}

	switch event.Type {
	case "reasoning-delta":
		acc.ReasoningContent.WriteString(event.Text)

	case "text-delta":
		acc.Content.WriteString(event.Text)

	case "tool-input-start":
		id := event.NormalizeToolEventID()
		if id == "" {
			return nil
		}
		tc, exists := acc.toolCallByID[id]
		if !exists {
			tc = &ParsedToolCall{
				Index: len(acc.ToolCalls),
				ID:    id,
				Name:  event.NormalizeToolName(),
			}
			acc.ToolCalls = append(acc.ToolCalls, tc)
			acc.toolCallByID[id] = tc
		} else if tc.Name == "" && event.NormalizeToolName() != "" {
			tc.Name = event.NormalizeToolName()
		}

	case "tool-input-delta":
		id := event.NormalizeToolEventID()
		if id == "" {
			return nil
		}
		tc, exists := acc.toolCallByID[id]
		if !exists {
			tc = &ParsedToolCall{
				Index: len(acc.ToolCalls),
				ID:    id,
			}
			acc.ToolCalls = append(acc.ToolCalls, tc)
			acc.toolCallByID[id] = tc
		}
		tc.Arguments.WriteString(event.Delta)

	case "tool-input-end", "tool-call":
		id := event.NormalizeToolEventID()
		if id == "" {
			return nil
		}
		tc, exists := acc.toolCallByID[id]
		if !exists {
			tc = &ParsedToolCall{
				Index: len(acc.ToolCalls),
				ID:    id,
				Name:  event.NormalizeToolName(),
			}
			acc.ToolCalls = append(acc.ToolCalls, tc)
			acc.toolCallByID[id] = tc
		} else if tc.Name == "" && event.NormalizeToolName() != "" {
			tc.Name = event.NormalizeToolName()
		}

		rawInput := event.Input
		if len(rawInput) == 0 {
			rawInput = event.Args
		}
		if len(rawInput) > 0 {
			tc.Arguments.Reset()
			tc.Arguments.Write(rawInput)
		}

	case "finish-step", "finish":
		acc.SawTerminal = true
		u := event.Usage
		if u == nil {
			u = event.TotalUsage
		}
		if u != nil {
			acc.mergeUsage(u)
		}

		rawReason := event.FinishReason
		if rawReason == "" {
			rawReason = event.RawFinishReason
		}
		acc.FinishReason = MapFinishReason(rawReason, len(acc.ToolCalls) > 0)

	case "error":
		errMsg := event.Message
		if errMsg == "" && event.Error != nil {
			errMsg = fmt.Sprintf("%v", event.Error)
		}
		if errMsg == "" {
			errMsg = "Command Code stream error"
		}
		return errors.New(errMsg)

	default:
		// Unknown future event types are safely ignored without causing failure
	}

	return nil
}

func (acc *StreamAccumulator) mergeUsage(u *EventUsage) {
	inputTokens := u.InputTokens
	if inputTokens == 0 {
		inputTokens = u.InputTokensAlt
	}
	outputTokens := u.OutputTokens
	if outputTokens == 0 {
		outputTokens = u.OutputTokensAlt
	}
	cachedTokens := u.CachedInputTokens
	if cachedTokens == 0 && u.InputTokenDetails != nil {
		cachedTokens = u.InputTokenDetails.CacheReadTokens
	}
	if cachedTokens == 0 && u.Raw != nil {
		cachedTokens = u.Raw.PromptCacheHit
	}

	acc.PromptTokens = inputTokens
	acc.CompletionTokens = outputTokens
	acc.CachedTokens = cachedTokens
	acc.TotalTokens = u.TotalTokens
	if acc.TotalTokens == 0 {
		acc.TotalTokens = u.TotalTokensAlt
	}
	if acc.TotalTokens == 0 {
		acc.TotalTokens = acc.PromptTokens + acc.CompletionTokens
	}
}

// MapFinishReason maps upstream Command Code finish reasons to OpenAI finish reasons.
func MapFinishReason(reason string, hasToolCalls bool) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "tool-calls", "tool_calls", "tool_use":
		return "tool_calls"
	case "length", "max_tokens":
		return "length"
	case "content-filter", "content_filter":
		return "content_filter"
	case "stop", "end_turn":
		return "stop"
	default:
		if hasToolCalls {
			return "tool_calls"
		}
		return "stop"
	}
}

// ParseRawJSONLine parses a single raw NDJSON line into GatewayEvent.
func ParseRawJSONLine(line []byte) (*GatewayEvent, error) {
	if !gjson.ValidBytes(line) {
		return nil, errors.New("invalid JSON line")
	}

	var event GatewayEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return nil, err
	}
	return &event, nil
}
