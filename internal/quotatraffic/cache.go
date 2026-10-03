package quotatraffic

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Observation struct {
	AuthIndex  string      `json:"auth_index"`
	Provider   string      `json:"provider"`
	Window     string      `json:"window"`
	Headers    http.Header `json:"headers"`
	ObservedAt time.Time   `json:"observed_at"`
	Source     string      `json:"source"`
}

const maxObservations = 4096

var cache = struct {
	sync.Mutex
	items map[string]Observation
}{items: make(map[string]Observation)}

func Capture(index, provider string, headers http.Header, at time.Time, source string) {
	if index == "" || at.IsZero() {
		return
	}
	if provider != "codex" && provider != "claude" {
		return
	}
	switch source {
	case "api_response_headers", "websocket_event", "manual_provider_query", "scheduled_provider_query":
	default:
		return
	}
	groups := map[string]http.Header{}
	for key, values := range headers {
		key = http.CanonicalHeaderKey(key)
		lower := strings.ToLower(key)
		group := ""
		if provider == "codex" && strings.HasPrefix(lower, "x-codex-") {
			for _, marker := range []string{"-primary-", "-secondary-"} {
				if i := strings.LastIndex(lower, marker); i >= 0 {
					group = lower[:i] + marker[:len(marker)-1]
					break
				}
			}
		} else if provider == "claude" && strings.HasPrefix(lower, "anthropic-ratelimit-unified-") {
			for _, window := range []string{"5h", "7d", "7d-opus", "7d-sonnet"} {
				if strings.HasPrefix(lower, "anthropic-ratelimit-unified-"+window+"-") {
					group = "claude-" + window
				}
			}
		}
		allowed := false
		for _, suffix := range []string{"-used-percent", "-window-minutes", "-reset-at", "-reset-after-seconds", "-utilization", "-reset"} {
			if strings.HasSuffix(lower, suffix) {
				allowed = true
				break
			}
		}
		if !allowed || group == "" || len(values) == 0 {
			continue
		}
		value := strings.TrimSpace(values[0])
		if len(value) > 512 || strings.ContainsAny(value, "\r\n") {
			continue
		}
		// Only numeric quota fields are retained. An upstream header using a
		// recognized suffix must not smuggle arbitrary text into cache output.
		if strings.HasSuffix(lower, "-used-percent") || strings.HasSuffix(lower, "-utilization") {
			maximum := 100.0
			if provider == "claude" {
				maximum = 1
			}
			number, err := strconv.ParseFloat(value, 64)
			if err != nil || !(number >= 0 && number <= maximum) {
				continue
			}
		} else if number, err := strconv.ParseInt(value, 10, 64); err != nil || number < 0 {
			continue
		}
		if groups[group] == nil {
			groups[group] = http.Header{}
		}
		groups[group].Set(key, value)
	}
	cache.Lock()
	defer cache.Unlock()
	for group, h := range groups {
		for key, values := range headers {
			lower := strings.ToLower(key)
			if lower == "x-codex-plan-type" || lower == "x-codex-active-limit" || (strings.HasSuffix(lower, "-limit-name") && strings.HasPrefix(group, strings.TrimSuffix(lower, "-limit-name"))) {
				if len(values) > 0 && len(values[0]) <= 256 && !strings.ContainsAny(values[0], "\r\n") {
					h.Set(key, values[0])
				}
			}
		}
		// Percent/utilization is required: reset-only or unrelated headers are not a fresh quota observation.
		for name := range h {
			if strings.HasSuffix(strings.ToLower(name), "-limit-name") {
				id := strings.ToLower(strings.TrimSpace(h.Get(name)))
				if id != "" {
					window := "primary"
					if strings.HasSuffix(group, "secondary") {
						window = "secondary"
					}
					group = "x-codex-additional-" + id + "-" + window
				}
			}
		}
		valid := false
		for name := range h {
			if strings.HasSuffix(strings.ToLower(name), "-used-percent") || strings.HasSuffix(strings.ToLower(name), "-utilization") {
				n, e := strconv.ParseFloat(h.Get(name), 64)
				valid = e == nil && n >= 0 && n <= 100
				if valid {
					break
				}
			}
		}
		if !valid {
			continue
		}
		key := index + "\x00" + provider + "\x00" + group
		if old, ok := cache.items[key]; ok && !at.After(old.ObservedAt) {
			continue
		}
		if _, exists := cache.items[key]; !exists && len(cache.items) >= maxObservations {
			oldKey := ""
			var oldest time.Time
			for k, v := range cache.items {
				if oldKey == "" || v.ObservedAt.Before(oldest) || (v.ObservedAt.Equal(oldest) && k < oldKey) {
					oldKey = k
					oldest = v.ObservedAt
				}
			}
			delete(cache.items, oldKey)
		}
		cache.items[key] = Observation{index, provider, group, h.Clone(), at, source}
	}
}
func Read(allowed map[string]bool) []Observation {
	cache.Lock()
	defer cache.Unlock()
	items := []Observation{}
	for _, v := range cache.items {
		if allowed[v.AuthIndex] {
			v.Headers = v.Headers.Clone()
			items = append(items, v)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].AuthIndex == items[j].AuthIndex {
			return items[i].Window < items[j].Window
		}
		return items[i].AuthIndex < items[j].AuthIndex
	})
	return items
}

// Provider queries use explicit known payload shapes. Never retain raw bodies or credentials.
func CaptureQuery(index, provider string, body []byte, at time.Time, source string) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return
	}
	headers := http.Header{}
	if provider == "codex" {
		add := func(prefix string, raw json.RawMessage) {
			var limit map[string]json.RawMessage
			if json.Unmarshal(raw, &limit) != nil {
				return
			}
			for _, w := range []string{"primary", "secondary"} {
				var values map[string]json.RawMessage
				if json.Unmarshal(limit[w+"_window"], &values) != nil {
					continue
				}
				for field, suffix := range map[string]string{"used_percent": "Used-Percent", "reset_at": "Reset-At", "reset_after_seconds": "Reset-After-Seconds"} {
					if value := values[field]; value != nil {
						headers.Set(prefix+w+"-"+suffix, string(value))
					}
				}
				var seconds int64
				if json.Unmarshal(values["limit_window_seconds"], &seconds) == nil && seconds > 0 {
					headers.Set(prefix+w+"-Window-Minutes", strconv.FormatInt(seconds/60, 10))
				}
			}
		}
		add("X-Codex-", payload["rate_limit"])
		add("X-Codex-Code-Review-", payload["code_review_rate_limit"])
		var extra []struct {
			LimitName string          `json:"limit_name"`
			RateLimit json.RawMessage `json:"rate_limit"`
		}
		if json.Unmarshal(payload["additional_rate_limits"], &extra) == nil {
			for i, item := range extra {
				if i >= 8 {
					break
				}
				name := strings.TrimSpace(item.LimitName)
				if name == "" || len(name) > 128 || strings.ContainsAny(name, "\r\n") {
					continue
				}
				id := strings.Map(func(r rune) rune {
					if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
						return r
					}
					return '-'
				}, name)
				prefix := "X-Codex-Additional-" + id + "-"
				add(prefix, item.RateLimit)
				headers.Set(prefix+"Limit-Name", name)
			}
		}
	} else if provider == "claude" {
		for field, window := range map[string]string{"five_hour": "5h", "seven_day": "7d", "seven_day_opus": "7d-opus", "seven_day_sonnet": "7d-sonnet"} {
			var values struct {
				Utilization *float64 `json:"utilization"`
				ResetsAt    string   `json:"resets_at"`
			}
			if json.Unmarshal(payload[field], &values) != nil || values.Utilization == nil {
				continue
			}
			prefix := "Anthropic-Ratelimit-Unified-" + window + "-"
			headers.Set(prefix+"Utilization", strconv.FormatFloat(*values.Utilization/100, 'f', -1, 64))
			if reset, e := time.Parse(time.RFC3339, values.ResetsAt); e == nil {
				headers.Set(prefix+"Reset", strconv.FormatInt(reset.Unix(), 10))
			}
		}
	}
	Capture(index, provider, headers, at, source)
}
