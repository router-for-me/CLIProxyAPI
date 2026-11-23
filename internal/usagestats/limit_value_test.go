package usagestats

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// simWindow is a quota window of the simulated upstream. A request moves it,
// when the request completes, by its list-price cost divided by its model's
// rate in USD per percent. A model without a rate does not move it.
type simWindow struct {
	id       string
	rates    map[string]float64
	used     float64
	resetsAt time.Time
}

type simRequest struct {
	provider, authIndex, model string
	start                      time.Time
	ttft, latency              time.Duration
	stream                     bool
	input, output              int64
	headers                    http.Header
}

func (r *simRequest) doneAt() time.Time { return r.start.Add(r.latency) }

func (r *simRequest) costUSD() float64 {
	price := simPrices[r.model]
	return float64(r.input)*price[0]/1e6 + float64(r.output)*price[1]/1e6
}

// simPrices are the built-in list prices of the simulated models, in USD per
// million input and output tokens.
var simPrices = map[string][2]float64{
	"claude-opus-5-5":  {4, 20},
	"claude-fable-5-1": {10, 50},
	"gpt-5.5":          {5, 30},
}

// simulateUpstream runs requests and window resets in time order. A request's
// headers report every window as it stood when the request started, floored
// to a whole percent, the way Anthropic reports a two-decimal fraction and
// Codex an integer percent.
func simulateUpstream(windows map[string][]*simWindow, requests []*simRequest, resets map[time.Time]func()) {
	type event struct {
		at    time.Time
		order int
		run   func()
	}
	var events []event
	for at, reset := range resets {
		events = append(events, event{at: at, run: reset})
	}
	for _, request := range requests {
		events = append(events, event{at: request.start, order: 1, run: func() {
			request.headers = make(http.Header)
			for _, window := range windows[request.provider] {
				if request.provider == "claude" && window.id == "7d_oi" && !strings.Contains(request.model, "fable") {
					continue
				}
				used := math.Floor(math.Round(window.used*1e9) / 1e9)
				switch request.provider {
				case "claude":
					request.headers.Set("Anthropic-Ratelimit-Unified-"+window.id+"-Utilization", strconv.FormatFloat(used/100, 'f', 2, 64))
					request.headers.Set("Anthropic-Ratelimit-Unified-"+window.id+"-Reset", strconv.FormatInt(window.resetsAt.Unix(), 10))
				case "codex":
					minutes := map[string]string{"5h": "300", "7d": "10080"}[window.id]
					slot := map[string]string{"5h": "Primary", "7d": "Secondary"}[window.id]
					request.headers.Set("X-Codex-"+slot+"-Used-Percent", strconv.FormatFloat(used, 'f', 0, 64))
					request.headers.Set("X-Codex-"+slot+"-Window-Minutes", minutes)
					request.headers.Set("X-Codex-"+slot+"-Reset-At", strconv.FormatInt(window.resetsAt.Unix(), 10))
				}
			}
		}})
		events = append(events, event{at: request.doneAt(), order: 2, run: func() {
			for _, window := range windows[request.provider] {
				if rate, ok := window.rates[request.model]; ok {
					window.used += request.costUSD() / rate
				}
			}
		}})
	}
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].at.Equal(events[j].at) {
			return events[i].at.Before(events[j].at)
		}
		return events[i].order < events[j].order
	})
	for _, e := range events {
		e.run()
	}
}

// recordInCompletionOrder publishes each request's usage when it completes,
// as the proxy does.
func recordInCompletionOrder(requests []*simRequest) {
	ordered := append([]*simRequest(nil), requests...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].doneAt().Before(ordered[j].doneAt()) })
	for _, request := range ordered {
		recorder{}.HandleUsage(context.Background(), coreusage.Record{
			Provider:        request.provider,
			Model:           request.model,
			AuthIndex:       request.authIndex,
			AuthType:        "oauth",
			Source:          request.authIndex + "@example.com",
			Stream:          request.stream,
			RequestedAt:     request.start,
			TTFT:            request.ttft,
			Latency:         request.latency,
			ResponseHeaders: request.headers,
			Detail:          coreusage.Detail{TokenBreakdown: coreusage.NewIndependentTokenBreakdown(request.input, 0, 0, request.output, 0, request.input+request.output)},
		})
	}
}

func withinRelative(got *float64, want, tolerance float64) bool {
	return got != nil && math.Abs(*got-want) <= tolerance*want
}

// TestLimitValueRecoversKnownDollarsPerPercent drives the recorder with a
// simulated Claude and Codex account whose limits cost a known number of list
// price dollars per percent, and checks that LimitValue recovers them.
//
// The scenario covers 1% quantized reports, a window reset, a model-scoped
// window, intervals that mix two models, and a $34 stream that is in flight
// across every sample of the Fable phase. Quantization error cancels inside a
// run of rising intervals and leaves at most one request's share of the
// window at its ends. A request is at most 0.3% of a window here and every
// checked figure rests on at least 14%, so 2% bounds the relative error.
func TestLimitValueRecoversKnownDollarsPerPercent(t *testing.T) {
	store := configureTestRecorder(t)
	const opus, fable, codex = "claude-opus-5-5", "claude-fable-5-1", "gpt-5.5"
	const tolerance = 0.02
	weekReset := testDay.Add(100 * time.Hour)
	fiveHour := &simWindow{id: "5h", rates: map[string]float64{opus: 16, fable: 16}, resetsAt: testDay.Add(5 * time.Hour)}
	windows := map[string][]*simWindow{
		"claude": {
			fiveHour,
			{id: "7d", rates: map[string]float64{opus: 34, fable: 14}, resetsAt: weekReset},
			{id: "7d_oi", rates: map[string]float64{fable: 7}, resetsAt: weekReset},
		},
		"codex": {
			{id: "5h", rates: map[string]float64{codex: 3}, resetsAt: testDay.Add(5 * time.Hour)},
			{id: "7d", rates: map[string]float64{codex: 12}, resetsAt: weekReset},
		},
	}

	var requests []*simRequest
	at := testDay
	claude := func(model string) {
		input, output := int64(10000), int64(198000)
		if model == fable {
			input, output = 4000, 79200
		}
		stream := len(requests)%5 != 4
		requests = append(requests, &simRequest{provider: "claude", authIndex: "auth-c", model: model, start: at,
			ttft: time.Second, latency: 3 * time.Second, stream: stream, input: input, output: output})
		at = at.Add(10 * time.Second)
	}
	for range 200 {
		claude(opus)
	}
	resetAt := at.Add(-5 * time.Second)
	held := &simRequest{provider: "claude", authIndex: "auth-c", model: opus, start: at, ttft: time.Second,
		stream: true, input: 10000, output: 1698000}
	requests = append(requests, held)
	at = at.Add(10 * time.Second)
	for range 100 {
		claude(fable)
	}
	held.latency = at.Sub(held.start) - 5*time.Second
	for i := range 80 {
		if i%2 == 0 {
			claude(opus)
		} else {
			claude(fable)
		}
	}
	at = testDay
	for range 60 {
		requests = append(requests, &simRequest{provider: "codex", authIndex: "auth-x", model: codex, start: at,
			ttft: time.Second, latency: 3 * time.Second, stream: true, input: 6000, output: 99000})
		at = at.Add(17 * time.Second)
	}
	simulateUpstream(windows, requests, map[time.Time]func(){resetAt: func() {
		fiveHour.used = 0
		fiveHour.resetsAt = fiveHour.resetsAt.Add(5 * time.Hour)
	}})
	recordInCompletionOrder(requests)

	q := Query{From: testDay, To: testDay.Add(24 * time.Hour)}
	result, errValue := store.LimitValue(context.Background(), q)
	if errValue != nil {
		t.Fatalf("limit value: %v", errValue)
	}
	byKey := make(map[string]LimitValue, len(result.Series))
	for _, value := range result.Series {
		byKey[value.Provider+"/"+value.Window] = value
		t.Logf("%s/%s scope=%q intervals=%d percent=%.2f cost=$%.2f usd/%%=%.3f", value.Provider, value.Window, value.ModelScope,
			value.Intervals, value.PercentUsed, value.CostUSD, deref(value.USDPerPercent))
		for _, model := range value.Models {
			t.Logf("  %s intervals=%d percent=%.2f usd/%%=%.3f", model.Model, model.Intervals, model.PercentUsed, deref(model.USDPerPercent))
		}
	}
	if len(result.Series) != 5 {
		t.Fatalf("series = %d, want claude 5h, 7d, 7d_oi and codex 5h, 7d", len(result.Series))
	}

	for key, want := range map[string]float64{"claude/5h": 16, "claude/7d_oi": 7, "codex/5h": 3, "codex/7d": 12} {
		if value := byKey[key]; !withinRelative(value.USDPerPercent, want, tolerance) {
			t.Errorf("%s usd_per_percent = %v, want %v within %.0f%%", key, deref(value.USDPerPercent), want, tolerance*100)
		}
	}
	week := byKey["claude/7d"]
	for model, want := range map[string]float64{opus: 34, fable: 14} {
		found := false
		for _, spend := range week.Models {
			if spend.Model == model {
				found = true
				if !withinRelative(spend.USDPerPercent, want, tolerance) || spend.PercentUsed < 20 {
					t.Errorf("7d %s usd_per_percent = %v over %.1f%%, want %v within %.0f%% over at least 20%%",
						model, deref(spend.USDPerPercent), spend.PercentUsed, want, tolerance*100)
				}
			}
		}
		if !found {
			t.Errorf("7d has no %s breakdown: %+v", model, week.Models)
		}
	}

	scoped := byKey["claude/7d_oi"]
	if scoped.ModelScope != "fable" || len(scoped.Models) != 1 || scoped.Models[0].Model != fable {
		t.Fatalf("7d_oi scope = %q models = %+v, want fable requests only", scoped.ModelScope, scoped.Models)
	}
	if fableOnly := scoped.Models[0]; !withinRelative(fableOnly.USDPerPercent, 7, tolerance) || fableOnly.Intervals != scoped.Intervals {
		t.Errorf("7d_oi fable usd_per_percent = %v over %d of %d intervals, want 7 within %.0f%% over every interval",
			deref(fableOnly.USDPerPercent), fableOnly.Intervals, scoped.Intervals, tolerance*100)
	}
	if week.ModelScope != "" || byKey["claude/5h"].ModelScope != "" {
		t.Errorf("shared windows have scopes %q and %q, want none", week.ModelScope, byKey["claude/5h"].ModelScope)
	}

	// The blended 7d figure charges exactly the requests that completed between
	// its first and last sample, against a percent within one step of the truth.
	history := mustQuotaSamples(t, store, Query{From: q.From, To: q.To, Filters: map[string]string{"provider": "claude", "window": "7d"}})
	points := history.Series[0].Points
	first, last := points[0].TimestampMs, points[len(points)-1].TimestampMs
	var cost, truePercent float64
	var counted int64
	for _, request := range requests {
		if done := request.doneAt().UnixMilli(); request.provider == "claude" && done > first && done <= last {
			cost += request.costUSD()
			truePercent += request.costUSD() / windows["claude"][1].rates[request.model]
			counted++
		}
	}
	if math.Abs(week.CostUSD-cost) > 1e-6 || week.Requests != counted || math.Abs(week.PercentUsed-truePercent) > 1 {
		t.Errorf("7d blended = $%.4f over %d requests and %.2f%%, want $%.4f over %d requests and %.2f%% within one step",
			week.CostUSD, week.Requests, week.PercentUsed, cost, counted, truePercent)
	}
	if want := float64(week.OutputTokens+week.ReasoningTokens) / week.PercentUsed; week.OutputTokensPerPercent == nil || math.Abs(*week.OutputTokensPerPercent-want) > 1e-6 {
		t.Errorf("output_tokens_per_percent = %v, want %v", deref(week.OutputTokensPerPercent), want)
	}
}

func deref(v *float64) float64 {
	if v == nil {
		return math.NaN()
	}
	return *v
}

func TestLimitValueReportsNoRatioWithoutARise(t *testing.T) {
	store := openTestStore(t)
	key := QuotaSeriesKey{Provider: "claude", AuthIndex: "auth-1", Account: "me@example.com", Window: "5h"}
	mustRecordQuota(t, store,
		samplePoint(key, testDay, 40, testDay.Add(time.Hour)),
		samplePoint(key, testDay.Add(2*time.Hour), 0, testDay.Add(6*time.Hour)),
	)
	mustInsert(t, store, Row{Timestamp: testDay.Add(time.Hour), Model: "claude-opus-5-5", AuthIndex: "auth-1", OutputTokens: 1000, TotalTokens: 1000})

	result, errValue := store.LimitValue(context.Background(), Query{From: testDay, To: testDay.Add(24 * time.Hour)})
	if errValue != nil {
		t.Fatalf("limit value: %v", errValue)
	}
	if len(result.Series) != 1 {
		t.Fatalf("series = %+v", result.Series)
	}
	value := result.Series[0]
	if value.Intervals != 0 || value.PercentUsed != 0 || value.Requests != 0 || value.USDPerPercent != nil || value.OutputTokensPerPercent != nil || len(value.Models) != 0 {
		t.Fatalf("value = %+v, want a reset with no counted interval and no ratios", value)
	}
}

func TestLimitValueSkipsARiseAcrossAReset(t *testing.T) {
	store := openTestStore(t)
	key := QuotaSeriesKey{Provider: "claude", AuthIndex: "auth-1", Account: "me@example.com", Window: "5h"}
	firstReset, secondReset := testDay.Add(5*time.Hour), testDay.Add(11*time.Hour)
	mustRecordQuota(t, store,
		samplePoint(key, testDay, 10, firstReset),
		samplePoint(key, testDay.Add(time.Hour), 12, firstReset),
		samplePoint(key, testDay.Add(6*time.Hour), 20, secondReset),
		samplePoint(key, testDay.Add(7*time.Hour), 21, secondReset),
	)
	for _, at := range []time.Duration{30 * time.Minute, 5*time.Hour + 30*time.Minute, 6*time.Hour + 30*time.Minute} {
		mustInsert(t, store, Row{Timestamp: testDay.Add(at), Model: "claude-opus-5-5", AuthIndex: "auth-1", OutputTokens: 1000, TotalTokens: 1000})
	}

	result, errValue := store.LimitValue(context.Background(), Query{From: testDay, To: testDay.Add(24 * time.Hour)})
	if errValue != nil {
		t.Fatalf("limit value: %v", errValue)
	}
	if len(result.Series) != 1 {
		t.Fatalf("series = %+v", result.Series)
	}
	value := result.Series[0]
	if value.Intervals != 2 || value.PercentUsed != 3 || value.Requests != 2 {
		t.Fatalf("value = %+v, want the 2%% and 1%% rises inside one window each and not the 8%% rise across the reset", value)
	}
}

func TestLimitValueChargesARequestThatCompletesAsTheNextStartsBeforeIt(t *testing.T) {
	store := configureTestRecorder(t)
	record := func(at time.Time, utilization string) {
		recorder{}.HandleUsage(context.Background(), coreusage.Record{
			Provider:        "codex",
			Model:           "gpt-5.5",
			AuthIndex:       "auth-x",
			RequestedAt:     at,
			Latency:         time.Second,
			ResponseHeaders: headerOf("X-Codex-Primary-Used-Percent", utilization, "X-Codex-Primary-Window-Minutes", "300"),
			Detail:          coreusage.Detail{TokenBreakdown: coreusage.NewIndependentTokenBreakdown(0, 0, 0, 100_000, 0, 100_000)},
		})
	}
	// A sequential client sends each request in the millisecond the previous
	// one completed, and the upstream already counted the previous one.
	record(testDay, "0")
	record(testDay.Add(time.Second), "1")
	record(testDay.Add(2*time.Second), "2")

	result, errValue := store.LimitValue(context.Background(), Query{From: testDay, To: testDay.Add(time.Hour)})
	if errValue != nil {
		t.Fatalf("limit value: %v", errValue)
	}
	value := result.Series[0]
	if value.Intervals != 2 || value.Requests != 2 || value.USDPerPercent == nil || *value.USDPerPercent != 3 {
		t.Fatalf("value = %+v, want the first two $3 requests charged to the two 1%% rises", value)
	}
	// The series embeds Metrics, so it carries the cost split too: all output.
	if !approxEqual(value.OutputCostUSD, 6) || value.InputCostUSD != 0 || !approxEqual(value.CostUSD, 6) {
		t.Fatalf("cost = %v split output %v input %v, want 6 all output", value.CostUSD, value.OutputCostUSD, value.InputCostUSD)
	}
}

func TestModelInFamilyMatchesNameParts(t *testing.T) {
	for model, want := range map[string]bool{
		"claude-fable-5-1":           true,
		"devin/claude-fable-5-1":     true,
		"claude-fable-5-dd-abc":      true,
		"claude-opus-5-5":            false,
		"claude-fableish-5":          false,
		"CLAUDE-FABLE-5-1-20261101":  true,
		"claude-sonnet-4-5-20250929": false,
	} {
		if got := modelInFamily(model, "fable"); got != want {
			t.Errorf("modelInFamily(%q, fable) = %t, want %t", model, got, want)
		}
	}
	for window, want := range map[string]string{"5h": "", "7d": "", "7d_oi": "fable", "7d_sonnet": "sonnet", "7d_opus": "opus"} {
		if got := windowModelScope("claude", window); got != want {
			t.Errorf("claude %s scope = %q, want %q", window, got, want)
		}
	}
	if got := windowModelScope("codex", "7d_x"); got != "" {
		t.Errorf("codex 7d_x scope = %q, want none", got)
	}
}
