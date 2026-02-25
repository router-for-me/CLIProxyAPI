// Package usage provides message transformation capabilities for handling
// long conversations that exceed model context limits.
// 
// Supported transforms:
// - middle-out: Compress conversation by keeping start/end messages and trimming middle
package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// TransformType represents the type of message transformation
type TransformType string

const (
	// TransformMiddleOut keeps first half and last half of conversation, compresses middle
	TransformMiddleOut TransformType = "middle-out"
	// TransformTruncateStart keeps only the most recent messages
	TransformTruncateStart TransformType = "truncate-start"
	// TransformTruncateEnd keeps only the earliest messages
	TransformTruncateEnd TransformType = "truncate-end"
	// TransformSummarize summarizes the middle portion
	TransformSummarize TransformType = "summarize"
)

// Message represents a chat message
type Message struct {
	Role    string          `json:"role"`
	Content interface{}    `json:"content"`
	Name    string          `json:"name,omitempty"`
	ToolCalls []ToolCall   `json:"tool_calls,omitempty"`
}

// ToolCall represents a tool call in a message
type ToolCall struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall represents a function call
type FunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// TransformRequest specifies parameters for message transformation
type TransformRequest struct {
	// Transform is the transformation type to apply
	Transform TransformType
	// MaxMessages is the maximum number of messages to keep (0 = auto)
	MaxMessages int
	// MaxTokens is the target maximum tokens (0 = use MaxMessages)
	MaxTokens int
	// KeepSystem determines if system message should always be kept
	KeepSystem bool
	// SummaryPrompt is the prompt to use for summarization (if TransformSummarize)
	SummaryPrompt string
	// PreserveLatestN messages to always keep at the end
	PreserveLatestN int
	// PreserveFirstN messages to always keep at the start
	PreserveFirstN int
}

// TransformResponse contains the result of message transformation
type TransformResponse struct {
	Messages   []Message `json:"messages"`
	OriginalCount int    `json:"original_count"`
	FinalCount   int    `json:"final_count"`
	TokensRemoved int   `json:"tokens_removed"`
	Transform    string `json:"transform"`
	Reason      string `json:"reason,omitempty"`
}

// TransformMessages applies the specified transformation to messages
func TransformMessages(ctx context.Context, messages []Message, req *TransformRequest) (*TransformResponse, error) {
	if len(messages) == 0 {
		return &TransformResponse{
			Messages:       messages,
			OriginalCount: 0,
			FinalCount:    0,
			TokensRemoved: 0,
			Transform:    string(req.Transform),
		}, nil
	}

	// Set defaults
	if req.Transform == "" {
		req.Transform = TransformMiddleOut
	}
	if req.MaxMessages == 0 {
		req.MaxMessages = 20
	}
	if req.PreserveLatestN == 0 {
		req.PreserveLatestN = 5
	}

	// Make a copy to avoid modifying original
	result := make([]Message, len(messages))
	copy(result, messages)

	var reason string
	switch req.Transform {
	case TransformMiddleOut:
		result, reason = transformMiddleOut(result, req)
	case TransformTruncateStart:
		result, reason = transformTruncateStart(result, req)
	case TransformTruncateEnd:
		result, reason = transformTruncateEnd(result, req)
	default:
		return nil, fmt.Errorf("unknown transform type: %s", req.Transform)
	}

	return &TransformResponse{
		Messages:       result,
		OriginalCount: len(messages),
		FinalCount:    len(result),
		TokensRemoved: len(messages) - len(result),
		Transform:    string(req.Transform),
		Reason:       reason,
	}, nil
}

// transformMiddleOut keeps first N and last N messages, compresses middle
func transformMiddleOut(messages []Message, req *TransformRequest) ([]Message, string) {
	// Find system message if present
	var systemIdx = -1
	for i, m := range messages {
		if m.Role == "system" {
			systemIdx = i
			break
		}
	}

	// Calculate how many to keep from start and end
	available := len(messages)
	if systemIdx >= 0 {
		available--
	}

	startKeep := req.PreserveFirstN
	if startKeep == 0 {
		startKeep = available / 4
		if startKeep < 2 {
			startKeep = 2
		}
	}
	
	endKeep := req.PreserveLatestN
	if endKeep == 0 {
		endKeep = available / 4
		if endKeep < 2 {
			endKeep = 2
		}
	}

	// If we need to keep fewer than available, compress
	if startKeep+endKeep >= available {
		return messages, "conversation within limits, no transformation needed"
	}

	// Build result
	var result []Message

	// Add system message if present and KeepSystem is true
	if systemIdx >= 0 && req.KeepSystem {
		result = append(result, messages[systemIdx])
	}

	// Add messages from start
	if systemIdx > 0 {
		// System is at index 0
		result = append(result, messages[0:startKeep]...)
	} else {
		result = append(result, messages[0:startKeep]...)
	}

	// Add compression indicator
	compressedCount := available - startKeep - endKeep
	if compressedCount > 0 {
		result = append(result, Message{
			Role: "system",
			Content: fmt.Sprintf("[%d messages compressed due to context length limits]", compressedCount),
		})
	}

	// Add messages from end
	endStart := len(messages) - endKeep
	result = append(result, messages[endStart:]...)

	return result, fmt.Sprintf("compressed %d messages, kept %d from start and %d from end", 
		compressedCount, startKeep, endKeep)
}

// transformTruncateStart keeps only the most recent messages
func transformTruncateStart(messages []Message, req *TransformRequest) ([]Message, string) {
	if len(messages) <= req.MaxMessages {
		return messages, "within message limit"
	}

	// Find system message
	var systemMsg *Message
	var nonSystem []Message
	
	for _, m := range messages {
		if m.Role == "system" && req.KeepSystem {
			systemMsg = &m
		} else {
			nonSystem = append(nonSystem, m)
		}
	}

	// Keep most recent
	keep := req.MaxMessages
	if systemMsg != nil {
		keep--
	}
	
	if keep <= 0 {
		keep = 1
	}
	
	if keep >= len(nonSystem) {
		return messages, "within message limit"
	}

	nonSystem = nonSystem[len(nonSystem)-keep:]

	// Rebuild
	var result []Message
	if systemMsg != nil {
		result = append(result, *systemMsg)
	}
	result = append(result, nonSystem...)

	return result, fmt.Sprintf("truncated to last %d messages", len(result))
}

// transformTruncateEnd keeps only the earliest messages
func transformTruncateEnd(messages []Message, req *TransformRequest) ([]Message, string) {
	if len(messages) <= req.MaxMessages {
		return messages, "within message limit"
	}

	keep := req.MaxMessages
	if keep >= len(messages) {
		keep = len(messages)
	}

	result := messages[:keep]
	return result, fmt.Sprintf("truncated to first %d messages", len(result))
}

// EstimateTokens estimates the number of tokens in messages (rough approximation)
func EstimateTokens(messages []Message) int {
	total := 0
	for _, m := range messages {
		// Rough estimate: 1 token ≈ 4 characters
		switch content := m.Content.(type) {
		case string:
			total += len(content) / 4
		case []interface{}:
			for _, part := range content {
				if p, ok := part.(map[string]interface{}); ok {
					if text, ok := p["text"].(string); ok {
						total += len(text) / 4
					}
				}
			}
		}
		// Add role overhead
		total += len(m.Role) / 4
	}
	return total
}

// MiddleOutTransform creates a TransformRequest for middle-out compression
func MiddleOutTransform(preserveStart, preserveEnd int) *TransformRequest {
	return &TransformRequest{
		Transform:       TransformMiddleOut,
		PreserveFirstN:  preserveStart,
		PreserveLatestN: preserveEnd,
		KeepSystem:      true,
	}
}

// ParseTransformType parses a transform type string
func ParseTransformType(s string) (TransformType, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "middle-out", "middle_out", "middleout":
		return TransformMiddleOut, nil
	case "truncate-start", "truncate_start", "truncatestart":
		return TransformTruncateStart, nil
	case "truncate-end", "truncate_end", "truncateend":
		return TransformTruncateEnd, nil
	case "summarize":
		return TransformSummarize, nil
	default:
		return "", fmt.Errorf("unknown transform type: %s", s)
	}
}
