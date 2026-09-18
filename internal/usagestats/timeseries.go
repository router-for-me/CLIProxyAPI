package usagestats

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxHourlyRangeHours = 31 * 24 // 31 days max for hourly step
	maxDailyRangeDays   = 90      // 90 days max for daily step
)

// Filter specifies optional filter dimensions for queries.
type Filter struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Account  string `json:"account,omitempty"`
}

// TimeseriesBucket holds token and request counts for one time interval.
type TimeseriesBucket struct {
	Timestamp                string `json:"timestamp"`
	Records                  int64  `json:"records"`
	Failures                 int64  `json:"failures"`
	UncachedInputTokens      int64  `json:"uncached_input_tokens"`
	CacheReadTokens          int64  `json:"cache_read_tokens"`
	CacheWriteTokens         int64  `json:"cache_write_tokens"`
	NonReasoningOutputTokens int64  `json:"non_reasoning_output_tokens"`
	ReasoningTokens          int64  `json:"reasoning_tokens"`
	TotalTokens              int64  `json:"total_tokens"`
}

// QueryTimeseries aggregates usage stats into continuous, zero-filled time buckets
// (by "hour" or "day"). Time parameters preserve their location for local alignment.
func QueryTimeseries(dir string, from, to time.Time, step string, filter Filter) ([]TimeseriesBucket, error) {
	if to.Before(from) {
		from, to = to, from
	}

	loc := from.Location()
	step = strings.ToLower(strings.TrimSpace(step))
	if step != "day" {
		step = "hour"
	}

	var buckets []TimeseriesBucket
	var bucketIndex map[string]int
	var effectiveStart, effectiveEnd time.Time

	if step == "hour" {
		start := from.Truncate(time.Hour)
		end := to.Truncate(time.Hour)
		effectiveStart = start
		effectiveEnd = end.Add(time.Hour)
		hours := int(end.Sub(start) / time.Hour)
		if hours > maxHourlyRangeHours {
			return nil, fmt.Errorf("usagestats: hourly query range exceeds %d hours", maxHourlyRangeHours)
		}
		bucketCount := hours + 1
		buckets = make([]TimeseriesBucket, 0, bucketCount)
		bucketIndex = make(map[string]int, bucketCount)
		for t := start; !t.After(end); t = t.Add(time.Hour) {
			tsStr := t.Format(time.RFC3339)
			bucketIndex[tsStr] = len(buckets)
			buckets = append(buckets, TimeseriesBucket{Timestamp: tsStr})
		}
	} else {
		start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
		end := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc)
		effectiveStart = start
		effectiveEnd = end.AddDate(0, 0, 1)
		days := int(end.Sub(start) / (24 * time.Hour))
		if days > maxDailyRangeDays {
			return nil, fmt.Errorf("usagestats: daily query range exceeds %d days", maxDailyRangeDays)
		}
		bucketCount := days + 1
		buckets = make([]TimeseriesBucket, 0, bucketCount)
		bucketIndex = make(map[string]int, bucketCount)
		for t := start; !t.After(end); t = t.AddDate(0, 0, 1) {
			tsStr := t.Format(time.RFC3339)
			bucketIndex[tsStr] = len(buckets)
			buckets = append(buckets, TimeseriesBucket{Timestamp: tsStr})
		}
	}

	utcStartDay := effectiveStart.UTC().Truncate(24 * time.Hour)
	utcEndDay := effectiveEnd.UTC().Truncate(24 * time.Hour)

	providerFilter := strings.TrimSpace(filter.Provider)
	modelFilter := strings.TrimSpace(filter.Model)
	accountFilter := strings.TrimSpace(filter.Account)

	for day := utcStartDay; !day.After(utcEndDay); day = day.AddDate(0, 0, 1) {
		dateStr := day.Format(dateLayout)
		path := filepath.Join(dir, "usage-"+dateStr+".jsonl")

		f, errOpen := os.Open(path)
		if errOpen != nil {
			if os.IsNotExist(errOpen) {
				continue
			}
			return nil, fmt.Errorf("usagestats: open %s: %w", path, errOpen)
		}

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var e entry
			if errUnmarshal := json.Unmarshal(line, &e); errUnmarshal != nil {
				continue
			}

			if e.Timestamp.Before(effectiveStart) || !e.Timestamp.Before(effectiveEnd) {
				continue
			}
			if providerFilter != "" && e.Provider != providerFilter {
				continue
			}
			if modelFilter != "" && e.Model != modelFilter {
				continue
			}
			if accountFilter != "" && e.Account != accountFilter {
				continue
			}

			var bKey string
			if step == "hour" {
				bKey = e.Timestamp.In(loc).Truncate(time.Hour).Format(time.RFC3339)
			} else {
				tLoc := e.Timestamp.In(loc)
				bKey = time.Date(tLoc.Year(), tLoc.Month(), tLoc.Day(), 0, 0, 0, 0, loc).Format(time.RFC3339)
			}

			idx, found := bucketIndex[bKey]
			if !found {
				continue
			}

			b := &buckets[idx]
			b.Records++
			if e.Failed {
				b.Failures++
			}
			b.UncachedInputTokens += e.TokenBreakdown.Input.UncachedTokens
			b.CacheReadTokens += e.TokenBreakdown.Input.CacheReadTokens
			b.CacheWriteTokens += e.TokenBreakdown.Input.CacheWriteTokens
			b.NonReasoningOutputTokens += e.TokenBreakdown.Output.NonReasoningTokens
			b.ReasoningTokens += e.TokenBreakdown.Output.ReasoningTokens
			b.TotalTokens += e.TokenBreakdown.TotalTokens
		}
		_ = f.Close()
		if errScan := scanner.Err(); errScan != nil {
			return nil, fmt.Errorf("usagestats: scan %s: %w", path, errScan)
		}
	}

	return buckets, nil
}
