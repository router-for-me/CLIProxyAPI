package requestlogs

import (
	"context"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/usagestats"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// UsagePluginName is the usage plugin registration name.
const UsagePluginName = "proxx-request-logs"

// usageCapacity bounds how many requests keep joined usage in memory.
const usageCapacity = 20000

// requestUsage aggregates the usage records (one per upstream attempt) of one inbound request.
type requestUsage struct {
	records    int
	provider   string
	model      string
	success    bool
	in         int64
	out        int64
	cached     int64
	reasoning  int64
	cost       float64
	costKnown  bool
	costSource string
}

// usageIndex maps the short request id (the id in log file names) to aggregated usage.
// Usage records carry the inbound gin request id as TraceID (usage.Manager.Publish falls back
// to logging.GetRequestID), so no hook is needed.
type usageIndex struct {
	mu    sync.Mutex
	byID  map[string]*requestUsage
	order []string
	next  int
}

func newUsageIndex() *usageIndex {
	return &usageIndex{byID: map[string]*requestUsage{}}
}

// HandleUsage implements usage.Plugin. It only touches memory.
func (u *usageIndex) HandleUsage(_ context.Context, record usage.Record) {
	if u == nil {
		return
	}
	id := logging.ShortRequestID(record.TraceID)
	if id == "" {
		return
	}
	in, out, cached, reasoning := usagestats.TokenCounts(record.Detail)
	u.mu.Lock()
	defer u.mu.Unlock()
	agg, ok := u.byID[id]
	if !ok {
		agg = &requestUsage{}
		u.byID[id] = agg
		if len(u.order) < usageCapacity {
			u.order = append(u.order, id)
		} else {
			delete(u.byID, u.order[u.next])
			u.order[u.next] = id
			u.next = (u.next + 1) % usageCapacity
		}
	}
	agg.records++
	agg.in += in
	agg.out += out
	agg.cached += cached
	agg.reasoning += reasoning
	if !record.Failed || !agg.success {
		if record.Provider != "" {
			agg.provider = record.Provider
		}
		if model := strings.TrimSpace(record.Model); model != "" {
			agg.model = model
		}
	}
	if !record.Failed {
		agg.success = true
	}
	if cd := record.Detail.CostDetails; cd != nil {
		if cd.FinalCost != nil {
			agg.cost += *cd.FinalCost
			agg.costKnown = true
		}
		if cd.CostSource != "" && (!record.Failed || agg.costSource == "") {
			agg.costSource = cd.CostSource
		}
	}
}

func (u *usageIndex) get(id string) (requestUsage, bool) {
	if u == nil {
		return requestUsage{}, false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	agg, ok := u.byID[id]
	if !ok {
		return requestUsage{}, false
	}
	return *agg, true
}
