package usagestats

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// QuotaSeriesKey identifies one quota window of one upstream account.
type QuotaSeriesKey struct {
	Provider  string `json:"provider"`
	AuthIndex string `json:"auth_index"`
	Account   string `json:"account"`
	// Window is a provider-independent window id such as "5h" or "7d".
	Window string `json:"window"`
}

// QuotaPoint is the state of a quota window as an upstream response reported it.
type QuotaPoint struct {
	// TimestampMs is when the upstream reported the state, in Unix milliseconds.
	TimestampMs int64 `json:"ts_ms"`
	// UsedPercent is the used share of the window, from 0 to 100.
	UsedPercent float64 `json:"used_percent"`
	// ResetsAtMs is when the window resets, in Unix milliseconds, or 0 when unknown.
	ResetsAtMs int64 `json:"resets_at_ms"`
}

// QuotaSample is one recorded point of a quota series.
type QuotaSample struct {
	QuotaSeriesKey
	QuotaPoint
}

// quotaWindowState is what one upstream response reports about one quota window.
type quotaWindowState struct {
	window      string
	usedPercent float64
	resetsAtMs  int64
}

// quotaHeaderParsers read the quota windows that each provider reports in its
// response headers. A provider without an entry reports none.
var quotaHeaderParsers = map[string]func(headers http.Header, observedAt time.Time) []quotaWindowState{
	"claude": claudeQuotaWindows,
	"codex":  codexQuotaWindows,
}

// quotaSamplesFromRow turns the quota headers of one upstream response into
// samples of the account that served it.
func quotaSamplesFromRow(row Row, headers http.Header) []QuotaSample {
	parse, ok := quotaHeaderParsers[row.Provider]
	if !ok || row.AuthIndex == "" {
		return nil
	}
	// The upstream takes the state it reports when it admits the request, the
	// same check that can reject it with a 429, so the state leaves out the
	// request's own usage. The request start is the closest time the proxy
	// knows: the time to first token can include a model's thinking.
	observedAt := row.Timestamp
	windows := parse(headers, observedAt)
	samples := make([]QuotaSample, 0, len(windows))
	for _, state := range windows {
		samples = append(samples, QuotaSample{
			QuotaSeriesKey: QuotaSeriesKey{Provider: row.Provider, AuthIndex: row.AuthIndex, Account: row.Account, Window: state.window},
			QuotaPoint:     QuotaPoint{TimestampMs: observedAt.UnixMilli(), UsedPercent: state.usedPercent, ResetsAtMs: state.resetsAtMs},
		})
	}
	return samples
}

const claudeUnifiedHeaderPrefix = "anthropic-ratelimit-unified-"

// claudeQuotaWindows reads Anthropic-Ratelimit-Unified-<window>-Utilization,
// the used fraction of a window, and its -Reset in Unix seconds.
func claudeQuotaWindows(headers http.Header, _ time.Time) []quotaWindowState {
	var windows []quotaWindowState
	for key, values := range headers {
		window, ok := strings.CutPrefix(strings.ToLower(key), claudeUnifiedHeaderPrefix)
		if !ok {
			continue
		}
		window, ok = strings.CutSuffix(window, "-utilization")
		if !ok || !validQuotaWindowID(window) || len(values) == 0 {
			continue
		}
		fraction, ok := parseQuotaNumber(values[len(values)-1])
		if !ok {
			continue
		}
		windows = append(windows, quotaWindowState{
			window:      window,
			usedPercent: roundPercent(fraction * 100),
			resetsAtMs:  parseUnixSecondsMs(headers.Get("Anthropic-Ratelimit-Unified-" + window + "-Reset")),
		})
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].window < windows[j].window })
	return windows
}

// codexQuotaWindows reads the X-Codex-Primary-* and X-Codex-Secondary-*
// windows. Each reports its used percent, its length in minutes, and its reset
// as Reset-At (Unix seconds) or Reset-After-Seconds.
func codexQuotaWindows(headers http.Header, observedAt time.Time) []quotaWindowState {
	var windows []quotaWindowState
	for _, slot := range []string{"Primary", "Secondary"} {
		prefix := "X-Codex-" + slot + "-"
		used, ok := parseQuotaNumber(headers.Get(prefix + "Used-Percent"))
		if !ok {
			continue
		}
		resetsAtMs := parseUnixSecondsMs(headers.Get(prefix + "Reset-At"))
		if after, okAfter := parseQuotaNumber(headers.Get(prefix + "Reset-After-Seconds")); resetsAtMs == 0 && okAfter {
			resetsAtMs = observedAt.UnixMilli() + int64(math.Round(after*1000))
		}
		windows = append(windows, quotaWindowState{
			window:      codexWindowID(headers.Get(prefix+"Window-Minutes"), strings.ToLower(slot)),
			usedPercent: roundPercent(used),
			resetsAtMs:  resetsAtMs,
		})
	}
	return windows
}

// codexWindowID names a window by its length, so a Codex weekly window and a
// Claude weekly window share the id "7d". A window of unknown length keeps
// the name of the header slot that reported it.
func codexWindowID(rawMinutes, slot string) string {
	minutes, errParse := strconv.Atoi(strings.TrimSpace(rawMinutes))
	switch {
	case errParse != nil || minutes <= 0:
		return slot
	case minutes%(24*60) == 0:
		return strconv.Itoa(minutes/(24*60)) + "d"
	case minutes%60 == 0:
		return strconv.Itoa(minutes/60) + "h"
	default:
		return strconv.Itoa(minutes) + "m"
	}
}

func validQuotaWindowID(window string) bool {
	if window == "" || len(window) > 32 {
		return false
	}
	for _, c := range window {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

func parseQuotaNumber(raw string) (float64, bool) {
	value, errParse := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if errParse != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, false
	}
	return value, true
}

// parseUnixSecondsMs converts Unix seconds to Unix milliseconds, or 0 when
// raw is not a time.
func parseUnixSecondsMs(raw string) int64 {
	seconds, ok := parseQuotaNumber(raw)
	if !ok {
		return 0
	}
	return int64(math.Round(seconds * 1000))
}

// roundPercent drops binary floating-point noise, so a reported 0.62 is
// stored as 62 rather than 62.00000000000001.
func roundPercent(percent float64) float64 {
	return math.Round(percent*1e6) / 1e6
}
