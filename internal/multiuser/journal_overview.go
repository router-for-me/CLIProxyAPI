package multiuser

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// overviewModelLimit keeps huge upstream catalogs (10k+ model IDs) from
// becoming a response payload or an unbounded dropdown; the overview keeps the
// most-used IDs and a rare ID still resolves through the list's substring match.
const overviewModelLimit = 512

// overviewGroup is one SQL group of the single overview scan. The grouping
// dimensions follow the covering index column order so SQLite streams the
// groups without a temp b-tree.
type overviewGroup struct {
	day, consumer, owner, channel, model, credentialID string
	provider, endpoint, effort                         string
	status                                             int64
	stream, balanced, failed, superseded               bool
	hour                                               int64
	sum                                                Summary
}

func (g *overviewGroup) matchesAttrs(f Filter, skip string) bool {
	if skip != "provider" && f.Provider != "" && g.provider != f.Provider {
		return false
	}
	if skip != "stream" && f.Stream != "" && g.stream != (f.Stream == "stream") {
		return false
	}
	if skip != "status" && f.Status != "" {
		low, high, ok := statusRange(f.Status)
		if !ok || g.status < int64(low) || g.status > int64(high) {
			return false
		}
	}
	if skip != "endpoint" && f.Endpoint != "" && g.endpoint != f.Endpoint {
		return false
	}
	if skip != "effort" && f.Effort != "" && g.effort != effortValue(f.Effort) {
		return false
	}
	if skip != "balanced" && f.Balanced != "" && g.balanced != (f.Balanced == "yes") {
		return false
	}
	return true
}

func (s *Store) OverviewContext(ctx context.Context, filter Filter) (UsageOverview, error) {
	j, err := s.queryJournal(ctx)
	if err != nil {
		return UsageOverview{}, err
	}
	filter.Page, filter.PageSize, filter.Sort = 0, 0, ""
	key, err := json.Marshal(filter)
	if err != nil {
		return UsageOverview{}, err
	}
	value, err := j.aggregate(ctx, "overview:"+string(key), func(ctx context.Context) (any, int, error) {
		return j.computeOverview(ctx, filter)
	})
	if err != nil {
		return UsageOverview{}, err
	}
	return value.(UsageOverview), nil
}

// computeOverview reads the covering index once, grouped, and derives every
// statistic from those groups. SQL narrows by time, identity, resource and
// outcome only; the model match and the six attribute predicates run on the
// groups so a single pass yields totals, ranks, candidates and facets that can
// each ignore their own applied filter.
func (j *journalIndex) computeOverview(ctx context.Context, filter Filter) (any, int, error) {
	result := UsageOverview{
		Buckets: []SummaryBucket{},
		Rank:    map[string][]OverviewRankRow{"model": {}, "consumer": {}, "credential": {}},
		Models:  []string{},
		Facets:  UsageFacets{Providers: []string{}, Endpoints: []string{}, Statuses: []int{}, Efforts: []string{}},
	}
	sqlFilter := filter
	sqlFilter.Model, sqlFilter.Provider, sqlFilter.Stream, sqlFilter.Status = "", "", "", ""
	sqlFilter.Endpoint, sqlFilter.Effort, sqlFilter.Balanced = "", "", ""
	where, args := journalWhere(1, sqlFilter)
	// Stats must still see superseded rows: they count as failovers, not as
	// executions.
	where = strings.Replace(where, "superseded=0", "1=1", 1)
	hourly := !filter.From.IsZero() && !filter.To.IsZero() && filter.To.Sub(filter.From) <= 48*time.Hour
	groupBy := "day,consumer,owner,channel,model,credential_id,provider,status,stream,endpoint,effort,balanced,failed,superseded"
	hourColumn := "0"
	if hourly {
		groupBy += ",hour"
		hourColumn = "hour"
	}
	rows, err := j.db.QueryContext(ctx, "SELECT day,consumer,owner,channel,model,credential_id,provider,status,stream,endpoint,effort,balanced,failed,superseded,"+
		hourColumn+","+overviewGroupColumns+" FROM "+coverIndex+" WHERE "+where+" GROUP BY "+groupBy, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	buckets := map[int64]*SummaryBucket{}
	ranks := map[string]map[string]*Summary{"model": {}, "consumer": {}, "credential": {}}
	rankLabel := map[string]map[string]string{"model": {}, "consumer": {}, "credential": {}}
	dayStart := map[string]int64{}
	models := map[string]bool{}
	modelCalls := map[string]int64{}
	providers := map[string]bool{}
	endpoints := map[string]bool{}
	statuses := map[int]bool{}
	efforts := map[string]bool{}

	matchModel := func(g *overviewGroup) bool {
		return filter.Model == "" || matchModelQuery(UsageEvent{Model: g.model, Channel: g.channel}, filter.Model)
	}
	addRank := func(dimension, key, label string, g *overviewGroup) {
		if key == "" {
			key = label
		}
		sum := ranks[dimension][key]
		if sum == nil {
			sum = &Summary{}
			ranks[dimension][key] = sum
			rankLabel[dimension][key] = label
		}
		addSummary(sum, &g.sum)
	}

	var g overviewGroup
	for rows.Next() {
		g = overviewGroup{}
		dest := append([]any{&g.day, &g.consumer, &g.owner, &g.channel, &g.model, &g.credentialID, &g.provider, &g.status, &g.stream,
			&g.endpoint, &g.effort, &g.balanced, &g.failed, &g.superseded, &g.hour}, overviewRowDest(&g.sum)...)
		if err = rows.Scan(dest...); err != nil {
			return nil, 0, err
		}
		count := g.sum.Executions
		attrsOK := g.matchesAttrs(filter, "")
		if attrsOK && matchModel(&g) {
			if g.superseded {
				result.Total.Failovers += count
			} else {
				addSummary(&result.Total, &g.sum)
				addRank("model", g.model, g.model, &g)
				addRank("consumer", g.consumer, g.consumer, &g)
				addRank("credential", g.credentialID, g.channel, &g)
				start := g.hour
				if !hourly {
					var ok bool
					if start, ok = dayStart[g.day]; !ok {
						dayTime, _ := time.Parse(journalTimeFormat, g.day+"T00:00:00.000000000Z")
						start = dayTime.Unix()
						dayStart[g.day] = start
					}
				}
				b := buckets[start]
				if b == nil {
					b = &SummaryBucket{Start: start}
					buckets[start] = b
				}
				b.Calls += count
				b.Executions = b.Calls
				b.Failures += g.sum.Failures
				b.TotalTokens += g.sum.TotalTokens
				b.InputTokens += g.sum.InputTokens
				b.OutputTokens += g.sum.OutputTokens
			}
		}
		if attrsOK && g.model != "" {
			models[g.model] = true
			modelCalls[g.model] += count
			if g.channel != "" && !strings.HasPrefix(g.model, g.channel+"/") {
				serving := g.channel + "/" + g.model
				models[serving] = true
				modelCalls[serving] += count
			}
		}
		if g.provider != "" && g.matchesAttrs(filter, "provider") {
			providers[g.provider] = true
		}
		if g.endpoint != "" && g.matchesAttrs(filter, "endpoint") {
			endpoints[g.endpoint] = true
		}
		if g.status > 0 && g.matchesAttrs(filter, "status") {
			statuses[int(g.status)] = true
		}
		if g.matchesAttrs(filter, "effort") {
			efforts[g.effort] = true
		}
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	_ = rows.Close()

	// Distinct requests need the raw trace column, so they take their own
	// pass with the full filter applied in SQL.
	fullWhere, fullArgs := journalWhere(1, filter)
	if !strings.Contains(fullWhere, "superseded=0") {
		fullWhere += " AND superseded=0"
	}
	if err = j.db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT trace) FROM "+coverIndex+" WHERE "+fullWhere, fullArgs...).Scan(&result.Total.Calls); err != nil {
		return nil, 0, err
	}

	starts := make([]int64, 0, len(buckets))
	for start := range buckets {
		starts = append(starts, start)
	}
	sort.Slice(starts, func(i, k int) bool { return starts[i] < starts[k] })
	for _, start := range starts {
		result.Buckets = append(result.Buckets, *buckets[start])
	}
	for dimension, byKey := range ranks {
		list := make([]OverviewRankRow, 0, len(byKey))
		for key, sum := range byKey {
			list = append(list, OverviewRankRow{Key: key, Label: rankLabel[dimension][key], Value: *sum})
		}
		sort.Slice(list, func(i, k int) bool {
			if list[i].Value.TotalTokens != list[k].Value.TotalTokens {
				return list[i].Value.TotalTokens > list[k].Value.TotalTokens
			}
			if list[i].Value.Executions != list[k].Value.Executions {
				return list[i].Value.Executions > list[k].Value.Executions
			}
			if list[i].Label != list[k].Label {
				return list[i].Label < list[k].Label
			}
			return list[i].Key < list[k].Key
		})
		if len(list) > overviewRankLimit {
			list = list[:overviewRankLimit]
		}
		result.Rank[dimension] = list
	}
	for m := range models {
		result.Models = append(result.Models, m)
	}
	sort.Slice(result.Models, func(i, k int) bool {
		if modelCalls[result.Models[i]] != modelCalls[result.Models[k]] {
			return modelCalls[result.Models[i]] > modelCalls[result.Models[k]]
		}
		return result.Models[i] < result.Models[k]
	})
	if len(result.Models) > overviewModelLimit {
		result.Models = result.Models[:overviewModelLimit]
	}
	for v := range providers {
		result.Facets.Providers = append(result.Facets.Providers, v)
	}
	sort.Strings(result.Facets.Providers)
	for v := range endpoints {
		result.Facets.Endpoints = append(result.Facets.Endpoints, v)
	}
	sort.Strings(result.Facets.Endpoints)
	for v := range statuses {
		result.Facets.Statuses = append(result.Facets.Statuses, v)
	}
	sort.Ints(result.Facets.Statuses)
	for v := range efforts {
		if v == "" {
			v = "none"
		}
		result.Facets.Efforts = append(result.Facets.Efforts, v)
	}
	sort.Strings(result.Facets.Efforts)

	weight := 1 + len(result.Buckets) + len(result.Models)
	for _, list := range result.Rank {
		weight += len(list)
	}
	weight += len(result.Facets.Providers) + len(result.Facets.Endpoints) + len(result.Facets.Statuses) + len(result.Facets.Efforts)
	return result, weight, nil
}

// addSummary folds one group's counters into dst; Calls and Failovers are
// handled by the callers because they are not additive across groups.
func addSummary(dst, src *Summary) {
	dst.Executions += src.Executions
	dst.Failures += src.Failures
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.ReasoningTokens += src.ReasoningTokens
	dst.CacheReadTokens += src.CacheReadTokens
	dst.CacheCreationTokens += src.CacheCreationTokens
	dst.TotalTokens += src.TotalTokens
	dst.UnknownTokenRecords += src.UnknownTokenRecords
	dst.LatencyMS += src.LatencyMS
	dst.LatencyCount += src.LatencyCount
	dst.TTFTMS += src.TTFTMS
	dst.TTFTCount += src.TTFTCount
}

func overviewRowDest(sum *Summary) []any {
	return summaryDest(sum)[1:]
}
