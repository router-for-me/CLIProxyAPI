package multiuser

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type journalCacheEntry struct {
	value   any
	weight  int
	version uint64
	at      time.Time
}

type journalFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	users   int
	started bool
	value   any
	err     error
}

// aggregateSlots bounds how many full-index scans run at once. Each scan is
// CPU bound, so more than a couple only make every request slower on a small
// host; extra requests wait their turn and share identical results.
const aggregateSlots = 2

var errAggregateAbandoned = errors.New("aggregate abandoned before it started")

// aggregateTTL is how long a stale analytics entry may still be served. Usage
// appends continuously, so a strict version check would drop every entry on
// the next write; ~10s of freshness is fine for statistics.
const aggregateTTL = 10 * time.Second

// aggregateStaleOK reports whether a cache key belongs to an analytics
// endpoint that tolerates a slightly stale result.
func aggregateStaleOK(key string) bool {
	return strings.HasPrefix(key, "overview:") ||
		strings.HasPrefix(key, "summary:") ||
		strings.HasPrefix(key, "facets:")
}

// aggregate shares identical reads and bounds how many run at once. Work that
// has not started is dropped when its last reader leaves; work already running
// finishes and is cached, so a reader who navigates away and back (or retries
// after a client timeout) is served from the cache instead of paying again.
func (j *journalIndex) aggregate(ctx context.Context, key string, compute func(context.Context) (any, int, error)) (any, error) {
	j.cacheMu.Lock()
	// The cache key stays unversioned so an entry written under an older journal
	// version can still be served when it is fresh enough.
	if entry, ok := j.cache[key]; ok &&
		(entry.version == j.version || (aggregateStaleOK(key) && j.store.now().Sub(entry.at) <= aggregateTTL)) {
		j.cacheMu.Unlock()
		return entry.value, nil
	}
	flight := j.flights[key]
	if flight == nil {
		shared, cancel := context.WithCancel(j.ctx)
		flight = &journalFlight{done: make(chan struct{}), cancel: cancel}
		j.flights[key] = flight
		// The entry is stamped with the version it was computed against; a later
		// journal write demotes it to the stale-TTL path instead of letting it
		// claim freshness.
		version := j.version
		go func() {
			var value any
			var weight int
			var err error
			if strings.HasPrefix(key, "count:") {
				// A count is far cheaper than the statistics scans; queueing it
				// behind them would stall page loads for no CPU benefit.
				j.cacheMu.Lock()
				flight.started = true
				j.cacheMu.Unlock()
				value, weight, err = compute(j.ctx)
			} else {
				select {
				case j.slots <- struct{}{}:
					j.cacheMu.Lock()
					flight.started = true
					j.cacheMu.Unlock()
					value, weight, err = compute(j.ctx)
					<-j.slots
				case <-shared.Done():
					err = errAggregateAbandoned
				}
			}
			j.cacheMu.Lock()
			flight.value, flight.err = value, err
			if err == nil && j.ctx.Err() == nil && weight <= 32768 {
				if len(j.cache) >= 32 || j.weight+weight > 32768 {
					j.cache = map[string]journalCacheEntry{}
					j.weight = 0
				}
				j.cache[key] = journalCacheEntry{value: value, weight: weight, version: version, at: j.store.now()}
				j.weight += weight
			}
			if j.flights[key] == flight {
				delete(j.flights, key)
			}
			close(flight.done)
			j.cacheMu.Unlock()
			cancel()
		}()
	}
	flight.users++
	j.cacheMu.Unlock()
	defer func() {
		j.cacheMu.Lock()
		flight.users--
		if flight.users == 0 && !flight.started {
			flight.cancel()
			if j.flights[key] == flight {
				delete(j.flights, key)
			}
		}
		j.cacheMu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-flight.done:
		return flight.value, flight.err
	}
}

func journalWhere(kind int, f Filter) (string, []any) {
	clauses := []string{fmt.Sprintf("kind=%d", kind)}
	args := []any{}
	add := func(clause string, value any) {
		clauses = append(clauses, clause)
		args = append(args, value)
	}
	if !f.From.IsZero() {
		add("at>=?", f.From.UTC().Format(journalTimeFormat))
		if kind == 1 {
			// Redundant with the exact bound, but lets the planner seek the
			// leading day column of the covering index instead of scanning it.
			add("day>=?", f.From.UTC().Format("2006-01-02"))
		}
	}
	if !f.To.IsZero() {
		add("at<?", f.To.UTC().Format(journalTimeFormat))
		if kind == 1 {
			add("day<=?", f.To.UTC().Format("2006-01-02"))
		}
	}
	if kind == 1 || kind == 2 {
		for _, field := range []struct{ name, value string }{{"consumer", f.Consumer}, {"key_id", f.KeyID}} {
			if field.value != "" {
				add(field.name+"=?", field.value)
			}
		}
	}
	if kind == 1 {
		for _, field := range []struct{ name, value string }{{"owner", f.Owner}, {"channel_id", f.ChannelID}, {"credential_id", f.CredentialID}} {
			if field.value != "" {
				add(field.name+"=?", field.value)
			}
		}
		switch f.Outcome {
		case "failed":
			clauses = append(clauses, "failed=1")
		case "ok":
			clauses = append(clauses, "failed=0")
		}
		if f.Provider != "" {
			add("provider=?", f.Provider)
		}
		if f.Stream != "" {
			add("stream=?", f.Stream == "stream")
		}
		if low, high, ok := statusRange(f.Status); ok && f.Status != "" {
			clauses = append(clauses, "status BETWEEN ? AND ?")
			args = append(args, low, high)
		}
		if f.Endpoint != "" {
			add("endpoint=?", f.Endpoint)
		}
		if f.Effort != "" {
			add("effort=?", effortValue(f.Effort))
		}
		if f.Balanced != "" {
			add("balanced=?", f.Balanced == "yes")
		}
		if collapsesAttempts(f) {
			clauses = append(clauses, "superseded=0")
		}
		q := strings.ToLower(strings.TrimSpace(f.Model))
		if pin, ok := modelQueryPin(q); ok {
			clauses = append(clauses, "(serving_lower=? OR model_lower=?)")
			args = append(args, pin, pin)
		} else if q != "" {
			add("instr(model_lower,?)>0", q)
		}
	} else if kind == 2 && f.Model != "" {
		add("instr(model,?)>0", f.Model)
	} else if kind == 3 {
		if f.Actor != "" {
			add("actor=?", f.Actor)
		}
		if f.Action != "" {
			add("instr(action,?)>0", f.Action)
		}
	}
	return strings.Join(clauses, " AND "), args
}

// journalOrder maps a usage sort key to a stable ORDER BY clause; ties and all
// other kinds fall back to newest first.
func journalOrder(kind int, sort string) string {
	const newest = "at DESC, source, position"
	if kind != 1 {
		return newest
	}
	switch sort {
	case "tokens":
		return "total DESC, " + newest
	case "latency":
		return "latency DESC, " + newest
	default:
		return newest
	}
}

// UsageFacets lists the distinct values present under a filter, so dropdowns
// only offer choices that can match something.
type UsageFacets struct {
	Providers []string `json:"providers"`
	Endpoints []string `json:"endpoints"`
	Statuses  []int    `json:"statuses"`
	Efforts   []string `json:"efforts"`
}

func (s *Store) UsageFacetsContext(ctx context.Context, filter Filter) (UsageFacets, error) {
	j, err := s.queryJournal(ctx)
	if err != nil {
		return UsageFacets{}, err
	}
	filter.Page, filter.PageSize, filter.Sort = 0, 0, ""
	key, err := json.Marshal(filter)
	if err != nil {
		return UsageFacets{}, err
	}
	value, err := j.aggregate(ctx, "facets:"+string(key), func(ctx context.Context) (any, int, error) {
		where, args := journalWhere(1, filter)
		rows, errQuery := j.db.QueryContext(ctx, "SELECT DISTINCT provider, endpoint, status, effort FROM events WHERE "+where, args...)
		if errQuery != nil {
			return nil, 0, errQuery
		}
		defer func() { _ = rows.Close() }()
		seen := map[string]map[string]bool{"p": {}, "e": {}, "s": {}, "r": {}}
		out := UsageFacets{Providers: []string{}, Endpoints: []string{}, Statuses: []int{}, Efforts: []string{}}
		for rows.Next() {
			var provider, endpoint, effort string
			var status int
			if errScan := rows.Scan(&provider, &endpoint, &status, &effort); errScan != nil {
				return nil, 0, errScan
			}
			if provider != "" && !seen["p"][provider] {
				seen["p"][provider] = true
				out.Providers = append(out.Providers, provider)
			}
			if endpoint != "" && !seen["e"][endpoint] {
				seen["e"][endpoint] = true
				out.Endpoints = append(out.Endpoints, endpoint)
			}
			if code := strconv.Itoa(status); status > 0 && !seen["s"][code] {
				seen["s"][code] = true
				out.Statuses = append(out.Statuses, status)
			}
			if !seen["r"][effort] {
				seen["r"][effort] = true
				if effort == "" {
					effort = "none"
				}
				out.Efforts = append(out.Efforts, effort)
			}
		}
		sort.Strings(out.Providers)
		sort.Strings(out.Endpoints)
		sort.Ints(out.Statuses)
		sort.Strings(out.Efforts)
		return out, len(out.Providers) + len(out.Endpoints) + len(out.Statuses) + len(out.Efforts) + 1, rows.Err()
	})
	if err != nil {
		return UsageFacets{}, err
	}
	return value.(UsageFacets), nil
}

// collapsesAttempts reports whether earlier failed attempts of even-load-balanced
// requests are folded into their final attempt. Upstream-side filters (owner,
// channel, credential, pinned serving model) keep every attempt visible, since
// each one was a real call against that upstream.
func collapsesAttempts(f Filter) bool {
	if f.Owner != "" || f.ChannelID != "" || f.CredentialID != "" {
		return false
	}
	_, pinned := modelQueryPin(strings.ToLower(strings.TrimSpace(f.Model)))
	return !pinned
}

func journalPage[T any](ctx context.Context, j *journalIndex, kind int, filter Filter, event func(journalEntry) *T) ([]T, int, error) {
	where, args := journalWhere(kind, filter)
	// The total depends on the filter only, so paging through one result set
	// counts once and later pages reuse it. Unlike statistics it is never
	// served stale: any journal write invalidates it, so a list always includes
	// what was just recorded.
	countKey := filter
	countKey.Page, countKey.PageSize, countKey.Sort = 0, 0, ""
	rawKey, err := json.Marshal(countKey)
	if err != nil {
		return nil, 0, err
	}
	counted, err := j.aggregate(ctx, "count:"+strconv.Itoa(kind)+":"+string(rawKey), func(ctx context.Context) (any, int, error) {
		var total int
		errCount := j.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE "+where, args...).Scan(&total)
		return total, 1, errCount
	})
	if err != nil {
		return nil, 0, err
	}
	count := counted.(int)
	// The count waits for an aggregate slot while holding no connection; the
	// page transaction opens only afterwards so waiting requests can never
	// exhaust the connection pool that the running count needs.
	tx, err := j.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	start, end := pageBounds(filter, count)
	args = append(args, end-start, start)
	rows, err := tx.QueryContext(ctx, "SELECT source, position, length FROM events WHERE "+where+" ORDER BY "+journalOrder(kind, filter.Sort)+" LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	files := map[string]*os.File{}
	defer func() {
		for _, file := range files {
			_ = file.Close()
		}
	}()
	out := make([]T, 0, end-start)
	for rows.Next() {
		var source string
		var offset int64
		var length int
		if err = rows.Scan(&source, &offset, &length); err != nil {
			return nil, 0, err
		}
		if err = ctx.Err(); err != nil {
			return nil, 0, err
		}
		file := files[source]
		if file == nil {
			file, err = os.Open(filepath.Join(j.store.dir, "journal", source))
			if err != nil {
				return nil, 0, err
			}
			files[source] = file
		}
		data := make([]byte, length)
		if _, err = file.ReadAt(data, offset); err != nil && err != io.EOF {
			return nil, 0, err
		}
		var entry journalEntry
		if err = json.Unmarshal(data, &entry); err != nil {
			return nil, 0, fmt.Errorf("read indexed journal %s at byte %d: %w", source, offset, err)
		}
		item := event(entry)
		if item == nil {
			return nil, 0, fmt.Errorf("journal %s changed at byte %d", source, offset)
		}
		out = append(out, *item)
	}
	return out, count, rows.Err()
}

func (s *Store) UsageContext(ctx context.Context, filter Filter) ([]UsageEvent, int, error) {
	j, err := s.queryJournal(ctx)
	if err != nil {
		return nil, 0, err
	}
	items, total, err := journalPage(ctx, j, 1, filter, func(e journalEntry) *UsageEvent { return e.Usage })
	if err != nil || len(items) == 0 {
		return items, total, err
	}
	traces := []any{}
	for _, item := range items {
		if item.Balanced {
			traces = append(traces, usageTrace(item))
		}
	}
	if len(traces) == 0 {
		return items, total, nil
	}
	rows, err := j.db.QueryContext(ctx, "SELECT trace, COUNT(*) FROM events INDEXED BY usage_trace WHERE kind=1 AND trace IN (?"+strings.Repeat(",?", len(traces)-1)+") GROUP BY trace", traces...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	counts := map[string]int{}
	for rows.Next() {
		var trace string
		var count int
		if err = rows.Scan(&trace, &count); err != nil {
			return nil, 0, err
		}
		counts[trace] = count
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	for i := range items {
		if items[i].Balanced {
			items[i].Attempts = counts[usageTrace(items[i])]
		}
	}
	return items, total, nil
}

// UsageAttemptsContext returns every upstream attempt of one client request,
// oldest first, so a balanced call can show its full failover chain.
func (s *Store) UsageAttemptsContext(ctx context.Context, trace string) ([]UsageEvent, error) {
	j, err := s.queryJournal(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := j.db.QueryContext(ctx, "SELECT source, position, length FROM events INDEXED BY usage_trace WHERE kind=1 AND trace=? ORDER BY at, source, position LIMIT 200", trace)
	if err != nil {
		return nil, err
	}
	type location struct {
		source string
		offset int64
		length int
	}
	locations := []location{}
	for rows.Next() {
		var loc location
		if err = rows.Scan(&loc.source, &loc.offset, &loc.length); err != nil {
			_ = rows.Close()
			return nil, err
		}
		locations = append(locations, loc)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]UsageEvent, 0, len(locations))
	for _, loc := range locations {
		entry, errRead := readJournalEntry(j.store.dir, loc.source, loc.offset, loc.length)
		if errRead != nil {
			return nil, errRead
		}
		if entry.Usage != nil {
			out = append(out, *entry.Usage)
		}
	}
	return out, nil
}

func readJournalEntry(dir, source string, offset int64, length int) (journalEntry, error) {
	var entry journalEntry
	file, err := os.Open(filepath.Join(dir, "journal", source))
	if err != nil {
		return entry, err
	}
	defer func() { _ = file.Close() }()
	data := make([]byte, length)
	if _, err = file.ReadAt(data, offset); err != nil && err != io.EOF {
		return entry, err
	}
	if err = json.Unmarshal(data, &entry); err != nil {
		return entry, fmt.Errorf("read indexed journal %s at byte %d: %w", source, offset, err)
	}
	return entry, nil
}

func (s *Store) CallsContext(ctx context.Context, filter Filter) ([]CallEvent, int, error) {
	j, err := s.queryJournal(ctx)
	if err != nil {
		return nil, 0, err
	}
	return journalPage(ctx, j, 2, filter, func(e journalEntry) *CallEvent { return e.Call })
}

func (s *Store) AuditsContext(ctx context.Context, filter Filter) ([]AuditEvent, int, error) {
	j, err := s.queryJournal(ctx)
	if err != nil {
		return nil, 0, err
	}
	return journalPage(ctx, j, 3, filter, func(e journalEntry) *AuditEvent { return e.Audit })
}

const summaryColumns = `COUNT(DISTINCT trace), COUNT(*), COALESCE(SUM(failed),0),
 COALESCE(SUM(input),0), COALESCE(SUM(output),0), COALESCE(SUM(reasoning),0),
 COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_create),0), COALESCE(SUM(total),0), COALESCE(SUM(1-known),0),
 COALESCE(SUM(CASE WHEN failed=0 AND latency>0 THEN latency ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN failed=0 AND latency>0 THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN failed=0 AND ttft>0 THEN ttft ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN failed=0 AND ttft>0 THEN 1 ELSE 0 END),0)`

func summaryDest(sum *Summary) []any {
	return []any{&sum.Calls, &sum.Executions, &sum.Failures, &sum.InputTokens, &sum.OutputTokens, &sum.ReasoningTokens, &sum.CacheReadTokens, &sum.CacheCreationTokens, &sum.TotalTokens, &sum.UnknownTokenRecords, &sum.LatencyMS, &sum.LatencyCount, &sum.TTFTMS, &sum.TTFTCount}
}

// SummaryResult is one summary query: the overall total, per day/consumer/
// owner/channel/model groups, and hourly trend buckets.
type SummaryResult struct {
	Total   Summary
	Groups  []Summary
	Buckets []SummaryBucket
}

func (s *Store) SummaryContext(ctx context.Context, filter Filter) (Summary, []Summary, error) {
	result, err := s.SummaryResultContext(ctx, filter)
	return result.Total, result.Groups, err
}

func (s *Store) SummaryResultContext(ctx context.Context, filter Filter) (SummaryResult, error) {
	j, err := s.queryJournal(ctx)
	if err != nil {
		return SummaryResult{}, err
	}
	filter.Page, filter.PageSize, filter.Sort = 0, 0, ""
	key, err := json.Marshal(filter)
	if err != nil {
		return SummaryResult{}, err
	}
	value, err := j.aggregate(ctx, "summary:"+string(key), func(ctx context.Context) (any, int, error) {
		result := SummaryResult{Groups: []Summary{}, Buckets: []SummaryBucket{}}
		where, args := journalWhere(1, filter)
		// The covering index avoids a random table read per event, even when
		// an account or time filter happens to match nearly the whole history.
		from := coverIndex
		tx, errBegin := j.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if errBegin != nil {
			return nil, 0, errBegin
		}
		defer func() { _ = tx.Rollback() }()
		traceFrom := from
		if where == "kind=1" {
			traceFrom = "events INDEXED BY usage_trace"
		} else if filter.Consumer != "" && filter == (Filter{Consumer: filter.Consumer}) {
			traceFrom = "events INDEXED BY usage_consumer_trace"
		} else if filter.Owner != "" && filter == (Filter{Owner: filter.Owner}) {
			traceFrom = "events INDEXED BY usage_owner_trace"
		}
		if errTotal := tx.QueryRowContext(ctx, "SELECT COUNT(DISTINCT trace) FROM "+traceFrom+" WHERE "+where, args...).Scan(&result.Total.Calls); errTotal != nil {
			return nil, 0, errTotal
		}
		if collapsesAttempts(filter) {
			// The superseded clause carries no argument, so swapping it keeps args aligned.
			folded := strings.Replace(where, "superseded=0", "superseded=1", 1)
			if errFolded := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+from+" WHERE "+folded, args...).Scan(&result.Total.Failovers); errFolded != nil {
				return nil, 0, errFolded
			}
		}
		rows, errQuery := tx.QueryContext(ctx, "SELECT day, consumer, owner, channel, model, credential_id, "+summaryColumns+" FROM "+from+" WHERE "+where+" GROUP BY day,consumer,owner,channel,model,credential_id ORDER BY day DESC,channel||consumer||model", args...)
		if errQuery != nil {
			return nil, 0, errQuery
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var group Summary
			dest := append([]any{&group.Day, &group.Consumer, &group.Owner, &group.Channel, &group.Model, &group.CredentialID}, summaryDest(&group)...)
			if errScan := rows.Scan(dest...); errScan != nil {
				return nil, 0, errScan
			}
			result.Groups = append(result.Groups, group)
			t := &result.Total
			t.Executions += group.Executions
			t.Failures += group.Failures
			t.InputTokens += group.InputTokens
			t.OutputTokens += group.OutputTokens
			t.ReasoningTokens += group.ReasoningTokens
			t.CacheReadTokens += group.CacheReadTokens
			t.CacheCreationTokens += group.CacheCreationTokens
			t.TotalTokens += group.TotalTokens
			t.UnknownTokenRecords += group.UnknownTokenRecords
			t.LatencyMS += group.LatencyMS
			t.LatencyCount += group.LatencyCount
			t.TTFTMS += group.TTFTMS
			t.TTFTCount += group.TTFTCount
		}
		if errRows := rows.Err(); errRows != nil {
			return nil, 0, errRows
		}
		_ = rows.Close()
		// Hourly buckets; the client folds them into days for longer ranges.
		buckets, errBuckets := tx.QueryContext(ctx, "SELECT hour, COUNT(DISTINCT trace), COUNT(*), COALESCE(SUM(failed),0), COALESCE(SUM(total),0), COALESCE(SUM(input),0), COALESCE(SUM(output),0) FROM "+from+" WHERE "+where+" GROUP BY hour ORDER BY hour", args...)
		if errBuckets != nil {
			return nil, 0, errBuckets
		}
		defer func() { _ = buckets.Close() }()
		for buckets.Next() {
			var b SummaryBucket
			if errScan := buckets.Scan(&b.Start, &b.Calls, &b.Executions, &b.Failures, &b.TotalTokens, &b.InputTokens, &b.OutputTokens); errScan != nil {
				return nil, 0, errScan
			}
			result.Buckets = append(result.Buckets, b)
		}
		return result, len(result.Groups) + len(result.Buckets) + 1, buckets.Err()
	})
	if err != nil {
		return SummaryResult{}, err
	}
	result := value.(SummaryResult)
	return SummaryResult{
		Total:   result.Total,
		Groups:  append([]Summary{}, result.Groups...),
		Buckets: append([]SummaryBucket{}, result.Buckets...),
	}, nil
}

// overviewRankLimit caps each ranking tab so the payload stays small on large
// histories; the full breakdown remains reachable through the same filters.
const overviewRankLimit = 50

// OverviewRankRow is one ranked breakdown row. Label is the display value
// (model ID, username or credential prefix).
type OverviewRankRow struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Value Summary `json:"value"`
}

// UsageOverview is the bounded payload driving the usage page: totals,
// adaptive trend buckets, per-dimension top rows, model candidates and facet
// values. It replaces three unbounded requests the page used to make.
type UsageOverview struct {
	Total   Summary                      `json:"total"`
	Buckets []SummaryBucket              `json:"buckets"`
	Rank    map[string][]OverviewRankRow `json:"rank"`
	Models  []string                     `json:"models"`
	Facets  UsageFacets                  `json:"facets"`
}

// overviewGroupColumns mirrors summaryColumns minus the leading trace count;
// overviewRowDest mirrors summaryDest minus Calls.
const overviewGroupColumns = `COUNT(*), COALESCE(SUM(failed),0),
 COALESCE(SUM(input),0), COALESCE(SUM(output),0), COALESCE(SUM(reasoning),0),
 COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_create),0), COALESCE(SUM(total),0), COALESCE(SUM(1-known),0),
 COALESCE(SUM(CASE WHEN failed=0 AND latency>0 THEN latency ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN failed=0 AND latency>0 THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN failed=0 AND ttft>0 THEN ttft ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN failed=0 AND ttft>0 THEN 1 ELSE 0 END),0)`

func (s *Store) ModelPerformanceContext(ctx context.Context, now time.Time) (map[string]*ModelPerformance, error) {
	j, err := s.queryJournal(ctx)
	if err != nil {
		return nil, err
	}
	end := now.UTC().Truncate(time.Hour).Add(time.Hour)
	start := end.Add(-performanceBucketCount * time.Hour)
	value, err := j.aggregate(ctx, "performance:"+end.Format(journalTimeFormat), func(ctx context.Context) (any, int, error) {
		rows, errQuery := j.db.QueryContext(ctx, `SELECT model, hour, COUNT(*), SUM(failed),
 COALESCE(SUM(CASE WHEN failed=0 AND latency>0 THEN latency ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN failed=0 AND latency>0 THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN failed=0 AND ttft>0 THEN ttft ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN failed=0 AND ttft>0 THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN failed=0 AND latency>0 THEN output ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN failed=0 AND latency>0 THEN latency-CASE WHEN ttft>0 AND ttft<latency THEN ttft ELSE 0 END ELSE 0 END),0)
 FROM events INDEXED BY usage_performance WHERE kind=1 AND hour>=? AND hour<? AND model<>'' GROUP BY hour,model`, start.Unix(), end.Unix())
		if errQuery != nil {
			return nil, 0, errQuery
		}
		defer func() { _ = rows.Close() }()
		out := map[string]*ModelPerformance{}
		for rows.Next() {
			var model string
			var bucket PerformanceBucket
			if errScan := rows.Scan(&model, &bucket.Start, &bucket.Requests, &bucket.Failures, &bucket.LatencyMS, &bucket.LatencyCount, &bucket.TTFTMS, &bucket.TTFTCount, &bucket.OutputTokens, &bucket.GenerationMS); errScan != nil {
				return nil, 0, errScan
			}
			perf := out[model]
			if perf == nil {
				perf = &ModelPerformance{Model: model, Buckets: make([]PerformanceBucket, performanceBucketCount)}
				for i := range perf.Buckets {
					perf.Buckets[i].Start = start.Add(time.Duration(i) * time.Hour).Unix()
				}
				out[model] = perf
			}
			index := int((bucket.Start - start.Unix()) / 3600)
			if index >= 0 && index < performanceBucketCount {
				perf.Buckets[index] = bucket
			}
			perf.Totals.Requests += bucket.Requests
			perf.Totals.Failures += bucket.Failures
			perf.Totals.LatencyMS += bucket.LatencyMS
			perf.Totals.LatencyCount += bucket.LatencyCount
			perf.Totals.TTFTMS += bucket.TTFTMS
			perf.Totals.TTFTCount += bucket.TTFTCount
			perf.Totals.OutputTokens += bucket.OutputTokens
			perf.Totals.GenerationMS += bucket.GenerationMS
		}
		return out, len(out) * (performanceBucketCount + 1), rows.Err()
	})
	if err != nil {
		return nil, err
	}
	out := map[string]*ModelPerformance{}
	for model, perf := range value.(map[string]*ModelPerformance) {
		clone := *perf
		clone.Buckets = append([]PerformanceBucket{}, perf.Buckets...)
		out[model] = &clone
	}
	return out, nil
}
