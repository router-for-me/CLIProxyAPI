package policy

import "time"

// Allowed reports whether the supplied model falls within the policy's
// whitelist/blacklist. An empty whitelist means "all allowed"; an empty
// blacklist means "none blocked". Blacklist takes precedence: a blocked model
// is always rejected even when the whitelist is also set.
func Allowed(p Policy, model string) bool {
	if model == "" {
		// No model context (e.g. embedding-only key list) — defer to other
		// checks. The middleware will re-evaluate after parsing the body.
		return true
	}
	for _, blocked := range p.BlockedModels {
		if modelMatches(blocked, model) {
			return false
		}
	}
	if len(p.AllowedModels) == 0 {
		return true
	}
	for _, allowed := range p.AllowedModels {
		if modelMatches(allowed, model) {
			return true
		}
	}
	return false
}

// ModelCoveredByAllowed reports whether model is permitted by the supplied
// allowed-models list (exact or trailing-wildcard match). An empty list means
// "all allowed", so it returns true. It is the write-time validation analogue
// of Allowed (without the blocked-list precedence): a route may target a model
// only when that model is in the allowlist. Wildcard entries (e.g. "gpt-4*")
// cover any model matching their prefix.
func ModelCoveredByAllowed(allowedModels []string, model string) bool {
	if model == "" {
		return false
	}
	if len(allowedModels) == 0 {
		return true
	}
	for _, allowed := range allowedModels {
		if modelMatches(allowed, model) {
			return true
		}
	}
	return false
}

// modelMatches compares a policy entry against the requested model. Entries
// support a trailing '*' wildcard that matches any prefix, which is useful
// for guarding families like "gpt-4*" or "claude-*".
func modelMatches(entry, requested string) bool {
	if entry == "" {
		return false
	}
	if entry == requested {
		return true
	}
	if last := entry[len(entry)-1]; last == '*' {
		prefix := entry[:len(entry)-1]
		return len(requested) >= len(prefix) && requested[:len(prefix)] == prefix
	}
	return false
}

// modelAllowed reports whether the supplied model is in the user's grant list.
// An empty list means "all allowed" (matches LiteLLM's permission semantics).
func modelAllowed(allowed []string, model string) bool {
	if model == "" {
		return true
	}
	if len(allowed) == 0 {
		return true
	}
	for _, entry := range allowed {
		if modelMatches(entry, model) {
			return true
		}
	}
	return false
}

// isAdminRole reports whether the supplied internal-user role bypasses
// per-user budget/RPM enforcement. proxy_admin and proxy_admin_viewer still
// honor per-key budgets (those run unconditionally); only the user-level caps
// are skipped so operators are not throttled by their own user rows.
func isAdminRole(role string) bool {
	switch role {
	case "proxy_admin", "proxy_admin_viewer":
		return true
	default:
		return false
	}
}

// hourlyWindow returns the [start, end) boundaries of the calendar-aligned
// hourly window containing t. This is only a fallback used when no anchor
// (api_keys.created_at) is available; the per-key budget code paths anchor
// windows to created_at via windowFor + anchoredWindow instead.
func hourlyWindow(t time.Time) (time.Time, time.Time) {
	start := t.UTC().Truncate(time.Hour)
	return start, start.Add(time.Hour)
}

// weeklyWindow returns the Monday-anchored [start, end) boundaries of the
// calendar-aligned weekly window containing t. Week starts at midnight UTC on
// Monday to keep behavior deterministic across timezones. Fallback only — see
// hourlyWindow's doc for why the live path uses anchoredWindow.
func weeklyWindow(t time.Time) (time.Time, time.Time) {
	t = t.UTC()
	weekday := int(t.Weekday())
	// time.Weekday: Sunday=0 ... Saturday=6. Normalize so Monday=0.
	if weekday == 0 {
		weekday = 7
	}
	daysSinceMonday := weekday - 1
	start := t.Truncate(24*time.Hour).AddDate(0, 0, -daysSinceMonday)
	return start, start.Add(7 * 24 * time.Hour)
}

// monthlyWindow returns the [start, end) boundaries of the calendar-month
// window containing t in UTC. Fallback only — the live monthly path anchors to
// created_at and rolls in fixed 30-day durations (see anchoredWindow).
func monthlyWindow(t time.Time) (time.Time, time.Time) {
	t = t.UTC()
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	return start, end
}

// anchoredWindow resolves the [start, end) budget window containing t, anchored
// to the supplied anchor (api_keys.created_at or internal_users.created_at).
// Windows roll in fixed durations from the anchor: 1h (hourly), 7d (weekly),
// 30d (monthly). The monthly window is intentionally 30 fixed days rather than
// a calendar month so "30 hari setelah created at" holds exactly.
//
// When anchor is the zero value (non-PG path / legacy snapshot without
// created_at), behavior degrades to the calendar-aligned fallback so the
// caller never receives a zero start. created_at is non-NULL for every PG key,
// so this safety net is effectively unreachable in production but keeps the
// helper total.
//
// IMPORTANT (backward compat): switching from calendar-aligned to anchor-
// aligned window_start orphaned pre-existing usage_windows rows (their
// window_start no longer matches the anchor-derived value). Operators should
// TRUNCATE usage_windows / user_windows once after deploying so enforcement
// starts fresh from each key's created_at.
func anchoredWindow(d time.Duration, anchor, t time.Time, fallback func(time.Time) (time.Time, time.Time)) (time.Time, time.Time) {
	if anchor.IsZero() {
		return fallback(t)
	}
	a := anchor.UTC()
	tt := t.UTC()
	elapsed := tt.Sub(a)
	if elapsed < 0 {
		// Clock skew or a request arriving before created_at (impossible in
		// practice): clamp into the first window so we return a valid range.
		elapsed = 0
	}
	k := elapsed / d // integer number of full windows since the anchor
	start := a.Add(k * d)
	return start, start.Add(d)
}

// windowFor resolves window boundaries by window type, anchored to the
// supplied created_at timestamp. See anchoredWindow for the semantics and the
// fallback behavior when anchor is zero.
func windowFor(windowType string, anchor, t time.Time) (start, end time.Time) {
	switch windowType {
	case "hourly":
		return anchoredWindow(time.Hour, anchor, t, hourlyWindow)
	case "weekly":
		return anchoredWindow(7*24*time.Hour, anchor, t, weeklyWindow)
	case "monthly":
		return anchoredWindow(30*24*time.Hour, anchor, t, monthlyWindow)
	default:
		// Unknown windows default to hourly which is the most restrictive.
		return anchoredWindow(time.Hour, anchor, t, hourlyWindow)
	}
}
