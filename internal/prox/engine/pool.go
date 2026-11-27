package engine

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/router"
	proxusage "github.com/router-for-me/CLIProxyAPI/v8/internal/prox/usage"
)

// PlanWeight estimates how many "base plan" units of quota an account has,
// so pooled remaining percentages weight a Max 20x account above a Pro one.
func PlanWeight(provider, plan string) float64 {
	p := strings.ToLower(strings.TrimSpace(plan))
	switch provider {
	case "claude":
		switch {
		case strings.Contains(p, "20x"):
			return 20
		case strings.Contains(p, "5x"), strings.Contains(p, "max"):
			return 5
		}
	case "codex":
		switch {
		case p == "free" || p == "go":
			return 0.1
		case strings.Contains(p, "pro"):
			return 10
		}
	}
	return 1
}

// PoolWindow is the weighted view of one window kind across accounts.
type PoolWindow struct {
	Kind string `json:"kind"`
	// RemainingPercent is the plan-weighted average remaining.
	RemainingPercent float64    `json:"remaining_percent"`
	MinRemaining     float64    `json:"min_remaining_percent"`
	MaxRemaining     float64    `json:"max_remaining_percent"`
	Accounts         int        `json:"accounts"`
	NextReset        *time.Time `json:"next_reset,omitempty"`
	Exhausted        int        `json:"exhausted"`
}

// PoolProvider summarizes one provider's accounts.
type PoolProvider struct {
	Provider      string       `json:"provider"`
	Accounts      int          `json:"accounts"`
	Usable        int          `json:"usable"`
	TotalWeight   float64      `json:"total_weight"`
	Windows       []PoolWindow `json:"windows"`
	NextSession   string       `json:"next_session_account,omitempty"`
	CostToday     float64      `json:"cost_today_usd"`
	Cost7d        float64      `json:"cost_7d_usd"`
	TokensToday   int64        `json:"tokens_today"`
	RequestsToday int64        `json:"requests_today"`
}

// Pool aggregates every account by provider.
func (e *Engine) Pool() []PoolProvider {
	var accounts []router.AccountView
	if r := e.activeRouter(); r != nil {
		accounts = append(accounts, r.Accounts()...)
	}
	if mon := e.Monitor(); mon != nil {
		accounts = append(accounts, mon.Views()...)
	}
	now := e.now()
	type acc struct {
		sum, weight, min, max float64
		n, exhausted          int
		next                  *time.Time
	}
	byProv := map[string]*PoolProvider{}
	windows := map[string]map[string]*acc{}
	for _, a := range accounts {
		p := byProv[a.Provider]
		if p == nil {
			p = &PoolProvider{Provider: a.Provider}
			byProv[a.Provider] = p
			windows[a.Provider] = map[string]*acc{}
		}
		p.Accounts++
		if !a.Present || a.Disabled {
			continue
		}
		if a.QuotaState != "exhausted" && a.PollError == "" {
			p.Usable++
		}
		if a.Preferred && p.NextSession == "" {
			p.NextSession = a.ID
		}
		w := PlanWeight(a.Provider, a.Plan)
		p.TotalWeight += w
		for _, win := range a.Windows {
			if win.Kind == "model_weekly" || win.Stale {
				continue
			}
			ag := windows[a.Provider][win.Kind]
			if ag == nil {
				ag = &acc{min: 101, max: -1}
				windows[a.Provider][win.Kind] = ag
			}
			rem := win.Remaining
			if win.Passed {
				rem = 100
			}
			ag.sum += rem * w
			ag.weight += w
			ag.n++
			ag.min = min(ag.min, rem)
			ag.max = max(ag.max, rem)
			if rem <= 0 {
				ag.exhausted++
			}
			if win.ResetsAt != nil && win.ResetsAt.After(now) && (ag.next == nil || win.ResetsAt.Before(*ag.next)) {
				t := *win.ResetsAt
				ag.next = &t
			}
		}
	}
	if l := e.Ledger(); l != nil {
		day := now.Add(-24 * time.Hour)
		for _, row := range l.Run(proxusage.Query{Since: now.Add(-7 * 24 * time.Hour), GroupBy: []string{"provider"}, Step: time.Hour}) {
			p := byProv[row.Provider]
			if p == nil {
				continue
			}
			p.Cost7d += row.CostUSD
			if !row.Start.Before(day.Truncate(time.Hour)) {
				p.CostToday += row.CostUSD
				p.TokensToday += row.Tokens.Total()
				p.RequestsToday += row.Requests
			}
		}
	}
	out := make([]PoolProvider, 0, len(byProv))
	order := map[string]int{"five_hour": 0, "daily": 1, "weekly": 2, "monthly": 3}
	for prov, p := range byProv {
		for kind, ag := range windows[prov] {
			pw := PoolWindow{Kind: kind, Accounts: ag.n, MinRemaining: ag.min, MaxRemaining: ag.max, NextReset: ag.next, Exhausted: ag.exhausted}
			if ag.weight > 0 {
				pw.RemainingPercent = ag.sum / ag.weight
			}
			p.Windows = append(p.Windows, pw)
		}
		sort.Slice(p.Windows, func(i, j int) bool { return order[p.Windows[i].Kind] < order[p.Windows[j].Kind] })
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

func parseRange(s string, def time.Duration) time.Duration {
	switch s {
	case "1h":
		return time.Hour
	case "6h":
		return 6 * time.Hour
	case "24h", "1d":
		return 24 * time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	case "30d":
		return 30 * 24 * time.Hour
	case "90d":
		return 90 * 24 * time.Hour
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

// handleUsage serves /costs and /pool.
func (e *Engine) handleUsage(c *gin.Context, path string) bool {
	switch {
	case path == "/sink" && c.Request.Method == http.MethodGet:
		c.JSON(http.StatusOK, e.SinkStatus())
		return true
	case path == "/pool/chart" && c.Request.Method == http.MethodGet:
		e.handlePoolChart(c)
		return true
	case path == "/pool" && c.Request.Method == http.MethodGet:
		c.JSON(http.StatusOK, gin.H{"now": e.now(), "providers": e.Pool()})
		return true
	case path == "/costs" && c.Request.Method == http.MethodGet:
		l := e.Ledger()
		if l == nil {
			c.JSON(http.StatusOK, gin.H{"rows": []any{}})
			return true
		}
		span := parseRange(c.Query("range"), 24*time.Hour)
		var step time.Duration
		if s := c.Query("step"); s != "" {
			step = parseRange(s, 0)
		}
		var group []string
		for _, g := range strings.Split(c.DefaultQuery("group", "provider,model"), ",") {
			if g = strings.TrimSpace(g); g != "" {
				group = append(group, g)
			}
		}
		now := e.now()
		rows := l.Run(proxusage.Query{Since: now.Add(-span), GroupBy: group, Step: step, Provider: c.Query("provider"), AuthID: c.Query("auth_id")})
		var total proxusage.Row
		for _, r := range rows {
			total.Requests += r.Requests
			total.Failures += r.Failures
			total.Unpriced += r.Unpriced
			total.CostUSD += r.CostUSD
			total.Tokens.Input += r.Tokens.Input
			total.Tokens.Output += r.Tokens.Output
			total.Tokens.Reasoning += r.Tokens.Reasoning
			total.Tokens.CacheRead += r.Tokens.CacheRead
			total.Tokens.CacheWrite += r.Tokens.CacheWrite
		}
		var cacheShare *float64
		if in := total.Tokens.Input + total.Tokens.CacheRead + total.Tokens.CacheWrite; in > 0 {
			v := float64(total.Tokens.CacheRead) / float64(in)
			cacheShare = &v
		}
		labels := map[string]string{}
		for _, r := range rows {
			if r.AuthID != "" {
				labels[r.AuthID] = e.accountLabel(r.AuthID)
			}
		}
		c.JSON(http.StatusOK, gin.H{"now": now, "range": span.String(), "step": strconv.FormatInt(int64(step/time.Second), 10), "rows": rows, "total": total, "cache_read_share": cacheShare, "labels": labels})
		return true
	}
	return false
}
