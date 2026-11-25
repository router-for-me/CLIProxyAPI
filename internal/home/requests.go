package home

import (
	"context"
	"time"
)

// SchedulerContext contains only transport-safe request identity, never policy overrides.
type SchedulerContext struct {
	Stream             bool   `json:"stream,omitempty"`
	RequestID          string `json:"request_id,omitempty"`
	TraceID            string `json:"trace_id,omitempty"`
	ExecutionSessionID string `json:"execution_session_id,omitempty"`
	RequestedModel     string `json:"requested_model,omitempty"`
}

type schedulerContextKey struct{}
type executorPluginsContextKey struct{}

// WithSchedulerContext carries request-scoped scheduling inputs through legacy dispatcher interfaces.
func WithSchedulerContext(ctx context.Context, input SchedulerContext) context.Context {
	return context.WithValue(ctx, schedulerContextKey{}, input)
}

// WithExecutorPlugins carries the ready plugin IDs and their supported auth providers.
func WithExecutorPlugins(ctx context.Context, plugins map[string][]string) context.Context {
	return context.WithValue(ctx, executorPluginsContextKey{}, plugins)
}

type authDispatchRequest struct {
	SchedulerContext    *SchedulerContext   `json:"scheduler_context,omitempty"`
	Type                string              `json:"type"`
	Model               string              `json:"model"`
	Count               int                 `json:"count"`
	ConcurrencyProtocol int                 `json:"concurrency_protocol,omitempty"`
	ExecutorPlugins     map[string][]string `json:"executor_plugins,omitzero"`
	SessionID           string              `json:"session_id,omitempty"`
	ParentSessionID     string              `json:"parent_session_id,omitempty"`
	NodeKind            string              `json:"node_kind,omitempty"`
	Headers             map[string]string   `json:"headers,omitempty"`
	CredentialPolicy    string              `json:"credential_policy,omitempty"`
	RetryRound          *int                `json:"retry_round,omitempty"`
	ExcludedAuthIDs     *[]string           `json:"excluded_auth_ids,omitempty"`
	PinnedAuthID        string              `json:"pinned_auth_id,omitempty"`
}

type modelsRequest struct {
	Type    string            `json:"type"`
	Headers map[string]string `json:"headers,omitempty"`
	Query   map[string]string `json:"query,omitempty"`
}

type refreshRequest struct {
	Type                      string `json:"type"`
	AuthIndex                 string `json:"auth_index"`
	ObservedAccessTokenSHA256 string `json:"access_token_sha256,omitempty"`
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
