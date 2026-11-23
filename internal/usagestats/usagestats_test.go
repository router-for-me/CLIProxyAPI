package usagestats

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, errOpen := Open(filepath.Join(t.TempDir(), DefaultFileName))
	if errOpen != nil {
		t.Fatalf("open store: %v", errOpen)
	}
	t.Cleanup(func() {
		if errClose := store.Close(); errClose != nil {
			t.Errorf("close store: %v", errClose)
		}
	})
	return store
}

func mustInsert(t *testing.T, store *Store, rows ...Row) {
	t.Helper()
	for _, row := range rows {
		if errInsert := store.Insert(context.Background(), row); errInsert != nil {
			t.Fatalf("insert row: %v", errInsert)
		}
	}
}

func groupByKey(t *testing.T, aggregate Aggregate, dimension string) map[string]Group {
	t.Helper()
	out := make(map[string]Group, len(aggregate.Groups))
	for _, group := range aggregate.Groups {
		out[group.Dimensions[dimension]] = group
	}
	return out
}

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

var testDay = time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)

func TestAggregatePricesTokenBucketsIndependently(t *testing.T) {
	store := openTestStore(t)
	mustInsert(t, store, Row{
		Timestamp:          testDay.Add(time.Hour),
		Model:              "claude-opus-5-5",
		InputTokens:        1_000_000,
		CacheReadTokens:    2_000_000,
		CacheWriteTokens:   3_000_000,
		CacheWrite5mTokens: 1_000_000,
		CacheWrite1hTokens: 2_000_000,
		OutputTokens:       400_000,
		ReasoningTokens:    100_000,
		TotalTokens:        6_500_000,
	})

	aggregate, errAggregate := store.Aggregate(context.Background(), Query{
		From: testDay, To: testDay.Add(24 * time.Hour), GroupBy: []string{"model"},
	})
	if errAggregate != nil {
		t.Fatalf("aggregate: %v", errAggregate)
	}
	group := groupByKey(t, aggregate, "model")["claude-opus-5-5"]

	// Built-in prices per 1M: input 4, cache read 0.2, 5m write 5, 1h write 8, output 20.
	want := 1*4.0 + 2*0.2 + 1*5.0 + 2*8.0 + 0.5*20.0
	if !approxEqual(group.CostUSD, want) {
		t.Fatalf("cost = %v, want %v", group.CostUSD, want)
	}
	// The split covers every bucket once: reasoning is billed as output.
	if !approxEqual(group.InputCostUSD, 4) || !approxEqual(group.CacheReadCostUSD, 0.4) ||
		!approxEqual(group.CacheWriteCostUSD, 21) || !approxEqual(group.OutputCostUSD, 10) {
		t.Fatalf("cost split = input %v, cache read %v, cache write %v, output %v, want 4, 0.4, 21, 10",
			group.InputCostUSD, group.CacheReadCostUSD, group.CacheWriteCostUSD, group.OutputCostUSD)
	}
	if group.UnpricedTokens != 0 || len(aggregate.UnpricedModels) != 0 {
		t.Fatalf("unpriced tokens = %d, models = %v, want none", group.UnpricedTokens, aggregate.UnpricedModels)
	}
	if group.CacheWriteTokens != 3_000_000 || group.CacheWrite5mTokens != 1_000_000 || group.CacheWrite1hTokens != 2_000_000 {
		t.Fatalf("cache writes = %d (5m %d, 1h %d), want 3M split 1M and 2M",
			group.CacheWriteTokens, group.CacheWrite5mTokens, group.CacheWrite1hTokens)
	}
}

func TestAggregateFoldsModelsIntoRequestedGroups(t *testing.T) {
	store := openTestStore(t)
	mustInsert(t, store,
		Row{Timestamp: testDay, Model: "claude-opus-5-5", Project: "alpha", InputTokens: 1_000_000, TotalTokens: 1_000_000},
		Row{Timestamp: testDay, Model: "claude-haiku-5-5", Project: "alpha", InputTokens: 1_000_000, TotalTokens: 1_000_000, Failed: true},
		Row{Timestamp: testDay, Model: "claude-opus-5-5", Project: "beta", InputTokens: 2_000_000, TotalTokens: 2_000_000},
	)

	aggregate, errAggregate := store.Aggregate(context.Background(), Query{
		From: testDay, To: testDay.Add(time.Hour), GroupBy: []string{"project"},
	})
	if errAggregate != nil {
		t.Fatalf("aggregate: %v", errAggregate)
	}
	groups := groupByKey(t, aggregate, "project")
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	alpha := groups["alpha"]
	if alpha.Requests != 2 || alpha.Failed != 1 || alpha.TotalTokens != 2_000_000 {
		t.Fatalf("alpha = %+v", alpha.Metrics)
	}
	// Opus input 4 + Haiku input 0.1 per 1M: cost must be summed per model, not averaged.
	if !approxEqual(alpha.CostUSD, 4.1) {
		t.Fatalf("alpha cost = %v, want 4.1", alpha.CostUSD)
	}
	if !approxEqual(groups["beta"].CostUSD, 8) {
		t.Fatalf("beta cost = %v, want 8", groups["beta"].CostUSD)
	}
	if !approxEqual(alpha.InputCostUSD, 4.1) || alpha.OutputCostUSD != 0 {
		t.Fatalf("alpha cost split = input %v, output %v, want 4.1 and 0", alpha.InputCostUSD, alpha.OutputCostUSD)
	}
}

func TestAggregateCostPartsAddUpAcrossFoldedModels(t *testing.T) {
	store := openTestStore(t)
	mustInsert(t, store,
		Row{
			Timestamp: testDay, Model: "claude-opus-5-5",
			InputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 2_000_000,
			CacheWrite5mTokens: 1_000_000, CacheWrite1hTokens: 1_000_000,
			OutputTokens: 500_000, ReasoningTokens: 500_000, TotalTokens: 6_000_000,
		},
		// Codex reports cache writes without a lifetime; they are billed at the 5m rate.
		Row{
			Timestamp: testDay, Model: "gpt-6.2-sol",
			InputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 1_000_000,
			OutputTokens: 1_000_000, TotalTokens: 4_000_000,
		},
	)

	aggregate, errAggregate := store.Aggregate(context.Background(), Query{
		From: testDay, To: testDay.Add(time.Hour), GroupBy: []string{},
	})
	if errAggregate != nil {
		t.Fatalf("aggregate: %v", errAggregate)
	}
	total := aggregate.Groups[0]
	// Opus per 1M: input 4, cache read 0.2, 5m write 5, 1h write 8, output 20.
	// Sol per 1M: input 2, cache read 0.1, 5m write 2.5, output 10.
	want := map[string][2]float64{
		"input":       {total.InputCostUSD, 4 + 2},
		"cache read":  {total.CacheReadCostUSD, 0.2 + 0.1},
		"cache write": {total.CacheWriteCostUSD, 5 + 8 + 2.5},
		"output":      {total.OutputCostUSD, 20 + 10},
	}
	for name, pair := range want {
		if !approxEqual(pair[0], pair[1]) {
			t.Errorf("%s cost = %v, want %v", name, pair[0], pair[1])
		}
	}
	parts := total.InputCostUSD + total.CacheReadCostUSD + total.CacheWriteCostUSD + total.OutputCostUSD
	if !approxEqual(parts, total.CostUSD) || !approxEqual(total.CostUSD, 51.8) {
		t.Fatalf("cost = %v, parts add up to %v, want both 51.8", total.CostUSD, parts)
	}
}

func TestAggregateLeavesTheCostSplitOfUnpricedModelsEmpty(t *testing.T) {
	store := openTestStore(t)
	mustInsert(t, store,
		Row{Timestamp: testDay, Model: "claude-opus-5-5", InputTokens: 1_000_000, OutputTokens: 1_000_000, TotalTokens: 2_000_000},
		Row{Timestamp: testDay, Model: "no-such-model", InputTokens: 1_000_000, OutputTokens: 1_000_000, TotalTokens: 2_000_000},
	)

	aggregate, errAggregate := store.Aggregate(context.Background(), Query{
		From: testDay, To: testDay.Add(time.Hour), GroupBy: []string{},
	})
	if errAggregate != nil {
		t.Fatalf("aggregate: %v", errAggregate)
	}
	if len(aggregate.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(aggregate.Groups))
	}
	total := aggregate.Groups[0]
	// Only Opus is priced: input 4 and output 20 per 1M.
	if !approxEqual(total.InputCostUSD, 4) || !approxEqual(total.OutputCostUSD, 20) || !approxEqual(total.CostUSD, 24) {
		t.Fatalf("cost = %v split input %v output %v, want 24 split 4 and 20", total.CostUSD, total.InputCostUSD, total.OutputCostUSD)
	}
	if total.UnpricedTokens != 2_000_000 {
		t.Fatalf("unpriced tokens = %d, want 2M", total.UnpricedTokens)
	}
}

func TestAggregateBucketsDaysInTheViewersOffset(t *testing.T) {
	store := openTestStore(t)
	// 02:00 UTC on the 12th is still the 11th in UTC-8.
	mustInsert(t, store, Row{Timestamp: testDay.Add(2 * time.Hour), Model: "m", TotalTokens: 10})

	query := Query{From: testDay.Add(-24 * time.Hour), To: testDay.Add(24 * time.Hour), GroupBy: []string{"day"}}
	utc, errUTC := store.Aggregate(context.Background(), query)
	if errUTC != nil {
		t.Fatalf("aggregate utc: %v", errUTC)
	}
	if _, ok := groupByKey(t, utc, "day")["2026-11-12"]; !ok {
		t.Fatalf("utc groups = %+v, want 2026-11-12", utc.Groups)
	}

	query.UTCOffset = -8 * time.Hour
	pacific, errPacific := store.Aggregate(context.Background(), query)
	if errPacific != nil {
		t.Fatalf("aggregate pacific: %v", errPacific)
	}
	if _, ok := groupByKey(t, pacific, "day")["2026-11-11"]; !ok {
		t.Fatalf("pacific groups = %+v, want 2026-11-11", pacific.Groups)
	}
}

func TestAggregateReportsUnpricedModelsAndRespectsRangeAndFilters(t *testing.T) {
	store := openTestStore(t)
	mustInsert(t, store,
		Row{Timestamp: testDay, Model: "mystery-model", Provider: "p1", TotalTokens: 500, InputTokens: 500},
		Row{Timestamp: testDay, Model: "claude-opus-5-5", Provider: "p2", TotalTokens: 100, InputTokens: 90, UnclassifiedTokens: 10},
		Row{Timestamp: testDay.Add(48 * time.Hour), Model: "claude-opus-5-5", Provider: "p2", TotalTokens: 999},
	)
	query := Query{From: testDay, To: testDay.Add(time.Hour)}

	all, errAll := store.Aggregate(context.Background(), query)
	if errAll != nil {
		t.Fatalf("aggregate: %v", errAll)
	}
	if len(all.Groups) != 1 || all.Groups[0].TotalTokens != 600 {
		t.Fatalf("groups = %+v, want one group of 600 tokens", all.Groups)
	}
	if all.Groups[0].UnpricedTokens != 510 {
		t.Fatalf("unpriced tokens = %d, want 510", all.Groups[0].UnpricedTokens)
	}
	if len(all.UnpricedModels) != 1 || all.UnpricedModels[0] != "mystery-model" {
		t.Fatalf("unpriced models = %v", all.UnpricedModels)
	}

	query.Filters = map[string]string{"provider": "p2"}
	filtered, errFiltered := store.Aggregate(context.Background(), query)
	if errFiltered != nil {
		t.Fatalf("aggregate filtered: %v", errFiltered)
	}
	if len(filtered.Groups) != 1 || filtered.Groups[0].TotalTokens != 100 {
		t.Fatalf("filtered groups = %+v, want one group of 100 tokens", filtered.Groups)
	}
}

func TestAggregateTruncatesToLargestGroups(t *testing.T) {
	store := openTestStore(t)
	mustInsert(t, store,
		Row{Timestamp: testDay, Model: "m", Project: "small", TotalTokens: 1},
		Row{Timestamp: testDay, Model: "m", Project: "large", TotalTokens: 100},
	)
	aggregate, errAggregate := store.Aggregate(context.Background(), Query{
		From: testDay, To: testDay.Add(time.Hour), GroupBy: []string{"project"}, Limit: 1,
	})
	if errAggregate != nil {
		t.Fatalf("aggregate: %v", errAggregate)
	}
	if !aggregate.Truncated || len(aggregate.Groups) != 1 || aggregate.Groups[0].Dimensions["project"] != "large" {
		t.Fatalf("aggregate = %+v, want only the large group and truncated", aggregate)
	}
}

func TestQueryRejectsMalformedInput(t *testing.T) {
	store := openTestStore(t)
	valid := Query{From: testDay, To: testDay.Add(time.Hour)}
	cases := map[string]Query{
		"unknown dimension": {From: valid.From, To: valid.To, GroupBy: []string{"model; DROP TABLE requests"}},
		"unknown filter":    {From: valid.From, To: valid.To, Filters: map[string]string{"nope": "x"}},
		"empty range":       {From: valid.To, To: valid.From},
	}
	for name, query := range cases {
		if _, errAggregate := store.Aggregate(context.Background(), query); !errors.Is(errAggregate, ErrInvalidQuery) {
			t.Errorf("%s: error = %v, want ErrInvalidQuery", name, errAggregate)
		}
	}
	if _, errSpeed := store.Speed(context.Background(), valid, "day"); !errors.Is(errSpeed, ErrInvalidQuery) {
		t.Errorf("speed by day: error = %v, want ErrInvalidQuery", errSpeed)
	}
}

func TestSpeedReportsPercentilesAndThroughputOfSuccessfulRequests(t *testing.T) {
	store := openTestStore(t)
	for i := int64(1); i <= 100; i++ {
		mustInsert(t, store, Row{
			Timestamp: testDay, Model: "m", Stream: true,
			LatencyMs: i * 100, TTFTMs: i * 10, OutputTokens: 90, ReasoningTokens: 10,
		})
	}
	// Failed requests must not skew latency.
	mustInsert(t, store, Row{Timestamp: testDay, Model: "m", Failed: true, LatencyMs: 9_999_999})

	speeds, errSpeed := store.Speed(context.Background(), Query{From: testDay, To: testDay.Add(time.Hour)}, "model")
	if errSpeed != nil {
		t.Fatalf("speed: %v", errSpeed)
	}
	if len(speeds) != 1 {
		t.Fatalf("speeds = %+v, want one group", speeds)
	}
	got := speeds[0]
	if got.Requests != 100 || got.LatencyP50Ms != 5000 || got.LatencyP95Ms != 9500 || got.TTFTP50Ms != 500 || got.TTFTP95Ms != 950 {
		t.Fatalf("speed = %+v", got)
	}
	// 100 requests x 100 tokens over sum(i*90ms) = 454.5s of generation.
	if want := 10000 / 454.5; math.Abs(got.OutputTokensPerSecond-want) > 1e-6 {
		t.Fatalf("output tokens per second = %v, want %v", got.OutputTokensPerSecond, want)
	}
}

func TestSettingsOverridePricesAndPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFileName)
	store, errOpen := Open(path)
	if errOpen != nil {
		t.Fatalf("open store: %v", errOpen)
	}
	mustInsert(t, store, Row{Timestamp: testDay, Model: "mystery-model", InputTokens: 1_000_000, TotalTokens: 1_000_000})

	settings := Settings{
		ModelPrices:  map[string]Price{"Mystery-Model": {Input: 7, Output: 9}},
		AccountPlans: []AccountPlan{{Provider: "claude", Account: "me@example.com", MonthlyUSD: 200}},
	}
	if errSave := store.SaveSettings(context.Background(), settings); errSave != nil {
		t.Fatalf("save settings: %v", errSave)
	}
	if errClose := store.Close(); errClose != nil {
		t.Fatalf("close store: %v", errClose)
	}

	// Reopening must keep both rows and settings, and must not rerun migrations.
	reopened, errReopen := Open(path)
	if errReopen != nil {
		t.Fatalf("reopen store: %v", errReopen)
	}
	defer func() {
		if errClose := reopened.Close(); errClose != nil {
			t.Errorf("close reopened store: %v", errClose)
		}
	}()
	loaded, errLoad := reopened.Settings(context.Background())
	if errLoad != nil {
		t.Fatalf("load settings: %v", errLoad)
	}
	if len(loaded.AccountPlans) != 1 || loaded.AccountPlans[0].MonthlyUSD != 200 {
		t.Fatalf("account plans = %+v", loaded.AccountPlans)
	}
	aggregate, errAggregate := reopened.Aggregate(context.Background(), Query{From: testDay, To: testDay.Add(time.Hour)})
	if errAggregate != nil {
		t.Fatalf("aggregate: %v", errAggregate)
	}
	if len(aggregate.Groups) != 1 || !approxEqual(aggregate.Groups[0].CostUSD, 7) || len(aggregate.UnpricedModels) != 0 {
		t.Fatalf("aggregate = %+v, want cost 7 from the override", aggregate)
	}
}

func TestSaveSettingsRejectsInvalidValues(t *testing.T) {
	store := openTestStore(t)
	invalid := []Settings{
		{ModelPrices: map[string]Price{"m": {Input: -1}}},
		{ModelPrices: map[string]Price{" ": {Input: 1}}},
		{AccountPlans: []AccountPlan{{Account: "a", MonthlyUSD: math.NaN()}}},
		{AccountPlans: []AccountPlan{{Account: "", MonthlyUSD: 1}}},
	}
	for i, settings := range invalid {
		if errSave := store.SaveSettings(context.Background(), settings); errSave == nil {
			t.Errorf("case %d: save succeeded, want validation error", i)
		}
	}
}

func TestPriceLookupMatchesBaseModelAtSeparators(t *testing.T) {
	table, errTable := NewPriceTable(map[string]Price{"gpt-5": {Input: 1}, "gpt-5.4": {Input: 2}})
	if errTable != nil {
		t.Fatalf("new price table: %v", errTable)
	}
	cases := map[string]float64{
		"gpt-5":              1,
		"GPT-5.4":            2,
		"gpt-5.4-2026-01-01": 2,
		"gpt-5-high":         1,
		"gpt-5[1m]":          1,
	}
	for model, want := range cases {
		price, ok := table.Lookup(model)
		if !ok || price.Input != want {
			t.Errorf("Lookup(%q) = %v, %v; want input %v", model, price.Input, ok, want)
		}
	}
	// A longer version number is a different model, not a variant.
	if price, ok := table.Lookup("gpt-5.45"); ok {
		t.Errorf("Lookup(gpt-5.45) = %+v, want no match", price)
	}
	if _, ok := table.Lookup("unknown-model"); ok {
		t.Error("Lookup(unknown-model) matched, want no match")
	}
}

func TestBuiltInPricesCoverCurrentModels(t *testing.T) {
	table, errTable := NewPriceTable(nil)
	if errTable != nil {
		t.Fatalf("new price table: %v", errTable)
	}
	for _, model := range []string{"claude-fable-5-2", "claude-opus-4-5-20251101", "gpt-6-sol", "gpt-6.2-sol", "gpt-6.2-astra", "gemini-4-argon-high", "gemini-4-hydrogen-xhigh", "gemini-4-neon-xhigh"} {
		if price, ok := table.Lookup(model); !ok || price.Input <= 0 || price.Output <= 0 {
			t.Errorf("Lookup(%q) = %+v, %v; want a positive price", model, price, ok)
		}
	}
}

func TestRecorderPersistsUsageRecordWithoutSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFileName)
	if errConfigure := Configure(true, path); errConfigure != nil {
		t.Fatalf("configure: %v", errConfigure)
	}
	t.Cleanup(func() {
		if errShutdown := Shutdown(); errShutdown != nil {
			t.Errorf("shutdown: %v", errShutdown)
		}
	})

	ctx := internallogging.WithClientRequestMetadata(context.Background(), internallogging.ClientRequestMetadata{
		ResolvedClientIP: "100.64.0.1",
		UserAgent:        "claude-cli/9.9.9",
		WorkingDir:       `C:\Users\me\.t3\worktrees\CLIProxyAPI\t3-abc`,
	})
	ctx = internallogging.WithEndpoint(ctx, "POST /v1/messages")
	const sessionID = "0196f4a2-7c1e-7c3a-9d5e-3f2a1b4c5d6e"
	const parentSessionID = "0196f4a2-7c1e-7c3a-9d5e-000000000001"
	record := coreusage.Record{
		RequestID:       "exec-1",
		Provider:        "claude",
		Model:           "claude-opus-5-5",
		AuthType:        "apikey",
		Source:          "sk-ant-upstream-secret-key",
		APIKey:          "sk-client-secret-key",
		SessionID:       sessionID,
		ParentSessionID: parentSessionID,
		RequestedAt:     testDay,
		Latency:         1500 * time.Millisecond,
		TTFT:            300 * time.Millisecond,
		Detail: coreusage.Detail{
			TokenBreakdown:        coreusage.NewIndependentTokenBreakdown(100, 200, 300, 40, 10, 650),
			CacheCreation1hTokens: coreusage.Int64Ptr(300),
			StopReason:            coreusage.StopReasonEndTurn,
		},
	}
	recorder{}.HandleUsage(ctx, record)

	// A later request of the same session that declares no working directory
	// inherits the session's project.
	record.RequestID = "exec-2"
	recorder{}.HandleUsage(context.Background(), record)

	store := Current()
	aggregate, errAggregate := store.Aggregate(context.Background(), Query{
		From: testDay, To: testDay.Add(time.Hour),
		GroupBy: []string{"project", "account", "client_key", "session_id", "parent_session_id", "stop_reason"},
	})
	if errAggregate != nil {
		t.Fatalf("aggregate: %v", errAggregate)
	}
	if len(aggregate.Groups) != 1 {
		t.Fatalf("groups = %+v, want both requests in one group", aggregate.Groups)
	}
	group := aggregate.Groups[0]
	want := map[string]string{
		"project":           "CLIProxyAPI",
		"account":           "sk-a...-key",
		"client_key":        "sk-c...-key",
		"session_id":        sessionID,
		"parent_session_id": parentSessionID,
		"stop_reason":       "end_turn",
	}
	for name, value := range want {
		if group.Dimensions[name] != value {
			t.Errorf("%s = %q, want %q", name, group.Dimensions[name], value)
		}
	}
	if group.Requests != 2 || group.Turns != 2 || group.Sessions != 1 || group.InputTokens != 200 || group.CacheReadTokens != 400 ||
		group.CacheWriteTokens != 600 || group.CacheWrite1hTokens != 600 || group.OutputTokens != 80 || group.ReasoningTokens != 20 || group.TotalTokens != 1300 {
		t.Fatalf("metrics = %+v", group.Metrics)
	}

	byEndpoint, errEndpoint := store.Aggregate(context.Background(), Query{
		From: testDay, To: testDay.Add(time.Hour), GroupBy: []string{"endpoint", "client_ip", "user_agent", "status_code"},
		Filters: map[string]string{"endpoint": "POST /v1/messages"},
	})
	if errEndpoint != nil {
		t.Fatalf("aggregate by endpoint: %v", errEndpoint)
	}
	if len(byEndpoint.Groups) != 1 {
		t.Fatalf("endpoint groups = %+v, want one", byEndpoint.Groups)
	}
	dims := byEndpoint.Groups[0].Dimensions
	if dims["client_ip"] != "100.64.0.1" || dims["user_agent"] != "claude-cli/9.9.9" || dims["status_code"] != "200" {
		t.Fatalf("dimensions = %+v", dims)
	}
}

func TestRecorderPrefersTheServiceTierTheUpstreamReported(t *testing.T) {
	requested := coreusage.Record{ServiceTier: "auto"}
	if got := rowFromRecord(context.Background(), requested).ServiceTier; got != "auto" {
		t.Errorf("service tier = %q, want the requested tier when nothing else is known", got)
	}
	served := coreusage.Record{ServiceTier: "auto", EffectiveServiceTier: "priority", ResponseServiceTier: "standard"}
	if got := rowFromRecord(context.Background(), served).ServiceTier; got != "standard" {
		t.Errorf("service tier = %q, want the tier the upstream reported", got)
	}
}

func TestRecorderKeepsOAuthAccountIdentityAndFailureStatus(t *testing.T) {
	row := rowFromRecord(context.Background(), coreusage.Record{
		AuthType: "oauth",
		Source:   "me@example.com",
		Failed:   true,
		Fail:     coreusage.Failure{StatusCode: 429, Code: "rate_limited"},
	})
	if row.Account != "me@example.com" {
		t.Errorf("account = %q, want the email kept", row.Account)
	}
	if !row.Failed || row.StatusCode != 429 || row.FailCode != "rate_limited" {
		t.Errorf("failure = %v/%d/%q", row.Failed, row.StatusCode, row.FailCode)
	}
}

func TestConfigureIsIdempotentAndDisables(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFileName)
	if errConfigure := Configure(true, path); errConfigure != nil {
		t.Fatalf("configure: %v", errConfigure)
	}
	first := Current()
	if errConfigure := Configure(true, path); errConfigure != nil {
		t.Fatalf("reconfigure: %v", errConfigure)
	}
	if Current() != first {
		t.Fatal("reconfiguring with the same path reopened the store")
	}
	if errShutdown := Shutdown(); errShutdown != nil {
		t.Fatalf("shutdown: %v", errShutdown)
	}
	if Enabled() {
		t.Fatal("store still enabled after shutdown")
	}
	// Recording while disabled must be a no-op, not a panic.
	recorder{}.HandleUsage(context.Background(), coreusage.Record{Model: "m"})
}

func TestConfigureKeepsTheWorkingStoreWhenTheReplacementCannotOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFileName)
	if errConfigure := Configure(true, path); errConfigure != nil {
		t.Fatalf("configure: %v", errConfigure)
	}
	t.Cleanup(func() {
		if errShutdown := Shutdown(); errShutdown != nil {
			t.Errorf("shutdown: %v", errShutdown)
		}
	})
	working := Current()

	// A regular file where the database directory should be cannot be opened.
	blocker := filepath.Join(dir, "blocker")
	if errWrite := os.WriteFile(blocker, nil, 0o600); errWrite != nil {
		t.Fatalf("write blocker: %v", errWrite)
	}
	if errConfigure := Configure(true, filepath.Join(blocker, DefaultFileName)); errConfigure == nil {
		t.Fatal("configure with an unusable path succeeded")
	}
	if Current() != working {
		t.Fatal("the working store was retired although its replacement failed to open")
	}
	recorder{}.HandleUsage(context.Background(), coreusage.Record{Model: "m", RequestedAt: testDay})
	aggregate, errAggregate := working.Aggregate(context.Background(), Query{From: testDay, To: testDay.Add(time.Hour)})
	if errAggregate != nil || len(aggregate.Groups) != 1 {
		t.Fatalf("aggregate = %+v, %v; want the record in the working store", aggregate, errAggregate)
	}
}

// An OAuth credential without an account identity reports the client key as
// its source, which must not be stored in full.
func TestRecorderMasksAClientKeyReportedAsTheOAuthSource(t *testing.T) {
	row := rowFromRecord(context.Background(), coreusage.Record{
		AuthType: "oauth",
		Source:   "sk-client-secret-key",
		APIKey:   "sk-client-secret-key",
	})
	if row.Account != "sk-c...-key" || row.ClientKey != "sk-c...-key" {
		t.Fatalf("account = %q, client key = %q; want both masked", row.Account, row.ClientKey)
	}
}

func TestRecorderKeepsTheSuccessfulResponseStatus(t *testing.T) {
	ctx := internallogging.WithResponseStatusHolder(context.Background())
	internallogging.SetResponseStatus(ctx, 201)
	if got := rowFromRecord(ctx, coreusage.Record{}).StatusCode; got != 201 {
		t.Errorf("status code = %d, want 201", got)
	}
	if got := rowFromRecord(context.Background(), coreusage.Record{}).StatusCode; got != 200 {
		t.Errorf("status code = %d, want 200 when no status was captured", got)
	}
}

func TestWorkingDirForSessionIsScopedToTheClientKey(t *testing.T) {
	store := openTestStore(t)
	mustInsert(t, store, Row{Timestamp: testDay, SessionID: "shared", ClientKey: "sk-a...aaaa", WorkingDir: "/home/a/repo"})

	own, errOwn := store.WorkingDirForSession(context.Background(), "shared", "sk-a...aaaa")
	if errOwn != nil || own != "/home/a/repo" {
		t.Fatalf("own lookup = %q, %v", own, errOwn)
	}
	other, errOther := store.WorkingDirForSession(context.Background(), "shared", "sk-b...bbbb")
	if errOther != nil || other != "" {
		t.Fatalf("another client's lookup = %q, %v; want nothing", other, errOther)
	}
}

func TestSpeedKeepsTheBusiestGroupsWithinTheLimit(t *testing.T) {
	store := openTestStore(t)
	for model, requests := range map[string]int{"busy": 3, "busier": 5, "quiet": 1} {
		for range requests {
			mustInsert(t, store, Row{Timestamp: testDay, Model: model, LatencyMs: 100})
		}
	}
	speeds, errSpeed := store.Speed(context.Background(), Query{From: testDay, To: testDay.Add(time.Hour), Limit: 2}, "model")
	if errSpeed != nil {
		t.Fatalf("speed: %v", errSpeed)
	}
	if len(speeds) != 2 || speeds[0].Value != "busier" || speeds[1].Value != "busy" {
		t.Fatalf("speeds = %+v, want busier and busy", speeds)
	}
}

func TestSaveSettingsRejectsTwoSpellingsOfOneModel(t *testing.T) {
	store := openTestStore(t)
	settings := Settings{ModelPrices: map[string]Price{"GPT-5": {Input: 1}, "gpt-5": {Input: 2}}}
	if errSave := store.SaveSettings(context.Background(), settings); errSave == nil {
		t.Fatal("save succeeded, want a duplicate model error")
	}
}
