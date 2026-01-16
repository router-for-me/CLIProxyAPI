// Package thinking provides unified thinking configuration processing logic.
package thinking

import "net/http"

// ErrorCode represents the type of thinking configuration error.
type ErrorCode string

// Error codes for thinking configuration processing.
const (
	// ErrInvalidSuffix indicates the suffix format cannot be parsed.
	// Example: "model(abc" (missing closing parenthesis)
	ErrInvalidSuffix ErrorCode = "INVALID_SUFFIX"

	// ErrUnknownLevel indicates the level value is not in the valid list.
	// Example: "model(ultra)" where "ultra" is not a valid level
	ErrUnknownLevel ErrorCode = "UNKNOWN_LEVEL"

	// ErrThinkingNotSupported indicates the model does not support thinking.
	// Example: claude-haiku-4-5 does not have thinking capability
	ErrThinkingNotSupported ErrorCode = "THINKING_NOT_SUPPORTED"

	// ErrLevelNotSupported indicates the model does not support level mode.
	// Example: using level with a budget-only model
	ErrLevelNotSupported ErrorCode = "LEVEL_NOT_SUPPORTED"

	// ErrProviderMismatch indicates the provider does not match the model.
	// Example: applying Claude format to a Gemini model
	ErrProviderMismatch ErrorCode = "PROVIDER_MISMATCH"
)

// ThinkingError represents an error that occurred during thinking configuration processing.
//
// This error type provides structured information about the error, including:
//   - Code: A machine-readable error code for programmatic handling
//   - Message: A human-readable description of the error
//   - Model: The model name related to the error (optional)
//   - Details: Additional context information (optional)
type ThinkingError struct {
	// Code is the machine-readable error code
	Code ErrorCode
	// Message is the human-readable error description.
	// Should be lowercase, no trailing period, with context if applicable.
	Message string
	// Model is the model name related to this error (optional)
	Model string
	// Details contains additional context information (optional)
	Details map[string]interface{}
}

// Error implements the error interface.
// Returns the message directly without code prefix.
// Use Code field for programmatic error handling.
func (e *ThinkingError) Error() string {
	return e.Message
}

// NewThinkingError creates a new ThinkingError with the given code and message.
func NewThinkingError(code ErrorCode, message string) *ThinkingError {
	return &ThinkingError{
		Code:    code,
		Message: message,
	}
}

// NewThinkingErrorWithModel creates a new ThinkingError with model context.
func NewThinkingErrorWithModel(code ErrorCode, message, model string) *ThinkingError {
	return &ThinkingError{
		Code:    code,
		Message: message,
		Model:   model,
	}
}

// StatusCode implements a portable status code interface for HTTP handlers.
func (e *ThinkingError) StatusCode() int {
	return http.StatusBadRequest
}
