package auth

import (
	"math"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
)

func quotaNumber(r ojson.Result) (float64, bool) {
	if r.Type != ojson.Number {
		return 0, false
	}
	n := r.Float()
	return n, !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0
}

// parseRoutingQuota accepts the documented Codex wham, Claude OAuth usage and
// Antigravity retrieveUserQuotaSummary shapes.
// It never interprets retry-after, billing credits or code-review quota as inference quota.
func parseRoutingQuota(provider string, data []byte, now time.Time) ([]RoutingWindow, bool) {
	if !ojson.ValidBytes(data) {
		return nil, false
	}
	root := ojson.ParseBytes(data)
	windows := make([]RoutingWindow, 0, 6)
	malformed := false
	idle := false
	if provider == "codex" {
		add := func(rate ojson.Result, prefix, model string) {
			if !rate.IsObject() {
				malformed = true
				return
			}
			denied := (rate.Get("allowed").Type == ojson.False) || (rate.Get("limit_reached").Type == ojson.True)
			start := len(windows)
			anyFull := false
			for _, key := range []string{"primary_window", "secondary_window"} {
				w := rate.Get(key)
				if !w.Exists() || w.Type == ojson.Null {
					continue
				}
				used, okUsed := quotaNumber(w.Get("used_percent"))
				seconds, okSeconds := quotaNumber(w.Get("limit_window_seconds"))
				reset, okReset := quotaNumber(w.Get("reset_at"))
				var resetAt time.Time
				if okReset && reset > 0 && reset < 1e11 {
					resetAt = time.Unix(int64(reset), 0)
				} else if after, ok := quotaNumber(w.Get("reset_after_seconds")); ok && after > 0 && after <= 2678400 {
					resetAt = now.Add(time.Duration(after) * time.Second)
				}
				if !okUsed || !okSeconds || seconds <= 0 || seconds > 2678400 || resetAt.IsZero() || resetAt.After(now.Add(32*24*time.Hour)) {
					malformed = true
					continue
				}
				anyFull = anyFull || used >= 100
				windows = append(windows, RoutingWindow{Key: prefix + key, ResetsAt: resetAt, UsedPercent: used, seconds: int64(seconds), model: model})
			}
			// A group-level denial does not identify which limit was exhausted.
			// Use full counters when available; otherwise conservatively block the group.
			for i := start; i < len(windows); i++ {
				windows[i].denied = denied && !anyFull
			}
		}
		add(root.Get("rate_limit"), "", "")
		extra := root.Get("additional_rate_limits")
		if extra.IsArray() {
			extra.ForEach(func(_, v ojson.Result) bool {
				name := v.Get("limit_name")
				if name.Type != ojson.String {
					return true
				}
				model := normalizeQuotaModel(name.String())
				rate := v.Get("rate_limit")
				if model != "" && rate.Exists() && rate.Type != ojson.Null {
					add(rate, "additional:"+model+":", model)
				}
				return len(windows) < 32
			})
		}
	} else if provider == "claude" {
		add := func(w ojson.Result, key, model, usageKey string, seconds int64) {
			if !w.Exists() || w.Type == ojson.Null {
				return
			}
			util := w.Get(usageKey)
			used, ok := quotaNumber(util)
			reset := w.Get("resets_at")
			// Claude has no reset for a window that has not started this period.
			if reset.Exists() && reset.Type == ojson.Null && util.Exists() && (util.Type == ojson.Null || (ok && used == 0)) {
				idle = true
				return
			}
			resetAt, err := time.Parse(time.RFC3339Nano, reset.String())
			if !ok || reset.Type != ojson.String || err != nil || resetAt.After(now.Add(32*24*time.Hour)) {
				malformed = true
				return
			}
			windows = append(windows, RoutingWindow{Key: key, ResetsAt: resetAt, UsedPercent: used, seconds: seconds, model: model})
		}
		for _, spec := range []struct {
			key, model string
			seconds    int64
		}{{"five_hour", "", 18000}, {"seven_day", "", 604800}, {"seven_day_opus", "opus", 604800}, {"seven_day_sonnet", "sonnet", 604800}} {
			add(root.Get(spec.key), spec.key, spec.model, "utilization", spec.seconds)
		}
		// Scoped Fable quota supplements the shared windows. Prefer the active
		// modern entry over a duplicate or legacy counter, as the dashboard does.
		var fable ojson.Result
		limits := root.Get("limits")
		if limits.Exists() && limits.Type != ojson.Null && !limits.IsArray() {
			malformed = true
		}
		limits.ForEach(func(_, limit ojson.Result) bool {
			name := strings.ToLower(strings.TrimSpace(limit.Get("scope.model.display_name").String()))
			isFable := name == "fable" || name == "fable 5"
			if strings.ToLower(strings.TrimSpace(limit.Get("kind").String())) != "weekly_scoped" || !isFable {
				return true
			}
			if !fable.Exists() || (fable.Get("is_active").Type != ojson.True && limit.Get("is_active").Type == ojson.True) {
				fable = limit
			}
			return true
		})
		if fable.Exists() {
			add(fable, "seven_day_fable", "fable", "percent", 604800)
		} else {
			legacy := root.Get("iguana_necktie")
			// This legacy key is also used for dollar balances on some plans.
			// Those credits are not a subscription quota or a reset deadline.
			for _, key := range []string{"limit_dollars", "used_dollars", "remaining_dollars"} {
				if value := legacy.Get(key); value.Exists() && value.Type != ojson.Null {
					legacy = ojson.Result{}
					break
				}
			}
			add(legacy, "seven_day_fable", "fable", "utilization", 604800)
		}
	} else if provider == "antigravity" {
		var recognized, bad bool
		windows, recognized, bad = parseAntigravityRoutingQuota(root, now)
		// Buckets that are all full, unstarted or disabled are a valid snapshot without deadlines.
		idle = recognized
		malformed = malformed || bad
	}
	// A partly malformed snapshot cannot establish that all applicable limits are usable.
	// A valid all-idle response clears an earlier snapshot without inventing a reset.
	return windows, (len(windows) > 0 || idle) && !malformed
}
