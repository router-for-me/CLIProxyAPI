package tui

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRequestEventLineStyles(t *testing.T) {
	model := logsTabModel{}
	cacheMiss := `[info] request_event operation=inference cache_outcome=miss cache_miss=true`
	if got, want := model.styleLine(cacheMiss), logErrorStyle.Render(cacheMiss); got != want {
		t.Fatalf("cache miss style = %q, want red style %q", got, want)
	}

	compaction := `[info] request_event operation=compaction cache_outcome=hit cache_miss=false`
	if got, want := model.styleLine(compaction), logCompactionStyle.Render(compaction); got != want {
		t.Fatalf("compaction style = %q, want magenta style %q", got, want)
	}
}

func TestRenderObservabilitySummaryIncludesFixedCounters(t *testing.T) {
	app := App{
		width: 180,
		observability: observabilitySnapshot{
			Requests:         12,
			InputTokens:      345,
			OutputTokens:     67,
			CacheMisses:      3,
			Compactions:      2,
			EstimatedCostUSD: 1.25,
			PricedRequests:   12,
		},
	}
	rendered := app.renderObservabilitySummary()
	for _, want := range []string{"Requests 12", "In 345", "Out 67", "Cache misses 3", "Compactions 2", "Est. cost $1.2500"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("summary %q missing %q", rendered, want)
		}
	}
}

func TestRenderObservabilitySummaryUsesCompactLabelsWhenNarrow(t *testing.T) {
	app := App{
		width: 80,
		observability: observabilitySnapshot{
			Requests:         12,
			InputTokens:      345,
			OutputTokens:     67,
			CacheMisses:      3,
			Compactions:      2,
			EstimatedCostUSD: 1.25,
			PricedRequests:   12,
		},
	}
	rendered := app.renderObservabilitySummary()
	for _, want := range []string{"Req 12", "In 345", "Out 67", "Miss 3", "Cmp 2", "Est $1.2500"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("compact summary %q missing %q", rendered, want)
		}
	}
}

func TestFormatObservabilityCostShowsCompleteness(t *testing.T) {
	tests := []struct {
		name     string
		snapshot observabilitySnapshot
		want     string
	}{
		{name: "none priced", snapshot: observabilitySnapshot{UnpricedRequests: 2}, want: "—"},
		{name: "all priced", snapshot: observabilitySnapshot{PricedRequests: 2, EstimatedCostUSD: 1.25}, want: "$1.2500"},
		{name: "partially priced", snapshot: observabilitySnapshot{PricedRequests: 1, UnpricedRequests: 1, EstimatedCostUSD: 1.25}, want: "partial $1.2500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatObservabilityCost(tt.snapshot); got != tt.want {
				t.Fatalf("cost = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestObservabilityOnlyLogsAppendEachRequestOnce(t *testing.T) {
	model := newLogsTabModel(nil, nil)
	model.SetObservabilityOnly(true)
	model.SetSize(140, 20)
	events := []observabilityEvent{{
		Sequence:               1,
		Provider:               "codex",
		Model:                  "gpt-5.6-sol",
		Operation:              "compaction",
		InputTokens:            240000,
		OutputTokens:           1024,
		CacheOutcome:           "miss",
		CacheMiss:              true,
		EstimatedCostUSD:       1.23,
		EstimatedCostAvailable: true,
	}}
	model.AddObservabilityEvents(events)
	model.AddObservabilityEvents(events)
	if len(model.lines) != 1 {
		t.Fatalf("observability lines = %d, want 1", len(model.lines))
	}
	for _, want := range []string{"operation=compaction", `provider="codex"`, "cache_outcome=miss", "cache_miss=true"} {
		if !strings.Contains(model.lines[0], want) {
			t.Fatalf("request event %q missing %q", model.lines[0], want)
		}
	}
	if got, want := model.styleLine(model.lines[0]), logErrorStyle.Render(model.lines[0]); got != want {
		t.Fatalf("cache-miss compaction style = %q, want red %q", got, want)
	}
}

func TestObservabilityLogResetsSequenceOnRestartAndShowsGap(t *testing.T) {
	model := newLogsTabModel(nil, nil)
	model.SetObservabilityOnly(true)
	model.AddObservabilityEvents([]observabilityEvent{{Sequence: 10, Operation: "inference"}})
	model.AddObservabilityGap(11, 14)
	model.AddObservabilityGap(11, 14)
	model.ResetObservabilityCursor("new-boot")
	model.AddObservabilityEvents([]observabilityEvent{{Sequence: 1, Operation: "inference"}})

	if len(model.lines) != 4 {
		t.Fatalf("observability lines = %d, want event + gap + restart + new event", len(model.lines))
	}
	for _, want := range []string{"request_event_gap missing_sequences=11-14", `request_event_stream_restart boot_id="new-boot"`, "operation=inference"} {
		if !strings.Contains(strings.Join(model.lines, "\n"), want) {
			t.Fatalf("observability log lines %q missing %q", model.lines, want)
		}
	}
	if got, want := model.styleLine(model.lines[1]), logWarnStyle.Render(model.lines[1]); got != want {
		t.Fatalf("gap style = %q, want warning %q", got, want)
	}
}

func TestObservabilityCursorResetsWhileFileLogsAreActive(t *testing.T) {
	model := newLogsTabModel(nil, &LogHook{})
	model.lines = []string{"existing file log"}
	model.lastObservabilitySequence = 42
	model.lastObservabilityGapTo = 41

	model.ResetObservabilityCursor("new-boot")

	if model.lastObservabilitySequence != 0 || model.lastObservabilityGapTo != 0 {
		t.Fatalf("cursor/gap = %d/%d, want 0/0", model.lastObservabilitySequence, model.lastObservabilityGapTo)
	}
	if len(model.lines) != 1 || model.lines[0] != "existing file log" {
		t.Fatalf("file log lines = %q, want unchanged without restart marker", model.lines)
	}
}

func TestObservabilityPollErrorPreservesSnapshotAndShowsStaleIndicator(t *testing.T) {
	app := App{
		width:                    180,
		observability:            observabilitySnapshot{Requests: 7, PricedRequests: 1, EstimatedCostUSD: 0.5},
		observabilityLastSuccess: time.Now().Add(-time.Second),
		logs:                     newLogsTabModel(nil, nil),
	}
	updatedModel, _ := app.Update(observabilityPollMsg{err: errors.New("management API offline")})
	updated := updatedModel.(App)
	if updated.observability.Requests != 7 || updated.observability.EstimatedCostUSD != 0.5 {
		t.Fatalf("last snapshot was replaced on error: %+v", updated.observability)
	}
	rendered := updated.renderObservabilitySummary()
	for _, want := range []string{"Requests 7", "$0.5000", "stale", "management API offline"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("stale summary %q missing %q", rendered, want)
		}
	}
}

func TestObservabilityPollResetsCursorAcrossProcessRestart(t *testing.T) {
	logs := newLogsTabModel(nil, nil)
	logs.SetObservabilityOnly(true)
	app := App{
		observabilityBootID: "old-boot",
		observabilityCursor: 99,
		logs:                logs,
	}
	updatedModel, _ := app.Update(observabilityPollMsg{snapshot: observabilitySnapshot{
		BootID:      "new-boot",
		CursorReset: true,
		NextAfter:   1,
		RecentEvents: []observabilityEvent{{
			Sequence:  1,
			Operation: "inference",
		}},
	}})
	updated := updatedModel.(App)
	if updated.observabilityBootID != "new-boot" || updated.observabilityCursor != 1 {
		t.Fatalf("boot/cursor = %q/%d, want new-boot/1", updated.observabilityBootID, updated.observabilityCursor)
	}
	if len(updated.logs.lines) != 2 || !strings.Contains(updated.logs.lines[0], "request_event_stream_restart") || !strings.Contains(updated.logs.lines[1], "operation=inference") {
		t.Fatalf("restart log lines = %q, want restart marker then new event", updated.logs.lines)
	}
}
