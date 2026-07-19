// Package policy enforces per-API-key limits (RPM, hourly rate, model access,
// and time-windowed budget caps) on incoming requests. It is the single owner
// of the in-memory rate-limit state and the on-demand budget-window reader
// against PostgreSQL. The package is intentionally stateless across restarts
// for sliding-window counters (resets are acceptable); budget windows are
// persisted to the usage_windows table and survive restarts.
package policy

import (
	"context"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// Decision is the outcome of a Check call.
type Decision struct {
	Allow      bool
	Reason     string // populated when Allow=false
	StatusCode int    // HTTP status code to surface on reject (default 403)
}

// TokenCounts carries the per-request usage that Consume applies toward the
// budget windows. All fields are best-effort: callers that cannot determine a
// given counter should pass zero rather than skip the call.
type TokenCounts struct {
	Input     int64
	Output    int64
	Reasoning int64
	Cached    int64
	Total     int64
	Cost      float64
}

// Policy is a store-level policy snapshot cached in memory to avoid a DB
// round-trip on every request. Mirrors store.Policy but inlined here so the
// policy package does not need to extend the struct when adding fields.
type Policy struct {
	APIKeyID         string
	RPMLimit         *int
	HourlyRateLimit  *int
	BudgetHourlyUSD  *float64
	BudgetWeeklyUSD  *float64
	BudgetMonthlyUSD *float64
	AllowedModels    []string
	BlockedModels    []string
}

// APIKeySnapshot pairs a key's identity with its resolved policy for caching.
type APIKeySnapshot struct {
	APIKey   store.APIKey
	Policy   *store.Policy
	LoadedAt time.Time
}

// PolicyService is the contract consumed by request middleware and by the
// usage-plugin sink.
type PolicyService interface {
	// Active reports whether the service is enabled. The middleware uses this
	// to fast-path pass-through when no PG backend is wired (file-only mode).
	Active() bool

	// Check evaluates whether the principal (the plaintext API key string,
	// which is what the gin context exposes as userApiKey) is permitted to
	// issue a request for the supplied model right now. RPM and hourly-rate
	// counters are incremented synchronously here; budget windows are read
	// but their increment happens via Consume (after tokens are known).
	Check(ctx context.Context, principal, model string) (Decision, error)

	// Consume records tokens + cost against the budget windows. Called from
	// the usage plugin sink after the upstream response has been parsed.
	Consume(ctx context.Context, principal, model string, tokens TokenCounts) error

	// InvalidateKey drops the cached snapshot for the supplied principal.
	// Called by management handlers after a policy/key mutation so the next
	// request re-reads from PG.
	InvalidateKey(ctx context.Context, principal string) error

	// InvalidateAll flushes the entire cache. Used by bulk operations and
	// tests.
	InvalidateAll()
}
