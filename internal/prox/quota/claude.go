package quota

import (
	"errors"
	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/regexp"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// familyKey matches seven_day_<family> keys that name a model family
// (seven_day_opus, seven_day_fable). Multi-word suffixes such as
// seven_day_oauth_apps describe non-model limits and are ignored.
var familyKey = regexp.MustCompile(`^seven_day_([a-z][a-z0-9]*)$`)
var fableLimitName = regexp.MustCompile(`^fable(?:\s+[0-9]+)?$`)

type claudeWindow struct {
	Utilization *float64   `json:"utilization"`
	ResetsAt    *time.Time `json:"resets_at"`
}

// ParseClaudeUsage parses GET https://api.anthropic.com/api/oauth/usage.
// Utilization is reported as 0–100 percent used.
func ParseClaudeUsage(raw []byte, now time.Time) (Snapshot, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Snapshot{}, errors.New("invalid_quota_json")
	}
	snap := Snapshot{Provider: "claude"}
	weekly, ok := doc["seven_day"]
	if !ok {
		return Snapshot{}, errors.New("missing_weekly_window")
	}
	w, present, err := claudePollWindow(weekly, Weekly, "", now)
	if err != nil || !present {
		return Snapshot{}, errors.New("invalid_weekly_window")
	}
	snap.Windows = append(snap.Windows, w)
	if raw, ok := doc["five_hour"]; ok {
		w, present, err := claudePollWindow(raw, FiveHour, "", now)
		if err != nil {
			return Snapshot{}, errors.New("invalid_five_hour_window")
		}
		if present {
			snap.Windows = append(snap.Windows, w)
		}
	}
	for key, raw := range doc {
		m := familyKey.FindStringSubmatch(key)
		if m == nil {
			continue
		}
		// Other seven_day_* keys (seven_day_breakdown) are not windows; only
		// objects that report utilization are.
		if !hasUtilization(raw) {
			continue
		}
		w, present, err := claudePollWindow(raw, ModelWeekly, m[1], now)
		if err != nil {
			return Snapshot{}, errors.New("invalid_model_window")
		}
		if present {
			snap.Windows = append(snap.Windows, w)
		}
	}
	// A top-level seven_day_fable window takes precedence; a null one does not hide limits[].
	if _, ok := snap.Window(Window{Kind: ModelWeekly, Family: "fable"}.Key()); !ok {
		if raw, ok := doc["limits"]; ok {
			w, present, err := claudeFablePollWindow(raw, now)
			if err != nil {
				return Snapshot{}, errors.New("invalid_model_window")
			}
			if present {
				snap.Windows = append(snap.Windows, w)
			}
		}
	}
	snap.sort()
	return snap, nil
}

type claudeWeeklyLimit struct {
	Kind     string          `json:"kind"`
	Percent  json.RawMessage `json:"percent"`
	ResetsAt json.RawMessage `json:"resets_at"`
	IsActive bool            `json:"is_active"`
	Scope    struct {
		Model struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

func claudeFablePollWindow(raw json.RawMessage, now time.Time) (Window, bool, error) {
	var limits []json.RawMessage
	if err := json.Unmarshal(raw, &limits); err != nil {
		return Window{}, false, nil
	}
	var selected *claudeWeeklyLimit
	for _, rawLimit := range limits {
		var limit claudeWeeklyLimit
		if err := json.Unmarshal(rawLimit, &limit); err != nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(limit.Scope.Model.DisplayName))
		if strings.ToLower(strings.TrimSpace(limit.Kind)) != "weekly_scoped" || !fableLimitName.MatchString(name) || len(limit.Percent) == 0 || strings.TrimSpace(string(limit.Percent)) == "null" {
			continue
		}
		if selected == nil || (!selected.IsActive && limit.IsActive) {
			selected = &limit
		}
	}
	if selected == nil {
		return Window{}, false, nil
	}
	var utilization float64
	if err := json.Unmarshal(selected.Percent, &utilization); err != nil {
		return Window{}, false, err
	}
	window := claudeWindow{Utilization: &utilization}
	if len(selected.ResetsAt) != 0 && strings.TrimSpace(string(selected.ResetsAt)) != "null" {
		var resetsAt time.Time
		if err := json.Unmarshal(selected.ResetsAt, &resetsAt); err != nil {
			return Window{}, false, err
		}
		window.ResetsAt = &resetsAt
	}
	return claudeWindowValue(window, ModelWeekly, "fable", now)
}

func hasUtilization(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	_, ok := fields["utilization"]
	return ok
}

func claudePollWindow(raw json.RawMessage, kind Kind, family string, now time.Time) (Window, bool, error) {
	if string(raw) == "null" {
		return Window{}, false, nil
	}
	var cw claudeWindow
	if err := json.Unmarshal(raw, &cw); err != nil {
		return Window{}, false, err
	}
	return claudeWindowValue(cw, kind, family, now)
}

func claudeWindowValue(cw claudeWindow, kind Kind, family string, now time.Time) (Window, bool, error) {
	if cw.Utilization == nil || !validPercent(*cw.Utilization) {
		return Window{}, false, errors.New("invalid utilization")
	}
	if cw.ResetsAt != nil && cw.ResetsAt.IsZero() {
		return Window{}, false, errors.New("zero reset")
	}
	// A window with usage must say when it resets; an unused window may not have started.
	if cw.ResetsAt == nil && *cw.Utilization != 0 {
		return Window{}, false, errors.New("missing reset")
	}
	w := Window{Kind: kind, Family: family, UsedPercent: *cw.Utilization, Source: SourcePoll, ObservedAt: now}
	if cw.ResetsAt != nil {
		t := cw.ResetsAt.UTC()
		w.ResetsAt = &t
	}
	return w, true, nil
}

func validPercent(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 100
}

// ParseClaudeHeaders reads Anthropic unified rate-limit response headers.
// Utilization headers are 0–1 fractions and are scaled to percent here.
// A window is reported only when its utilization and reset are both valid,
// or when its status is "rejected" with a valid reset.
func ParseClaudeHeaders(h http.Header, now time.Time) []Window {
	if h == nil {
		return nil
	}
	var out []Window
	for _, spec := range []struct {
		prefix string
		kind   Kind
	}{{"5h", FiveHour}, {"7d", Weekly}} {
		base := "Anthropic-Ratelimit-Unified-" + spec.prefix + "-"
		reset, okReset := unixHeader(h.Get(base + "Reset"))
		if !okReset {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(h.Get(base + "Status")))
		util, okUtil := fractionHeader(h.Get(base + "Utilization"))
		rejected := status == "rejected"
		if !okUtil && !rejected {
			continue
		}
		used := util * 100
		if rejected {
			used = math.Max(used, 100)
		}
		used = math.Min(used, 100)
		t := reset
		out = append(out, Window{Kind: spec.kind, UsedPercent: used, ResetsAt: &t, Rejected: rejected, Source: SourcePassive, ObservedAt: now})
	}
	if reset, okReset := unixHeader(h.Get("Anthropic-Ratelimit-Unified-7d_oi-Reset")); okReset {
		if utilization, okUtil := fractionHeader(h.Get("Anthropic-Ratelimit-Unified-7d_oi-Utilization")); okUtil && utilization <= 1 {
			t := reset
			out = append(out, Window{Kind: ModelWeekly, Family: "fable", UsedPercent: utilization * 100, ResetsAt: &t, Source: SourcePassive, ObservedAt: now})
		}
	}
	return out
}

func fractionHeader(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1.5 {
		return 0, false
	}
	return v, true
}

func unixHeader(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v > 0 {
		if v > 1e12 { // milliseconds
			v /= 1000
		}
		return time.Unix(v, 0).UTC(), true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// ClaudeRejection describes a 429's provider attribution.
type ClaudeRejection struct {
	// Shared is true when the five-hour or weekly shared window rejected.
	Shared bool
	// ModelOnly is true when only an overage or model-specific claim rejected.
	ModelOnly bool
	Until     *time.Time
}

// ClassifyClaude429 interprets the unified rate-limit headers of a 429.
func ClassifyClaude429(h http.Header, now time.Time) ClaudeRejection {
	var r ClaudeRejection
	if h == nil {
		return r
	}
	s5 := strings.ToLower(strings.TrimSpace(h.Get("Anthropic-Ratelimit-Unified-5h-Status")))
	s7 := strings.ToLower(strings.TrimSpace(h.Get("Anthropic-Ratelimit-Unified-7d-Status")))
	unified := strings.ToLower(strings.TrimSpace(h.Get("Anthropic-Ratelimit-Unified-Status")))
	if s5 == "rejected" || s7 == "rejected" {
		r.Shared = true
		return r
	}
	if unified == "rejected" {
		r.ModelOnly = true
		if t, ok := unixHeader(h.Get("Anthropic-Ratelimit-Unified-Reset")); ok && t.After(now) {
			r.Until = &t
		}
	}
	return r
}
