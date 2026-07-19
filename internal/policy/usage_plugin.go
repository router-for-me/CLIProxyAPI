package policy

import (
	"context"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// usagePlugin is a coreusage.Plugin adapter that propagates token+cost
// information from each completed request to the PolicyService so budget
// windows are incremented. It does not block the publish goroutine: Consume
// is invoked synchronously but is itself a fast PG upsert (single small
// INSERT...ON CONFLICT). When PolicyService is not active the plugin is a
// no-op.
type usagePlugin struct {
	svc PolicyService
}

// NewUsagePlugin wraps a PolicyService so it can be registered with the
// core usage manager as a Plugin. Returns nil when svc is nil so the caller
// can register unconditionally and rely on the nil coalescing.
func NewUsagePlugin(svc PolicyService) coreusage.Plugin {
	if svc == nil {
		return nil
	}
	return &usagePlugin{svc: svc}
}

// HandleUsage satisfies coreusage.Plugin. It extracts the (principal, model,
// tokens, cost) tuple from the in-memory Record and forwards it to the
// policy service for budget accounting. Errors are silent: the usage sink
// is best-effort and must not propagate failures back to the request path.
func (p *usagePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil || p.svc == nil || !p.svc.Active() {
		return
	}
	model := record.Model
	if model == "" {
		model = record.Alias
	}
	principal := record.APIKey
	if principal == "" {
		return
	}
	total := record.Detail.TotalTokens
	if total == 0 {
		total = record.Detail.InputTokens + record.Detail.OutputTokens + record.Detail.ReasoningTokens
	}
	// The PG flusher computes cost from the pricing table; here we submit a
	// zero cost so the policy Consume path is decoupled from pricing lookups.
	// This keeps the budget enforcement resilient to pricing-table misses:
	// even when no pricing is configured, no-cost signals still increment
	// request counts and budget windows when a USD limit is set to 0 (which
	// would otherwise be a degenerate "always-reject" — handled by the > 0
	// guard in checkBudget).
	tokens := TokenCounts{
		Input:     record.Detail.InputTokens,
		Output:    record.Detail.OutputTokens,
		Reasoning: record.Detail.ReasoningTokens,
		Cached:    record.Detail.CachedTokens,
		Total:     total,
	}
	consumeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = p.svc.Consume(consumeCtx, principal, model, tokens)
}

// Compile-time assertion that *usagePlugin satisfies coreusage.Plugin.
var _ coreusage.Plugin = (*usagePlugin)(nil)
