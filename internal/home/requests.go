package home

import "time"

// DispatchSession describes the session identity CPA resolved for a request.
// CPA sees the full HTTP request, so its classification is authoritative; Home
// validates the bounded values instead of re-deriving them from headers.
type DispatchSession struct {
	// ID is the canonical namespaced session identifier CPA routed on.
	ID string `json:"id"`
	// Aliases lists other identifiers addressing the same session, such as a
	// Responses conversation ID observed alongside a prompt_cache_key. They let
	// Home reconcile sessions that different CPA nodes saw through different signals.
	Aliases []string `json:"aliases,omitempty"`
	// Source names the signal the identifier came from.
	Source string `json:"source,omitempty"`
	// Confidence reports how strongly the identifier represents one conversation.
	Confidence string `json:"confidence,omitempty"`
	// Scope reports whether the identifier addresses a session, thread, user, or transport.
	Scope string `json:"scope,omitempty"`
	// ClientType names the detected downstream client.
	ClientType string `json:"client_type,omitempty"`
	// ThreadID and ParentThreadID are observability fields for sub-agent and fork
	// relationships. They never replace the root session.
	ThreadID       string `json:"thread_id,omitempty"`
	ParentThreadID string `json:"parent_thread_id,omitempty"`
	// RequestKind, ThreadSource and TurnID are observability fields the client
	// reported about this request: what kind of work it is (a conversation turn
	// versus compaction, title generation or other housekeeping), where the thread
	// came from, and which client turn it belongs to. They never affect routing.
	RequestKind  string `json:"request_kind,omitempty"`
	ThreadSource string `json:"thread_source,omitempty"`
	TurnID       string `json:"turn_id,omitempty"`
	// ClientProvided reports whether the client sent the identifier itself.
	ClientProvided bool `json:"client_provided,omitempty"`
}

type authDispatchRequest struct {
	Type                string `json:"type"`
	Model               string `json:"model"`
	Count               int    `json:"count"`
	ConcurrencyProtocol int    `json:"concurrency_protocol,omitempty"`
	// SessionID stays for Home versions that predate the structured session object.
	SessionID string `json:"session_id,omitempty"`
	// Session carries the structured identity. Older Home versions ignore it.
	Session *DispatchSession  `json:"session,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type modelsRequest struct {
	Type    string            `json:"type"`
	Headers map[string]string `json:"headers,omitempty"`
	Query   map[string]string `json:"query,omitempty"`
}

type refreshRequest struct {
	Type      string `json:"type"`
	AuthIndex string `json:"auth_index"`
}

type InFlightFrameKind string
type InFlightAccountedStatus string

const (
	InFlightFramePart     InFlightFrameKind       = "part"
	InFlightFrameOverflow InFlightFrameKind       = "overflow"
	InFlightAccounted     InFlightAccountedStatus = "accounted"
	InFlightUnaccounted   InFlightAccountedStatus = "unaccounted"
)

type InFlightAggregate struct {
	CredentialID string                  `json:"credential_id"`
	Model        string                  `json:"model"`
	Status       InFlightAccountedStatus `json:"status"`
	Count        int64                   `json:"count"`
}

type InFlightRequestDetail struct {
	RequestID    string    `json:"request_id"`
	CredentialID string    `json:"credential_id"`
	Model        string    `json:"model"`
	RequestKind  string    `json:"request_kind"`
	StartedAt    time.Time `json:"started_at"`
}

type InFlightSnapshotFrame struct {
	Kind                InFlightFrameKind       `json:"kind"`
	Revision            int64                   `json:"revision"`
	ObservedAt          time.Time               `json:"observed_at"`
	BarrierRevision     int64                   `json:"barrier_revision"`
	PartIndex           *int                    `json:"part_index,omitempty"`
	PartCount           *int                    `json:"part_count,omitempty"`
	DetailsTruncated    bool                    `json:"details_truncated,omitempty"`
	Aggregates          []InFlightAggregate     `json:"aggregates,omitempty"`
	Details             []InFlightRequestDetail `json:"details,omitempty"`
	AggregateGroupCount int                     `json:"aggregate_group_count,omitempty"`
}
