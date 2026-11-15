package usagestats

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// maxQuotaPoints bounds the points returned for one quota series.
const maxQuotaPoints = 1000

// quotaResetTolerance is how far two reported resets of one window may differ
// and still be the same reset. Codex can report a reset as seconds from now,
// which shifts with every response's latency.
const quotaResetTolerance = time.Minute

// quotaFilterColumns maps the dimensions a quota query may filter by to their column.
var quotaFilterColumns = map[string]string{
	"provider":   "provider",
	"auth_index": "auth_index",
	"account":    "account",
	"window":     `"window"`,
}

// QuotaSeries is the recorded history of one quota window in a time range.
type QuotaSeries struct {
	QuotaSeriesKey
	// CarryIn is the newest sample before the range, so a chart can start at
	// the range's left edge. It is nil when the series has no earlier sample.
	CarryIn *QuotaPoint `json:"carry_in"`
	// Points are the samples in the range, oldest first.
	Points []QuotaPoint `json:"points"`
	// Thinned reports that Points keeps only the highest and the last sample of
	// each bucket of QuotaHistory.BucketMs.
	Thinned bool `json:"thinned"`
}

// QuotaHistory is the result of a quota sample query.
type QuotaHistory struct {
	Series []QuotaSeries `json:"series"`
	// BucketMs is the bucket width used to thin series, or 0 when none was thinned.
	BucketMs int64 `json:"bucket_ms"`
}

// RecordQuotaSample stores a sample when it reports a newer state of its window
// than the newest stored sample. The newest sample of each series is cached,
// so recording costs a map lookup unless the state changed.
func (s *Store) RecordQuotaSample(ctx context.Context, sample QuotaSample) error {
	if s == nil {
		return errors.New("usagestats: store is closed")
	}
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	newest, known := s.newestQuota[sample.QuotaSeriesKey]
	if !known {
		var errNewest error
		newest, known, errNewest = s.newestStoredQuotaPoint(ctx, sample.QuotaSeriesKey)
		if errNewest != nil {
			return errNewest
		}
		if known {
			s.newestQuota[sample.QuotaSeriesKey] = newest
		}
	}
	if known && !quotaPointAdvances(newest, sample.QuotaPoint) {
		return nil
	}
	_, errExec := s.db.ExecContext(ctx,
		`INSERT INTO quota_samples (ts_ms, provider, auth_index, account, "window", used_percent, resets_at_ms) VALUES (?,?,?,?,?,?,?)`,
		sample.TimestampMs, sample.Provider, sample.AuthIndex, sample.Account, sample.Window, sample.UsedPercent, sample.ResetsAtMs,
	)
	if errExec != nil {
		return fmt.Errorf("usagestats: insert quota sample: %w", errExec)
	}
	s.newestQuota[sample.QuotaSeriesKey] = sample.QuotaPoint
	return nil
}

// quotaPointAdvances reports whether next is a newer state of a window than
// newest. A point observed before newest arrives late, typically from a long
// stream, and never replaces a newer state.
func quotaPointAdvances(newest, next QuotaPoint) bool {
	if next.TimestampMs < newest.TimestampMs {
		return false
	}
	if next.UsedPercent != newest.UsedPercent {
		return true
	}
	if next.ResetsAtMs == 0 {
		return false
	}
	shift := next.ResetsAtMs - newest.ResetsAtMs
	return newest.ResetsAtMs == 0 || max(shift, -shift) >= quotaResetTolerance.Milliseconds()
}

func (s *Store) newestStoredQuotaPoint(ctx context.Context, key QuotaSeriesKey) (QuotaPoint, bool, error) {
	var point QuotaPoint
	errScan := s.db.QueryRowContext(ctx,
		`SELECT ts_ms, used_percent, resets_at_ms FROM quota_samples
		WHERE provider = ? AND auth_index = ? AND "window" = ? AND account = ?
		ORDER BY ts_ms DESC, id DESC LIMIT 1`,
		key.Provider, key.AuthIndex, key.Window, key.Account,
	).Scan(&point.TimestampMs, &point.UsedPercent, &point.ResetsAtMs)
	if errors.Is(errScan, sql.ErrNoRows) {
		return point, false, nil
	}
	if errScan != nil {
		return point, false, fmt.Errorf("usagestats: read newest quota sample: %w", errScan)
	}
	return point, true, nil
}

// quotaFilter returns the query's series filters as SQL to append to a WHERE
// clause, with their arguments.
func (q Query) quotaFilter() (string, []any, error) {
	if q.From.IsZero() || q.To.IsZero() || !q.To.After(q.From) {
		return "", nil, fmt.Errorf("%w: from must be before to", ErrInvalidQuery)
	}
	names := make([]string, 0, len(q.Filters))
	for name := range q.Filters {
		names = append(names, name)
	}
	sort.Strings(names)
	var filter strings.Builder
	args := make([]any, 0, len(names))
	for _, name := range names {
		column, ok := quotaFilterColumns[name]
		if !ok {
			return "", nil, fmt.Errorf("%w: unknown filter %q", ErrInvalidQuery, name)
		}
		filter.WriteString(" AND " + column + " = ?")
		args = append(args, q.Filters[name])
	}
	return filter.String(), args, nil
}

// QuotaSamples returns the quota series that match the query's filters and
// have a sample in the range or before it.
func (s *Store) QuotaSamples(ctx context.Context, q Query) (QuotaHistory, error) {
	history := QuotaHistory{Series: []QuotaSeries{}}
	if s == nil {
		return history, errors.New("usagestats: store is closed")
	}
	byKey, errLoad := s.loadQuotaSeries(ctx, q)
	if errLoad != nil {
		return history, errLoad
	}
	bucketMs := quotaBucketMs(q)
	for _, series := range byKey {
		if len(series.Points) > maxQuotaPoints {
			series.Points = thinQuotaPoints(series.Points, q.From.UnixMilli(), bucketMs)
			series.Thinned = true
			history.BucketMs = bucketMs
		}
		history.Series = append(history.Series, *series)
	}
	slices.SortFunc(history.Series, func(a, b QuotaSeries) int { return compareQuotaSeriesKeys(a.QuotaSeriesKey, b.QuotaSeriesKey) })
	return history, nil
}

// loadQuotaSeries reads every matching series: its points in the range and
// the newest point before it.
func (s *Store) loadQuotaSeries(ctx context.Context, q Query) (map[QuotaSeriesKey]*QuotaSeries, error) {
	filter, filterArgs, errFilter := q.quotaFilter()
	if errFilter != nil {
		return nil, errFilter
	}
	byKey := make(map[QuotaSeriesKey]*QuotaSeries)
	seriesOf := func(key QuotaSeriesKey) *QuotaSeries {
		series, ok := byKey[key]
		if !ok {
			series = &QuotaSeries{QuotaSeriesKey: key, Points: []QuotaPoint{}}
			byKey[key] = series
		}
		return series
	}

	// SQLite takes the bare columns of a MAX() aggregate from the row holding the maximum.
	carryIns, errCarry := s.db.QueryContext(ctx,
		`SELECT provider, auth_index, account, "window", MAX(ts_ms), used_percent, resets_at_ms
		FROM quota_samples WHERE ts_ms < ?`+filter+`
		GROUP BY provider, auth_index, account, "window"`,
		append([]any{q.From.UnixMilli()}, filterArgs...)...,
	)
	if errCarry != nil {
		return nil, fmt.Errorf("usagestats: read quota carry-in samples: %w", errCarry)
	}
	errScan := scanQuotaSamples(carryIns, func(sample QuotaSample) {
		point := sample.QuotaPoint
		seriesOf(sample.QuotaSeriesKey).CarryIn = &point
	})
	if errScan != nil {
		return nil, errScan
	}

	points, errPoints := s.db.QueryContext(ctx,
		`SELECT provider, auth_index, account, "window", ts_ms, used_percent, resets_at_ms
		FROM quota_samples WHERE ts_ms >= ? AND ts_ms < ?`+filter+`
		ORDER BY ts_ms, id`,
		append([]any{q.From.UnixMilli(), q.To.UnixMilli()}, filterArgs...)...,
	)
	if errPoints != nil {
		return nil, fmt.Errorf("usagestats: read quota samples: %w", errPoints)
	}
	errScan = scanQuotaSamples(points, func(sample QuotaSample) {
		series := seriesOf(sample.QuotaSeriesKey)
		series.Points = append(series.Points, sample.QuotaPoint)
	})
	if errScan != nil {
		return nil, errScan
	}
	return byKey, nil
}

func scanQuotaSamples(rows *sql.Rows, visit func(QuotaSample)) error {
	defer closeRows(rows)
	for rows.Next() {
		var sample QuotaSample
		if errScan := rows.Scan(&sample.Provider, &sample.AuthIndex, &sample.Account, &sample.Window,
			&sample.TimestampMs, &sample.UsedPercent, &sample.ResetsAtMs); errScan != nil {
			return fmt.Errorf("usagestats: scan quota sample: %w", errScan)
		}
		visit(sample)
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("usagestats: read quota samples: %w", errRows)
	}
	return nil
}

// quotaBucketMs splits the range into maxQuotaPoints/2 buckets, since thinning
// keeps up to two points per bucket.
func quotaBucketMs(q Query) int64 {
	buckets := int64(maxQuotaPoints / 2)
	span := q.To.UnixMilli() - q.From.UnixMilli()
	return max((span+buckets-1)/buckets, 1)
}

// thinQuotaPoints keeps the highest and the last point of each bucket. The
// highest point keeps the peak a window reached before it reset.
func thinQuotaPoints(points []QuotaPoint, fromMs, bucketMs int64) []QuotaPoint {
	thinned := make([]QuotaPoint, 0, maxQuotaPoints)
	for start := 0; start < len(points); {
		bucket := (points[start].TimestampMs - fromMs) / bucketMs
		peak, end := start, start
		for end < len(points) && (points[end].TimestampMs-fromMs)/bucketMs == bucket {
			if points[end].UsedPercent > points[peak].UsedPercent {
				peak = end
			}
			end++
		}
		if peak != end-1 {
			thinned = append(thinned, points[peak])
		}
		thinned = append(thinned, points[end-1])
		start = end
	}
	return thinned
}

func compareQuotaSeriesKeys(a, b QuotaSeriesKey) int {
	return cmp.Or(
		cmp.Compare(a.Provider, b.Provider),
		cmp.Compare(a.AuthIndex, b.AuthIndex),
		cmp.Compare(a.Account, b.Account),
		cmp.Compare(a.Window, b.Window),
	)
}
