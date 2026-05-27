// Package common provides shared validation and sanitization utilities
// for tool calls across all translator implementations.
package common

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"
)

// Tool call ID validation constants.
const (
	// MaxToolCallIDLength is the maximum allowed length for tool call IDs.
	MaxToolCallIDLength = 64
	// MaxToolNameLength is the maximum allowed length for tool names.
	MaxToolNameLength = 64
)

var (
	// validToolCallIDPattern matches Claude-compatible tool_use IDs.
	validToolCallIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	// invalidToolCallIDChars matches characters that need sanitization in tool call IDs.
	invalidToolCallIDChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	// toolCallIDCounter provides unique fallback IDs.
	toolCallIDCounter uint64
)

// ToolCallValidationError describes a validation failure for a tool call.
type ToolCallValidationError struct {
	Field   string // Field that failed validation (e.g., "call_id", "name", "arguments")
	Message string // Human-readable error message
}

func (e *ToolCallValidationError) Error() string {
	return fmt.Sprintf("tool call validation error [%s]: %s", e.Field, e.Message)
}

// ValidatedToolCall represents a tool call that has passed validation.
type ValidatedToolCall struct {
	CallID    string          // Validated and sanitized call ID
	Name      string          // Validated tool name
	Arguments json.RawMessage // Validated JSON arguments
	Type      string          // "function_call" or "custom_tool_call"
}

// ValidatedToolOutput represents a tool output that has passed validation.
type ValidatedToolOutput struct {
	CallID string          // Validated call ID
	Output json.RawMessage // Validated output content
	Type   string          // "function_call_output" or "custom_tool_call_output"
}

// ValidateToolCallID validates and sanitizes a tool call ID.
// Returns the sanitized ID and an error if the ID is completely invalid.
func ValidateToolCallID(id string) (string, *ToolCallValidationError) {
	id = strings.TrimSpace(id)

	if id == "" {
		return "", &ToolCallValidationError{
			Field:   "call_id",
			Message: "tool call ID is empty",
		}
	}

	// Sanitize: replace invalid characters with underscore
	sanitized := invalidToolCallIDChars.ReplaceAllString(id, "_")

	// Truncate if too long
	if len(sanitized) > MaxToolCallIDLength {
		sanitized = sanitized[:MaxToolCallIDLength]
	}

	// Generate fallback if sanitization resulted in empty string
	if sanitized == "" {
		sanitized = generateFallbackToolCallID()
	}

	return sanitized, nil
}

// SanitizeToolCallID sanitizes a tool call ID for Claude API compatibility.
// This is a convenience wrapper around ValidateToolCallID that returns only
// the sanitized ID, suitable for use in hot paths where validation errors
// are handled by dropping the item.
// Unlike ValidateToolCallID, empty IDs get a generated fallback instead of an error.
func SanitizeToolCallID(id string) string {
	// Replace invalid characters with underscore (no trimming, matching original behavior)
	sanitized := invalidToolCallIDChars.ReplaceAllString(id, "_")

	// Truncate if too long
	if len(sanitized) > MaxToolCallIDLength {
		sanitized = sanitized[:MaxToolCallIDLength]
	}

	// Generate fallback if sanitization resulted in empty string
	if sanitized == "" {
		sanitized = generateFallbackToolCallID()
	}

	return sanitized
}

// ValidateToolName validates a tool name.
// Returns an error if the name is empty.
func ValidateToolName(name string) *ToolCallValidationError {
	name = strings.TrimSpace(name)

	if name == "" {
		return &ToolCallValidationError{
			Field:   "name",
			Message: "tool name is empty",
		}
	}

	return nil
}

// ValidateToolCallJSON validates that the given bytes represent valid JSON
// that can be used as tool call arguments.
func ValidateToolCallJSON(data []byte) *ToolCallValidationError {
	if len(data) == 0 {
		return nil // Empty arguments are valid for some tools
	}

	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}

	// Must be a JSON object for tool call arguments
	if !gjson.Valid(trimmed) {
		return &ToolCallValidationError{
			Field:   "arguments",
			Message: "invalid JSON: " + truncateString(trimmed, 100),
		}
	}

	parsed := gjson.Parse(trimmed)
	if !parsed.IsObject() {
		return &ToolCallValidationError{
			Field:   "arguments",
			Message: "arguments must be a JSON object, got: " + parsed.Type.String(),
		}
	}

	return nil
}

// ValidateToolCallPairing checks that every tool_call has a corresponding
// tool_result and vice versa. Returns lists of orphaned calls and outputs.
func ValidateToolCallPairing(items []json.RawMessage) (orphanedCalls []string, orphanedOutputs []string) {
	callIDs := make(map[string]struct{})
	outputIDs := make(map[string]struct{})

	for _, item := range items {
		if len(item) == 0 {
			continue
		}

		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())

		if callID == "" {
			continue
		}

		switch itemType {
		case "function_call", "custom_tool_call":
			callIDs[callID] = struct{}{}
		case "function_call_output", "custom_tool_call_output":
			outputIDs[callID] = struct{}{}
		}
	}

	// Find orphaned outputs (outputs without corresponding calls)
	for id := range outputIDs {
		if _, hasCall := callIDs[id]; !hasCall {
			orphanedOutputs = append(orphanedOutputs, id)
		}
	}

	// Find orphaned calls (calls without corresponding outputs)
	for id := range callIDs {
		if _, hasOutput := outputIDs[id]; !hasOutput {
			orphanedCalls = append(orphanedCalls, id)
		}
	}

	return orphanedCalls, orphanedOutputs
}

// RepairToolCallPairing removes orphaned tool outputs (outputs without
// matching calls). Orphaned calls (calls without matching outputs) are
// preserved since their outputs may arrive in a subsequent request.
// Empty items are dropped.
func RepairToolCallPairing(items []json.RawMessage) []json.RawMessage {
	if items == nil {
		return nil
	}

	type callInfo struct {
		index int
	}
	type outputInfo struct {
		index int
	}

	calls := make(map[string]callInfo)
	outputs := make(map[string]outputInfo)

	for i, item := range items {
		if len(item) == 0 {
			continue
		}
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
		if callID == "" {
			continue
		}
		switch itemType {
		case "function_call", "custom_tool_call":
			calls[callID] = callInfo{index: i}
		case "function_call_output", "custom_tool_call_output":
			outputs[callID] = outputInfo{index: i}
		}
	}

	// Identify orphaned outputs
	orphanedOutputs := make(map[string]struct{})
	for id := range outputs {
		if _, hasCall := calls[id]; !hasCall {
			orphanedOutputs[id] = struct{}{}
		}
	}

	if len(orphanedOutputs) == 0 {
		// No orphans; just filter empty items
		filtered := make([]json.RawMessage, 0, len(items))
		for _, item := range items {
			if len(item) > 0 {
				filtered = append(filtered, item)
			}
		}
		if len(filtered) == len(items) {
			return items
		}
		return filtered
	}

	// Build result excluding orphaned outputs and empty items
	filtered := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		if IsToolCallOutputType(itemType) {
			callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
			if _, orphaned := orphanedOutputs[callID]; orphaned {
				continue
			}
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// ValidateAndSanitizeToolCall validates and sanitizes a single tool call item.
// Returns a ValidatedToolCall if valid, or an error describing the problem.
func ValidateAndSanitizeToolCall(item []byte) (*ValidatedToolCall, *ToolCallValidationError) {
	if len(item) == 0 {
		return nil, &ToolCallValidationError{
			Field:   "item",
			Message: "empty tool call item",
		}
	}

	itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
	if !IsToolCallType(itemType) {
		return nil, &ToolCallValidationError{
			Field:   "type",
			Message: "not a tool call type: " + itemType,
		}
	}

	callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
	sanitizedID, err := ValidateToolCallID(callID)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(gjson.GetBytes(item, "name").String())
	if nameErr := ValidateToolName(name); nameErr != nil {
		return nil, nameErr
	}

	arguments := gjson.GetBytes(item, "arguments")
	if argErr := ValidateToolCallJSON([]byte(arguments.Raw)); argErr != nil {
		return nil, argErr
	}

	return &ValidatedToolCall{
		CallID:    sanitizedID,
		Name:      name,
		Arguments: json.RawMessage(arguments.Raw),
		Type:      itemType,
	}, nil
}

// ValidateAndSanitizeToolOutput validates and sanitizes a single tool output item.
// Returns a ValidatedToolOutput if valid, or an error describing the problem.
func ValidateAndSanitizeToolOutput(item []byte) (*ValidatedToolOutput, *ToolCallValidationError) {
	if len(item) == 0 {
		return nil, &ToolCallValidationError{
			Field:   "item",
			Message: "empty tool output item",
		}
	}

	itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
	if !IsToolCallOutputType(itemType) {
		return nil, &ToolCallValidationError{
			Field:   "type",
			Message: "not a tool output type: " + itemType,
		}
	}

	callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
	sanitizedID, err := ValidateToolCallID(callID)
	if err != nil {
		return nil, err
	}

	output := gjson.GetBytes(item, "output")

	return &ValidatedToolOutput{
		CallID: sanitizedID,
		Output: json.RawMessage(output.Raw),
		Type:   itemType,
	}, nil
}

// IsToolCallType returns true if the type string represents a tool call.
func IsToolCallType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "function_call", "custom_tool_call":
		return true
	default:
		return false
	}
}

// IsToolCallOutputType returns true if the type string represents a tool output.
func IsToolCallOutputType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "function_call_output", "custom_tool_call_output":
		return true
	default:
		return false
	}
}

// generateFallbackToolCallID creates a unique tool call ID.
func generateFallbackToolCallID() string {
	return fmt.Sprintf("toolu_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&toolCallIDCounter, 1))
}

// truncateString truncates a string to maxLen characters, appending "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
