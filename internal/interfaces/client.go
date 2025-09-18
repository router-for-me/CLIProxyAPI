// Package interfaces defines the core interfaces and shared structures for the CLI Proxy API server.
// These interfaces provide a common contract for different components of the application,
// such as AI service clients, API handlers, and data models.
package interfaces

import (
	"context"
	"sync"
)

// Client defines the interface that all AI API clients must implement.
// This interface provides methods for interacting with various AI services
// including sending messages, streaming responses, and managing authentication.
type Client interface {
	// Type returns the client type identifier (e.g., "gemini", "claude").
	Type() string

	// GetRequestMutex returns the mutex used to synchronize requests for this client.
	// This ensures that only one request is processed at a time for quota management.
	GetRequestMutex() *sync.Mutex

	// GetUserAgent returns the User-Agent string used for HTTP requests.
	GetUserAgent() string

	// SendRawMessage sends a raw JSON message to the AI service without translation.
	// This method is used when the request is already in the service's native format.
	SendRawMessage(ctx context.Context, modelName string, rawJSON []byte, alt string) ([]byte, *ErrorMessage)

	// SendRawMessageStream sends a raw JSON message and returns streaming responses.
	// Similar to SendRawMessage but for streaming responses.
	SendRawMessageStream(ctx context.Context, modelName string, rawJSON []byte, alt string) (<-chan []byte, <-chan *ErrorMessage)

	// SendRawTokenCount sends a token count request to the AI service.
	// This method is used to estimate the number of tokens in a given text.
	SendRawTokenCount(ctx context.Context, modelName string, rawJSON []byte, alt string) ([]byte, *ErrorMessage)

	// SaveTokenToFile saves the client's authentication token to a file.
	// This is used for persisting authentication state between sessions.
	SaveTokenToFile() error

	// IsModelQuotaExceeded checks if the specified model has exceeded its quota.
	// This helps with load balancing and automatic failover to alternative models.
	IsModelQuotaExceeded(model string) bool

	// GetEmail returns the email associated with the client's authentication.
	// This is used for logging and identification purposes.
	GetEmail() string

	// CanProvideModel checks if the client can provide the specified model.
	CanProvideModel(modelName string) bool

	// Provider returns the name of the AI service provider (e.g., "gemini", "claude").
	Provider() string

	RefreshTokens(ctx context.Context) error
}

// UnregisterReason describes the context for unregistering a client instance.
type UnregisterReason string

const (
	// UnregisterReasonReload indicates a full reload is replacing the client.
	UnregisterReasonReload UnregisterReason = "reload"
	// UnregisterReasonShutdown indicates the service is shutting down.
	UnregisterReasonShutdown UnregisterReason = "shutdown"
	// UnregisterReasonAuthFileRemoved indicates the underlying auth file was deleted.
	UnregisterReasonAuthFileRemoved UnregisterReason = "auth-file-removed"
	// UnregisterReasonAuthFileUpdated indicates the auth file content was modified.
	UnregisterReasonAuthFileUpdated UnregisterReason = "auth-file-updated"
)
