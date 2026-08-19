package usage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestRequestStatisticsRecordIncludesLatency(t *testing.T) {
	prevStatsEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() {
		SetStatisticsEnabled(prevStatsEnabled)
	})

	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Latency:     1500 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})

	snapshot := stats.Snapshot()
	apiSnapshot := onlyAPISnapshot(t, snapshot)
	if strings.Contains(onlyAPIName(t, snapshot), "test-key") {
		t.Fatalf("api bucket leaked raw key: %q", onlyAPIName(t, snapshot))
	}
	details := apiSnapshot.Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if details[0].LatencyMs != 1500 {
		t.Fatalf("latency_ms = %d, want 1500", details[0].LatencyMs)
	}
}

func TestRequestStatisticsSanitizesDetailIdentifiers(t *testing.T) {
	prevStatsEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() {
		SetStatisticsEnabled(prevStatsEnabled)
	})

	const rawAPIKey = "sk-test-client-secret"
	const rawSource = "person@example.com"
	const rawAuthIndex = "raw-auth-index-secret"

	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      rawAPIKey,
		Model:       "gpt-5.4",
		Source:      rawSource,
		AuthIndex:   rawAuthIndex,
		RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
	})

	snapshot := stats.Snapshot()
	apiName := onlyAPIName(t, snapshot)
	if strings.Contains(apiName, rawAPIKey) {
		t.Fatalf("api bucket leaked raw key: %q", apiName)
	}
	details := onlyAPISnapshot(t, snapshot).Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	assertNoRawUsageIdentifier(t, details[0].Source, rawSource)
	assertNoRawUsageIdentifier(t, details[0].AuthIndex, rawAuthIndex)
}

func TestRequestStatisticsRestoreSanitizesImportedDetails(t *testing.T) {
	prevStatsEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() {
		SetStatisticsEnabled(prevStatsEnabled)
	})

	const rawSource = "source:imported-person@example.com"
	const rawAuthIndex = "auth:imported-auth-secret"
	const rawAPIKey = "api-key:sk-imported-api-secret"

	stats := NewRequestStatistics()
	stats.RestoreSnapshot(StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			rawAPIKey: {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
							Source:    rawSource,
							AuthIndex: rawAuthIndex,
						}},
					},
				},
			},
		},
	})

	snapshot := stats.Snapshot()
	assertNoRawUsageIdentifier(t, onlyAPIName(t, snapshot), rawAPIKey)
	details := onlyAPISnapshot(t, snapshot).Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	assertNoRawUsageIdentifier(t, details[0].Source, rawSource)
	assertNoRawUsageIdentifier(t, details[0].AuthIndex, rawAuthIndex)
}

func TestRequestStatisticsUsesStableLoggingContext(t *testing.T) {
	prevStatsEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() {
		SetStatisticsEnabled(prevStatsEnabled)
	})

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "http://example.com/v1/chat/completions", nil)
	ginCtx.Status(http.StatusOK)

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	ctx = internallogging.WithEndpoint(ctx, "POST /v1/chat/completions")
	ctx = internallogging.WithResponseStatusHolder(ctx)
	internallogging.SetResponseStatus(ctx, http.StatusInternalServerError)

	ginCtx.Request = httptest.NewRequest(http.MethodGet, "http://example.com/v1/responses", nil)
	ginCtx.Status(http.StatusOK)

	stats := NewRequestStatistics()
	stats.Record(ctx, coreusage.Record{
		Provider:    "openai",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
	})

	snapshot := stats.Snapshot()
	apiSnapshot, ok := snapshot.APIs["POST /v1/chat/completions"]
	if !ok {
		t.Fatalf("snapshot APIs = %#v, want stable logging endpoint", snapshot.APIs)
	}
	if snapshot.FailureCount != 1 || apiSnapshot.FailureCount != 1 {
		t.Fatalf("failure counts = root:%d api:%d, want 1/1", snapshot.FailureCount, apiSnapshot.FailureCount)
	}
	modelSnapshot := apiSnapshot.Models["gpt-5.4"]
	if modelSnapshot.FailureCount != 1 {
		t.Fatalf("model failure count = %d, want 1", modelSnapshot.FailureCount)
	}
}

func TestRequestStatisticsCapsRetainedDetails(t *testing.T) {
	prevStatsEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() {
		SetStatisticsEnabled(prevStatsEnabled)
	})

	stats := NewRequestStatistics()
	start := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < maxRequestDetailsPerModel+5; i++ {
		stats.Record(context.Background(), coreusage.Record{
			APIKey:      "test-key",
			Model:       "gpt-5.4",
			RequestedAt: start.Add(time.Duration(i) * time.Second),
			Detail: coreusage.Detail{
				InputTokens:  1,
				OutputTokens: 1,
				TotalTokens:  2,
			},
		})
	}

	snapshot := stats.Snapshot()
	model := onlyAPISnapshot(t, snapshot).Models["gpt-5.4"]
	if model.TotalRequests != int64(maxRequestDetailsPerModel+5) {
		t.Fatalf("total requests = %d, want %d", model.TotalRequests, maxRequestDetailsPerModel+5)
	}
	if model.TotalTokens != int64((maxRequestDetailsPerModel+5)*2) {
		t.Fatalf("total tokens = %d, want %d", model.TotalTokens, (maxRequestDetailsPerModel+5)*2)
	}
	if len(model.Details) != maxRequestDetailsPerModel {
		t.Fatalf("details len = %d, want %d", len(model.Details), maxRequestDetailsPerModel)
	}
	if got, want := model.Details[0].Timestamp, start.Add(5*time.Second); !got.Equal(want) {
		t.Fatalf("first retained timestamp = %v, want %v", got, want)
	}
}

func TestRequestStatisticsCapsAPIAndModelCardinality(t *testing.T) {
	prevStatsEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() {
		SetStatisticsEnabled(prevStatsEnabled)
	})

	apiStats := NewRequestStatistics()
	for i := 0; i < maxTrackedAPIs+5; i++ {
		apiStats.Record(context.Background(), coreusage.Record{
			APIKey:      fmt.Sprintf("test-api-key-%d", i),
			Model:       "gpt-5.4",
			RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		})
	}
	apiSnapshot := apiStats.Snapshot()
	if len(apiSnapshot.APIs) > maxTrackedAPIs {
		t.Fatalf("api buckets = %d, want <= %d", len(apiSnapshot.APIs), maxTrackedAPIs)
	}
	if _, ok := apiSnapshot.APIs[overflowUsageBucket]; !ok {
		t.Fatalf("snapshot APIs missing overflow bucket")
	}

	modelStats := NewRequestStatistics()
	for i := 0; i < maxModelsPerAPI+5; i++ {
		modelStats.Record(context.Background(), coreusage.Record{
			APIKey:      "test-key",
			Model:       fmt.Sprintf("model-%d", i),
			RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		})
	}
	modelSnapshot := onlyAPISnapshot(t, modelStats.Snapshot())
	if len(modelSnapshot.Models) > maxModelsPerAPI {
		t.Fatalf("model buckets = %d, want <= %d", len(modelSnapshot.Models), maxModelsPerAPI)
	}
	if _, ok := modelSnapshot.Models[overflowUsageBucket]; !ok {
		t.Fatalf("models missing overflow bucket")
	}
}

func TestRequestStatisticsRestoreSnapshotPreservesAggregateTotalsWithCappedDetails(t *testing.T) {
	prevStatsEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() {
		SetStatisticsEnabled(prevStatsEnabled)
	})

	source := NewRequestStatistics()
	start := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < maxRequestDetailsPerModel+5; i++ {
		source.Record(context.Background(), coreusage.Record{
			Provider:    "openai",
			Model:       "gpt-5.4",
			RequestedAt: start.Add(time.Duration(i) * time.Second),
			Failed:      i%10 == 0,
			Detail: coreusage.Detail{
				InputTokens:  1,
				OutputTokens: 1,
				TotalTokens:  2,
			},
		})
	}

	exported := source.Snapshot()
	if got := len(exported.APIs["openai"].Models["gpt-5.4"].Details); got != maxRequestDetailsPerModel {
		t.Fatalf("exported details len = %d, want cap %d", got, maxRequestDetailsPerModel)
	}

	target := NewRequestStatistics()
	result := target.RestoreSnapshot(exported)
	if result.Added != exported.TotalRequests || result.Skipped != 0 {
		t.Fatalf("restore result = %+v, want added=%d skipped=0", result, exported.TotalRequests)
	}

	restored := target.Snapshot()
	requireSnapshotTotals(t, restored, exported)

	for i := 0; i < maxRequestDetailsPerModel+5; i++ {
		target.Record(context.Background(), coreusage.Record{
			Provider:    "local",
			Model:       fmt.Sprintf("local-model-%d", i),
			RequestedAt: start.Add(time.Duration(i) * time.Second),
			Detail: coreusage.Detail{
				InputTokens:  3,
				OutputTokens: 4,
				TotalTokens:  7,
			},
		})
	}

	result = target.RestoreSnapshot(exported)
	if result.Added != exported.TotalRequests || result.Skipped != 0 {
		t.Fatalf("second restore result = %+v, want added=%d skipped=0", result, exported.TotalRequests)
	}
	restoredAgain := target.Snapshot()
	requireSnapshotTotals(t, restoredAgain, exported)
	if _, ok := restoredAgain.APIs["local"]; ok {
		t.Fatalf("restore left unrelated local API in snapshot")
	}
}

func onlyAPIName(t *testing.T, snapshot StatisticsSnapshot) string {
	t.Helper()
	if len(snapshot.APIs) != 1 {
		t.Fatalf("api count = %d, want 1", len(snapshot.APIs))
	}
	for apiName := range snapshot.APIs {
		return apiName
	}
	return ""
}

func onlyAPISnapshot(t *testing.T, snapshot StatisticsSnapshot) APISnapshot {
	t.Helper()
	return snapshot.APIs[onlyAPIName(t, snapshot)]
}

func requireSnapshotTotals(t *testing.T, got, want StatisticsSnapshot) {
	t.Helper()

	if got.TotalRequests != want.TotalRequests {
		t.Fatalf("total requests = %d, want %d", got.TotalRequests, want.TotalRequests)
	}
	if got.TotalTokens != want.TotalTokens {
		t.Fatalf("total tokens = %d, want %d", got.TotalTokens, want.TotalTokens)
	}
	if got.FailureCount != want.FailureCount {
		t.Fatalf("failure count = %d, want %d", got.FailureCount, want.FailureCount)
	}
	gotAPI := got.APIs["openai"]
	wantAPI := want.APIs["openai"]
	if gotAPI.TotalRequests != wantAPI.TotalRequests || gotAPI.TotalTokens != wantAPI.TotalTokens || gotAPI.FailureCount != wantAPI.FailureCount {
		t.Fatalf("api totals = requests:%d tokens:%d failures:%d, want requests:%d tokens:%d failures:%d",
			gotAPI.TotalRequests,
			gotAPI.TotalTokens,
			gotAPI.FailureCount,
			wantAPI.TotalRequests,
			wantAPI.TotalTokens,
			wantAPI.FailureCount,
		)
	}
	gotModel := gotAPI.Models["gpt-5.4"]
	wantModel := wantAPI.Models["gpt-5.4"]
	if gotModel.TotalRequests != wantModel.TotalRequests || gotModel.TotalTokens != wantModel.TotalTokens || gotModel.FailureCount != wantModel.FailureCount {
		t.Fatalf("model totals = requests:%d tokens:%d failures:%d, want requests:%d tokens:%d failures:%d",
			gotModel.TotalRequests,
			gotModel.TotalTokens,
			gotModel.FailureCount,
			wantModel.TotalRequests,
			wantModel.TotalTokens,
			wantModel.FailureCount,
		)
	}
	if got.RequestsByHour["12"] != want.RequestsByHour["12"] {
		t.Fatalf("requests by hour[12] = %d, want %d", got.RequestsByHour["12"], want.RequestsByHour["12"])
	}
	if got.TokensByHour["12"] != want.TokensByHour["12"] {
		t.Fatalf("tokens by hour[12] = %d, want %d", got.TokensByHour["12"], want.TokensByHour["12"])
	}
}

func assertNoRawUsageIdentifier(t *testing.T, got, raw string) {
	t.Helper()
	if got == "" {
		t.Fatalf("sanitized identifier is empty")
	}
	if got == raw || strings.Contains(got, raw) {
		t.Fatalf("identifier leaked raw value: got %q raw %q", got, raw)
	}
	if !strings.HasPrefix(got, "source:") && !strings.HasPrefix(got, "auth:") && !strings.HasPrefix(got, "api-key:") {
		t.Fatalf("identifier = %q, want stable redacted prefix", got)
	}
}
