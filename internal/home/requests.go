package home

import "time"

type authDispatchRequest struct {
	Type                string            `json:"type"`
	Model               string            `json:"model"`
	Count               int               `json:"count"`
	ConcurrencyProtocol int               `json:"concurrency_protocol,omitempty"`
	SessionID           string            `json:"session_id,omitempty"`
	Headers             map[string]string `json:"headers,omitempty"`
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
