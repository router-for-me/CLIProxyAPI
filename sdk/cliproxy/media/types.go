// Package media provides operation-scoped media execution contracts for
// embedders that own durable job state. Provider transport and auth selection
// remain with the cliproxy auth Manager / ProviderExecutor.
package media

import (
	"context"
	"encoding/json"
	"net/http"
)

// Operation identifies a media capability.
type Operation string

const (
	OpImageGeneration   Operation = "image_generation"
	OpVideoGeneration   Operation = "video_generation"
	OpSpeechSynthesis   Operation = "speech_synthesis"
	OpMusicGeneration   Operation = "music_generation"
	OpTranscription     Operation = "transcription"
	OpImageToVideo      Operation = "image_to_video"
	OpTextToVideo       Operation = "text_to_video"
)

// RetryPolicy is request-scoped and must not be replaced by global SetRetryConfig.
type RetryPolicy string

const (
	// RetryPreResponseFailoverOnly allows failover only before any HTTP response
	// or accepted provider handle. Used for paid create/submit.
	RetryPreResponseFailoverOnly RetryPolicy = "pre_response_failover_only"
	// RetryIdempotent allows retries for safe status/content queries.
	RetryIdempotent RetryPolicy = "idempotent"
	// RetryNone never automatically retries (default for cancel).
	RetryNone RetryPolicy = "none"
)

// Phase is the media lifecycle step for this call.
type Phase string

const (
	PhaseCreate  Phase = "create"
	PhaseStatus  Phase = "status"
	PhaseContent Phase = "content"
	PhaseCancel  Phase = "cancel"
)

// Request is a provider-neutral media execution request.
type Request struct {
	Provider      string
	Model         string
	Operation     Operation
	Phase         Phase
	Prompt        string
	Input         string
	Params        map[string]any
	// Handle is the opaque provider handle from a prior create (status/content/cancel).
	Handle json.RawMessage
	// ContentURL when the provider returned a temporary asset URL for content fetch.
	ContentURL string
}

// SelectedAuth identifies the auth chosen by the conductor. Never contains secrets.
type SelectedAuth struct {
	AuthID   string
	Provider string
}

// Asset describes provider content ready for caller-owned durable ingest.
type Asset struct {
	MimeType   string
	Data       []byte
	RemoteURL  string
	Width      *int
	Height     *int
	DurationMS *int
}

// Result is returned after a media operation attempt.
type Result struct {
	SelectedAuth SelectedAuth
	// Handle is the opaque provider task handle (create/status may update it).
	Handle json.RawMessage
	// Status is a provider-normalized status hint: queued|in_progress|completed|failed.
	Status string
	// SyncComplete is true when final assets are already present (sync create).
	SyncComplete bool
	Assets       []Asset
	// Usage is provider-reported usage facts (nullable members).
	Usage map[string]any
	// ErrorCode/ErrorMessage are redacted provider failure summaries.
	ErrorCode    string
	ErrorMessage string
	// HTTPResponded is true when any HTTP response was received (blocks create replay).
	HTTPResponded bool
	// AcceptedHandle is true when a provider task handle was accepted.
	AcceptedHandle bool
}

// Options control a single media execution.
type Options struct {
	RetryPolicy RetryPolicy
	// PinnedAuthID forces follow-up status/content/cancel onto the selected credential.
	PinnedAuthID string
	// Headers forwarded to provider builders when applicable.
	Headers http.Header
}

// Executor is implemented by provider-specific media executors (often also a ProviderExecutor).
type Executor interface {
	// Identifier is the provider key (matches auth Manager registration).
	Identifier() string
	// Operations lists supported operations (empty means unrestricted for that provider).
	Operations() []Operation
	// ExecuteMedia runs create/status/content/cancel for this provider.
	ExecuteMedia(ctx context.Context, req Request, opts Options) (Result, error)
}

// RetryPolicyMetadataKey carries RetryPolicy in cliproxy executor Options.Metadata.
const RetryPolicyMetadataKey = "media_retry_policy"

// PhaseMetadataKey carries Phase in metadata.
const PhaseMetadataKey = "media_phase"

// OperationMetadataKey carries Operation in metadata.
const OperationMetadataKey = "media_operation"
