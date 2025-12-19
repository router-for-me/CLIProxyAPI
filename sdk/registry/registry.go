// Package registry provides model registry interfaces for the CLI Proxy SDK.
// It defines the types and interfaces for managing available AI models
// without depending on internal packages.
package registry

import (
	"time"
)

// ModelInfo represents information about an available model.
// This struct is used to describe models that can be served by the proxy.
type ModelInfo struct {
	// ID is the unique identifier for the model
	ID string `json:"id"`
	// Object type for the model (typically "model")
	Object string `json:"object"`
	// Created timestamp when the model was created
	Created int64 `json:"created"`
	// OwnedBy indicates the organization that owns the model
	OwnedBy string `json:"owned_by"`
	// Type indicates the model type (e.g., "claude", "gemini", "openai")
	Type string `json:"type"`
	// DisplayName is the human-readable name for the model
	DisplayName string `json:"display_name,omitempty"`
	// Name is used for Gemini-style model names
	Name string `json:"name,omitempty"`
	// Version is the model version
	Version string `json:"version,omitempty"`
	// Description provides detailed information about the model
	Description string `json:"description,omitempty"`
	// InputTokenLimit is the maximum input token limit
	InputTokenLimit int `json:"inputTokenLimit,omitempty"`
	// OutputTokenLimit is the maximum output token limit
	OutputTokenLimit int `json:"outputTokenLimit,omitempty"`
	// SupportedGenerationMethods lists supported generation methods
	SupportedGenerationMethods []string `json:"supportedGenerationMethods,omitempty"`
	// ContextLength is the context window size
	ContextLength int `json:"context_length,omitempty"`
	// MaxCompletionTokens is the maximum completion tokens
	MaxCompletionTokens int `json:"max_completion_tokens,omitempty"`
	// SupportedParameters lists supported parameters
	SupportedParameters []string `json:"supported_parameters,omitempty"`
	// Thinking holds provider-specific reasoning/thinking budget capabilities.
	Thinking *ThinkingSupport `json:"thinking,omitempty"`
}

// ThinkingSupport describes a model family's supported internal reasoning budget range.
type ThinkingSupport struct {
	// Min is the minimum allowed thinking budget (inclusive).
	Min int `json:"min,omitempty"`
	// Max is the maximum allowed thinking budget (inclusive).
	Max int `json:"max,omitempty"`
	// ZeroAllowed indicates whether 0 is a valid value (to disable thinking).
	ZeroAllowed bool `json:"zero_allowed,omitempty"`
	// DynamicAllowed indicates whether -1 is a valid value (dynamic thinking budget).
	DynamicAllowed bool `json:"dynamic_allowed,omitempty"`
	// Levels defines discrete reasoning effort levels (e.g., "low", "medium", "high").
	Levels []string `json:"levels,omitempty"`
}

// ModelRegistration tracks a model's availability.
type ModelRegistration struct {
	// Info contains the model metadata
	Info *ModelInfo
	// Count is the number of active clients that can provide this model
	Count int
	// LastUpdated tracks when this registration was last modified
	LastUpdated time.Time
	// QuotaExceededClients tracks which clients have exceeded quota for this model
	QuotaExceededClients map[string]*time.Time
	// Providers tracks available clients grouped by provider identifier
	Providers map[string]int
	// SuspendedClients tracks temporarily disabled clients keyed by client ID
	SuspendedClients map[string]string
}

// ModelRegistry defines the interface for managing available models.
// Implementations should be safe for concurrent use.
type ModelRegistry interface {
	// RegisterClient registers a client and its supported models.
	// Parameters:
	//   - clientID: Unique identifier for the client
	//   - clientProvider: Provider name (e.g., "gemini", "claude", "openai")
	//   - models: List of models that this client can provide
	RegisterClient(clientID, clientProvider string, models []*ModelInfo)

	// UnregisterClient removes a client and decrements counts for its models.
	// Parameters:
	//   - clientID: Unique identifier for the client to remove
	UnregisterClient(clientID string)

	// GetAvailableModels returns all models that have at least one available client.
	// Parameters:
	//   - handlerType: The handler type to filter models for (e.g., "openai", "claude", "gemini")
	// Returns:
	//   - []map[string]any: List of available models in the requested format
	GetAvailableModels(handlerType string) []map[string]any

	// GetModelCount returns the number of available clients for a specific model.
	// Parameters:
	//   - modelID: The model ID to check
	// Returns:
	//   - int: Number of available clients for the model
	GetModelCount(modelID string) int

	// GetModelInfo returns the registered ModelInfo for the given model ID, if present.
	// Returns nil if the model is unknown to the registry.
	GetModelInfo(modelID string) *ModelInfo

	// GetModelProviders returns provider identifiers that currently supply the given model.
	// Parameters:
	//   - modelID: The model ID to check
	// Returns:
	//   - []string: Provider identifiers ordered by availability count (descending)
	GetModelProviders(modelID string) []string

	// SetModelQuotaExceeded marks a model as quota exceeded for a specific client.
	SetModelQuotaExceeded(clientID, modelID string)

	// ClearModelQuotaExceeded removes quota exceeded status for a model and client.
	ClearModelQuotaExceeded(clientID, modelID string)

	// SuspendClientModel marks a client's model as temporarily unavailable.
	SuspendClientModel(clientID, modelID, reason string)

	// ResumeClientModel clears a previous suspension.
	ResumeClientModel(clientID, modelID string)

	// ClientSupportsModel reports whether the client registered support for modelID.
	ClientSupportsModel(clientID, modelID string) bool

	// GetModelsForClient returns the models registered for a specific client.
	GetModelsForClient(clientID string) []*ModelInfo
}
