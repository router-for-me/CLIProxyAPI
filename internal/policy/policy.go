// Package policy enforces per-API-key limits (RPM, hourly rate, model access,
// and time-windowed budget caps) on incoming requests. It is the single owner
// of the in-memory rate-limit state and the on-demand budget-window reader
// against PostgreSQL. The package is intentionally stateless across restarts
// for sliding-window counters (resets are acceptable); budget windows are
// persisted to the usage_windows table and survive restarts.
package policy

import (
	"context"
	"strings"
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
	Input         int64
	Output        int64
	Reasoning     int64
	Cached        int64 // cache-read tokens (subset-of-input semantics already normalized by the parser)
	CacheCreation int64 // cache-write tokens
	Total         int64
	Cost          float64
}

// computeCostFromTokens mirrors the UsageFlusher's ComputeCost computation so
// the budget windows tracked by the policy service stay in sync with the
// cost_usd persisted on usage_events. Argument order matches store.ComputeCost
// (cached = cache-creation / write, cacheRead = cache-read):
//
//	input         × InputPer1M
//	output        × OutputPer1M
//	reasoning     × ReasoningPer1M
//	cacheCreation × CachedInputPer1M (write surcharge)
//	cached        × CachedReadPer1M    (read discount)
//
// Pre-fix the policy Consume path always wrote Cost=0, so usage_windows.cost_usd
// (and budget enforcement against USD caps) was permanently stuck at 0 even
// when pricing was configured. Centralizing the formula here makes that
// contract testable without a live PG connection.
func computeCostFromTokens(p store.Pricing, tokens TokenCounts) float64 {
	return store.ComputeCost(p, tokens.Input, tokens.Output,
		tokens.Reasoning, tokens.CacheCreation, tokens.Cached)
}

// resolveDiscount returns the discount percentage (0-100) that applies to a
// request for the given model under the supplied policy. A per-model entry
// (keyed by the lowercased model id) takes precedence over the group-level
// default; 0 means "no discount". Returns 0 when the policy is nil or carries
// no discount, so callers can apply `cost *= (1 - pct/100)` unconditionally.
// Mirrored by store.ModelGroup resolution so the flusher (events path) and
// Consume (budget-windows path) apply the identical factor and stay in sync.
func resolveDiscount(p *store.Policy, model string) float64 {
	if p == nil || model == "" {
		return 0
	}
	if v, ok := p.ModelDiscountPcts[strings.ToLower(model)]; ok && v > 0 {
		return v
	}
	if p.DiscountPct != nil && *p.DiscountPct > 0 {
		return *p.DiscountPct
	}
	return 0
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
	// ModelRoutes optionally pins specific allowed model IDs to a subset of
	// upstream providers. See store.Policy.ModelRoutes for semantics.
	ModelRoutes []store.ModelRoute
	// DiscountPct is the group-level default discount percentage (0-100) when
	// a model group is attached to this policy. nil/0 = no discount. Applied to
	// the computed cost in Consume (budget windows) so usage_windows.cost_usd
	// stays in sync with the post-discount cost_usd persisted on usage_events.
	DiscountPct *float64
	// ModelDiscountPcts carries per-model discount overrides (keyed by model id)
	// that take precedence over DiscountPct. Populated by the group-override
	// snapshot path; never persisted on the policy row itself.
	ModelDiscountPcts map[string]float64
}

// ModelLists carries the resolved AllowedModels / BlockedModels for a
// principal's policy (after Model Group override). The policy middleware
// stashes it into the gin context so /v1/models can filter the registry
// catalog per API key without re-running the snapshot.
type ModelLists struct {
	Allowed []string
	Blocked []string
}

// APIKeySnapshot pairs a key's identity with its resolved policy for caching.
type APIKeySnapshot struct {
	APIKey store.APIKey
	Policy *store.Policy
	// InternalUser is resolved when the snapshotted API key attaches to an
	// internal user owner. nil when the key is unassigned or when the
	// UserStore was not wired (older deployments).
	InternalUser *store.InternalUser
	LoadedAt     time.Time
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

	// ResolvedRoutes returns the per-allowed-model upstream provider routes
	// configured on the principal's policy (snapshot-cached). Returns nil when
	// the service is inactive, no policy is attached, or no routes are set.
	// The middleware stashes the result into the request context so the
	// handler can confine provider selection to the pinned set.
	ResolvedRoutes(ctx context.Context, principal string) []store.ModelRoute

	// ResolvedModelLists returns the post-override AllowedModels/BlockedModels
	// for the principal's policy (snapshot-cached, includes Model Group
	// override). Returns nil slices when the service is inactive, no policy is
	// attached, or the principal is unknown (legacy/file-only key — in which
	// case /v1/models lists everything, preserving default behavior). The
	// middleware stashes the result so /v1/models can filter the catalog per
	// API key without an extra DB round-trip.
	ResolvedModelLists(ctx context.Context, principal string) (allowed, blocked []string)

	// ResolvedIPLists returns the AllowedIPs/BlockedIPs configured on the
	// principal's policy (snapshot-cached). Returns nil slices when the service
	// is inactive, no policy is attached, the principal is unknown (legacy/
	// file-only key), or both lists are empty — callers treat nil as "do not
	// filter by IP" so non-PG keys keep default behavior. The middleware uses
	// this to enforce the per-API-key source IP allowlist/blocklist right
	// after svc.Check, before AcquireParallel, without an extra DB round-trip.
	ResolvedIPLists(ctx context.Context, principal string) (allowed, blocked []string)

	// Consume records tokens + cost against the budget windows. Called from
	// the usage plugin sink after the upstream response has been parsed.
	// model is the resolved upstream model (e.g. "glm-5.2-flex"); alias is the
	// client-requested model name (e.g. "glm-5.2"). Pricing rows are keyed by
	// the alias-facing catalog id, so Consume resolves cost via the alias
	// fallback when the resolved model has no pricing row.
	Consume(ctx context.Context, principal, model, alias string, tokens TokenCounts) error

	// InvalidateKey drops the cached snapshot for the supplied principal.
	// Called by management handlers after a policy/key mutation so the next
	// request re-reads from PG.
	InvalidateKey(ctx context.Context, principal string) error

	// InvalidateAll flushes the entire cache. Used by bulk operations and
	// tests.
	InvalidateAll()

	// AcquireParallel reserves an in-flight slot for the principal under the
	// configured max_parallel_requests cap (per-key takes precedence; when
	// unset, falls back to the owner user's cap). Returns false when the
	// limit is hit — the middleware then surfaces HTTP 429. The middleware
	// MUST call ReleaseParallel with the same principal at request end.
	AcquireParallel(ctx context.Context, principal string) (bool, error)

	// ReleaseParallel releases an in-flight slot acquired by AcquireParallel.
	// Idempotent; safe to call after a failed Acquire (no-op).
	ReleaseParallel(ctx context.Context, principal string) error
}
