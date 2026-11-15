package usagestats

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

// pureIntervalShare is the share of an interval's cost one model must carry
// for the interval to count toward that model's own value.
const pureIntervalShare = 0.9

// maxRequestDuration bounds how long before a range a request may have
// started and still complete inside it.
const maxRequestDuration = 24 * time.Hour

// windowModelFamilies names the one model family that moves a quota window,
// keyed by provider and window. A Claude window "7d_<x>" that is not listed
// counts the family x, as "7d_sonnet" and "7d_opus" do. Every other window
// counts all models of the credential.
var windowModelFamilies = map[string]string{
	// Anthropic reports the Fable weekly limit under this window. While it is
	// exhausted, Opus requests on the same account still succeed (upstream
	// issue #7197).
	"claude/7d_oi": "fable",
}

// LimitSpend sums the requests of a set of intervals against the share of a
// limit those intervals used.
type LimitSpend struct {
	// Intervals counts the intervals between consecutive samples in which the
	// used percent rose.
	Intervals   int64   `json:"intervals"`
	PercentUsed float64 `json:"percent_used"`
	Metrics
	// USDPerPercent and OutputTokensPerPercent are nil when no percent was used.
	USDPerPercent          *float64 `json:"usd_per_percent"`
	OutputTokensPerPercent *float64 `json:"output_tokens_per_percent"`
}

// ModelLimitSpend is a model's spend over the intervals it dominated.
type ModelLimitSpend struct {
	Model string `json:"model"`
	LimitSpend
}

// LimitValue is what the usage of one quota window bought.
type LimitValue struct {
	QuotaSeriesKey
	// ModelScope is the model family whose requests move the window, or ""
	// when every model of the credential does.
	ModelScope string `json:"model_scope"`
	LimitSpend
	// Models lists, per model, the intervals in which that model carried at
	// least pureIntervalShare of the cost.
	Models []ModelLimitSpend `json:"models"`
}

// LimitValueResult is the result of a limit value query.
type LimitValueResult struct {
	Series []LimitValue `json:"series"`
	// UnpricedModels lists models in the counted intervals that have no known price.
	UnpricedModels []string `json:"unpriced_models"`
}

// tokenSum is the summed token buckets of requests of one model, ready to price.
type tokenSum struct {
	metrics      Metrics
	cacheWrite1h int64
}

// completedRequest is a request reduced to what limit value needs.
type completedRequest struct {
	doneMs int64
	model  string
	tokenSum
}

// LimitValue answers, for each quota window, what one percent of the limit
// bought. Each interval between consecutive samples in which the used percent
// rose is charged with the requests that completed after its first sample and
// no later than its second. An interval counts toward the range when its later
// sample falls in the range.
func (s *Store) LimitValue(ctx context.Context, q Query) (LimitValueResult, error) {
	result := LimitValueResult{Series: []LimitValue{}, UnpricedModels: []string{}}
	if s == nil {
		return result, errors.New("usagestats: store is closed")
	}
	byKey, errLoad := s.loadQuotaSeries(ctx, q)
	if errLoad != nil {
		return result, errLoad
	}
	prices, errPrices := s.priceTable(ctx)
	if errPrices != nil {
		return result, errPrices
	}
	requests, errRequests := s.completedRequests(ctx, byKey)
	if errRequests != nil {
		return result, errRequests
	}
	unpriced := make(map[string]struct{})
	for _, series := range byKey {
		result.Series = append(result.Series, limitValueOf(series, requests[series.AuthIndex], prices, unpriced))
	}
	slices.SortFunc(result.Series, func(a, b LimitValue) int { return compareQuotaSeriesKeys(a.QuotaSeriesKey, b.QuotaSeriesKey) })
	for model := range unpriced {
		result.UnpricedModels = append(result.UnpricedModels, model)
	}
	slices.Sort(result.UnpricedModels)
	return result, nil
}

// completedRequests reads, per credential, the requests that completed within
// the span of the series' intervals, ordered by completion.
func (s *Store) completedRequests(ctx context.Context, byKey map[QuotaSeriesKey]*QuotaSeries) (map[string][]completedRequest, error) {
	byAuth := make(map[string][]completedRequest)
	var authIndexes []any
	var fromMs, toMs int64
	for _, series := range byKey {
		points := seriesPoints(series)
		if len(points) < 2 {
			continue
		}
		if _, seen := byAuth[series.AuthIndex]; !seen {
			byAuth[series.AuthIndex] = nil
			authIndexes = append(authIndexes, series.AuthIndex)
		}
		if first := points[0].TimestampMs; fromMs == 0 || first < fromMs {
			fromMs = first
		}
		toMs = max(toMs, points[len(points)-1].TimestampMs)
	}
	if len(authIndexes) == 0 {
		return byAuth, nil
	}
	rows, errQuery := s.db.QueryContext(ctx,
		`SELECT auth_index, ts_ms + latency_ms, model, failed, input_tokens, cache_read_tokens, cache_write_tokens,
		cache_write_1h_tokens, output_tokens, reasoning_tokens, unclassified_tokens, total_tokens, web_search_requests
		FROM requests WHERE ts_ms >= ? AND ts_ms <= ? AND ts_ms + latency_ms > ? AND ts_ms + latency_ms <= ?
		AND auth_index IN (?`+strings.Repeat(",?", len(authIndexes)-1)+`)`,
		append([]any{fromMs - maxRequestDuration.Milliseconds(), toMs, fromMs, toMs}, authIndexes...)...,
	)
	if errQuery != nil {
		return nil, fmt.Errorf("usagestats: read requests for limit value: %w", errQuery)
	}
	defer closeRows(rows)
	for rows.Next() {
		var authIndex string
		var request completedRequest
		var failed bool
		m := &request.metrics
		if errScan := rows.Scan(&authIndex, &request.doneMs, &request.model, &failed, &m.InputTokens, &m.CacheReadTokens,
			&m.CacheWriteTokens, &request.cacheWrite1h, &m.OutputTokens, &m.ReasoningTokens, &m.UnclassifiedTokens,
			&m.TotalTokens, &m.WebSearchRequests); errScan != nil {
			return nil, fmt.Errorf("usagestats: scan request for limit value: %w", errScan)
		}
		m.Requests = 1
		if failed {
			m.Failed = 1
		}
		byAuth[authIndex] = append(byAuth[authIndex], request)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("usagestats: read requests for limit value: %w", errRows)
	}
	for _, requests := range byAuth {
		slices.SortStableFunc(requests, func(a, b completedRequest) int { return cmp.Compare(a.doneMs, b.doneMs) })
	}
	return byAuth, nil
}

// seriesPoints returns the carry-in point, when there is one, and the points
// in the range.
func seriesPoints(series *QuotaSeries) []QuotaPoint {
	if series.CarryIn == nil {
		return series.Points
	}
	return append([]QuotaPoint{*series.CarryIn}, series.Points...)
}

// limitValueOf charges each rising interval of a series with the requests of
// its scope that completed inside it.
func limitValueOf(series *QuotaSeries, requests []completedRequest, prices *PriceTable, unpriced map[string]struct{}) LimitValue {
	value := LimitValue{QuotaSeriesKey: series.QuotaSeriesKey, ModelScope: windowModelScope(series.Provider, series.Window)}
	byModel := make(map[string]*LimitSpend)
	points := seriesPoints(series)
	next := 0
	for i := 1; i < len(points); i++ {
		start, end := points[i-1], points[i]
		for next < len(requests) && requests[next].doneMs <= start.TimestampMs {
			next++
		}
		byModelTokens := make(map[string]*tokenSum)
		for ; next < len(requests) && requests[next].doneMs <= end.TimestampMs; next++ {
			request := requests[next]
			if value.ModelScope != "" && !modelInFamily(request.model, value.ModelScope) {
				continue
			}
			sum, ok := byModelTokens[request.model]
			if !ok {
				sum = &tokenSum{}
				byModelTokens[request.model] = sum
			}
			sum.metrics.add(request.metrics)
			sum.cacheWrite1h += request.cacheWrite1h
		}
		percent := end.UsedPercent - start.UsedPercent
		// A drop is a window reset or a redeemed reset grant, not usage. A rise
		// across a reset is not usage of one window either: usage the proxy did
		// not see can lift the new window above where the old one ended.
		if percent <= 0 || spansReset(start, end) {
			continue
		}
		var interval Metrics
		dominant, dominantCost := "", 0.0
		for model, sum := range byModelTokens {
			if !prices.priceMetrics(model, &sum.metrics, sum.cacheWrite1h) && sum.metrics.TotalTokens > 0 {
				unpriced[model] = struct{}{}
			}
			interval.add(sum.metrics)
			if sum.metrics.CostUSD > dominantCost {
				dominant, dominantCost = model, sum.metrics.CostUSD
			}
		}
		value.LimitSpend.add(percent, interval)
		if dominant != "" && dominantCost >= pureIntervalShare*interval.CostUSD {
			spend, ok := byModel[dominant]
			if !ok {
				spend = &LimitSpend{}
				byModel[dominant] = spend
			}
			spend.add(percent, interval)
		}
	}
	value.LimitSpend.finish()
	value.Models = make([]ModelLimitSpend, 0, len(byModel))
	for model, spend := range byModel {
		spend.finish()
		value.Models = append(value.Models, ModelLimitSpend{Model: model, LimitSpend: *spend})
	}
	slices.SortFunc(value.Models, func(a, b ModelLimitSpend) int { return strings.Compare(a.Model, b.Model) })
	return value
}

// spansReset reports whether two samples of a window report different resets.
// A sample with no reset time does not mark one.
func spansReset(start, end QuotaPoint) bool {
	if start.ResetsAtMs == 0 || end.ResetsAtMs == 0 {
		return false
	}
	shift := end.ResetsAtMs - start.ResetsAtMs
	return max(shift, -shift) >= quotaResetTolerance.Milliseconds()
}

func (l *LimitSpend) add(percent float64, interval Metrics) {
	l.Intervals++
	l.PercentUsed += percent
	l.Metrics.add(interval)
}

// finish rounds the summed percent and derives the per-percent ratios.
func (l *LimitSpend) finish() {
	l.PercentUsed = roundPercent(l.PercentUsed)
	if l.PercentUsed <= 0 {
		return
	}
	usd := l.CostUSD / l.PercentUsed
	output := float64(l.OutputTokens+l.ReasoningTokens) / l.PercentUsed
	l.USDPerPercent, l.OutputTokensPerPercent = &usd, &output
}

// windowModelScope returns the model family that moves a window, or "" when
// every model does.
func windowModelScope(provider, window string) string {
	if family, ok := windowModelFamilies[provider+"/"+window]; ok {
		return family
	}
	if family, ok := strings.CutPrefix(window, "7d_"); ok && provider == "claude" {
		return family
	}
	return ""
}

// modelInFamily reports whether family is one of the parts of a model name,
// so "claude-fable-5-1" is in the family "fable".
func modelInFamily(model, family string) bool {
	parts := strings.FieldsFunc(strings.ToLower(model), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	return slices.Contains(parts, family)
}
