package usage

import (
	"context"
	"sync/atomic"
)

// proxx: per-request cost hook. The pricing package (internal/proxx/pricing) installs a
// resolver here so this SDK package does not import it. Publish calls it once per record,
// before the record is queued for plugins, so every usage consumer sees the final cost.

// CostHook resolves Detail.CostDetails.FinalCost and CostSource for a record in place.
type CostHook func(ctx context.Context, record *Record)

var costHook atomic.Pointer[CostHook]

// SetCostHook installs (or clears, with nil) the per-request cost resolver.
func SetCostHook(hook CostHook) {
	if hook == nil {
		costHook.Store(nil)
		return
	}
	costHook.Store(&hook)
}

func applyCostHook(ctx context.Context, record *Record) {
	hook := costHook.Load()
	if hook == nil || *hook == nil || record == nil {
		return
	}
	defer func() {
		_ = recover() // a pricing bug must never drop a usage record
	}()
	(*hook)(ctx, record)
}
