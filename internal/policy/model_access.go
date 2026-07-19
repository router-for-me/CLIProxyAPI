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

// hourlyWindow returns the [start, end) boundaries of the hourly window
// containing t.
func hourlyWindow(t time.Time) (time.Time, time.Time) {
	start := t.UTC().Truncate(time.Hour)
	return start, start.Add(time.Hour)
}

// weeklyWindow returns the Monday-anchored [start, end) boundaries of the
// weekly window containing t. Week starts at midnight UTC on Monday to keep
// behavior deterministic across timezones.
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
// window containing t in UTC.
func monthlyWindow(t time.Time) (time.Time, time.Time) {
	t = t.UTC()
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	return start, end
}

// windowFor resolves window boundaries by window type.
func windowFor(windowType string, t time.Time) (start, end time.Time) {
	switch windowType {
	case "hourly":
		return hourlyWindow(t)
	case "weekly":
		return weeklyWindow(t)
	case "monthly":
		return monthlyWindow(t)
	default:
		// Unknown windows default to hourly which is the most restrictive.
		return hourlyWindow(t)
	}
}
