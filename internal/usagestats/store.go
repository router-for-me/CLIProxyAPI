package usagestats

import (
	"context"
	"time"
)

// Store is the persistence interface for usage statistics events.
// Implementations must be safe for concurrent use.
type Store interface {
	// EnsureSchema creates required tables and indexes if they do not exist.
	// It is idempotent and safe to call on every startup.
	EnsureSchema(ctx context.Context) error

	// Append inserts a sanitized usage event.
	// Errors should be logged but must not fail the caller's request.
	Append(ctx context.Context, event Event) error

	// Summary returns aggregated statistics and optional recent records.
	Summary(ctx context.Context, query Query) (*SummaryResult, error)

	// Close releases underlying resources.
	Close() error
}

// Query defines parameters for a usage statistics summary query.
type Query struct {
	From        time.Time
	To          time.Time
	GroupBy     GroupBy
	RecentLimit int
}

// TokenSummary holds aggregated token counts.
type TokenSummary struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

// CostSummary holds aggregated cost information.
type CostSummary struct {
	// Known indicates whether all aggregated records had a matching price.
	// When false, total_usd reflects only priced records; unknown counts are separate.
	Known         bool    `json:"known"`
	TotalUSD      float64 `json:"total_usd"`
	TotalMicros   int64   `json:"-"`
	UnknownReqs   int64   `json:"unknown_requests"`
	UnknownTokens int64   `json:"unknown_tokens"`
}

// SummaryRow is one row of grouped aggregation results.
type SummaryRow struct {
	Key     string      `json:"key"`
	Reqs    int64       `json:"requests"`
	OK      int64       `json:"success"`
	Fail    int64       `json:"failed"`
	Tokens  TokenSummary `json:"tokens"`
	Cost    CostSummary  `json:"cost"`
}

// SummaryTotal is the overall summary across all matching records.
type SummaryTotal struct {
	Reqs   int64        `json:"requests"`
	OK     int64        `json:"success"`
	Fail   int64        `json:"failed"`
	Tokens TokenSummary `json:"tokens"`
	Cost   CostSummary  `json:"cost"`
}

// RecentRecord is a sanitized recent usage record for API responses.
type RecentRecord struct {
	Time            string     `json:"time"`
	RequestID       string     `json:"request_id"`
	Provider        string     `json:"provider"`
	Model           string     `json:"model"`
	Alias           string     `json:"alias"`
	StatusCode      int        `json:"status_code"`
	Failed          bool       `json:"failed"`
	Tokens          TokenSummary `json:"tokens"`
	Cost            CostSummary  `json:"cost"`
	APIKeyID        string     `json:"api_key_id"`
	AuthIndex       string     `json:"auth_index"`
	AuthType        string     `json:"auth_type"`
	LatencyMs       int64      `json:"latency_ms"`
	ErrorType       string     `json:"error_type"`
	ReasoningEffort string     `json:"reasoning_effort"`
}

// SummaryResult is the full API response for usage statistics queries.
type SummaryResult struct {
	From   string        `json:"from"`
	To     string        `json:"to"`
	GroupBy string       `json:"group_by"`
	Summary SummaryTotal `json:"summary"`
	Groups  []SummaryRow `json:"groups"`
	Recent  []RecentRecord `json:"recent,omitempty"`
}
