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

// usageIndex maps the full inbound request id to aggregated usage. Usage records carry the gin
// request id as TraceID (usage.Manager.Publish falls back to logging.GetRequestID), so no hook is
// needed. Files are matched by the full id from their X-Cpa-Trace-Id response header, and by the
// short id in the file name (latest request with that short id) when the header is missing.
type usageIndex struct {
	mu      sync.Mutex
	byID    map[string]*requestUsage
	byShort map[string]string
	order   []string
	next    int
}

func newUsageIndex() *usageIndex {
	return &usageIndex{byID: map[string]*requestUsage{}, byShort: map[string]string{}}
}

// HandleUsage implements usage.Plugin. It only touches memory.
func (u *usageIndex) HandleUsage(_ context.Context, record usage.Record) {
	if u == nil {
		return
	}
	id := strings.TrimSpace(record.TraceID)
	if id == "" {
		return
	}
	short := logging.ShortRequestID(id)
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
			evicted := u.order[u.next]
			delete(u.byID, evicted)
			if s := logging.ShortRequestID(evicted); u.byShort[s] == evicted {
				delete(u.byShort, s)
			}
			u.order[u.next] = id
			u.next = (u.next + 1) % usageCapacity
		}
		u.byShort[short] = id
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

// get looks up usage by full request id, else by short id.
func (u *usageIndex) get(fullID, shortID string) (requestUsage, bool) {
	if u == nil {
		return requestUsage{}, false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	id := fullID
	if id == "" {
		id = u.byShort[shortID]
	}
	agg, ok := u.byID[id]
	if !ok {
		return requestUsage{}, false
	}
	return *agg, true
}
